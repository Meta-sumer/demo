package pricing

import (
	"math"
	"math/rand/v2"
	"testing"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
)

// The payout formula (owner's specification sections 6, 7, 12, 16).

func defaultRules() Rules {
	cfg := models.DefaultRiskConfig()
	return RulesFrom(&cfg)
}

// eurusd is the specification's forex rule set (section 11).
func eurusd() Terms {
	return Terms{Symbol: "EUR/USD", Enabled: true, Base: 0.90, Min: 0.70, Max: 0.92, Margin: 0.030,
		BaselineVol: 1.25e-5, HedgeCost: 0.020, MaxExposure: 50_000}
}

func btcusd() Terms {
	return Terms{Symbol: "BTC/USD", Enabled: true, Base: 0.86, Min: 0.65, Max: 0.88, Margin: 0.035,
		BaselineVol: 9e-5, HedgeCost: 0.035, MaxExposure: 25_000}
}

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s = %.12g, want %.12g", name, got, want)
	}
}

// TestSpecWorkedExample reproduces section 12: EUR/USD, 60 s, 1,000 stake on
// CALL, calm market, empty book, vol_ratio 0.712, spread 0.6 bps -> 83.0%.
func TestSpecWorkedExample(t *testing.T) {
	r := defaultRules()
	res := ComputeOffer(r, eurusd(), OfferInput{Direction: models.DirectionUp, ExpirySeconds: 60, Stake: 1000,
		Sigma: 0.712 * 1.25e-5, SpreadBps: 0.6, Hedge: HedgeCostEstimate{Available: true}})
	if !res.OK {
		t.Fatalf("blocked: %s %s", res.Code, res.Reason)
	}
	c := res.Components
	near(t, "vol_ratio", c.VolRatio, 0.712, 1e-12)
	near(t, "vol_adj", c.VolAdj, 0, 0)
	near(t, "expected_move", c.ExpectedMoveBps, 0.712*1.25e-5*math.Sqrt(60)*1e4, 1e-12)
	near(t, "expected_move ~0.69", c.ExpectedMoveBps, 0.69, 0.005)
	near(t, "spread_adj", c.SpreadAdj, 0.0174, 0.00005)
	near(t, "hedge_adj", c.HedgeAdj, 0.0200, 1e-12)
	near(t, "margin_adj", c.MarginAdj, 0.0300, 1e-12)
	near(t, "util", c.UtilEstimate, 0.018, 1e-12)
	near(t, "exposure_adj", c.ExposureAdj, 0.08*0.018*0.018, 1e-15)
	near(t, "raw", c.Raw, 0.8326, 0.00005)
	near(t, "clamped", c.Clamped, c.Raw, 0)
	near(t, "payout", c.Payout, 0.830, 1e-12)
	if PayoutPercent(c.Payout) != 83 {
		t.Fatalf("percent %v", PayoutPercent(c.Payout))
	}
	// 1000 * (1 + 0.83) = 1830 credited on a win.
	if got := 1000 * (1 + c.Payout); math.Abs(got-1830) > 1e-9 {
		t.Fatalf("credit %v", got)
	}
}

// TestVolatilitySpike: at vol_ratio 2.0 a 60 s expiry loses 6 points, a 30 s
// one 7.8 (expiry factor 1.3); the deduction is capped at 12 points and
// quoting stops above 4x.
func TestVolatilitySpike(t *testing.T) {
	r := defaultRules()
	in := OfferInput{Direction: models.DirectionUp, ExpirySeconds: 60, Stake: 1000, Sigma: 2 * 1.25e-5,
		SpreadBps: 0.6, Hedge: HedgeCostEstimate{Available: true}}
	near(t, "vol_adj 60s", ComputeOffer(r, eurusd(), in).Components.VolAdj, 0.06, 1e-12)
	in.ExpirySeconds = 30
	near(t, "vol_adj 30s", ComputeOffer(r, eurusd(), in).Components.VolAdj, 0.078, 1e-12)
	in.Sigma = 3.5 * 1.25e-5 // 0.06 x 2.5 x 1.3 = 0.195 -> capped
	near(t, "vol_adj capped", ComputeOffer(r, eurusd(), in).Components.VolAdj, 0.12, 1e-12)
	in.Sigma = 4 * 1.25e-5 // at the block ratio: still quoted
	if res := ComputeOffer(r, eurusd(), in); !res.OK {
		t.Fatalf("at 4x: %s", res.Reason)
	}
	in.Sigma = 4.01 * 1.25e-5
	if res := ComputeOffer(r, eurusd(), in); res.OK || res.Code != CodeVolatilityBlock {
		t.Fatalf("above 4x: %+v", res)
	}
	// Calm markets are not rewarded.
	in.Sigma, in.ExpirySeconds = 0.3*1.25e-5, 60
	near(t, "calm", ComputeOffer(r, eurusd(), in).Components.VolAdj, 0, 0)
}

