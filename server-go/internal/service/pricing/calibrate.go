package pricing

import (
	"math"
	"sort"
	"strconv"

	"binarytrading/internal/models"
)

// Calibration (audit finding F-02): turns a historical price series into a
// model entry, with out-of-sample metrics. Pure, so cmd/calibrate and the
// tests run the same code. Method (spec section 4):
//
//  1. Samples: at every StrideSteps-th step k the features are computed from
//     the window before k exactly as the live engine does (ComputeFeatures),
//     the entry is the close at k and the outcome the close h = expiry /
//     resolution steps later (UP if higher, DOWN if lower, DRAW if equal).
//  2. Three periods in time order: train (first TrainFraction), holdout
//     (next HoldoutFraction) and test (the rest). Parameters come from train
//     only, the uncertainty add-ons from holdout only, and every reported
//     metric and the margin simulation from test only.
//  3. Within train, the last ValidationFraction chooses the form (drift
//     intercept and momentum slope each in or out, by validation log loss);
//     the chosen form is refitted on the whole train set (IRLS logistic
//     regression, ridge on the slope).
//     The draw probability is a logistic regression on ln(sigma), also on
//     train.
//  4. Volatility regimes are the training terciles of sigma. Per regime, on
//     holdout: the win add-on u (worst one-sided bias over directions and
//     the subsets a customer can select, plus 1.96 standard errors at the
//     effective sample size) and the draw add-on dq (one-sided bias plus
//     1.96 standard errors).
//  5. Status CALIBRATED only with enough training, holdout and test samples
//     in total and enough per regime; otherwise INSUFFICIENT_DATA and the
//     engine refuses that asset and expiry.

// Sample is one historical entry and its outcome.
type Sample struct {
	T        int64
	Features Features
	Up       bool
	Draw     bool
}

// CalibrationSpec configures one entry.
type CalibrationSpec struct {
	ProviderSymbol string
	Symbol         string
	ExpirySeconds  int64
	PriceSource    string
	Features       models.PricingFeatureSpec
	StrideSteps    int
	// Time split; defaults 0.6 train, 0.2 holdout (test = the rest), and
	// 0.2 of train for the momentum validation.
	TrainFraction      float64
	HoldoutFraction    float64
	ValidationFraction float64
	MinTrainSamples    int
	MinTestEffective   float64
	// Per regime.
	MinRegimeTrain         int
	MinRegimeTestEffective float64
}

func (c *CalibrationSpec) defaults() {
	if c.TrainFraction <= 0 || c.TrainFraction >= 1 {
		c.TrainFraction = 0.6
	}
	if c.HoldoutFraction <= 0 || c.TrainFraction+c.HoldoutFraction >= 1 {
		c.HoldoutFraction = 0.2
	}
	if c.ValidationFraction <= 0 || c.ValidationFraction >= 1 {
		c.ValidationFraction = 0.2
	}
	if c.StrideSteps <= 0 {
		c.StrideSteps = 1
	}
	if c.MinTrainSamples <= 0 {
		c.MinTrainSamples = 2000
	}
	if c.MinTestEffective <= 0 {
		c.MinTestEffective = 200
	}
	if c.MinRegimeTrain <= 0 {
		c.MinRegimeTrain = 300
	}
	if c.MinRegimeTestEffective <= 0 {
		c.MinRegimeTestEffective = 50
	}
}

// HorizonSteps is the expiry in grid steps; 0 when it is not a whole number
// of steps.
func (c *CalibrationSpec) HorizonSteps() int {
	res := c.Features.ResolutionSeconds
	if res <= 0 || c.ExpirySeconds%res != 0 {
		return 0
	}
	return int(c.ExpirySeconds / res)
}

// OverlapFactor scales a sample count to effective independent outcomes:
// samples StrideSteps apart with outcomes h steps long share min(1, stride/h)
// of their information.
func (c *CalibrationSpec) OverlapFactor() float64 {
	h := c.HorizonSteps()
	if h <= 0 {
		return 1
	}
	return math.Min(1, float64(c.StrideSteps)/float64(h))
}

