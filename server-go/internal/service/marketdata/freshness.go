package marketdata

import (
	"context"
	"strconv"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/service/riskconfig"
)

// The freshness rule: one rule for quoting and for accepting.
//
// A price may be offered (GET /binary/quotes, pricing.Evaluate) and an offer
// or an inline price accepted (the trade gate AssertFeedTradable, then
// pricing.TakeOffer re-validating the offer) only on a last accepted tick
// that passes AssessFreshness - the same function, on the same thresholds,
// at both moments (owner's pricing specification 8.1 step 5, stale_after_s,
// and 8.2, re-validation at acceptance). So an offer is never issued on a
// price that acceptance would refuse at that instant; a refusal at
// acceptance means the market changed in between (and comes with a fresh
// quote, see binary.CreateTrade).
//
//	no tick, or a price that is not positive              -> NO_PRICE
//	age = now - receivedAt > pricingStaleAfterMs (2 s)    -> STALE_MARKET_DATA
//	   (never beyond feedTickMaxAgeMs: an OFFLINE feed always refuses)
//	a feed data rule block (TICKS_DROPPED, CLOCK_DRIFT)   -> the block's code
//	delivery latency (tick.totalLatencyMs) beyond the feed bands, where the
//	operator's switches ask for it: > feedDegradedMaxMs with
//	haltTradingOnStaleFeed, > feedHealthyMaxMs with haltTradingOnDegradedFeed
//	                                                      -> FEED_LATENCY
//
// The feed status bands of GetFeedHealth (HEALTHY / DEGRADED / STALE by
// max(latency, age)) remain the monitoring view of the feed; for quotes and
// trades the age of the price is judged by pricingStaleAfterMs alone, and the
// halt switches judge how late the price arrived. Sell now keeps its own,
// stricter, versioned rule (settlement policy earlyCloseMaxTickAgeMs).

// Freshness verdict codes (Freshness.Code). A feed data rule block reports
// its own code (BlockTicksDropped, BlockClockDrift).
const (
	FreshnessOK      = "OK"
	FreshnessNoPrice = "NO_PRICE"
	// The same string as pricing.CodeStaleMarketData.
	FreshnessStale   = "STALE_MARKET_DATA"
	FreshnessLatency = "FEED_LATENCY"
)

// DefaultStaleAfterMs is pricingStaleAfterMs when a configuration lacks it.
const DefaultStaleAfterMs = 2000

// Freshness is the verdict on one tick.
type Freshness struct {
	OK     bool
	Code   string
	Reason string
	// now - tick.receivedAt (0 without a tick).
	AgeMs int64
	// The age limit applied: pricingStaleAfterMs, capped at feedTickMaxAgeMs.
	StaleAfterMs int64
}

// StaleAfterMsFor is the age limit of the freshness rule.
func StaleAfterMsFor(cfg *models.RiskConfig) int64 {
	limit := cfg.PricingStaleAfterMs
	if limit <= 0 {
		limit = DefaultStaleAfterMs
	}
	if cfg.FeedTickMaxAgeMs > 0 && cfg.FeedTickMaxAgeMs < limit {
		limit = cfg.FeedTickMaxAgeMs
	}
	return limit
}

// AssessFreshness applies the freshness rule (pure). block is the asset's
// feed data rule block, nil when none.
func AssessFreshness(cfg *models.RiskConfig, tick *models.NormalisedTick, block *FeedBlock, now int64) Freshness {
	f := Freshness{StaleAfterMs: StaleAfterMsFor(cfg)}
	if tick == nil || !(tick.Price > 0) {
		f.Code, f.Reason = FreshnessNoPrice, "no price has been received"
		return f
	}
	f.AgeMs = now - tick.ReceivedAt
	if f.AgeMs > f.StaleAfterMs {
		f.Code = FreshnessStale
		f.Reason = "the last price is " + strconv.FormatInt(f.AgeMs, 10) + " ms old (limit " +
			strconv.FormatInt(f.StaleAfterMs, 10) + " ms)"
		return f
	}
	if block != nil {
		f.Code, f.Reason = block.Code, "cannot be trusted: "+block.String()
		return f
	}
	late := tick.TotalLatencyMs
	if (cfg.HaltTradingOnStaleFeed && late > cfg.FeedDegradedMaxMs) ||
		(cfg.HaltTradingOnDegradedFeed && late > cfg.FeedHealthyMaxMs) {
		f.Code = FreshnessLatency
		f.Reason = "prices are arriving " + strconv.FormatInt(late, 10) + " ms late"
		return f
	}
	f.OK, f.Code = true, FreshnessOK
	return f
}

// CheckFreshness applies the freshness rule to tick for an asset, reading
// the active configuration and the asset's feed data rule block.
func CheckFreshness(ctx context.Context, assetID string, tick *models.NormalisedTick, now int64) (Freshness, error) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return Freshness{}, err
	}
	block, err := FeedBlockFor(ctx, assetID)
	if err != nil {
		return Freshness{}, err
	}
	return AssessFreshness(cfg, tick, block, now), nil
}

// FreshnessError is the API error for a refused verdict: 503 FEED_UNHEALTHY
// (NO_PRICE_AVAILABLE when no price exists at all), the verdict in
// details.reason.
func FreshnessError(symbol string, f Freshness) error {
	details := map[string]any{"reason": f.Code, "detail": f.Reason, "ageMs": f.AgeMs, "staleAfterMs": f.StaleAfterMs}
	msg := "Market data for " + symbol + " is not fresh. New trades are paused for this asset."
	switch f.Code {
	case FreshnessNoPrice:
		msg = "Market data for " + symbol + " is OFFLINE. New trades are paused for this asset."
	case BlockTicksDropped, BlockClockDrift:
		msg = "Market data for " + symbol + " cannot be trusted right now. New trades are paused for this asset."
	case FreshnessLatency:
		msg = "Market data for " + symbol + " is arriving late. New trades are paused for this asset."
	}
	return apperr.New(apperr.FeedUnhealthy, msg, 503, details)
}
