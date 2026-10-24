package riskconfig

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/testsupport"
)

// checkPatch is UpdateRiskConfig's validation without the store: the patch
// applied to the defaults, the structural rules, then the pricing rules.
func checkPatch(p RiskConfigPatch, base func(c *models.RiskConfig)) error {
	c := models.DefaultRiskConfig()
	if base != nil {
		base(&c)
	}
	p.Apply(&c)
	if err := ValidateConfig(&c); err != nil {
		return err
	}
	return validatePricingPatch(&p, &c)
}

func ptr[T any](v T) *T { return &v }

func ef(expirySeconds int64, factor float64) models.PricingExpiryFactor {
	return models.PricingExpiryFactor{ExpirySeconds: expirySeconds, Factor: factor}
}

// rulesWith returns the default instrument rules with BTC/USD changed.
func rulesWith(edit func(r *models.PricingSymbolRule)) []models.PricingSymbolRule {
	rules := models.DefaultPricingSymbolRules()
	edit(&rules[0])
	return rules
}

// Every pricing parameter the admin console edits: what is refused, with
// which message, and what goes through.
func TestPricingPatchValidation(t *testing.T) {
	refused := []struct {
		name  string
		patch RiskConfigPatch
		want  string
	}{
		{"lambda 1", RiskConfigPatch{PricingEwmaLambda: ptr(1.0)}, "pricingEwmaLambda must be between 0 and 1"},
		{"lambda 0", RiskConfigPatch{PricingEwmaLambda: ptr(0.0)}, "pricingEwmaLambda must be between 0 and 1"},
		{"step 0", RiskConfigPatch{PricingPayoutStep: ptr(0.0)}, "pricingPayoutStep must be greater than 0"},
		{"step off the bounds' grid", RiskConfigPatch{PricingPayoutStep: ptr(0.02)}, "multiples of pricingPayoutStep"},
		{"offer ttl under 500 ms", RiskConfigPatch{PricingOfferTTLMs: ptr(int64(499))}, "pricingOfferTtlMs at least 500"},
		{"stale after under 100 ms", RiskConfigPatch{PricingStaleAfterMs: ptr(int64(50))}, "pricingStaleAfterMs at least 100"},
		{"no jump confirmation", RiskConfigPatch{PricingJumpConfirmations: ptr(int64(0))}, "pricingJumpConfirmations must be at least 1"},
		{"block ratio 1", RiskConfigPatch{PricingVolBlockRatio: ptr(1.0)}, "pricingVolBlockRatio must be above 1"},
		{"max spread 0", RiskConfigPatch{PricingMaxSpreadBps: ptr(0.0)}, "pricingMaxSpreadBps must be above 0"},
		{"pending outlier age 0", RiskConfigPatch{PricingPendingOutlierMaxAgeMs: ptr(int64(0))}, "pricingPendingOutlierMaxAgeMs must be at least 100"},
		{"negative k_vol", RiskConfigPatch{PricingKVol: ptr(-0.01)}, "pricingKVol cannot be negative"},
		{"negative exposure cap", RiskConfigPatch{PricingMaxExposureAdj: ptr(-0.1)}, "pricingMaxExposureAdj cannot be negative"},
		{"negative hedge outage", RiskConfigPatch{PricingHedgeUnavailableAdj: ptr(-0.03)}, "pricingHedgeUnavailableAdj cannot be negative"},
		{"negative taker fee", RiskConfigPatch{PricingHedgeTakerFeeBps: ptr(-1.0)}, "pricingHedgeTakerFeeBps cannot be negative"},
		{"negative sample interval", RiskConfigPatch{PricingVolSampleIntervalMs: ptr(int64(-1))}, "pricingVolSampleIntervalMs cannot be negative"},
		{"mode", RiskConfigPatch{PricingMode: ptr("AUTO")}, "pricingMode must be DYNAMIC or FIXED"},
		{"hedge action", RiskConfigPatch{PricingHedgeUnavailableAction: ptr("IGNORE")}, "WIDEN or REFUSE"},
		{"factor 0", RiskConfigPatch{PricingExpiryFactors: []models.PricingExpiryFactor{{ExpirySeconds: 30, Factor: 0}}}, "pricingExpiryFactors"},
		{"expiry twice", RiskConfigPatch{PricingExpiryFactors: []models.PricingExpiryFactor{ef(30, 1.3), ef(30, 1.1)}}, "each expiry once"},
		{"too many factors", RiskConfigPatch{PricingExpiryFactors: func() []models.PricingExpiryFactor {
			out := []models.PricingExpiryFactor{}
			for i := int64(1); i <= MaxPricingExpiryFactors+1; i++ {
				out = append(out, models.PricingExpiryFactor{ExpirySeconds: i * 10, Factor: 1})
			}
			return out
		}()}, "at most 32 rows"},
		{"base above max", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.BasePayout = 0.9 })},
			"BTC/USD: need minPayout <= basePayout <= maxPayout <= 1 (100%); got 0.65, 0.9, 0.88"},
		{"base below min", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.BasePayout = 0.6 })},
			"minPayout <= basePayout"},
		{"max above 100%", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MaxPayout, r.BasePayout = 1.05, 1.0 })},
			"maxPayout <= 1"},
		{"min above max", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MinPayout = 0.9 })},
			"0 < minPayout <= maxPayout"},
		{"min off the step grid", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MinPayout = 0.652 })},
			"multiples of pricingPayoutStep"},
		{"negative hedge cost", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.HedgeCost = -0.01 })},
			"hedgeCost >= 0"},
		{"negative margin", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.Margin = -0.01 })},
			"margin >= 0"},
		{"no baseline", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.BaselineVol = 0 })},
			"baselineVol > 0"},
		{"no exposure limit", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MaxExposure = 0 })},
			"maxExposure > 0"},
		{"negative stake", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MinStake = -1 })},
			"minStake and maxStake cannot be negative"},
		{"stake range inverted", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MinStake, r.MaxStake = 500_00, 100_00 })},
			"minStake cannot exceed maxStake"},
		{"symbol twice", RiskConfigPatch{PricingSymbolRules: append(models.DefaultPricingSymbolRules(), models.DefaultPricingSymbolRules()[0])},
			"each symbol once"},
		{"early-close floor", RiskConfigPatch{EarlyCloseMinProbabilityPercent: ptr(0.0)}, "earlyCloseMinProbabilityPercent"},
	}
	for _, c := range refused {
		t.Run(c.name, func(t *testing.T) {
			err := checkPatch(c.patch, nil)
			e, ok := apperr.As(err)
			if !ok || e.Code != apperr.ValidationError || e.StatusCode != 422 || !strings.Contains(e.Message, c.want) {
				t.Fatalf("want a validation error containing %q, got %v", c.want, err)
			}
		})
	}

	accepted := []struct {
		name  string
		patch RiskConfigPatch
		base  func(c *models.RiskConfig)
	}{
		{"every scalar at an operator value", RiskConfigPatch{
			PricingMode: ptr(models.PricingModeFixed), PricingRuleLabel: ptr("rules-2026.11.1"),
			PricingStaleAfterMs: ptr(int64(1500)), PricingMaxSpreadBps: ptr(6.5), PricingMaxJumpBps: ptr(120.0),
			PricingJumpConfirmations: ptr(int64(4)), PricingPendingOutlierMaxAgeMs: ptr(int64(4000)),
			PricingEwmaLambda: ptr(0.95), PricingVolSampleIntervalMs: ptr(int64(0)),
			PricingKVol: ptr(0.07), PricingMaxVolAdj: ptr(0.1), PricingVolBlockRatio: ptr(3.5),
			PricingKSpread: ptr(0.025), PricingMaxSpreadAdj: ptr(0.04), PricingKExposure: ptr(0.09), PricingMaxExposureAdj: ptr(0.12),
			PricingPayoutStep: ptr(0.005), PricingOfferTTLMs: ptr(int64(2500)), PricingMaxSlippageBps: ptr(3.0),
			PricingDisplayTolerancePercent: ptr(0.5), PricingGuardEnabled: ptr(false), PricingGuardMinEdgePercent: ptr(1.5),
			PricingGuardConservative: ptr(true), PricingHedgeUnavailableAction: ptr(models.PricingActionRefuse),
			PricingHedgeUnavailableAdj: ptr(0.04), PricingHedgeTakerFeeBps: ptr(4.5), PricingHedgeSpreadBps: ptr(0.0),
			PricingHedgeSlippageBps: ptr(1.0), EarlyCloseMinProbabilityPercent: ptr(0.1),
		}, nil},
		{"expiry factors replaced", RiskConfigPatch{PricingExpiryFactors: []models.PricingExpiryFactor{ef(30, 1.25), ef(60, 1), ef(900, 0.75)}}, nil},
		{"expiry factors cleared (1.0 everywhere)", RiskConfigPatch{PricingExpiryFactors: []models.PricingExpiryFactor{}}, nil},
		{"instrument rule edited with a stake range", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) {
			r.BasePayout, r.MinPayout, r.MaxPayout, r.HedgeCost, r.Margin = 0.87, 0.7, 0.9, 0.03, 0.04
			r.BaselineVol, r.MaxExposure, r.MinStake, r.MaxStake = 5e-5, 1_000_000_00, 50_00, 50_000_00
		})}, nil},
		{"base at both bounds", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.BasePayout, r.MinPayout = 0.88, 0.88 })}, nil},
		{"payout of 100%", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.BasePayout, r.MaxPayout = 1, 1 })}, nil},
		{"only a maximum stake", RiskConfigPatch{PricingSymbolRules: rulesWith(func(r *models.PricingSymbolRule) { r.MaxStake = 2_000_00 })}, nil},
		{"a new disabled rule", RiskConfigPatch{PricingSymbolRules: append(models.DefaultPricingSymbolRules(), models.PricingSymbolRule{
			Symbol: "XAU/USD", DisabledReason: "not offered at launch", BasePayout: 0.9, MinPayout: 0.7, MaxPayout: 0.92,
			BaselineVol: 1.25e-5, HedgeCost: 0.02, Margin: 0.03, MaxExposure: 2_500_000_00})}, nil},
		// A stored rule from before the 100% bound (the API allowed up
		// to 5) does not block a halt or an unrelated change.
		{"halt over a legacy rule", RiskConfigPatch{TradingHalted: ptr(true), TradingHaltReason: ptr("exchange outage"),
			PricingHalts: []models.PricingHalt{{Symbol: "BTC/USD", Reason: "exchange outage"}}},
			func(c *models.RiskConfig) {
				c.PricingSymbolRules[0].MaxPayout, c.PricingSymbolRules[0].BasePayout = 1.2, 1.1
			}},
		{"another field over a legacy rule", RiskConfigPatch{PricingKVol: ptr(0.05)},
			func(c *models.RiskConfig) { c.PricingSymbolRules[0].BasePayout = 0.6 }},
	}
	for _, c := range accepted {
		t.Run(c.name, func(t *testing.T) {
			if err := checkPatch(c.patch, c.base); err != nil {
				t.Fatalf("refused: %v", err)
			}
		})
	}
}

