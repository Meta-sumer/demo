package public

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/testsupport"
)

func resetCaches() {
	riskconfig.InvalidateRiskConfigCache()
	payouts.InvalidatePayoutCache()
	assets.InvalidateAssetCache()
	pricing.InvalidateBoard()
	pricing.ResetLiveCaches()
	pricing.InvalidatePublicMarkets()
}

// setRiskConfig activates a new version of the global configuration with
// fields changed.
func setRiskConfig(t *testing.T, ctx context.Context, fields bson.M) {
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
	resetCaches()
}

// freshTick stores a last quote received now, so the engine can price.
func freshTick(t *testing.T, ctx context.Context, a models.AssetView) {
	t.Helper()
	now := strconv.FormatInt(util.NowMs(), 10)
	if err := redisx.R().HSet(ctx, config.RedisKeys.LastTick(a.ID), map[string]any{
		"assetId": a.ID, "symbol": a.Symbol, "price": "100", "bid": "", "ask": "",
		"timestamp": now, "providerTimestamp": now, "receivedAt": now, "processedAt": now, "sequence": "",
		"feedLatencyMs": "0", "processingLatencyMs": "0", "totalLatencyMs": "5", "provider": "test-feed", "spreadBps": "0.5",
	}).Err(); err != nil {
		t.Fatal(err)
	}
}