func TestExposureBlocksTheLoadedSideOnly(t *testing.T) {
	r := defaultRules()
	terms := btcusd()
	base := OfferInput{ExpirySeconds: 60, Stake: 1000, Sigma: 9e-5, SpreadBps: 0.1, Hedge: HedgeCostEstimate{Available: true}}
	// L(UP) = 24,500: one more UP trade at the base payout passes 25,000.
	up := base
	up.Direction, up.Liability = models.DirectionUp, 24_500
	if res := ComputeOffer(r, terms, up); res.OK || res.Code != CodeExposureLimit {
		t.Fatalf("loaded side: %+v", res)
	}
	// The other side offsets the liability: L(DOWN) is negative.
	down := base
	down.Direction, down.Liability = models.DirectionDown, -10_000
	res := ComputeOffer(r, terms, down)
	if !res.OK || res.Components.UtilEstimate != 0 || res.Components.ExposureAdj != 0 {
		t.Fatalf("offsetting side: %+v", res)
	}
	// Quadratic penalty, capped: util 0.5 -> 0.08 x 0.25 = 2 points.
	up.Liability = int64(0.5*25_000 - 1000*0.86)
	near(t, "exposure_adj", ComputeOffer(r, terms, up).Components.ExposureAdj, 0.02, 1e-12)
}

func TestFinalExposureCheckUsesTheRealPayout(t *testing.T) {
	r := defaultRules()
	terms := btcusd()
	terms.Base = 0.60 // below the minimum: the clamp lifts the payout to 0.65
	in := OfferInput{Direction: models.DirectionUp, ExpirySeconds: 60, Stake: 1000, Sigma: 9e-5, SpreadBps: 0.1,
		Hedge: HedgeCostEstimate{Available: true}, Liability: 24_380}
	// Estimate: 24,380 + 600 = 24,980 <= 25,000; final: 24,380 + 650 > 25,000.
	res := ComputeOffer(r, terms, in)
	if res.OK || res.Code != CodeExposureLimit || res.Components.UtilEstimate > 1 || res.Components.UtilFinal <= 1 {
		t.Fatalf("final check: %+v", res)
	}
}

func TestClampAndFloor(t *testing.T) {
	if FloorToStep(0.83, 0.005) != 0.83 {
		t.Fatalf("0.83 -> %v (165.999... must not floor to 0.825)", FloorToStep(0.83, 0.005))
	}
	if got := FloorToStep(0.83499, 0.005); math.Abs(got-0.83) > 1e-12 {
		t.Fatalf("0.83499 -> %v", got)
	}
	r := defaultRules()
	terms := eurusd()
	in := OfferInput{Direction: models.DirectionUp, ExpirySeconds: 300, Stake: 1, Sigma: 0.5 * 1.25e-5, SpreadBps: 0,
		Hedge: HedgeCostEstimate{Available: true}}
	terms.Base = 1.2 // far above max: clamped to 0.92
	if res := ComputeOffer(r, terms, in); res.Components.Payout != 0.92 {
		t.Fatalf("clamped high: %v", res.Components.Payout)
	}
	terms.Base = 0.5 // below min: lifted to 0.70
	if res := ComputeOffer(r, terms, in); math.Abs(res.Components.Payout-0.70) > 1e-12 {
		t.Fatalf("clamped low: %v", res.Components.Payout)
	}
}

