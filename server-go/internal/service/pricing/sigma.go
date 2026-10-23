package pricing

import (
	"context"

	"binarytrading/internal/models"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/riskconfig"
)

// Volatility sources reported with a sigma.
const (
	SigmaSourceEWMA     = "pricing-ewma"
	SigmaSourceBaseline = "pricing-baseline"
)

// sigmaFrom is the engine's volatility (spec 5): the shared EWMA when one is
// stored, otherwise the instrument's baseline. Per sqrt(second).
func sigmaFrom(ctx context.Context, assetID string, baseline float64) (float64, string, error) {
	v, ok, err := marketdata.GetVolatility(ctx, assetID)
	if err != nil {
		return 0, "", err
	}
	if ok && v.Var > 0 {
		return v.Sigma(), SigmaSourceEWMA, nil
	}
	return baseline, SigmaSourceBaseline, nil
}

// Sigma is the volatility the engine prices an asset with, per
// sqrt(second), and where it came from. ok is false when the asset has no
// pricing rule (no baseline) and no EWMA state. The hedge target uses it
// (risk.HedgeSigmaSource, installed by service/pricinghedge), so the hedge
// and the payout are computed on the same volatility.
func Sigma(ctx context.Context, assetID string) (sigma float64, source string, ok bool) {
	asset, err := assets.RequireAssetDoc(ctx, assetID)
	if err != nil {
		return 0, "", false
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return 0, "", false
	}
	terms, _ := TermsFor(cfg, asset.Symbol, nil)
	s, src, err := sigmaFrom(ctx, assetID, terms.BaselineVol)
	if err != nil || !(s > 0) {
		return 0, "", false
	}
	return s, src, true
}

// SymbolPriced reports whether the engine can price a symbol at all (an
// enabled instrument rule) and, if not, why. The launch scope prices BTC/USD
// and ETH/USD only.
func SymbolPriced(cfg *models.RiskConfig, symbol string) (bool, string) {
	terms, known := TermsFor(cfg, symbol, nil)
	if known && terms.Enabled {
		return true, ""
	}
	reason := terms.DisabledReason
	if reason == "" {
		reason = "not priced"
	}
	return false, reason
}
