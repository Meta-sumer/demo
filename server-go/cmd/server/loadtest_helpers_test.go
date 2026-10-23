//go:build loadtest

package main

// Shared by the load test (loadtest_test.go) and the multi-process failover
// test (failover_test.go). Both are opt-in: they need the build tag and
// LOADTEST=1, so `go test ./...` and CI never run them. Results go to
// docs/audit/operations/load-and-failover.md.
//
//   MONGO_URI=... REDIS_URL=... LOADTEST=1 go test -tags loadtest -run TestLoad -v ./cmd/server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
)

const (
	ltStake  = int64(100_00)
	ltSecret = "loadtest-access-secret-0123456789abcdefghij"
	ltRefSec = "loadtest-refresh-secret-0123456789abcdefghi"
)

func requireLoadTest(t *testing.T) {
	t.Helper()
	if os.Getenv("LOADTEST") == "" {
		t.Skip("set LOADTEST=1 (and the loadtest build tag) to run the load and failover tests")
	}
	if os.Getenv("MONGO_URI") == "" || os.Getenv("REDIS_URL") == "" {
		t.Skip("MONGO_URI and REDIS_URL must point at the isolated test stores")
	}
}

// loadConfig builds config.E from the environment with test secrets.
func loadConfig(t *testing.T, extra map[string]string) {
	t.Helper()
	env := map[string]string{
		"NODE_ENV": "test", "LOG_LEVEL": "error", "JWT_SECRET": ltSecret, "JWT_REFRESH_SECRET": ltRefSec,
		"WORKERS_ENABLED": "false", "MARKET_DATA_PROVIDER": "simulated", "ALERT_EMAIL_ENABLED": "false",
	}
	for k, v := range extra {
		env[k] = v
	}
	for k, v := range env {
		t.Setenv(k, v)
	}
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
}

// setRisk activates a new global risk-config version with fields changed.
func setRisk(t *testing.T, ctx context.Context, fields bson.M) *models.RiskConfig {
	t.Helper()
	if _, err := riskconfig.GetRiskConfig(ctx); err != nil {
		t.Fatal(err)
	}
	coll := mongox.C(models.CollRiskConfigs)
	var current bson.M
	if err := coll.FindOne(ctx, bson.M{"scope": models.RiskScopeGlobal, "active": true}).Decode(&current); err != nil {
		t.Fatal(err)
	}
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": current["_id"]}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		t.Fatal(err)
	}
	delete(current, "_id")
	v, _ := current["version"].(int64)
	current["version"], current["active"] = v+1, true
	for k, val := range fields {
		current[k] = val
	}
	if _, err := coll.InsertOne(ctx, current); err != nil {
		t.Fatal(err)
	}
	riskconfig.InvalidateRiskConfigCache()
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// tradingRisk: FIXED pricing (the payout row as is; the dynamic engine and
// its model are another agent's work and out of scope here), relaxed feed
// thresholds for a 50 ms feed on a busy test machine, the concentration rule
// off, and the per-user exposure ceiling under test.
func tradingRisk(maxUser int64) bson.M {
	return bson.M{
		"pricingMode": models.PricingModeFixed, "feedHealthyMaxMs": int64(1500), "feedDegradedMaxMs": int64(3000),
		"feedStaleMaxMs": int64(6000), "feedTickMaxAgeMs": int64(10_000), "maxUserExposure": maxUser,
		"maxUserAssetConcentrationPercent": float64(100), "maxOpenTradesPerUser": int64(200), "slippageTolerancePercent": float64(0),
	}
}

