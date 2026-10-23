package pricing

import (
	"fmt"
	"math"
	"sort"

	"binarytrading/internal/models"
)

// Early-close ("Sell now") back-test: audit finding F-12.
//
// The live Sell-now price (service/binary earlyclose.go QuoteEarlyClose and
// earlyclose_service.go volatilityPerSecond) values an open position as
//
//	value = stake x (1 + payout) x clamp(Phi(z), 0.01, 0.99) x (1 - fee)
//	z     = signed distance in the money / (price x sigma x sqrt(seconds left))
//
// with sigma the realised volatility of the last 60 s of ticks. This file
// replays that valuation on historical prices: positions are opened at
// sampled entries, valued at several times before expiry (never inside the
// cut-off), and each value is compared with what the position paid when held
// to expiry (win: stake x (1 + payout); draw: stake refunded; loss: 0). The
// valuation is injected (EarlyCloseValuer) because this package cannot import
// service/binary: cmd/calibrate passes binary.QuoteEarlyClose, so the
// back-test prices with the production function.
//
// Reported per cell (period x seconds remaining x volatility regime): the
// calibration of P(win) (reliability, Brier score, log loss, ECE) and the
// platform's edge on the buy-back, per unit stake,
//
//	edge = payoff if held - value paid
//
// averaged over the cell: what the platform gains by buying the position back
// instead of letting it run. A negative edge means Sell now paid more than
// the position was worth on average.

// EarlyCloseCase is one open position valued before expiry.
type EarlyCloseCase struct {
	Stake               int64
	PayoutPercent       float64
	Direction           models.Direction
	EntryPrice          float64
	CurrentPrice        float64
	SecondsRemaining    float64
	VolatilityPerSecond float64
	FeePercent          float64
	// Symbol of the instrument (for per-instrument settings of the valuer).
	Symbol string
}

// EarlyCloseValuer prices a case as the live service does: the P(win) it
// records and the value it pays, minor units.
type EarlyCloseValuer func(EarlyCloseCase) (winProbability float64, value int64)

// EarlyCloseVolatilityWindowSeconds is the live estimator's window
// (binary.volatilityWindowMs).
const EarlyCloseVolatilityWindowSeconds = 60

// EarlyCloseVolatility is the live per-second volatility of a window of tick
// prices, oldest first (binary.volatilityPerSecond over
// marketdata.GetRealisedVolatility): the sample standard deviation of the log
// returns between consecutive ticks, rescaled by sqrt(max(0.1, ticks / 60)).
// 0 with fewer than 3 ticks or no movement - the live service then refuses
// the close. TestEarlyCloseBacktestVolatilityIsTheLiveOne (service/binary)
// checks it against the live function on the same ticks.
func EarlyCloseVolatility(prices []float64) float64 {
	if len(prices) < 3 {
		return 0
	}
	var sum float64
	n := 0
	for i := 1; i < len(prices); i++ {
		if prices[i-1] > 0 {
			sum += math.Log(prices[i] / prices[i-1])
			n++
		}
	}
	if n < 2 {
		return 0
	}
	mean := sum / float64(n)
	var acc float64
	for i := 1; i < len(prices); i++ {
		if prices[i-1] > 0 {
			r := math.Log(prices[i] / prices[i-1])
			acc += (r - mean) * (r - mean)
		}
	}
	perTick := math.Sqrt(acc / float64(n-1))
	if perTick <= 0 {
		return 0
	}
	return perTick * math.Sqrt(math.Max(0.1, float64(len(prices))/EarlyCloseVolatilityWindowSeconds))
}

// EarlyCloseLiveSigma is the live estimator on a 1-second series: at step j
// the ticks are the observed closes of the 60 seconds ending at j (one tick
// per second that had a trade). Not safe for concurrent use.
func EarlyCloseLiveSigma(s *Series) func(j int) float64 {
	w := int(EarlyCloseVolatilityWindowSeconds * 1000 / s.StepMs)
	buf := make([]float64, 0, w+1)
	return func(j int) float64 {
		buf = buf[:0]
		for i := max(0, j-w+1); i <= j && i < s.Len(); i++ {
			if s.Observed(i) {
				buf = append(buf, s.Close[i])
			}
		}
		return EarlyCloseVolatility(buf)
	}
}

