package pricing

import (
	"math"
	"sort"

	"binarytrading/internal/models"
)

// Back-test and margin evidence for the payout formula (audit F-02, F-03;
// specification section 15). Pure, so cmd/calibrate and the tests share it.
//
// The formula's payout r falls as volatility, spread and exposure rise, and
// the platform's expected result per unit stake with draws refunded,
//
//	E(r) = (1 - p_win - p_draw) - p_win * r,
//
// falls as r rises. So for every regime two figures are reported: E at the
// payouts the engine actually offered over the period (EWMA run exactly as
// live), and E at the highest payout the formula can offer in a calm market
// (vol_adj = exposure_adj = 0) - if the latter is positive, every quote in
// that regime has a positive expected result.

// EWMASigmas runs the live estimator (VolState semantics: one observation
// per step at the series' resolution, var0 = baseline^2) and returns sigma
// after each step, as float32 to keep long series small.
func EWMASigmas(s *Series, baseline, lambda float64) []float32 {
	out := make([]float32, s.Len())
	v := baseline * baseline
	prev := math.NaN()
	dt := float64(s.StepMs) / 1000
	for i, c := range s.ForwardFilled() {
		if !math.IsNaN(prev) && !math.IsNaN(c) && prev > 0 {
			r := math.Log(c / prev)
			v = lambda*v + (1-lambda)*r*r/dt
		}
		if !math.IsNaN(c) {
			prev = c
		}
		out[i] = float32(math.Sqrt(v))
	}
	return out
}

// BacktestConfig is one (instrument, expiry) back-test on a 1-second series.
type BacktestConfig struct {
	Rules         Rules
	Terms         Terms
	ExpirySeconds int64
	StrideSteps   int
	// Steps [FromStep, ToStep) of the series are sampled.
	FromStep, ToStep int
	// Price tick of the instrument; the spread is assumed to be one tick
	// (klines carry no book).
	TickSize float64
	Stake    int64
}

// RegimeOutcome is the evidence for one volatility regime.
type RegimeOutcome struct {
	Regime    string  `json:"regime"`
	RatioFrom float64 `json:"volRatioFrom"`
	RatioTo   float64 `json:"volRatioTo"`
	Samples   int     `json:"samples"`
	Effective float64 `json:"effective"`
	Blocked   int     `json:"blocked"`
	// Offered payouts, percent.
	MeanPayout float64 `json:"meanPayoutPercent"`
	MinPayout  float64 `json:"minPayoutPercent"`
	MaxPayout  float64 `json:"maxPayoutPercent"`
	AtMinimum  int     `json:"atMinimum"`
	// Empirical probabilities over the offered samples.
	WinUp   float64 `json:"winUp"`
	WinDown float64 `json:"winDown"`
	Draw    float64 `json:"draw"`
	// Expected result per unit stake, E = (1 - p - q) - p * mean(r), and
	// the realised mean result, per direction bought, percent of stake.
	ExpectedUpPct   float64 `json:"expectedUpPercent"`
	ExpectedDownPct float64 `json:"expectedDownPercent"`
	RealisedUpPct   float64 `json:"realisedUpPercent"`
	RealisedDownPct float64 `json:"realisedDownPercent"`
	// 95% half-width of the realised means at the effective sample size.
	HalfWidthPct float64 `json:"halfWidthPercent"`
	// RatioTo 0 = unbounded. E at the calm-market (highest) payout the
	// formula can offer.
	CalmPayout        float64 `json:"calmPayoutPercent"`
	CalmExpectedUpPct float64 `json:"calmExpectedUpPercent"`
	CalmExpectedDnPct float64 `json:"calmExpectedDownPercent"`
	// Every expected figure above is positive.
	Positive bool `json:"positive"`
}

// BacktestResult is one (instrument, expiry).
type BacktestResult struct {
	ExpirySeconds int64           `json:"expirySeconds"`
	Samples       int             `json:"samples"`
	Blocked       int             `json:"blocked"`
	BlockRate     float64         `json:"blockRate"`
	AtMinimum     int             `json:"atMinimum"`
	MeanPayout    float64         `json:"meanPayoutPercent"`
	Regimes       []RegimeOutcome `json:"regimes"`
	AllPositive   bool            `json:"allPositive"`
}

type btSample struct {
	ratio  float64
	payout float64 // 0 = blocked
	up     bool
	draw   bool
}

// CalmPayout is the highest payout the formula offers for these terms: vol
// at baseline (no vol_adj), the given spread, empty book.
func CalmPayout(r Rules, t Terms, expirySeconds int64, spreadBps float64) float64 {
	res := ComputeOffer(r, t, OfferInput{Direction: models.DirectionUp, ExpirySeconds: expirySeconds, Stake: 1,
		Sigma: t.BaselineVol, SpreadBps: spreadBps, Hedge: HedgeCostEstimate{Available: true}})
	return res.Components.Payout
}

