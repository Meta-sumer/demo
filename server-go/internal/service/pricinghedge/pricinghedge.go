// Package pricinghedge connects the hedging service to the pricing engine
// (audit finding F-10; docs/audit/hedging/hedging-spec.md section 7.4,
// docs/audit/pricing/pricing-spec.md section 9).
//
// Two hooks are installed at server start (Install, called from cmd/server):
//
//   - pricing.SetHedgeCostEstimator: whether a hedge can be placed now comes
//     from the hedging service (hedging.CostEstimateFor: venue symbol, hedging
//     enabled, keys and live switch, INR rate fresh, venue snapshot fresh,
//     round-trip cost within HEDGE_MAX_COST_BPS). A hedge that is unavailable
//     or too expensive makes the engine widen or refuse the price on an asset
//     that relies on hedging (pricingHedgeUnavailableAction). The charge stays
//     the instrument's configured hedge_cost (CostPoints 0) until the
//     per-trade model is calibrated on real fills; the live venue cost is
//     reported next to it.
//   - risk.HedgeSigmaSource: the hedge target's delta uses the pricing
//     engine's volatility (the EWMA, else the instrument's baseline), so the
//     hedge and the payout are computed on the same sigma.
//
// The package exists because pricing and hedging must not import each other.
package pricinghedge

import (
	"context"

	"binarytrading/internal/service/hedging"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/risk"
)

// Basis is reported with every estimate.
const Basis = "configured hedge_cost; availability and venue cost from the hedging service"

// costFunc is hedging.CostEstimateFor (tests replace it).
type costFunc func(ctx context.Context, q hedging.CostQuery) (hedging.HedgeCost, error)

// Estimator is the pricing.HedgeCostEstimator backed by the hedging service.
type Estimator struct {
	cost costFunc
}

// NewEstimator returns the estimator over hedging.CostEstimateFor.
func NewEstimator() Estimator { return Estimator{cost: hedging.CostEstimateFor} }

// EstimateHedgeCost implements pricing.HedgeCostEstimator. An error is
// treated by the engine as "hedge unavailable".
func (e Estimator) EstimateHedgeCost(ctx context.Context, r pricing.HedgeCostRequest) (pricing.HedgeCostEstimate, error) {
	out := pricing.HedgeCostEstimate{Available: true, Basis: Basis, Provider: "hedging"}
	if r.Asset == nil || !r.Asset.HedgeEnabled {
		// Not hedged: nothing is required, the configured cost applies.
		return out, nil
	}
	out.Required = true
	cost := e.cost
	if cost == nil {
		cost = hedging.CostEstimateFor
	}
	hold := r.ExpirySeconds
	if hold <= 0 {
		hold = hedging.DefaultHoldSeconds
	}
	c, err := cost(ctx, hedging.CostQuery{AssetID: r.Asset.ID.Hex(), HoldSeconds: hold})
	if err != nil {
		return pricing.HedgeCostEstimate{}, err
	}
	// NOT_HEDGEABLE, HEDGE_DISABLED, HEDGE_NOT_CONFIGURED,
	// LIVE_EXECUTION_DISABLED, FX_MISSING, FX_STALE, VENUE_UNAVAILABLE,
	// VENUE_STALE and COST_LIMIT (available but too expensive) all mean the
	// platform cannot hedge this trade at an acceptable cost now.
	out.Available = c.Available && !c.Expensive
	out.Reason = c.Reason
	if c.Venue != "" {
		out.Provider = c.Venue
	}
	out.VenueCostFraction = c.RoundTripBps / 10_000
	return out, nil
}

// Sigma is the risk.SigmaSourceFunc over the pricing engine's volatility.
func Sigma(ctx context.Context, assetID string) (float64, string, bool) {
	return pricing.Sigma(ctx, assetID)
}

// Install wires both hooks. Call once at server start, before the workers.
func Install() {
	pricing.SetHedgeCostEstimator(NewEstimator())
	risk.HedgeSigmaSource = Sigma
}

// Uninstall restores the defaults (tests).
func Uninstall() {
	pricing.SetHedgeCostEstimator(nil)
	risk.HedgeSigmaSource = nil
}
