package pricing

import (
	"fmt"
	"math"
	"strconv"

	"binarytrading/internal/models"
)

// The payout formula: the owner's Dynamic Pricing specification, sections 6
// and 7 (docs/audit/pricing/pricing-spec.md). Pure - every input is passed
// in - so each term is tested at its boundaries.
//
//	vol_ratio      = sigma / baseline_vol                         (BLOCK above vol_block_ratio)
//	util_estimate  = max(0, L(D) + s * base) / max_exposure       (BLOCK above 1)
//	vol_adj        = min(max_vol_adj, k_vol * max(0, vol_ratio - 1) * expiry_factor)
//	expected_move  = max(sigma * sqrt(T) * 10^4, 0.01)            (bps)
//	spread_adj     = min(max_spread_adj, k_spread * spread_bps / expected_move)
//	hedge_adj      = hedge_cost * expiry_factor
//	exposure_adj   = min(max_exposure_adj, k_exposure * util_estimate^2)
//	margin_adj     = margin
//	raw            = base - vol_adj - spread_adj - hedge_adj - exposure_adj - margin_adj
//	payout         = floor(clamp(raw, min, max) / step + 1e-9) * step
//	final check    : max(0, L(D) + s * payout) <= max_exposure    (BLOCK otherwise)
//
// One addition: when a hedge the asset relies on is unavailable, WIDEN adds
// hedge_unavailable_adj to the deductions (REFUSE blocks) - audit finding
// F-10.

// Block codes (details.reason of the API errors, reasonCode of offers).
const (
	CodeNotPriced        = "NOT_PRICED"
	CodeTradingHalted    = "TRADING_HALTED"
	CodeSymbolHalted     = "SYMBOL_HALTED"
	CodeExpiryNotOffered = "EXPIRY_NOT_OFFERED"
	CodeStakeOutOfRange  = "STAKE_OUT_OF_RANGE"
	CodeStaleMarketData  = "STALE_MARKET_DATA"
	CodeVolatilityBlock  = "VOLATILITY_BLOCK"
	CodeExposureLimit    = "EXPOSURE_LIMIT"
	CodeHedgeUnavailable = "HEDGE_UNAVAILABLE"
	CodeGuard            = "GUARD_BLOCK"
)

// Guard refusal reasons (PricingGuard.Reason).
const (
	ReasonModelNotAvailable  = "MODEL_NOT_AVAILABLE"
	ReasonPriceSource        = "PRICE_SOURCE_MISMATCH"
	ReasonInsufficientData   = "INSUFFICIENT_DATA"
	ReasonVolatility         = "VOLATILITY_OUT_OF_CALIBRATION"
	ReasonInvalidModelOutput = "INVALID_MODEL_OUTPUT"
	ReasonEdgeBelowMinimum   = "EDGE_BELOW_MINIMUM"
)

// Rules are the global parameters (spec section 11) from one risk
// configuration version.
type Rules struct {
	Label         string
	ConfigVersion int64
	StaleAfterMs  int64

	KVol, MaxVolAdj, VolBlockRatio float64
	KSpread, MaxSpreadAdj          float64
	KExposure, MaxExposureAdj      float64
	Step                           float64
	ExpiryFactors                  map[int64]float64

	OfferTTLMs              int64
	MaxSlippageBps          float64
	DisplayTolerancePercent float64

	HedgeUnavailableAction string
	HedgeUnavailableAdj    float64

	GuardEnabled      bool
	GuardMinEdge      float64
	GuardConservative bool
	DrawPolicy        models.DrawPolicy
}

// RulesFrom reads the rules from a risk configuration.
func RulesFrom(cfg *models.RiskConfig) Rules {
	r := Rules{
		Label: cfg.PricingRuleLabel, ConfigVersion: cfg.Version, StaleAfterMs: cfg.PricingStaleAfterMs,
		KVol: cfg.PricingKVol, MaxVolAdj: cfg.PricingMaxVolAdj, VolBlockRatio: cfg.PricingVolBlockRatio,
		KSpread: cfg.PricingKSpread, MaxSpreadAdj: cfg.PricingMaxSpreadAdj,
		KExposure: cfg.PricingKExposure, MaxExposureAdj: cfg.PricingMaxExposureAdj,
		Step: cfg.PricingPayoutStep, ExpiryFactors: map[int64]float64{},
		OfferTTLMs: cfg.PricingOfferTTLMs, MaxSlippageBps: cfg.PricingMaxSlippageBps,
		DisplayTolerancePercent: cfg.PricingDisplayTolerancePercent,
		HedgeUnavailableAction:  cfg.PricingHedgeUnavailableAction, HedgeUnavailableAdj: cfg.PricingHedgeUnavailableAdj,
		GuardEnabled: cfg.PricingGuardEnabled, GuardMinEdge: cfg.PricingGuardMinEdgePercent / 100,
		GuardConservative: cfg.PricingGuardConservative,
		DrawPolicy:        cfg.DrawPolicy,
	}
	for _, f := range cfg.PricingExpiryFactors {
		r.ExpiryFactors[f.ExpirySeconds] = f.Factor
	}
	if !(r.Step > 0) {
		r.Step = 0.005
	}
	return r
}

