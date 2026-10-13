package riskconfig

import (
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
)

func TestValidateConfig(t *testing.T) {
	ok := models.DefaultRiskConfig()
	if err := ValidateConfig(&ok); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}

	cases := map[string]func(c *models.RiskConfig){
		"Feed thresholds must satisfy healthy < degraded < stale": func(c *models.RiskConfig) { c.FeedDegradedMaxMs = c.FeedStaleMaxMs },
		"minTradeAmount cannot exceed maxTradeAmount":             func(c *models.RiskConfig) { c.MinTradeAmount = c.MaxTradeAmount + 1 },
		"hedgeThresholdPercent must be between 0 and 100":         func(c *models.RiskConfig) { c.HedgeThresholdPercent = 101 },
		"Hedge level HIGH_HEDGE has an empty band":                func(c *models.RiskConfig) { c.HedgeLevels[2].ToPercent = 60 },
		"Hedge levels overlap around 55%": func(c *models.RiskConfig) {
			c.HedgeLevels[2].FromPercent = 55
		},
	}
	for want, mutate := range cases {
		c := models.DefaultRiskConfig()
		mutate(&c)
		err := ValidateConfig(&c)
		e, isApp := apperr.As(err)
		if !isApp || e.Code != apperr.ValidationError || e.Message != want || e.StatusCode != 422 {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}

func TestPatchApply(t *testing.T) {
	c := models.DefaultRiskConfig()
	halted := true
	fee := 7.5
	p := RiskConfigPatch{TradingHalted: &halted, EarlyCloseFeePercent: &fee}
	p.Apply(&c)
	if !c.TradingHalted || c.EarlyCloseFeePercent != 7.5 || c.MaxOpenTradesPerUser != 50 || len(c.HedgeLevels) != 4 {
		t.Fatalf("bad apply %+v", c)
	}
}
