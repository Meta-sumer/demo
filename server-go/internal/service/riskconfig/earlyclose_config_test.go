package riskconfig

import (
	"strings"
	"testing"

	"binarytrading/internal/models"
)

// The calibrated Sell-now fields (audit F-12): the defaults validate, bad
// rows and clamps are refused, and a patch replaces or clears the table.
func TestEarlyCloseCalibrationConfig(t *testing.T) {
	ok := models.DefaultRiskConfig()
	if len(ok.EarlyCloseCalibration) == 0 || ok.EarlyCloseMinProbabilityPercent != models.DefaultEarlyCloseMinProbabilityPercent {
		t.Fatalf("defaults missing: %d rows, clamp %v", len(ok.EarlyCloseCalibration), ok.EarlyCloseMinProbabilityPercent)
	}
	if err := ValidateConfig(&ok); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	bad := map[string]func(c *models.RiskConfig){
		"earlyCloseMinProbabilityPercent": func(c *models.RiskConfig) { c.EarlyCloseMinProbabilityPercent = 0 },
		"sigmaScale":                      func(c *models.RiskConfig) { c.EarlyCloseCalibration[0].SigmaScale = 0 },
		"stickiness":                      func(c *models.RiskConfig) { c.EarlyCloseCalibration[0].Stickiness = 0.99 },
		"minVolRatio":                     func(c *models.RiskConfig) { c.EarlyCloseCalibration[0].MinVolRatio = -1 },
		"bounds cannot be negative":       func(c *models.RiskConfig) { c.EarlyCloseCalibration[0].MaxVolRatio = -0.1 },
		"at most":                         func(c *models.RiskConfig) { c.EarlyCloseCalibration = make([]models.EarlyCloseCalibrationRow, 200) },
	}
	for want, mutate := range bad {
		c := models.DefaultRiskConfig()
		mutate(&c)
		if err := ValidateConfig(&c); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: got %v", want, err)
		}
	}

	c := models.DefaultRiskConfig()
	clamp := 0.5
	row := models.EarlyCloseCalibrationRow{Symbol: "BTC/USD", MaxSecondsRemaining: 15, SigmaScale: 2, Stickiness: 0.5}
	(&RiskConfigPatch{EarlyCloseCalibration: []models.EarlyCloseCalibrationRow{row}, EarlyCloseMinProbabilityPercent: &clamp}).Apply(&c)
	if len(c.EarlyCloseCalibration) != 1 || c.EarlyCloseCalibration[0] != row || c.EarlyCloseMinProbabilityPercent != 0.5 {
		t.Fatalf("patch: %+v %v", c.EarlyCloseCalibration, c.EarlyCloseMinProbabilityPercent)
	}
	(&RiskConfigPatch{}).Apply(&c)
	if len(c.EarlyCloseCalibration) != 1 {
		t.Fatal("an absent table must leave it unchanged")
	}
	(&RiskConfigPatch{EarlyCloseCalibration: []models.EarlyCloseCalibrationRow{}}).Apply(&c)
	if c.EarlyCloseCalibration == nil || len(c.EarlyCloseCalibration) != 0 {
		t.Fatal("an empty table switches the correction off")
	}
	if err := ValidateConfig(&c); err != nil {
		t.Fatalf("an empty table is valid: %v", err)
	}
}
