// Package wallets is the wallet service (spec section 9).
//
// Every balance change goes through applyMutation, which is:
//   - guarded    - the conditions (sufficient available funds, sufficient
//     locked funds) are part of the update filter, so two concurrent requests
//     cannot both pass the check;
//   - locked     - a Redis lock serialises mutations of one wallet across
//     server instances;
//   - atomic     - the balance update and its ledger row commit together when
//     the deployment has transactions;
//   - idempotent - the ledger's unique index on idempotencyKey makes a repeated
//     operation a no-op rather than a second credit.
//
// No other package writes to `wallets` directly.
package wallets

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
)

// MutationResult is what every balance change returns.
type MutationResult struct {
	Wallet models.WalletView `json:"wallet"`
	// Ledger row id ("" never happens in practice; TS types it string | null).
	TransactionID string `json:"transactionId"`
	// True when the idempotency key had already been applied.
	Replayed bool `json:"replayed"`
}

// mutationInput is the single description of a balance change.
type mutationInput struct {
	UserID string
	// LIVE or DEMO; "" means LIVE.
	AccountType  string
	Currency     string
	BalanceDelta int64
	LockedDelta  int64
	// Minimum available balance required before the change (nil = no guard).
	RequireAvailable *int64
	// Minimum locked balance required before the change (nil = no guard).
	RequireLocked *int64
	Type          models.WalletTransactionType
	Movement      models.MoneyMovement
	// Positive magnitude recorded on the ledger row.
	Amount         int64
	ReferenceType  *string
	ReferenceID    *string
	IdempotencyKey *string
	Description    *string
	Metadata       map[string]any
	// USDT ledger rows: the network and the provider fee (nil elsewhere).
	Network *string
	Fee     *int64
	// InTx runs inside the mutation's transaction, after the ledger row is
	// written; returning an error rolls the balance change back. It does not
	// run when the idempotency key had already been applied (Replayed).
	InTx func(ctx context.Context, transactionID string) error
	// Skip the Redis lock when the caller already holds one.
	ExternallyLocked bool
}

func i64(v int64) *int64 { return &v }

func normCurrency(c string) string {
	if c == "" {
		return models.WalletDefaultCurrency
	}
	// Mongoose applies the schema's `uppercase` setter to writes and queries.
	return strings.ToUpper(c)
}

// walletLockKey serialises mutations of one wallet. The LIVE key is the one
// used before accounts existed.
func walletLockKey(userID, accountType, currency string) string {
	if acct := models.NormAccountType(accountType); acct != models.AccountLive {
		return "wallet:" + userID + ":" + acct + ":" + currency
	}
	return "wallet:" + userID + ":" + currency
}

func parseUserID(userID string) (bson.ObjectID, error) {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return bson.NilObjectID, fmt.Errorf("invalid user id %q: %w", userID, err)
	}
	return id, nil
}

// GetOrCreateWallet returns the user's wallet of that account, creating an
// empty one on first use. accountType "" means LIVE, currency "" means INR.
// A DEMO wallet should be obtained through EnsureDemoWallet, which also funds it.
func GetOrCreateWallet(ctx context.Context, userID, accountType, currency string) (models.WalletView, error) {
	uid, err := parseUserID(userID)
	if err != nil {
		return models.WalletView{}, err
	}
	accountType = models.NormAccountType(accountType)
	currency = normCurrency(currency)
	now := mongox.Now()
	var w models.Wallet
	err = mongox.C(models.CollWallets).FindOneAndUpdate(ctx,
		bson.M{"userId": uid, "accountType": accountType, "currency": currency},
		bson.M{
			"$setOnInsert": bson.M{"balance": int64(0), "lockedBalance": int64(0), "version": int64(0), "createdAt": now},
			"$set":         bson.M{"updatedAt": now},
		},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&w)
	if err != nil {
		return models.WalletView{}, err
	}
	return models.ToWalletView(&w), nil
}

