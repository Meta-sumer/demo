package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollBinaryTrades holds binary trades (spec section 10).
const CollBinaryTrades = "binary_trades"

const BinaryTradeDefaultCurrency = "INR"

// BinaryTrade: every field the platform used to accept the trade is frozen on
// the document - entry price, payout percent and the payout config version.
// Later admin changes never touch a historical trade (rule 11).
type BinaryTrade struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	Reference string        `bson:"reference"`
	UserID    bson.ObjectID `bson:"userId"`
	AssetID   bson.ObjectID `bson:"assetId"`
	Symbol    string        `bson:"symbol"`

	Direction Direction `bson:"direction"`

	// LIVE or DEMO - the wallet the stake is locked on and settled into. A
	// stored trade without it is LIVE. DEMO trades never touch the house book.
	AccountType string `bson:"accountType,omitempty"`

	// Stake in minor units.
	Stake         int64   `bson:"stake"`
	Currency      string  `bson:"currency"`
	PayoutPercent float64 `bson:"payoutPercent"`
	// Profit paid on a win, minor units - derived at accept time.
	Profit              int64          `bson:"profit"`
	PayoutConfigID      *bson.ObjectID `bson:"payoutConfigId"`
	PayoutConfigVersion *int64         `bson:"payoutConfigVersion"`

	// Server-authoritative entry price. Never supplied by a client.
	EntryPrice  float64  `bson:"entryPrice"`
	ExpiryPrice *float64 `bson:"expiryPrice"`

	// Epoch ms.
	EntryTimestamp  int64 `bson:"entryTimestamp"`
	ExpiryTimestamp int64 `bson:"expiryTimestamp"`

	Status TradeStatus  `bson:"status"`
	Result *TradeResult `bson:"result"`

	// Net wallet effect at settlement, minor units (positive = credited).
	SettlementAmount *int64 `bson:"settlementAmount"`
	// Platform gross result on this trade, minor units (positive = platform gain).
	PlatformResult *int64 `bson:"platformResult"`

	IdempotencyKey *string `bson:"idempotencyKey"`
	// Hash of the accepted request payload, so a key replayed with a different
	// request is detected even after the Redis reservation has expired.
	IdempotencyFingerprint *string `bson:"idempotencyFingerprint"`
	// Provenance of the entry price, kept for dispute resolution.
	EntryTickSequence       *int64 `bson:"entryTickSequence"`
	EntryProviderTimestamp  *int64 `bson:"entryProviderTimestamp"`
	EntryFeedLatencyMs      *int64 `bson:"entryFeedLatencyMs"`
	ExpiryTickSequence      *int64 `bson:"expiryTickSequence"`
	ExpiryProviderTimestamp *int64 `bson:"expiryProviderTimestamp"`
	// Which tick rule produced expiryPrice.
	ExpiryPriceSource *string `bson:"expiryPriceSource"`

	// Settlement state machine (models/settlement_evidence.go): who moved the
	// trade out of OPEN, and the immutable outcome recorded with that claim.
	// Absent on trades that are still OPEN and on trades settled before the
	// state machine existed.
	SettlementClaim  *SettlementClaim  `bson:"settlementClaim,omitempty"`
	SettlementIntent *SettlementIntent `bson:"settlementIntent,omitempty"`

	SettledAt           *time.Time `bson:"settledAt"`
	SettlementLatencyMs *int64     `bson:"settlementLatencyMs"`
	CancelReason        *string    `bson:"cancelReason"`
	IP                  *string    `bson:"ip"`
	CreatedAt           time.Time  `bson:"createdAt"`
	UpdatedAt           time.Time  `bson:"updatedAt"`
	V                   int64      `bson:"__v"`
}

func init() {
	c := CollBinaryTrades
	RegisterIndexes(
		// Spec section 32.
		mongox.Index{Collection: c, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "status", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "status", Value: 1}, {Key: "expiryTimestamp", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "expiryTimestamp", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
		// One trade per (user, idempotency key) - the database-level duplicate guard.
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "userId", Value: 1}, {Key: "idempotencyKey", Value: 1}},
			Options: options.Index().SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "idempotencyKey", Value: bson.D{{Key: "$type", Value: "string"}}}}),
		},
	)
}

type BinaryTradeView struct {
	ID               string       `json:"id"`
	Reference        string       `json:"reference"`
	UserID           string       `json:"userId"`
	AssetID          string       `json:"assetId"`
	Symbol           string       `json:"symbol"`
	Direction        Direction    `json:"direction"`
	AccountType      string       `json:"accountType"`
	Stake            int64        `json:"stake"`
	Currency         string       `json:"currency"`
	PayoutPercent    float64      `json:"payoutPercent"`
	Profit           int64        `json:"profit"`
	PotentialReturn  int64        `json:"potentialReturn"`
	EntryPrice       float64      `json:"entryPrice"`
	ExpiryPrice      *float64     `json:"expiryPrice"`
	EntryTimestamp   int64        `json:"entryTimestamp"`
	ExpiryTimestamp  int64        `json:"expiryTimestamp"`
	Status           TradeStatus  `json:"status"`
	Result           *TradeResult `json:"result"`
	SettlementAmount *int64       `json:"settlementAmount"`
	// Where the order came from. Their own data, and the way a customer spots
	// a session they did not open.
	IP        *string   `json:"ip"`
	SettledAt *iso.Time `json:"settledAt"`
	CreatedAt iso.Time  `json:"createdAt"`
}

func ToTradeView(t *BinaryTrade) BinaryTradeView {
	return BinaryTradeView{
		ID:            t.ID.Hex(),
		Reference:     t.Reference,
		UserID:        t.UserID.Hex(),
		AssetID:       t.AssetID.Hex(),
		Symbol:        t.Symbol,
		Direction:     t.Direction,
		AccountType:   NormAccountType(t.AccountType),
		Stake:         t.Stake,
		Currency:      t.Currency,
		PayoutPercent: t.PayoutPercent,
		Profit:        t.Profit,
		// What the user gets back on a win: their stake plus the profit.
		PotentialReturn:  t.Stake + t.Profit,
		EntryPrice:       t.EntryPrice,
		ExpiryPrice:      t.ExpiryPrice,
		EntryTimestamp:   t.EntryTimestamp,
		ExpiryTimestamp:  t.ExpiryTimestamp,
		Status:           t.Status,
		Result:           t.Result,
		SettlementAmount: t.SettlementAmount,
		IP:               t.IP,
		SettledAt:        iso.Ptr(t.SettledAt),
		CreatedAt:        iso.From(t.CreatedAt),
	}
}
