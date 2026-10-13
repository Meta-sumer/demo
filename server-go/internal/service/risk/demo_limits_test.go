package risk

import (
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/util"
)

func TestDemoTradeSkipsHouseLimits(t *testing.T) {
	// A book that breaches every house-level limit.
	crowded := func(in *TradeLimitInput) {
		in.Demo = true
		in.GlobalGrossExposure = util.ToMinor(19_999_900)
		in.UserTotalExposure = util.ToMinor(499_900)
		in.Exposure = models.ExposureSnapshot{GrossExposure: util.ToMinor(999_900), NetExposure: util.ToMinor(399_900)}
	}
	wantNone(t, limits(crowded))

	// Stake bounds and the open trade count still apply.
	wantCode(t, limits(func(in *TradeLimitInput) { crowded(in); in.Stake = util.ToMinor(50) }), apperr.StakeBelowMin)
	wantCode(t, limits(func(in *TradeLimitInput) { crowded(in); in.Stake = util.ToMinor(60_000) }), apperr.StakeAboveMax)
	wantCode(t, limits(func(in *TradeLimitInput) { crowded(in); in.UserOpenTrades = 50 }), apperr.UserLimitExceeded)

	// The same book refuses a LIVE trade.
	live := limits(func(in *TradeLimitInput) { crowded(in); in.Demo = false })
	wantCode(t, live, apperr.RiskLimitExceeded)
	wantCode(t, live, apperr.ExposureLimitExceeded)
}
