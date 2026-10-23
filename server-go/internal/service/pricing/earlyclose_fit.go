package pricing

import (
	"math"
	"sort"

	"binarytrading/internal/models"
)

// Calibration of the Sell-now probability (audit F-12). The back-test
// (earlyclose_eval.go) shows two systematic errors of P(win) = Phi(z) with
// the 60 s realised volatility:
//
//   - short horizons in calm and normal markets: once the price has really
//     moved away from the entry it mostly stays on that side until expiry, so
//     Phi(z) is far too close to 0.5 (out-of-the-money positions overpaid);
//   - long horizons in calm markets: 60 s of calm understates the
//     volatility still to come, so Phi(z) is too sure (deep in-the-money
//     positions overpaid).
//
// The correction keeps the formula's shape and adds three parameters per cell
// of (seconds remaining band x volatility ratio band), applied only when the
// price is not at the entry (models.EarlyCloseCalibrationRow):
//
//	sigma' = max(sigma x scale, floor x baseline)
//	z'     = signed move / (price x sigma' x sqrt(seconds left))
//	P      = stickiness x [in the money] + (1 - stickiness) x Phi(z')
//
// stickiness is the probability that the price simply stays on its current
// side; scale and floor correct the volatility. They are fitted here by
// maximum likelihood (log loss of "the position wins") on the FIT period, and
// the back-test then measures the corrected price on the TEST period.

// EarlyCloseSample is one valuation of an UP position; the DOWN position is
// its mirror (z -> -z, Up <-> Down).
type EarlyCloseSample struct {
	Z         float32 // uncorrected z of the UP position
	Remaining int32   // seconds before expiry
	VolRatio  float32 // sigma / the instrument's baseline volatility
	Up, Down  bool    // expiry above / below the entry (neither: a draw)
	AtStrike  bool    // current price within the at-the-strike tolerance
}

// EarlyCloseCollect gathers the valuations of entries in [from, to) as the
// back-test makes them (same entries, valuation times, volatility and
// at-the-strike rule).
func EarlyCloseCollect(s *Series, c EarlyCloseBacktestConfig, baseline float64, from, to int) []EarlyCloseSample {
	if c.TickSize > 0 && c.AtStrikeTicks <= 0 {
		c.AtStrikeTicks = 1
	}
	h := int(c.ExpirySeconds * 1000 / s.StepMs)
	stepSec := float64(s.StepMs) / 1000
	filled := s.ForwardFilled()
	var out []EarlyCloseSample
	for k := max(from, 1); k < to && k+h < s.Len(); k += max(1, c.StrideSteps) {
		entry, exit := filled[k], filled[k+h]
		if math.IsNaN(entry) || math.IsNaN(exit) || !(entry > 0) {
			continue
		}
		for _, rem := range c.RemainingSeconds {
			rs := int(rem * 1000 / s.StepMs)
			if rs <= 0 || rs >= h {
				continue
			}
			j := k + h - rs
			cur := filled[j]
			sigma := c.Sigma(j)
			if !(sigma > 0) || math.IsNaN(cur) {
				continue
			}
			tau := float64(rs) * stepSec
			out = append(out, EarlyCloseSample{
				Z:         float32((cur - entry) / (cur * sigma * math.Sqrt(tau))),
				Remaining: int32(tau),
				VolRatio:  float32(sigma / baseline),
				Up:        exit > entry, Down: exit < entry,
				AtStrike: c.TickSize > 0 && math.Abs(cur-entry) <= c.AtStrikeTicks*c.TickSize*(1+1e-9),
			})
		}
	}
	return out
}

