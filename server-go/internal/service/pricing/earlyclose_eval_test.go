package pricing

import (
	"math"
	"math/rand/v2"
	"testing"

	"binarytrading/internal/models"
)

// The live estimator: sample standard deviation of tick log returns,
// rescaled by sqrt(max(0.1, ticks / 60)); 0 below three ticks or without
// movement.
func TestEarlyCloseVolatility(t *testing.T) {
	prices := []float64{100, 101, 100, 101}
	r := []float64{math.Log(1.01), math.Log(100.0 / 101), math.Log(1.01)}
	mean := (r[0] + r[1] + r[2]) / 3
	var acc float64
	for _, x := range r {
		acc += (x - mean) * (x - mean)
	}
	want := math.Sqrt(acc/2) * math.Sqrt(0.1)
	if got := EarlyCloseVolatility(prices); math.Abs(got-want) > 1e-15 {
		t.Fatalf("got %v want %v", got, want)
	}
	// 120 ticks in the window: the rescale is sqrt(2).
	many := make([]float64, 120)
	for i := range many {
		many[i] = 100 + float64(i%2)
	}
	per := EarlyCloseVolatility(many) / math.Sqrt(2)
	if math.Abs(per-EarlyCloseVolatility(many[:60])) > 0.01*per {
		t.Fatalf("rescale by the tick rate: %v vs %v", per, EarlyCloseVolatility(many[:60]))
	}
	for _, p := range [][]float64{nil, {100, 101}, {100, 100, 100, 100}} {
		if v := EarlyCloseVolatility(p); v != 0 {
			t.Fatalf("%v: %v, want 0", p, v)
		}
	}
}

// randomWalk is a 1-second series of a driftless Gaussian walk with
// volatility sigma per sqrt(second) and a trade every second.
func randomWalk(n int, sigma float64, seed uint64) *Series {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	s := &Series{StartMs: 0, StepMs: 1000, Close: make([]float64, n), Trades: make([]int32, n)}
	p := 100.0
	for i := range s.Close {
		p *= math.Exp(sigma * rng.NormFloat64())
		s.Close[i] = p
		s.Trades[i] = 1
	}
	return s
}

func phiValuer(c EarlyCloseCase) (float64, int64) {
	move := c.CurrentPrice - c.EntryPrice
	if c.Direction != models.DirectionUp {
		move = -move
	}
	z := move / (c.CurrentPrice * c.VolatilityPerSecond * math.Sqrt(c.SecondsRemaining))
	p := math.Min(0.99, math.Max(0.01, 0.5*(1+math.Erf(z/math.Sqrt2))))
	full := float64(c.Stake) * (1 + c.PayoutPercent/100)
	return p, int64(math.Floor(full*p*(1-c.FeePercent/100) + 0.5))
}

// On a random walk valued with its true volatility, Phi(z) is calibrated:
// the evaluator must find a small calibration error, an edge before the fee
// of about zero, and the fee as the edge after it.
func TestEarlyCloseBacktestOnARandomWalk(t *testing.T) {
	const sigma = 5e-5
	s := randomWalk(400_000, sigma, 7)
	res := EarlyCloseBacktest(s, EarlyCloseBacktestConfig{
		Symbol: "RW", ExpirySeconds: 60, RemainingSeconds: []int64{45, 30, 15, 10}, StrideSteps: 10,
		FromStep: 60, SplitStep: 240_000, ToStep: s.Len(), PayoutPercent: 80, FeePercent: 5, Stake: 1000_00,
		Sigma: func(int) float64 { return sigma }, RegimeCuts: [2]float64{sigma / 2, sigma * 2}, Value: phiValuer,
	})
	c := res.Cell(ECPeriodTest, 0, ECRegimeAll)
	if c == nil || res.FitEntries == 0 || res.TestEntries == 0 || res.Refused != 0 {
		t.Fatalf("result %+v", res)
	}
	if c.ECE > 0.02 || math.Abs(c.EdgeBeforeFeePct) > 1.5 || c.DrawRate != 0 {
		t.Fatalf("calibrated walk: ECE %.4f, edge before fee %.2f%%, draws %.4f", c.ECE, c.EdgeBeforeFeePct, c.DrawRate)
	}
	// The fee is 5% of the value before it (the value paid is 95% of it).
	if want := c.MeanValuePct * 0.05 / 0.95; math.Abs(c.EdgePct-c.EdgeBeforeFeePct-want) > 0.05 {
		t.Fatalf("edge %.3f, before fee %.3f, fee share of value %.3f", c.EdgePct, c.EdgeBeforeFeePct, want)
	}
	for _, b := range c.Reliability {
		if b.Effective > 200 && math.Abs(b.MeanPredicted-b.Observed) > b.HalfWidth+0.02 {
			t.Fatalf("bin %+v off the diagonal", b)
		}
	}
}

