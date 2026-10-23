package marketdata

import (
	"testing"

	"binarytrading/internal/models"
)

// TestAssessFreshness: the one freshness rule for quotes and acceptances.
func TestAssessFreshness(t *testing.T) {
	cfg := models.DefaultRiskConfig()
	cfg.FeedHealthyMaxMs, cfg.FeedDegradedMaxMs, cfg.FeedTickMaxAgeMs = 100, 300, 5000
	cfg.PricingStaleAfterMs, cfg.HaltTradingOnStaleFeed, cfg.HaltTradingOnDegradedFeed = 2000, true, false
	now := int64(1_000_000)
	tick := func(age, latency int64) *models.NormalisedTick {
		return &models.NormalisedTick{MarketTick: models.MarketTick{Price: 100, ReceivedAt: now - age}, TotalLatencyMs: latency}
	}
	cases := []struct {
		name  string
		tick  *models.NormalisedTick
		block *FeedBlock
		mod   func(*models.RiskConfig)
		code  string
	}{
		{"fresh", tick(10, 5), nil, nil, FreshnessOK},
		{"1 s old: STALE for monitoring, fresh for pricing", tick(1000, 5), nil, nil, FreshnessOK},
		{"exactly 2 s", tick(2000, 5), nil, nil, FreshnessOK},
		{"older than 2 s", tick(2001, 5), nil, nil, FreshnessStale},
		{"no tick", nil, nil, nil, FreshnessNoPrice},
		{"no price", &models.NormalisedTick{MarketTick: models.MarketTick{ReceivedAt: now}}, nil, nil, FreshnessNoPrice},
		{"a block refuses a fresh tick", tick(10, 5), &FeedBlock{Code: BlockClockDrift, Detail: "x"}, nil, BlockClockDrift},
		{"late delivery, stale switch on", tick(10, 301), nil, nil, FreshnessLatency},
		{"late delivery, stale switch off", tick(10, 301), nil, func(c *models.RiskConfig) { c.HaltTradingOnStaleFeed = false }, FreshnessOK},
		{"degraded delivery, degraded switch on", tick(10, 150), nil, func(c *models.RiskConfig) { c.HaltTradingOnDegradedFeed = true }, FreshnessLatency},
		{"OFFLINE limit below stale_after", tick(1500, 5), nil, func(c *models.RiskConfig) { c.FeedTickMaxAgeMs = 1000 }, FreshnessStale},
		{"missing stale_after defaults to 2 s", tick(1999, 5), nil, func(c *models.RiskConfig) { c.PricingStaleAfterMs = 0 }, FreshnessOK},
	}
	for _, c := range cases {
		cc := cfg
		if c.mod != nil {
			c.mod(&cc)
		}
		got := AssessFreshness(&cc, c.tick, c.block, now)
		if got.Code != c.code || got.OK != (c.code == FreshnessOK) {
			t.Errorf("%s: %+v, want %s", c.name, got, c.code)
		}
	}
}
