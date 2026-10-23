package pricinghedge

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/hedging"
	"binarytrading/internal/service/hedging/binancefake"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/testsupport"
)

// offer prices one 60 s UP trade of 1,000.00 on an empty book at the
// baseline volatility with the given hedge estimate.
func offer(t *testing.T, cfg *models.RiskConfig, hedge pricing.HedgeCostEstimate) pricing.OfferResult {
	t.Helper()
	terms, ok := pricing.TermsFor(cfg, "BTC/USD", nil)
	if !ok {
		t.Fatal("no BTC/USD rule")
	}
	return pricing.ComputeOffer(pricing.RulesFrom(cfg), terms, pricing.OfferInput{Direction: models.DirectionUp,
		ExpirySeconds: 60, Stake: 1_000_00, Liability: 0, Sigma: terms.BaselineVol, SpreadBps: 0.2, Hedge: hedge})
}

func withAction(action string) *models.RiskConfig {
	c := models.DefaultRiskConfig()
	c.PricingHedgeUnavailableAction = action
	return &c
}

// assertWidenOrRefuse checks the engine's answer to an unavailable hedge:
// WIDEN deducts pricingHedgeUnavailableAdj (3 points) more than the normal
// payout, REFUSE blocks with HEDGE_UNAVAILABLE.
func assertWidenOrRefuse(t *testing.T, label string, est pricing.HedgeCostEstimate) {
	t.Helper()
	if !est.Required || est.Available {
		t.Fatalf("%s: estimate %+v, want required and unavailable", label, est)
	}
	normal := offer(t, withAction(models.PricingActionWiden), pricing.HedgeCostEstimate{Required: true, Available: true})
	widened := offer(t, withAction(models.PricingActionWiden), est)
	if !normal.OK || !widened.OK {
		t.Fatalf("%s: normal %+v widened %+v", label, normal, widened)
	}
	if widened.Components.HedgeUnavailableAdj != 0.03 {
		t.Fatalf("%s: hedge_unavailable_adj %v", label, widened.Components.HedgeUnavailableAdj)
	}
	diff := normal.Components.Payout - widened.Components.Payout
	if diff < 0.03-0.005-1e-9 || diff > 0.03+0.005+1e-9 {
		t.Fatalf("%s: WIDEN moved the payout by %v (normal %v, widened %v)", label, diff, normal.Components.Payout, widened.Components.Payout)
	}
	refused := offer(t, withAction(models.PricingActionRefuse), est)
	if refused.OK || refused.Code != pricing.CodeHedgeUnavailable {
		t.Fatalf("%s: REFUSE gave %+v", label, refused)
	}
}

func TestEstimatorMapsHedgingVerdicts(t *testing.T) {
	ctx := context.Background()
	hedged := &models.Asset{ID: bson.NewObjectID(), Symbol: "BTC/USD", Category: models.AssetCategoryCrypto, HedgeEnabled: true}
	calls := 0
	answer := hedging.HedgeCost{}
	var answerErr error
	e := Estimator{cost: func(_ context.Context, q hedging.CostQuery) (hedging.HedgeCost, error) {
		calls++
		if q.AssetID != hedged.ID.Hex() || q.HoldSeconds != 60 {
			t.Fatalf("query %+v", q)
		}
		return answer, answerErr
	}}
	req := func(a *models.Asset) pricing.HedgeCostRequest {
		return pricing.HedgeCostRequest{Asset: a, Direction: models.DirectionUp, Stake: 1_000_00, ExpirySeconds: 60}
	}

	// An asset that is not hedged needs no hedge; the venue is not asked.
	unhedged := *hedged
	unhedged.HedgeEnabled = false
	est, err := e.EstimateHedgeCost(ctx, req(&unhedged))
	if err != nil || est.Required || !est.Available || est.CostPoints != 0 || calls != 0 {
		t.Fatalf("unhedged %+v %v calls %d", est, err, calls)
	}

	// Available: the configured hedge cost is charged and the payout is normal.
	answer = hedging.HedgeCost{Available: true, Venue: "binance-testnet", HedgeCostBps: models.HedgeCostBps{RoundTripBps: 14}}
	est, err = e.EstimateHedgeCost(ctx, req(hedged))
	if err != nil || !est.Required || !est.Available || est.CostPoints != 0 || est.Provider != "binance-testnet" ||
		math.Abs(est.VenueCostFraction-0.0014) > 1e-12 {
		t.Fatalf("available %+v %v", est, err)
	}
	normal := offer(t, withAction(models.PricingActionRefuse), pricing.HedgeCostEstimate{})
	got := offer(t, withAction(models.PricingActionRefuse), est)
	if !got.OK || got.Components.Payout != normal.Components.Payout || got.Components.HedgeUnavailableAdj != 0 {
		t.Fatalf("available hedge changed the payout: %+v vs %+v", got, normal)
	}

	for _, c := range []struct {
		label  string
		answer hedging.HedgeCost
	}{
		{"venue unavailable", hedging.HedgeCost{Available: false, Reason: hedging.ReasonVenueUnavail}},
		{"venue stale", hedging.HedgeCost{Available: false, Reason: hedging.ReasonVenueStale}},
		{"fx stale", hedging.HedgeCost{Available: false, Reason: "FX_STALE"}},
		{"cost over limit", hedging.HedgeCost{Available: true, Expensive: true, Reason: hedging.ReasonCostLimit}},
	} {
		answer = c.answer
		est, err := e.EstimateHedgeCost(ctx, req(hedged))
		if err != nil || est.Reason != c.answer.Reason {
			t.Fatalf("%s: %+v %v", c.label, est, err)
		}
		assertWidenOrRefuse(t, c.label, est)
	}

	// An error from the hedging service is passed on; the engine treats it
	// as an unavailable hedge (pricing.Evaluate, ESTIMATOR_ERROR).
	answerErr = errors.New("redis down")
	if _, err := e.EstimateHedgeCost(ctx, req(hedged)); err == nil {
		t.Fatal("error swallowed")
	}
}