// ExpiryFactor is the table value, 1.0 for an expiry not in the table.
func (r Rules) ExpiryFactor(expirySeconds int64) float64 {
	if f, ok := r.ExpiryFactors[expirySeconds]; ok && f > 0 {
		return f
	}
	return 1
}

// RuleVersion stamps an offer: the rule-set label, the risk configuration
// version and the payout row version, so every decision is attributable to
// one versioned, audited rule set.
func RuleVersion(label string, configVersion, payoutVersion int64) string {
	return fmt.Sprintf("%s+rc%d+p%d", label, configVersion, payoutVersion)
}

// Terms are the instrument and (asset, expiry) terms: the symbol rule with
// the payout row applied (base = the row's percent; min, max and margin from
// the row when it sets them).
type Terms struct {
	Symbol         string
	Enabled        bool
	DisabledReason string
	Base           float64
	Min            float64
	Max            float64
	Margin         float64
	BaselineVol    float64
	HedgeCost      float64
	MaxExposure    int64
}

// FindSymbolRule returns the instrument's rule, or nil.
func FindSymbolRule(cfg *models.RiskConfig, symbol string) *models.PricingSymbolRule {
	for i := range cfg.PricingSymbolRules {
		if cfg.PricingSymbolRules[i].Symbol == symbol {
			return &cfg.PricingSymbolRules[i]
		}
	}
	return nil
}

// TermsFor combines the instrument rule and the payout row. ok is false when
// the instrument has no rule.
func TermsFor(cfg *models.RiskConfig, symbol string, row *models.PayoutConfig) (Terms, bool) {
	sr := FindSymbolRule(cfg, symbol)
	if sr == nil {
		return Terms{Symbol: symbol, DisabledReason: "no pricing rule for this instrument"}, false
	}
	t := Terms{Symbol: symbol, Enabled: sr.Enabled, DisabledReason: sr.DisabledReason, Base: sr.BasePayout,
		Min: sr.MinPayout, Max: sr.MaxPayout, Margin: sr.Margin, BaselineVol: sr.BaselineVol,
		HedgeCost: sr.HedgeCost, MaxExposure: sr.MaxExposure}
	if row != nil {
		t.Base = row.PayoutPercent / 100
		if row.MinPayoutPercent != nil {
			t.Min = *row.MinPayoutPercent / 100
		}
		if row.MaxPayoutPercent != nil {
			t.Max = *row.MaxPayoutPercent / 100
		}
		if row.MarginPercent != nil {
			t.Margin = *row.MarginPercent / 100
		}
	}
	return t, true
}

// OfferInput is the market and book state one offer is computed from.
type OfferInput struct {
	Direction     models.Direction
	ExpirySeconds int64
	// Minor units; the liability is the side's L(D) before this trade
	// (0 for DEMO, which never touches the house book).
	Stake     int64
	Liability int64
	// EWMA volatility per sqrt(second) and the quote's spread.
	Sigma     float64
	SpreadBps float64
	Hedge     HedgeCostEstimate
}

// OfferResult is a computed payout or a block.
type OfferResult struct {
	OK         bool
	Code       string
	Reason     string
	Components models.PricingComponents
}

func block(c models.PricingComponents, code, reason string) OfferResult {
	c.Payout = 0
	return OfferResult{Code: code, Reason: reason, Components: c}
}

// FloorToStep rounds down to the step: floor(x / step + 1e-9) * step. The
// 1e-9 (spec 6.7) absorbs float artefacts such as 0.83 / 0.005 =
// 165.99999...; it can move the result up by at most 1e-9 steps.
func FloorToStep(x, step float64) float64 {
	// Rounded to 1e-9 to drop the representation error of n * step
	// (166 * 0.005 = 0.8300000000000001).
	return math.Round(math.Floor(x/step+1e-9)*step*1e9) / 1e9
}

// CeilToStep is the smallest multiple of step at or above x.
func CeilToStep(x, step float64) float64 {
	return math.Ceil(x/step-1e-9) * step
}

func sideName(d models.Direction) string {
	if d == models.DirectionUp {
		return "UP"
	}
	return "DOWN"
}

