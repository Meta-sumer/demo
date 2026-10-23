// Package reconciliation compares the wallet, trade, settlement and hedge
// ledgers against each other (spec section 42).
//
// A mismatch produces a RECONCILIATION_ALERT for an operator to investigate -
// nothing here ever "fixes" a balance, because a silent correction would hide
// the bug that caused the drift.
package reconciliation

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/audit"
)

// DefaultWindowMs is the default platform-result window (24h).
const DefaultWindowMs int64 = 24 * 60 * 60 * 1000

var settledTradeStatuses = bson.A{
	models.TradeStatusWon, models.TradeStatusLost, models.TradeStatusDraw, models.TradeStatusClosedEarly,
}

// CheckView is a check as the API returns it.
type CheckView struct {
	Name       string         `json:"name"`
	Passed     bool           `json:"passed"`
	Expected   int64          `json:"expected"`
	Actual     int64          `json:"actual"`
	Difference int64          `json:"difference"`
	Details    map[string]any `json:"details"`
}

type ReconciliationResult struct {
	Reference  string                      `json:"reference"`
	Status     models.ReconciliationStatus `json:"status"`
	Checks     []CheckView                 `json:"checks"`
	Alerts     []string                    `json:"alerts"`
	DurationMs int64                       `json:"durationMs"`
	WindowFrom iso.Time                    `json:"windowFrom"`
	WindowTo   iso.Time                    `json:"windowTo"`
}

