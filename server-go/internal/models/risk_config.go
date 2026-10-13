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

	// Expiry and feed policy.
	DrawPolicy        DrawPolicy `bson:"drawPolicy"`
	FeedHealthyMaxMs  int64      `bson:"feedHealthyMaxMs"`
	FeedDegradedMaxMs int64      `bson:"feedDegradedMaxMs"`
	FeedStaleMaxMs    int64      `bson:"feedStaleMaxMs"`
	FeedTickMaxAgeMs  int64      `bson:"feedTickMaxAgeMs"`
	// Refuse new trades on an asset whose feed is STALE or OFFLINE.
	HaltTradingOnStaleFeed bool `bson:"haltTradingOnStaleFeed"`
	// Refuse new trades while the feed is merely DEGRADED.
	HaltTradingOnDegradedFeed bool `bson:"haltTradingOnDegradedFeed"`
	// Global kill switch.
	TradingHalted bool `bson:"tradingHalted"`

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
		DrawPolicy:                       DrawPolicyRefund,
		FeedHealthyMaxMs:                 100,
		FeedDegradedMaxMs:                300,
		FeedStaleMaxMs:                   1000,
		FeedTickMaxAgeMs:                 5000,
		HaltTradingOnStaleFeed:           true,
		HaltTradingOnDegradedFeed:        false,
		TradingHalted:                    false,
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
	UpdatedBy                        *string      `json:"updatedBy"`
	Note                             *string      `json:"note"`
	CreatedAt                        iso.Time     `json:"createdAt"`
	UpdatedAt                        iso.Time     `json:"updatedAt"`
	V                                int64        `json:"__v"`
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
		UpdatedBy:                        hexPtr(c.UpdatedBy),
		Note:                             c.Note,
		CreatedAt:                        iso.From(c.CreatedAt),
		UpdatedAt:                        iso.From(c.UpdatedAt),
		V:                                c.V,
	}
}