func createAsset(t *testing.T, ctx context.Context, symbol string, cat models.AssetCategory, active bool, rows map[int64]float64) models.AssetView {
	t.Helper()
	expiries := []int64{}
	for e := range rows {
		expiries = append(expiries, e)
	}
	slices.Sort(expiries)
	a, err := assets.CreateAsset(ctx, assets.CreateAssetInput{
		Symbol: symbol, Name: symbol + " name", Category: cat, ProviderSymbol: "pub" + bson.NewObjectID().Hex()[16:],
		SupportedExpiries: expiries, Active: &active,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range expiries {
		if _, _, err := payouts.UpsertPayout(ctx, payouts.UpsertPayoutInput{AssetID: a.ID, ExpirySeconds: e, PayoutPercent: rows[e]}, ""); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// get calls GET /public/markets on a router mounted like cmd/server's.
func get(t *testing.T, ip, bearer string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	r := chi.NewRouter()
	r.Route("/public", Mount)
	req := httptest.NewRequest(http.MethodGet, "/public/markets", nil)
	req.RemoteAddr = ip + ":40000"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestPublicMarketsListsOnlyActivePricedAssetsWithoutASession(t *testing.T) {
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	resetCaches()

	// BTC/USD and ETH/USD carry enabled rules by default and EUR/USD a
	// disabled one; XRP/USD gets an enabled rule but its asset is off.
	btc := createAsset(t, ctx, "BTC/USD", models.AssetCategoryCrypto, true, map[int64]float64{30: 82, 60: 84, 300: 87})
	eth := createAsset(t, ctx, "ETH/USD", models.AssetCategoryCrypto, true, map[int64]float64{60: 83})
	eur := createAsset(t, ctx, "EUR/USD", models.AssetCategoryForex, true, map[int64]float64{60: 80})
	xrp := createAsset(t, ctx, "XRP/USD", models.AssetCategoryCrypto, false, map[int64]float64{60: 90})

	ip := "198.51.100." + strconv.Itoa(int(time.Now().UnixNano()%200)+20)
	t.Cleanup(func() {
		c := context.Background()
		keys := []string{}
		for _, a := range []models.AssetView{btc, eth, eur, xrp} {
			iter := redisx.R().Scan(c, 0, "*"+a.ID+"*", 500).Iterator()
			for iter.Next(c) {
				keys = append(keys, iter.Val())
			}
		}
		iter := redisx.R().Scan(c, 0, config.RedisKeys.RateLimit(marketsLimit.Bucket, "ip:"+ip)+"*", 100).Iterator()
		for iter.Next(c) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			_ = redisx.R().Del(c, keys...).Err()
		}
		resetCaches()
	})

	// FIXED mode pays the row's percent, so the figures are exact; the
	// engine's other checks (scope, expiry, fresh data) still apply.
	cfg := models.DefaultRiskConfig()
	xrpRule := cfg.PricingSymbolRules[0]
	xrpRule.Symbol = "XRP/USD"
	setRiskConfig(t, ctx, bson.M{"pricingMode": models.PricingModeFixed, "feedTickMaxAgeMs": int64(10_000),
		"pricingSymbolRules": append(cfg.PricingSymbolRules, xrpRule)})
	// ETH/USD has no price: listed, without figures.
	for _, a := range []models.AssetView{btc, eur, xrp} {
		freshTick(t, ctx, a)
	}

	rec, body := get(t, ip, "not-a-token")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=5" {
		t.Fatalf("Cache-Control %q", cc)
	}
	if body["success"] != true {
		t.Fatalf("%v", body)
	}
	data := body["data"].(map[string]any)

	// The shape is the contract the landing pages read; nothing else leaks.
	if got, want := keysOf(data), []string{"generatedAt", "markets", "maxAgeSeconds", "maxPayoutPercent", "minPayoutPercent", "payoutBasis"}; !slices.Equal(got, want) {
		t.Fatalf("top-level keys %v, want %v", got, want)
	}
	if data["payoutBasis"] != pricing.PublicPayoutBasis || data["maxAgeSeconds"] != float64(5) {
		t.Fatalf("%v", data)
	}
	if _, err := time.Parse(time.RFC3339, data["generatedAt"].(string)); err != nil {
		t.Fatalf("generatedAt: %v", err)
	}
	if data["minPayoutPercent"] != float64(82) || data["maxPayoutPercent"] != float64(87) {
		t.Fatalf("range %v-%v", data["minPayoutPercent"], data["maxPayoutPercent"])
	}

	list := data["markets"].([]any)
	symbols := []string{}
	for _, m := range list {
		symbols = append(symbols, m.(map[string]any)["symbol"].(string))
	}
	if !slices.Equal(symbols, []string{"BTC/USD", "ETH/USD"}) {
		t.Fatalf("markets %v, want only the active priced ones", symbols)
	}

	b := list[0].(map[string]any)
	if got, want := keysOf(b), []string{"category", "maxPayoutPercent", "minPayoutPercent", "name", "payouts", "symbol"}; !slices.Equal(got, want) {
		t.Fatalf("market keys %v, want %v", got, want)
	}
	if b["name"] != "BTC/USD name" || b["category"] != "CRYPTO" || b["minPayoutPercent"] != float64(82) || b["maxPayoutPercent"] != float64(87) {
		t.Fatalf("%v", b)
	}
	rows := b["payouts"].([]any)
	want := [][2]float64{{30, 82}, {60, 84}, {300, 87}}
	if len(rows) != len(want) {
		t.Fatalf("payouts %v", rows)
	}
	for i, r := range rows {
		p := r.(map[string]any)
		if got := keysOf(p); !slices.Equal(got, []string{"expirySeconds", "payoutPercent"}) {
			t.Fatalf("payout keys %v", got)
		}
		if p["expirySeconds"] != want[i][0] || p["payoutPercent"] != want[i][1] {
			t.Fatalf("payout %d: %v, want %v", i, p, want[i])
		}
	}

	e := list[1].(map[string]any)
	if ps, ok := e["payouts"].([]any); !ok || len(ps) != 0 || e["minPayoutPercent"] != nil || e["maxPayoutPercent"] != nil {
		t.Fatalf("an instrument that cannot be priced now is listed without figures: %v", e)
	}

	// Served from the snapshot: a second call inside the TTL is the same one.
	_, again := get(t, ip, "")
	if again["data"].(map[string]any)["generatedAt"] != data["generatedAt"] {
		t.Fatalf("snapshot rebuilt inside its TTL")
	}
}