// EarlyCloseProxySigma stands in for the live estimator on a coarser grid
// (1-minute klines, where 60 s of ticks do not exist): the per-second
// volatility of the last windowSteps returns of the forward-filled closes.
// It is NOT the live estimator; it only lets the Phi(z) form be checked over
// a longer history.
func EarlyCloseProxySigma(s *Series, windowSteps int) func(j int) float64 {
	filled := s.ForwardFilled()
	dt := float64(s.StepMs) / 1000
	return func(j int) float64 {
		if j-windowSteps < 0 || j >= len(filled) {
			return 0
		}
		var sum, sum2 float64
		n := 0
		for i := j - windowSteps + 1; i <= j; i++ {
			a, b := filled[i-1], filled[i]
			if math.IsNaN(a) || math.IsNaN(b) || a <= 0 {
				continue
			}
			r := math.Log(b / a)
			sum += r
			sum2 += r * r
			n++
		}
		if n < 2 {
			return 0
		}
		v := (sum2 - sum*sum/float64(n)) / float64(n-1)
		if v <= 0 {
			return 0
		}
		return math.Sqrt(v / dt)
	}
}

// EarlyCloseRegimeCuts are the terciles of sigma (> 0) at every stride-th
// step of [from, to).
func EarlyCloseRegimeCuts(sigma func(int) float64, from, to, stride int) [2]float64 {
	var xs []float64
	for j := max(0, from); j < to; j += max(1, stride) {
		if v := sigma(j); v > 0 {
			xs = append(xs, v)
		}
	}
	sort.Float64s(xs)
	if len(xs) == 0 {
		return [2]float64{}
	}
	return [2]float64{Quantile(xs, 1.0/3), Quantile(xs, 2.0/3)}
}

// EarlyCloseBacktestConfig is one (instrument, expiry) back-test.
type EarlyCloseBacktestConfig struct {
	Symbol        string
	ExpirySeconds int64
	// Valuation times, seconds before expiry (each below the expiry).
	RemainingSeconds []int64
	StrideSteps      int
	// Entries in [FromStep, ToStep) whose expiry lies in the series; entries
	// before SplitStep form the FIT period, the others the TEST period.
	FromStep, SplitStep, ToStep int
	PayoutPercent               float64
	FeePercent                  float64
	Stake                       int64
	// Volatility per sqrt(second) at a step; 0 = the position cannot be
	// valued (the live service refuses the close).
	Sigma      func(step int) float64
	RegimeCuts [2]float64
	Value      EarlyCloseValuer
	// Reliability bins on [0, 1] (default 10); bins with fewer effective
	// samples than MinBinEffective are not used for the worst-bin figure
	// (default 50).
	Bins            int
	MinBinEffective float64
	// Attach the reliability table to every cell, not only to the cells of
	// every valuation time.
	ReliabilityEveryCell bool
	// Price tick of the instrument. A valuation whose current price is
	// within AtStrikeTicks ticks of the entry (default 1) goes to its own
	// "at the strike" bin instead of a probability bin: klines carry the
	// last trade, which bounces between bid and ask, so a one-tick
	// difference there is usually no move of the mid the live engine uses.
	TickSize      float64
	AtStrikeTicks float64
}

// Periods and regimes of the cells.
const (
	ECPeriodFit  = "FIT"
	ECPeriodTest = "TEST"
	ECPeriodAll  = "ALL"
	ECRegimeAll  = "ALL"
)

var ecRegimes = []string{"LOW", "MID", "HIGH"}

// EarlyCloseBin is one row of a reliability table.
type EarlyCloseBin struct {
	// The "at the strike" bin (see EarlyCloseBacktestConfig.TickSize).
	AtStrike      bool    `json:"atStrike,omitempty"`
	PredLow       float64 `json:"predLow"`
	PredHigh      float64 `json:"predHigh"`
	Count         int     `json:"count"`
	Effective     float64 `json:"effective"`
	MeanPredicted float64 `json:"meanPredicted"`
	Observed      float64 `json:"observed"`
	DrawRate      float64 `json:"drawRate"`
	// 95% half-width of Observed at the effective count.
	HalfWidth float64 `json:"halfWidth"`
	// Platform edge after the fee, percent of stake, and its 95% half-width.
	EdgePct          float64 `json:"edgePercent"`
	EdgeHalfWidthPct float64 `json:"edgeHalfWidthPercent"`
}

