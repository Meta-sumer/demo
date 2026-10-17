package settlement

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/notifications"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
	"binarytrading/internal/testsupport"
)

// Integration harness: a throwaway Mongo database, and Redis keys of this
// test's own (queues and the exposure set are renamed per test; per-asset and
// per-user keys use fresh ids and are deleted afterwards).

const (
	testBalance = int64(1_000_00)
	testStake   = int64(100_00)
	testPayout  = 80.0
	testProfit  = int64(80_00)
)

type harness struct {
	t      *testing.T
	ctx    context.Context
	ns     string
	keys   []string
	userID bson.ObjectID
}

func newHarness(t *testing.T) *harness { return newHarnessTx(t, false) }

// newHarnessTx with tx=true reconnects to the same throwaway database with
// multi-document transactions enabled (skips when the deployment has none).
func newHarnessTx(t *testing.T, tx bool) *harness {
	t.Helper()
	testsupport.LiveStores(t)
	if tx {
		enableTransactions(t)
	}
	// The unique indexes are part of the guarantees under test.
	models.EnsureIndexes(context.Background())
	h := &harness{t: t, ctx: context.Background(), ns: "test:settle:" + bson.NewObjectID().Hex(), userID: bson.NewObjectID()}

	saved := config.RedisKeys
	config.RedisKeys.ExpiryQueue = h.ns + ":expiry:queue"
	config.RedisKeys.SettlementQueue = h.ns + ":settlement:queue"
	config.RedisKeys.ExposureAssets = h.ns + ":exp:assets"
	h.keys = append(h.keys, config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue,
		config.RedisKeys.ExposureAssets, config.RedisKeys.UserExposure(h.userID.Hex()))

	savedNotify, savedHook := notify, testHook
	notify = func(userID string, in notifications.Input) {
		if _, err := notifications.CreateNotification(context.Background(), userID, in); err != nil {
			t.Errorf("notification: %v", err)
		}
	}
	resetPolicyCache()
	riskconfig.InvalidateRiskConfigCache()

	t.Cleanup(func() {
		testHook, notify = savedHook, savedNotify
		_ = redisx.R().Del(context.Background(), h.keys...).Err()
		config.RedisKeys = saved
		resetPolicyCache()
		riskconfig.InvalidateRiskConfigCache()
		// Let fire-and-forget websocket publishes drain before the store
		// connections close.
		time.Sleep(50 * time.Millisecond)
	})

	h.fund(models.AccountLive)
	return h
}

func (h *harness) fund(accountType string) {
	h.t.Helper()
	if _, err := wallets.GetOrCreateWallet(h.ctx, h.userID.Hex(), accountType, ""); err != nil {
		h.t.Fatalf("wallet: %v", err)
	}
	if _, err := wallets.CreditWallet(h.ctx, wallets.AdjustmentInput{
		UserID: h.userID.Hex(), AccountType: accountType, Amount: testBalance, Type: models.WalletTxAdjustmentCredit,
		IdempotencyKey: "test:credit:" + accountType + ":" + h.userID.Hex(),
	}); err != nil {
		h.t.Fatalf("credit: %v", err)
	}
}

func enableTransactions(t *testing.T) {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		t.Skip("MONGO_URI not set in the environment")
	}
	name := mongox.DB().Name()
	scheme, rest := "mongodb://", strings.TrimPrefix(uri, "mongodb://")
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = mongox.Disconnect(ctx)
	if err := mongox.Connect(ctx, scheme+rest+"/"+name+query, true); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if mongox.DB().Name() != name {
		t.Fatalf("reconnected to %q", mongox.DB().Name())
	}
	if !mongox.TransactionsAvailable() {
		t.Skip("MongoDB deployment has no transactions")
	}
}

func resetPolicyCache() {
	policyCacheMu.Lock()
	policyCache = map[bson.ObjectID]models.SettlementPolicy{}
	policyCacheMu.Unlock()
}

