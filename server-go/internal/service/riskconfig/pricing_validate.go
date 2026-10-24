package riskconfig

import (
	"context"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
)

// Operator-facing bounds on the pricing engine (docs/audit/pricing/pricing-spec.md
// section 11), applied to what a patch changes. The admin console edits every
// one of these fields (Settings, "Pricing engine"); the admin API bounds each
// value on its own, ValidateConfig applies the structural rules to the whole
// configuration, and these are the rules that keep an edited value from
// quietly meaning something else to the engine.
//
// Only the fields the patch sets are checked here, against the configuration
// the patch produces. A halt or an unrelated edit is therefore never refused
// because of a value stored under an older, looser rule: the kill switch
// (POST /admin/pricing/halt, a risk configuration version too) must always
// go through.

// Bounds on the pricing tables.
const (
	MaxPricingExpiryFactors = 32
	MaxPricingSymbolRules   = 64
)

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func validatePricingPatch(p *RiskConfigPatch, c *models.RiskConfig) error {
	bad := func(msg string) error { return apperr.Validation(msg, nil) }

	// 0 is not "off" for these two: the normaliser falls back to its
	// built-in value (marketdata.NormParamsFrom), so the screen would say
	// one thing and the feed do another.
	if p.PricingMaxSpreadBps != nil && !(c.PricingMaxSpreadBps > 0) {
		return bad("pricingMaxSpreadBps must be above 0 (0 is not off: the feed would use its built-in 8 bps)")
	}
	if p.PricingPendingOutlierMaxAgeMs != nil && c.PricingPendingOutlierMaxAgeMs < 100 {
		return bad("pricingPendingOutlierMaxAgeMs must be at least 100 (0 is not off: the feed would use its built-in 5000 ms)")
	}

	// No negative costs or deductions: a negative one would raise payouts.
	nonNegative := []struct {
		set   bool
		name  string
		value float64
	}{
		{p.PricingKVol != nil, "pricingKVol", c.PricingKVol},
		{p.PricingMaxVolAdj != nil, "pricingMaxVolAdj", c.PricingMaxVolAdj},
		{p.PricingKSpread != nil, "pricingKSpread", c.PricingKSpread},
		{p.PricingMaxSpreadAdj != nil, "pricingMaxSpreadAdj", c.PricingMaxSpreadAdj},
		{p.PricingKExposure != nil, "pricingKExposure", c.PricingKExposure},
		{p.PricingMaxExposureAdj != nil, "pricingMaxExposureAdj", c.PricingMaxExposureAdj},
		{p.PricingHedgeUnavailableAdj != nil, "pricingHedgeUnavailableAdj", c.PricingHedgeUnavailableAdj},
		{p.PricingHedgeTakerFeeBps != nil, "pricingHedgeTakerFeeBps", c.PricingHedgeTakerFeeBps},
		{p.PricingHedgeSpreadBps != nil, "pricingHedgeSpreadBps", c.PricingHedgeSpreadBps},
		{p.PricingHedgeSlippageBps != nil, "pricingHedgeSlippageBps", c.PricingHedgeSlippageBps},
		{p.PricingMaxSlippageBps != nil, "pricingMaxSlippageBps", c.PricingMaxSlippageBps},
		{p.PricingDisplayTolerancePercent != nil, "pricingDisplayTolerancePercent", c.PricingDisplayTolerancePercent},
		{p.PricingVolSampleIntervalMs != nil, "pricingVolSampleIntervalMs", float64(c.PricingVolSampleIntervalMs)},
	}
	for _, f := range nonNegative {
		if f.set && !(f.value >= 0) {
			return bad(f.name + " cannot be negative")
		}
	}

	if p.PricingExpiryFactors != nil && len(c.PricingExpiryFactors) > MaxPricingExpiryFactors {
		return bad("pricingExpiryFactors may have at most " + itoa(MaxPricingExpiryFactors) + " rows")
	}

	if p.PricingSymbolRules != nil {
		if len(c.PricingSymbolRules) > MaxPricingSymbolRules {
			return bad("pricingSymbolRules may have at most " + itoa(MaxPricingSymbolRules) + " rules")
		}
		for _, r := range c.PricingSymbolRules {
			prefix := "pricingSymbolRules " + r.Symbol + ": "
			if !(r.MinPayout <= r.BasePayout && r.BasePayout <= r.MaxPayout && r.MaxPayout <= 1) {
				return bad(prefix + "need minPayout <= basePayout <= maxPayout <= 1 (100%); got " +
					ftoa(r.MinPayout) + ", " + ftoa(r.BasePayout) + ", " + ftoa(r.MaxPayout))
			}
			if r.MinStake < 0 || r.MaxStake < 0 {
				return bad(prefix + "minStake and maxStake cannot be negative (0 = no limit of its own)")
			}
			if r.MinStake > 0 && r.MaxStake > 0 && r.MinStake > r.MaxStake {
				return bad(prefix + "minStake cannot exceed maxStake")
			}
		}
	}
	return nil
}

// checkRuleSymbols refuses a pricing rule for an instrument the platform does
// not list: a typo ("BTC/USDT") would be stored and never apply, leaving the
// instrument meant without the change. A symbol that already has a rule stays
// accepted, so a rule whose asset was removed can still be saved alongside
// other edits.
func checkRuleSymbols(ctx context.Context, before *models.RiskConfig, rules []models.PricingSymbolRule) error {
	known := map[string]bool{}
	for _, r := range before.PricingSymbolRules {
		known[r.Symbol] = true
	}
	ask := []string{}
	for _, r := range rules {
		if !known[r.Symbol] {
			ask = append(ask, r.Symbol)
		}
	}
	if len(ask) == 0 {
		return nil
	}
	cur, err := mongox.C(models.CollAssets).Find(ctx, bson.M{"symbol": bson.M{"$in": ask}},
		options.Find().SetProjection(bson.M{"symbol": 1}))
	if err != nil {
		return err
	}
	var listed []struct {
		Symbol string `bson:"symbol"`
	}
	if err := cur.All(ctx, &listed); err != nil {
		return err
	}
	for _, a := range listed {
		known[a.Symbol] = true
	}
	unknown := []string{}
	for _, s := range ask {
		if !known[s] {
			unknown = append(unknown, s)
		}
	}
	if len(unknown) > 0 {
		return apperr.Validation("pricingSymbolRules: unknown symbol "+strings.Join(unknown, ", ")+
			" (no asset is listed under it)", map[string]any{"unknownSymbols": unknown})
	}
	return nil
}
