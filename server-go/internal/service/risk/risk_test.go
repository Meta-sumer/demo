package risk

import (
	"slices"
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/exposure"
)

var limitConfig = &models.RiskConfig{
	MinTradeAmount:                   util.ToMinor(10),
	MaxTradeAmount:                   util.ToMinor(100_000),
	MaxAssetExposure:                 util.ToMinor(5_000_000),
	MaxGrossExposure:                 util.ToMinor(20_000_000),
	MaxUserExposure:                  util.ToMinor(500_000),
	MaxOpenTradesPerUser:             50,
	MaxUserAssetConcentrationPercent: 40,
}

var limitAsset = &models.Asset{
	Symbol:           "BTC/USD",
	MinStake:         util.ToMinor(100),
	MaxStake:         util.ToMinor(50_000),
	ExposureLimit:    util.ToMinor(1_000_000),
	NetExposureLimit: util.ToMinor(400_000),
}

func book(c exposure.RawCounters) models.ExposureSnapshot {
	return exposure.DeriveExposure("a1", "BTC/USD", c, util.NowMs())
}

func limits(mod func(*TradeLimitInput)) []LimitViolation {
	in := TradeLimitInput{
		Stake:     util.ToMinor(1_000),
		Profit:    util.ToMinor(820),
		Direction: models.DirectionUp,
		Asset:     limitAsset,
		Config:    limitConfig,
		Exposure:  book(exposure.RawCounters{}),
	}
	if mod != nil {
		mod(&in)
	}
	return CheckTradeLimits(in)
}

func codes(v []LimitViolation) []string {
	out := []string{}
	for _, x := range v {
		out = append(out, x.Code)
	}
	return out
}

func wantCode(t *testing.T, v []LimitViolation, code string) {
	t.Helper()
	if !slices.Contains(codes(v), code) {
		t.Fatalf("violations %v do not contain %s", codes(v), code)
	}
}

func wantNone(t *testing.T, v []LimitViolation) {
	t.Helper()
	if v == nil || len(v) != 0 {
		t.Fatalf("expected no violations, got %v", v)
	}
}

func ptr(v int64) *int64 { return &v }

func TestTradeLimits(t *testing.T) {
	t.Run("accepts a trade inside every limit", func(t *testing.T) { wantNone(t, limits(nil)) })

	t.Run("stake below minimum", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) { in.Stake = util.ToMinor(50) }), apperr.StakeBelowMin)
	})
	t.Run("stake above maximum", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) { in.Stake = util.ToMinor(60_000) }), apperr.StakeAboveMax)
	})
	t.Run("per-user stake cap tighter than asset cap", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) {
			in.Stake = util.ToMinor(2_000)
			in.UserMaxTradeAmount = ptr(util.ToMinor(1_000))
		}), apperr.StakeAboveMax)
	})
	t.Run("user open exposure limit", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) { in.UserTotalExposure = util.ToMinor(499_500) }), apperr.UserLimitExceeded)
	})
	t.Run("max open trades", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) { in.UserOpenTrades = 50 }), apperr.UserLimitExceeded)
	})
	t.Run("asset gross exposure limit", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) {
			in.Stake = util.ToMinor(50_000)
			in.Exposure = book(exposure.RawCounters{UpStake: util.ToMinor(500_000), DownStake: util.ToMinor(480_000), OpenTrades: 40})
		}), apperr.ExposureLimitExceeded)
	})
	t.Run("one-sided exposure limit", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) {
			in.Stake = util.ToMinor(50_000)
			in.Exposure = book(exposure.RawCounters{UpStake: util.ToMinor(390_000), OpenTrades: 10})
		}), apperr.ExposureLimitExceeded)
	})
	t.Run("trade reducing a one-sided book is allowed", func(t *testing.T) {
		wantNone(t, limits(func(in *TradeLimitInput) {
			in.Stake = util.ToMinor(50_000)
			in.Direction = models.DirectionDown
			in.Exposure = book(exposure.RawCounters{UpStake: util.ToMinor(390_000), OpenTrades: 10})
		}))
	})
	t.Run("platform gross limit", func(t *testing.T) {
		wantCode(t, limits(func(in *TradeLimitInput) { in.GlobalGrossExposure = util.ToMinor(19_999_500) }), apperr.RiskLimitExceeded)
	})
	t.Run("user dominating a substantial book", func(t *testing.T) {
		// Book 600,000 > per-user limit 500,000: 310,000 of 610,000 is over 40%.
		wantCode(t, limits(func(in *TradeLimitInput) {
			in.Stake = util.ToMinor(10_000)
			in.UserAssetExposure = util.ToMinor(300_000)
			in.Exposure = book(exposure.RawCounters{UpStake: util.ToMinor(400_000), DownStake: util.ToMinor(200_000), OpenTrades: 30})
		}), apperr.RiskLimitExceeded)
	})
	t.Run("no concentration rule on a small book", func(t *testing.T) {
		wantNone(t, limits(func(in *TradeLimitInput) {
			in.Stake = util.ToMinor(10_000)
			in.UserAssetExposure = util.ToMinor(10_000)
			in.Exposure = book(exposure.RawCounters{UpStake: util.ToMinor(10_000), OpenTrades: 1})
		}))
	})
	t.Run("reports every violation", func(t *testing.T) {
		v := limits(func(in *TradeLimitInput) { in.Stake = util.ToMinor(90_000); in.UserOpenTrades = 60 })
		if len(v) <= 1 {
			t.Fatalf("expected several violations, got %v", v)
		}
	})
}