// GetWallet returns the wallet or WALLET_NOT_FOUND. accountType "" means LIVE,
// currency "" means INR.
func GetWallet(ctx context.Context, userID, accountType, currency string) (models.WalletView, error) {
	uid, err := parseUserID(userID)
	if err != nil {
		return models.WalletView{}, err
	}
	var w models.Wallet
	err = mongox.C(models.CollWallets).FindOne(ctx, bson.M{
		"userId": uid, "accountType": models.NormAccountType(accountType), "currency": normCurrency(currency),
	}).Decode(&w)
	if err != nil {
		if mongox.IsNotFound(err) {
			return models.WalletView{}, apperr.NotFoundErr(apperr.WalletNotFound, "Wallet not found")
		}
		return models.WalletView{}, err
	}
	return models.ToWalletView(&w), nil
}

const (
	// How long a wallet operation may wait for the account lock. Operations on
	// one wallet are serialised, so a burst - a customer opening many positions
	// at once - queues behind itself; the budget has to cover a realistic burst.
	walletLockWait = 15 * time.Second
	// Long enough for the slowest mutation, short enough that a crash frees it.
	walletLockTTL = 10 * time.Second
)

// applyMutation is the one place a wallet balance is allowed to change.
func applyMutation(ctx context.Context, in mutationInput) (MutationResult, error) {
	currency := normCurrency(in.Currency)
	in.AccountType = models.NormAccountType(in.AccountType)
	// A money movement must not be abandoned half way because the HTTP client
	// went away: Node never cancels an in-flight write, so neither do we.
	ctx = context.WithoutCancel(ctx)

	if in.ExternallyLocked {
		return performMutation(ctx, in, currency)
	}

	var result MutationResult
	err := redisx.WithLock(ctx, walletLockKey(in.UserID, in.AccountType, currency), walletLockTTL, walletLockWait, func() error {
		var err error
		result, err = performMutation(ctx, in, currency)
		return err
	})
	if err != nil {
		if redisx.IsLockTimeout(err) {
			// A queueing problem, not an accounting one: tell the caller to retry
			// rather than leaking an internal lock name.
			logx.Warn("wallet lock wait exceeded", "userId", in.UserID, "type", string(in.Type))
			return MutationResult{}, apperr.Unavailable(apperr.ServiceUnavailable,
				"Your account is busy processing another operation. Please try again.")
		}
		return MutationResult{}, err
	}
	return result, nil
}

// replay returns the result of an already applied operation.
func replay(ctx context.Context, in mutationInput, currency string) (MutationResult, bool, error) {
	var existing struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := mongox.C(models.CollWalletTransactions).FindOne(ctx, bson.M{"idempotencyKey": *in.IdempotencyKey}).Decode(&existing)
	if err != nil {
		if mongox.IsNotFound(err) {
			return MutationResult{}, false, nil
		}
		return MutationResult{}, false, err
	}
	wallet, err := GetWallet(ctx, in.UserID, in.AccountType, currency)
	if err != nil {
		return MutationResult{}, false, err
	}
	return MutationResult{Wallet: wallet, TransactionID: existing.ID.Hex(), Replayed: true}, true, nil
}

func performMutation(ctx context.Context, in mutationInput, currency string) (MutationResult, error) {
	// A replay of an already applied operation must not move money again.
	if in.IdempotencyKey != nil {
		res, found, err := replay(ctx, in, currency)
		if err != nil {
			return MutationResult{}, err
		}
		if found {
			return res, nil
		}
	}

	var result MutationResult
	err := mongox.WithTransaction(ctx, func(tctx context.Context) error {
		var err error
		result, err = mutateInTx(tctx, in, currency)
		if err != nil {
			return err
		}
		if in.InTx != nil {
			return in.InTx(tctx, result.TransactionID)
		}
		return nil
	})
	if err != nil {
		// Two racing callers with the same idempotency key: the loser reads back
		// the winner's row instead of applying a second change.
		if !(mongox.IsDuplicateKey(err) && in.IdempotencyKey != nil) {
			return MutationResult{}, err
		}
		res, found, rerr := replay(ctx, in, currency)
		if rerr != nil {
			return MutationResult{}, rerr
		}
		if !found {
			return MutationResult{}, err
		}
		result = res
	}

	ws.Emit([]string{config.ChUser(in.UserID)}, config.EvWalletUpdated, result.Wallet)
	return result, nil
}

