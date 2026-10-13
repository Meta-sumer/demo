package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollWallets holds wallet balances (spec section 9). All amounts are integer
// MINOR units.
//
//	availableBalance = balance - lockedBalance   (always derived, never stored)
//
//	open : lockedBalance += stake                     (balance unchanged)
//	lose : lockedBalance -= stake, balance -= stake
//	win  : lockedBalance -= stake, balance += profit
//	draw : lockedBalance -= stake                     (stake released)
const CollWallets = "wallets"

const WalletDefaultCurrency = "INR"

// Account types. Every user has one wallet of each: LIVE holds real money
// (deposits, withdrawals, the house book); DEMO holds virtual practice money
// and never touches the house book.
//
// FUNDING is the customer's crypto wallet (currency USDT): money deposited
// and withdrawn through BeeX Payments. It is real money but it is not trading
// money, so it is never part of a live-trading aggregate (house book, P&L,
// exposure, risk). It is not requestable where an API takes an account type,
// which is why it is not in AccountTypeValues.
const (
	AccountLive    = "LIVE"
	AccountDemo    = "DEMO"
	AccountFunding = "FUNDING"
)

var AccountTypeValues = []string{AccountLive, AccountDemo}

// CurrencyUSDT is the FUNDING wallet's currency; amounts are micro-USDT.
const CurrencyUSDT = "USDT"

// DemoOpeningBalance is what a DEMO wallet starts with and is reset to:
// 10,000.00 in minor units.
const DemoOpeningBalance int64 = 1_000_000

// NormAccountType maps a stored or requested account type to LIVE, DEMO or
// FUNDING. A missing value (documents written before accounts existed) is
// LIVE.
func NormAccountType(s string) string {
	switch s {
	case AccountDemo:
		return AccountDemo
	case AccountFunding:
		return AccountFunding
	}
	return AccountLive
}

// NotDemo matches documents of the LIVE account, including those written
// before the field existed. Use it wherever the house book is aggregated.
// FUNDING (crypto wallet) documents are excluded too: they are real money but
// not trading money. $nin matches a missing field, as $ne did.
func NotDemo() bson.M { return bson.M{"$nin": bson.A{AccountDemo, AccountFunding}} }

// AccountFilter is the accountType condition selecting one account's
// documents (trades, ledger rows, settlements).
func AccountFilter(accountType string) any {
	switch NormAccountType(accountType) {
	case AccountDemo:
		return AccountDemo
	case AccountFunding:
		return AccountFunding
	}
	return NotDemo()
}

// INRCurrency matches documents in the platform's trading currency,
// including any written without a currency (the schema default is INR).
// Money sums must never mix currencies.
func INRCurrency() bson.M { return bson.M{"$in": bson.A{WalletDefaultCurrency, nil}} }

// Wallet: currency is uppercased (apply in the service).
type Wallet struct {
	ID     bson.ObjectID `bson:"_id,omitempty"`
	UserID bson.ObjectID `bson:"userId"`
	// LIVE or DEMO. A stored wallet without it is LIVE (the startup migration
	// back-fills it).
	AccountType string `bson:"accountType"`
	Currency    string `bson:"currency"`
	// Total owned funds including what is currently locked.
	Balance int64 `bson:"balance"`
	// Funds reserved by open trades or pending withdrawals.
	LockedBalance int64     `bson:"lockedBalance"`
	Version       int64     `bson:"version"`
	CreatedAt     time.Time `bson:"createdAt"`
	UpdatedAt     time.Time `bson:"updatedAt"`
	V             int64     `bson:"__v"`
}

// AvailableBalance = balance - lockedBalance.
func (w *Wallet) AvailableBalance() int64 { return w.Balance - w.LockedBalance }

func init() {
	RegisterIndexes(
		mongox.Index{
			Collection: CollWallets,
			// Created after Migrate has dropped the old {userId, currency} index.
			Keys:    bson.D{{Key: "userId", Value: 1}, {Key: "accountType", Value: 1}, {Key: "currency", Value: 1}},
			Options: options.Index().SetUnique(true),
		},
	)
}

type WalletView struct {
	ID               string   `json:"id"`
	UserID           string   `json:"userId"`
	AccountType      string   `json:"accountType"`
	Currency         string   `json:"currency"`
	Balance          int64    `json:"balance"`
	LockedBalance    int64    `json:"lockedBalance"`
	AvailableBalance int64    `json:"availableBalance"`
	UpdatedAt        iso.Time `json:"updatedAt"`
}

func ToWalletView(w *Wallet) WalletView {
	return WalletView{
		ID:               w.ID.Hex(),
		UserID:           w.UserID.Hex(),
		AccountType:      NormAccountType(w.AccountType),
		Currency:         w.Currency,
		Balance:          w.Balance,
		LockedBalance:    w.LockedBalance,
		AvailableBalance: w.AvailableBalance(),
		UpdatedAt:        iso.From(w.UpdatedAt),
	}
}
