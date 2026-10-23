package pricing

import (
	"math"
	"math/rand/v2"
	"testing"

	"binarytrading/internal/models"
)

// syntheticSeries is TEST DATA ONLY: a driftless Gaussian random walk on a
// 0.01 tick grid. It checks that the calibration recovers a known process; it
// is never a model for a real market.
func syntheticSeries(n int, sigma float64, seed uint64) *Series {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e37))
	s := &Series{StartMs: 1_700_000_000_000, StepMs: 1000, Close: make([]float64, n), Trades: make([]int32, n)}
	logp := math.Log(100)
	for i := 0; i < n; i++ {
		logp += rng.NormFloat64() * sigma
		s.Close[i] = math.Round(math.Exp(logp)*100) / 100
		s.Trades[i] = 1
	}
	return s
}

func TestFitLogisticRecoversParameters(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 5))
	const a, b = 0.3, -0.8
	x := make([]float64, 40_000)
	y := make([]bool, len(x))
	for i := range x {
		x[i] = rng.NormFloat64()
		y[i] = rng.Float64() < sigmoid(a+b*x[i])
	}
	ga, gb, ok := FitLogistic(x, y, true, true, 1)
	if !ok || math.Abs(ga-a) > 0.05 || math.Abs(gb-b) > 0.05 {
		t.Fatalf("fit a=%v b=%v ok=%v", ga, gb, ok)
	}
	X := make([][]float64, len(x))
	for i := range x {
		X[i] = []float64{x[i]}
	}
	beta, ok := FitLogisticN(X, y, 1)
	if !ok || math.Abs(beta[0]-ga) > 1e-6 || math.Abs(beta[1]-gb) > 1e-6 {
		t.Fatalf("FitLogisticN %v vs %v %v", beta, ga, gb)
	}
	if _, b0, _ := FitLogistic(x, y, true, false, 0); b0 != 0 {
		t.Fatal("slope pinned at 0")
	}
}

func TestCalibrateEntryOnSyntheticRandomWalk(t *testing.T) {
	s := syntheticSeries(260_000, 3e-4, 42)
	spec := CalibrationSpec{
		ProviderSymbol: "synth", Symbol: "SYN/TEST", ExpirySeconds: 30, PriceSource: "synthetic-test",
		Features:    models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 300, MomentumSteps: 60, MinObservedSteps: 120, DrawLagSteps: 30},
		StrideSteps: 10,
	}
	det := CalibrateEntry(s, spec)
	e := det.Entry
	if e.Status != models.PricingEntryCalibrated {
		t.Fatalf("status %s: %s", e.Status, e.StatusReason)
	}
	// A driftless walk: P(UP | no draw) close to 1/2 and no useful momentum.
	if p := sigmoid(e.Intercept); math.Abs(p-0.5) > 0.01 || math.Abs(e.MomentumCoef) > 0.05 {
		t.Fatalf("a=%v b=%v", e.Intercept, e.MomentumCoef)
	}
	// Out-of-sample metrics are present and no better than the truth allows.
	m := e.Metrics
	if m.TrainSamples == 0 || m.HoldoutSamples == 0 || m.TestSamples == 0 || !(m.TrainTo < m.HoldoutFrom && m.HoldoutTo < m.TestFrom) {
		t.Fatalf("periods %+v", m)
	}
	if math.Abs(m.BrierTest-m.BrierBaselineTest) > 0.002 || len(det.Test.Reliability) != 10 {
		t.Fatalf("brier %v baseline %v bins %d", m.BrierTest, m.BrierBaselineTest, len(det.Test.Reliability))
	}
	for _, r := range e.Regimes {
		if !(r.Uncertainty > 0 && r.Uncertainty < 0.1) || r.DrawUncertainty < 0 {
			t.Fatalf("regime %+v", r)
		}
	}
	// The margin evidence on the same synthetic walk: the formula's payouts
	// (EWMA run as live) leave a positive expected result in every regime, and
	// the guard vouches for the calm-market payout.
	cfg := models.DefaultRiskConfig()
	r := RulesFrom(&cfg)
	terms := Terms{Symbol: "SYN/TEST", Enabled: true, Base: 0.86, Min: 0.65, Max: 0.88, Margin: 0.035,
		BaselineVol: 3e-4, HedgeCost: 0.035, MaxExposure: 1_000_000}
	sig := EWMASigmas(s, terms.BaselineVol, cfg.PricingEwmaLambda)
	bt := Backtest(s, sig, BacktestConfig{Rules: r, Terms: terms, ExpirySeconds: 30, StrideSteps: 10,
		FromStep: s.Len() * 4 / 5, ToStep: s.Len(), TickSize: 0.01, Stake: 100})
	if bt.Samples < 1000 || !bt.AllPositive || len(bt.Regimes) != 3 {
		t.Fatalf("backtest %+v", bt)
	}
	calm := CalmPayout(r, terms, 30, 0.01)
	ev := LongHorizonEvidence(det.TestSamples, calm, spec.OverlapFactor(), &e, models.DrawPolicyRefund, 0.01, false)
	if !ev.AllPositive || ev.GuardChecked == 0 || ev.GuardBlocked > ev.GuardChecked/10 {
		t.Fatalf("evidence %+v", ev)
	}
}

func TestCalibrateEntryRefusesShortHistory(t *testing.T) {
	s := syntheticSeries(5_000, 3e-4, 1)
	spec := CalibrationSpec{
		ProviderSymbol: "synth", Symbol: "SYN/TEST", ExpirySeconds: 60, PriceSource: "synthetic-test",
		Features:    models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 300, MomentumSteps: 60, MinObservedSteps: 120},
		StrideSteps: 10,
	}
	if e := CalibrateEntry(s, spec).Entry; e.Status != models.PricingEntryInsufficientData || e.StatusReason == "" {
		t.Fatalf("short history: %s %q", e.Status, e.StatusReason)
	}
	spec.ExpirySeconds = 90
	spec.Features.ResolutionSeconds = 60
	if e := CalibrateEntry(s, spec).Entry; e.Status != models.PricingEntryInsufficientData {
		t.Fatal("an expiry that is not a whole number of steps must not calibrate")
	}
}

func TestWilsonAndQuantile(t *testing.T) {
	if u := WilsonUpper(0, 100); !(u > 0.03 && u < 0.04) {
		t.Fatalf("wilson(0/100) = %v", u)
	}
	if u := WilsonUpper(50, 100); !(u > 0.59 && u < 0.61) {
		t.Fatalf("wilson(50/100) = %v", u)
	}
	if q := Quantile([]float64{1, 2, 3, 4}, 0.5); q != 2.5 {
		t.Fatalf("median %v", q)
	}
}