func TestClassifyRiskLevel(t *testing.T) {
	cfg := &models.RiskConfig{CriticalExposurePercent: 90, HedgeThresholdPercent: 30}
	cases := map[float64]models.RiskLevel{5: "LOW", 35: "MEDIUM", 70: "HIGH", 95: "CRITICAL"}
	for score, want := range cases {
		if got := ClassifyRiskLevel(score, cfg); got != want {
			t.Errorf("score %v: %s, want %s", score, got, want)
		}
	}
	strict := &models.RiskConfig{CriticalExposurePercent: 50, HedgeThresholdPercent: 10}
	if got := ClassifyRiskLevel(35, strict); got != models.RiskLevelHigh {
		t.Errorf("strict 35: %s", got)
	}
	if got := ClassifyRiskLevel(55, strict); got != models.RiskLevelCritical {
		t.Errorf("strict 55: %s", got)
	}
}

func TestSelectHedgeLevel(t *testing.T) {
	levels := models.DefaultHedgeLevels()
	cases := []struct {
		u    float64
		want string
	}{
		{10, "NO_HEDGE"}, {45, "PARTIAL_HEDGE"}, {70, "HIGH_HEDGE"}, {95, "MAX_ALLOWED_HEDGE"},
		// The boundary value belongs to the band it opens.
		{30, "PARTIAL_HEDGE"}, {60, "HIGH_HEDGE"}, {80, "MAX_ALLOWED_HEDGE"},
		// Caps at the top band beyond 100 percent.
		{250, "MAX_ALLOWED_HEDGE"},
	}
	for _, c := range cases {
		if got := SelectHedgeLevel(c.u, levels); got == nil || got.Name != c.want {
			t.Errorf("utilisation %v: %v, want %s", c.u, got, c.want)
		}
	}
	if SelectHedgeLevel(10, nil) != nil {
		t.Error("empty levels must return nil")
	}
}

func TestToFixed(t *testing.T) {
	cases := []struct {
		x    float64
		d    int
		want string
	}{
		{0.125, 2, "0.13"}, {1.005, 2, "1.00"}, {2.5, 0, "3"}, {42, 1, "42.0"},
		{0, 2, "0.00"}, {-1.25, 1, "-1.3"}, {0.0001, 4, "0.0001"}, {12.3456, 2, "12.35"},
	}
	for _, c := range cases {
		if got := toFixed(c.x, c.d); got != c.want {
			t.Errorf("toFixed(%v, %d) = %s, want %s", c.x, c.d, got, c.want)
		}
	}
}