func TestInstallWiresBothHooks(t *testing.T) {
	Install()
	t.Cleanup(Uninstall)
	if _, ok := pricing.CurrentHedgeCostEstimator().(Estimator); !ok {
		t.Fatalf("estimator %T", pricing.CurrentHedgeCostEstimator())
	}
	if risk.HedgeSigmaSource == nil {
		t.Fatal("sigma source not installed")
	}
	Uninstall()
	if _, ok := pricing.CurrentHedgeCostEstimator().(pricing.DefaultHedgeCostEstimator); !ok || risk.HedgeSigmaSource != nil {
		t.Fatal("uninstall did not restore the defaults")
	}
}

// harness: a throwaway database, the fake Binance futures API, a hedged
// BTC/USD asset and a fresh INR rate.
type harness struct {
	ctx   context.Context
	fake  *binancefake.Fake
	venue *providers.Binance
	asset *models.Asset
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	fake := binancefake.New()
	t.Cleanup(fake.Close)
	prev := config.E
	env := &config.Env{
		NodeEnv: "test", HedgeProvider: "binance", HedgeBinanceEnv: "testnet", HedgeAPIURL: fake.URL(),
		HedgeAPIKey: fake.APIKey, HedgeAPISecret: fake.Secret, HedgeSymbols: map[string]string{"BTC/USD": "BTCUSDT"},
		HedgeINRPerUSDT: 88, HedgeINRPerUSDTAsOf: time.Now().Add(-time.Hour).UnixMilli(), HedgeFXMaxAgeMs: 86_400_000,
		HedgeTakerFeeBps: 5, HedgeSlippageBps: 2, HedgeFallbackSpreadBps: 1, HedgeMaxCostBps: 30,
		HedgeMaxOrderUSDT: 50_000, HedgeRecvWindowMs: 5_000, HedgeRequestTimeoutMs: 2_000, HedgeVenueMaxAgeMs: 15_000,
		FeedHealthyMaxMs: 100, FeedDegradedMaxMs: 300, FeedTickMaxAgeMs: 5000,
	}
	config.E = env
	t.Cleanup(func() { config.E = prev })
	venue := providers.NewBinance(providers.BinanceConfigFromEnv(env))
	providers.SetVenue(venue)
	t.Cleanup(func() { providers.SetVenue(nil) })
	riskconfig.InvalidateRiskConfigCache()
	t.Cleanup(riskconfig.InvalidateRiskConfigCache)

	asset := &models.Asset{
		ID: bson.NewObjectID(), Symbol: "BTC/USD", Name: "Bitcoin", Category: models.AssetCategoryCrypto, Active: true,
		ProviderSymbol: "btcusdt", Provider: "binance", PricePrecision: 2, MinStake: 10_00, MaxStake: 100_000_00,
		SupportedExpiries: []int64{60, 300}, HedgeEnabled: true, TargetHedgeRatio: 1, MinHedgeNotional: 1_000_00,
		MaxHedgeNotional: 1_000_000_00, TradingWindows: []models.TradingWindow{}, CreatedAt: mongox.Now(), UpdatedAt: mongox.Now(),
	}
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, asset); err != nil {
		t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	t.Cleanup(assets.InvalidateAssetCache)
	t.Cleanup(func() { redisx.R().Del(context.Background(), "md:vol:"+asset.ID.Hex()) })
	return &harness{ctx: ctx, fake: fake, venue: venue, asset: asset}
}