func TestHedgeUnavailableWidensOrRefuses(t *testing.T) {
	r := defaultRules()
	in := OfferInput{Direction: models.DirectionUp, ExpirySeconds: 60, Stake: 1000, Sigma: 9e-5, SpreadBps: 0.1,
		Hedge: HedgeCostEstimate{Required: true, Available: false, Reason: "TEST"}}
	ok := ComputeOffer(r, btcusd(), OfferInput{Direction: models.DirectionUp, ExpirySeconds: 60, Stake: 1000, Sigma: 9e-5,
		SpreadBps: 0.1, Hedge: HedgeCostEstimate{Available: true}})
	wide := ComputeOffer(r, btcusd(), in)
	if !wide.OK || wide.Components.HedgeUnavailableAdj != 0.03 || wide.Components.Payout >= ok.Components.Payout {
		t.Fatalf("widen: %+v vs %+v", wide.Components, ok.Components)
	}
	r.HedgeUnavailableAction = models.PricingActionRefuse
	if res := ComputeOffer(r, btcusd(), in); res.OK || res.Code != CodeHedgeUnavailable {
		t.Fatalf("refuse: %+v", res)
	}
	// A live venue cost replaces the configured one.
	r = defaultRules()
	in.Hedge = HedgeCostEstimate{Required: true, Available: true, CostPoints: 0.01}
	near(t, "venue hedge_adj", ComputeOffer(r, btcusd(), in).Components.HedgeAdj, 0.01, 1e-12)
}

// TestPayoutProperties (spec 16, recommended): whatever the inputs, an offer
// stays within [min, max], is on the step grid and never rounds up.
func TestPayoutProperties(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 9))
	r := defaultRules()
	offered := 0
	for i := 0; i < 50_000; i++ {
		terms := btcusd()
		terms.Min = float64(100+rng.IntN(60)) * 0.005 // 0.50 .. 0.795, on the grid
		terms.Max = terms.Min + float64(rng.IntN(40))*0.005
		terms.Base = 0.4 + rng.Float64()*0.7
		terms.Margin = rng.Float64() * 0.08
		terms.HedgeCost = rng.Float64() * 0.06
		in := OfferInput{
			Direction:     []models.Direction{models.DirectionUp, models.DirectionDown}[rng.IntN(2)],
			ExpirySeconds: []int64{30, 60, 120, 300, 900, 1800}[rng.IntN(6)], Stake: int64(1 + rng.IntN(5000)),
			Liability: int64(rng.IntN(40_000)) - 15_000, Sigma: terms.BaselineVol * rng.Float64() * 5,
			SpreadBps: rng.Float64() * 8, Hedge: HedgeCostEstimate{Available: true},
		}
		res := ComputeOffer(r, terms, in)
		if !res.OK {
			continue
		}
		offered++
		c := res.Components
		if c.Payout < terms.Min-1e-12 || c.Payout > terms.Max+1e-12 {
			t.Fatalf("case %d: payout %v outside [%v, %v]", i, c.Payout, terms.Min, terms.Max)
		}
		if c.Payout > c.Clamped+1e-9 {
			t.Fatalf("case %d: rounded up %v > %v", i, c.Payout, c.Clamped)
		}
		if steps := c.Payout / r.Step; math.Abs(steps-math.Round(steps)) > 1e-6 {
			t.Fatalf("case %d: %v not on the step grid", i, c.Payout)
		}
		if c.Raw < c.Clamped-1e-12 && c.Clamped != terms.Min {
			t.Fatalf("case %d: clamp raised the payout above raw without hitting the minimum", i)
		}
	}
	if offered < 5000 {
		t.Fatalf("only %d offered", offered)
	}
}