// RunReconciliation runs every check; windowMs <= 0 means DefaultWindowMs.
func RunReconciliation(ctx context.Context, windowMs int64) (*ReconciliationResult, error) {
	if windowMs <= 0 {
		windowMs = DefaultWindowMs
	}
	startedAt := time.Now()
	windowTo := mongox.Now()
	windowFrom := time.UnixMilli(startedAt.UnixMilli() - windowMs).UTC()
	checks := []models.ReconciliationCheck{}

	steps := []func(context.Context) (models.ReconciliationCheck, error){
		checkWalletLedgerAgainstBalances,
		checkLockedFundsAgainstOpenPositions,
		checkSettledTradesHaveSettlements,
		checkSettlementsHaveWalletTransactions,
		checkCancelledTradesReleased,
		checkStakeLocksHaveOpenTrades,
		func(ctx context.Context) (models.ReconciliationCheck, error) {
			return checkPlatformResultConsistency(ctx, windowFrom)
		},
		checkHedgeOrderIntegrity,
		checkTransfersAgainstLedger,
		checkStuckApprovedTransfers,
		checkUsdtWalletLedgerAgainstBalances,
		checkUsdtLockedAgainstOpenWithdrawals,
		checkUsdtPaidDepositsCredited,
		checkUsdtCreditedDepositsLedger,
		checkUsdtCompletedWithdrawalsSettled,
		checkUsdtProviderAlerts,
		checkStuckSettlements,
	}

	status := models.ReconciliationOK
	alerts := []string{}
	for _, step := range steps {
		c, err := step(ctx)
		if err != nil {
			logx.Error("reconciliation run failed", "err", err.Error())
			status = models.ReconciliationFailed
			alerts = []string{"Reconciliation aborted: " + errMessage(err)}
			break
		}
		checks = append(checks, c)
	}

	if status != models.ReconciliationFailed {
		for _, c := range checks {
			if !c.Passed {
				alerts = append(alerts, c.Name+": expected "+itoa(c.Expected)+", found "+itoa(c.Actual))
			}
		}
		if len(alerts) > 0 {
			status = models.ReconciliationAlert
		}
	}

	run := &models.ReconciliationRun{
		Reference:  util.Reference("REC"),
		WindowFrom: windowFrom,
		WindowTo:   windowTo,
		Status:     status,
		Checks:     checks,
		Alerts:     alerts,
		DurationMs: time.Since(startedAt).Milliseconds(),
		CreatedAt:  mongox.Now(),
	}
	if _, err := mongox.C(models.CollReconciliationRuns).InsertOne(ctx, run); err != nil {
		return nil, err
	}

	if status == models.ReconciliationAlert {
		audit.Log(audit.Entry{
			ActorRole:  "SYSTEM",
			Action:     audit.ReconciliationAlert,
			Resource:   "ReconciliationRun",
			ResourceID: run.Reference,
			After:      map[string]any{"alerts": alerts},
			Failed:     true,
			Reason:     strings.Join(alerts, "; "),
		})
	}

	views := make([]CheckView, len(checks))
	for i, c := range checks {
		views[i] = CheckView{Name: c.Name, Passed: c.Passed, Expected: c.Expected, Actual: c.Actual, Difference: c.Difference, Details: c.Details}
	}
	return &ReconciliationResult{
		Reference:  run.Reference,
		Status:     status,
		Checks:     views,
		Alerts:     alerts,
		DurationMs: run.DurationMs,
		WindowFrom: iso.From(windowFrom),
		WindowTo:   iso.From(windowTo),
	}, nil
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func capped(ms []map[string]any) []map[string]any {
	if len(ms) > 20 {
		return ms[:20]
	}
	return ms
}

// checkWalletLedgerAgainstBalances: every wallet's balance must equal the sum
// of the movements recorded against it. The ledger is the authority; the
// wallet row is a running total of it. Trading-currency (INR) wallets only:
// the totals are sums of money, which must not mix currencies; the USDT
// wallets have their own check.
func checkWalletLedgerAgainstBalances(ctx context.Context) (models.ReconciliationCheck, error) {
	return ledgerAgainstBalances(ctx, "wallet_ledger_vs_balance", models.INRCurrency())
}

func ledgerAgainstBalances(ctx context.Context, name string, currency any) (models.ReconciliationCheck, error) {
	cur, err := mongox.C(models.CollWalletTransactions).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"currency": currency}},
		bson.M{"$group": bson.M{
			"_id":          "$walletId",
			"balanceDelta": bson.M{"$sum": bson.M{"$subtract": bson.A{"$balanceAfter", "$balanceBefore"}}},
			"lockedDelta":  bson.M{"$sum": bson.M{"$subtract": bson.A{"$lockedAfter", "$lockedBefore"}}},
		}},
	})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	type entry struct {
		ID           bson.ObjectID `bson:"_id"`
		BalanceDelta int64         `bson:"balanceDelta"`
		LockedDelta  int64         `bson:"lockedDelta"`
	}
	var rows []entry
	if err := cur.All(ctx, &rows); err != nil {
		return models.ReconciliationCheck{}, err
	}
	ledger := make(map[string]entry, len(rows))
	for _, r := range rows {
		ledger[r.ID.Hex()] = r
	}

	wcur, err := mongox.C(models.CollWallets).Find(ctx, bson.M{"currency": currency},
		options.Find().SetProjection(bson.M{"balance": 1, "lockedBalance": 1}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var wallets []struct {
		ID            bson.ObjectID `bson:"_id"`
		Balance       int64         `bson:"balance"`
		LockedBalance int64         `bson:"lockedBalance"`
	}
	if err := wcur.All(ctx, &wallets); err != nil {
		return models.ReconciliationCheck{}, err
	}

	mismatches := []map[string]any{}
	var expectedTotal, actualTotal int64
	for _, w := range wallets {
		walletID := w.ID.Hex()
		e := ledger[walletID]
		expectedTotal += e.BalanceDelta
		actualTotal += w.Balance
		if e.BalanceDelta != w.Balance || e.LockedDelta != w.LockedBalance {
			mismatches = append(mismatches, map[string]any{
				"walletId":      walletID,
				"ledgerBalance": e.BalanceDelta,
				"walletBalance": w.Balance,
				"ledgerLocked":  e.LockedDelta,
				"walletLocked":  w.LockedBalance,
			})
		}
	}

	return models.ReconciliationCheck{
		Name:       name,
		Passed:     len(mismatches) == 0,
		Expected:   expectedTotal,
		Actual:     actualTotal,
		Difference: actualTotal - expectedTotal,
		Details: map[string]any{
			"wallets":       len(wallets),
			"mismatches":    capped(mismatches),
			"mismatchCount": len(mismatches),
		},
	}, nil
}

type userSum struct {
	ID struct {
		UserID      bson.ObjectID `bson:"userId"`
		AccountType *string       `bson:"accountType"`
	} `bson:"_id"`
	Sum int64 `bson:"sum"`
}

// key identifies one wallet: a user's LIVE or DEMO account.
func (u userSum) key() string {
	acct := ""
	if u.ID.AccountType != nil {
		acct = *u.ID.AccountType
	}
	return walletKey(u.ID.UserID, acct)
}

func walletKey(userID bson.ObjectID, accountType string) string {
	return userID.Hex() + ":" + models.NormAccountType(accountType)
}

// sumByUser sums field per (user, account). Documents without an accountType
// (trades before accounts existed, transfers) belong to LIVE.
func sumByUser(ctx context.Context, coll string, match bson.M, field string) ([]userSum, error) {
	cur, err := mongox.C(coll).Aggregate(ctx, bson.A{
		bson.M{"$match": match},
		bson.M{"$group": bson.M{
			"_id": bson.M{"userId": "$userId", "accountType": "$accountType"},
			"sum": bson.M{"$sum": "$" + field},
		}},
	})
	if err != nil {
		return nil, err
	}
	var rows []userSum
	return rows, cur.All(ctx, &rows)
}

// checkLockedFundsAgainstOpenPositions: each wallet's locked funds must equal
// the stakes of the open trades of its own account, plus (LIVE only) the
// amounts reserved for pending withdrawals. Anything else means a lock was
// leaked or released twice.
func checkLockedFundsAgainstOpenPositions(ctx context.Context) (models.ReconciliationCheck, error) {
	tradeLocks, err := sumByUser(ctx, models.CollBinaryTrades,
		bson.M{"status": bson.M{"$in": bson.A{models.TradeStatusOpen, models.TradeStatusSettling}}}, "stake")
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	withdrawalLocks, err := sumByUser(ctx, models.CollTransfers,
		bson.M{"kind": models.TransferKindWithdrawal, "status": "PENDING"}, "amount")
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	// An APPROVED withdrawal keeps its reservation until its settlement is
	// confirmed (lockedAmount is cleared in the same write as COMPLETED).
	approvedLocks, err := sumByUser(ctx, models.CollTransfers, bson.M{
		"kind": models.TransferKindWithdrawal, "status": models.TransferStatusApproved, "lockedAmount": bson.M{"$gt": 0},
	}, "lockedAmount")
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	withdrawalLocks = append(withdrawalLocks, approvedLocks...)
	// Trading wallets only: a USDT wallet's locks are withdrawals, checked by
	// checkUsdtLockedAgainstOpenWithdrawals.
	wcur, err := mongox.C(models.CollWallets).Find(ctx, bson.M{
		"lockedBalance": bson.M{"$gt": 0},
		"accountType":   bson.M{"$ne": models.AccountFunding},
		"currency":      models.INRCurrency(),
	},
		options.Find().SetProjection(bson.M{"userId": 1, "accountType": 1, "lockedBalance": 1}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var walletLocks []struct {
		UserID        bson.ObjectID `bson:"userId"`
		AccountType   string        `bson:"accountType"`
		LockedBalance int64         `bson:"lockedBalance"`
	}
	if err := wcur.All(ctx, &walletLocks); err != nil {
		return models.ReconciliationCheck{}, err
	}

	// Insertion-ordered like the TS Map.
	expectedByUser := map[string]int64{}
	keys := []string{}
	add := func(id string, v int64) {
		if _, ok := expectedByUser[id]; !ok {
			keys = append(keys, id)
		}
		expectedByUser[id] += v
	}
	for _, r := range tradeLocks {
		add(r.key(), r.Sum)
	}
	for _, r := range withdrawalLocks {
		// Transfers are LIVE only.
		add(walletKey(r.ID.UserID, models.AccountLive), r.Sum)
	}

	mismatches := []map[string]any{}
	var expectedTotal, actualTotal int64
	seen := map[string]bool{}
	for _, w := range walletLocks {
		key := walletKey(w.UserID, w.AccountType)
		seen[key] = true
		expected := expectedByUser[key]
		expectedTotal += expected
		actualTotal += w.LockedBalance
		if expected != w.LockedBalance {
			mismatches = append(mismatches, map[string]any{
				"userId": w.UserID.Hex(), "accountType": models.NormAccountType(w.AccountType),
				"expectedLocked": expected, "actualLocked": w.LockedBalance,
			})
		}
	}

	// Users who should have locked funds but whose wallet shows none.
	for _, key := range keys {
		expected := expectedByUser[key]
		if seen[key] || expected == 0 {
			continue
		}
		expectedTotal += expected
		userID, account, _ := strings.Cut(key, ":")
		mismatches = append(mismatches, map[string]any{
			"userId": userID, "accountType": account, "expectedLocked": expected, "actualLocked": 0,
		})
	}

	return models.ReconciliationCheck{
		Name:       "locked_vs_open_positions",
		Passed:     len(mismatches) == 0,
		Expected:   expectedTotal,
		Actual:     actualTotal,
		Difference: actualTotal - expectedTotal,
		Details:    map[string]any{"mismatches": capped(mismatches), "mismatchCount": len(mismatches)},
	}, nil
}

// checkSettledTradesHaveSettlements: every settled trade must have exactly one
// settlement row.
func checkSettledTradesHaveSettlements(ctx context.Context) (models.ReconciliationCheck, error) {
	settledTrades, err := mongox.C(models.CollBinaryTrades).CountDocuments(ctx, bson.M{"status": bson.M{"$in": settledTradeStatuses}})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	settlements, err := mongox.C(models.CollSettlements).CountDocuments(ctx, bson.M{})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	return models.ReconciliationCheck{
		Name:       "settled_trades_vs_settlements",
		Passed:     settledTrades == settlements,
		Expected:   settledTrades,
		Actual:     settlements,
		Difference: settlements - settledTrades,
		Details:    map[string]any{"note": "a settlement row is created exactly once per settled trade"},
	}, nil
}

// checkSettlementsHaveWalletTransactions: every settlement must be backed by
// exactly one wallet ledger row.
func checkSettlementsHaveWalletTransactions(ctx context.Context) (models.ReconciliationCheck, error) {
	settlements, err := mongox.C(models.CollSettlements).CountDocuments(ctx, bson.M{})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	txs, err := mongox.C(models.CollWalletTransactions).CountDocuments(ctx, bson.M{"type": bson.M{"$in": bson.A{
		models.WalletTxTradeWin, models.WalletTxTradeLoss, models.WalletTxTradeRefund, models.WalletTxTradeEarlyClose,
	}}})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	return models.ReconciliationCheck{
		Name:       "settlements_vs_wallet_transactions",
		Passed:     settlements == txs,
		Expected:   settlements,
		Actual:     txs,
		Difference: txs - settlements,
		Details:    map[string]any{"note": "keyed on trade:settle:<tradeId>, so a duplicate is impossible by construction"},
	}, nil
}

func sumTotal(ctx context.Context, coll string, match bson.M, field string) (int64, error) {
	cur, err := mongox.C(coll).Aggregate(ctx, bson.A{
		bson.M{"$match": match},
		bson.M{"$group": bson.M{"_id": nil, "total": bson.M{"$sum": "$" + field}}},
	})
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Total int64 `bson:"total"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Total, nil
}

// checkPlatformResultConsistency: platform gross result recorded on trades
// must equal the settlement ledger. These are written by different code
// paths, so agreement is meaningful. DEMO trades are not the platform's.
func checkPlatformResultConsistency(ctx context.Context, from time.Time) (models.ReconciliationCheck, error) {
	actual, err := sumTotal(ctx, models.CollBinaryTrades, bson.M{
		"settledAt": bson.M{"$gte": from}, "status": bson.M{"$in": settledTradeStatuses}, "accountType": models.NotDemo(),
	}, "platformResult")
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	expected, err := sumTotal(ctx, models.CollSettlements,
		bson.M{"createdAt": bson.M{"$gte": from}, "accountType": models.NotDemo()}, "platformResult")
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	return models.ReconciliationCheck{
		Name:       "platform_result_consistency",
		Passed:     expected == actual,
		Expected:   expected,
		Actual:     actual,
		Difference: actual - expected,
		Details:    map[string]any{"window": from.UTC().Format("2006-01-02T15:04:05.000Z")},
	}, nil
}

// checkHedgeOrderIntegrity: filled hedge orders must carry a venue id and a
// fill price.
func checkHedgeOrderIntegrity(ctx context.Context) (models.ReconciliationCheck, error) {
	filledStatuses := bson.M{"$in": bson.A{models.HedgeOrderStatusFilled, models.HedgeOrderStatusPartiallyFilled}}
	c := mongox.C(models.CollHedgeOrders)
	filled, err := c.CountDocuments(ctx, bson.M{"status": filledStatuses})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	malformed, err := c.CountDocuments(ctx, bson.M{
		"status": filledStatuses,
		"$or": bson.A{
			bson.M{"averageFillPrice": nil},
			bson.M{"providerOrderId": nil},
			bson.M{"filledNotional": bson.M{"$lte": 0}},
		},
	})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	return models.ReconciliationCheck{
		Name:       "hedge_order_integrity",
		Passed:     malformed == 0,
		Expected:   0,
		Actual:     malformed,
		Difference: malformed,
		Details:    map[string]any{"filledOrders": filled},
	}, nil
}

// ListReconciliationRuns returns runs newest first (lean documents).
func ListReconciliationRuns(ctx context.Context, page, limit int64) ([]models.ReconciliationRunLean, int64, error) {
	page, limit = max(page, 1), max(limit, 1)
	c := mongox.C(models.CollReconciliationRuns)
	cur, err := c.Find(ctx, bson.M{}, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	var runs []models.ReconciliationRun
	if err := cur.All(ctx, &runs); err != nil {
		return nil, 0, err
	}
	items := make([]models.ReconciliationRunLean, len(runs))
	for i := range runs {
		items[i] = models.ToReconciliationRunLean(&runs[i])
	}
	total, err := c.CountDocuments(ctx, bson.M{})
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ResolveReconciliationRun records an operator's resolution note on a run.
func ResolveReconciliationRun(ctx context.Context, runID, actorID, note string) error {
	rid, ok := mongox.ParseID(runID)
	if !ok {
		return apperr.Internal("input must be a 24 character hex string, 12 byte Uint8Array, or an integer")
	}
	aid, ok := mongox.ParseID(actorID)
	if !ok {
		return apperr.Internal("input must be a 24 character hex string, 12 byte Uint8Array, or an integer")
	}
	_, err := mongox.C(models.CollReconciliationRuns).UpdateOne(ctx, bson.M{"_id": rid},
		bson.M{"$set": bson.M{"resolvedBy": aid, "resolvedAt": mongox.Now(), "resolutionNote": note}})
	return err
}

func errMessage(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}
