package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollHedgeOrders: hedge orders placed with an external execution provider
// (spec sections 14-16, 22). Hedging manages the platform's aggregate market
// exposure only; the settlement engine never reads this collection.
const CollHedgeOrders = "hedge_orders"

// HedgeDecisionSnapshot records the decision inputs - hedging must be
// auditable (rule 9).
type HedgeDecisionSnapshot struct {
	NetExposureAtDecision   int64     `bson:"netExposureAtDecision"`
	GrossExposureAtDecision int64     `bson:"grossExposureAtDecision"`
	UtilisationPercent      float64   `bson:"utilisationPercent"`
	RiskLevel               RiskLevel `bson:"riskLevel"`
	HedgeLevel              string    `bson:"hedgeLevel"`
	TargetHedgeRatio        float64   `bson:"targetHedgeRatio"`
	TargetHedgeNotional     int64     `bson:"targetHedgeNotional"`
	ExistingHedgeNotional   int64     `bson:"existingHedgeNotional"`
	RiskConfigVersion       int64     `bson:"riskConfigVersion"`
	Volatility              float64   `bson:"volatility"`
	SecondsToNearestExpiry  *float64  `bson:"secondsToNearestExpiry"`
}

type HedgeOrder struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	Reference string        `bson:"reference"`
	AssetID   bson.ObjectID `bson:"assetId"`
	Symbol    string        `bson:"symbol"`
	Provider  string        `bson:"provider"`
	// Market side the platform takes to offset the book.
	Side Direction `bson:"side"`
	// Requested notional, minor units.
	RequestedNotional int64                 `bson:"requestedNotional"`
	FilledNotional    int64                 `bson:"filledNotional"`
	AverageFillPrice  *float64              `bson:"averageFillPrice"`
	RequestPrice      *float64              `bson:"requestPrice"`
	Status            HedgeOrderStatus      `bson:"status"`
	ProviderOrderID   *string               `bson:"providerOrderId"`
	Decision          HedgeDecisionSnapshot `bson:"decision"`
	// Realised profit or loss on the hedge leg, minor units.
	RealisedPnl      int64      `bson:"realisedPnl"`
	UnrealisedPnl    int64      `bson:"unrealisedPnl"`
	ClosedAt         *time.Time `bson:"closedAt"`
	Error            *string    `bson:"error"`
	RequestLatencyMs *int64     `bson:"requestLatencyMs"`
	FillLatencyMs    *int64     `bson:"fillLatencyMs"`
	IdempotencyKey   string     `bson:"idempotencyKey"`
	CreatedAt        time.Time  `bson:"createdAt"`
	UpdatedAt        time.Time  `bson:"updatedAt"`
	V                int64      `bson:"__v"`
}

func init() {
	c := CollHedgeOrders
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "idempotencyKey", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "status", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}

type HedgeOrderView struct {
	ID                  string           `json:"id"`
	Reference           string           `json:"reference"`
	AssetID             string           `json:"assetId"`
	Symbol              string           `json:"symbol"`
	Provider            string           `json:"provider"`
	Side                Direction        `json:"side"`
	RequestedNotional   int64            `json:"requestedNotional"`
	FilledNotional      int64            `json:"filledNotional"`
	RemainingNotional   int64            `json:"remainingNotional"`
	AverageFillPrice    *float64         `json:"averageFillPrice"`
	RequestPrice        *float64         `json:"requestPrice"`
	Status              HedgeOrderStatus `json:"status"`
	ProviderOrderID     *string          `json:"providerOrderId"`
	RiskLevel           RiskLevel        `json:"riskLevel"`
	HedgeLevel          string           `json:"hedgeLevel"`
	TargetHedgeNotional int64            `json:"targetHedgeNotional"`
	RealisedPnl         int64            `json:"realisedPnl"`
	UnrealisedPnl       int64            `json:"unrealisedPnl"`
	Error               *string          `json:"error"`
	RequestLatencyMs    *int64           `json:"requestLatencyMs"`
	CreatedAt           iso.Time         `json:"createdAt"`
	UpdatedAt           iso.Time         `json:"updatedAt"`
}

func ToHedgeView(o *HedgeOrder) HedgeOrderView {
	return HedgeOrderView{
		ID:                  o.ID.Hex(),
		Reference:           o.Reference,
		AssetID:             o.AssetID.Hex(),
		Symbol:              o.Symbol,
		Provider:            o.Provider,
		Side:                o.Side,
		RequestedNotional:   o.RequestedNotional,
		FilledNotional:      o.FilledNotional,
		RemainingNotional:   max(0, o.RequestedNotional-o.FilledNotional),
		AverageFillPrice:    o.AverageFillPrice,
		RequestPrice:        o.RequestPrice,
		Status:              o.Status,
		ProviderOrderID:     o.ProviderOrderID,
		RiskLevel:           o.Decision.RiskLevel,
		HedgeLevel:          o.Decision.HedgeLevel,
		TargetHedgeNotional: o.Decision.TargetHedgeNotional,
		RealisedPnl:         o.RealisedPnl,
		UnrealisedPnl:       o.UnrealisedPnl,
		Error:               o.Error,
		RequestLatencyMs:    o.RequestLatencyMs,
		CreatedAt:           iso.From(o.CreatedAt),
		UpdatedAt:           iso.From(o.UpdatedAt),
	}
}
