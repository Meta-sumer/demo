package usdt

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/wallets"
)

// WalletView is the USDT wallet as the API returns it. Amounts micro-USDT.
type WalletView struct {
	ID               string `json:"id"`
	Currency         string `json:"currency"`
	Decimals         int    `json:"decimals"`
	Balance          int64  `json:"balance"`
	LockedBalance    int64  `json:"lockedBalance"`
	AvailableBalance int64  `json:"availableBalance"`
	// Received on deposits BeeX has not confirmed yet (partially_paid,
	// confirming); not spendable.
	PendingDeposits int64    `json:"pendingDeposits"`
	UpdatedAt       iso.Time `json:"updatedAt"`
}

func toWalletView(w models.WalletView, pending int64) WalletView {
	return WalletView{
		ID: w.ID, Currency: models.CurrencyUSDT, Decimals: models.USDTDecimals,
		Balance: w.Balance, LockedBalance: w.LockedBalance, AvailableBalance: w.AvailableBalance,
		PendingDeposits: pending, UpdatedAt: w.UpdatedAt,
	}
}

// fundingWallet returns (creating if needed) the customer's FUNDING wallet.
func fundingWallet(ctx context.Context, userID string) (models.WalletView, error) {
	return wallets.GetOrCreateWallet(ctx, userID, models.AccountFunding, models.CurrencyUSDT)
}

func pendingDeposits(ctx context.Context, uid bson.ObjectID) (int64, error) {
	cur, err := mongox.C(models.CollUsdtDeposits).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{
			"userId":   uid,
			"credited": false,
			"status":   bson.M{"$in": bson.A{models.InvoicePartiallyPaid, models.InvoiceConfirming}},
		}},
		bson.M{"$group": bson.M{"_id": nil, "sum": bson.M{"$sum": "$amountReceived"}}},
	})
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Sum int64 `bson:"sum"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Sum, nil
}

// GetWallet is GET /wallet/usdt/balance.
func GetWallet(ctx context.Context, userID string) (WalletView, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return WalletView{}, apperr.Unauth()
	}
	w, err := fundingWallet(ctx, userID)
	if err != nil {
		return WalletView{}, err
	}
	pending, err := pendingDeposits(ctx, uid)
	if err != nil {
		return WalletView{}, err
	}
	return toWalletView(w, pending), nil
}

// TradingWallet is the customer's LIVE USDT wallet (the transfer target), or
// nil when it has never been used. It is never part of the trading account
// list, which holds the INR LIVE and DEMO wallets.
func TradingWallet(ctx context.Context, userID string) (*models.WalletView, error) {
	w, err := wallets.GetWallet(ctx, userID, models.AccountLive, models.CurrencyUSDT)
	if err != nil {
		if apperr.HasCode(err, apperr.WalletNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

// Overview is GET /wallet/usdt.
type Overview struct {
	Wallet                   WalletView               `json:"wallet"`
	Trading                  *models.WalletView       `json:"trading"`
	Networks                 []models.UsdtNetworkView `json:"networks"`
	Enabled                  bool                     `json:"enabled"`
	TransferToTradingEnabled bool                     `json:"transferToTradingEnabled"`
}

func GetOverview(ctx context.Context, userID string) (Overview, error) {
	wallet, err := GetWallet(ctx, userID)
	if err != nil {
		return Overview{}, err
	}
	trading, err := TradingWallet(ctx, userID)
	if err != nil {
		return Overview{}, err
	}
	networks, err := NetworkViews(ctx)
	if err != nil {
		return Overview{}, err
	}
	return Overview{
		Wallet: wallet, Trading: trading, Networks: networks,
		Enabled: Enabled(), TransferToTradingEnabled: Enabled() && TransferToTradingEnabled(),
	}, nil
}

// ListTransactions is the FUNDING wallet's ledger.
func ListTransactions(ctx context.Context, userID string, typ *models.WalletTransactionType, page, limit int64) ([]models.WalletTransactionLean, int64, error) {
	return wallets.ListWalletTransactions(ctx, wallets.LedgerQuery{
		UserID: userID, AccountType: models.AccountFunding, Currency: models.CurrencyUSDT,
		Type: typ, Page: page, Limit: limit,
	})
}