func TestRulesFromDefaults(t *testing.T) {
	cfg := models.DefaultRiskConfig()
	r := RulesFrom(&cfg)
	if r.ExpiryFactor(30) != 1.3 || r.ExpiryFactor(60) != 1 || r.ExpiryFactor(120) != 0.9 || r.ExpiryFactor(300) != 0.8 ||
		r.ExpiryFactor(900) != 1 || r.Step != 0.005 || r.OfferTTLMs != 3000 || r.MaxSlippageBps != 2 || r.StaleAfterMs != 2000 {
		t.Fatalf("rules %+v", r)
	}
	if got := RuleVersion("rules-2026.10.1", 7, 3); got != "rules-2026.10.1+rc7+p3" {
		t.Fatal(got)
	}
	// Launch scope: crypto priced, forex kept but disabled, gold absent.
	for sym, want := range map[string]bool{"BTC/USD": true, "ETH/USD": true, "EUR/USD": false, "GBP/USD": false} {
		tm, ok := TermsFor(&cfg, sym, nil)
		if !ok || tm.Enabled != want {
			t.Fatalf("%s: %+v", sym, tm)
		}
	}
	if _, ok := TermsFor(&cfg, "XAU/USD", nil); ok {
		t.Fatal("no rule for gold")
	}
	mn, mx, mg := 70.0, 85.0, 4.0
	tm, _ := TermsFor(&cfg, "BTC/USD", &models.PayoutConfig{PayoutPercent: 84, MinPayoutPercent: &mn, MaxPayoutPercent: &mx, MarginPercent: &mg})
	if tm.Base != 0.84 || tm.Min != 0.7 || tm.Max != 0.85 || tm.Margin != 0.04 {
		t.Fatalf("row terms %+v", tm)
	}
}

func TestHaltFor(t *testing.T) {
	cfg := models.DefaultRiskConfig()
	if _, _, h := HaltFor(&cfg, "BTC/USD"); h {
		t.Fatal("not halted")
	}
	cfg.PricingHalts = []models.PricingHalt{{Symbol: "BTC/USD", Reason: "exchange outage"}}
	if code, reason, h := HaltFor(&cfg, "BTC/USD"); !h || code != CodeSymbolHalted || reason != "exchange outage" {
		t.Fatal("symbol halt")
	}
	if _, _, h := HaltFor(&cfg, "ETH/USD"); h {
		t.Fatal("other symbol")
	}
	cfg.TradingHalted, cfg.TradingHaltReason = true, "maintenance"
	if code, _, h := HaltFor(&cfg, "ETH/USD"); !h || code != CodeTradingHalted {
		t.Fatal("global halt blocks every symbol")
	}
}

// The instrument rule's stake range narrows the asset's, the configuration's
// and the payout row's limits; 0 leaves them as they are.
func TestStakeBoundsWithTheInstrumentRule(t *testing.T) {
	cfg := models.DefaultRiskConfig() // 10.00 to 1,00,000.00
	asset := &models.Asset{Symbol: "BTC/USD", MinStake: 5_00, MaxStake: 200_000_00}
	i64 := func(v int64) *int64 { return &v }
	cases := []struct {
		name             string
		ruleMin, ruleMax int64
		row              *models.PayoutConfig
		lo, hi, ref      int64
	}{
		{"no rule limits", 0, 0, nil, 10_00, 100_000_00, 10_00},
		{"rule narrows both ends", 50_00, 25_000_00, nil, 50_00, 25_000_00, 50_00},
		{"rule wider than the config changes nothing", 1_00, 500_000_00, nil, 10_00, 100_000_00, 10_00},
		{"payout row narrows further", 50_00, 25_000_00, &models.PayoutConfig{MinStake: i64(100_00), MaxStake: i64(10_000_00)}, 100_00, 10_000_00, 100_00},
		{"only a maximum", 0, 2_000_00, nil, 10_00, 2_000_00, 10_00},
	}
	for _, c := range cases {
		cfg.PricingSymbolRules[0].MinStake, cfg.PricingSymbolRules[0].MaxStake = c.ruleMin, c.ruleMax
		lo, hi := StakeBounds(&cfg, asset, c.row)
		if lo != c.lo || hi != c.hi {
			t.Errorf("%s: bounds %d..%d, want %d..%d", c.name, lo, hi, c.lo, c.hi)
		}
		if ref := ReferenceStake(&cfg, asset, c.row); ref != c.ref {
			t.Errorf("%s: reference stake %d, want %d", c.name, ref, c.ref)
		}
	}
	// Another instrument's rule does not apply.
	cfg.PricingSymbolRules[0].MinStake = 50_00
	if lo, _ := StakeBounds(&cfg, &models.Asset{Symbol: "ETH/USD", MinStake: 5_00, MaxStake: 200_000_00}, nil); lo != 10_00 {
		t.Fatalf("ETH/USD took BTC/USD's minimum: %d", lo)
	}
}

