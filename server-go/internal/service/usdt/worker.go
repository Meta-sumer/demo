package usdt

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// Background work, each on exactly one instance (leader loops):
//   - webhook retry: reprocess stored events that failed or were deferred;
//   - payout submission: re-send payouts whose outcome is unknown;
//   - reconciliation sweep: poll BeeX for records webhooks seem to have
//     missed. Webhooks stay primary; this is the fallback.

const (
	retryInterval   = 30 * time.Second
	retryBatch      = 50
	submitBatch     = 20
	sweepInterval   = 10 * time.Minute
	sweepBatch      = 50
	stuckAfter      = 30 * time.Minute
	reRefreshAfter  = 10 * time.Minute
	sweepCallBudget = 5 * time.Minute
)

// SweepResult reports one reconciliation sweep.
type SweepResult struct {
	Deposits    int `json:"deposits"`
	Withdrawals int `json:"withdrawals"`
	Changed     int `json:"changed"`
	Errors      int `json:"errors"`
}

// Sweep refreshes deposits stuck in a non-final state for over 30 minutes
// with no webhook in the last 30 minutes, and withdrawals stuck in
// requested/awaiting_approval/sending - at most 50 records per run.
func Sweep(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	if !Enabled() {
		return res, nil
	}
	ctx, cancel := context.WithTimeout(ctx, sweepCallBudget)
	defer cancel()
	cutoff := now.Add(-stuckAfter)
	refreshCutoff := now.Add(-reRefreshAfter)

	dcur, err := depositsColl().Find(ctx, bson.M{
		"credited":  false,
		"status":    bson.M{"$in": models.InvoiceOpenStatuses},
		"createdAt": bson.M{"$lt": cutoff},
		"$and": bson.A{
			bson.M{"$or": bson.A{bson.M{"lastWebhookAt": nil}, bson.M{"lastWebhookAt": bson.M{"$lt": cutoff}}}},
			bson.M{"$or": bson.A{bson.M{"lastRefreshedAt": nil}, bson.M{"lastRefreshedAt": bson.M{"$lt": refreshCutoff}}}},
		},
	}, options.Find().SetSort(bson.D{{Key: "lastRefreshedAt", Value: 1}, {Key: "createdAt", Value: 1}}).
		SetLimit(sweepBatch).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return res, err
	}
	var deps []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := dcur.All(ctx, &deps); err != nil {
		return res, err
	}
	for _, d := range deps {
		res.Deposits++
		r, _, err := RefreshDeposit(ctx, d.ID, "sweeper")
		if err != nil {
			res.Errors++
			logx.Warn("BEEX_RECONCILIATION deposit refresh failed", "depositId", d.ID.Hex(), "err", err.Error())
			continue
		}
		if r.Changed {
			res.Changed++
		}
	}

	remaining := int64(sweepBatch - len(deps))
	if remaining > 0 {
		wcur, err := withdrawalsColl().Find(ctx, bson.M{
			"status":            bson.M{"$in": submittedStatuses},
			"submissionPending": false,
			"updatedAt":         bson.M{"$lt": cutoff},
			"$or":               bson.A{bson.M{"lastRefreshedAt": nil}, bson.M{"lastRefreshedAt": bson.M{"$lt": refreshCutoff}}},
		}, options.Find().SetSort(bson.D{{Key: "lastRefreshedAt", Value: 1}, {Key: "createdAt", Value: 1}}).
			SetLimit(remaining).SetProjection(bson.M{"_id": 1}))
		if err != nil {
			return res, err
		}
		var ws []struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := wcur.All(ctx, &ws); err != nil {
			return res, err
		}
		for _, w := range ws {
			res.Withdrawals++
			r, _, err := RefreshWithdrawal(ctx, w.ID, "sweeper")
			if err != nil {
				res.Errors++
				logx.Warn("BEEX_RECONCILIATION withdrawal refresh failed", "withdrawalId", w.ID.Hex(), "err", err.Error())
				continue
			}
			if r.Changed {
				res.Changed++
			}
		}
	}
	if res.Deposits+res.Withdrawals > 0 {
		logx.Info("BEEX_RECONCILIATION sweep", "deposits", res.Deposits, "withdrawals", res.Withdrawals,
			"changed", res.Changed, "errors", res.Errors)
	}
	return res, nil
}

var (
	loopMu sync.Mutex
	loops  []*leader.Loop
)

// Start runs the USDT loops (no-op when workers or USDT are disabled).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	if !Enabled() {
		logx.Info("USDT payments disabled - USDT workers not started")
		return
	}
	if !mongox.TransactionsAvailable() {
		logx.Warn("USDT payments are running without MongoDB transactions; balance changes and their records cannot commit atomically. Use a replica set.")
	}
	loopMu.Lock()
	defer loopMu.Unlock()
	if len(loops) == 0 {
		loops = []*leader.Loop{
			leader.New(leader.Options{
				Name: "usdt-webhook-retry", Interval: retryInterval, LockTTL: 5 * time.Minute,
				OnTick: func(ctx context.Context) error { _, err := RetryWebhookEvents(ctx, retryBatch); return err },
			}),
			leader.New(leader.Options{
				Name: "usdt-payout-submit", Interval: retryInterval, LockTTL: 10 * time.Minute,
				OnTick: func(ctx context.Context) error { _, err := RetryPendingSubmissions(ctx, submitBatch); return err },
			}),
			leader.New(leader.Options{
				Name: "usdt-reconciliation", Interval: sweepInterval, LockTTL: 2 * sweepInterval,
				OnTick: func(ctx context.Context) error { _, err := Sweep(ctx, time.Now()); return err },
			}),
		}
	}
	for _, l := range loops {
		l.Start()
	}
}

// Stop ends the loops.
func Stop(ctx context.Context) {
	loopMu.Lock()
	ls := append([]*leader.Loop(nil), loops...)
	loopMu.Unlock()
	for _, l := range ls {
		l.Stop()
	}
}
