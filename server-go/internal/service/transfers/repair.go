package transfers

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/wallets"
)

// Repair finishes what an interrupted workflow left behind. Every action it
// takes is the one the workflow itself would have taken next, keyed on the
// same ledger idempotency keys, so running it twice - or alongside the
// workflow - can never move money twice.
//
//   - APPROVED transfers: the operator's decision stands; the wallet effect is
//     applied (or found applied) and the transfer completed.
//   - REJECTED withdrawals still holding a reservation: it is released.
//   - Withdrawal reservations whose transfer was never written: released.

const (
	// DefaultRepairGrace keeps the sweep away from workflows still in flight.
	DefaultRepairGrace = 60 * time.Second
	repairMaxPerRun    = 500
	// A busy transfer is skipped, not waited for: its owner is working on it.
	repairLockWait = 2 * time.Second
)

// RepairedTransfer is one item of a repair report.
type RepairedTransfer struct {
	TransferID string `json:"transferId"`
	Reference  string `json:"reference,omitempty"`
	Kind       string `json:"kind,omitempty"`
	Amount     int64  `json:"amount"`
	// completed | reservation_released | orphan_reservation_released
	Action string `json:"action"`
	Error  string `json:"error,omitempty"`
}

// TransferRepairResult reports one RepairTransfers pass.
type TransferRepairResult struct {
	Completed            []RepairedTransfer `json:"completed"`
	ReleasedReservations []RepairedTransfer `json:"releasedReservations"`
	Failed               []RepairedTransfer `json:"failed"`
}

func errText(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Code + ": " + ae.Message
	}
	return err.Error()
}

// RepairTransfers runs every repair once, touching only states older than
// grace (<= 0 means DefaultRepairGrace). actorID is the operator running it,
// recorded as the reviewer where an approval has none.
func RepairTransfers(ctx context.Context, actorID string, grace time.Duration) (TransferRepairResult, error) {
	ctx = context.WithoutCancel(ctx)
	if grace <= 0 {
		grace = DefaultRepairGrace
	}
	actor, err := bson.ObjectIDFromHex(actorID)
	if err != nil {
		return TransferRepairResult{}, apperr.Unauth()
	}
	out := TransferRepairResult{Completed: []RepairedTransfer{}, ReleasedReservations: []RepairedTransfer{}, Failed: []RepairedTransfer{}}
	cutoff := time.Now().Add(-grace)

	if err := completeStuckApprovals(ctx, actor, cutoff, &out); err != nil {
		return out, err
	}
	if err := releaseRejectedReservations(ctx, cutoff, &out); err != nil {
		return out, err
	}
	// The transfer lock protects the two passes above at any grace. Nothing
	// locks a withdrawal request between its reservation and its transfer, so
	// an orphan is only ever judged after the full default grace.
	orphanCutoff := cutoff
	if grace < DefaultRepairGrace {
		orphanCutoff = time.Now().Add(-DefaultRepairGrace)
	}
	if err := releaseOrphanedReservations(ctx, orphanCutoff, &out); err != nil {
		return out, err
	}
	logx.Info("transfer repair complete", "completed", len(out.Completed),
		"releasedReservations", len(out.ReleasedReservations), "failed", len(out.Failed))
	return out, nil
}

func itemOf(t *models.Transfer, action string) RepairedTransfer {
	return RepairedTransfer{TransferID: t.ID.Hex(), Reference: t.Reference, Kind: string(t.Kind), Amount: t.Amount, Action: action}
}

func findTransfers(ctx context.Context, filter bson.M) ([]models.Transfer, error) {
	cur, err := transfersColl().Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "updatedAt", Value: 1}}).SetLimit(repairMaxPerRun))
	if err != nil {
		return nil, err
	}
	var rows []models.Transfer
	return rows, cur.All(ctx, &rows)
}

func completeStuckApprovals(ctx context.Context, actor bson.ObjectID, cutoff time.Time, out *TransferRepairResult) error {
	rows, err := findTransfers(ctx, bson.M{"status": models.TransferStatusApproved, "updatedAt": bson.M{"$lt": cutoff}})
	if err != nil {
		return err
	}
	for i := range rows {
		item := itemOf(&rows[i], "completed")
		err := withTransferLock(ctx, rows[i].ID, repairLockWait, func() error {
			t, err := loadTransfer(ctx, rows[i].ID)
			if err != nil {
				return err
			}
			if t.Status != models.TransferStatusApproved {
				return nil
			}
			_, err = completeApproved(ctx, t, approval{reviewer: actor})
			return err
		})
		if err != nil {
			item.Error = errText(err)
			out.Failed = append(out.Failed, item)
			continue
		}
		out.Completed = append(out.Completed, item)
	}
	return nil
}