// BuildSamples walks the series. excluded counts steps skipped because the
// window could not support features or the expiry had no recent trade.
func BuildSamples(s *Series, spec CalibrationSpec) ([]Sample, int) {
	spec.defaults()
	h := spec.HorizonSteps()
	if h <= 0 {
		return nil, 0
	}
	filled := s.ForwardFilled()
	// The expiry price must come from a trade in the expiry step or just
	// before it (the settlement rule allows only a recent tick).
	recent := 1
	if spec.Features.ResolutionSeconds == 1 {
		recent = 5
	}
	var out []Sample
	excluded := 0
	for k := spec.Features.WindowSteps; k+h < s.Len(); k += spec.StrideSteps {
		closes, observed, price, ok := WindowAt(s, filled, spec.Features, k)
		if !ok {
			excluded++
			continue
		}
		f, ok := ComputeFeatures(spec.Features, closes, observed, price)
		if !ok {
			excluded++
			continue
		}
		x := filled[k+h]
		seen := false
		for j := 0; j < recent; j++ {
			if s.Observed(k + h - j) {
				seen = true
				break
			}
		}
		if math.IsNaN(x) || !seen {
			excluded++
			continue
		}
		out = append(out, Sample{T: s.TimeOf(k), Features: f, Up: x > price, Draw: x == price})
	}
	return out, excluded
}

// FitLogistic fits P(y=1) = sigmoid(a + b*x) by Newton-Raphson (IRLS) with a
// ridge penalty on b. withIntercept / withSlope false pin a / b at 0.
func FitLogistic(x []float64, y []bool, withIntercept, withSlope bool, ridge float64) (a, b float64, ok bool) {
	n := len(x)
	if n == 0 {
		return 0, 0, false
	}
	if !withIntercept && !withSlope {
		return 0, 0, true
	}
	if withIntercept {
		var pos float64
		for _, v := range y {
			if v {
				pos++
			}
		}
		// Jeffreys-smoothed base rate as the starting point.
		a = math.Log((pos + 0.5) / (float64(n) - pos + 0.5))
	}
	for iter := 0; iter < 100; iter++ {
		var ga, gb, haa, hab, hbb float64
		for i := 0; i < n; i++ {
			p := sigmoid(a + b*x[i])
			yi := 0.0
			if y[i] {
				yi = 1
			}
			w := p * (1 - p)
			ga += yi - p
			haa += w
			gb += (yi - p) * x[i]
			hab += w * x[i]
			hbb += w * x[i] * x[i]
		}
		gb -= ridge * b
		hbb += ridge
		var da, db float64
		switch {
		case withIntercept && withSlope:
			det := haa*hbb - hab*hab
			if !(det > 0) {
				return a, b, false
			}
			da = (hbb*ga - hab*gb) / det
			db = (haa*gb - hab*ga) / det
		case withIntercept:
			if !(haa > 0) {
				return a, 0, false
			}
			da = ga / haa
		default:
			if !(hbb > 0) {
				return 0, b, false
			}
			db = gb / hbb
		}
		a, b = a+da, b+db
		if math.Abs(da) < 1e-10 && math.Abs(db) < 1e-10 {
			break
		}
	}
	return a, b, finite(a, b)
}

