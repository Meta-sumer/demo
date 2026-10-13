package usdt

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/wallets"
)

// Transfers to the trading account move USDT from the FUNDING wallet to the
// customer's LIVE USDT wallet - same currency, no conversion. The trading
// engine trades INR only today, so this is off unless
// USDT_TRANSFER_TO_TRADING_ENABLED=true; the LIVE USDT wallet is never part of
// the trading account list.

// TransferResult is POST /wallet/usdt/transfer-to-trading.
type TransferResult struct {
	Transfer models.UsdtTransferView `json:"transfer"`
	Wallet   WalletView              `json:"wallet"`
	Trading  models.WalletView       `json:"trading"`
}

// TransferToTrading moves amount (micro-USDT) in one transaction: a debit
// row on the FUNDING wallet and a credit row on the LIVE USDT wallet, both
// referencing the transfer. The idempotency key makes a retry a replay.
func TransferToTrading(ctx context.Context, userID string, amount int64, idempotencyKey string) (TransferResult, bool, error) {
	if err := requireEnabled(); err != nil {
		return TransferResult{}, false, err
	}
	if !TransferToTradingEnabled() {
		return TransferResult{}, false, apperr.ConflictErr(apperr.UsdtTransferDisabled, "Transfers to the trading account are not available yet")
	}
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return TransferResult{}, false, apperr.Unauth()
	}
	if amount <= 0 {
		return TransferResult{}, false, apperr.New(apperr.InvalidAmount, "Transfer amount must be positive", 400)
	}
	ctx = context.WithoutCancel(ctx)
	coll := mongox.C(models.CollUsdtTransfers)
	hash := requestHash("transfer", itoa(amount))

	var existing models.UsdtTransfer
	err := coll.FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": idempotencyKey}).Decode(&existing)
	if err == nil {
		if existing.RequestHash != hash {
			return TransferResult{}, false, payloadMismatch()
		}
		res, err := transferResult(ctx, userID, &existing)
		return res, false, err
	}
	if !mongox.IsNotFound(err) {
		return TransferResult{}, false, err
	}

	if _, err := fundingWallet(ctx, userID); err != nil {
		return TransferResult{}, false, err
	}
	if _, err := wallets.GetOrCreateWallet(ctx, userID, models.AccountLive, models.CurrencyUSDT); err != nil {
		return TransferResult{}, false, err
	}
	id := bson.NewObjectID()
	doc := models.UsdtTransfer{
		ID: id, UserID: uid, IdempotencyKey: idempotencyKey, RequestHash: hash, Amount: amount,
		From: models.UsdtTransferFrom, To: models.UsdtTransferTo, CreatedAt: mongox.Now(),
	}
	// The ledger key derives from the customer's key, so two concurrent
	// requests with one key collide on the ledger (the wallet locks serialise
	// them) and the second replays the first.
	moved, err := wallets.TransferBetweenAccounts(ctx, wallets.AccountTransferInput{
		UserID: userID, Currency: models.CurrencyUSDT,
		FromAccount: models.AccountFunding, ToAccount: models.AccountLive,
		Amount: amount, Type: models.WalletTxTransfer,
		ReferenceType: RefTransfer, ReferenceID: id.Hex(),
		IdempotencyKey: "usdt:transfer:" + uid.Hex() + ":" + requestHash(idempotencyKey)[:32],
		Description:    "USDT wallet to trading account",
		Metadata:       map[string]any{"from": models.UsdtTransferFrom, "to": models.UsdtTransferTo},
		InTx: func(tctx context.Context, fromTx, toTx string) error {
			if oid, ok := mongox.ParseID(fromTx); ok {
				doc.FromTransactionID = &oid
			}
			if oid, ok := mongox.ParseID(toTx); ok {
				doc.ToTransactionID = &oid
			}
			_, err := coll.InsertOne(tctx, doc)
			return err
		},
	})
	if err != nil {
		if mongox.IsDuplicateKey(err) {
			// A concurrent request with the same key committed first.
			if ferr := coll.FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": idempotencyKey}).Decode(&existing); ferr == nil {
				if existing.RequestHash != hash {
					return TransferResult{}, false, payloadMismatch()
				}
				res, rerr := transferResult(ctx, userID, &existing)
				return res, false, rerr
			}
		}
		return TransferResult{}, false, err
	}
	if moved.From.Replayed {
		if ferr := coll.FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": idempotencyKey}).Decode(&existing); ferr != nil {
			return TransferResult{}, false, ferr
		}
		if existing.RequestHash != hash {
			return TransferResult{}, false, payloadMismatch()
		}
		res, rerr := transferResult(ctx, userID, &existing)
		return res, false, rerr
	}
	logx.Info("USDT transferred to the trading account", "transferId", id.Hex(), "userId", userID, "amount", micro.Format(amount))
	emitUpdated(userID, KindTransfer, id.Hex(), "completed")
	res, err := transferResult(ctx, userID, &doc)
	return res, true, err
}

func transferResult(ctx context.Context, userID string, t *models.UsdtTransfer) (TransferResult, error) {
	wallet, err := GetWallet(ctx, userID)
	if err != nil {
		return TransferResult{}, err
	}
	trading, err := wallets.GetOrCreateWallet(ctx, userID, models.AccountLive, models.CurrencyUSDT)
	if err != nil {
		return TransferResult{}, err
	}
	return TransferResult{Transfer: models.ToUsdtTransferView(t), Wallet: wallet, Trading: trading}, nil
}
