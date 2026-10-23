package hedging

import (
	"context"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/hedging/binancefake"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/testsupport"
)

// Venue harness: a throwaway MongoDB database, the shared Redis under keys
// of this test's own asset id, and the fake Binance (httptest). Nothing
// leaves the machine.

const testSigma = 0.0001 // per sqrt(second)

type harness struct {
	t     *testing.T
	ctx   context.Context
	fake  *binancefake.Fake
	venue *providers.Binance
	asset *models.Asset
}

func newHarness(t *testing.T, mods ...func(*config.Env)) *harness {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)

	fake := binancefake.New()
	t.Cleanup(fake.Close)
	prev := config.E
	env := &config.Env{
		NodeEnv: "test", WorkersEnabled: false, HedgeIntervalMs: 1000,
		HedgeProvider: "binance", HedgeBinanceEnv: "testnet", HedgeAPIURL: fake.URL(),
		HedgeAPIKey: fake.APIKey, HedgeAPISecret: fake.Secret,
		HedgeSymbols:    map[string]string{"BTC/USD": "BTCUSDT"},
		HedgeINRPerUSDT: 88, HedgeINRPerUSDTAsOf: time.Now().Add(-time.Hour).UnixMilli(), HedgeFXMaxAgeMs: 86_400_000,
		HedgeTakerFeeBps: 5, HedgeSlippageBps: 2, HedgeFallbackSpreadBps: 1, HedgeMaxCostBps: 30,
		HedgeMaxLiabilityMultiple: 0, HedgeMaxOrderUSDT: 50_000, HedgeReconcileIntervalMs: 60_000,
		HedgeRecvWindowMs: 5_000, HedgeRequestTimeoutMs: 2_000, HedgeVenueMaxAgeMs: 15_000,
	}
	for _, m := range mods {
		m(env)
	}
	config.E = env
	t.Cleanup(func() { config.E = prev })
	b := providers.NewBinance(providers.BinanceConfigFromEnv(env))
	providers.SetVenue(b)
	t.Cleanup(func() { providers.SetVenue(nil) })
	invalidateFxCache()
	t.Cleanup(invalidateFxCache)
	risk.HedgeSigmaSource = func(context.Context, string) (float64, string, bool) { return testSigma, "test", true }
	t.Cleanup(func() { risk.HedgeSigmaSource = nil })

	// Risk configuration: one band from 0% hedging the whole delta, so the
	// tests do not need a loaded book to cross a threshold.
	cfg := models.DefaultRiskConfig()
	cfg.HedgeThresholdPercent = 0
	cfg.HedgeLevels = []models.HedgeLevel{{Name: "MAX_ALLOWED_HEDGE", FromPercent: 0, ToPercent: 100_000, HedgeRatio: 1}}
	// A slow machine must not turn the test feed stale between the tick and
	// the assessment (feed health is not under test here).
	cfg.FeedHealthyMaxMs, cfg.FeedDegradedMaxMs, cfg.FeedStaleMaxMs, cfg.FeedTickMaxAgeMs = 5_000, 10_000, 20_000, 60_000
	cfg.CreatedAt, cfg.UpdatedAt = mongox.Now(), mongox.Now()
	if _, err := mongox.C(models.CollRiskConfigs).InsertOne(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	riskconfig.InvalidateRiskConfigCache()
	t.Cleanup(riskconfig.InvalidateRiskConfigCache)

	asset := &models.Asset{
		ID: bson.NewObjectID(), Symbol: "BTC/USD", Name: "Bitcoin", Category: models.AssetCategoryCrypto, Active: true,
		ProviderSymbol: "btcusdt", Provider: "binance", PricePrecision: 2, MinStake: 10_00, MaxStake: 100_000_00,
		SupportedExpiries: []int64{60, 300}, ExposureLimit: 5_000_000_00, NetExposureLimit: 2_000_000_00,
		HedgeEnabled: true, TargetHedgeRatio: 1, MinHedgeNotional: 1_000_00, MaxHedgeNotional: 1_000_000_00,
		TradingWindows: []models.TradingWindow{}, CreatedAt: mongox.Now(), UpdatedAt: mongox.Now(),
	}
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, asset); err != nil {
		t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	t.Cleanup(assets.InvalidateAssetCache)
	id := asset.ID.Hex()
	t.Cleanup(func() {
		redisx.R().Del(context.Background(), config.RedisKeys.LastTick(id), config.RedisKeys.RiskState(id),
			config.RedisKeys.Exposure(id), config.RedisKeys.TickHistory(id), venueKey("BTCUSDT"))
	})
	h := &harness{t: t, ctx: ctx, fake: fake, venue: b, asset: asset}
	h.tick(60_000)
	return h
}

// tick writes a fresh platform price (the feed must look healthy).
func (h *harness) tick(price float64) {
	now := util.NowMs()
	s := strconv.FormatFloat(price, 'f', -1, 64)
	n := strconv.FormatInt(now, 10)
	err := redisx.R().HSet(h.ctx, config.RedisKeys.LastTick(h.asset.ID.Hex()), map[string]any{
		"assetId": h.asset.ID.Hex(), "symbol": h.asset.Symbol, "price": s, "bid": "", "ask": "",
		"timestamp": n, "providerTimestamp": n, "receivedAt": n, "processedAt": n, "sequence": "",
		"feedLatencyMs": "0", "processingLatencyMs": "0", "totalLatencyMs": "0", "provider": "binance",
	}).Err()
	if err != nil {
		h.t.Fatal(err)
	}
}

// trade opens a LIVE trade on the asset at the money.
func (h *harness) trade(dir models.Direction, stake int64, expirySeconds int64) bson.ObjectID {
	now := util.NowMs()
	tr := models.BinaryTrade{
		ID: bson.NewObjectID(), Reference: util.Reference("BT"), UserID: bson.NewObjectID(), AssetID: h.asset.ID,
		Symbol: h.asset.Symbol, Direction: dir, AccountType: "LIVE", Stake: stake, Currency: "INR", PayoutPercent: 85,
		Profit: util.CalculateProfit(stake, 85), EntryPrice: 60_000, EntryTimestamp: now,
		ExpiryTimestamp: now + expirySeconds*1000, Status: models.TradeStatusOpen,
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(h.ctx, tr); err != nil {
		h.t.Fatal(err)
	}
	return tr.ID
}

func (h *harness) closeAllTrades() {
	if _, err := mongox.C(models.CollBinaryTrades).UpdateMany(h.ctx, bson.M{"assetId": h.asset.ID},
		bson.M{"$set": bson.M{"status": models.TradeStatusWon}}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) evaluate() *HedgeEvaluation {
	h.t.Helper()
	h.tick(60_000)
	ev, err := EvaluateAndHedge(h.ctx, h.asset.ID.Hex())
	if err != nil {
		h.t.Fatal(err)
	}
	return ev
}

func (h *harness) position() *models.HedgePosition {
	h.t.Helper()
	p, err := GetPosition(h.ctx, h.asset.ID)
	if err != nil {
		h.t.Fatal(err)
	}
	if p == nil {
		return &models.HedgePosition{}
	}
	return p
}

func (h *harness) order(id bson.ObjectID) *models.HedgeOrder {
	h.t.Helper()
	o, err := FindHedgeOrderByID(h.ctx, id.Hex())
	if err != nil || o == nil {
		h.t.Fatalf("order %v: %v", id, err)
	}
	return o
}

func (h *harness) count(coll string, filter bson.M) int64 {
	h.t.Helper()
	n, err := mongox.C(coll).CountDocuments(h.ctx, filter)
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}