// ComputeOffer applies sections 6 and 7 (steps 6 to 9 of the quoting order).
func ComputeOffer(r Rules, t Terms, in OfferInput) OfferResult {
	ef := r.ExpiryFactor(in.ExpirySeconds)
	c := models.PricingComponents{
		Base: t.Base, Min: t.Min, Max: t.Max, Step: r.Step,
		Sigma: in.Sigma, BaselineVol: t.BaselineVol, SpreadBps: in.SpreadBps, ExpiryFactor: ef,
		Liability: in.Liability, MaxExposure: t.MaxExposure,
		KVol: r.KVol, MaxVolAdj: r.MaxVolAdj, VolBlockRatio: r.VolBlockRatio, KSpread: r.KSpread,
		MaxSpreadAdj: r.MaxSpreadAdj, KExposure: r.KExposure, MaxExposureAdj: r.MaxExposureAdj,
		HedgeAvailable: in.Hedge.Available, HedgeBasis: in.Hedge.Basis, HedgeVenueCost: in.Hedge.VenueCostFraction,
	}
	if !(t.BaselineVol > 0) || !finite(in.Sigma) || in.Sigma < 0 || !finite(in.SpreadBps) || in.SpreadBps < 0 || t.MaxExposure <= 0 {
		return block(c, CodeNotPriced, "the pricing inputs are not usable")
	}
	// 6. Volatility.
	c.VolRatio = in.Sigma / t.BaselineVol
	if c.VolRatio > r.VolBlockRatio {
		return block(c, CodeVolatilityBlock, fmt.Sprintf("volatility %.2fx baseline is above the %.2fx block ratio", c.VolRatio, r.VolBlockRatio))
	}
	// 7. Exposure estimate at the base payout.
	s := float64(in.Stake)
	c.UtilEstimate = math.Max(0, float64(in.Liability)+s*t.Base) / float64(t.MaxExposure)
	if c.UtilEstimate > 1 {
		return block(c, CodeExposureLimit, "exposure limit reached on "+sideName(in.Direction)+" side")
	}
	// 8. The payout.
	c.VolAdj = math.Min(r.MaxVolAdj, r.KVol*math.Max(0, c.VolRatio-1)*ef)
	c.ExpectedMoveBps = math.Max(in.Sigma*math.Sqrt(float64(in.ExpirySeconds))*1e4, 0.01)
	c.SpreadAdj = math.Min(r.MaxSpreadAdj, r.KSpread*in.SpreadBps/c.ExpectedMoveBps)
	c.HedgeCost = t.HedgeCost
	if in.Hedge.CostPoints > 0 {
		c.HedgeCost = in.Hedge.CostPoints
	}
	c.HedgeAdj = c.HedgeCost * ef
	if in.Hedge.Required && !in.Hedge.Available {
		if r.HedgeUnavailableAction == models.PricingActionRefuse {
			return block(c, CodeHedgeUnavailable, "the hedge this asset relies on is unavailable ("+in.Hedge.Reason+")")
		}
		c.HedgeUnavailableAdj = r.HedgeUnavailableAdj
	}
	c.ExposureAdj = math.Min(r.MaxExposureAdj, r.KExposure*c.UtilEstimate*c.UtilEstimate)
	c.MarginAdj = t.Margin
	c.Raw = t.Base - c.VolAdj - c.SpreadAdj - c.HedgeAdj - c.HedgeUnavailableAdj - c.ExposureAdj - c.MarginAdj
	// The lower bound is taken up to the step grid (a no-op for the
	// validated on-grid bounds), so rounding down never leaves [min, max].
	lo := math.Min(CeilToStep(t.Min, r.Step), t.Max)
	c.Clamped = math.Min(t.Max, math.Max(lo, c.Raw))
	c.Payout = FloorToStep(c.Clamped, r.Step)
	if !finite(c.Raw, c.Clamped, c.Payout) || c.Payout <= 0 {
		return block(c, CodeNotPriced, "the payout could not be computed")
	}
	// 9. Final hard check with the real payout.
	c.UtilFinal = math.Max(0, float64(in.Liability)+s*c.Payout) / float64(t.MaxExposure)
	if c.UtilFinal > 1 {
		return block(c, CodeExposureLimit, "exposure limit reached on "+sideName(in.Direction)+" side")
	}
	return OfferResult{OK: true, Components: c}
}

// PayoutPercent is the payout as a percent, rounded to the step's
// precision (to drop float noise such as 82.99999999999999).
func PayoutPercent(payout float64) float64 { return roundTo(payout*100, 6) }

// ── the calibrated guard (audit F-02, F-03) ─────────────────────────────

// GuardExtremeFactor: sigma beyond this multiple of the calibrated range is
// outside what the probability model can vouch for.
const GuardExtremeFactor = 3.0

// GuardInput is what the guard checks one offer against.
type GuardInput struct {
	Model      *models.PricingModel
	Entry      *models.PricingModelEntry
	Features   Features
	FeaturesOK bool
	SourceOK   bool
	Mismatch   bool
	Direction  models.Direction
	Payout     float64
	DrawPolicy models.DrawPolicy
	MinEdge    float64
	// Require the edge on the conservative bound (probabilities plus the
	// calibrated uncertainty add-ons) instead of the point estimate.
	Conservative bool
}

