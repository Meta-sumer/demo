package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/audit"
	authsvc "binarytrading/internal/service/auth"
	"binarytrading/internal/service/riskconfig"
)

func parseBody(t *testing.T, body any) (riskconfig.RiskConfigPatch, *string, error) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/admin/settings/risk", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	return parseRiskPatch(req)
}

// btcRule is a valid instrument rule as the admin console sends it.
func btcRule() map[string]any {
	return map[string]any{"symbol": "BTC/USD", "enabled": true, "disabledReason": "", "basePayout": 0.86,
		"minPayout": 0.65, "maxPayout": 0.88, "baselineVol": 4.45e-5, "hedgeCost": 0.035, "margin": 0.035,
		"maxExposure": 125_000_000, "minStake": 5_000, "maxStake": 5_000_000}
}

func withRule(edit func(r map[string]any)) map[string]any {
	r := btcRule()
	edit(r)
	return map[string]any{"pricingSymbolRules": []any{r}}
}

// Every pricing parameter of the risk configuration is accepted by
// PUT /admin/settings/risk with exactly the value sent.
func TestParseRiskPatchAcceptsEveryPricingField(t *testing.T) {
	factors := []any{map[string]any{"expirySeconds": 30, "factor": 1.25}, map[string]any{"expirySeconds": 900, "factor": 0.75}}
	body := map[string]any{
		"pricingMode": "FIXED", "pricingRuleLabel": "rules-2026.11.1",
		"pricingStaleAfterMs": 1500, "pricingMaxSpreadBps": 6.5, "pricingMaxJumpBps": 120, "pricingJumpConfirmations": 4,
		"pricingPendingOutlierMaxAgeMs": 4000, "pricingEwmaLambda": 0.95, "pricingVolSampleIntervalMs": 0,
		"pricingKVol": 0.07, "pricingMaxVolAdj": 0.1, "pricingVolBlockRatio": 3.5,
		"pricingKSpread": 0.025, "pricingMaxSpreadAdj": 0.04, "pricingKExposure": 0.09, "pricingMaxExposureAdj": 0.12,
		"pricingExpiryFactors": factors, "pricingPayoutStep": 0.005, "pricingOfferTtlMs": 2500,
		"pricingMaxSlippageBps": 3, "pricingDisplayTolerancePercent": 0.5,
		"pricingGuardEnabled": false, "pricingGuardMinEdgePercent": 1.5, "pricingGuardConservative": true,
		"pricingHedgeUnavailableAction": "REFUSE", "pricingHedgeUnavailableAdj": 0.04,
		"pricingHedgeTakerFeeBps": 4.5, "pricingHedgeSpreadBps": 0, "pricingHedgeSlippageBps": 1,
		"earlyCloseMinProbabilityPercent": 0.1,
		"pricingSymbolRules":              []any{btcRule()},
		"note":                            "retune after the volatility review",
	}
	p, note, err := parseBody(t, body)
	if err != nil {
		t.Fatal(err)
	}
	if note == nil || *note != "retune after the volatility review" {
		t.Fatalf("note %v", note)
	}
	checks := []struct {
		name string
		ok   bool
	}{
		{"pricingMode", *p.PricingMode == "FIXED"},
		{"pricingRuleLabel", *p.PricingRuleLabel == "rules-2026.11.1"},
		{"pricingStaleAfterMs", *p.PricingStaleAfterMs == 1500},
		{"pricingMaxSpreadBps", *p.PricingMaxSpreadBps == 6.5},
		{"pricingMaxJumpBps", *p.PricingMaxJumpBps == 120},
		{"pricingJumpConfirmations", *p.PricingJumpConfirmations == 4},
		{"pricingPendingOutlierMaxAgeMs", *p.PricingPendingOutlierMaxAgeMs == 4000},
		{"pricingEwmaLambda", *p.PricingEwmaLambda == 0.95},
		{"pricingVolSampleIntervalMs", *p.PricingVolSampleIntervalMs == 0},
		{"pricingKVol", *p.PricingKVol == 0.07},
		{"pricingMaxVolAdj", *p.PricingMaxVolAdj == 0.1},
		{"pricingVolBlockRatio", *p.PricingVolBlockRatio == 3.5},
		{"pricingKSpread", *p.PricingKSpread == 0.025},
		{"pricingMaxSpreadAdj", *p.PricingMaxSpreadAdj == 0.04},
		{"pricingKExposure", *p.PricingKExposure == 0.09},
		{"pricingMaxExposureAdj", *p.PricingMaxExposureAdj == 0.12},
		{"pricingExpiryFactors", len(p.PricingExpiryFactors) == 2 && p.PricingExpiryFactors[0] == models.PricingExpiryFactor{ExpirySeconds: 30, Factor: 1.25} &&
			p.PricingExpiryFactors[1] == models.PricingExpiryFactor{ExpirySeconds: 900, Factor: 0.75}},
		{"pricingPayoutStep", *p.PricingPayoutStep == 0.005},
		{"pricingOfferTtlMs", *p.PricingOfferTTLMs == 2500},
		{"pricingMaxSlippageBps", *p.PricingMaxSlippageBps == 3},
		{"pricingDisplayTolerancePercent", *p.PricingDisplayTolerancePercent == 0.5},
		{"pricingGuardEnabled", !*p.PricingGuardEnabled},
		{"pricingGuardMinEdgePercent", *p.PricingGuardMinEdgePercent == 1.5},
		{"pricingGuardConservative", *p.PricingGuardConservative},
		{"pricingHedgeUnavailableAction", *p.PricingHedgeUnavailableAction == "REFUSE"},
		{"pricingHedgeUnavailableAdj", *p.PricingHedgeUnavailableAdj == 0.04},
		{"pricingHedgeTakerFeeBps", *p.PricingHedgeTakerFeeBps == 4.5},
		{"pricingHedgeSpreadBps", *p.PricingHedgeSpreadBps == 0},
		{"pricingHedgeSlippageBps", *p.PricingHedgeSlippageBps == 1},
		{"earlyCloseMinProbabilityPercent", *p.EarlyCloseMinProbabilityPercent == 0.1},
		{"pricingSymbolRules", len(p.PricingSymbolRules) == 1 && p.PricingSymbolRules[0] == models.PricingSymbolRule{
			Symbol: "BTC/USD", Enabled: true, BasePayout: 0.86, MinPayout: 0.65, MaxPayout: 0.88, BaselineVol: 4.45e-5,
			HedgeCost: 0.035, Margin: 0.035, MaxExposure: 125_000_000, MinStake: 5_000, MaxStake: 5_000_000}},
	}
	for _, c := range checks {
		if !c.ok {
			t.Errorf("%s not parsed exactly: %+v", c.name, p)
		}
	}

	// The stake range is optional: an older console does not send it.
	p, _, err = parseBody(t, withRule(func(r map[string]any) { delete(r, "minStake"); delete(r, "maxStake") }))
	if err != nil || p.PricingSymbolRules[0].MinStake != 0 || p.PricingSymbolRules[0].MaxStake != 0 {
		t.Fatalf("rule without a stake range: %v %+v", err, p.PricingSymbolRules)
	}
}

