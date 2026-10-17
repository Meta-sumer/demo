package settlement

import (
	"context"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// Settlement and early-close policy (F-11, F-12).
//
// The values are risk configuration: they are read from the active, versioned
// risk-config row by field name (riskConfigPolicyFields), so a new version
// takes effect like any other risk setting, and every outcome stores the
// version and the effective values it was decided under. A field that is not
// on the row falls back to DefaultPolicy.

// EarlyCloseHardFloorMs: an early close is refused unless expiry is at least
// this far away, whatever minSecondsRemainingForEarlyClose says - closing at or
// after expiry would let a customer sell a position whose outcome is already
// known.
const EarlyCloseHardFloorMs = 1000

// DefaultPolicy is the policy when the risk configuration sets nothing.
func DefaultPolicy() models.SettlementPolicy {
	return models.SettlementPolicy{
		MaxTickAgeMs:                    5000,
		ConfirmationWindowMs:            2000,
		DeferWindowMs:                   60_000,
		RetryDelayMs:                    1000,
		MaxClockSkewMs:                  2000,
		NoPriceRule:                     models.DefaultSettlementNoPriceRule,
		EarlyCloseFloorMs:               EarlyCloseHardFloorMs,
		EarlyCloseMaxTickAgeMs:          1000,
		EarlyCloseQuoteMaxAgeMs:         10_000,
		EarlyCloseQuoteTolerancePercent: 5,
	}
}

// riskConfigPolicyFields maps risk-config fields (bson names) onto the policy.
// The riskconfig owner adds these to models.RiskConfig, its patch and admin
// validation; until then the defaults apply.
var riskConfigPolicyFields = []struct {
	field string
	set   func(p *models.SettlementPolicy, v any)
}{
	{"settlementMaxTickAgeMs", func(p *models.SettlementPolicy, v any) { setInt(&p.MaxTickAgeMs, v) }},
	{"settlementConfirmationWindowMs", func(p *models.SettlementPolicy, v any) { setInt(&p.ConfirmationWindowMs, v) }},
	{"settlementDeferWindowMs", func(p *models.SettlementPolicy, v any) { setInt(&p.DeferWindowMs, v) }},
	{"settlementRetryDelayMs", func(p *models.SettlementPolicy, v any) { setInt(&p.RetryDelayMs, v) }},
	{"settlementMaxClockSkewMs", func(p *models.SettlementPolicy, v any) { setInt(&p.MaxClockSkewMs, v) }},
	{"settlementNoPriceRule", func(p *models.SettlementPolicy, v any) {
		if s, ok := v.(string); ok {
			p.NoPriceRule = s
		}
	}},
	{"earlyCloseMaxTickAgeMs", func(p *models.SettlementPolicy, v any) { setInt(&p.EarlyCloseMaxTickAgeMs, v) }},
	{"earlyCloseQuoteMaxAgeMs", func(p *models.SettlementPolicy, v any) { setInt(&p.EarlyCloseQuoteMaxAgeMs, v) }},
	{"earlyCloseQuoteTolerancePercent", func(p *models.SettlementPolicy, v any) { setFloat(&p.EarlyCloseQuoteTolerancePercent, v) }},
}

func setInt(dst *int64, v any) {
	switch n := v.(type) {
	case int32:
		*dst = int64(n)
	case int64:
		*dst = n
	case float64:
		*dst = int64(n)
	}
}

func setFloat(dst *float64, v any) {
	switch n := v.(type) {
	case int32:
		*dst = float64(n)
	case int64:
		*dst = float64(n)
	case float64:
		*dst = n
	}
}

// Risk-config rows are immutable (a change inserts a new version), so the
// policy read from one is cached by its id for the life of the process.
var (
	policyCacheMu sync.Mutex
	policyCache   = map[bson.ObjectID]models.SettlementPolicy{}
)

// PolicyFor returns the effective policy under cfg.
func PolicyFor(ctx context.Context, cfg *models.RiskConfig) (models.SettlementPolicy, error) {
	if cfg == nil || cfg.ID.IsZero() {
		return NormalisePolicy(DefaultPolicy()), nil
	}
	policyCacheMu.Lock()
	p, ok := policyCache[cfg.ID]
	policyCacheMu.Unlock()
	if ok {
		return p, nil
	}

	projection := bson.M{}
	for _, f := range riskConfigPolicyFields {
		projection[f.field] = 1
	}
	var raw bson.M
	err := mongox.C(models.CollRiskConfigs).FindOne(ctx, bson.M{"_id": cfg.ID},
		options.FindOne().SetProjection(projection)).Decode(&raw)
	if err != nil && !mongox.IsNotFound(err) {
		return models.SettlementPolicy{}, err
	}
	p = DefaultPolicy()
	for _, f := range riskConfigPolicyFields {
		if v, ok := raw[f.field]; ok && v != nil {
			f.set(&p, v)
		}
	}
	p = NormalisePolicy(p)
	policyCacheMu.Lock()
	policyCache[cfg.ID] = p
	policyCacheMu.Unlock()
	return p, nil
}

// NormalisePolicy replaces out-of-range values with safe ones. It never
// loosens a hard rule: the early-close floor cannot go below
// EarlyCloseHardFloorMs and a post-expiry tick is never usable whatever the
// values.
func NormalisePolicy(p models.SettlementPolicy) models.SettlementPolicy {
	d := DefaultPolicy()
	if p.MaxTickAgeMs <= 0 {
		p.MaxTickAgeMs = d.MaxTickAgeMs
	}
	if p.ConfirmationWindowMs < 0 {
		p.ConfirmationWindowMs = d.ConfirmationWindowMs
	}
	if p.DeferWindowMs <= 0 {
		p.DeferWindowMs = d.DeferWindowMs
	}
	if p.DeferWindowMs < p.ConfirmationWindowMs {
		p.DeferWindowMs = p.ConfirmationWindowMs
	}
	if p.RetryDelayMs < 100 {
		p.RetryDelayMs = d.RetryDelayMs
	}
	if p.MaxClockSkewMs < 0 {
		p.MaxClockSkewMs = 0
	}
	switch p.NoPriceRule {
	case models.NoPriceRuleVoidRefund, models.NoPriceRuleStaleTickThenVoid:
	default:
		p.NoPriceRule = d.NoPriceRule
	}
	if p.EarlyCloseFloorMs < EarlyCloseHardFloorMs {
		p.EarlyCloseFloorMs = EarlyCloseHardFloorMs
	}
	if p.EarlyCloseMaxTickAgeMs <= 0 {
		p.EarlyCloseMaxTickAgeMs = d.EarlyCloseMaxTickAgeMs
	}
	if p.EarlyCloseQuoteMaxAgeMs <= 0 {
		p.EarlyCloseQuoteMaxAgeMs = d.EarlyCloseQuoteMaxAgeMs
	}
	if p.EarlyCloseQuoteTolerancePercent < 0 || p.EarlyCloseQuoteTolerancePercent > 100 {
		p.EarlyCloseQuoteTolerancePercent = d.EarlyCloseQuoteTolerancePercent
	}
	return p
}

// EarlyCloseFloorMsFor is how long before expiry an early close stops being
// possible: the configured minimum seconds, but never under the hard floor.
func EarlyCloseFloorMsFor(cfg *models.RiskConfig, p models.SettlementPolicy) int64 {
	floor := p.EarlyCloseFloorMs
	if floor < EarlyCloseHardFloorMs {
		floor = EarlyCloseHardFloorMs
	}
	if cfg != nil && cfg.MinSecondsRemainingForEarlyClose*1000 > floor {
		floor = cfg.MinSecondsRemainingForEarlyClose * 1000
	}
	return floor
}