func releaseRejectedReservations(ctx context.Context, cutoff time.Time, out *TransferRepairResult) error {
	rows, err := findTransfers(ctx, bson.M{
		"kind": models.TransferKindWithdrawal, "status": models.TransferStatusRejected,
		"lockedAmount": bson.M{"$gt": 0}, "updatedAt": bson.M{"$lt": cutoff},
	})
	if err != nil {
		return err
	}
	for i := range rows {
		item := itemOf(&rows[i], "reservation_released")
		released := false
		err := withTransferLock(ctx, rows[i].ID, repairLockWait, func() error {
			t, err := loadTransfer(ctx, rows[i].ID)
			if err != nil {
				return err
			}
			if t.Status != models.TransferStatusRejected {
				return nil
			}
			amount, err := reservationToRelease(ctx, t)
			if err != nil {
				return err
			}
			if amount > 0 {
				if _, err := wallets.UnlockWithdrawalFunds(ctx, releaseInput(t, amount, "Withdrawal "+t.Reference+" rejected")); err != nil {
					return err
				}
				released = true
			}
			_, err = transfersColl().UpdateOne(ctx, bson.M{"_id": t.ID, "status": models.TransferStatusRejected},
				bson.M{"$set": bson.M{"lockedAmount": nil, "updatedAt": mongox.Now()}})
			return err
		})
		if err != nil {
			item.Error = errText(err)
			out.Failed = append(out.Failed, item)
			continue
		}
		if released {
			out.ReleasedReservations = append(out.ReleasedReservations, item)
		}
	}
	return nil
}

// releaseOrphanedReservations releases WITHDRAWAL/LOCK rows whose transfer
// does not exist and that were never released. Such a reservation can only be
// left by a request that failed between its reservation and its transfer, so
// releasing it returns the customer's own money and unwinds nothing real.
func releaseOrphanedReservations(ctx context.Context, cutoff time.Time, out *TransferRepairResult) error {
	cur, err := mongox.C(models.CollWalletTransactions).Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{
			"type": models.WalletTxWithdrawal, "movement": models.MovementLock,
			"referenceType": models.TransferLedgerReference, "createdAt": bson.M{"$lt": cutoff},
		}}},
		{{Key: "$lookup", Value: bson.M{
			"from": models.CollTransfers,
			"let":  bson.M{"rid": bson.M{"$convert": bson.M{"input": "$referenceId", "to": "objectId", "onError": nil, "onNull": nil}}},
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"$expr": bson.M{"$eq": bson.A{"$_id", "$$rid"}}}},
				bson.M{"$project": bson.M{"_id": 1}},
			},
			"as": "transfer",
		}}},
		{{Key: "$match", Value: bson.M{"transfer": bson.M{"$size": 0}}}},
		{{Key: "$limit", Value: repairMaxPerRun}},
	})
	if err != nil {
		return err
	}
	var locks []models.WalletTransaction
	if err := cur.All(ctx, &locks); err != nil {
		return err
	}
	for _, lock := range locks {
		if lock.ReferenceID == nil || *lock.ReferenceID == "" {
			continue
		}
		transferID := *lock.ReferenceID
		item := RepairedTransfer{TransferID: transferID, Kind: string(models.TransferKindWithdrawal), Amount: lock.Amount,
			Action: "orphan_reservation_released"}
		done := false
		for _, key := range []string{models.WithdrawalUnlockKey(transferID), models.WithdrawalSettleKey(transferID)} {
			row, err := wallets.LedgerRowByKey(ctx, key)
			if err != nil {
				return err
			}
			if row != nil {
				done = true
			}
		}
		if done {
			continue
		}
		_, err := wallets.UnlockWithdrawalFunds(ctx, wallets.AdjustmentInput{
			UserID:         lock.UserID.Hex(),
			Amount:         lock.Amount,
			Type:           models.WalletTxWithdrawal,
			ReferenceType:  models.TransferLedgerReference,
			ReferenceID:    transferID,
			IdempotencyKey: models.WithdrawalUnlockKey(transferID),
			Description:    "Reservation of an unrecorded withdrawal request released",
		})
		if err != nil {
			item.Error = errText(err)
			out.Failed = append(out.Failed, item)
			continue
		}
		logx.Warn("released a withdrawal reservation whose transfer was never recorded",
			"transferId", transferID, "userId", lock.UserID.Hex(), "amount", lock.Amount)
		out.ReleasedReservations = append(out.ReleasedReservations, item)
	}
	return nil
}