// setRiskConfig activates a new risk-config version with the given fields
// set (including settlement policy fields read by name).
func (h *harness) setRiskConfig(fields bson.M) {
	h.t.Helper()
	if _, err := riskconfig.GetRiskConfig(h.ctx); err != nil { // bootstrap v1
		h.t.Fatal(err)
	}
	coll := mongox.C(models.CollRiskConfigs)
	var current bson.M
	if err := coll.FindOne(h.ctx, bson.M{"scope": models.RiskScopeGlobal, "active": true}).Decode(&current); err != nil {
		h.t.Fatal(err)
	}
	if _, err := coll.UpdateOne(h.ctx, bson.M{"_id": current["_id"]}, bson.M{"$set": bson.M{"active": false}}); err != nil {
		h.t.Fatal(err)
	}
	next := bson.M{}
	for k, v := range current {
		next[k] = v
	}
	delete(next, "_id")
	next["version"] = toInt64(current["version"]) + 1
	next["active"] = true
	for k, v := range fields {
		next[k] = v
	}
	if _, err := coll.InsertOne(h.ctx, next); err != nil {
		h.t.Fatal(err)
	}
	riskconfig.InvalidateRiskConfigCache()
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int32:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	return 0
}

type tradeOpts struct {
	direction   models.Direction
	entry       float64
	expiry      int64
	accountType string
}

