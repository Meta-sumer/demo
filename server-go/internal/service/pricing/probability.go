package pricing

import (
	"math"

	"binarytrading/internal/models"
)

// The win-probability model (docs/audit/pricing/pricing-spec.md, section 4).
//
// Outcome: with E the entry price and X the expiry price (the last tick at or
// before expiry, settlement's rule), UP wins when X > E, DOWN wins when
// X < E, and X == E is a draw, settled under the draw policy.
//
//	P(draw)          = q = 1 / (1 + exp(-(c + d * ln sigma + g * logit(local draw rate))))
//	P(UP | no draw)  = 1 / (1 + exp(-(a + b * z)))              (logistic on the momentum z-score)
//	P(UP)            = (1 - q) * P(UP | no draw)
//	P(DOWN)          = (1 - q) * (1 - P(UP | no draw))
//
// a, b, c, d and g are fitted per (instrument, expiry) by cmd/calibrate on the
// training period; the uncertainty add-ons u (win) and dq (draw) per
// volatility regime are measured on the later holdout period.

// Probability bounds: an estimate outside them is not trusted.
const (
	MinProbability = 0.01
	MaxProbability = 0.99
)

// Probability is the model's view of one (instrument, expiry) right now.
type Probability struct {
	Up        float64 `json:"up"`
	Down      float64 `json:"down"`
	Draw      float64 `json:"draw"`
	DrawUpper float64 `json:"drawUpper"`
	// Additive uncertainty on a win probability (both directions).
	Uncertainty float64 `json:"uncertainty"`
	Regime      string  `json:"regime"`
	// Sigma inside the calibrated range; outside it by more than the extreme
	// factor.
	SigmaInRange bool `json:"sigmaInRange"`
	SigmaExtreme bool `json:"sigmaExtreme"`
}

// Win is the strict win probability of a direction.
func (p Probability) Win(d models.Direction) float64 {
	if d == models.DirectionUp {
		return p.Up
	}
	return p.Down
}

func sigmoid(x float64) float64 {
	if x >= 0 {
		return 1 / (1 + math.Exp(-x))
	}
	e := math.Exp(x)
	return e / (1 + e)
}

// RegimeFor picks the volatility regime: the first whose upper bound holds
// sigma, the last (unbounded) otherwise. nil when the entry has none.
func RegimeFor(e *models.PricingModelEntry, sigma float64) *models.PricingRegime {
	if len(e.Regimes) == 0 {
		return nil
	}
	for i := range e.Regimes {
		r := &e.Regimes[i]
		if r.SigmaUpTo > 0 && sigma <= r.SigmaUpTo {
			return r
		}
	}
	return &e.Regimes[len(e.Regimes)-1]
}

// DrawProbability is the modelled P(expiry price == entry price).
func DrawProbability(e *models.PricingModelEntry, f Features) float64 {
	if !(f.Sigma > 0) {
		return math.NaN()
	}
	x := e.DrawIntercept + e.DrawSigmaCoef*math.Log(f.Sigma)
	if e.Features.DrawLagSteps > 0 {
		if !(f.LocalDrawRate > 0 && f.LocalDrawRate < 1) {
			return math.NaN()
		}
		x += e.DrawLocalCoef * logit(f.LocalDrawRate)
	}
	return sigmoid(x)
}

func logit(p float64) float64 { return math.Log(p / (1 - p)) }

// ModelProbability applies an entry to the features. extremeFactor bounds
// how far outside its calibrated range sigma may be before the price is
// refused outright (SigmaExtreme).
func ModelProbability(e *models.PricingModelEntry, f Features, extremeFactor float64) Probability {
	out := Probability{Up: math.NaN(), Down: math.NaN()}
	reg := RegimeFor(e, f.Sigma)
	if reg == nil {
		return out
	}
	out.Regime = reg.Name
	out.Draw = DrawProbability(e, f)
	out.DrawUpper = out.Draw + reg.DrawUncertainty
	out.Uncertainty = reg.Uncertainty
	out.SigmaInRange = f.Sigma >= e.SigmaMin && f.Sigma <= e.SigmaMax
	if extremeFactor < 1 {
		extremeFactor = 1
	}
	out.SigmaExtreme = f.Sigma < e.SigmaMin/extremeFactor || f.Sigma > e.SigmaMax*extremeFactor

	up := sigmoid(e.Intercept + e.MomentumCoef*f.Momentum)
	out.Up = (1 - out.Draw) * up
	out.Down = (1 - out.Draw) * (1 - up)
	return out
}