// mutateInTx applies one guarded change and writes its ledger row, inside the
// caller's transaction.
func mutateInTx(tctx context.Context, in mutationInput, currency string) (MutationResult, error) {
	before, err := guardedUpdate(tctx, in, currency)
	if err != nil {
		return MutationResult{}, err
	}
	balanceAfter := before.Balance + in.BalanceDelta
	lockedAfter := before.LockedBalance + in.LockedDelta

	amount := in.Amount
	if amount < 0 {
		amount = -amount
	}
	metadata := in.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	row := models.WalletTransaction{
		WalletID:       before.ID,
		UserID:         before.UserID,
		AccountType:    in.AccountType,
		Type:           in.Type,
		Movement:       in.Movement,
		Amount:         amount,
		Currency:       currency,
		BalanceBefore:  before.Balance,
		BalanceAfter:   balanceAfter,
		LockedBefore:   before.LockedBalance,
		LockedAfter:    lockedAfter,
		ReferenceType:  in.ReferenceType,
		ReferenceID:    in.ReferenceID,
		IdempotencyKey: in.IdempotencyKey,
		Description:    in.Description,
		Metadata:       metadata,
		Network:        in.Network,
		Fee:            in.Fee,
		CreatedAt:      mongox.Now(),
	}
	res, err := mongox.C(models.CollWalletTransactions).InsertOne(tctx, row)
	if err != nil {
		return MutationResult{}, err
	}
	txID, _ := res.InsertedID.(bson.ObjectID)

	return MutationResult{
		Wallet: models.WalletView{
			ID:               before.ID.Hex(),
			UserID:           before.UserID.Hex(),
			AccountType:      in.AccountType,
			Currency:         currency,
			Balance:          balanceAfter,
			LockedBalance:    lockedAfter,
			AvailableBalance: balanceAfter - lockedAfter,
			UpdatedAt:        iso.From(time.Now()),
		},
		TransactionID: txID.Hex(),
		Replayed:      false,
	}, nil
}

// guardFilter builds the wallet filter with the pre-conditions in $expr.
func guardFilter(uid bson.ObjectID, currency string, in mutationInput) bson.M {
	guards := bson.A{}
	if in.RequireAvailable != nil {
		guards = append(guards, bson.M{"$gte": bson.A{bson.M{"$subtract": bson.A{"$balance", "$lockedBalance"}}, *in.RequireAvailable}})
	}
	if in.RequireLocked != nil {
		guards = append(guards, bson.M{"$gte": bson.A{"$lockedBalance", *in.RequireLocked}})
	}
	if in.BalanceDelta < 0 {
		guards = append(guards, bson.M{"$gte": bson.A{"$balance", -in.BalanceDelta}})
	}
	if in.LockedDelta < 0 {
		guards = append(guards, bson.M{"$gte": bson.A{"$lockedBalance", -in.LockedDelta}})
	}
	filter := bson.M{"userId": uid, "accountType": models.NormAccountType(in.AccountType), "currency": currency}
	if len(guards) > 0 {
		filter["$expr"] = bson.M{"$and": guards}
	}
	return filter
}

// guardedUpdate applies the increments only if the pre-conditions still hold
// in the database, and returns the wallet as it was before.
func guardedUpdate(ctx context.Context, in mutationInput, currency string) (*models.Wallet, error) {
	uid, err := parseUserID(in.UserID)
	if err != nil {
		return nil, err
	}
	coll := mongox.C(models.CollWallets)
	var before models.Wallet
	err = coll.FindOneAndUpdate(ctx,
		guardFilter(uid, currency, in),
		bson.M{
			"$inc": bson.M{"balance": in.BalanceDelta, "lockedBalance": in.LockedDelta, "version": int64(1)},
			"$set": bson.M{"updatedAt": mongox.Now()},
		},
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&before)
	if err == nil {
		return &before, nil
	}
	if !mongox.IsNotFound(err) {
		return nil, err
	}

	err = coll.FindOne(ctx, bson.M{"userId": uid, "accountType": models.NormAccountType(in.AccountType), "currency": currency},
		options.FindOne().SetProjection(bson.M{"_id": 1})).Err()
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.NotFoundErr(apperr.WalletNotFound, "Wallet not found")
		}
		return nil, err
	}
	if in.RequireLocked != nil {
		return nil, apperr.New(apperr.LockedFundsMismatch, "Locked funds are lower than the amount being released", 409)
	}
	return nil, apperr.New(apperr.InsufficientBalance, "Insufficient available balance", 400)
}

