package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollRiskConfigs: platform risk, hedging and trading configuration (spec
// sections 13, 15, 17). Versioned and append-only: saving a change deactivates
// the current row and inserts the next version, so every risk decision can be
// replayed against the configuration that was live at the time.
const CollRiskConfigs = "risk_configs"

const RiskScopeGlobal = "GLOBAL"

var RiskScopeValues = []string{RiskScopeGlobal}

type DrawPolicy string

const (
	DrawPolicyRefund DrawPolicy = "REFUND"
	DrawPolicyLoss   DrawPolicy = "LOSS"
	DrawPolicyWin    DrawPolicy = "WIN"
)

var DrawPolicyValues = []string{string(DrawPolicyRefund), string(DrawPolicyLoss), string(DrawPolicyWin)}

type HedgeLevel struct {
	Name string `bson:"name" json:"name"`
	// Exposure utilisation band, percent of the applicable exposure limit.
	FromPercent float64 `bson:"fromPercent" json:"fromPercent"`
	ToPercent   float64 `bson:"toPercent" json:"toPercent"`
	// Fraction of net exposure to hedge inside this band, 0..1.
	HedgeRatio float64 `bson:"hedgeRatio" json:"hedgeRatio"`
}

// DefaultHedgeLevels are the spec section 15 defaults - bands and ratios, not
// hardcoded behaviour. Returns a fresh slice.
func DefaultHedgeLevels() []HedgeLevel {
	return []HedgeLevel{
		{Name: "NO_HEDGE", FromPercent: 0, ToPercent: 30, HedgeRatio: 0},
		{Name: "PARTIAL_HEDGE", FromPercent: 30, ToPercent: 60, HedgeRatio: 0.4},
		{Name: "HIGH_HEDGE", FromPercent: 60, ToPercent: 80, HedgeRatio: 0.7},
		{Name: "MAX_ALLOWED_HEDGE", FromPercent: 80, ToPercent: 100_000, HedgeRatio: 0.95},
	}
}

type RiskConfig struct {
	ID      bson.ObjectID `bson:"_id,omitempty"`
	Scope   string        `bson:"scope"`
	Version int64         `bson:"version"`
	Active  bool          `bson:"active"`

	// Exposure ceilings, minor units.
	MaxAssetExposure int64 `bson:"maxAssetExposure"`
	MaxUserExposure  int64 `bson:"maxUserExposure"`
	MaxGrossExposure int64 `bson:"maxGrossExposure"`

	// Per trade bounds, minor units.
	MinTradeAmount       int64 `bson:"minTradeAmount"`
	MaxTradeAmount       int64 `bson:"maxTradeAmount"`
	MaxOpenTradesPerUser int64 `bson:"maxOpenTradesPerUser"`
	// A single user may not hold more than this share of one asset's book.
	MaxUserAssetConcentrationPercent float64 `bson:"maxUserAssetConcentrationPercent"`

	// Liability ceilings, minor units; 0 disables the rule. Liability is the
	// worst-case net payout of the open LIVE book using each trade's frozen
	// profit, with UP and DOWN netted only inside one expiry bucket:
	//   bucket   max(0, upProfit - downStake, downProfit - upStake)
	//   asset    sum over its buckets
	//   platform sum over assets
	// A trade is refused only when it raises the figure above the ceiling.
	MaxExpiryLiability   int64 `bson:"maxExpiryLiability"`
	MaxAssetLiability    int64 `bson:"maxAssetLiability"`
	MaxPlatformLiability int64 `bson:"maxPlatformLiability"`

	// Hedging.
	HedgeThresholdPercent   float64      `bson:"hedgeThresholdPercent"`
	CriticalExposurePercent float64      `bson:"criticalExposurePercent"`
	HedgeLevels             []HedgeLevel `bson:"hedgeLevels"`
	// Below this remaining time a new hedge is pointless, seconds.
	MinSecondsToExpiryForHedge int64 `bson:"minSecondsToExpiryForHedge"`
	// Extra hedge ratio applied when realised volatility is elevated, 0..1.
	VolatilityHedgeBoost float64 `bson:"volatilityHedgeBoost"`

	// Maximum adverse move between the price the client showed and the fill,
	// in percent. 0 disables the guard.
	SlippageTolerancePercent float64 `bson:"slippageTolerancePercent"`
	// Early close ("Sell now"): the fee is the platform spread for buying an
	// unsettled position back; the cutoff stops a customer closing in the final
	// seconds, where latency would decide the outcome.
	EarlyCloseEnabled                bool    `bson:"earlyCloseEnabled"`
	EarlyCloseFeePercent             float64 `bson:"earlyCloseFeePercent"`
	MinSecondsRemainingForEarlyClose int64   `bson:"minSecondsRemainingForEarlyClose"`
	// Calibrated correction of the Sell-now probability (audit F-12,
	// docs/audit/pricing/early-close.md); empty = the uncorrected formula.
	EarlyCloseCalibration []EarlyCloseCalibrationRow `bson:"earlyCloseCalibration"`
	// Lower clamp of the Sell-now P(win), percent (the upper clamp stays
	// 99%). 1 was the fixed value before audit F-12.
	EarlyCloseMinProbabilityPercent float64 `bson:"earlyCloseMinProbabilityPercent"`

	// Expiry and feed policy.
	DrawPolicy        DrawPolicy `bson:"drawPolicy"`
	FeedHealthyMaxMs  int64      `bson:"feedHealthyMaxMs"`
	FeedDegradedMaxMs int64      `bson:"feedDegradedMaxMs"`
	// Deprecated: never read. The STALE band is everything above
	// FeedDegradedMaxMs up to FeedTickMaxAgeMs (OFFLINE beyond it). Kept so
	// stored versions decode and old admin builds read it; no longer
	// editable or validated (docs/audit/operations/feed-rules.md).
	FeedStaleMaxMs   int64 `bson:"feedStaleMaxMs"`
	FeedTickMaxAgeMs int64 `bson:"feedTickMaxAgeMs"`
	// Refuse new trades on an asset whose feed is STALE or OFFLINE.
	HaltTradingOnStaleFeed bool `bson:"haltTradingOnStaleFeed"`
	// Refuse new trades while the feed is merely DEGRADED.
	HaltTradingOnDegradedFeed bool `bson:"haltTradingOnDegradedFeed"`
	// Global kill switch.
	TradingHalted bool `bson:"tradingHalted"`

	// Feed data rules (docs/audit/operations/feed-rules.md, audit F-17).
	// How long accepted ticks are kept in md:hist: settlement reads the
	// expiry price from it, so it must cover the settlement defer window
	// with a margin (validated).
	FeedTickHistoryRetentionMs int64 `bson:"feedTickHistoryRetentionMs"`
	// Gap detection: a symbol is in a gap when nothing at all (no update, no
	// heartbeat) has arrived for max(FeedGapMinMs, FeedGapIntervalMultiple x
	// its expected interval between messages).
	FeedGapMinMs            int64   `bson:"feedGapMinMs"`
	FeedGapIntervalMultiple float64 `bson:"feedGapIntervalMultiple"`
	// After a tick is dropped (ingestion buffer full) the asset's feed is
	// treated as STALE for this long; 0 = drops are only counted.
	FeedSaturationHoldMs int64 `bson:"feedSaturationHoldMs"`
	// The server clock may differ from the provider's by at most this much
	// (clock-sync check, net of half the round trip); beyond it new trades
	// are refused.
	FeedMaxClockDriftMs int64 `bson:"feedMaxClockDriftMs"`
	// How often the clock-sync check runs on the feed leader; 0 = off.
	FeedClockCheckIntervalMs int64 `bson:"feedClockCheckIntervalMs"`

	// Settlement and early-close policy (service/settlement/policy.go,
	// docs/audit/customer/disclosures.md; audit F-11). Read by field name by
	// settlement.PolicyFor; a stored version without them uses the defaults.
	SettlementMaxTickAgeMs          int64   `bson:"settlementMaxTickAgeMs"`
	SettlementConfirmationWindowMs  int64   `bson:"settlementConfirmationWindowMs"`
	SettlementDeferWindowMs         int64   `bson:"settlementDeferWindowMs"`
	SettlementRetryDelayMs          int64   `bson:"settlementRetryDelayMs"`
	SettlementMaxClockSkewMs        int64   `bson:"settlementMaxClockSkewMs"`
	SettlementNoPriceRule           string  `bson:"settlementNoPriceRule"`
	EarlyCloseFloorMs               int64   `bson:"earlyCloseFloorMs"`
	EarlyCloseMaxTickAgeMs          int64   `bson:"earlyCloseMaxTickAgeMs"`
	EarlyCloseQuoteMaxAgeMs         int64   `bson:"earlyCloseQuoteMaxAgeMs"`
	EarlyCloseQuoteTolerancePercent float64 `bson:"earlyCloseQuoteTolerancePercent"`

	// Why trading is halted (kill switch, spec 7.4); "" when not halted.
	TradingHaltReason string `bson:"tradingHaltReason"`

	// Pricing engine (docs/audit/pricing/pricing-spec.md; the owner's Dynamic
	// Pricing specification, section 11). DYNAMIC prices every trade with the
	// payout formula inside the payout row's bounds; FIXED pays the payout
	// row's percent (the pre-engine behaviour). Payout terms are fractions
	// (0.02 = 2 payout points). Every offer and trade is stamped with
	// RuleVersion (PricingRuleLabel + this config's version + the payout row's).
	PricingMode         string `bson:"pricingMode"`
	PricingRuleLabel    string `bson:"pricingRuleLabel"`
	PricingStaleAfterMs int64  `bson:"pricingStaleAfterMs"`
	// Market-data normalisation (spec 4).
	PricingMaxSpreadBps           float64 `bson:"pricingMaxSpreadBps"`
	PricingMaxJumpBps             float64 `bson:"pricingMaxJumpBps"`
	PricingJumpConfirmations      int64   `bson:"pricingJumpConfirmations"`
	PricingPendingOutlierMaxAgeMs int64   `bson:"pricingPendingOutlierMaxAgeMs"`
	// Volatility (spec 5). VolSampleIntervalMs spaces the EWMA observations
	// (0 = every accepted quote, the spec's literal rule).
	PricingEwmaLambda          float64 `bson:"pricingEwmaLambda"`
	PricingVolSampleIntervalMs int64   `bson:"pricingVolSampleIntervalMs"`
	// Payout terms (spec 6).
	PricingKVol           float64               `bson:"pricingKVol"`
	PricingMaxVolAdj      float64               `bson:"pricingMaxVolAdj"`
	PricingVolBlockRatio  float64               `bson:"pricingVolBlockRatio"`
	PricingKSpread        float64               `bson:"pricingKSpread"`
	PricingMaxSpreadAdj   float64               `bson:"pricingMaxSpreadAdj"`
	PricingKExposure      float64               `bson:"pricingKExposure"`
	PricingMaxExposureAdj float64               `bson:"pricingMaxExposureAdj"`
	PricingExpiryFactors  []PricingExpiryFactor `bson:"pricingExpiryFactors"`
	PricingPayoutStep     float64               `bson:"pricingPayoutStep"`
	// Offers (spec 8).
	PricingOfferTTLMs     int64   `bson:"pricingOfferTtlMs"`
	PricingMaxSlippageBps float64 `bson:"pricingMaxSlippageBps"`
	// A trade sent without an offer is refused for a re-quote when the payout
	// the app displayed is more than this many points above the price now.
	PricingDisplayTolerancePercent float64 `bson:"pricingDisplayTolerancePercent"`
	// Calibrated safety guard (audit F-02/F-03): refuse an offer whose
	// conservative expected platform result, from the calibrated
	// win/draw-probability model, is below GuardMinEdgePercent of the stake.
	PricingGuardEnabled        bool    `bson:"pricingGuardEnabled"`
	PricingGuardMinEdgePercent float64 `bson:"pricingGuardMinEdgePercent"`
	// Check the conservative bound (probabilities plus the calibrated
	// uncertainty add-ons) instead of the point estimate.
	PricingGuardConservative bool `bson:"pricingGuardConservative"`
	// Hedging: a hedge the asset relies on is unavailable: WIDEN adds
	// HedgeUnavailableAdj to the payout deductions, REFUSE blocks. The Binance
	// cost basis (bps per side; the taker fee is the USD-M futures rate, the
	// hedging venue) is reported next to each symbol's configured hedge cost.
	PricingHedgeUnavailableAction string  `bson:"pricingHedgeUnavailableAction"`
	PricingHedgeUnavailableAdj    float64 `bson:"pricingHedgeUnavailableAdj"`
	PricingHedgeTakerFeeBps       float64 `bson:"pricingHedgeTakerFeeBps"`
	PricingHedgeSpreadBps         float64 `bson:"pricingHedgeSpreadBps"`
	PricingHedgeSlippageBps       float64 `bson:"pricingHedgeSlippageBps"`
	// Per-instrument rules (spec 11) and per-symbol halts (spec 7.4).
	PricingSymbolRules []PricingSymbolRule `bson:"pricingSymbolRules"`
	PricingHalts       []PricingHalt       `bson:"pricingHalts"`

	UpdatedBy *bson.ObjectID `bson:"updatedBy"`
	Note      *string        `bson:"note"`
	CreatedAt time.Time      `bson:"createdAt"`
	UpdatedAt time.Time      `bson:"updatedAt"`
	V         int64          `bson:"__v"`
}

// DefaultRiskConfig returns a row with every Mongoose schema default applied.
// The TS service reads the active row hydrated (not lean) precisely so that
// fields added to the schema since the row was stored get their defaults; the
// Go service gets the same effect by decoding into this value.
func DefaultRiskConfig() RiskConfig {
	return RiskConfig{
		Scope:                            RiskScopeGlobal,
		Version:                          1,
		Active:                           true,
		MaxAssetExposure:                 5_000_000_00,
		MaxUserExposure:                  500_000_00,
		MaxGrossExposure:                 20_000_000_00,
		MinTradeAmount:                   10_00,
		MaxTradeAmount:                   100_000_00,
		MaxOpenTradesPerUser:             50,
		MaxUserAssetConcentrationPercent: 40,
		HedgeThresholdPercent:            30,
		CriticalExposurePercent:          90,
		HedgeLevels:                      DefaultHedgeLevels(),
		MinSecondsToExpiryForHedge:       10,
		VolatilityHedgeBoost:             0.1,
		SlippageTolerancePercent:         0.1,
		EarlyCloseEnabled:                true,
		EarlyCloseFeePercent:             5,
		MinSecondsRemainingForEarlyClose: 10,
		EarlyCloseCalibration:            DefaultEarlyCloseCalibration(),
		EarlyCloseMinProbabilityPercent:  DefaultEarlyCloseMinProbabilityPercent,
		DrawPolicy:                       DrawPolicyRefund,
		FeedHealthyMaxMs:                 100,
		FeedDegradedMaxMs:                300,
		FeedStaleMaxMs:                   1000,
		FeedTickMaxAgeMs:                 5000,
		HaltTradingOnStaleFeed:           true,
		HaltTradingOnDegradedFeed:        false,
		TradingHalted:                    false,

		FeedTickHistoryRetentionMs: DefaultTickHistoryRetentionMs,
		FeedGapMinMs:               3000,
		FeedGapIntervalMultiple:    10,
		FeedSaturationHoldMs:       5000,
		FeedMaxClockDriftMs:        1000,
		FeedClockCheckIntervalMs:   60_000,

		SettlementMaxTickAgeMs:          2000,
		SettlementConfirmationWindowMs:  2000,
		SettlementDeferWindowMs:         60_000,
		SettlementRetryDelayMs:          1000,
		SettlementMaxClockSkewMs:        2000,
		SettlementNoPriceRule:           DefaultSettlementNoPriceRule,
		EarlyCloseFloorMs:               1000,
		EarlyCloseMaxTickAgeMs:          1000,
		EarlyCloseQuoteMaxAgeMs:         10_000,
		EarlyCloseQuoteTolerancePercent: 5,

		PricingMode:                    PricingModeDynamic,
		PricingRuleLabel:               "rules-2026.10.1",
		PricingStaleAfterMs:            2000,
		PricingMaxSpreadBps:            8,
		PricingMaxJumpBps:              150,
		PricingJumpConfirmations:       3,
		PricingPendingOutlierMaxAgeMs:  5000,
		PricingEwmaLambda:              0.97,
		PricingVolSampleIntervalMs:     1000,
		PricingKVol:                    0.06,
		PricingMaxVolAdj:               0.12,
		PricingVolBlockRatio:           4,
		PricingKSpread:                 0.02,
		PricingMaxSpreadAdj:            0.05,
		PricingKExposure:               0.08,
		PricingMaxExposureAdj:          0.10,
		PricingExpiryFactors:           DefaultPricingExpiryFactors(),
		PricingPayoutStep:              0.005,
		PricingOfferTTLMs:              3000,
		PricingMaxSlippageBps:          2,
		PricingDisplayTolerancePercent: 0,
		PricingGuardEnabled:            true,
		PricingGuardMinEdgePercent:     1,
		PricingHedgeUnavailableAction:  PricingActionWiden,
		PricingHedgeUnavailableAdj:     0.03,
		PricingHedgeTakerFeeBps:        5,
		PricingHedgeSpreadBps:          1,
		PricingHedgeSlippageBps:        2,
		PricingSymbolRules:             DefaultPricingSymbolRules(),
		PricingHalts:                   []PricingHalt{},
	}
}

func init() {
	c := CollRiskConfigs
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "scope", Value: 1}, {Key: "version", Value: -1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "scope", Value: 1}},
			Options:    options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "active", Value: true}}),
		},
	)
}

