package wallets

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// Orphaned stake locks.
//
// Trade acceptance reserves the stake before it writes the trade, because
// nothing that could pay out should exist until the money behind it is held.
// If the process dies between the two, the customer's wallet carries a lock for
// a trade that was never created, and nothing releases it.
//
// An orphan is unambiguous - a TRADE_LOCK whose trade document does not exist
// at all - so releasing it can only ever return the customer their own money.
// It is never a way to unwind a trade that exists.
//
// The sweep goes through every TRADE_LOCK row (one aggregation cursor, read in
// batches, no cap): a lock whose release row (trade:settle:<id>) exists is
// done; of the rest, those whose trade is OPEN or SETTLING are live; what is
// left either has no trade (an orphan, released here) or has a finished
// trade (reported, never released here - cancel repair and reconciliation
// own those). It runs on the leader of the "stake-lock-orphans" loop
// (orphan_worker.go), fenced per batch.

const (
	// orphanGrace is the whole safety argument: a trade being created right now
	// is momentarily in exactly the state an orphan is in, so the sweep must not
	// be able to see it. A minute is far longer than acceptance takes.
	orphanGrace = 60 * time.Second
	// orphanBatch is how many unreleased locks are handled between two fence
	// checks.
	orphanBatch = 500
)

// OrphanGrace is the age below which a stake lock is never treated as an
// orphan (exported for the reconciliation check).
const OrphanGrace = orphanGrace

// OrphanSweepResult reports one sweep.
type OrphanSweepResult struct {
	// Unreleased stake locks whose trade is not open (orphans + finished).
	Scanned       int64 `json:"scanned"`
	Released      int64 `json:"released"`
	ReleasedMinor int64 `json:"releasedMinor"`
	Failed        int64 `json:"failed"`
	// Unreleased locks whose trade exists but is finished: not this sweep's
	// to release (reconciliation reports them).
	Finished int64 `json:"finished"`
	Batches  int64 `json:"batches"`
}

// UnreleasedStakeLock is a TRADE_LOCK row with no release whose trade is not
// OPEN or SETTLING.
type UnreleasedStakeLock struct {
	TransactionID bson.ObjectID `bson:"_id"`
	UserID        bson.ObjectID `bson:"userId"`
	AccountType   string        `bson:"accountType"`
	Currency      string        `bson:"currency"`
	Amount        int64         `bson:"amount"`
	TradeID       string        `bson:"referenceId"`
	CreatedAt     time.Time     `bson:"createdAt"`
	// "" when the trade does not exist (an orphan), else its status.
	TradeStatus string `bson:"tradeStatus"`
}

// Orphan reports whether the lock's trade does not exist.
func (l UnreleasedStakeLock) Orphan() bool { return l.TradeStatus == "" }