// FitLogisticN fits P(y=1) = sigmoid(b0 + b1*x1 + ... + bk*xk) by Newton-
// Raphson with a ridge penalty on b1..bk. Returns [b0, b1, ..., bk].
func FitLogisticN(X [][]float64, y []bool, ridge float64) ([]float64, bool) {
	if len(X) == 0 {
		return nil, false
	}
	k := len(X[0]) + 1
	beta := make([]float64, k)
	var pos float64
	for _, v := range y {
		if v {
			pos++
		}
	}
	beta[0] = math.Log((pos + 0.5) / (float64(len(y)) - pos + 0.5))
	row := make([]float64, k)
	for iter := 0; iter < 100; iter++ {
		g := make([]float64, k)
		h := make([][]float64, k)
		for i := range h {
			h[i] = make([]float64, k)
		}
		for i, x := range X {
			row[0] = 1
			copy(row[1:], x)
			z := 0.0
			for j := 0; j < k; j++ {
				z += beta[j] * row[j]
			}
			p := sigmoid(z)
			yi := 0.0
			if y[i] {
				yi = 1
			}
			w := p * (1 - p)
			for a := 0; a < k; a++ {
				g[a] += (yi - p) * row[a]
				for b := 0; b < k; b++ {
					h[a][b] += w * row[a] * row[b]
				}
			}
		}
		for j := 1; j < k; j++ {
			g[j] -= ridge * beta[j]
			h[j][j] += ridge
		}
		step, ok := solve(h, g)
		if !ok {
			return beta, false
		}
		done := true
		for j := range beta {
			beta[j] += step[j]
			if math.Abs(step[j]) > 1e-10 {
				done = false
			}
		}
		if done {
			break
		}
	}
	return beta, finite(beta...)
}

// solve is Gaussian elimination with partial pivoting for a small system.
func solve(a [][]float64, b []float64) ([]float64, bool) {
	n := len(b)
	m := make([][]float64, n)
	for i := range a {
		m[i] = append(append([]float64(nil), a[i]...), b[i])
	}
	for c := 0; c < n; c++ {
		piv := c
		for r := c + 1; r < n; r++ {
			if math.Abs(m[r][c]) > math.Abs(m[piv][c]) {
				piv = r
			}
		}
		if math.Abs(m[piv][c]) < 1e-300 {
			return nil, false
		}
		m[c], m[piv] = m[piv], m[c]
		for r := c + 1; r < n; r++ {
			f := m[r][c] / m[c][c]
			for j := c; j <= n; j++ {
				m[r][j] -= f * m[c][j]
			}
		}
	}
	x := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := m[r][n]
		for j := r + 1; j < n; j++ {
			s -= m[r][j] * x[j]
		}
		x[r] = s / m[r][r]
	}
	return x, true
}

func logLoss(p float64, y bool) float64 {
	p = math.Min(1-1e-12, math.Max(1e-12, p))
	if y {
		return -math.Log(p)
	}
	return -math.Log(1 - p)
}

// Quantile of a sorted slice, linear interpolation.
func Quantile(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	pos := q * float64(len(sorted)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	if lo == hi {
		return sorted[lo]
	}
	return sorted[lo] + (sorted[hi]-sorted[lo])*(pos-float64(lo))
}

// WilsonUpper is the upper end of the two-sided 95% Wilson interval for k
// successes in n trials (n may be an effective, fractional count).
func WilsonUpper(k, n float64) float64 {
	if n <= 0 {
		return 1
	}
	const z = 1.96
	p := k / n
	den := 1 + z*z/n
	centre := p + z*z/(2*n)
	half := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n))
	return math.Min(1, (centre+half)/den)
}

// ReliabilityBin is one row of a reliability table: samples whose predicted
// UP win probability falls in the bin.
type ReliabilityBin struct {
	PredLow   float64 `json:"predLow"`
	PredHigh  float64 `json:"predHigh"`
	Count     int     `json:"count"`
	Effective float64 `json:"effective"`
	MeanPred  float64 `json:"meanPredicted"`
	Observed  float64 `json:"observed"`
	// 95% sampling half-width of Observed at the effective count.
	HalfWidth float64 `json:"halfWidth"`
}