// ── trade funds ─────────────────────────────────────────────────────────

// TradeFundsInput identifies a trade's stake. AccountType "" means LIVE,
// Currency "" means INR.
type TradeFundsInput struct {
	UserID           string
	AccountType      string
	Currency         string
	Stake            int64
	TradeID          string
	ExternallyLocked bool
}

// ReleaseReason is why a stake is released without a win or loss.
type ReleaseReason string

const (
	ReleaseDraw      ReleaseReason = "DRAW"
	ReleaseCancelled ReleaseReason = "CANCELLED"
)

const refBinaryTrade = "BinaryTrade"

func tradeLockInput(in TradeFundsInput) mutationInput {
	return mutationInput{
		UserID:           in.UserID,
		AccountType:      in.AccountType,
		Currency:         in.Currency,
		BalanceDelta:     0,
		LockedDelta:      in.Stake,
		RequireAvailable: i64(in.Stake),
		Type:             models.WalletTxTradeLock,
		Movement:         models.MovementLock,
		Amount:           in.Stake,
		ReferenceType:    models.Str(refBinaryTrade),
		ReferenceID:      models.Str(in.TradeID),
		IdempotencyKey:   models.Str("trade:lock:" + in.TradeID),
		Description:      models.Str("Stake reserved for trade " + in.TradeID),
		ExternallyLocked: in.ExternallyLocked,
	}
}

func tradeLossInput(in TradeFundsInput) mutationInput {
	return mutationInput{
		UserID:           in.UserID,
		AccountType:      in.AccountType,
		Currency:         in.Currency,
		BalanceDelta:     -in.Stake,
		LockedDelta:      -in.Stake,
		RequireLocked:    i64(in.Stake),
		Type:             models.WalletTxTradeLoss,
		Movement:         models.MovementDebit,
		Amount:           in.Stake,
		ReferenceType:    models.Str(refBinaryTrade),
		ReferenceID:      models.Str(in.TradeID),
		IdempotencyKey:   models.Str("trade:settle:" + in.TradeID),
		Description:      models.Str("Loss settled for trade " + in.TradeID),
		ExternallyLocked: in.ExternallyLocked,
	}
}

func tradeWinInput(in TradeFundsInput, profit int64) mutationInput {
	return mutationInput{
		UserID:           in.UserID,
		AccountType:      in.AccountType,
		Currency:         in.Currency,
		BalanceDelta:     profit,
		LockedDelta:      -in.Stake,
		RequireLocked:    i64(in.Stake),
		Type:             models.WalletTxTradeWin,
		Movement:         models.MovementCredit,
		Amount:           profit,
		ReferenceType:    models.Str(refBinaryTrade),
		ReferenceID:      models.Str(in.TradeID),
		IdempotencyKey:   models.Str("trade:settle:" + in.TradeID),
		Description:      models.Str("Win settled for trade " + in.TradeID),
		Metadata:         map[string]any{"stake": in.Stake, "profit": profit},
		ExternallyLocked: in.ExternallyLocked,
	}
}

func tradeReleaseInput(in TradeFundsInput, reason ReleaseReason) mutationInput {
	typ := models.WalletTxTradeCancel
	if reason == ReleaseDraw {
		typ = models.WalletTxTradeRefund
	}
	return mutationInput{
		UserID:           in.UserID,
		AccountType:      in.AccountType,
		Currency:         in.Currency,
		BalanceDelta:     0,
		LockedDelta:      -in.Stake,
		RequireLocked:    i64(in.Stake),
		Type:             typ,
		Movement:         models.MovementUnlock,
		Amount:           in.Stake,
		ReferenceType:    models.Str(refBinaryTrade),
		ReferenceID:      models.Str(in.TradeID),
		IdempotencyKey:   models.Str("trade:settle:" + in.TradeID),
		Description:      models.Str("Stake released for trade " + in.TradeID + " (" + string(reason) + ")"),
		ExternallyLocked: in.ExternallyLocked,
	}
}