// A refused value names its field and the rule it broke.
func TestParseRiskPatchRefusesUnsafePricingValues(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		path string
		want string
	}{
		{"lambda 1", map[string]any{"pricingEwmaLambda": 1}, "pricingEwmaLambda", "less than or equal to 0.9999"},
		{"lambda below range", map[string]any{"pricingEwmaLambda": 0.2}, "pricingEwmaLambda", "greater than or equal to 0.5"},
		{"offer ttl", map[string]any{"pricingOfferTtlMs": 100}, "pricingOfferTtlMs", "greater than or equal to 500"},
		{"step 0", map[string]any{"pricingPayoutStep": 0}, "pricingPayoutStep", "greater than or equal to 0.0001"},
		{"block ratio under 1", map[string]any{"pricingVolBlockRatio": 0.5}, "pricingVolBlockRatio", "greater than or equal to 1"},
		{"negative k_vol", map[string]any{"pricingKVol": -0.01}, "pricingKVol", "greater than or equal to 0"},
		{"negative hedge fee", map[string]any{"pricingHedgeTakerFeeBps": -1}, "pricingHedgeTakerFeeBps", "greater than or equal to 0"},
		{"jump confirmations", map[string]any{"pricingJumpConfirmations": 0}, "pricingJumpConfirmations", "greater than or equal to 1"},
		{"fractional confirmations", map[string]any{"pricingJumpConfirmations": 2.5}, "pricingJumpConfirmations", "Expected integer"},
		{"mode", map[string]any{"pricingMode": "AUTO"}, "pricingMode", "Invalid enum value"},
		{"hedge action", map[string]any{"pricingHedgeUnavailableAction": "IGNORE"}, "pricingHedgeUnavailableAction", "Invalid enum value"},
		{"rule label", map[string]any{"pricingRuleLabel": ""}, "pricingRuleLabel", "at least 3 character"},
		{"early-close floor", map[string]any{"earlyCloseMinProbabilityPercent": 0}, "earlyCloseMinProbabilityPercent", "greater than or equal to 0.01"},
		{"factor 0", map[string]any{"pricingExpiryFactors": []any{map[string]any{"expirySeconds": 30, "factor": 0}}},
			"pricingExpiryFactors.0.factor", "greater than or equal to 0.01"},
		{"expiry 0", map[string]any{"pricingExpiryFactors": []any{map[string]any{"expirySeconds": 0, "factor": 1}}},
			"pricingExpiryFactors.0.expirySeconds", "greater than 0"},
		{"expiry beyond a day", map[string]any{"pricingExpiryFactors": []any{map[string]any{"expirySeconds": 90_000, "factor": 1}}},
			"pricingExpiryFactors.0.expirySeconds", "less than or equal to 86400"},
		{"base above 100%", withRule(func(r map[string]any) { r["basePayout"] = 1.2 }), "pricingSymbolRules.0.basePayout", "less than or equal to 1"},
		{"max above 100%", withRule(func(r map[string]any) { r["maxPayout"] = 1.5 }), "pricingSymbolRules.0.maxPayout", "less than or equal to 1"},
		{"negative hedge cost", withRule(func(r map[string]any) { r["hedgeCost"] = -0.01 }), "pricingSymbolRules.0.hedgeCost", "greater than or equal to 0"},
		{"negative margin", withRule(func(r map[string]any) { r["margin"] = -0.01 }), "pricingSymbolRules.0.margin", "greater than or equal to 0"},
		{"baseline 0", withRule(func(r map[string]any) { r["baselineVol"] = 0 }), "pricingSymbolRules.0.baselineVol", "greater than or equal to 0.000000001"},
		{"exposure 0", withRule(func(r map[string]any) { r["maxExposure"] = 0 }), "pricingSymbolRules.0.maxExposure", "greater than 0"},
		{"negative stake", withRule(func(r map[string]any) { r["minStake"] = -100 }), "pricingSymbolRules.0.minStake", "greater than or equal to 0"},
		{"fractional stake", withRule(func(r map[string]any) { r["maxStake"] = 10.5 }), "pricingSymbolRules.0.maxStake", "Expected integer"},
		{"missing exposure", withRule(func(r map[string]any) { delete(r, "maxExposure") }), "pricingSymbolRules.0.maxExposure", "Required"},
		{"enabled not a boolean", withRule(func(r map[string]any) { r["enabled"] = "yes" }), "pricingSymbolRules.0.enabled", "Expected boolean"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := parseBody(t, c.body)
			e, ok := apperr.As(err)
			if !ok || e.Code != apperr.ValidationError || e.StatusCode != 422 {
				t.Fatalf("want a 422 validation error, got %v", err)
			}
			issues, _ := e.Details.([]apperr.FieldIssue)
			for _, is := range issues {
				if is.Path == c.path && strings.Contains(is.Message, c.want) {
					return
				}
			}
			t.Fatalf("no issue %s: %q in %+v", c.path, c.want, issues)
		})
	}
}