// Backtest prices every sampled second with the formula (empty book, hedge
// available) at the EWMA sigma of that second and books the real outcome.
func Backtest(s *Series, sigmas []float32, c BacktestConfig) BacktestResult {
	h := int(c.ExpirySeconds * 1000 / s.StepMs)
	filled := s.ForwardFilled()
	stride := max(1, c.StrideSteps)
	out := BacktestResult{ExpirySeconds: c.ExpirySeconds, Regimes: []RegimeOutcome{}}
	var samples []btSample
	for k := max(c.FromStep, 1); k+h < min(c.ToStep, s.Len()); k += stride {
		entry, exit := filled[k], filled[k+h]
		if math.IsNaN(entry) || math.IsNaN(exit) || !(entry > 0) {
			continue
		}
		spread := c.TickSize / entry * 1e4
		sigma := float64(sigmas[k])
		res := ComputeOffer(c.Rules, c.Terms, OfferInput{Direction: models.DirectionUp, ExpirySeconds: c.ExpirySeconds,
			Stake: c.Stake, Sigma: sigma, SpreadBps: spread, Hedge: HedgeCostEstimate{Available: true}})
		bs := btSample{ratio: sigma / c.Terms.BaselineVol, up: exit > entry, draw: exit == entry}
		if res.OK {
			bs.payout = res.Components.Payout
		}
		samples = append(samples, bs)
	}
	out.Samples = len(samples)
	if len(samples) == 0 {
		return out
	}
	overlap := math.Min(1, float64(stride)/float64(h))
	ratios := make([]float64, 0, len(samples))
	var sumPay float64
	offered := 0
	for _, b := range samples {
		if b.payout == 0 {
			out.Blocked++
			continue
		}
		ratios = append(ratios, b.ratio)
		sumPay += b.payout
		offered++
		if b.payout <= c.Terms.Min+1e-12 {
			out.AtMinimum++
		}
	}
	out.BlockRate = float64(out.Blocked) / float64(len(samples))
	if offered > 0 {
		out.MeanPayout = PayoutPercent(sumPay / float64(offered))
	}
	sort.Float64s(ratios)
	t1, t2 := Quantile(ratios, 1.0/3), Quantile(ratios, 2.0/3)
	bounds := [][2]float64{{0, t1}, {t1, t2}, {t2, math.Inf(1)}}
	names := []string{"LOW", "MID", "HIGH"}
	calm := CalmPayout(c.Rules, c.Terms, c.ExpirySeconds, c.TickSize/filled[len(filled)-1]*1e4)
	out.AllPositive = true
	for ri, name := range names {
		ro := RegimeOutcome{Regime: name, RatioFrom: bounds[ri][0], RatioTo: bounds[ri][1], MinPayout: math.Inf(1),
			CalmPayout: PayoutPercent(calm)}
		var ups, downs, draws, sumR, sumUp, sumUp2, sumDn, sumDn2 float64
		for _, b := range samples {
			in := b.ratio > bounds[ri][0] && b.ratio <= bounds[ri][1]
			if ri == 0 {
				in = b.ratio <= bounds[0][1]
			}
			if !in {
				continue
			}
			if b.payout == 0 {
				ro.Blocked++
				continue
			}
			ro.Samples++
			sumR += b.payout
			ro.MinPayout = math.Min(ro.MinPayout, b.payout)
			ro.MaxPayout = math.Max(ro.MaxPayout, b.payout)
			if b.payout <= c.Terms.Min+1e-12 {
				ro.AtMinimum++
			}
			// Platform result per unit stake when the customer bought UP /
			// DOWN: -r on a customer win, +1 on a loss, 0 on a refunded draw.
			var rUp, rDn float64
			switch {
			case b.draw:
				draws++
			case b.up:
				ups++
				rUp, rDn = -b.payout, 1
			default:
				downs++
				rUp, rDn = 1, -b.payout
			}
			sumUp += rUp
			sumUp2 += rUp * rUp
			sumDn += rDn
			sumDn2 += rDn * rDn
		}
		if math.IsInf(ro.RatioTo, 1) {
			ro.RatioTo = 0 // unbounded (JSON has no infinity)
		}
		if ro.Samples == 0 {
			ro.MinPayout = 0
			out.Regimes = append(out.Regimes, ro)
			continue
		}
		n := float64(ro.Samples)
		ro.Effective = n * overlap
		ro.WinUp, ro.WinDown, ro.Draw = ups/n, downs/n, draws/n
		meanR := sumR / n
		ro.MeanPayout, ro.MinPayout, ro.MaxPayout = PayoutPercent(meanR), PayoutPercent(ro.MinPayout), PayoutPercent(ro.MaxPayout)
		ro.ExpectedUpPct = ((1 - ro.WinUp - ro.Draw) - ro.WinUp*meanR) * 100
		ro.ExpectedDownPct = ((1 - ro.WinDown - ro.Draw) - ro.WinDown*meanR) * 100
		ro.RealisedUpPct, ro.RealisedDownPct = sumUp/n*100, sumDn/n*100
		sd := math.Max(math.Sqrt(math.Max(0, sumUp2/n-(sumUp/n)*(sumUp/n))), math.Sqrt(math.Max(0, sumDn2/n-(sumDn/n)*(sumDn/n))))
		ro.HalfWidthPct = 1.96 * sd / math.Sqrt(math.Max(1, ro.Effective)) * 100
		ro.CalmExpectedUpPct = ((1 - ro.WinUp - ro.Draw) - ro.WinUp*calm) * 100
		ro.CalmExpectedDnPct = ((1 - ro.WinDown - ro.Draw) - ro.WinDown*calm) * 100
		ro.Positive = ro.ExpectedUpPct > 0 && ro.ExpectedDownPct > 0 && ro.CalmExpectedUpPct > 0 && ro.CalmExpectedDnPct > 0
		out.AllPositive = out.AllPositive && ro.Positive
		out.Regimes = append(out.Regimes, ro)
	}
	return out
}