func tradeEarlyCloseInput(in TradeFundsInput, value int64) mutationInput {
	difference := value - in.Stake
	movement := models.MovementCredit
	if difference < 0 {
		movement = models.MovementDebit
	}
	amount := difference
	if amount < 0 {
		amount = -amount
	}
	return mutationInput{
		UserID:           in.UserID,
		AccountType:      in.AccountType,
		Currency:         in.Currency,
		BalanceDelta:     difference,
		LockedDelta:      -in.Stake,
		RequireLocked:    i64(in.Stake),
		Type:             models.WalletTxTradeEarlyClose,
		Movement:         movement,
		Amount:           amount,
		ReferenceType:    models.Str(refBinaryTrade),
		ReferenceID:      models.Str(in.TradeID),
		IdempotencyKey:   models.Str("trade:settle:" + in.TradeID),
		Description:      models.Str("Position closed early for trade " + in.TradeID),
		Metadata:         map[string]any{"stake": in.Stake, "closeValue": value},
		ExternallyLocked: in.ExternallyLocked,
	}
}

// LockTradeFunds: trade opened, reserve the stake. Balance is untouched,
// available drops.
func LockTradeFunds(ctx context.Context, in TradeFundsInput) (MutationResult, error) {
	return applyMutation(ctx, tradeLockInput(in))
}

// SettleTradeLoss: the reserved stake leaves the wallet.
func SettleTradeLoss(ctx context.Context, in TradeFundsInput) (MutationResult, error) {
	return applyMutation(ctx, tradeLossInput(in))
}

// SettleTradeWin releases the stake and credits the profit.
func SettleTradeWin(ctx context.Context, in TradeFundsInput, profit int64) (MutationResult, error) {
	return applyMutation(ctx, tradeWinInput(in, profit))
}

// ReleaseTradeFunds: draw refund or cancellation - release the stake, balance
// unchanged.
func ReleaseTradeFunds(ctx context.Context, in TradeFundsInput, reason ReleaseReason) (MutationResult, error) {
	return applyMutation(ctx, tradeReleaseInput(in, reason))
}

// SettleTradeEarlyClose: the position is bought back at value.
//
// One guarded operation rather than a release followed by a credit, so the
// wallet is never briefly in a state where the stake is free but the buy-back
// has not landed. It shares the `trade:settle:` key with normal settlement, so
// a trade can be settled exactly once by either path.
func SettleTradeEarlyClose(ctx context.Context, in TradeFundsInput, value int64) (MutationResult, error) {
	return applyMutation(ctx, tradeEarlyCloseInput(in, value))
}

// ── adjustments & withdrawals ───────────────────────────────────────────

// AdjustmentInput describes a non-trade movement. Empty optional strings are
// stored as null; AccountType "" means LIVE, Currency "" means INR.
type AdjustmentInput struct {
	UserID         string
	AccountType    string
	Currency       string
	Amount         int64
	Type           models.WalletTransactionType
	ReferenceType  string
	ReferenceID    string
	IdempotencyKey string
	Description    string
	Metadata       map[string]any
	// USDT ledger rows: network code and provider fee; "" / nil elsewhere.
	Network string
	Fee     *int64
	// InTx runs inside the same transaction as the balance change (see
	// mutationInput.InTx).
	InTx func(ctx context.Context, transactionID string) error
}

func (a AdjustmentInput) base() mutationInput {
	return mutationInput{
		UserID:         a.UserID,
		AccountType:    a.AccountType,
		Currency:       a.Currency,
		Type:           a.Type,
		Amount:         a.Amount,
		ReferenceType:  models.StrOrNil(a.ReferenceType),
		ReferenceID:    models.StrOrNil(a.ReferenceID),
		IdempotencyKey: models.StrOrNil(a.IdempotencyKey),
		Description:    models.StrOrNil(a.Description),
		Metadata:       a.Metadata,
		Network:        models.StrOrNil(a.Network),
		Fee:            a.Fee,
		InTx:           a.InTx,
	}
}

func creditInput(a AdjustmentInput) mutationInput {
	m := a.base()
	m.BalanceDelta, m.LockedDelta, m.Movement = a.Amount, 0, models.MovementCredit
	return m
}

func debitInput(a AdjustmentInput) mutationInput {
	m := a.base()
	m.BalanceDelta, m.LockedDelta, m.Movement = -a.Amount, 0, models.MovementDebit
	m.RequireAvailable = i64(a.Amount)
	return m
}

