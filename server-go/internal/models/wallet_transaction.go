package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollWalletTransactions is the append-only wallet ledger. Every balance
// change has exactly one row, carrying before/after figures so the wallet can
// be replayed and reconciled against the trade and settlement ledgers (spec
// sections 9, 30, 42). timestamps { createdAt only }.
const CollWalletTransactions = "wallet_transactions"

// WalletTransaction: all amounts minor units.
type WalletTransaction struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	WalletID bson.ObjectID `bson:"walletId"`
	UserID   bson.ObjectID `bson:"userId"`
	// The wallet account; missing = LIVE (rows written before accounts existed).
	AccountType string                `bson:"accountType,omitempty"`
	Type        WalletTransactionType `bson:"type"`
	Movement    MoneyMovement         `bson:"movement"`
	// Always positive; movement carries the direction.
	Amount        int64   `bson:"amount"`
	Currency      string  `bson:"currency"`
	BalanceBefore int64   `bson:"balanceBefore"`
	BalanceAfter  int64   `bson:"balanceAfter"`
	LockedBefore  int64   `bson:"lockedBefore"`
	LockedAfter   int64   `bson:"lockedAfter"`
	ReferenceType *string `bson:"referenceType"`
	ReferenceID   *string `bson:"referenceId"`
	// Unique per logical operation - the guard against double credits.
	IdempotencyKey *string `bson:"idempotencyKey"`
	Description    *string `bson:"description"`
	// Mixed, default {}.
	Metadata map[string]any `bson:"metadata"`
	// USDT rows only: the network (e.g. USDTTRC20) and the provider fee in
	// micro-USDT. Absent on every other row.
	Network   *string   `bson:"network,omitempty"`
	Fee       *int64    `bson:"fee,omitempty"`
	CreatedAt time.Time `bson:"createdAt"`
	V         int64     `bson:"__v"`
}

func init() {
	c := CollWalletTransactions
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "type", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "referenceId", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "walletId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
		// Partial unique index: rows without a key are unconstrained, rows with
		// one can only ever be written once. This is what makes credits
		// non-repeatable.
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "idempotencyKey", Value: 1}},
			Options: options.Index().SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "idempotencyKey", Value: bson.D{{Key: "$type", Value: "string"}}}}),
		},
	)
}

// WalletTransactionLean is the lean() ledger row listWalletTransactions sends.
type WalletTransactionLean struct {
	ID             string                `json:"_id"`
	WalletID       string                `json:"walletId"`
	UserID         string                `json:"userId"`
	AccountType    string                `json:"accountType"`
	Type           WalletTransactionType `json:"type"`
	Movement       MoneyMovement         `json:"movement"`
	Amount         int64                 `json:"amount"`
	Currency       string                `json:"currency"`
	BalanceBefore  int64                 `json:"balanceBefore"`
	BalanceAfter   int64                 `json:"balanceAfter"`
	LockedBefore   int64                 `json:"lockedBefore"`
	LockedAfter    int64                 `json:"lockedAfter"`
	ReferenceType  *string               `json:"referenceType"`
	ReferenceID    *string               `json:"referenceId"`
	IdempotencyKey *string               `json:"idempotencyKey"`
	Description    *string               `json:"description"`
	Metadata       any                   `json:"metadata,omitempty"`
	Network        *string               `json:"network,omitempty"`
	Fee            *int64                `json:"fee,omitempty"`
	CreatedAt      iso.Time              `json:"createdAt"`
	V              int64                 `json:"__v"`
}

func ToWalletTransactionLean(t *WalletTransaction) WalletTransactionLean {
	out := WalletTransactionLean{
		ID:             t.ID.Hex(),
		WalletID:       t.WalletID.Hex(),
		UserID:         t.UserID.Hex(),
		AccountType:    NormAccountType(t.AccountType),
		Type:           t.Type,
		Movement:       t.Movement,
		Amount:         t.Amount,
		Currency:       t.Currency,
		BalanceBefore:  t.BalanceBefore,
		BalanceAfter:   t.BalanceAfter,
		LockedBefore:   t.LockedBefore,
		LockedAfter:    t.LockedAfter,
		ReferenceType:  t.ReferenceType,
		ReferenceID:    t.ReferenceID,
		IdempotencyKey: t.IdempotencyKey,
		Description:    t.Description,
		Network:        t.Network,
		Fee:            t.Fee,
		CreatedAt:      iso.From(t.CreatedAt),
		V:              t.V,
	}
	if t.Metadata != nil {
		out.Metadata = mongox.JSONValue(t.Metadata)
	}
	return out
}