// EarlyCloseCell is one period x seconds remaining x regime.
type EarlyCloseCell struct {
	Period string `json:"period"`
	// 0 = every valuation time.
	Remaining     int64   `json:"secondsRemaining"`
	Regime        string  `json:"regime"`
	Samples       int     `json:"samples"`
	Effective     float64 `json:"effective"`
	MeanPredicted float64 `json:"meanPredicted"`
	WinRate       float64 `json:"winRate"`
	DrawRate      float64 `json:"drawRate"`
	// "win" against the model's P(win); draws count as not a win. The
	// no-information baseline (P = 0.5) scores 0.25 and ln 2 = 0.6931.
	Brier   float64 `json:"brier"`
	LogLoss float64 `json:"logLoss"`
	ECE     float64 `json:"ece"`
	// Means per unit stake, percent: value paid, payoff if held, and the
	// edge = payoff - value (after the fee, with its 95% half-width, and
	// before the fee).
	MeanValuePct     float64         `json:"meanValuePercent"`
	MeanPayoffPct    float64         `json:"meanPayoffPercent"`
	EdgePct          float64         `json:"edgePercent"`
	EdgeHalfWidthPct float64         `json:"edgeHalfWidthPercent"`
	EdgeBeforeFeePct float64         `json:"edgeBeforeFeePercent"`
	Reliability      []EarlyCloseBin `json:"reliability,omitempty"`
}

// EarlyCloseBacktestResult is one (instrument, expiry).
type EarlyCloseBacktestResult struct {
	Symbol           string           `json:"symbol"`
	ExpirySeconds    int64            `json:"expirySeconds"`
	PayoutPercent    float64          `json:"payoutPercent"`
	FeePercent       float64          `json:"feePercent"`
	StrideSteps      int              `json:"strideSteps"`
	RemainingSeconds []int64          `json:"remainingSeconds"`
	RegimeCuts       [2]float64       `json:"regimeCuts"`
	FitEntries       int              `json:"fitEntries"`
	TestEntries      int              `json:"testEntries"`
	Valuations       int              `json:"valuations"`
	Refused          int              `json:"refused"`
	Cells            []EarlyCloseCell `json:"cells"`
	// Lowest TEST-period edges: over the cells of one valuation time and one
	// regime, and over their reliability bins with enough samples (the price
	// a customer who sells only when the model is most generous gets).
	WorstCellEdgePct     float64 `json:"worstCellEdgePercent"`
	WorstCellHalfWidth   float64 `json:"worstCellHalfWidthPercent"`
	WorstCell            string  `json:"worstCell"`
	WorstBinEdgePct      float64 `json:"worstBinEdgePercent"`
	WorstBinHalfWidthPct float64 `json:"worstBinHalfWidthPercent"`
	WorstBin             string  `json:"worstBin"`
	// TEST period, the reliability bins of each valuation time and regime
	// with at least MinBinEffective effective samples: how many, how many
	// with a negative edge, and how many significantly negative (edge plus
	// its 95% half-width below zero).
	BinsChecked               int `json:"binsChecked"`
	BinsNegative              int `json:"binsNegative"`
	BinsSignificantlyNegative int `json:"binsSignificantlyNegative"`
}

// Cell lookup.
func (r *EarlyCloseBacktestResult) Cell(period string, remaining int64, regime string) *EarlyCloseCell {
	for i := range r.Cells {
		c := &r.Cells[i]
		if c.Period == period && c.Remaining == remaining && c.Regime == regime {
			return c
		}
	}
	return nil
}

type ecBinAcc struct {
	n                                int
	eff, sumP, wins, draws, e1, e1sq float64
}

type ecAcc struct {
	n                                    int
	eff, sumP, wins, draws, brier, ll    float64
	paid, payoff, edge, edge2, edgeNoFee float64
	bins                                 []ecBinAcc
}

type ecKey struct {
	period string
	rem    int64
	regime string
}

