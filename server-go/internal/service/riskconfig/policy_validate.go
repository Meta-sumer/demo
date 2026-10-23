package riskconfig

import (
	"slices"
	"strconv"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
)

// Hard limits on the settlement policy and the feed data rules. They are
// what an administrator cannot configure away: a post-expiry tick is never
// usable (the selection rule has no switch for it, and the clock-skew guard
// that rejects ticks stamped ahead of their receipt cannot be turned off),
// an early close is never possible within EarlyCloseHardFloorMs of expiry,
// and the tick history always covers the settlement defer window with a
// margin.

// MinTickHistoryMarginMs is how much longer than the settlement defer window
// plus the maximum tick age the tick history must be kept: a deferred trade
// is still settled from md:hist after a worker restart or a slow retry.
const MinTickHistoryMarginMs = 5 * 60 * 1000

// MaxTickHistoryRetentionMs bounds the history (Redis memory).
const MaxTickHistoryRetentionMs = 24 * 60 * 60 * 1000

func inRange(v, lo, hi int64) bool { return v >= lo && v <= hi }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func validateSettlement(c *models.RiskConfig) error {
	bad := func(msg string) error { return apperr.Validation(msg, nil) }
	switch {
	case !inRange(c.SettlementMaxTickAgeMs, 100, 60_000):
		return bad("settlementMaxTickAgeMs must be between 100 and 60000")
	case !inRange(c.SettlementConfirmationWindowMs, 0, 60_000):
		return bad("settlementConfirmationWindowMs must be between 0 and 60000")
	case !inRange(c.SettlementDeferWindowMs, 1000, 3_600_000):
		return bad("settlementDeferWindowMs must be between 1000 and 3600000")
	case c.SettlementDeferWindowMs < c.SettlementConfirmationWindowMs:
		return bad("settlementDeferWindowMs must not be shorter than settlementConfirmationWindowMs")
	case !inRange(c.SettlementRetryDelayMs, 100, 60_000):
		return bad("settlementRetryDelayMs must be between 100 and 60000")
	case !inRange(c.SettlementMaxClockSkewMs, 1, 60_000):
		// 0 would disable the guard against ticks stamped ahead of their
		// receipt - a tick whose clock cannot be placed against expiry.
		return bad("settlementMaxClockSkewMs must be between 1 and 60000 (the clock-skew guard cannot be disabled)")
	case !slices.Contains(models.NoPriceRuleValues, c.SettlementNoPriceRule):
		return bad("settlementNoPriceRule must be VOID_REFUND or STALE_TICK_THEN_VOID")
	case !inRange(c.EarlyCloseFloorMs, models.EarlyCloseHardFloorMs, 3_600_000):
		return bad("earlyCloseFloorMs must be between " + itoa(models.EarlyCloseHardFloorMs) + " and 3600000")
	case !inRange(c.EarlyCloseMaxTickAgeMs, 100, 10_000):
		return bad("earlyCloseMaxTickAgeMs must be between 100 and 10000")
	case !inRange(c.EarlyCloseQuoteMaxAgeMs, 1000, 60_000):
		return bad("earlyCloseQuoteMaxAgeMs must be between 1000 and 60000")
	case !(c.EarlyCloseQuoteTolerancePercent >= 0 && c.EarlyCloseQuoteTolerancePercent <= 50):
		return bad("earlyCloseQuoteTolerancePercent must be between 0 and 50")
	case !(c.EarlyCloseMinProbabilityPercent >= 0.01 && c.EarlyCloseMinProbabilityPercent <= 10):
		return bad("earlyCloseMinProbabilityPercent must be between 0.01 and 10")
	case len(c.EarlyCloseCalibration) > MaxEarlyCloseCalibrationRows:
		return bad("earlyCloseCalibration may have at most " + itoa(MaxEarlyCloseCalibrationRows) + " rows")
	}
	for _, r := range c.EarlyCloseCalibration {
		switch {
		case len(r.Symbol) > 20:
			return bad("earlyCloseCalibration: symbol is too long")
		case r.MaxSecondsRemaining < 0 || r.MaxVolRatio < 0:
			return bad("earlyCloseCalibration: bounds cannot be negative (0 = unbounded)")
		case !(r.SigmaScale > 0 && r.SigmaScale <= 20):
			return bad("earlyCloseCalibration: sigmaScale must be above 0 and at most 20")
		case !(r.MinVolRatio >= 0 && r.MinVolRatio <= 10):
			return bad("earlyCloseCalibration: minVolRatio must be between 0 and 10")
		case !(r.Stickiness >= 0 && r.Stickiness <= 0.95):
			return bad("earlyCloseCalibration: stickiness must be between 0 and 0.95")
		}
	}
	return nil
}

// MaxEarlyCloseCalibrationRows bounds the Sell-now correction table.
const MaxEarlyCloseCalibrationRows = 128

// MinTickHistoryRetentionMs is the shortest history the policy in c allows.
func MinTickHistoryRetentionMs(c *models.RiskConfig) int64 {
	return c.SettlementDeferWindowMs + c.SettlementMaxTickAgeMs + MinTickHistoryMarginMs
}

func validateFeedRules(c *models.RiskConfig) error {
	bad := func(msg string) error { return apperr.Validation(msg, nil) }
	if minRetention := MinTickHistoryRetentionMs(c); !inRange(c.FeedTickHistoryRetentionMs, minRetention, MaxTickHistoryRetentionMs) {
		return bad("feedTickHistoryRetentionMs must be between " + itoa(minRetention) +
			" (settlement defer window + max tick age + 5 minutes) and " + itoa(MaxTickHistoryRetentionMs))
	}
	switch {
	case !inRange(c.FeedGapMinMs, 500, 600_000):
		return bad("feedGapMinMs must be between 500 and 600000")
	case !(c.FeedGapIntervalMultiple >= 2 && c.FeedGapIntervalMultiple <= 1000):
		return bad("feedGapIntervalMultiple must be between 2 and 1000")
	case !inRange(c.FeedSaturationHoldMs, 0, 600_000):
		return bad("feedSaturationHoldMs must be between 0 and 600000")
	case !inRange(c.FeedMaxClockDriftMs, 50, 60_000):
		return bad("feedMaxClockDriftMs must be between 50 and 60000")
	case c.FeedClockCheckIntervalMs != 0 && !inRange(c.FeedClockCheckIntervalMs, 10_000, 86_400_000):
		return bad("feedClockCheckIntervalMs must be 0 (off) or between 10000 and 86400000")
	}
	return nil
}