// The patch carries the new instrument fields through Apply unchanged.
func TestPricingPatchApplyKeepsExactValues(t *testing.T) {
	c := models.DefaultRiskConfig()
	rules := rulesWith(func(r *models.PricingSymbolRule) { r.MinStake, r.MaxStake, r.HedgeCost = 25_00, 40_000_00, 0.03 })
	p := RiskConfigPatch{PricingKVol: ptr(0.07), PricingExpiryFactors: []models.PricingExpiryFactor{ef(30, 1.25)}, PricingSymbolRules: rules}
	p.Apply(&c)
	r := c.PricingSymbolRules[0]
	if c.PricingKVol != 0.07 || len(c.PricingExpiryFactors) != 1 || c.PricingExpiryFactors[0].Factor != 1.25 ||
		r.MinStake != 25_00 || r.MaxStake != 40_000_00 || r.HedgeCost != 0.03 || c.PricingMaxVolAdj != 0.12 {
		t.Fatalf("apply %+v", c)
	}
	// Changing the patch's slice afterwards does not reach the configuration.
	rules[0].MinStake = 1
	if c.PricingSymbolRules[0].MinStake != 25_00 {
		t.Fatal("rules aliased")
	}
}

// A rule for a symbol no asset is listed under is refused when published; a
// listed asset, or a symbol that already has a rule, is accepted. Every
// accepted change is a new version holding exactly the numbers sent.
func TestUpdateRiskConfigChecksRuleSymbols(t *testing.T) {
	testsupport.LiveStores(t)
	InvalidateRiskConfigCache()
	t.Cleanup(InvalidateRiskConfigCache)
	ctx := context.Background()
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, bson.M{"symbol": "XAU/USD", "active": false}); err != nil {
		t.Fatal(err)
	}
	actor := bson.NewObjectID().Hex()
	start, err := GetRiskConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}

	gold := models.PricingSymbolRule{Symbol: "XAU/USD", DisabledReason: "not offered at launch", BasePayout: 0.9,
		MinPayout: 0.7, MaxPayout: 0.92, BaselineVol: 1.25e-5, HedgeCost: 0.02, Margin: 0.03, MaxExposure: 2_500_000_00}
	typo := gold
	typo.Symbol = "BTC/USDT"

	_, _, err = UpdateRiskConfig(ctx, RiskConfigPatch{PricingSymbolRules: append(models.DefaultPricingSymbolRules(), typo)}, actor, nil)
	e, ok := apperr.As(err)
	if !ok || e.Code != apperr.ValidationError || !strings.Contains(e.Message, "unknown symbol BTC/USDT") {
		t.Fatalf("typo: %v", err)
	}
	if cur, _ := GetRiskConfig(ctx); cur.Version != start.Version {
		t.Fatalf("a refused change published version %d", cur.Version)
	}

	// EUR/USD and GBP/USD have no asset in this database but already have
	// rules; XAU/USD is a listed asset.
	rules := rulesWith(func(r *models.PricingSymbolRule) { r.HedgeCost, r.MinStake, r.MaxStake = 0.03, 50_00, 50_000_00 })
	note := "pricing console test"
	_, after, err := UpdateRiskConfig(ctx, RiskConfigPatch{PricingSymbolRules: append(rules, gold),
		PricingExpiryFactors: []models.PricingExpiryFactor{ef(30, 1.25), ef(60, 1)}, PricingKVol: ptr(0.07)}, actor, &note)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != start.Version+1 || after.Note == nil || *after.Note != note {
		t.Fatalf("version %d note %v", after.Version, after.Note)
	}
	InvalidateRiskConfigCache()
	stored, err := GetRiskConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	btc := stored.PricingSymbolRules[0]
	if len(stored.PricingSymbolRules) != 5 || stored.PricingSymbolRules[4].Symbol != "XAU/USD" || btc.HedgeCost != 0.03 ||
		btc.MinStake != 50_00 || btc.MaxStake != 50_000_00 || stored.PricingKVol != 0.07 ||
		len(stored.PricingExpiryFactors) != 2 || stored.PricingExpiryFactors[0].Factor != 1.25 {
		t.Fatalf("stored %+v", stored)
	}
}