// RiskConfigLean is the whole document as JSON, the shape getRiskConfig /
// listRiskConfigVersions send (admin RiskConfig type).
type RiskConfigLean struct {
	ID                               string       `json:"_id"`
	Scope                            string       `json:"scope"`
	Version                          int64        `json:"version"`
	Active                           bool         `json:"active"`
	MaxAssetExposure                 int64        `json:"maxAssetExposure"`
	MaxUserExposure                  int64        `json:"maxUserExposure"`
	MaxGrossExposure                 int64        `json:"maxGrossExposure"`
	MinTradeAmount                   int64        `json:"minTradeAmount"`
	MaxTradeAmount                   int64        `json:"maxTradeAmount"`
	MaxOpenTradesPerUser             int64        `json:"maxOpenTradesPerUser"`
	MaxUserAssetConcentrationPercent float64      `json:"maxUserAssetConcentrationPercent"`
	MaxExpiryLiability               int64        `json:"maxExpiryLiability"`
	MaxAssetLiability                int64        `json:"maxAssetLiability"`
	MaxPlatformLiability             int64        `json:"maxPlatformLiability"`
	HedgeThresholdPercent            float64      `json:"hedgeThresholdPercent"`
	CriticalExposurePercent          float64      `json:"criticalExposurePercent"`
	HedgeLevels                      []HedgeLevel `json:"hedgeLevels"`
	MinSecondsToExpiryForHedge       int64        `json:"minSecondsToExpiryForHedge"`
	VolatilityHedgeBoost             float64      `json:"volatilityHedgeBoost"`
	SlippageTolerancePercent         float64      `json:"slippageTolerancePercent"`
	EarlyCloseEnabled                bool         `json:"earlyCloseEnabled"`
	EarlyCloseFeePercent             float64      `json:"earlyCloseFeePercent"`
	MinSecondsRemainingForEarlyClose int64        `json:"minSecondsRemainingForEarlyClose"`
	DrawPolicy                       DrawPolicy   `json:"drawPolicy"`
	FeedHealthyMaxMs                 int64        `json:"feedHealthyMaxMs"`
	FeedDegradedMaxMs                int64        `json:"feedDegradedMaxMs"`
	FeedStaleMaxMs                   int64        `json:"feedStaleMaxMs"`
	FeedTickMaxAgeMs                 int64        `json:"feedTickMaxAgeMs"`
	HaltTradingOnStaleFeed           bool         `json:"haltTradingOnStaleFeed"`
	HaltTradingOnDegradedFeed        bool         `json:"haltTradingOnDegradedFeed"`
	TradingHalted                    bool         `json:"tradingHalted"`

	FeedTickHistoryRetentionMs      int64   `json:"feedTickHistoryRetentionMs"`
	FeedGapMinMs                    int64   `json:"feedGapMinMs"`
	FeedGapIntervalMultiple         float64 `json:"feedGapIntervalMultiple"`
	FeedSaturationHoldMs            int64   `json:"feedSaturationHoldMs"`
	FeedMaxClockDriftMs             int64   `json:"feedMaxClockDriftMs"`
	FeedClockCheckIntervalMs        int64   `json:"feedClockCheckIntervalMs"`
	SettlementMaxTickAgeMs          int64   `json:"settlementMaxTickAgeMs"`
	SettlementConfirmationWindowMs  int64   `json:"settlementConfirmationWindowMs"`
	SettlementDeferWindowMs         int64   `json:"settlementDeferWindowMs"`
	SettlementRetryDelayMs          int64   `json:"settlementRetryDelayMs"`
	SettlementMaxClockSkewMs        int64   `json:"settlementMaxClockSkewMs"`
	SettlementNoPriceRule           string  `json:"settlementNoPriceRule"`
	EarlyCloseFloorMs               int64   `json:"earlyCloseFloorMs"`
	EarlyCloseMaxTickAgeMs          int64   `json:"earlyCloseMaxTickAgeMs"`
	EarlyCloseQuoteMaxAgeMs         int64   `json:"earlyCloseQuoteMaxAgeMs"`
	EarlyCloseQuoteTolerancePercent float64 `json:"earlyCloseQuoteTolerancePercent"`
	// Added: the calibrated Sell-now correction (never null) and the lower
	// clamp of its probability.
	EarlyCloseCalibration           []EarlyCloseCalibrationRow `json:"earlyCloseCalibration"`
	EarlyCloseMinProbabilityPercent float64                    `json:"earlyCloseMinProbabilityPercent"`

	TradingHaltReason              string                `json:"tradingHaltReason"`
	PricingMode                    string                `json:"pricingMode"`
	PricingRuleLabel               string                `json:"pricingRuleLabel"`
	PricingStaleAfterMs            int64                 `json:"pricingStaleAfterMs"`
	PricingMaxSpreadBps            float64               `json:"pricingMaxSpreadBps"`
	PricingMaxJumpBps              float64               `json:"pricingMaxJumpBps"`
	PricingJumpConfirmations       int64                 `json:"pricingJumpConfirmations"`
	PricingPendingOutlierMaxAgeMs  int64                 `json:"pricingPendingOutlierMaxAgeMs"`
	PricingEwmaLambda              float64               `json:"pricingEwmaLambda"`
	PricingVolSampleIntervalMs     int64                 `json:"pricingVolSampleIntervalMs"`
	PricingKVol                    float64               `json:"pricingKVol"`
	PricingMaxVolAdj               float64               `json:"pricingMaxVolAdj"`
	PricingVolBlockRatio           float64               `json:"pricingVolBlockRatio"`
	PricingKSpread                 float64               `json:"pricingKSpread"`
	PricingMaxSpreadAdj            float64               `json:"pricingMaxSpreadAdj"`
	PricingKExposure               float64               `json:"pricingKExposure"`
	PricingMaxExposureAdj          float64               `json:"pricingMaxExposureAdj"`
	PricingExpiryFactors           []PricingExpiryFactor `json:"pricingExpiryFactors"`
	PricingPayoutStep              float64               `json:"pricingPayoutStep"`
	PricingOfferTTLMs              int64                 `json:"pricingOfferTtlMs"`
	PricingMaxSlippageBps          float64               `json:"pricingMaxSlippageBps"`
	PricingDisplayTolerancePercent float64               `json:"pricingDisplayTolerancePercent"`
	PricingGuardEnabled            bool                  `json:"pricingGuardEnabled"`
	PricingGuardMinEdgePercent     float64               `json:"pricingGuardMinEdgePercent"`
	PricingGuardConservative       bool                  `json:"pricingGuardConservative"`
	PricingHedgeUnavailableAction  string                `json:"pricingHedgeUnavailableAction"`
	PricingHedgeUnavailableAdj     float64               `json:"pricingHedgeUnavailableAdj"`
	PricingHedgeTakerFeeBps        float64               `json:"pricingHedgeTakerFeeBps"`
	PricingHedgeSpreadBps          float64               `json:"pricingHedgeSpreadBps"`
	PricingHedgeSlippageBps        float64               `json:"pricingHedgeSlippageBps"`
	PricingSymbolRules             []PricingSymbolRule   `json:"pricingSymbolRules"`
	PricingHalts                   []PricingHalt         `json:"pricingHalts"`

	UpdatedBy *string  `json:"updatedBy"`
	Note      *string  `json:"note"`
	CreatedAt iso.Time `json:"createdAt"`
	UpdatedAt iso.Time `json:"updatedAt"`
	V         int64    `json:"__v"`
}