// Scores are out-of-sample metrics for the UP win event (draws count as not
// a win), against the no-skill symmetric baseline (1 - q) / 2.
type Scores struct {
	Samples           int              `json:"samples"`
	Effective         float64          `json:"effective"`
	Brier             float64          `json:"brier"`
	BrierBaseline     float64          `json:"brierBaseline"`
	LogLoss           float64          `json:"logLoss"`
	LogLossBaseline   float64          `json:"logLossBaseline"`
	ECE               float64          `json:"ece"`
	UpRate            float64          `json:"upRate"`
	MeanPredUp        float64          `json:"meanPredictedUp"`
	DrawRate          float64          `json:"drawRate"`
	MeanPredDraw      float64          `json:"meanPredictedDraw"`
	Reliability       []ReliabilityBin `json:"reliability"`
	UncertaintyAddOn  float64          `json:"uncertaintyAddOn"`
	UncertaintySubset string           `json:"uncertaintySubset"`
}

// RegimeDetail is the per-regime calibration, for the report.
type RegimeDetail struct {
	Regime models.PricingRegime `json:"regime"`
	Test   Scores               `json:"test"`
}

// CalibrationDetail is everything the report shows about one entry.
type CalibrationDetail struct {
	Entry        models.PricingModelEntry `json:"entry"`
	Train        Scores                   `json:"train"`
	Holdout      Scores                   `json:"holdout"`
	Test         Scores                   `json:"test"`
	Regimes      []RegimeDetail           `json:"regimes"`
	ValidationLL struct {
		Symmetric     float64 `json:"symmetric"`
		InterceptOnly float64 `json:"interceptOnly"`
		MomentumOnly  float64 `json:"momentumOnly"`
		WithMomentum  float64 `json:"withMomentum"`
	} `json:"validationLogLoss"`
	TestSamples []Sample `json:"-"`
}

// predict applies an entry to one sample (the engine's ModelProbability).
func predict(e *models.PricingModelEntry, s Sample) Probability {
	return ModelProbability(e, s.Features, 1)
}

// score computes the metrics of a sample set under an entry.
func score(e *models.PricingModelEntry, samples []Sample, overlap float64, bins int) Scores {
	sc := Scores{Samples: len(samples), Effective: float64(len(samples)) * overlap, Reliability: []ReliabilityBin{}}
	if len(samples) == 0 {
		return sc
	}
	type pt struct {
		p    float64
		up   bool
		base float64
	}
	pts := make([]pt, len(samples))
	var sumB, sumBB, sumL, sumLB, ups, draws, sumP, sumQ float64
	for i, s := range samples {
		pr := predict(e, s)
		base := (1 - pr.Draw) / 2
		pts[i] = pt{pr.Up, s.Up, base}
		y := 0.0
		if s.Up {
			y = 1
			ups++
		}
		if s.Draw {
			draws++
		}
		sumB += (y - pr.Up) * (y - pr.Up)
		sumBB += (y - base) * (y - base)
		sumL += logLoss(pr.Up, s.Up)
		sumLB += logLoss(base, s.Up)
		sumP += pr.Up
		sumQ += pr.Draw
	}
	n := float64(len(samples))
	sc.Brier, sc.BrierBaseline = sumB/n, sumBB/n
	sc.LogLoss, sc.LogLossBaseline = sumL/n, sumLB/n
	sc.UpRate, sc.MeanPredUp = ups/n, sumP/n
	sc.DrawRate, sc.MeanPredDraw = draws/n, sumQ/n

	// Equal-count bins over the predicted probability.
	sort.SliceStable(pts, func(i, j int) bool { return pts[i].p < pts[j].p })
	if bins > len(pts) {
		bins = len(pts)
	}
	var ece float64
	for b := 0; b < bins; b++ {
		lo, hi := b*len(pts)/bins, (b+1)*len(pts)/bins
		if hi <= lo {
			continue
		}
		var sp, so float64
		for _, q := range pts[lo:hi] {
			sp += q.p
			if q.up {
				so++
			}
		}
		cnt := float64(hi - lo)
		bin := ReliabilityBin{
			PredLow: pts[lo].p, PredHigh: pts[hi-1].p, Count: hi - lo, Effective: cnt * overlap,
			MeanPred: sp / cnt, Observed: so / cnt,
		}
		bin.HalfWidth = 1.96 * math.Sqrt(math.Max(bin.Observed*(1-bin.Observed), 1e-9)/math.Max(1, bin.Effective))
		ece += cnt / n * math.Abs(bin.Observed-bin.MeanPred)
		sc.Reliability = append(sc.Reliability, bin)
	}
	sc.ECE = ece
	sc.UncertaintyAddOn, sc.UncertaintySubset = uncertaintyAddOn(e, samples, overlap)
	return sc
}