// EarlyCloseWinProbability is the corrected P(win) before the clamps, for a
// position whose uncorrected z (its own direction) is z, with ratio = sigma
// / baseline (<= 0: unknown, no floor). z == 0 (at the entry) is 0.5
// whatever the correction. The live function is binary.QuoteEarlyClose; this
// copy serves the fit, and TestEarlyCloseCorrectionMatchesTheService
// (service/binary) keeps the two equal.
func EarlyCloseWinProbability(z, ratio float64, row models.EarlyCloseCalibrationRow) float64 {
	if z == 0 {
		return 0.5
	}
	scale := row.SigmaScale
	if scale <= 0 {
		scale = 1
	}
	if ratio > 0 && row.MinVolRatio > 0 {
		scale = math.Max(scale, row.MinVolRatio/ratio)
	}
	p := 0.5 * (1 + math.Erf(z/scale/math.Sqrt2))
	itm := 0.0
	if z > 0 {
		itm = 1
	}
	s := math.Min(1, math.Max(0, row.Stickiness))
	return s*itm + (1-s)*p
}

// Grids searched by the fit (coordinate descent, one parameter at a time).
var (
	earlyCloseScaleGrid      = []float64{0.1, 0.15, 0.2, 0.25, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1, 1.1, 1.25, 1.5, 1.75, 2, 2.5, 3, 4, 5, 6, 8, 10}
	earlyCloseFloorGrid      = []float64{0, 0.25, 0.5, 0.75, 1, 1.25, 1.5, 2}
	earlyCloseStickinessGrid = []float64{0, 0.05, 0.1, 0.15, 0.2, 0.25, 0.3, 0.35, 0.4, 0.45, 0.5, 0.55, 0.6, 0.65, 0.7, 0.75, 0.8, 0.85, 0.9}
)

// EarlyCloseFitCell is one fitted cell, with its evidence.
type EarlyCloseFitCell struct {
	models.EarlyCloseCalibrationRow
	Samples int `json:"samples"`
	// Mean log loss of "the position wins" on the cell's FIT samples, both
	// directions, with the fitted parameters and without (the identity).
	LogLoss       float64 `json:"logLoss"`
	LogLossBefore float64 `json:"logLossBefore"`
}

// EarlyCloseFloorEvidence is, per band of seconds remaining and threshold,
// how the positions the corrected model prices below the threshold actually
// ended (FIT period): a lower clamp at the threshold pays more than such a
// position is worth unless (1 - fee) x threshold stays below their win rate.
type EarlyCloseFloorEvidence struct {
	MaxSecondsRemaining int64   `json:"maxSecondsRemaining"`
	BelowPercent        float64 `json:"belowPercent"`
	Samples             int     `json:"samples"`
	MeanPredicted       float64 `json:"meanPredicted"`
	WinRate             float64 `json:"winRate"`
	DrawRate            float64 `json:"drawRate"`
}

// EarlyCloseFit is the fitted table and the floor evidence.
type EarlyCloseFit struct {
	Cells []EarlyCloseFitCell       `json:"cells"`
	Floor []EarlyCloseFloorEvidence `json:"floor"`
}