func ToRiskConfigLean(c *RiskConfig) RiskConfigLean {
	levels := c.HedgeLevels
	if levels == nil {
		levels = []HedgeLevel{}
	}
	return RiskConfigLean{
		ID:                               c.ID.Hex(),
		Scope:                            c.Scope,
		Version:                          c.Version,
		Active:                           c.Active,
		MaxAssetExposure:                 c.MaxAssetExposure,
		MaxUserExposure:                  c.MaxUserExposure,
		MaxGrossExposure:                 c.MaxGrossExposure,
		MinTradeAmount:                   c.MinTradeAmount,
		MaxTradeAmount:                   c.MaxTradeAmount,
		MaxOpenTradesPerUser:             c.MaxOpenTradesPerUser,
		MaxUserAssetConcentrationPercent: c.MaxUserAssetConcentrationPercent,
		MaxExpiryLiability:               c.MaxExpiryLiability,
		MaxAssetLiability:                c.MaxAssetLiability,
		MaxPlatformLiability:             c.MaxPlatformLiability,
		HedgeThresholdPercent:            c.HedgeThresholdPercent,
		CriticalExposurePercent:          c.CriticalExposurePercent,
		HedgeLevels:                      levels,
		MinSecondsToExpiryForHedge:       c.MinSecondsToExpiryForHedge,
		VolatilityHedgeBoost:             c.VolatilityHedgeBoost,
		SlippageTolerancePercent:         c.SlippageTolerancePercent,
		EarlyCloseEnabled:                c.EarlyCloseEnabled,
		EarlyCloseFeePercent:             c.EarlyCloseFeePercent,
		MinSecondsRemainingForEarlyClose: c.MinSecondsRemainingForEarlyClose,
		DrawPolicy:                       c.DrawPolicy,
		FeedHealthyMaxMs:                 c.FeedHealthyMaxMs,
		FeedDegradedMaxMs:                c.FeedDegradedMaxMs,
		FeedStaleMaxMs:                   c.FeedStaleMaxMs,
		FeedTickMaxAgeMs:                 c.FeedTickMaxAgeMs,
		HaltTradingOnStaleFeed:           c.HaltTradingOnStaleFeed,
		HaltTradingOnDegradedFeed:        c.HaltTradingOnDegradedFeed,
		TradingHalted:                    c.TradingHalted,

		FeedTickHistoryRetentionMs:      c.FeedTickHistoryRetentionMs,
		FeedGapMinMs:                    c.FeedGapMinMs,
		FeedGapIntervalMultiple:         c.FeedGapIntervalMultiple,
		FeedSaturationHoldMs:            c.FeedSaturationHoldMs,
		FeedMaxClockDriftMs:             c.FeedMaxClockDriftMs,
		FeedClockCheckIntervalMs:        c.FeedClockCheckIntervalMs,
		SettlementMaxTickAgeMs:          c.SettlementMaxTickAgeMs,
		SettlementConfirmationWindowMs:  c.SettlementConfirmationWindowMs,
		SettlementDeferWindowMs:         c.SettlementDeferWindowMs,
		SettlementRetryDelayMs:          c.SettlementRetryDelayMs,
		SettlementMaxClockSkewMs:        c.SettlementMaxClockSkewMs,
		SettlementNoPriceRule:           c.SettlementNoPriceRule,
		EarlyCloseFloorMs:               c.EarlyCloseFloorMs,
		EarlyCloseMaxTickAgeMs:          c.EarlyCloseMaxTickAgeMs,
		EarlyCloseQuoteMaxAgeMs:         c.EarlyCloseQuoteMaxAgeMs,
		EarlyCloseQuoteTolerancePercent: c.EarlyCloseQuoteTolerancePercent,
		EarlyCloseCalibration:           nonNilEarlyClose(c.EarlyCloseCalibration),
		EarlyCloseMinProbabilityPercent: c.EarlyCloseMinProbabilityPercent,

		TradingHaltReason:              c.TradingHaltReason,
		PricingMode:                    c.PricingMode,
		PricingRuleLabel:               c.PricingRuleLabel,
		PricingStaleAfterMs:            c.PricingStaleAfterMs,
		PricingMaxSpreadBps:            c.PricingMaxSpreadBps,
		PricingMaxJumpBps:              c.PricingMaxJumpBps,
		PricingJumpConfirmations:       c.PricingJumpConfirmations,
		PricingPendingOutlierMaxAgeMs:  c.PricingPendingOutlierMaxAgeMs,
		PricingEwmaLambda:              c.PricingEwmaLambda,
		PricingVolSampleIntervalMs:     c.PricingVolSampleIntervalMs,
		PricingKVol:                    c.PricingKVol,
		PricingMaxVolAdj:               c.PricingMaxVolAdj,
		PricingVolBlockRatio:           c.PricingVolBlockRatio,
		PricingKSpread:                 c.PricingKSpread,
		PricingMaxSpreadAdj:            c.PricingMaxSpreadAdj,
		PricingKExposure:               c.PricingKExposure,
		PricingMaxExposureAdj:          c.PricingMaxExposureAdj,
		PricingExpiryFactors:           nonNilFactors(c.PricingExpiryFactors),
		PricingPayoutStep:              c.PricingPayoutStep,
		PricingOfferTTLMs:              c.PricingOfferTTLMs,
		PricingMaxSlippageBps:          c.PricingMaxSlippageBps,
		PricingDisplayTolerancePercent: c.PricingDisplayTolerancePercent,
		PricingGuardEnabled:            c.PricingGuardEnabled,
		PricingGuardMinEdgePercent:     c.PricingGuardMinEdgePercent,
		PricingGuardConservative:       c.PricingGuardConservative,
		PricingHedgeUnavailableAction:  c.PricingHedgeUnavailableAction,
		PricingHedgeUnavailableAdj:     c.PricingHedgeUnavailableAdj,
		PricingHedgeTakerFeeBps:        c.PricingHedgeTakerFeeBps,
		PricingHedgeSpreadBps:          c.PricingHedgeSpreadBps,
		PricingHedgeSlippageBps:        c.PricingHedgeSlippageBps,
		PricingSymbolRules:             nonNilRules(c.PricingSymbolRules),
		PricingHalts:                   nonNilHalts(c.PricingHalts),

		UpdatedBy: hexPtr(c.UpdatedBy),
		Note:      c.Note,
		CreatedAt: iso.From(c.CreatedAt),
		UpdatedAt: iso.From(c.UpdatedAt),
		V:         c.V,
	}
}

