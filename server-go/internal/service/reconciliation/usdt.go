package reconciliation

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// USDT wallet checks. Every figure here is micro-USDT and is never added to
// an INR figure.

// usdtDepositRef is the ledger referenceType of a USDT deposit credit.
const usdtDepositRef = "UsdtDeposit"

// checkUsdtWalletLedgerAgainstBalances: balance = ledger sum for every USDT
// wallet (the FUNDING wallets and any LIVE USDT trading wallet).
func checkUsdtWalletLedgerAgainstBalances(ctx context.Context) (models.ReconciliationCheck, error) {
	return ledgerAgainstBalances(ctx, "usdt_wallet_ledger_vs_balance", models.CurrencyUSDT)
}

// checkUsdtLockedAgainstOpenWithdrawals: a FUNDING wallet's locked balance
// is exactly the gross of its open withdrawals - nothing else locks it.
func checkUsdtLockedAgainstOpenWithdrawals(ctx context.Context) (models.ReconciliationCheck, error) {
	cur, err := mongox.C(models.CollUsdtWithdrawals).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"status": bson.M{"$in": models.WithdrawalOpenStatuses}}},
		bson.M{"$group": bson.M{"_id": "$userId", "sum": bson.M{"$sum": "$amount"}}},
	})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var open []struct {
		ID  bson.ObjectID `bson:"_id"`
		Sum int64         `bson:"sum"`
	}
	if err := cur.All(ctx, &open); err != nil {
		return models.ReconciliationCheck{}, err
	}
	expected := map[bson.ObjectID]int64{}
	for _, o := range open {
		expected[o.ID] = o.Sum
	}

	wcur, err := mongox.C(models.CollWallets).Find(ctx,
		bson.M{"accountType": models.AccountFunding, "currency": models.CurrencyUSDT},
		options.Find().SetProjection(bson.M{"userId": 1, "lockedBalance": 1}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var wallets []struct {
		UserID        bson.ObjectID `bson:"userId"`
		LockedBalance int64         `bson:"lockedBalance"`
	}
	if err := wcur.All(ctx, &wallets); err != nil {
		return models.ReconciliationCheck{}, err
	}

	mismatches := []map[string]any{}
	var expectedTotal, actualTotal int64
	seen := map[bson.ObjectID]bool{}
	for _, w := range wallets {
		seen[w.UserID] = true
		e := expected[w.UserID]
		expectedTotal += e
		actualTotal += w.LockedBalance
		if e != w.LockedBalance {
			mismatches = append(mismatches, map[string]any{
				"userId": w.UserID.Hex(), "expectedLocked": e, "actualLocked": w.LockedBalance,
			})
		}
	}
	for _, o := range open {
		if seen[o.ID] || o.Sum == 0 {
			continue
		}
		expectedTotal += o.Sum
		mismatches = append(mismatches, map[string]any{"userId": o.ID.Hex(), "expectedLocked": o.Sum, "actualLocked": 0})
	}
	return models.ReconciliationCheck{
		Name:       "usdt_locked_vs_open_withdrawals",
		Passed:     len(mismatches) == 0,
		Expected:   expectedTotal,
		Actual:     actualTotal,
		Difference: actualTotal - expectedTotal,
		Details:    map[string]any{"mismatches": capped(mismatches), "mismatchCount": len(mismatches), "unit": "micro-USDT"},
	}, nil
}

// checkUsdtPaidDepositsCredited: a deposit BeeX reported paid/overpaid must
// have been credited.
func checkUsdtPaidDepositsCredited(ctx context.Context) (models.ReconciliationCheck, error) {
	c := mongox.C(models.CollUsdtDeposits)
	filter := bson.M{"status": bson.M{"$in": bson.A{models.InvoicePaid, models.InvoiceOverpaid}}, "credited": bson.M{"$ne": true}}
	n, err := c.CountDocuments(ctx, filter)
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	ids := []string{}
	if n > 0 {
		cur, err := c.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(20))
		if err != nil {
			return models.ReconciliationCheck{}, err
		}
		var rows []struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := cur.All(ctx, &rows); err != nil {
			return models.ReconciliationCheck{}, err
		}
		for _, r := range rows {
			ids = append(ids, r.ID.Hex())
		}
	}
	return models.ReconciliationCheck{
		Name:       "usdt_paid_deposits_credited",
		Passed:     n == 0,
		Expected:   0,
		Actual:     n,
		Difference: n,
		Details:    map[string]any{"depositIds": ids},
	}, nil
}