// FitEarlyCloseCalibration fits (scale, floor, stickiness) per cell of
// maxSeconds x maxVolRatio (each list ascending; the last band of each is
// unbounded, written as 0) by coordinate descent on the log loss, the
// probabilities clamped to [minProbability, 0.99]. Samples at the strike
// are left out (the correction does not apply there). Cells with fewer than
// minSamples keep the identity. The floor evidence is reported for each of
// floorThresholds.
func FitEarlyCloseCalibration(samples []EarlyCloseSample, maxSeconds []int64, maxVolRatio []float64, minSamples int, minProbability float64, floorThresholds []float64) EarlyCloseFit {
	type cellSamples struct {
		z, ratio []float64
		win      []bool
		draw     []bool
	}
	cells := make([]cellSamples, len(maxSeconds)*len(maxVolRatio))
	for _, s := range samples {
		if s.AtStrike || s.Z == 0 {
			continue
		}
		bi := bandIndex(float64(s.Remaining), maxSeconds)
		ri := ratioIndex(float64(s.VolRatio), maxVolRatio)
		c := &cells[bi*len(maxVolRatio)+ri]
		z, r := float64(s.Z), float64(s.VolRatio)
		draw := !s.Up && !s.Down
		c.z = append(c.z, z, -z)
		c.ratio = append(c.ratio, r, r)
		c.win = append(c.win, s.Up, s.Down)
		c.draw = append(c.draw, draw, draw)
	}
	clampP := func(p float64) float64 { return math.Min(0.99, math.Max(minProbability, p)) }
	loss := func(c cellSamples, row models.EarlyCloseCalibrationRow) float64 {
		var ll float64
		for i, z := range c.z {
			ll += logLoss(clampP(EarlyCloseWinProbability(z, c.ratio[i], row)), c.win[i])
		}
		return ll / float64(len(c.z))
	}
	out := EarlyCloseFit{}
	type floorAcc struct{ n, wins, draws, sumP float64 }
	for bi, sec := range maxSeconds {
		floors := make([]floorAcc, len(floorThresholds))
		for ri, ratio := range maxVolRatio {
			c := cells[bi*len(maxVolRatio)+ri]
			row := models.EarlyCloseCalibrationRow{MaxSecondsRemaining: sec, MaxVolRatio: ratio, SigmaScale: 1}
			fc := EarlyCloseFitCell{EarlyCloseCalibrationRow: row, Samples: len(c.z) / 2}
			if len(c.z) > 0 {
				fc.LogLossBefore = loss(c, row)
				fc.LogLoss = fc.LogLossBefore
			}
			if fc.Samples >= minSamples {
				for round := 0; round < 4; round++ {
					changed := false
					try := func(cand models.EarlyCloseCalibrationRow) {
						if l := loss(c, cand); l < fc.LogLoss-1e-12 {
							fc.LogLoss, fc.EarlyCloseCalibrationRow, changed = l, cand, true
						}
					}
					for _, v := range earlyCloseScaleGrid {
						cand := fc.EarlyCloseCalibrationRow
						cand.SigmaScale = v
						try(cand)
					}
					for _, v := range earlyCloseFloorGrid {
						cand := fc.EarlyCloseCalibrationRow
						cand.MinVolRatio = v
						try(cand)
					}
					for _, v := range earlyCloseStickinessGrid {
						cand := fc.EarlyCloseCalibrationRow
						cand.Stickiness = v
						try(cand)
					}
					if !changed {
						break
					}
				}
			}
			out.Cells = append(out.Cells, fc)
			for i, z := range c.z {
				p := EarlyCloseWinProbability(z, c.ratio[i], fc.EarlyCloseCalibrationRow)
				for ti, th := range floorThresholds {
					if p < th {
						f := &floors[ti]
						f.n++
						f.sumP += p
						if c.win[i] {
							f.wins++
						}
						if c.draw[i] {
							f.draws++
						}
					}
				}
			}
		}
		for ti, th := range floorThresholds {
			f := floors[ti]
			ev := EarlyCloseFloorEvidence{MaxSecondsRemaining: sec, BelowPercent: th * 100, Samples: int(f.n)}
			if f.n > 0 {
				ev.MeanPredicted, ev.WinRate, ev.DrawRate = f.sumP/f.n, f.wins/f.n, f.draws/f.n
			}
			out.Floor = append(out.Floor, ev)
		}
	}
	return out
}

func bandIndex(x float64, upper []int64) int {
	for i, u := range upper {
		if u > 0 && x <= float64(u) {
			return i
		}
	}
	return len(upper) - 1
}

func ratioIndex(x float64, upper []float64) int {
	for i, u := range upper {
		if u > 0 && x <= u {
			return i
		}
	}
	return len(upper) - 1
}

// SortedEarlyCloseRows returns rows ordered for the first-match lookup:
// rows for a symbol before rows for any symbol (""), then by seconds, then
// by ratio, unbounded (0) last.
func SortedEarlyCloseRows(rows []models.EarlyCloseCalibrationRow) []models.EarlyCloseCalibrationRow {
	out := append([]models.EarlyCloseCalibrationRow(nil), rows...)
	key := func(v float64) float64 {
		if v <= 0 {
			return math.Inf(1)
		}
		return v
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Symbol == "") != (out[j].Symbol == "") {
			return out[i].Symbol != ""
		}
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		a, b := key(float64(out[i].MaxSecondsRemaining)), key(float64(out[j].MaxSecondsRemaining))
		if a != b {
			return a < b
		}
		return key(out[i].MaxVolRatio) < key(out[j].MaxVolRatio)
	})
	return out
}