// Guard checks that the payout leaves an expected platform result of at
// least MinEdge per unit stake under the calibrated model - the payout must
// not exceed the calibrated break-even minus that margin:
//
//	w   = P(win) under the draw policy,   w_ub = w + u (+ dq where draws pay)
//	k   = P(platform keeps the stake),    k_lb = 1 - w_ub (- q_ub where draws are refunded)
//	point edge        = k - w * payout                 >= MinEdge   (default)
//	conservative edge = k_lb - w_ub * payout           >= MinEdge   (Conservative)
//	break-even payout = (k - MinEdge) / w   or   (k_lb - MinEdge) / w_ub
//
// Both edges are recorded on every offer.
// Without a calibrated model, enough data or a valid output it refuses: no
// default probability is ever substituted.
func Guard(in GuardInput) models.PricingGuard {
	g := models.PricingGuard{Enabled: true, MinEdge: in.MinEdge, Conservative: in.Conservative, SourceMismatch: in.Mismatch,
		Sigma: in.Features.Sigma, Momentum: in.Features.Momentum, ObservedSteps: in.Features.ObservedSteps}
	if in.Model != nil {
		g.ModelName, g.ModelVersion, g.ModelHash = in.Model.Name, in.Model.Version, in.Model.Hash
	}
	e := in.Entry
	if e == nil || e.Status != models.PricingEntryCalibrated {
		g.Reason = ReasonModelNotAvailable
		return g
	}
	if !in.SourceOK {
		g.Reason = ReasonPriceSource
		return g
	}
	if !in.FeaturesOK || in.Features.ObservedSteps < RequiredObservedSteps(e.Features, 40) {
		g.Reason = ReasonInsufficientData
		return g
	}
	p := ModelProbability(e, in.Features, GuardExtremeFactor)
	g.Regime = p.Regime
	if !finite(p.Up, p.Down, p.Draw, p.DrawUpper, p.Uncertainty) ||
		p.Up < MinProbability || p.Up > MaxProbability || p.Down < MinProbability || p.Down > MaxProbability ||
		p.Draw < 0 || p.Draw >= MaxDrawProbability || p.DrawUpper < p.Draw || p.DrawUpper >= MaxDrawProbability ||
		p.Uncertainty < 0 || p.Uncertainty > 0.25 {
		g.Reason = ReasonInvalidModelOutput
		return g
	}
	if p.SigmaExtreme {
		g.Reason = ReasonVolatility
		return g
	}
	win := p.Win(in.Direction)
	q, qU, u := p.Draw, p.DrawUpper, p.Uncertainty
	g.ProbWin, g.ProbDraw, g.Uncertainty, g.DrawUncertainty = win, q, u, qU-q
	var w, wU, k, kL float64
	switch in.DrawPolicy {
	case models.DrawPolicyLoss:
		w, wU = win, win+u
		k, kL = 1-win, 1-(win+u)
	case models.DrawPolicyWin:
		w, wU = win+q, win+u+qU
		k, kL = 1-win-q, 1-(win+u+qU)
	default: // REFUND: a draw returns the stake, result 0.
		w, wU = win, win+u
		k, kL = 1-win-q, 1-(win+u)-qU
	}
	g.PointEdge = k - w*in.Payout
	g.ConservativeEdge = kL - wU*in.Payout
	edge := g.PointEdge
	g.BreakEvenPayout = (k - in.MinEdge) / w
	if in.Conservative {
		edge = g.ConservativeEdge
		g.BreakEvenPayout = (kL - in.MinEdge) / wU
	}
	if !finite(g.PointEdge, g.ConservativeEdge, g.BreakEvenPayout) {
		g.Reason = ReasonInvalidModelOutput
		return g
	}
	if edge < in.MinEdge-1e-12 {
		g.Reason = ReasonEdgeBelowMinimum
		return g
	}
	g.OK = true
	return g
}

// RequiredObservedSteps is the minimum number of traded steps in the window:
// the entry's own floor or the given share of the window, whichever is
// higher.
func RequiredObservedSteps(spec models.PricingFeatureSpec, minObservedPercent float64) int {
	need := spec.MinObservedSteps
	if p := int(math.Ceil(minObservedPercent / 100 * float64(spec.WindowSteps))); p > need {
		need = p
	}
	return need
}

// MaxDrawProbability bounds the draw estimate the guard accepts.
const MaxDrawProbability = 0.95

func finite(xs ...float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}

func f4(x float64) string { return strconv.FormatFloat(x, 'f', 4, 64) }

// roundTo rounds half away from zero to the given decimals (display figures
// only; money never goes through it).
func roundTo(x float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(x*p) / p
}
