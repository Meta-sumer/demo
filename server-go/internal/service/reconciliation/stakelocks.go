package reconciliation

import (
	"context"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/service/wallets"
)

// checkStakeLocksHaveOpenTrades: every stake lock (TRADE_LOCK) older than the
// orphan grace that was never released (no trade:settle:<id> row) must belong
// to a trade that is OPEN or SETTLING. Every lock row is examined. A lock
// whose trade does not exist is an orphan (the leader loop
// "stake-lock-orphans" releases it); a lock on a finished trade is money held
// for nothing (a cancel not yet repaired - see cancelled_trades_released - or
// a settlement recorded without its wallet movement) and needs an operator.
func checkStakeLocksHaveOpenTrades(ctx context.Context) (models.ReconciliationCheck, error) {
	var listed []map[string]any
	var orphans, finished int64
	var lockedMinor int64
	err := wallets.ScanUnreleasedStakeLocks(ctx, time.Time{}, time.Now().Add(-wallets.OrphanGrace), 500, func(locks []wallets.UnreleasedStakeLock) error {
		for _, l := range locks {
			kind := "finished_trade"
			if l.Orphan() {
				kind = "orphan"
				orphans++
			} else {
				finished++
			}
			lockedMinor += l.Amount
			listed = append(listed, map[string]any{
				"tradeId": l.TradeID, "userId": l.UserID.Hex(), "accountType": models.NormAccountType(l.AccountType),
				"amount": l.Amount, "kind": kind, "tradeStatus": l.TradeStatus, "lockedAt": l.CreatedAt,
			})
		}
		return nil
	})
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	n := orphans + finished
	return models.ReconciliationCheck{
		Name:       "stake_locks_have_open_trades",
		Passed:     n == 0,
		Expected:   0,
		Actual:     n,
		Difference: n,
		Details: map[string]any{
			"note": "every unreleased stake lock (trade:lock:<id> without trade:settle:<id>) must belong to an OPEN or SETTLING trade; " +
				"orphans are released by the stake-lock-orphans loop, finished trades need an operator (cancelled: POST /admin/binary/trades/cancellations/repair)",
			"orphans":     orphans,
			"finished":    finished,
			"lockedMinor": lockedMinor,
			"locks":       capped(listed),
			"locksOf":     len(listed),
		},
	}, nil
}