// PricingExpiryFactor is one row of the expiry-factor table (spec 6.5).
type PricingExpiryFactor struct {
	ExpirySeconds int64   `bson:"expirySeconds" json:"expirySeconds"`
	Factor        float64 `bson:"factor" json:"factor"`
}

// DefaultPricingExpiryFactors is the spec's table; other expiries use 1.0.
func DefaultPricingExpiryFactors() []PricingExpiryFactor {
	return []PricingExpiryFactor{{30, 1.3}, {60, 1.0}, {120, 0.9}, {300, 0.8}}
}

// PricingSymbolRule is one instrument's pricing rules (spec 3, 11). The
// payout row of each (asset, expiry) supplies the base payout and may narrow
// min/max and override the margin; these are the defaults and the
// instrument-wide terms. Payout figures are fractions; MaxExposure is minor
// units (paise).
type PricingSymbolRule struct {
	// Platform symbol, e.g. "BTC/USD".
	Symbol string `bson:"symbol" json:"symbol"`
	// A disabled instrument is never quoted (offer blocked with the reason).
	Enabled        bool    `bson:"enabled" json:"enabled"`
	DisabledReason string  `bson:"disabledReason" json:"disabledReason"`
	BasePayout     float64 `bson:"basePayout" json:"basePayout"`
	MinPayout      float64 `bson:"minPayout" json:"minPayout"`
	MaxPayout      float64 `bson:"maxPayout" json:"maxPayout"`
	// Per-second volatility (fraction per sqrt(second)) the engine treats as
	// normal; calibrated from Binance 1-second data (calibration report).
	BaselineVol float64 `bson:"baselineVol" json:"baselineVol"`
	HedgeCost   float64 `bson:"hedgeCost" json:"hedgeCost"`
	Margin      float64 `bson:"margin" json:"margin"`
	MaxExposure int64   `bson:"maxExposure" json:"maxExposure"`
	// Stake range of the instrument, minor units; 0 = no limit of its own.
	// It narrows the asset's, the risk configuration's and the payout row's
	// limits (pricing.StakeBounds), never widens them. A stored rule without
	// these fields decodes as 0.
	MinStake int64 `bson:"minStake" json:"minStake"`
	MaxStake int64 `bson:"maxStake" json:"maxStake"`
}

