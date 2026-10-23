package settlement

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/riskconfig"
)

// The settlement policy is versioned risk configuration edited through the
// admin path (riskconfig.UpdateRiskConfig = PUT /admin/settings/risk), and
// PolicyFor reads it from the version in force.
func TestPolicyComesFromTheVersionedRiskConfig(t *testing.T) {
	h := newHarness(t)

	// A stored version that predates the fields: the built-in defaults.
	cfg, err := riskconfig.GetRiskConfig(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mongox.C(models.CollRiskConfigs).UpdateOne(h.ctx, bson.M{"_id": cfg.ID}, bson.M{"$unset": bson.M{
		"settlementMaxTickAgeMs": "", "settlementDeferWindowMs": "", "earlyCloseFloorMs": "", "settlementNoPriceRule": ""}}); err != nil {
		t.Fatal(err)
	}
	resetPolicyCache()
	p, err := PolicyFor(h.ctx, cfg)
	if err != nil || p != NormalisePolicy(DefaultPolicy()) {
		t.Fatalf("old version: %+v %v", p, err)
	}

	tick, deferW, retry, floor, quoteAge := int64(500), int64(5000), int64(250), int64(3000), int64(5000)
	tol := 2.0
	rule := models.NoPriceRuleVoidRefund
	_, after, err := riskconfig.UpdateRiskConfig(h.ctx, riskconfig.RiskConfigPatch{
		SettlementMaxTickAgeMs: &tick, SettlementDeferWindowMs: &deferW, SettlementRetryDelayMs: &retry,
		SettlementNoPriceRule: &rule, EarlyCloseFloorMs: &floor, EarlyCloseQuoteMaxAgeMs: &quoteAge,
		EarlyCloseQuoteTolerancePercent: &tol,
	}, bson.NewObjectID().Hex(), nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err = PolicyFor(h.ctx, after)
	if err != nil {
		t.Fatal(err)
	}
	if p.MaxTickAgeMs != 500 || p.DeferWindowMs != 5000 || p.RetryDelayMs != 250 || p.EarlyCloseFloorMs != 3000 ||
		p.EarlyCloseQuoteMaxAgeMs != 5000 || p.EarlyCloseQuoteTolerancePercent != 2 || p.ConfirmationWindowMs != 2000 ||
		p.MaxClockSkewMs != 2000 || p.NoPriceRule != models.NoPriceRuleVoidRefund {
		t.Fatalf("policy %+v", p)
	}
	// The cut-off customers see is the larger of the configured seconds and
	// the configured floor, and never under the hard floor.
	if got := EarlyCloseFloorMsFor(after, p); got != 10_000 {
		t.Fatalf("floor with minSecondsRemainingForEarlyClose=10: %d", got)
	}
	zero := int64(0)
	_, after, err = riskconfig.UpdateRiskConfig(h.ctx, riskconfig.RiskConfigPatch{MinSecondsRemainingForEarlyClose: &zero},
		bson.NewObjectID().Hex(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ = PolicyFor(h.ctx, after); EarlyCloseFloorMsFor(after, p) != 3000 {
		t.Fatalf("floor %d", EarlyCloseFloorMsFor(after, p))
	}

	// Hard limits are refused, and the version in force is unchanged.
	for name, patch := range map[string]riskconfig.RiskConfigPatch{
		"clock-skew guard off": {SettlementMaxClockSkewMs: &zero},
		"floor under 1 s":      {EarlyCloseFloorMs: func() *int64 { v := int64(999); return &v }()},
		"unknown rule":         {SettlementNoPriceRule: func() *string { s := "LAST_KNOWN_TICK"; return &s }()},
	} {
		if _, _, err := riskconfig.UpdateRiskConfig(h.ctx, patch, bson.NewObjectID().Hex(), nil); !apperr.HasCode(err, apperr.ValidationError) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	current, _ := riskconfig.GetRiskConfig(h.ctx)
	if current.Version != after.Version {
		t.Fatalf("a refused change was published: v%d", current.Version)
	}

	// Settlement runs under the configured policy: a tick 1 s before expiry
	// is too old for the 500 ms maximum, the trade is deferred, then voided
	// and refunded once the 5 s defer window has passed - never priced on
	// the post-expiry tick.
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 101, ts: exp - 1000})
	h.tick(trade, tickOpts{price: 102, ts: exp + 5})
	if out := settle(h, trade); out.Status != StatusSkippedNoPrice {
		t.Fatalf("outcome %+v", out)
	}
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(h.ctx, bson.M{"_id": trade.ID},
		bson.M{"$set": bson.M{"expiryTimestamp": exp - 6000}}); err != nil {
		t.Fatal(err)
	}
	out := settle(h, trade)
	if out.Rule != models.SettlementRuleNoPriceVoidRefund {
		t.Fatalf("outcome %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusDraw, 0)
	if row.Evidence.Policy.MaxTickAgeMs != 500 || row.Evidence.Policy.DeferWindowMs != 5000 || row.Evidence.RiskConfigVersion != after.Version {
		t.Fatalf("evidence %+v", row.Evidence)
	}
	// The apps are told it was a void, not a tie (additive view fields).
	v := models.ToTradeView(h.load(trade.ID))
	if !v.Voided || v.VoidReason == "" || v.ExpiryPriceSource == nil || *v.ExpiryPriceSource != models.SettlementRuleNoPriceVoidRefund {
		t.Fatalf("view %+v", v)
	}
}