// EarlyCloseBacktest runs one back-test (see the file comment).
func EarlyCloseBacktest(s *Series, c EarlyCloseBacktestConfig) EarlyCloseBacktestResult {
	if c.Bins <= 0 {
		c.Bins = 10
	}
	if c.MinBinEffective <= 0 {
		c.MinBinEffective = 50
	}
	if c.Stake <= 0 {
		c.Stake = 1000_00
	}
	if c.TickSize > 0 && c.AtStrikeTicks <= 0 {
		c.AtStrikeTicks = 1
	}
	out := EarlyCloseBacktestResult{Symbol: c.Symbol, ExpirySeconds: c.ExpirySeconds, PayoutPercent: c.PayoutPercent,
		FeePercent: c.FeePercent, StrideSteps: max(1, c.StrideSteps), RemainingSeconds: []int64{}, RegimeCuts: c.RegimeCuts,
		Cells: []EarlyCloseCell{}}
	stepSec := float64(s.StepMs) / 1000
	h := int(c.ExpirySeconds * 1000 / s.StepMs)
	var rems []int64
	for _, r := range c.RemainingSeconds {
		if rs := int(r * 1000 / s.StepMs); rs > 0 && rs < h {
			rems = append(rems, r)
		}
	}
	out.RemainingSeconds = append(out.RemainingSeconds, rems...)
	filled := s.ForwardFilled()
	stride := out.StrideSteps
	accs := map[ecKey]*ecAcc{}
	get := func(k ecKey) *ecAcc {
		a := accs[k]
		if a == nil {
			a = &ecAcc{bins: make([]ecBinAcc, c.Bins+1)}
			accs[k] = a
		}
		return a
	}
	regimeOf := func(sigma float64) string {
		switch {
		case sigma <= c.RegimeCuts[0]:
			return ecRegimes[0]
		case sigma <= c.RegimeCuts[1]:
			return ecRegimes[1]
		}
		return ecRegimes[2]
	}
	stake := float64(c.Stake)
	win := 1 + c.PayoutPercent/100
	for k := max(c.FromStep, 1); k < c.ToStep && k+h < s.Len(); k += stride {
		entry, exit := filled[k], filled[k+h]
		if math.IsNaN(entry) || math.IsNaN(exit) || !(entry > 0) {
			continue
		}
		period := ECPeriodFit
		if k >= c.SplitStep {
			period = ECPeriodTest
			out.TestEntries++
		} else {
			out.FitEntries++
		}
		valued := 0
		type val struct {
			rem      int64
			regime   string
			w        float64
			atStrike bool
			p        [2]float64
			v, v0    [2]int64
		}
		var vals []val
		for _, rem := range rems {
			rs := int(rem * 1000 / s.StepMs)
			j := k + h - rs
			cur := filled[j]
			out.Valuations++
			sigma := c.Sigma(j)
			if !(sigma > 0) || math.IsNaN(cur) {
				out.Refused++
				continue
			}
			x := val{rem: rem, regime: regimeOf(sigma), w: math.Min(1, float64(stride)/float64(rs)) / 2,
				atStrike: c.TickSize > 0 && math.Abs(cur-entry) <= c.AtStrikeTicks*c.TickSize*(1+1e-9)}
			for di, d := range []models.Direction{models.DirectionUp, models.DirectionDown} {
				ec := EarlyCloseCase{Stake: c.Stake, PayoutPercent: c.PayoutPercent, Direction: d, EntryPrice: entry,
					CurrentPrice: cur, SecondsRemaining: float64(rs) * stepSec, VolatilityPerSecond: sigma,
					FeePercent: c.FeePercent, Symbol: c.Symbol}
				x.p[di], x.v[di] = c.Value(ec)
				ec.FeePercent = 0
				_, x.v0[di] = c.Value(ec)
			}
			vals = append(vals, x)
			valued++
		}
		for _, x := range vals {
			for di := range 2 {
				up := di == 0
				won := (up && exit > entry) || (!up && exit < entry)
				draw := exit == entry
				payoff := 0.0
				switch {
				case won:
					payoff = win
				case draw:
					payoff = 1
				}
				paid := float64(x.v[di]) / stake
				paid0 := float64(x.v0[di]) / stake
				p := x.p[di]
				bin := min(c.Bins-1, int(p*float64(c.Bins)))
				if x.atStrike {
					bin = c.Bins
				}
				for _, per := range []string{period, ECPeriodAll} {
					for _, rem := range []int64{x.rem, 0} {
						w := x.w
						if rem == 0 {
							// Samples of one entry share its outcome.
							w /= float64(valued)
						}
						for _, rg := range []string{x.regime, ECRegimeAll} {
							a := get(ecKey{per, rem, rg})
							a.n++
							a.eff += w
							a.sumP += p
							y := 0.0
							if won {
								y = 1
								a.wins++
							}
							if draw {
								a.draws++
							}
							a.brier += (p - y) * (p - y)
							a.ll += logLoss(p, won)
							e := payoff - paid
							a.paid += paid
							a.payoff += payoff
							a.edge += e
							a.edge2 += e * e
							a.edgeNoFee += payoff - paid0
							b := &a.bins[bin]
							b.n++
							b.eff += w
							b.sumP += p
							b.wins += y
							if draw {
								b.draws++
							}
							b.e1 += e
							b.e1sq += e * e
						}
					}
				}
			}
		}
	}

	periods := []string{ECPeriodFit, ECPeriodTest, ECPeriodAll}
	remList := append(append([]int64{}, rems...), 0)
	regs := append(append([]string{}, ecRegimes...), ECRegimeAll)
	out.WorstCellEdgePct, out.WorstBinEdgePct = math.Inf(1), math.Inf(1)
	for _, per := range periods {
		for _, rem := range remList {
			for _, rg := range regs {
				a := accs[ecKey{per, rem, rg}]
				if a == nil || a.n == 0 {
					continue
				}
				cell := finishCell(a, per, rem, rg, c.Bins)
				if per == ECPeriodTest {
					if rem != 0 && rg != ECRegimeAll && cell.Effective >= c.MinBinEffective && cell.EdgePct < out.WorstCellEdgePct {
						out.WorstCellEdgePct, out.WorstCellHalfWidth = cell.EdgePct, cell.EdgeHalfWidthPct
						out.WorstCell = fmt.Sprintf("%ds before expiry, %s volatility", rem, rg)
					}
					for _, b := range binsOf(a, c.Bins) {
						if b.Effective >= c.MinBinEffective && rem != 0 && rg != ECRegimeAll {
							out.BinsChecked++
							if b.EdgePct < 0 {
								out.BinsNegative++
							}
							if b.EdgePct+b.EdgeHalfWidthPct < 0 {
								out.BinsSignificantlyNegative++
							}
						}
						if b.Effective >= c.MinBinEffective && b.EdgePct < out.WorstBinEdgePct {
							out.WorstBinEdgePct, out.WorstBinHalfWidthPct = b.EdgePct, b.EdgeHalfWidthPct
							where := "every valuation time"
							if rem != 0 {
								where = fmt.Sprintf("%ds before expiry", rem)
							}
							bin := fmt.Sprintf("P(win) %.1f-%.1f", b.PredLow, b.PredHigh)
							if b.AtStrike {
								bin = "at the strike"
							}
							out.WorstBin = fmt.Sprintf("%s, %s, %s volatility", bin, where, rg)
						}
					}
				}
				if rem == 0 || c.ReliabilityEveryCell {
					cell.Reliability = binsOf(a, c.Bins)
				}
				out.Cells = append(out.Cells, cell)
			}
		}
	}
	if math.IsInf(out.WorstCellEdgePct, 1) {
		out.WorstCellEdgePct = 0
	}
	if math.IsInf(out.WorstBinEdgePct, 1) {
		out.WorstBinEdgePct = 0
	}
	return out
}