// PricingHalt is a per-symbol kill switch (spec 7.4).
type PricingHalt struct {
	Symbol string    `bson:"symbol" json:"symbol"`
	Reason string    `bson:"reason" json:"reason"`
	At     time.Time `bson:"at" json:"at"`
	By     string    `bson:"by" json:"by"`
}

// DefaultTickHistoryRetentionMs is the default of feedTickHistoryRetentionMs
// (15 minutes, as config.TickHistoryRetentionMs).
const DefaultTickHistoryRetentionMs = 15 * 60 * 1000

// Launch scope: BTC/USD and ETH/USD trade; the forex rules are kept but
// disabled (no live price source for them).
const forexDisabledReason = "out of launch scope: no live price source for this instrument"

// DefaultPricingSymbolRules: the spec's crypto and forex defaults, with the
// crypto baselines replaced by the values calibrated from Binance 1-second
// data (docs/audit/pricing/calibration-report.md). MaxExposure keeps the
// spec's ratio to the largest stake (12.5x for crypto, 10x for forex) at this
// platform's stake limits.
func DefaultPricingSymbolRules() []PricingSymbolRule {
	crypto := func(sym string, baseline float64) PricingSymbolRule {
		return PricingSymbolRule{Symbol: sym, Enabled: true, BasePayout: 0.86, MinPayout: 0.65, MaxPayout: 0.88,
			BaselineVol: baseline, HedgeCost: 0.035, Margin: 0.035, MaxExposure: 1_250_000_00}
	}
	forex := func(sym string) PricingSymbolRule {
		return PricingSymbolRule{Symbol: sym, Enabled: false, DisabledReason: forexDisabledReason, BasePayout: 0.90,
			MinPayout: 0.70, MaxPayout: 0.92, BaselineVol: 1.25e-5, HedgeCost: 0.020, Margin: 0.030, MaxExposure: 2_500_000_00}
	}
	return []PricingSymbolRule{
		crypto("BTC/USD", DefaultBTCBaselineVol),
		crypto("ETH/USD", DefaultETHBaselineVol),
		forex("EUR/USD"),
		forex("GBP/USD"),
	}
}