func (h *harness) estimate(t *testing.T) pricing.HedgeCostEstimate {
	t.Helper()
	est, err := pricing.CurrentHedgeCostEstimator().EstimateHedgeCost(h.ctx, pricing.HedgeCostRequest{
		Asset: h.asset, Direction: models.DirectionUp, Stake: 1_000_00, ExpirySeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	return est
}

// The installed estimator follows the hedging service's live state: venue,
// INR rate and cost limit.
func TestHedgeStateDrivesThePayout(t *testing.T) {
	h := newHarness(t)
	Install()
	t.Cleanup(Uninstall)

	hedging.RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")
	est := h.estimate(t)
	if !est.Required || !est.Available || est.Reason != "" || !(est.VenueCostFraction > 0) {
		t.Fatalf("healthy venue %+v", est)
	}
	normal := offer(t, withAction(models.PricingActionWiden), pricing.HedgeCostEstimate{})
	if got := offer(t, withAction(models.PricingActionWiden), est); !got.OK || got.Components.Payout != normal.Components.Payout {
		t.Fatalf("available hedge: %+v vs normal %+v", got.Components, normal.Components)
	}

	// Venue outage.
	h.fake.Set(func(f *binancefake.Fake) { f.Down = true })
	hedging.RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")
	est = h.estimate(t)
	if est.Reason != hedging.ReasonVenueUnavail {
		t.Fatalf("outage %+v", est)
	}
	assertWidenOrRefuse(t, "venue outage", est)
	h.fake.Set(func(f *binancefake.Fake) { f.Down = false })
	hedging.RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")

	// INR rate older than HEDGE_FX_MAX_AGE_MS.
	config.E.HedgeINRPerUSDTAsOf = time.Now().Add(-48 * time.Hour).UnixMilli()
	est = h.estimate(t)
	if est.Reason != "FX_STALE" {
		t.Fatalf("stale rate %+v", est)
	}
	assertWidenOrRefuse(t, "fx stale", est)
	config.E.HedgeINRPerUSDTAsOf = time.Now().Add(-time.Hour).UnixMilli()

	// Round trip above HEDGE_MAX_COST_BPS.
	config.E.HedgeMaxCostBps = 1
	est = h.estimate(t)
	if est.Reason != hedging.ReasonCostLimit {
		t.Fatalf("expensive %+v", est)
	}
	assertWidenOrRefuse(t, "cost over limit", est)
	config.E.HedgeMaxCostBps = 30
	if est = h.estimate(t); !est.Available {
		t.Fatalf("recovered %+v", est)
	}
}

// The hedge target's delta uses the pricing engine's volatility: the EWMA
// when one is stored, else the instrument's baseline.
func TestHedgeSigmaIsThePricingVolatility(t *testing.T) {
	h := newHarness(t)
	Install()
	t.Cleanup(Uninstall)
	id := h.asset.ID.Hex()

	s, src, ok := risk.HedgeSigmaSource(h.ctx, id)
	if !ok || s != models.DefaultBTCBaselineVol || src != pricing.SigmaSourceBaseline {
		t.Fatalf("baseline: %v %q %v", s, src, ok)
	}
	raw, _ := json.Marshal(map[string]any{"var": 4e-8, "updatedAt": time.Now().UnixMilli()})
	if err := redisx.R().Set(h.ctx, "md:vol:"+id, raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	s, src, ok = risk.HedgeSigmaSource(h.ctx, id)
	if !ok || math.Abs(s-2e-4) > 1e-12 || src != pricing.SigmaSourceEWMA {
		t.Fatalf("ewma: %v %q %v", s, src, ok)
	}
	if ps, psrc, _ := pricing.Sigma(h.ctx, id); ps != s || psrc != src {
		t.Fatalf("pricing %v %q, hedge %v %q", ps, psrc, s, src)
	}
	// An asset without a pricing rule has no engine volatility: the hedge
	// falls back to its realised volatility.
	other := *h.asset
	other.ID, other.Symbol, other.ProviderSymbol = bson.NewObjectID(), "XAU/USD", "xauusd"
	if _, err := mongox.C(models.CollAssets).InsertOne(h.ctx, other); err != nil {
		t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	if _, _, ok := risk.HedgeSigmaSource(h.ctx, other.ID.Hex()); ok {
		t.Fatal("sigma for an unpriced asset")
	}
}