// End to end: an operator with risk:write changes pricing values, the API
// stores exactly those numbers as a new audited version, a refused value and
// an unknown symbol leave the configuration as it was, and an operator
// without risk:write can read but not change it.
func TestPricingSettingsOverHTTP(t *testing.T) {
	h := newHarness(t)
	// The first version is seeded from the environment's feed bands.
	config.E.FeedHealthyMaxMs, config.E.FeedDegradedMaxMs, config.E.FeedTickMaxAgeMs = 100, 300, 5000
	riskconfig.InvalidateRiskConfigCache()
	t.Cleanup(riskconfig.InvalidateRiskConfigCache)
	ctx := context.Background()
	risk := h.token(h.user(config.RoleRiskManager))

	code, env := h.do(http.MethodGet, "/admin/settings/risk", risk, nil)
	if code != http.StatusOK {
		t.Fatalf("GET: %d", code)
	}
	var got struct {
		Config models.RiskConfigLean `json:"config"`
	}
	_ = json.Unmarshal(env["data"], &got)
	start := got.Config.Version
	rules := got.Config.PricingSymbolRules
	if len(rules) != 4 || rules[0].Symbol != "BTC/USD" {
		t.Fatalf("default rules %+v", rules)
	}
	rules[0].HedgeCost, rules[0].MinStake, rules[0].MaxStake = 0.03, 50_00, 50_000_00
	rules[2].BasePayout = 0.89

	code, env = h.do(http.MethodPut, "/admin/settings/risk", risk, map[string]any{
		"pricingKVol": 0.07, "pricingOfferTtlMs": 2500, "pricingHedgeUnavailableAction": "REFUSE",
		"pricingExpiryFactors": []models.PricingExpiryFactor{{ExpirySeconds: 30, Factor: 1.25}, {ExpirySeconds: 60, Factor: 1}},
		"pricingSymbolRules":   rules, "note": "pricing console review",
	})
	if code != http.StatusOK {
		t.Fatalf("PUT: %d %s", code, env["error"])
	}
	var after models.RiskConfigLean
	_ = json.Unmarshal(env["data"], &after)
	if after.Version != start+1 || after.PricingKVol != 0.07 || after.PricingOfferTTLMs != 2500 ||
		after.PricingHedgeUnavailableAction != "REFUSE" || len(after.PricingExpiryFactors) != 2 ||
		after.PricingExpiryFactors[0].Factor != 1.25 || after.PricingSymbolRules[0].HedgeCost != 0.03 ||
		after.PricingSymbolRules[0].MinStake != 50_00 || after.PricingSymbolRules[0].MaxStake != 50_000_00 ||
		after.PricingSymbolRules[2].BasePayout != 0.89 || after.PricingMaxVolAdj != 0.12 {
		t.Fatalf("stored %+v", after)
	}
	waitAudit(t, bson.M{"action": audit.RiskConfigUpdated, "resourceId": strconv.FormatInt(after.Version, 10)})

	refuse := func(name string, body map[string]any, want string) {
		t.Helper()
		code, env := h.do(http.MethodPut, "/admin/settings/risk", risk, body)
		var e struct {
			Code, Message string
			Details       json.RawMessage
		}
		_ = json.Unmarshal(env["error"], &e)
		if code != http.StatusUnprocessableEntity || e.Code != "VALIDATION_ERROR" || !strings.Contains(e.Message+string(e.Details), want) {
			t.Fatalf("%s: %d %s", name, code, env["error"])
		}
	}
	bad := append([]models.PricingSymbolRule(nil), rules...)
	bad[0].BasePayout = 0.9 // above its 0.88 maximum
	refuse("base above max", map[string]any{"pricingSymbolRules": bad}, "minPayout <= basePayout <= maxPayout <= 1")
	refuse("block ratio 1", map[string]any{"pricingVolBlockRatio": 1}, "pricingVolBlockRatio must be above 1")
	refuse("lambda", map[string]any{"pricingEwmaLambda": 1}, "pricingEwmaLambda")
	typo := append(append([]models.PricingSymbolRule(nil), rules...), rules[0])
	typo[len(typo)-1].Symbol = "BTC/USDT"
	refuse("unknown symbol", map[string]any{"pricingSymbolRules": typo}, "unknown symbol BTC/USDT")
	_, env = h.do(http.MethodGet, "/admin/settings/risk", risk, nil)
	_ = json.Unmarshal(env["data"], &got)
	if got.Config.Version != after.Version {
		t.Fatalf("a refused change published version %d", got.Config.Version)
	}

	// risk:read without risk:write: reads, cannot publish.
	hash, err := authsvc.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	id := bson.NewObjectID()
	now := mongox.Now()
	reader := models.User{ID: id, Email: "r" + id.Hex() + "@example.test", Username: "r" + id.Hex()[14:], PasswordHash: hash,
		Role: config.RoleSupport, Permissions: []string{config.PermRiskRead}, Status: models.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	if _, err := mongox.C(models.CollUsers).InsertOne(ctx, reader); err != nil {
		t.Fatal(err)
	}
	h.ids = append(h.ids, id.Hex())
	readOnly := h.token(reader)
	if code, _ := h.do(http.MethodGet, "/admin/settings/risk", readOnly, nil); code != http.StatusOK {
		t.Fatalf("read-only GET: %d", code)
	}
	if code, _ := h.do(http.MethodPut, "/admin/settings/risk", readOnly, map[string]any{"pricingKVol": 0.05}); code != http.StatusForbidden {
		t.Fatalf("read-only PUT: %d", code)
	}
}