// uncertaintyAddOn is the amount by which the model may understate a
// customer's win probability: for each direction and each subset a customer
// can choose (all trades, the trades the model favours UP, those it favours
// DOWN), the one-sided bias mean(win - predicted) plus 1.96 standard errors at
// the effective sample size; the worst case is returned. Subsets with fewer
// than 100 effective samples are skipped (except "all").
func uncertaintyAddOn(e *models.PricingModelEntry, samples []Sample, overlap float64) (float64, string) {
	type acc struct{ n, sumY, sumP float64 }
	var sets [3][2]acc // subset x direction
	for _, s := range samples {
		pr := predict(e, s)
		favUp := pr.Up > pr.Down
		for si := 0; si < 3; si++ {
			if si == 1 && !favUp || si == 2 && favUp {
				continue
			}
			for d := 0; d < 2; d++ {
				win, p := s.Up, pr.Up
				if d == 1 {
					win, p = !s.Up && !s.Draw, pr.Down
				}
				a := &sets[si][d]
				a.n++
				a.sumP += p
				if win {
					a.sumY++
				}
			}
		}
	}
	names := [3]string{"all", "model favours UP", "model favours DOWN"}
	dirs := [2]string{"UP", "DOWN"}
	worst, label := 0.0, "all/UP"
	for si := 0; si < 3; si++ {
		for d := 0; d < 2; d++ {
			a := sets[si][d]
			eff := a.n * overlap
			if a.n == 0 || (si > 0 && eff < 100) {
				continue
			}
			pbar := a.sumP / a.n
			bias := math.Max(0, (a.sumY-a.sumP)/a.n)
			v := bias + 1.96*math.Sqrt(pbar*(1-pbar)/math.Max(1, eff))
			if v > worst {
				worst, label = v, names[si]+"/"+dirs[d]
			}
		}
	}
	return worst, label
}

// drawAddOn is the one-sided holdout bias of the draw model plus 1.96
// standard errors at the effective sample size.
func drawAddOn(e *models.PricingModelEntry, samples []Sample, overlap float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	var draws, pred float64
	for _, s := range samples {
		pred += DrawProbability(e, s.Features)
		if s.Draw {
			draws++
		}
	}
	n := float64(len(samples))
	qbar := pred / n
	bias := math.Max(0, (draws-pred)/n)
	return bias + 1.96*math.Sqrt(math.Max(qbar*(1-qbar), 1e-6)/math.Max(1, n*overlap))
}

