package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
)

// CollSettlements is the settlement ledger (spec sections 18 and 42).
//
// One row per settled trade, enforced by a unique index on tradeId. Inserting
// this row is the operation that claims the settlement: a second worker that
// tries the same trade collides with the index and stops, which is what makes
// double payouts structurally impossible. timestamps { createdAt only }.
const CollSettlements = "settlements"

const SettlementDefaultSettledBy = "settlement.worker"

type Settlement struct {
	ID      bson.ObjectID `bson:"_id,omitempty"`
	TradeID bson.ObjectID `bson:"tradeId"`
	UserID  bson.ObjectID `bson:"userId"`
	AssetID bson.ObjectID `bson:"assetId"`
	Symbol  string        `bson:"symbol"`
	Result  TradeResult   `bson:"result"`
	// The trade account; missing = LIVE. DEMO rows are left out of the house P&L.
	AccountType     string  `bson:"accountType,omitempty"`
	EntryPrice      float64 `bson:"entryPrice"`
	ExpiryPrice     float64 `bson:"expiryPrice"`
	ExpiryTimestamp int64   `bson:"expiryTimestamp"`
	Stake           int64   `bson:"stake"`
	PayoutPercent   float64 `bson:"payoutPercent"`
	// Credited back to the user, minor units (stake + profit on a win).
	PayoutAmount int64 `bson:"payoutAmount"`
	// Platform gross result, minor units (positive = platform gain).
	PlatformResult      int64          `bson:"platformResult"`
	Currency            string         `bson:"currency"`
	WalletTransactionID *bson.ObjectID `bson:"walletTransactionId"`
	SettledBy           string         `bson:"settledBy"`
	LatencyMs           *int64         `bson:"latencyMs"`
	// How the outcome was decided (price, tick timestamps, provider, rule,
	// risk-config version, policy). Absent on rows written before it existed.
	Evidence  *SettlementEvidence `bson:"evidence,omitempty"`
	CreatedAt time.Time           `bson:"createdAt"`
	V         int64               `bson:"__v"`
}

func init() {
	c := CollSettlements
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "tradeId", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}