// ScanUnreleasedStakeLocks streams every TRADE_LOCK row older than cutoff
// that has no release row (trade:settle:<id>) and whose trade is not OPEN or
// SETTLING, handing them to fn in batches of up to batch. from zero examines
// every lock row (no cap, no blocking sort); otherwise only those created at
// or after from (the createdAt index). The release and the trade are looked
// up by index (referenceId, _id) inside one aggregation, so only the few
// unreleased locks reach the trade lookup.
func ScanUnreleasedStakeLocks(ctx context.Context, from, cutoff time.Time, batch int, fn func([]UnreleasedStakeLock) error) error {
	if batch <= 0 {
		batch = orphanBatch
	}
	created := bson.M{"$lt": cutoff}
	if !from.IsZero() {
		created["$gte"] = from
	}
	pipeline := bson.A{
		bson.M{"$match": bson.M{
			"type": models.WalletTxTradeLock, "referenceType": refBinaryTrade,
			"referenceId": bson.M{"$type": "string"}, "createdAt": created,
		}},
		// The trade's other ledger rows (its release, if any) by referenceId.
		bson.M{"$lookup": bson.M{
			"from": models.CollWalletTransactions, "localField": "referenceId", "foreignField": "referenceId", "as": "rel",
			"pipeline": bson.A{
				bson.M{"$match": bson.M{"referenceType": refBinaryTrade}},
				bson.M{"$project": bson.M{"_id": 0, "idempotencyKey": 1}},
			},
		}},
		bson.M{"$match": bson.M{"$expr": bson.M{"$not": bson.A{bson.M{"$in": bson.A{
			bson.M{"$concat": bson.A{"trade:settle:", "$referenceId"}}, bson.M{"$ifNull": bson.A{"$rel.idempotencyKey", bson.A{}}},
		}}}}}},
		bson.M{"$addFields": bson.M{"tradeOid": bson.M{"$convert": bson.M{
			"input": "$referenceId", "to": "objectId", "onError": nil, "onNull": nil,
		}}}},
		bson.M{"$lookup": bson.M{
			"from": models.CollBinaryTrades, "localField": "tradeOid", "foreignField": "_id", "as": "trade",
			"pipeline": bson.A{bson.M{"$project": bson.M{"_id": 0, "status": 1}}},
		}},
		bson.M{"$addFields": bson.M{"tradeStatus": bson.M{"$ifNull": bson.A{bson.M{"$first": "$trade.status"}, ""}}}},
		bson.M{"$match": bson.M{"tradeStatus": bson.M{"$nin": bson.A{
			string(models.TradeStatusOpen), string(models.TradeStatusSettling),
		}}}},
		bson.M{"$project": bson.M{"userId": 1, "accountType": 1, "currency": 1, "amount": 1, "referenceId": 1,
			"createdAt": 1, "tradeStatus": 1}},
	}
	opts := options.Aggregate().SetBatchSize(int32(batch))
	if !from.IsZero() {
		// A recent window reads only its own rows (index {createdAt: -1}).
		opts.SetHint(bson.D{{Key: "createdAt", Value: -1}})
	}
	cur, err := mongox.C(models.CollWalletTransactions).Aggregate(ctx, pipeline, opts)
	if err != nil {
		return err
	}
	defer cur.Close(context.WithoutCancel(ctx))
	page := make([]UnreleasedStakeLock, 0, batch)
	for cur.Next(ctx) {
		var l UnreleasedStakeLock
		if err := cur.Decode(&l); err != nil {
			return err
		}
		page = append(page, l)
		if len(page) == batch {
			if err := fn(page); err != nil {
				return err
			}
			page = make([]UnreleasedStakeLock, 0, batch)
		}
	}
	if err := cur.Err(); err != nil {
		return err
	}
	if len(page) > 0 {
		return fn(page)
	}
	return nil
}

// ReleaseOrphanedTradeLocks releases every stake lock whose trade was never
// written, examining every lock. now is the reference time (zero =
// time.Now()). Inside a leader loop each batch first checks the loop's fence,
// so a former leader stops.
func ReleaseOrphanedTradeLocks(ctx context.Context, now time.Time) (OrphanSweepResult, error) {
	return releaseOrphanedTradeLocks(ctx, time.Time{}, now)
}

// releaseOrphanedTradeLocks: from zero = every lock, else only locks created
// at or after from.
func releaseOrphanedTradeLocks(ctx context.Context, from, now time.Time) (OrphanSweepResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	res := OrphanSweepResult{}
	err := ScanUnreleasedStakeLocks(ctx, from, now.Add(-orphanGrace), orphanBatch, func(locks []UnreleasedStakeLock) error {
		res.Batches++
		if f := leader.FenceFromContext(ctx); f != nil {
			if err := f.Check(ctx); err != nil {
				return err
			}
		}
		for _, lock := range locks {
			res.Scanned++
			if !lock.Orphan() {
				res.Finished++
				continue
			}
			// Shares the 'trade:settle:<id>' idempotency key with settlement, so a
			// repeat pass - or a racing settlement - can only apply once.
			mr, err := ReleaseTradeFunds(ctx, TradeFundsInput{
				UserID:      lock.UserID.Hex(),
				AccountType: lock.AccountType,
				Currency:    lock.Currency,
				Stake:       lock.Amount,
				TradeID:     lock.TradeID,
			}, ReleaseCancelled)
			if err != nil {
				res.Failed++
				logx.Error("failed to release an orphaned stake lock", "err", err.Error(), "tradeId", lock.TradeID)
				continue
			}
			if mr.Replayed {
				continue // released meanwhile by another path
			}
			res.Released++
			res.ReleasedMinor += lock.Amount
			logx.Warn("released a stake lock whose trade was never created",
				"tradeId", lock.TradeID, "userId", lock.UserID.Hex(), "amount", lock.Amount)
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	if res.Scanned > 0 {
		logx.Info("orphaned stake lock sweep complete", "scanned", res.Scanned, "released", res.Released,
			"releasedMinor", res.ReleasedMinor, "failed", res.Failed, "finished", res.Finished)
	}
	return res, nil
}
