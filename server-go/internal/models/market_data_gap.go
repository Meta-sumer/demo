package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollMarketDataGaps records every gap in an asset's market data (audit
// F-17, docs/audit/operations/feed-rules.md): a stretch in which nothing at
// all - no update, no heartbeat - arrived from the provider for longer than
// the asset's gap threshold. A quiet market (heartbeats arriving, the price
// not moving) is not a gap. Written by the market-feed leader
// (service/marketdata/feedquality.go); one row per gap, unique on
// (assetId, startedAt).
const CollMarketDataGaps = "market_data_gaps"

// Gap reasons.
const (
	// Connected, but nothing arrived for the symbol.
	GapReasonSilence = "SILENCE"
	// The provider connection was down.
	GapReasonDisconnected = "DISCONNECTED"
	// Nothing was ingested at all (no feed leader: a restart or a leader
	// change); found when data resumes.
	GapReasonNotIngesting = "NOT_INGESTING"
)

type MarketDataGap struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	AssetID  bson.ObjectID `bson:"assetId"`
	Symbol   string        `bson:"symbol"`
	Provider string        `bson:"provider"`
	Reason   string        `bson:"reason"`
	// Server receipt time of the last data before the gap and of the first
	// data after it (epoch ms); EndedAt is nil while the gap is open.
	StartedAt  int64  `bson:"startedAt"`
	EndedAt    *int64 `bson:"endedAt"`
	DurationMs *int64 `bson:"durationMs"`
	// When the gap was detected (live), or when data resumed (found after
	// the fact).
	DetectedAt int64 `bson:"detectedAt"`
	// The threshold in force and the expected interval between messages it
	// was derived from.
	ThresholdMs        int64     `bson:"thresholdMs"`
	ExpectedIntervalMs float64   `bson:"expectedIntervalMs"`
	Open               bool      `bson:"open"`
	CreatedAt          time.Time `bson:"createdAt"`
	UpdatedAt          time.Time `bson:"updatedAt"`
}

func init() {
	c := CollMarketDataGaps
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "startedAt", Value: -1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "open", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}

// MarketDataGapView is a gap as the admin API returns it.
type MarketDataGapView struct {
	ID                 string   `json:"id"`
	AssetID            string   `json:"assetId"`
	Symbol             string   `json:"symbol"`
	Provider           string   `json:"provider"`
	Reason             string   `json:"reason"`
	StartedAt          int64    `json:"startedAt"`
	EndedAt            *int64   `json:"endedAt"`
	DurationMs         *int64   `json:"durationMs"`
	DetectedAt         int64    `json:"detectedAt"`
	ThresholdMs        int64    `json:"thresholdMs"`
	ExpectedIntervalMs float64  `json:"expectedIntervalMs"`
	Open               bool     `json:"open"`
	CreatedAt          iso.Time `json:"createdAt"`
}

func ToMarketDataGapView(g *MarketDataGap) MarketDataGapView {
	return MarketDataGapView{
		ID: g.ID.Hex(), AssetID: g.AssetID.Hex(), Symbol: g.Symbol, Provider: g.Provider, Reason: g.Reason,
		StartedAt: g.StartedAt, EndedAt: g.EndedAt, DurationMs: g.DurationMs, DetectedAt: g.DetectedAt,
		ThresholdMs: g.ThresholdMs, ExpectedIntervalMs: g.ExpectedIntervalMs, Open: g.Open, CreatedAt: iso.From(g.CreatedAt),
	}
}