// CalibrateEntry fits one entry from a series.
func CalibrateEntry(s *Series, spec CalibrationSpec) CalibrationDetail {
	spec.defaults()
	e := models.PricingModelEntry{
		ProviderSymbol: spec.ProviderSymbol, Symbol: spec.Symbol, ExpirySeconds: spec.ExpirySeconds,
		PriceSource: spec.PriceSource, Features: spec.Features, Regimes: []models.PricingRegime{},
	}
	det := CalibrationDetail{}
	insufficient := func(why string) CalibrationDetail {
		e.Status, e.StatusReason = models.PricingEntryInsufficientData, why
		det.Entry = e
		return det
	}
	if spec.HorizonSteps() == 0 {
		return insufficient("expiry is not a whole number of " + strconv.FormatInt(spec.Features.ResolutionSeconds, 10) + "s steps")
	}
	samples, excluded := BuildSamples(s, spec)
	e.Metrics.Excluded = excluded
	e.Metrics.StrideSeconds = int64(spec.StrideSteps) * spec.Features.ResolutionSeconds
	nTrain := int(float64(len(samples)) * spec.TrainFraction)
	nHold := int(float64(len(samples)) * spec.HoldoutFraction)
	train, hold, test := samples[:nTrain], samples[nTrain:nTrain+nHold], samples[nTrain+nHold:]
	overlap := spec.OverlapFactor()
	span := func(x []Sample) (int64, int64) {
		if len(x) == 0 {
			return 0, 0
		}
		return x[0].T, x[len(x)-1].T
	}
	e.Metrics.TrainFrom, e.Metrics.TrainTo = span(train)
	e.Metrics.HoldoutFrom, e.Metrics.HoldoutTo = span(hold)
	e.Metrics.TestFrom, e.Metrics.TestTo = span(test)
	e.Metrics.TrainSamples, e.Metrics.HoldoutSamples, e.Metrics.TestSamples = len(train), len(hold), len(test)
	e.Metrics.HoldoutEffective = float64(len(hold)) * overlap
	e.Metrics.TestEffectiveSamples = float64(len(test)) * overlap
	if len(train) < spec.MinTrainSamples {
		return insufficient("only " + strconv.Itoa(len(train)) + " training samples (need " + strconv.Itoa(spec.MinTrainSamples) + ")")
	}
	for _, c := range []struct {
		name string
		eff  float64
	}{{"holdout", e.Metrics.HoldoutEffective}, {"test", e.Metrics.TestEffectiveSamples}} {
		if c.eff < spec.MinTestEffective {
			return insufficient("only " + strconv.FormatFloat(c.eff, 'f', 0, 64) + " effective " + c.name +
				" samples (need " + strconv.FormatFloat(spec.MinTestEffective, 'f', 0, 64) + ")")
		}
	}

	// Volatility range and regimes from train only.
	sig := make([]float64, len(train))
	for i, t := range train {
		sig[i] = t.Features.Sigma
	}
	sort.Float64s(sig)
	e.SigmaMin, e.SigmaMax = Quantile(sig, 0.005), Quantile(sig, 0.995)
	t1, t2 := Quantile(sig, 1.0/3), Quantile(sig, 2.0/3)
	bounds := []float64{t1, t2, 0}
	names := []string{"LOW", "MID", "HIGH"}
	regimeOf := func(sigma float64) int {
		if sigma <= t1 {
			return 0
		}
		if sigma <= t2 {
			return 1
		}
		return 2
	}

	// Model form on the validation tail of train: with or without a drift
	// (intercept) and with or without momentum. Over seconds to minutes a
	// drift is expected to be zero, and a momentum effect has to earn its
	// place; the simplest form within 1e-6 of the best log loss wins. The
	// chosen form is then refitted on the whole training period.
	split := int(float64(len(train)) * (1 - spec.ValidationFraction))
	fitX, fitY := noDrawXY(train[:split])
	vx, vy := noDrawXY(train[split:])
	type form struct {
		intercept, slope bool
		ll               float64
	}
	forms := []form{{false, false, 0}, {true, false, 0}, {false, true, 0}, {true, true, 0}}
	best := -1
	for i := range forms {
		fa, fb, ok := FitLogistic(fitX, fitY, forms[i].intercept, forms[i].slope, 1)
		if !ok {
			forms[i].ll = math.Inf(1)
			continue
		}
		for j := range vx {
			forms[i].ll += logLoss(sigmoid(fa+fb*vx[j]), vy[j])
		}
		if len(vx) > 0 {
			forms[i].ll /= float64(len(vx))
		}
		if best < 0 || forms[i].ll < forms[best].ll-1e-6 {
			best = i
		}
	}
	det.ValidationLL.Symmetric, det.ValidationLL.InterceptOnly = forms[0].ll, forms[1].ll
	det.ValidationLL.MomentumOnly, det.ValidationLL.WithMomentum = forms[2].ll, forms[3].ll
	allX, allY := noDrawXY(train)
	fa, fb, ok := FitLogistic(allX, allY, forms[best].intercept, forms[best].slope, 1)
	if !ok {
		return insufficient("the logistic fit did not converge")
	}
	e.Intercept, e.MomentumCoef, e.MomentumUsed = fa, fb, forms[best].slope

	// Draw model: logistic in ln(sigma) and the local draw rate, on train.
	useLocal := spec.Features.DrawLagSteps > 0
	dX := make([][]float64, len(train))
	dy := make([]bool, len(train))
	for i, t := range train {
		row := []float64{math.Log(t.Features.Sigma)}
		if useLocal {
			row = append(row, logit(t.Features.LocalDrawRate))
		}
		dX[i], dy[i] = row, t.Draw
	}
	beta, okD := FitLogisticN(dX, dy, 1)
	if !okD {
		return insufficient("the draw-probability fit did not converge")
	}
	e.DrawIntercept, e.DrawSigmaCoef = beta[0], beta[1]
	if useLocal {
		e.DrawLocalCoef = beta[2]
	}

	var trainBy, holdBy, testBy [3][]Sample
	for _, t := range train {
		r := regimeOf(t.Features.Sigma)
		trainBy[r] = append(trainBy[r], t)
	}
	for _, t := range hold {
		r := regimeOf(t.Features.Sigma)
		holdBy[r] = append(holdBy[r], t)
	}
	for _, t := range test {
		r := regimeOf(t.Features.Sigma)
		testBy[r] = append(testBy[r], t)
	}
	short := ""
	for i := 0; i < 3; i++ {
		reg := models.PricingRegime{Name: names[i], SigmaUpTo: bounds[i], TrainSamples: len(trainBy[i]),
			HoldoutSamples: len(holdBy[i]), HoldoutEffective: float64(len(holdBy[i])) * overlap}
		var draws float64
		for _, t := range trainBy[i] {
			if t.Draw {
				draws++
			}
		}
		if n := float64(len(trainBy[i])); n > 0 {
			reg.DrawProb = draws / n
		}
		e.Regimes = append(e.Regimes, reg)
	}
	// Add-ons from holdout, with the regimes in place (predict reads them).
	for i := 0; i < 3; i++ {
		e.Regimes[i].DrawUncertainty = drawAddOn(&e, holdBy[i], overlap)
		e.Regimes[i].Uncertainty, _ = uncertaintyAddOn(&e, holdBy[i], overlap)
		if e.Regimes[i].TrainSamples < spec.MinRegimeTrain || e.Regimes[i].HoldoutEffective < spec.MinRegimeTestEffective {
			short = names[i]
		}
	}
	det.Regimes = make([]RegimeDetail, 3)
	for i := 0; i < 3; i++ {
		det.Regimes[i] = RegimeDetail{Regime: e.Regimes[i], Test: score(&e, testBy[i], overlap, 5)}
	}
	det.Train = score(&e, train, overlap, 10)
	det.Holdout = score(&e, hold, overlap, 10)
	det.Test = score(&e, test, overlap, 10)
	e.Metrics.BrierTest, e.Metrics.BrierBaselineTest = det.Test.Brier, det.Test.BrierBaseline
	e.Metrics.LogLossTest, e.Metrics.LogLossBaselineTest = det.Test.LogLoss, det.Test.LogLossBaseline
	e.Metrics.ECETest, e.Metrics.DrawRateTest = det.Test.ECE, det.Test.DrawRate
	det.TestSamples = test
	if short != "" {
		return insufficient("the " + short + " volatility regime has too few samples")
	}
	for _, r := range e.Regimes {
		if r.Uncertainty > 0.25 || r.DrawUncertainty > 0.25 {
			return insufficient("the " + r.Name + " volatility regime is too uncertain to price")
		}
	}
	e.Status = models.PricingEntryCalibrated
	det.Entry = e
	return det
}

func noDrawXY(samples []Sample) ([]float64, []bool) {
	x := make([]float64, 0, len(samples))
	y := make([]bool, 0, len(samples))
	for _, s := range samples {
		if s.Draw {
			continue
		}
		x = append(x, s.Features.Momentum)
		y = append(y, s.Up)
	}
	return x, y
}
