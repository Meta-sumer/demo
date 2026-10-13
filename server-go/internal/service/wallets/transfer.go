package wallets

import (
	"context"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
)

// AccountTransferInput moves money between two of one customer's own
// accounts in the same currency (no conversion).
type AccountTransferInput struct {
	UserID      string
	Currency    string
	FromAccount string
	ToAccount   string
	Amount      int64
	Type        models.WalletTransactionType
	// Both ledger rows carry this reference; their idempotency keys are
	// IdempotencyKey+":out" and IdempotencyKey+":in".
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
	Description    string
	Network        string
	Metadata       map[string]any
	// InTx runs inside the transaction once both rows are written.
	InTx func(ctx context.Context, fromTransactionID, toTransactionID string) error
}

// TransferResult is both sides of a transfer.
type TransferResult struct {
	From MutationResult
	To   MutationResult
}

// TransferBetweenAccounts debits one account and credits the other in a
// single transaction: either both ledger rows exist or neither does. Both
// wallets' locks are held (in a fixed order, so two opposite transfers cannot
// deadlock), and the idempotency key makes a repeat a replay.
func TransferBetweenAccounts(ctx context.Context, in AccountTransferInput) (TransferResult, error) {
	if in.Amount <= 0 {
		return TransferResult{}, apperr.New(apperr.InvalidAmount, "Transfer amount must be positive", 400)
	}
	from, to := models.NormAccountType(in.FromAccount), models.NormAccountType(in.ToAccount)
	if from == to {
		return TransferResult{}, apperr.New(apperr.ValidationError, "Transfer source and destination must differ", 400)
	}
	if in.IdempotencyKey == "" {
		return TransferResult{}, apperr.New(apperr.ValidationError, "A transfer needs an idempotency key", 400)
	}
	currency := normCurrency(in.Currency)
	ctx = context.WithoutCancel(ctx)

	base := func(account string, suffix string) mutationInput {
		return mutationInput{
			UserID:         in.UserID,
			AccountType:    account,
			Currency:       currency,
			Type:           in.Type,
			Amount:         in.Amount,
			ReferenceType:  models.StrOrNil(in.ReferenceType),
			ReferenceID:    models.StrOrNil(in.ReferenceID),
			IdempotencyKey: models.Str(in.IdempotencyKey + suffix),
			Description:    models.StrOrNil(in.Description),
			Metadata:       in.Metadata,
			Network:        models.StrOrNil(in.Network),
		}
	}
	out := base(from, ":out")
	out.BalanceDelta, out.Movement, out.RequireAvailable = -in.Amount, models.MovementDebit, i64(in.Amount)
	inc := base(to, ":in")
	inc.BalanceDelta, inc.Movement = in.Amount, models.MovementCredit

	keys := []string{walletLockKey(in.UserID, from, currency), walletLockKey(in.UserID, to, currency)}
	sort.Strings(keys)

	var result TransferResult
	err := redisx.WithLock(ctx, keys[0], walletLockTTL, walletLockWait, func() error {
		return redisx.WithLock(ctx, keys[1], walletLockTTL, walletLockWait, func() error {
			var err error
			result, err = performTransfer(ctx, out, inc, currency, in.InTx)
			return err
		})
	})
	if err != nil {
		if redisx.IsLockTimeout(err) {
			logx.Warn("wallet lock wait exceeded", "userId", in.UserID, "type", string(in.Type))
			return TransferResult{}, apperr.Unavailable(apperr.ServiceUnavailable,
				"Your account is busy processing another operation. Please try again.")
		}
		return TransferResult{}, err
	}
	return result, nil
}

func performTransfer(ctx context.Context, out, inc mutationInput, currency string,
	inTx func(ctx context.Context, fromTxID, toTxID string) error) (TransferResult, error) {
	replayBoth := func() (TransferResult, bool, error) {
		o, found, err := replay(ctx, out, currency)
		if err != nil || !found {
			return TransferResult{}, found, err
		}
		i, found, err := replay(ctx, inc, currency)
		if err != nil || !found {
			return TransferResult{}, found, err
		}
		return TransferResult{From: o, To: i}, true, nil
	}
	if res, found, err := replayBoth(); err != nil || found {
		return res, err
	}

	var result TransferResult
	err := mongox.WithTransaction(ctx, func(tctx context.Context) error {
		o, err := mutateInTx(tctx, out, currency)
		if err != nil {
			return err
		}
		i, err := mutateInTx(tctx, inc, currency)
		if err != nil {
			return err
		}
		result = TransferResult{From: o, To: i}
		if inTx != nil {
			return inTx(tctx, o.TransactionID, i.TransactionID)
		}
		return nil
	})
	if err != nil {
		if !mongox.IsDuplicateKey(err) {
			return TransferResult{}, err
		}
		res, found, rerr := replayBoth()
		if rerr != nil {
			return TransferResult{}, rerr
		}
		if !found {
			return TransferResult{}, err
		}
		result = res
	}
	ws.Emit([]string{config.ChUser(out.UserID)}, config.EvWalletUpdated, result.From.Wallet)
	ws.Emit([]string{config.ChUser(out.UserID)}, config.EvWalletUpdated, result.To.Wallet)
	return result, nil
}

// LedgerRowsFor returns the ledger rows of one referenced operation, oldest
// first (e.g. every row of a USDT deposit or withdrawal).
func LedgerRowsFor(ctx context.Context, referenceType, referenceID string) ([]models.WalletTransactionLean, error) {
	cur, err := mongox.C(models.CollWalletTransactions).Find(ctx,
		bson.M{"referenceType": referenceType, "referenceId": referenceID},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rows []models.WalletTransaction
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]models.WalletTransactionLean, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToWalletTransactionLean(&rows[i]))
	}
	return out, nil
}
