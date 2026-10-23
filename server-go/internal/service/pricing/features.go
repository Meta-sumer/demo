package pricing

import (
	"math"

	"binarytrading/internal/models"
)

// Features are the model inputs at the moment of pricing, built from a price
// grid by ComputeFeatures. The calibration CLI and the live engine both call
// it, so the model is applied to exactly the quantity it was fitted on.
type Features struct {
	// Per-step standard deviation of log returns over the window
	// (dimensionless; per second when the resolution is 1 s).
	Sigma float64 `json:"sigma"`
	// Normalised momentum: ln(price / close M steps ago) / (Sigma * sqrt(M)),
	// clipped to +-MomentumClip. Dimensionless (a z-score).
	Momentum float64 `json:"momentum"`
	// Share of pairs of window prices spec.DrawLagSteps apart that were
	// exactly equal, Jeffreys-smoothed ((k + 0.5) / (n + 1)); 0 when unused.
	LocalDrawRate float64 `json:"localDrawRate"`
	// Steps in the window with at least one trade, and log returns used.
	ObservedSteps int `json:"observedSteps"`
	Returns       int `json:"returns"`
	// The current price the features end at.
	Price float64 `json:"price"`
}

// MomentumClip bounds the momentum z-score, so one bad print cannot drive
// the logistic to a certainty.
const MomentumClip = 6.0

// minReturns is the floor below which a standard deviation is not computed.
const minReturns = 30

// ComputeFeatures turns the window into features.
//
// closes holds the spec.WindowSteps completed steps before now, oldest first:
// closes[i] is the last trade price in step i, NaN when nothing traded.
// observed[i] says a trade happened in step i. price is the current price
// (the trade that prices the entry). A step without a trade carries the
// previous close forward; steps before the first trade are skipped.
//
// ok is false when the window cannot support a volatility estimate (too few
// observed steps or returns, a non-positive price, or zero movement); the
// engine then refuses with INSUFFICIENT_DATA rather than guessing.
func ComputeFeatures(spec models.PricingFeatureSpec, closes []float64, observed []bool, price float64) (Features, bool) {
	f := Features{Price: price}
	if !(price > 0) || math.IsInf(price, 0) || len(closes) == 0 {
		return f, false
	}
	for i := range closes {
		if i < len(observed) && observed[i] {
			f.ObservedSteps++
		}
	}
	// Forward-filled series with the current price appended.
	series := make([]float64, 0, len(closes)+1)
	last := math.NaN()
	for _, c := range closes {
		if c > 0 && !math.IsInf(c, 0) {
			last = c
		}
		series = append(series, last)
	}
	series = append(series, price)

	var sum, sumSq float64
	n := 0
	for i := 1; i < len(series); i++ {
		a, b := series[i-1], series[i]
		if math.IsNaN(a) || math.IsNaN(b) {
			continue
		}
		r := math.Log(b / a)
		sum += r
		sumSq += r * r
		n++
	}
	f.Returns = n
	if n < minReturns || f.ObservedSteps < spec.MinObservedSteps {
		return f, false
	}
	mean := sum / float64(n)
	variance := (sumSq - float64(n)*mean*mean) / float64(n-1)
	if !(variance > 0) {
		return f, false
	}
	f.Sigma = math.Sqrt(variance)

	m := spec.MomentumSteps
	if m <= 0 || m > len(closes) {
		m = len(closes)
	}
	ref := series[len(series)-1-m]
	if math.IsNaN(ref) || !(ref > 0) {
		f.Momentum = 0
	} else {
		z := math.Log(price/ref) / (f.Sigma * math.Sqrt(float64(m)))
		f.Momentum = math.Max(-MomentumClip, math.Min(MomentumClip, z))
	}
	if math.IsNaN(f.Momentum) || math.IsInf(f.Momentum, 0) {
		return f, false
	}
	if lag := spec.DrawLagSteps; lag > 0 && lag < len(series) {
		var k, pairs float64
		for i := 0; i+lag < len(series); i++ {
			a, b := series[i], series[i+lag]
			if math.IsNaN(a) || math.IsNaN(b) {
				continue
			}
			pairs++
			if a == b {
				k++
			}
		}
		f.LocalDrawRate = (k + 0.5) / (pairs + 1)
	}
	return f, true
}

// WindowAt cuts the calibration window that ends at step k of a forward
// filled series: the spec.WindowSteps closes before k, their observed flags,
// and the price at k (the entry). ok is false when k is too early.
func WindowAt(s *Series, filled []float64, spec models.PricingFeatureSpec, k int) ([]float64, []bool, float64, bool) {
	w := spec.WindowSteps
	if k-w < 0 || k >= len(filled) {
		return nil, nil, 0, false
	}
	closes := make([]float64, w)
	observed := make([]bool, w)
	for i := 0; i < w; i++ {
		j := k - w + i
		if s.Observed(j) {
			closes[i] = s.Close[j]
			observed[i] = true
		} else {
			closes[i] = math.NaN()
		}
	}
	return closes, observed, filled[k], !math.IsNaN(filled[k])
}
