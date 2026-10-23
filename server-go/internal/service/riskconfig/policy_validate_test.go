package riskconfig

import (
	"strings"
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
)

// The settlement policy and the feed data rules are versioned risk
// configuration with hard limits an administrator cannot configure away.
func TestSettlementPolicyAndFeedRulesValidation(t *testing.T) {
	d := models.DefaultRiskConfig()
	if err := ValidateConfig(&d); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	// The defaults are the built-in policy (settlement.DefaultPolicy).
	if d.SettlementMaxTickAgeMs != 2000 || d.SettlementConfirmationWindowMs != 2000 || d.SettlementDeferWindowMs != 60_000 ||
		d.SettlementRetryDelayMs != 1000 || d.SettlementMaxClockSkewMs != 2000 || d.SettlementNoPriceRule != models.NoPriceRuleVoidRefund ||
		d.EarlyCloseFloorMs != 1000 || d.EarlyCloseMaxTickAgeMs != 1000 || d.EarlyCloseQuoteMaxAgeMs != 10_000 ||
		d.EarlyCloseQuoteTolerancePercent != 5 || d.FeedTickHistoryRetentionMs != 15*60*1000 {
		t.Fatalf("defaults %+v", d)
	}
	// The default history covers the defer window with the margin.
	if d.FeedTickHistoryRetentionMs < MinTickHistoryRetentionMs(&d) {
		t.Fatal("default retention below the minimum")
	}

	cases := map[string]func(c *models.RiskConfig){
		"settlementMaxTickAgeMs":          func(c *models.RiskConfig) { c.SettlementMaxTickAgeMs = 0 },
		"settlementConfirmationWindowMs":  func(c *models.RiskConfig) { c.SettlementConfirmationWindowMs = -1 },
		"settlementDeferWindowMs must be": func(c *models.RiskConfig) { c.SettlementDeferWindowMs = 500 },
		"must not be shorter than": func(c *models.RiskConfig) {
			c.SettlementConfirmationWindowMs, c.SettlementDeferWindowMs = 10_000, 5_000
		},
		"settlementRetryDelayMs":                     func(c *models.RiskConfig) { c.SettlementRetryDelayMs = 50 },
		"the clock-skew guard cannot be disabled":    func(c *models.RiskConfig) { c.SettlementMaxClockSkewMs = 0 },
		"settlementNoPriceRule":                      func(c *models.RiskConfig) { c.SettlementNoPriceRule = "LAST_KNOWN_TICK" },
		"earlyCloseFloorMs must be between 1000":     func(c *models.RiskConfig) { c.EarlyCloseFloorMs = 999 },
		"earlyCloseMaxTickAgeMs":                     func(c *models.RiskConfig) { c.EarlyCloseMaxTickAgeMs = 0 },
		"earlyCloseQuoteMaxAgeMs":                    func(c *models.RiskConfig) { c.EarlyCloseQuoteMaxAgeMs = 500 },
		"earlyCloseQuoteTolerancePercent":            func(c *models.RiskConfig) { c.EarlyCloseQuoteTolerancePercent = 51 },
		"feedTickHistoryRetentionMs must be between": func(c *models.RiskConfig) { c.FeedTickHistoryRetentionMs = 60_000 },
		"feedGapMinMs":                               func(c *models.RiskConfig) { c.FeedGapMinMs = 100 },
		"feedGapIntervalMultiple":                    func(c *models.RiskConfig) { c.FeedGapIntervalMultiple = 1 },
		"feedSaturationHoldMs":                       func(c *models.RiskConfig) { c.FeedSaturationHoldMs = -1 },
		"feedMaxClockDriftMs":                        func(c *models.RiskConfig) { c.FeedMaxClockDriftMs = 10 },
		"feedClockCheckIntervalMs":                   func(c *models.RiskConfig) { c.FeedClockCheckIntervalMs = 5000 },
	}
	for want, mutate := range cases {
		c := models.DefaultRiskConfig()
		mutate(&c)
		err := ValidateConfig(&c)
		e, ok := apperr.As(err)
		if !ok || e.Code != apperr.ValidationError || !strings.Contains(e.Message, want) {
			t.Errorf("%s: got %v", want, err)
		}
	}

	// A longer defer window needs a longer history.
	c := models.DefaultRiskConfig()
	c.SettlementDeferWindowMs = 30 * 60 * 1000
	if err := ValidateConfig(&c); err == nil {
		t.Fatal("a 30 min defer window with a 15 min history validated")
	}
	c.FeedTickHistoryRetentionMs = MinTickHistoryRetentionMs(&c)
	if err := ValidateConfig(&c); err != nil {
		t.Fatalf("retention at the minimum: %v", err)
	}
	// The clock check can be switched off.
	c.FeedClockCheckIntervalMs = 0
	if err := ValidateConfig(&c); err != nil {
		t.Fatalf("clock check off: %v", err)
	}
}

func TestPolicyPatchApply(t *testing.T) {
	c := models.DefaultRiskConfig()
	tick, floor, rule, retention := int64(1500), int64(3000), models.NoPriceRuleStaleTickThenVoid, int64(30*60*1000)
	tol := 2.5
	p := RiskConfigPatch{SettlementMaxTickAgeMs: &tick, EarlyCloseFloorMs: &floor, SettlementNoPriceRule: &rule,
		EarlyCloseQuoteTolerancePercent: &tol, FeedTickHistoryRetentionMs: &retention}
	p.Apply(&c)
	if c.SettlementMaxTickAgeMs != 1500 || c.EarlyCloseFloorMs != 3000 || c.SettlementNoPriceRule != rule ||
		c.EarlyCloseQuoteTolerancePercent != 2.5 || c.FeedTickHistoryRetentionMs != retention || c.SettlementDeferWindowMs != 60_000 {
		t.Fatalf("apply %+v", c)
	}
	if err := ValidateConfig(&c); err != nil {
		t.Fatal(err)
	}
}
