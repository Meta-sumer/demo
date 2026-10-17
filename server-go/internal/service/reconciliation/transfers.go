package reconciliation

import (
	"context"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// Manual INR transfers against the ledger. Each transfer state implies a
// precise set of ledger rows, found by the transfer's idempotency keys:
//
//	DEPOSIT     COMPLETED        credit (amount = transfer amount)
//	            PENDING|REJECTED|FAILED   no credit
//	            APPROVED         either (an approval in progress; see the stuck check)
//	WITHDRAWAL  PENDING          lock, no settle, no unlock
//	            APPROVED         lock, no unlock
//	            COMPLETED        lock + settle, no unlock
//	            REJECTED|FAILED  no settle; a lock only together with its unlock
//
// and every Transfer ledger row must belong to an existing transfer of the
// same customer.

const (
	// stuckApprovalAfter: an APPROVED transfer older than this did not finish.
	stuckApprovalAfter = 5 * time.Minute
	// orphanRowGrace keeps a withdrawal request in flight (reservation written,
	// transfer about to be) out of the orphan count.
	orphanRowGrace = time.Minute
)

type transferRow struct {
	ID                  bson.ObjectID         `bson:"_id"`
	Reference           string                `bson:"reference"`
	Kind                models.TransferKind   `bson:"kind"`
	Status              models.TransferStatus `bson:"status"`
	UserID              bson.ObjectID         `bson:"userId"`
	Amount              int64                 `bson:"amount"`
	WalletTransactionID *bson.ObjectID        `bson:"walletTransactionId"`
}

type ledgerRow struct {
	ID             bson.ObjectID                `bson:"_id"`
	UserID         bson.ObjectID                `bson:"userId"`
	Type           models.WalletTransactionType `bson:"type"`
	Movement       models.MoneyMovement         `bson:"movement"`
	Amount         int64                        `bson:"amount"`
	ReferenceID    *string                      `bson:"referenceId"`
	IdempotencyKey *string                      `bson:"idempotencyKey"`
	CreatedAt      time.Time                    `bson:"createdAt"`
}

// checkTransfersAgainstLedger: every manual transfer's state agrees with its
// ledger rows, and no transfer ledger row is without its transfer.
func checkTransfersAgainstLedger(ctx context.Context) (models.ReconciliationCheck, error) {
	return transfersAgainstLedger(ctx, time.Now())
}

func transfersAgainstLedger(ctx context.Context, now time.Time) (models.ReconciliationCheck, error) {
	tcur, err := mongox.C(models.CollTransfers).Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{
		"reference": 1, "kind": 1, "status": 1, "userId": 1, "amount": 1, "walletTransactionId": 1,
	}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var transfers []transferRow
	if err := tcur.All(ctx, &transfers); err != nil {
		return models.ReconciliationCheck{}, err
	}
	lcur, err := mongox.C(models.CollWalletTransactions).Find(ctx,
		bson.M{"referenceType": models.TransferLedgerReference, "currency": models.INRCurrency()},
		options.Find().SetProjection(bson.M{
			"userId": 1, "type": 1, "movement": 1, "amount": 1, "referenceId": 1, "idempotencyKey": 1, "createdAt": 1,
		}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var rows []ledgerRow
	if err := lcur.All(ctx, &rows); err != nil {
		return models.ReconciliationCheck{}, err
	}

	byKey := make(map[string]*ledgerRow, len(rows))
	for i := range rows {
		if rows[i].IdempotencyKey != nil {
			byKey[*rows[i].IdempotencyKey] = &rows[i]
		}
	}
	known := make(map[string]transferRow, len(transfers))
	mismatches := []map[string]any{}
	add := func(t transferRow, problem string, extra map[string]any) {
		m := map[string]any{
			"transferId": t.ID.Hex(), "reference": t.Reference, "kind": string(t.Kind),
			"status": string(t.Status), "amount": t.Amount, "problem": problem,
		}
		for k, v := range extra {
			m[k] = v
		}
		mismatches = append(mismatches, m)
	}
	var depositsCompleted, depositCredits, withdrawalsCompleted, withdrawalSettles int64

	// expect checks the row under key: present (with this movement and the
	// transfer's amount and customer) or absent.
	expect := func(t transferRow, key string, want bool, movement models.MoneyMovement) *ledgerRow {
		row := byKey[key]
		switch {
		case want && row == nil:
			add(t, "missing ledger row "+key, nil)
		case !want && row != nil:
			add(t, "unexpected ledger row "+key, map[string]any{"ledgerRowId": row.ID.Hex()})
		case row != nil && (row.Movement != movement || row.Amount != t.Amount || row.UserID != t.UserID):
			add(t, "ledger row "+key+" does not match the transfer", map[string]any{
				"ledgerRowId": row.ID.Hex(), "ledgerAmount": row.Amount, "ledgerMovement": string(row.Movement),
				"ledgerUserId": row.UserID.Hex(),
			})
		}
		return row
	}

	for _, t := range transfers {
		id := t.ID.Hex()
		known[id] = t
		switch t.Kind {
		case models.TransferKindDeposit:
			credit := models.DepositCreditKey(id)
			switch t.Status {
			case models.TransferStatusCompleted:
				depositsCompleted += t.Amount
				if row := expect(t, credit, true, models.MovementCredit); row != nil {
					depositCredits += row.Amount
					if t.WalletTransactionID != nil && *t.WalletTransactionID != row.ID {
						add(t, "walletTransactionId does not point at the credit", map[string]any{"ledgerRowId": row.ID.Hex()})
					}
				}
			case models.TransferStatusApproved:
				if byKey[credit] != nil {
					expect(t, credit, true, models.MovementCredit)
				}
			default:
				expect(t, credit, false, models.MovementCredit)
			}
		case models.TransferKindWithdrawal:
			lock, settle, unlock := models.WithdrawalLockKey(id), models.WithdrawalSettleKey(id), models.WithdrawalUnlockKey(id)
			switch t.Status {
			case models.TransferStatusPending:
				expect(t, lock, true, models.MovementLock)
				expect(t, settle, false, models.MovementDebit)
				expect(t, unlock, false, models.MovementUnlock)
			case models.TransferStatusApproved:
				expect(t, lock, true, models.MovementLock)
				expect(t, unlock, false, models.MovementUnlock)
				if byKey[settle] != nil {
					expect(t, settle, true, models.MovementDebit)
				}
			case models.TransferStatusCompleted:
				withdrawalsCompleted += t.Amount
				expect(t, lock, true, models.MovementLock)
				if row := expect(t, settle, true, models.MovementDebit); row != nil {
					withdrawalSettles += row.Amount
					if t.WalletTransactionID != nil && *t.WalletTransactionID != row.ID {
						add(t, "walletTransactionId does not point at the settlement", map[string]any{"ledgerRowId": row.ID.Hex()})
					}
				}
				expect(t, unlock, false, models.MovementUnlock)
			default: // REJECTED, FAILED
				expect(t, settle, false, models.MovementDebit)
				if byKey[lock] != nil {
					expect(t, lock, true, models.MovementLock)
					expect(t, unlock, true, models.MovementUnlock)
				} else {
					expect(t, unlock, false, models.MovementUnlock)
				}
			}
		}
	}

	// Ledger rows that belong to no transfer, or to another customer's.
	orphans := 0
	for i := range rows {
		r := &rows[i]
		if r.ReferenceID == nil {
			continue
		}
		t, ok := known[*r.ReferenceID]
		if ok {
			// Rows under the transfer's own keys were checked above.
			if r.IdempotencyKey == nil || !isTransferKey(t, *r.IdempotencyKey) {
				add(t, "ledger row with a key that is not one of the transfer's", map[string]any{"ledgerRowId": r.ID.Hex()})
			}
			continue
		}
		if now.Sub(r.CreatedAt) < orphanRowGrace {
			continue
		}
		// A reservation released after its transfer failed to be written is
		// a balanced pair, not a problem.
		if r.Movement == models.MovementLock && byKey[models.WithdrawalUnlockKey(*r.ReferenceID)] != nil {
			continue
		}
		if r.Movement == models.MovementUnlock && byKey[models.WithdrawalLockKey(*r.ReferenceID)] != nil {
			continue
		}
		orphans++
		mismatches = append(mismatches, map[string]any{
			"transferId": *r.ReferenceID, "ledgerRowId": r.ID.Hex(), "userId": r.UserID.Hex(),
			"type": string(r.Type), "movement": string(r.Movement), "amount": r.Amount,
			"problem": "ledger row references a transfer that does not exist",
		})
	}
	sort.SliceStable(mismatches, func(i, j int) bool {
		return mismatches[i]["transferId"].(string) < mismatches[j]["transferId"].(string)
	})

	n := int64(len(mismatches))
	return models.ReconciliationCheck{
		Name:       "transfers_vs_ledger",
		Passed:     n == 0,
		Expected:   0,
		Actual:     n,
		Difference: n,
		Details: map[string]any{
			"transfers":                 len(transfers),
			"ledgerRows":                len(rows),
			"orphanRows":                orphans,
			"depositsCompletedTotal":    depositsCompleted,
			"depositCreditsTotal":       depositCredits,
			"withdrawalsCompletedTotal": withdrawalsCompleted,
			"withdrawalSettlesTotal":    withdrawalSettles,
			"mismatches":                capped(mismatches),
			"mismatchCount":             len(mismatches),
		},
	}, nil
}

func isTransferKey(t transferRow, key string) bool {
	id := t.ID.Hex()
	if t.Kind == models.TransferKindDeposit {
		return key == models.DepositCreditKey(id)
	}
	return key == models.WithdrawalLockKey(id) || key == models.WithdrawalSettleKey(id) || key == models.WithdrawalUnlockKey(id)
}

// checkStuckApprovedTransfers: an APPROVED transfer is an operator decision
// whose wallet effect has not been confirmed. One that stays APPROVED needs
// POST /admin/transfers/{id}/complete (or /admin/transfers/repair).
func checkStuckApprovedTransfers(ctx context.Context) (models.ReconciliationCheck, error) {
	return stuckApprovedTransfers(ctx, time.Now())
}

func stuckApprovedTransfers(ctx context.Context, now time.Time) (models.ReconciliationCheck, error) {
	cutoff := now.Add(-stuckApprovalAfter)
	cur, err := mongox.C(models.CollTransfers).Find(ctx, bson.M{
		"status": models.TransferStatusApproved,
		"$or": bson.A{
			bson.M{"reviewedAt": bson.M{"$lt": cutoff}},
			bson.M{"reviewedAt": nil, "updatedAt": bson.M{"$lt": cutoff}},
		},
	}, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: 1}}).SetProjection(bson.M{
		"reference": 1, "kind": 1, "amount": 1, "reviewedAt": 1, "updatedAt": 1, "lastError": 1, "completionAttempts": 1,
	}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var stuck []models.Transfer
	if err := cur.All(ctx, &stuck); err != nil {
		return models.ReconciliationCheck{}, err
	}
	items := make([]map[string]any, 0, len(stuck))
	for _, t := range stuck {
		items = append(items, map[string]any{
			"transferId": t.ID.Hex(), "reference": t.Reference, "kind": string(t.Kind), "amount": t.Amount,
			"lastError": t.LastError, "completionAttempts": t.CompletionAttempts,
		})
	}
	n := int64(len(stuck))
	return models.ReconciliationCheck{
		Name:       "transfers_stuck_approved",
		Passed:     n == 0,
		Expected:   0,
		Actual:     n,
		Difference: n,
		Details: map[string]any{
			"olderThanMinutes": int64(stuckApprovalAfter / time.Minute),
			"transfers":        capped(items),
			"count":            len(items),
		},
	}, nil
}