// The fit recovers the stickiness and the scale of data generated from the
// corrected model, and leaves a cell with too few samples at the identity.
func TestFitEarlyCloseCalibrationRecoversParameters(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	truth := models.EarlyCloseCalibrationRow{SigmaScale: 2, Stickiness: 0.6}
	var samples []EarlyCloseSample
	for i := 0; i < 40_000; i++ {
		z := rng.NormFloat64() * 1.5
		up := rng.Float64() < EarlyCloseWinProbability(z, 0.5, truth)
		samples = append(samples, EarlyCloseSample{Z: float32(z), Remaining: 10, VolRatio: 0.5, Up: up, Down: !up})
	}
	samples = append(samples, EarlyCloseSample{Z: 1, Remaining: 1000, VolRatio: 0.5, Up: true})
	fit := FitEarlyCloseCalibration(samples, []int64{15, 0}, []float64{0}, 1000, 0.01, []float64{0.01})
	if len(fit.Cells) != 2 {
		t.Fatalf("cells %+v", fit.Cells)
	}
	got := fit.Cells[0]
	if math.Abs(got.Stickiness-0.6) > 0.05 || math.Abs(got.SigmaScale-2) > 0.5 || got.LogLoss >= got.LogLossBefore {
		t.Fatalf("fitted %+v, want scale 2 stickiness 0.6", got)
	}
	if id := fit.Cells[1]; id.SigmaScale != 1 || id.Stickiness != 0 || id.MinVolRatio != 0 {
		t.Fatalf("a thin cell must keep the identity: %+v", id)
	}
}

// The corrected probability: the identity row is Phi(z); the floor replaces
// a scaled sigma below MinVolRatio x baseline; stickiness moves the
// probability towards the current side; at the entry it stays 0.5.
func TestEarlyCloseWinProbability(t *testing.T) {
	phi := func(z float64) float64 { return 0.5 * (1 + math.Erf(z/math.Sqrt2)) }
	id := models.EarlyCloseCalibrationRow{}
	if p := EarlyCloseWinProbability(0.7, 0.5, id); math.Abs(p-phi(0.7)) > 1e-15 {
		t.Fatalf("identity %v", p)
	}
	floor := models.EarlyCloseCalibrationRow{SigmaScale: 1, MinVolRatio: 1}
	if p := EarlyCloseWinProbability(2, 0.25, floor); math.Abs(p-phi(0.5)) > 1e-15 {
		t.Fatalf("floor: sigma at a quarter of baseline is raised to the baseline, z/4: %v", p)
	}
	if p := EarlyCloseWinProbability(2, 0, floor); math.Abs(p-phi(2)) > 1e-15 {
		t.Fatalf("unknown baseline: no floor: %v", p)
	}
	st := models.EarlyCloseCalibrationRow{Stickiness: 0.5}
	if p := EarlyCloseWinProbability(-0.1, 1, st); math.Abs(p-0.5*phi(-0.1)) > 1e-15 {
		t.Fatalf("out of the money %v", p)
	}
	if p := EarlyCloseWinProbability(0.1, 1, st); math.Abs(p-(0.5+0.5*phi(0.1))) > 1e-15 {
		t.Fatalf("in the money %v", p)
	}
	if p := EarlyCloseWinProbability(0, 1, st); p != 0.5 {
		t.Fatalf("at the entry %v", p)
	}
	rows := SortedEarlyCloseRows([]models.EarlyCloseCalibrationRow{
		{MaxSecondsRemaining: 0, MaxVolRatio: 0}, {MaxSecondsRemaining: 60, MaxVolRatio: 0},
		{MaxSecondsRemaining: 15, MaxVolRatio: 0.7}, {MaxSecondsRemaining: 15, MaxVolRatio: 0.33},
	})
	want := [][2]float64{{15, 0.33}, {15, 0.7}, {60, 0}, {0, 0}}
	for i, r := range rows {
		if float64(r.MaxSecondsRemaining) != want[i][0] || r.MaxVolRatio != want[i][1] {
			t.Fatalf("order %+v", rows)
		}
	}
}