// openTrade writes an OPEN trade the way acceptance leaves it: stake locked,
// exposure added, expiry queued.
func (h *harness) openTrade(o tradeOpts) *models.BinaryTrade {
	h.t.Helper()
	if o.direction == "" {
		o.direction = models.DirectionUp
	}
	if o.accountType == "" {
		o.accountType = models.AccountLive
	}
	now := mongox.Now()
	trade := &models.BinaryTrade{
		ID:              bson.NewObjectID(),
		Reference:       "T-" + bson.NewObjectID().Hex(),
		UserID:          h.userID,
		AssetID:         bson.NewObjectID(),
		Symbol:          "TESTUSD",
		Direction:       o.direction,
		AccountType:     o.accountType,
		Stake:           testStake,
		Currency:        "INR",
		PayoutPercent:   testPayout,
		Profit:          testProfit,
		EntryPrice:      o.entry,
		EntryTimestamp:  o.expiry - 60_000,
		ExpiryTimestamp: o.expiry,
		Status:          models.TradeStatusOpen,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	assetHex := trade.AssetID.Hex()
	h.keys = append(h.keys, config.RedisKeys.TickHistory(assetHex), config.RedisKeys.LastTick(assetHex),
		config.RedisKeys.Exposure(assetHex))
	if _, err := wallets.LockTradeFunds(h.ctx, wallets.TradeFundsInput{
		UserID: h.userID.Hex(), AccountType: o.accountType, Stake: testStake, TradeID: trade.ID.Hex(),
	}); err != nil {
		h.t.Fatalf("lock: %v", err)
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(h.ctx, trade); err != nil {
		h.t.Fatalf("insert trade: %v", err)
	}
	if o.accountType == models.AccountLive {
		if _, err := exposure.ApplyExposureDelta(h.ctx, exposure.ExposureDelta{
			AssetID: assetHex, Symbol: trade.Symbol, UserID: h.userID.Hex(), Direction: trade.Direction,
			Stake: trade.Stake, Profit: trade.Profit, Sign: 1,
		}); err != nil {
			h.t.Fatalf("exposure: %v", err)
		}
	}
	if err := Reschedule(h.ctx, trade.ID.Hex(), trade.ExpiryTimestamp); err != nil {
		h.t.Fatal(err)
	}
	return trade
}

type tickOpts struct {
	price      float64
	ts         int64 // market (provider) timestamp
	receivedAt int64 // 0 = ts
	seq        *int64
}

// tick records one tick in the asset's history (and as the last tick).
func (h *harness) tick(trade *models.BinaryTrade, o tickOpts) {
	h.t.Helper()
	if o.receivedAt == 0 {
		o.receivedAt = o.ts
	}
	assetHex := trade.AssetID.Hex()
	pt := o.ts
	t := models.NormalisedTick{
		MarketTick: models.MarketTick{
			Asset: trade.Symbol, Price: o.price, Timestamp: o.ts, ProviderTimestamp: &pt, ReceivedAt: o.receivedAt, Sequence: o.seq,
		},
		AssetID: assetHex, Symbol: trade.Symbol, ProcessedAt: o.receivedAt,
	}
	member, _ := json.Marshal(t)
	if err := redisx.R().ZAdd(h.ctx, config.RedisKeys.TickHistory(assetHex), redisZ(o.ts, string(member))).Err(); err != nil {
		h.t.Fatal(err)
	}
	i := func(n int64) string { return strconv.FormatInt(n, 10) }
	if err := redisx.R().HSet(h.ctx, config.RedisKeys.LastTick(assetHex), map[string]any{
		"assetId": assetHex, "symbol": trade.Symbol, "price": strconv.FormatFloat(o.price, 'f', -1, 64),
		"bid": "", "ask": "", "timestamp": i(o.ts), "providerTimestamp": i(o.ts), "receivedAt": i(o.receivedAt),
		"processedAt": i(o.receivedAt), "sequence": "", "feedLatencyMs": "0", "processingLatencyMs": "0",
		"totalLatencyMs": "0", "provider": "test-feed",
	}).Err(); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) load(id bson.ObjectID) *models.BinaryTrade {
	h.t.Helper()
	trade, err := loadTrade(h.ctx, id)
	if err != nil || trade == nil {
		h.t.Fatalf("load trade: %v", err)
	}
	return trade
}

func (h *harness) wallet() models.WalletView {
	h.t.Helper()
	w, err := wallets.GetWallet(h.ctx, h.userID.Hex(), models.AccountLive, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return w
}

func (h *harness) count(coll string, filter bson.M) int64 {
	h.t.Helper()
	n, err := mongox.C(coll).CountDocuments(h.ctx, filter)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) row(id bson.ObjectID) *models.Settlement {
	h.t.Helper()
	row, err := findSettlementRow(h.ctx, id)
	if err != nil {
		h.t.Fatal(err)
	}
	return row
}

// assertBooked checks the one-outcome invariant: final status, exactly one
// settlement row and one settle ledger row, and a wallet that moved once.
func (h *harness) assertBooked(trade *models.BinaryTrade, status models.TradeStatus, balanceDelta int64) *models.Settlement {
	h.t.Helper()
	got := h.load(trade.ID)
	if got.Status != status {
		h.t.Fatalf("status %s, want %s", got.Status, status)
	}
	if n := h.count(models.CollSettlements, bson.M{"tradeId": trade.ID}); n != 1 {
		h.t.Fatalf("%d settlement rows", n)
	}
	if n := h.count(models.CollWalletTransactions, bson.M{"idempotencyKey": SettleKey(trade.ID.Hex())}); n != 1 {
		h.t.Fatalf("%d settle ledger rows", n)
	}
	w := h.wallet()
	if w.Balance != testBalance+balanceDelta || w.LockedBalance != 0 {
		h.t.Fatalf("wallet balance %d locked %d, want %d / 0", w.Balance, w.LockedBalance, testBalance+balanceDelta)
	}
	row := h.row(trade.ID)
	if got.SettlementAmount == nil || *got.SettlementAmount != balanceDelta {
		h.t.Fatalf("trade settlementAmount %v, want %d", got.SettlementAmount, balanceDelta)
	}
	if got.PlatformResult == nil || *got.PlatformResult != row.PlatformResult {
		h.t.Fatalf("trade platformResult %v, row %d", got.PlatformResult, row.PlatformResult)
	}
	if row.Result != *got.Result {
		h.t.Fatalf("row result %s, trade %s", row.Result, *got.Result)
	}
	return row
}

func (h *harness) exposureOpenTrades(trade *models.BinaryTrade) int64 {
	h.t.Helper()
	v, err := redisx.R().HGet(h.ctx, config.RedisKeys.Exposure(trade.AssetID.Hex()), "openTrades").Int64()
	if err != nil && !redisx.IsNil(err) {
		h.t.Fatal(err)
	}
	return v
}

func (h *harness) queued(key, id string) bool {
	h.t.Helper()
	if key == config.RedisKeys.ExpiryQueue {
		_, err := redisx.R().ZScore(h.ctx, key, id).Result()
		return err == nil
	}
	items, err := redisx.R().LRange(h.ctx, key, 0, -1).Result()
	if err != nil {
		h.t.Fatal(err)
	}
	for _, it := range items {
		if it == id {
			return true
		}
	}
	return false
}

func now() int64 { return util.NowMs() }

func i64p(v int64) *int64 { return &v }