// ── the calibrated guard ────────────────────────────────────────────────

func guardEntry() *models.PricingModelEntry {
	return &models.PricingModelEntry{
		ProviderSymbol: "testusd", ExpirySeconds: 60, PriceSource: "test-feed", Status: models.PricingEntryCalibrated,
		Features:      models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 300, MomentumSteps: 60, MinObservedSteps: 120},
		DrawIntercept: -4, SigmaMin: 1e-5, SigmaMax: 1e-3,
		Regimes: []models.PricingRegime{{Name: "ALL", Uncertainty: 0.015, DrawUncertainty: 0.004}},
	}
}

func guardIn(payout float64) GuardInput {
	return GuardInput{Entry: guardEntry(), Features: Features{Sigma: 2e-4, ObservedSteps: 300}, FeaturesOK: true,
		SourceOK: true, Direction: models.DirectionUp, Payout: payout, DrawPolicy: models.DrawPolicyRefund}
}

func TestGuard(t *testing.T) {
	// q = 1/(1+e^4) = 0.017986..., w = (1-q)/2 = 0.4910069 = k (draws
	// refunded), u = 0.015, dq = 0.004.
	g := Guard(guardIn(0.83))
	if !g.OK || g.Conservative {
		t.Fatalf("83%%: %+v", g)
	}
	w, k := 0.4910068950189542, 0.4910068950189542
	wU, kL := w+0.015, 1-(w+0.015)-(0.01798620996209156+0.004)
	near(t, "prob win", g.ProbWin, w, 1e-12)
	near(t, "point break-even", g.BreakEvenPayout, k/w, 1e-12)
	near(t, "point edge", g.PointEdge, k-w*0.83, 1e-12)
	near(t, "conservative edge", g.ConservativeEdge, kL-wU*0.83, 1e-12)
	// A 1% minimum edge: break-even 0.9796; 0.98 is refused.
	in := guardIn(0.98)
	in.MinEdge = 0.01
	if g := Guard(in); g.OK || g.Reason != ReasonEdgeBelowMinimum {
		t.Fatalf("98%% with 1%% edge: %+v", g)
	}
	in.Payout = 0.975
	if g := Guard(in); !g.OK {
		t.Fatalf("97.5%% with 1%% edge: %+v", g)
	}
	// Conservative mode: break-even (k_lb) / w_ub = 0.9328.
	in = guardIn(0.94)
	in.Conservative = true
	if g := Guard(in); g.OK || g.Reason != ReasonEdgeBelowMinimum {
		t.Fatalf("94%% conservative: %+v", g)
	}
	near(t, "conservative break-even", Guard(in).BreakEvenPayout, kL/wU, 1e-12)
	in.Payout = 0.93
	if g := Guard(in); !g.OK {
		t.Fatalf("93%% conservative: %+v", g)
	}
	for name, mutate := range map[string]func(*GuardInput){
		"no model":       func(g *GuardInput) { g.Entry = nil },
		"not calibrated": func(g *GuardInput) { g.Entry.Status = models.PricingEntryInsufficientData },
		"source":         func(g *GuardInput) { g.SourceOK = false },
		"thin data":      func(g *GuardInput) { g.Features.ObservedSteps = 100 },
		"no features":    func(g *GuardInput) { g.FeaturesOK = false },
		"NaN":            func(g *GuardInput) { g.Entry.Intercept = math.NaN() },
		"extreme vol":    func(g *GuardInput) { g.Features.Sigma = 1e-2 },
	} {
		in := guardIn(0.80)
		mutate(&in)
		if g := Guard(in); g.OK || g.Reason == "" {
			t.Fatalf("%s: %+v", name, g)
		}
	}
}