// createAsset makes BTC/USD (the symbol the default pricing rules know) with
// the given expiry and exposure ceilings, and its payout row.
func createAsset(t *testing.T, ctx context.Context, expiry, gross, net int64) models.AssetView {
	t.Helper()
	minStake, maxStake := int64(100), int64(1_000_000_00)
	a, err := assets.CreateAsset(ctx, assets.CreateAssetInput{
		Symbol: "BTC/USD", Name: "Bitcoin (load test)", Category: models.AssetCategoryCrypto, ProviderSymbol: "btcusdt",
		MinStake: &minStake, MaxStake: &maxStake, SupportedExpiries: []int64{expiry}, ExposureLimit: &gross, NetExposureLimit: &net,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := payouts.UpsertPayout(ctx, payouts.UpsertPayoutInput{AssetID: a.ID, ExpirySeconds: expiry, PayoutPercent: 85}, ""); err != nil {
		t.Fatal(err)
	}
	payouts.InvalidatePayoutCache()
	assets.InvalidateAssetCache()
	return a
}

type ltUser struct {
	id      bson.ObjectID
	hex     string
	token   string
	balance int64
}

// createUsers inserts active customers with a funded LIVE wallet and mints
// their access tokens (no login round trip: the login limit is per IP).
func createUsers(t *testing.T, ctx context.Context, n int, balance func(i int) int64) []ltUser {
	t.Helper()
	out := make([]ltUser, n)
	docs := make([]any, n)
	now := mongox.Now()
	for i := range out {
		id := bson.NewObjectID()
		out[i] = ltUser{id: id, hex: id.Hex(), balance: balance(i)}
		docs[i] = bson.M{"_id": id, "email": id.Hex() + "@load.test", "username": "lt" + id.Hex()[12:],
			"status": string(models.UserStatusActive), "role": "USER", "createdAt": now, "updatedAt": now}
	}
	if _, err := mongox.C(models.CollUsers).InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	for i := range out {
		u := &out[i]
		if _, err := wallets.GetOrCreateWallet(ctx, u.hex, models.AccountLive, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{UserID: u.hex, AccountType: models.AccountLive,
			Amount: u.balance, Type: models.WalletTxAdjustmentCredit, IdempotencyKey: "loadtest:fund:" + u.hex}); err != nil {
			t.Fatal(err)
		}
		tok, err := authtoken.SignAccess(u.hex, "USER", nil, uuid.NewString())
		if err != nil {
			t.Fatal(err)
		}
		u.token = tok
	}
	return out
}

// ── HTTP ────────────────────────────────────────────────────────────────

type tradeReq struct {
	AssetID        string `json:"assetId"`
	Direction      string `json:"direction"`
	Amount         int64  `json:"amount"`
	ExpirySeconds  int64  `json:"expirySeconds"`
	AccountType    string `json:"accountType"`
	IdempotencyKey string `json:"idempotencyKey"`
}

type tradeResp struct {
	Status  int
	Code    string
	TradeID string
	Latency time.Duration
	Err     error
}

func postTrade(ctx context.Context, c *http.Client, base, token string, body tradeReq) tradeResp {
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/binary/trades", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	start := time.Now()
	res, err := c.Do(req)
	out := tradeResp{Latency: time.Since(start)}
	if err != nil {
		out.Err = err
		return out
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	out.Latency = time.Since(start)
	out.Status = res.StatusCode
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(data, &env)
	if env.Error != nil {
		out.Code = env.Error.Code
	}
	if res.StatusCode < 300 {
		var tr struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(env.Data, &tr)
		out.TradeID = tr.ID
	} else if out.Code == "" {
		out.Code = strings.TrimSpace(string(data))
		if len(out.Code) > 80 {
			out.Code = out.Code[:80]
		}
	}
	return out
}

// ── books from MongoDB ──────────────────────────────────────────────────

type mongoBook struct {
	up, down, open int64
	perUser        map[string]int64
	trades         map[string]models.BinaryTrade
}

// openLiveBook is the house book as MongoDB records it: open LIVE trades.
func openLiveBook(t *testing.T, ctx context.Context, assetID bson.ObjectID) mongoBook {
	t.Helper()
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{"assetId": assetID,
		"status": bson.M{"$in": bson.A{models.TradeStatusOpen, models.TradeStatusSettling}}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.BinaryTrade
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	b := mongoBook{perUser: map[string]int64{}, trades: map[string]models.BinaryTrade{}}
	for _, r := range rows {
		if models.NormAccountType(r.AccountType) != models.AccountLive {
			continue
		}
		if r.Direction == models.DirectionUp {
			b.up += r.Stake
		} else {
			b.down += r.Stake
		}
		b.open++
		b.perUser[r.UserID.Hex()] += r.Stake
		b.trades[r.ID.Hex()] = r
	}
	return b
}

// walletOf returns a user's LIVE wallet (balance, locked).
func walletOf(t *testing.T, ctx context.Context, user bson.ObjectID) models.Wallet {
	t.Helper()
	var w models.Wallet
	if err := mongox.C(models.CollWallets).FindOne(ctx, bson.M{"userId": user, "accountType": models.AccountLive},
		options.FindOne()).Decode(&w); err != nil {
		t.Fatal(err)
	}
	return w
}

// ── stats ───────────────────────────────────────────────────────────────

type latencies struct {
	mu sync.Mutex
	d  []time.Duration
}

func (l *latencies) add(d time.Duration) { l.mu.Lock(); l.d = append(l.d, d); l.mu.Unlock() }

func (l *latencies) pct(p float64) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.d) == 0 {
		return 0
	}
	s := append([]time.Duration(nil), l.d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	i := int(math.Ceil(p/100*float64(len(s)))) - 1
	return s[max(0, min(i, len(s)-1))]
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000) }

// writeResult appends a JSON result line to $LOADTEST_OUT when set.
func writeResult(t *testing.T, name string, v any) {
	path := os.Getenv("LOADTEST_OUT")
	if path == "" {
		return
	}
	raw, _ := json.Marshal(map[string]any{"test": name, "at": time.Now().UTC().Format(time.RFC3339), "result": v})
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Logf("cannot write %s: %v", path, err)
		return
	}
	defer f.Close()
	_, _ = f.Write(append(raw, '\n'))
}
