package pricing

import (
	"context"
	"math"
	"sync"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
)

// Hedge cost hook (audit finding F-10, pricing side).
//
// The payout formula deducts hedge_adj = hedge_cost x expiry_factor (spec
// 6.3). The cost comes from a HedgeCostEstimator: the default charges each
// instrument's configured hedge_cost (versioned risk configuration) and
// reports the Binance venue basis next to it; the hedging service installs
// its own estimator at startup with SetHedgeCostEstimator, returning the
// venue's live cost (CostPoints) and whether a hedge can be placed now
// (Available). When the asset relies on hedging and the hedge is unavailable
// the engine widens (pricingHedgeUnavailableAdj) or blocks
// (pricingHedgeUnavailableAction = REFUSE). Nothing here places orders.

// HedgeCostRequest describes the trade being priced. Money is minor units.
type HedgeCostRequest struct {
	Asset         *models.Asset
	Direction     models.Direction
	Stake         int64
	ExpirySeconds int64
	// The side's worst-case loss before this trade (spec 7.1).
	Liability int64
	// Current mid of the instrument.
	Price  float64
	Config *models.RiskConfig
}

// HedgeCostEstimate is the estimator's answer.
type HedgeCostEstimate struct {
	// The asset relies on hedging (an unavailable hedge then widens or
	// blocks the price).
	Required bool `json:"required"`
	// A hedge can be placed now.
	Available bool `json:"available"`
	// Hedge cost in payout points (fraction of the stake) before the expiry
	// factor; 0 = charge the instrument's configured hedge_cost.
	CostPoints float64 `json:"costPoints"`
	// Round-trip venue cost per unit of hedged notional (information only).
	VenueCostFraction float64 `json:"venueCostFraction"`
	// Why the hedge is unavailable, and what the figures are based on.
	Reason   string `json:"reason,omitempty"`
	Basis    string `json:"basis"`
	Provider string `json:"provider"`
}

// HedgeCostEstimator estimates the hedge cost of one trade. Implementations
// must be safe for concurrent use and fast (it runs on every quote). An
// error is treated as "hedge unavailable".
type HedgeCostEstimator interface {
	EstimateHedgeCost(ctx context.Context, req HedgeCostRequest) (HedgeCostEstimate, error)
}

var (
	estimatorMu sync.RWMutex
	estimator   HedgeCostEstimator = DefaultHedgeCostEstimator{}
)

// SetHedgeCostEstimator installs the estimator the engine uses; nil restores
// the default.
func SetHedgeCostEstimator(e HedgeCostEstimator) {
	estimatorMu.Lock()
	if e == nil {
		e = DefaultHedgeCostEstimator{}
	}
	estimator = e
	estimatorMu.Unlock()
}

// CurrentHedgeCostEstimator returns the installed estimator.
func CurrentHedgeCostEstimator() HedgeCostEstimator {
	estimatorMu.RLock()
	defer estimatorMu.RUnlock()
	return estimator
}

// DefaultHedgeCostEstimator uses configuration only:
//
//   - cost: the instrument's configured hedge_cost (CostPoints 0);
//   - venue basis (reported): Binance, per side the taker fee + half spread +
//     slippage (risk config pricingHedge*Bps), twice for the round trip;
//   - Required: the asset has hedging enabled;
//   - Available: the paper venue always; the binance venue with its
//     credentials (and HEDGE_LIVE_EXECUTION_ENABLED for the live exchange);
//     only crypto has a venue.
type DefaultHedgeCostEstimator struct{}

func (DefaultHedgeCostEstimator) EstimateHedgeCost(_ context.Context, req HedgeCostRequest) (HedgeCostEstimate, error) {
	return defaultEstimate(req, config.E), nil
}

func defaultEstimate(req HedgeCostRequest, env *config.Env) HedgeCostEstimate {
	provider := "paper"
	if env != nil && env.HedgeProvider != "" {
		provider = env.HedgeProvider
	}
	out := HedgeCostEstimate{Available: true, Provider: provider, Basis: "configured hedge_cost"}
	if cfg := req.Config; cfg != nil {
		perSide := math.Max(0, cfg.PricingHedgeTakerFeeBps) + math.Max(0, cfg.PricingHedgeSpreadBps) + math.Max(0, cfg.PricingHedgeSlippageBps)
		out.VenueCostFraction = 2 * perSide / 10_000
		out.Basis = "configured hedge_cost; venue basis Binance taker fee + half spread + slippage, round trip"
	}
	if req.Asset == nil || !req.Asset.HedgeEnabled {
		return out
	}
	out.Required = true
	if req.Asset.Category != models.AssetCategoryCrypto {
		out.Available, out.Reason = false, "NO_HEDGE_VENUE"
		return out
	}
	if provider != "paper" {
		hasKeys := env != nil && env.HedgeAPIKey != "" && env.HedgeAPISecret != ""
		live := env != nil && env.HedgeBinanceEnv == "live"
		switch {
		case !hasKeys:
			out.Available, out.Reason = false, "HEDGE_NOT_CONFIGURED"
		case live && !env.HedgeLiveExecutionEnabled:
			out.Available, out.Reason = false, "LIVE_EXECUTION_DISABLED"
		}
	}
	return out
}