// EarlyCloseCalibrationRow corrects the Sell-now probability in one cell of
// (seconds remaining x volatility ratio), audit F-12
// (docs/audit/pricing/early-close.md). With sigma the 60 s realised
// volatility, baseline the instrument's baselineVol and ratio = sigma /
// baseline, the first row (in the order of SortedEarlyCloseRows: rows for
// the trade's symbol before rows for any symbol, then seconds, then ratio,
// unbounded last) whose bounds hold applies, and for a price that has moved
// off the entry
//
//	sigma' = max(sigma x SigmaScale, MinVolRatio x baseline)
//	z'     = signed move / (price x sigma' x sqrt(seconds left))
//	P      = Stickiness x [in the money] + (1 - Stickiness) x Phi(z')
//
// before the clamps. No matching row (or an empty list): scale 1, no floor,
// stickiness 0 - the uncorrected formula.
type EarlyCloseCalibrationRow struct {
	// Instrument the row applies to, e.g. "BTC/USD"; "" = any instrument.
	Symbol string `bson:"symbol" json:"symbol"`
	// Upper bounds of the cell, inclusive; 0 = unbounded.
	MaxSecondsRemaining int64   `bson:"maxSecondsRemaining" json:"maxSecondsRemaining"`
	MaxVolRatio         float64 `bson:"maxVolRatio" json:"maxVolRatio"`
	SigmaScale          float64 `bson:"sigmaScale" json:"sigmaScale"`
	// Volatility floor, as a multiple of the instrument's baseline.
	MinVolRatio float64 `bson:"minVolRatio" json:"minVolRatio"`
	Stickiness  float64 `bson:"stickiness" json:"stickiness"`
}