func withdrawalLockInput(a AdjustmentInput) mutationInput {
	m := a.base()
	m.BalanceDelta, m.LockedDelta, m.Movement = 0, a.Amount, models.MovementLock
	m.RequireAvailable = i64(a.Amount)
	return m
}

func withdrawalUnlockInput(a AdjustmentInput) mutationInput {
	m := a.base()
	m.BalanceDelta, m.LockedDelta, m.Movement = 0, -a.Amount, models.MovementUnlock
	m.RequireLocked = i64(a.Amount)
	return m
}

func withdrawalSettleInput(a AdjustmentInput) mutationInput {
	m := a.base()
	m.BalanceDelta, m.LockedDelta, m.Movement = -a.Amount, -a.Amount, models.MovementDebit
	m.RequireLocked = i64(a.Amount)
	return m
}

// CreditWallet adds funds (deposit approval, admin credit).
func CreditWallet(ctx context.Context, in AdjustmentInput) (MutationResult, error) {
	if in.Amount <= 0 {
		return MutationResult{}, apperr.New(apperr.InvalidAmount, "Credit amount must be positive", 400)
	}
	return applyMutation(ctx, creditInput(in))
}

// DebitWallet removes available funds (admin debit).
func DebitWallet(ctx context.Context, in AdjustmentInput) (MutationResult, error) {
	if in.Amount <= 0 {
		return MutationResult{}, apperr.New(apperr.InvalidAmount, "Debit amount must be positive", 400)
	}
	return applyMutation(ctx, debitInput(in))
}

// LockWithdrawalFunds reserves funds for a pending withdrawal without
// removing them yet.
func LockWithdrawalFunds(ctx context.Context, in AdjustmentInput) (MutationResult, error) {
	return applyMutation(ctx, withdrawalLockInput(in))
}

// UnlockWithdrawalFunds releases a withdrawal reservation (rejected request).
func UnlockWithdrawalFunds(ctx context.Context, in AdjustmentInput) (MutationResult, error) {
	return applyMutation(ctx, withdrawalUnlockInput(in))
}

// SettleWithdrawal completes a withdrawal: the reserved funds finally leave
// the wallet.
func SettleWithdrawal(ctx context.Context, in AdjustmentInput) (MutationResult, error) {
	return applyMutation(ctx, withdrawalSettleInput(in))
}

// ── ledger ──────────────────────────────────────────────────────────────

// LedgerQuery filters a user's ledger.
type LedgerQuery struct {
	UserID string
	// "" lists every trading account (LIVE and DEMO); LIVE also matches rows
	// written before accounts existed. FUNDING rows are listed only on request.
	AccountType string
	// "" = the trading currency (INR), or USDT for the FUNDING account. Rows
	// of different currencies are never listed together.
	Currency string
	Type     *models.WalletTransactionType
	From     *time.Time
	To       *time.Time
	Page     int64
	Limit    int64
}

// ListWalletTransactions returns ledger rows newest first, as lean documents.
func ListWalletTransactions(ctx context.Context, q LedgerQuery) ([]models.WalletTransactionLean, int64, error) {
	uid, err := parseUserID(q.UserID)
	if err != nil {
		return nil, 0, err
	}
	filter := bson.M{"userId": uid}
	if q.AccountType != "" {
		filter["accountType"] = models.AccountFilter(q.AccountType)
	} else {
		filter["accountType"] = bson.M{"$ne": models.AccountFunding}
	}
	switch {
	case q.Currency != "":
		filter["currency"] = normCurrency(q.Currency)
	case models.NormAccountType(q.AccountType) == models.AccountFunding:
		filter["currency"] = models.CurrencyUSDT
	default:
		filter["currency"] = models.INRCurrency()
	}
	if q.Type != nil {
		filter["type"] = *q.Type
	}
	if q.From != nil || q.To != nil {
		rng := bson.M{}
		if q.From != nil {
			rng["$gte"] = *q.From
		}
		if q.To != nil {
			rng["$lte"] = *q.To
		}
		filter["createdAt"] = rng
	}
	coll := mongox.C(models.CollWalletTransactions)
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip((q.Page-1)*q.Limit).
		SetLimit(q.Limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.WalletTransaction
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	items := make([]models.WalletTransactionLean, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToWalletTransactionLean(&rows[i]))
	}
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