// HorizonEvidence is the long-history check for one (instrument, expiry):
// per volatility tercile of the sample features, the empirical win and draw
// rates and E at the calm-market payout, plus how often the calibrated
// guard would refuse that payout.
type HorizonEvidence struct {
	ExpirySeconds int64           `json:"expirySeconds"`
	CalmPayout    float64         `json:"calmPayoutPercent"`
	Regimes       []RegimeOutcome `json:"regimes"`
	GuardChecked  int             `json:"guardChecked"`
	GuardBlocked  int             `json:"guardBlocked"`
	AllPositive   bool            `json:"allPositive"`
}

// LongHorizonEvidence evaluates samples (test period of a calibration) at
// the calm payout, with the guard of entry (nil = no guard check).
func LongHorizonEvidence(samples []Sample, calm float64, overlap float64, entry *models.PricingModelEntry, drawPolicy models.DrawPolicy, minEdge float64, conservative bool) HorizonEvidence {
	out := HorizonEvidence{CalmPayout: PayoutPercent(calm), Regimes: []RegimeOutcome{}, AllPositive: true}
	if len(samples) == 0 {
		out.AllPositive = false
		return out
	}
	sig := make([]float64, len(samples))
	for i, s := range samples {
		sig[i] = s.Features.Sigma
	}
	sort.Float64s(sig)
	t1, t2 := Quantile(sig, 1.0/3), Quantile(sig, 2.0/3)
	names := []string{"LOW", "MID", "HIGH"}
	for ri, name := range names {
		ro := RegimeOutcome{Regime: name, CalmPayout: PayoutPercent(calm)}
		var ups, downs, draws float64
		for _, s := range samples {
			x := s.Features.Sigma
			if (ri == 0 && x > t1) || (ri == 1 && (x <= t1 || x > t2)) || (ri == 2 && x <= t2) {
				continue
			}
			ro.Samples++
			switch {
			case s.Draw:
				draws++
			case s.Up:
				ups++
			default:
				downs++
			}
			if entry != nil {
				for _, d := range []models.Direction{models.DirectionUp, models.DirectionDown} {
					g := Guard(GuardInput{Entry: entry, Features: withObserved(s.Features, entry), FeaturesOK: true, SourceOK: true,
						Direction: d, Payout: calm, DrawPolicy: drawPolicy, MinEdge: minEdge, Conservative: conservative})
					out.GuardChecked++
					if !g.OK {
						out.GuardBlocked++
					}
				}
			}
		}
		if ro.Samples > 0 {
			n := float64(ro.Samples)
			ro.Effective = n * overlap
			ro.WinUp, ro.WinDown, ro.Draw = ups/n, downs/n, draws/n
			ro.CalmExpectedUpPct = ((1 - ro.WinUp - ro.Draw) - ro.WinUp*calm) * 100
			ro.CalmExpectedDnPct = ((1 - ro.WinDown - ro.Draw) - ro.WinDown*calm) * 100
			p := math.Max(ro.WinUp, ro.WinDown)
			ro.HalfWidthPct = 1.96 * math.Sqrt(p*(1-p)/math.Max(1, ro.Effective)) * (1 + calm) * 100
			ro.Positive = ro.CalmExpectedUpPct > 0 && ro.CalmExpectedDnPct > 0
		}
		out.AllPositive = out.AllPositive && ro.Positive
		out.Regimes = append(out.Regimes, ro)
	}
	return out
}

// withObserved marks a calibration sample's window as fully observed for
// the guard (BuildSamples only keeps samples whose window qualified).
func withObserved(f Features, e *models.PricingModelEntry) Features {
	f.ObservedSteps = max(f.ObservedSteps, e.Features.WindowSteps)
	return f
}