// DefaultEarlyCloseCalibration is the table fitted by
// `cmd/calibrate -early-close` on Binance BTCUSDT and ETHUSDT 1-second data
// (FIT period: the first 60% of 2026-10-02 to 2026-10-16; evidence in
// docs/audit/pricing/early-close-backtest.md, which says whether this is
// still the fitted table). Seconds-left bands 15 / 30 / 60 / 120 / 300 /
// more, sigma / baseline bands 0.33 / 0.70 / more. Returns a fresh slice.
func DefaultEarlyCloseCalibration() []EarlyCloseCalibrationRow {
	return []EarlyCloseCalibrationRow{
		{Symbol: "BTC/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0.33, SigmaScale: 2.5, MinVolRatio: 0.5, Stickiness: 0.75},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0.7, SigmaScale: 1.75, MinVolRatio: 0.75, Stickiness: 0.5},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0, SigmaScale: 1.25, MinVolRatio: 1, Stickiness: 0.25},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 30, MaxVolRatio: 0.33, SigmaScale: 2, MinVolRatio: 0, Stickiness: 0.5},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 30, MaxVolRatio: 0.7, SigmaScale: 1.75, MinVolRatio: 0, Stickiness: 0.35},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 30, MaxVolRatio: 0, SigmaScale: 1.25, MinVolRatio: 1, Stickiness: 0.15},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 60, MaxVolRatio: 0.33, SigmaScale: 2, MinVolRatio: 0, Stickiness: 0.45},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 60, MaxVolRatio: 0.7, SigmaScale: 1.5, MinVolRatio: 0, Stickiness: 0.2},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 60, MaxVolRatio: 0, SigmaScale: 1.25, MinVolRatio: 1, Stickiness: 0.1},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 120, MaxVolRatio: 0.33, SigmaScale: 4, MinVolRatio: 0.75, Stickiness: 0.45},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 120, MaxVolRatio: 0.7, SigmaScale: 2, MinVolRatio: 0.75, Stickiness: 0.15},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 120, MaxVolRatio: 0, SigmaScale: 1.1, MinVolRatio: 1.25, Stickiness: 0.1},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 300, MaxVolRatio: 0.33, SigmaScale: 3, MinVolRatio: 0.75, Stickiness: 0.25},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 300, MaxVolRatio: 0.7, SigmaScale: 1.75, MinVolRatio: 0.75, Stickiness: 0.1},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 300, MaxVolRatio: 0, SigmaScale: 1.25, MinVolRatio: 1.25, Stickiness: 0.1},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 0, MaxVolRatio: 0.33, SigmaScale: 3, MinVolRatio: 0.75, Stickiness: 0.15},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 0, MaxVolRatio: 0.7, SigmaScale: 2, MinVolRatio: 0.75, Stickiness: 0.05},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 0, MaxVolRatio: 0, SigmaScale: 1, MinVolRatio: 1.25, Stickiness: 0.05},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0.33, SigmaScale: 2.5, MinVolRatio: 0.25, Stickiness: 0.6},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0.7, SigmaScale: 1.5, MinVolRatio: 0.5, Stickiness: 0.35},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0, SigmaScale: 1.1, MinVolRatio: 0.75, Stickiness: 0.15},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 30, MaxVolRatio: 0.33, SigmaScale: 1.75, MinVolRatio: 0, Stickiness: 0.3},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 30, MaxVolRatio: 0.7, SigmaScale: 1.25, MinVolRatio: 0.5, Stickiness: 0.15},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 30, MaxVolRatio: 0, SigmaScale: 1.1, MinVolRatio: 0, Stickiness: 0.1},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 60, MaxVolRatio: 0.33, SigmaScale: 1.75, MinVolRatio: 0, Stickiness: 0.25},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 60, MaxVolRatio: 0.7, SigmaScale: 1.25, MinVolRatio: 0.5, Stickiness: 0.1},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 60, MaxVolRatio: 0, SigmaScale: 1, MinVolRatio: 0.75, Stickiness: 0},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 120, MaxVolRatio: 0.33, SigmaScale: 2.5, MinVolRatio: 0.5, Stickiness: 0.35},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 120, MaxVolRatio: 0.7, SigmaScale: 1.5, MinVolRatio: 0.5, Stickiness: 0.1},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 120, MaxVolRatio: 0, SigmaScale: 1, MinVolRatio: 1, Stickiness: 0.05},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 300, MaxVolRatio: 0.33, SigmaScale: 2.5, MinVolRatio: 0.5, Stickiness: 0.2},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 300, MaxVolRatio: 0.7, SigmaScale: 1.5, MinVolRatio: 0.5, Stickiness: 0.05},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 300, MaxVolRatio: 0, SigmaScale: 1, MinVolRatio: 1, Stickiness: 0.05},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 0, MaxVolRatio: 0.33, SigmaScale: 2.5, MinVolRatio: 0.5, Stickiness: 0.1},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 0, MaxVolRatio: 0.7, SigmaScale: 1.5, MinVolRatio: 0.5, Stickiness: 0.05},
		{Symbol: "ETH/USD", MaxSecondsRemaining: 0, MaxVolRatio: 0, SigmaScale: 0.8, MinVolRatio: 1, Stickiness: 0.05},
	}
}

// DefaultEarlyCloseMinProbabilityPercent is the lower clamp of the Sell-now
// P(win), from the same evidence: the largest threshold up to which the
// positions the corrected model prices below it won at least (1 - fee) x
// the threshold of the time (the clamp was a fixed 1% before audit F-12).
const DefaultEarlyCloseMinProbabilityPercent = 0.05

func nonNilEarlyClose(v []EarlyCloseCalibrationRow) []EarlyCloseCalibrationRow {
	if v == nil {
		return []EarlyCloseCalibrationRow{}
	}
	return v
}

func nonNilFactors(v []PricingExpiryFactor) []PricingExpiryFactor {
	if v == nil {
		return []PricingExpiryFactor{}
	}
	return v
}

func nonNilRules(v []PricingSymbolRule) []PricingSymbolRule {
	if v == nil {
		return []PricingSymbolRule{}
	}
	return v
}

func nonNilHalts(v []PricingHalt) []PricingHalt {
	if v == nil {
		return []PricingHalt{}
	}
	return v
}
