package wallets

import (
	"context"
	"strconv"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
)

// The DEMO account: virtual practice money. It moves through the same guarded,
// ledgered mutation path as real money, so its balance always reconciles with
// its ledger - it is simply never counted in the house book.

const refDemoAccount = "DemoAccount"

func demoOpeningKey(userID string) string { return "demo:open:" + userID }

// EnsureDemoWallet returns the user's DEMO wallet, creating it and crediting
// the opening balance on first use. The credit is keyed per user, so
// concurrent first requests fund it exactly once.
func EnsureDemoWallet(ctx context.Context, userID string) (models.WalletView, error) {
	wallet, err := GetOrCreateWallet(ctx, userID, models.AccountDemo, "")
	if err != nil {
		return models.WalletView{}, err
	}
	key := demoOpeningKey(userID)
	err = mongox.C(models.CollWalletTransactions).FindOne(ctx, bson.M{"idempotencyKey": key}).Err()
	if err == nil {
		return wallet, nil
	}
	if !mongox.IsNotFound(err) {
		return models.WalletView{}, err
	}
	res, err := CreditWallet(ctx, AdjustmentInput{
		UserID:         userID,
		AccountType:    models.AccountDemo,
		Amount:         models.DemoOpeningBalance,
		Type:           models.WalletTxAdjustmentCredit,
		ReferenceType:  refDemoAccount,
		ReferenceID:    "demo-open",
		IdempotencyKey: key,
		Description:    "Demo account opening balance",
	})
	if err != nil {
		return models.WalletView{}, err
	}
	return res.Wallet, nil
}

// ListAccounts returns the LIVE and DEMO wallets, in that order, creating
// either if missing.
func ListAccounts(ctx context.Context, userID string) ([]models.WalletView, error) {
	live, err := GetOrCreateWallet(ctx, userID, models.AccountLive, "")
	if err != nil {
		return nil, err
	}
	demo, err := EnsureDemoWallet(ctx, userID)
	if err != nil {
		return nil, err
	}
	return []models.WalletView{live, demo}, nil
}

// DemoResetDelta is the balance change that brings a DEMO wallet back to the
// opening balance (positive = credit).
func DemoResetDelta(balance int64) int64 { return models.DemoOpeningBalance - balance }

// ResetDemoWallet puts the DEMO balance back to the opening balance through
// a ledger adjustment. Refused while DEMO trades are open, because their
// stakes are locked against the balance being reset.
func ResetDemoWallet(ctx context.Context, userID string) (models.WalletView, error) {
	uid, err := parseUserID(userID)
	if err != nil {
		return models.WalletView{}, err
	}
	wallet, err := EnsureDemoWallet(ctx, userID)
	if err != nil {
		return models.WalletView{}, err
	}
	open, err := mongox.C(models.CollBinaryTrades).CountDocuments(ctx, bson.M{
		"userId":      uid,
		"accountType": models.AccountDemo,
		"status":      bson.M{"$in": bson.A{string(models.TradeStatusOpen), string(models.TradeStatusSettling)}},
	})
	if err != nil {
		return models.WalletView{}, err
	}
	if open > 0 || wallet.LockedBalance > 0 {
		return models.WalletView{}, apperr.ConflictErr(apperr.Conflict, "Close your open demo trades first")
	}

	delta := DemoResetDelta(wallet.Balance)
	if delta == 0 {
		return wallet, nil
	}
	in := AdjustmentInput{
		UserID:        userID,
		AccountType:   models.AccountDemo,
		ReferenceType: refDemoAccount,
		ReferenceID:   "demo-reset",
		// Every reset is its own operation; the nonce keeps a later reset from
		// replaying an earlier one.
		IdempotencyKey: "demo:reset:" + userID + ":" + strconv.FormatInt(time.Now().UnixMilli(), 10) + ":" + uuid.NewString(),
		Description:    "Demo account reset",
	}
	var res MutationResult
	if delta > 0 {
		in.Amount, in.Type = delta, models.WalletTxAdjustmentCredit
		res, err = CreditWallet(ctx, in)
	} else {
		in.Amount, in.Type = -delta, models.WalletTxAdjustmentDebit
		res, err = DebitWallet(ctx, in)
	}
	if err != nil {
		return models.WalletView{}, err
	}
	return res.Wallet, nil
}