func finishCell(a *ecAcc, period string, rem int64, regime string, bins int) EarlyCloseCell {
	n := float64(a.n)
	cell := EarlyCloseCell{Period: period, Remaining: rem, Regime: regime, Samples: a.n, Effective: a.eff,
		MeanPredicted: a.sumP / n, WinRate: a.wins / n, DrawRate: a.draws / n, Brier: a.brier / n, LogLoss: a.ll / n,
		MeanValuePct: a.paid / n * 100, MeanPayoffPct: a.payoff / n * 100, EdgePct: a.edge / n * 100,
		EdgeBeforeFeePct: a.edgeNoFee / n * 100}
	sd := math.Sqrt(math.Max(0, a.edge2/n-(a.edge/n)*(a.edge/n)))
	cell.EdgeHalfWidthPct = 1.96 * sd / math.Sqrt(math.Max(1, a.eff)) * 100
	for _, b := range a.bins {
		if b.n > 0 {
			cell.ECE += float64(b.n) / n * math.Abs(b.sumP/float64(b.n)-b.wins/float64(b.n))
		}
	}
	return cell
}

func binsOf(a *ecAcc, bins int) []EarlyCloseBin {
	out := []EarlyCloseBin{}
	for i, b := range a.bins {
		if b.n == 0 {
			continue
		}
		n := float64(b.n)
		obs := b.wins / n
		eb := EarlyCloseBin{AtStrike: i == bins, PredLow: float64(i) / float64(bins), PredHigh: float64(i+1) / float64(bins), Count: b.n,
			Effective: b.eff, MeanPredicted: b.sumP / n, Observed: obs, DrawRate: b.draws / n, EdgePct: b.e1 / n * 100}
		if eb.AtStrike {
			eb.PredLow, eb.PredHigh = 0, 1
		}
		eff := math.Max(1, b.eff)
		eb.HalfWidth = 1.96 * math.Sqrt(obs*(1-obs)/eff)
		sd := math.Sqrt(math.Max(0, b.e1sq/n-(b.e1/n)*(b.e1/n)))
		eb.EdgeHalfWidthPct = 1.96 * sd / math.Sqrt(eff) * 100
		out = append(out, eb)
	}
	return out
}
