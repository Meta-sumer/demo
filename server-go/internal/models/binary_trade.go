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

// TradeCancellation is the progress of an administrative cancel.
type TradeCancellation struct {
	RequestedAt time.Time `bson:"requestedAt"`
	// The stake has been released on the wallet (ledger key
	// trade:settle:<id>).
	FundsReleased   bool       `bson:"fundsReleased"`
	FundsReleasedAt *time.Time `bson:"fundsReleasedAt,omitempty"`
	// The trade's exposure has been taken off the book (claimed before the
	// book is changed, so it is removed at most once).
	ExposureReleased   bool       `bson:"exposureReleased"`
	ExposureReleasedAt *time.Time `bson:"exposureReleasedAt,omitempty"`
	// Attempts and the last error of the release, for the repair report.
	Attempts  int64  `bson:"attempts"`
	LastError string `bson:"lastError,omitempty"`
}

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
	// What an exact match of entry and expiry price settles as, frozen at
	// acceptance: part of the trade's terms, like the payout. Absent on
	// trades accepted before it was stored; they settle under the policy in
	// force at settlement (settlement.DrawPolicyFor).
	DrawPolicy *DrawPolicy `bson:"drawPolicy,omitempty"`

	// The pricing offer the trade accepted (an inline offer when the app sent
	// none) and the frozen pricing record: locked payout, rule version and
	// every component (docs/audit/pricing/pricing-spec.md). Absent on trades
	// accepted before the pricing engine existed.
	OfferID *string       `bson:"offerId,omitempty"`
	Pricing *TradePricing `bson:"pricing,omitempty"`

	// Settlement state machine (models/settlement_evidence.go): who moved the
	// trade out of OPEN, and the immutable outcome recorded with that claim.
	// Absent on trades that are still OPEN and on trades settled before the
	// state machine existed.
	SettlementClaim  *SettlementClaim  `bson:"settlementClaim,omitempty"`
	SettlementIntent *SettlementIntent `bson:"settlementIntent,omitempty"`

	SettledAt           *time.Time `bson:"settledAt"`
	SettlementLatencyMs *int64     `bson:"settlementLatencyMs"`
	CancelReason        *string    `bson:"cancelReason"`
	// Administrative cancellation (binary.CancelTrade): the stake release
	// and the exposure release are recorded as they complete, so a cancel
	// interrupted between them is finished by a retry or by
	// binary.RepairCancelledTrades. Absent on trades cancelled before.
	Cancellation *TradeCancellation `bson:"cancellation,omitempty"`
	IP           *string            `bson:"ip"`
	CreatedAt    time.Time          `bson:"createdAt"`
	UpdatedAt    time.Time          `bson:"updatedAt"`
	V            int64              `bson:"__v"`
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
	// The pricing offer this trade was accepted on.
	OfferID *string `json:"offerId,omitempty"`
	// The settlement rule behind expiryPrice (TICK_AT_OR_BEFORE_EXPIRY,
	// NO_PRICE_VOID_REFUND, ...); omitted while open and on trades settled
	// before it was recorded.
	ExpiryPriceSource *string `json:"expiryPriceSource,omitempty"`
	// True when the trade was voided and the stake returned because no valid
	// expiry price existed (status DRAW, rule NO_PRICE_VOID_REFUND). Apps
	// show it as "Void - refunded", not as a tie.
	Voided     bool   `json:"voided,omitempty"`
	VoidReason string `json:"voidReason,omitempty"`
	// The tie rule frozen on the trade at acceptance (omitted on older
	// trades).
	DrawPolicy *DrawPolicy `json:"drawPolicy,omitempty"`
	// Where the order came from. Their own data, and the way a customer spots
	// a session they did not open.
	IP        *string   `json:"ip"`
	SettledAt *iso.Time `json:"settledAt"`
	CreatedAt iso.Time  `json:"createdAt"`
}

// VoidReasonNoPrice is the customer wording of a void for want of a price.
const VoidReasonNoPrice = "No valid market price at expiry; stake returned"

// TradeVoided reports whether a trade was voided for want of a price.
func TradeVoided(t *BinaryTrade) bool {
	return t.ExpiryPriceSource != nil && *t.ExpiryPriceSource == SettlementRuleNoPriceVoidRefund
}

func ToTradeView(t *BinaryTrade) BinaryTradeView {
	v := toTradeView(t)
	v.ExpiryPriceSource = t.ExpiryPriceSource
	if TradeVoided(t) {
		v.Voided, v.VoidReason = true, VoidReasonNoPrice
	}
	return v
}

func toTradeView(t *BinaryTrade) BinaryTradeView {
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
		OfferID:          t.OfferID,
		DrawPolicy:       t.DrawPolicy,
		IP:               t.IP,
		SettledAt:        iso.Ptr(t.SettledAt),
		CreatedAt:        iso.From(t.CreatedAt),
	}
}