func TestDefaultHedgeEstimate(t *testing.T) {
	cfg := models.DefaultRiskConfig()
	asset := &models.Asset{Symbol: "BTC/USD", Category: models.AssetCategoryCrypto, HedgeEnabled: true}
	req := HedgeCostRequest{Asset: asset, Direction: models.DirectionUp, Stake: 100_00, ExpirySeconds: 60, Price: 100, Config: &cfg}
	e := defaultEstimate(req, &config.Env{HedgeProvider: "paper"})
	// Binance basis 2 x (5 + 1 + 2) bps; the charged cost is the configured one.
	if !e.Required || !e.Available || e.CostPoints != 0 || math.Abs(e.VenueCostFraction-0.0016) > 1e-12 {
		t.Fatalf("paper %+v", e)
	}
	if e := defaultEstimate(req, &config.Env{HedgeProvider: "binance"}); e.Available || e.Reason != "HEDGE_NOT_CONFIGURED" {
		t.Fatalf("no keys %+v", e)
	}
	keys := &config.Env{HedgeProvider: "binance", HedgeAPIKey: "k", HedgeAPISecret: "s", HedgeBinanceEnv: "live"}
	if e := defaultEstimate(req, keys); e.Available || e.Reason != "LIVE_EXECUTION_DISABLED" {
		t.Fatalf("live disabled %+v", e)
	}
	keys.HedgeLiveExecutionEnabled = true
	if e := defaultEstimate(req, keys); !e.Available {
		t.Fatalf("live %+v", e)
	}
	keys.HedgeBinanceEnv, keys.HedgeLiveExecutionEnabled = "testnet", false
	if e := defaultEstimate(req, keys); !e.Available {
		t.Fatalf("testnet %+v", e)
	}
	fx := *asset
	fx.Category = models.AssetCategoryForex
	req.Asset = &fx
	if e := defaultEstimate(req, nil); e.Available || e.Reason != "NO_HEDGE_VENUE" {
		t.Fatalf("forex %+v", e)
	}
	off := *asset
	off.HedgeEnabled = false
	req.Asset = &off
	if e := defaultEstimate(req, nil); e.Required {
		t.Fatalf("unhedged asset %+v", e)
	}
}

func TestSourceAllowed(t *testing.T) {
	saved := config.E
	defer func() { config.E = saved }()
	config.E = &config.Env{NodeEnv: "production"}
	if ok, mm := SourceAllowed("binance", "binance"); !ok || mm {
		t.Fatal("same source")
	}
	if ok, _ := SourceAllowed("binance", "simulated"); ok {
		t.Fatal("production must not vouch for a simulated feed with a market model")
	}
	config.E = &config.Env{NodeEnv: "production", AllowSimulatedFeedInProduction: true}
	if ok, mm := SourceAllowed("binance", "simulated"); !ok || !mm {
		t.Fatal("explicitly allowed simulated feed")
	}
	config.E = &config.Env{NodeEnv: "development"}
	if ok, mm := SourceAllowed("binance", "simulated"); !ok || !mm {
		t.Fatal("development")
	}
}

func TestEmbeddedModel(t *testing.T) {
	m, err := EmbeddedModel()
	if err != nil {
		t.Fatal(err)
	}
	if m.Version < 1 || m.Hash == "" {
		t.Fatalf("version %d hash %q", m.Version, m.Hash)
	}
	for _, sym := range []string{"btcusdt", "ethusdt"} {
		for _, exp := range []int64{30, 60, 300, 900, 1800} {
			if e := FindEntry(m, sym, exp); e == nil || e.Status != models.PricingEntryCalibrated || e.PriceSource != "binance" {
				t.Fatalf("%s %d: %+v", sym, exp, e)
			}
		}
	}
	for _, sym := range []string{"eurusd", "gbpusd", "xauusd"} {
		if e := FindEntry(m, sym, 60); e == nil || e.Status != models.PricingEntryNotPriced {
			t.Fatalf("%s must be NOT_PRICED: %+v", sym, e)
		}
	}
}

// Launch scope: only BTC/USD and ETH/USD are priced; the apps hide the rest.
func TestSymbolPricedLaunchScope(t *testing.T) {
	cfg := models.DefaultRiskConfig()
	for sym, want := range map[string]bool{"BTC/USD": true, "ETH/USD": true, "EUR/USD": false, "GBP/USD": false, "XAU/USD": false} {
		got, reason := SymbolPriced(&cfg, sym)
		if got != want || (!got && reason == "") {
			t.Errorf("%s: priced %v reason %q", sym, got, reason)
		}
	}
}