// checkUsdtCreditedDepositsLedger: every credited deposit has exactly one
// DEPOSIT ledger row, and no deposit row exists for an uncredited deposit.
func checkUsdtCreditedDepositsLedger(ctx context.Context) (models.ReconciliationCheck, error) {
	cur, err := mongox.C(models.CollWalletTransactions).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"referenceType": usdtDepositRef, "type": models.WalletTxDeposit}},
		bson.M{"$group": bson.M{"_id": "$referenceId", "rows": bson.M{"$sum": 1}, "amount": bson.M{"$sum": "$amount"}}},
	})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var ledger []struct {
		ID     string `bson:"_id"`
		Rows   int64  `bson:"rows"`
		Amount int64  `bson:"amount"`
	}
	if err := cur.All(ctx, &ledger); err != nil {
		return models.ReconciliationCheck{}, err
	}
	byDeposit := map[string]struct{ rows, amount int64 }{}
	for _, l := range ledger {
		byDeposit[l.ID] = struct{ rows, amount int64 }{l.Rows, l.Amount}
	}

	dcur, err := mongox.C(models.CollUsdtDeposits).Find(ctx, bson.M{"credited": true},
		options.Find().SetProjection(bson.M{"_id": 1, "amountCredited": 1}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var credited []struct {
		ID             bson.ObjectID `bson:"_id"`
		AmountCredited int64         `bson:"amountCredited"`
	}
	if err := dcur.All(ctx, &credited); err != nil {
		return models.ReconciliationCheck{}, err
	}

	mismatches := []map[string]any{}
	var expectedTotal, actualTotal int64
	seen := map[string]bool{}
	for _, d := range credited {
		id := d.ID.Hex()
		seen[id] = true
		l := byDeposit[id]
		expectedTotal += d.AmountCredited
		actualTotal += l.amount
		if l.rows != 1 || l.amount != d.AmountCredited {
			mismatches = append(mismatches, map[string]any{
				"depositId": id, "ledgerRows": l.rows, "ledgerAmount": l.amount, "amountCredited": d.AmountCredited,
			})
		}
	}
	for id, l := range byDeposit {
		if seen[id] {
			continue
		}
		actualTotal += l.amount
		mismatches = append(mismatches, map[string]any{"depositId": id, "ledgerRows": l.rows, "ledgerAmount": l.amount, "credited": false})
	}
	return models.ReconciliationCheck{
		Name:       "usdt_credited_deposits_vs_ledger",
		Passed:     len(mismatches) == 0,
		Expected:   expectedTotal,
		Actual:     actualTotal,
		Difference: actualTotal - expectedTotal,
		Details:    map[string]any{"creditedDeposits": len(credited), "mismatches": capped(mismatches), "mismatchCount": len(mismatches), "unit": "micro-USDT"},
	}, nil
}

// checkUsdtCompletedWithdrawalsSettled: every completed withdrawal has its
// settle row (the gross left the balance).
func checkUsdtCompletedWithdrawalsSettled(ctx context.Context) (models.ReconciliationCheck, error) {
	wcur, err := mongox.C(models.CollUsdtWithdrawals).Find(ctx, bson.M{"status": models.WithdrawalCompleted},
		options.Find().SetProjection(bson.M{"_id": 1, "amount": 1}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var completed []struct {
		ID     bson.ObjectID `bson:"_id"`
		Amount int64         `bson:"amount"`
	}
	if err := wcur.All(ctx, &completed); err != nil {
		return models.ReconciliationCheck{}, err
	}
	keys := bson.A{}
	for _, w := range completed {
		keys = append(keys, "usdt:withdrawal:settle:"+w.ID.Hex())
	}
	settled := map[string]int64{}
	if len(keys) > 0 {
		cur, err := mongox.C(models.CollWalletTransactions).Find(ctx, bson.M{"idempotencyKey": bson.M{"$in": keys}},
			options.Find().SetProjection(bson.M{"referenceId": 1, "amount": 1}))
		if err != nil {
			return models.ReconciliationCheck{}, err
		}
		var rows []struct {
			ReferenceID *string `bson:"referenceId"`
			Amount      int64   `bson:"amount"`
		}
		if err := cur.All(ctx, &rows); err != nil {
			return models.ReconciliationCheck{}, err
		}
		for _, r := range rows {
			if r.ReferenceID != nil {
				settled[*r.ReferenceID] = r.Amount
			}
		}
	}
	missing := []map[string]any{}
	var expectedTotal, actualTotal int64
	for _, w := range completed {
		expectedTotal += w.Amount
		amount, ok := settled[w.ID.Hex()]
		actualTotal += amount
		if !ok || amount != w.Amount {
			missing = append(missing, map[string]any{"withdrawalId": w.ID.Hex(), "amount": w.Amount, "settledAmount": amount})
		}
	}
	return models.ReconciliationCheck{
		Name:       "usdt_completed_withdrawals_settled",
		Passed:     len(missing) == 0,
		Expected:   expectedTotal,
		Actual:     actualTotal,
		Difference: actualTotal - expectedTotal,
		Details:    map[string]any{"completed": len(completed), "mismatches": capped(missing), "mismatchCount": len(missing), "unit": "micro-USDT"},
	}, nil
}
