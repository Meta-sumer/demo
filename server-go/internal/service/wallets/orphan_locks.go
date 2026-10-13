package wallets

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
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

const (
	// orphanGrace is the whole safety argument: a trade being created right now
	// is momentarily in exactly the state an orphan is in, so the sweep must not
	// be able to see it. A minute is far longer than acceptance takes.
	orphanGrace = 60 * time.Second
	// orphanMaxPerRun bounds one pass, so a large backlog cannot stall boot.
	orphanMaxPerRun = 500
)

// OrphanSweepResult reports one sweep.
type OrphanSweepResult struct {
	Scanned       int64 `json:"scanned"`
	Released      int64 `json:"released"`
	ReleasedMinor int64 `json:"releasedMinor"`
	Failed        int64 `json:"failed"`
}

// ReleaseOrphanedTradeLocks releases stake locks whose trade was never
// written. now is the reference time (zero = time.Now()).
func ReleaseOrphanedTradeLocks(ctx context.Context, now time.Time) (OrphanSweepResult, error) {
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-orphanGrace)

	ledger := mongox.C(models.CollWalletTransactions)
	cur, err := ledger.Find(ctx, bson.M{
		"type":          models.WalletTxTradeLock,
		"referenceType": refBinaryTrade,
		"referenceId":   bson.M{"$ne": nil},
		"createdAt":     bson.M{"$lt": cutoff},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(orphanMaxPerRun))
	if err != nil {
		return OrphanSweepResult{}, err
	}
	var locks []models.WalletTransaction
	if err := cur.All(ctx, &locks); err != nil {
		return OrphanSweepResult{}, err
	}
	if len(locks) == 0 {
		return OrphanSweepResult{}, nil
	}
	scanned := int64(len(locks))

	// One query for the whole batch rather than one per lock.
	seen := map[string]bool{}
	objectIDs := bson.A{}
	for _, l := range locks {
		if l.ReferenceID == nil || *l.ReferenceID == "" || seen[*l.ReferenceID] {
			continue
		}
		seen[*l.ReferenceID] = true
		if id, ok := mongox.ParseID(*l.ReferenceID); ok {
			objectIDs = append(objectIDs, id)
		}
	}
	existingIDs := map[string]bool{}
	if len(objectIDs) > 0 {
		tcur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{"_id": bson.M{"$in": objectIDs}},
			options.Find().SetProjection(bson.M{"_id": 1}))
		if err != nil {
			return OrphanSweepResult{}, err
		}
		var trades []struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := tcur.All(ctx, &trades); err != nil {
			return OrphanSweepResult{}, err
		}
		for _, t := range trades {
			existingIDs[t.ID.Hex()] = true
		}
	}

	var candidates []models.WalletTransaction
	for _, l := range locks {
		if l.ReferenceID != nil && *l.ReferenceID != "" && !existingIDs[*l.ReferenceID] {
			candidates = append(candidates, l)
		}
	}
	if len(candidates) == 0 {
		return OrphanSweepResult{Scanned: scanned}, nil
	}

	// The lock row is never deleted - the ledger is append-only - so a lock a
	// previous pass already released still looks like an orphan. The release
	// itself is idempotent, but counting it again would report work that did
	// not happen. The settle key is the marker that it did.
	settleKeys := bson.A{}
	for _, l := range candidates {
		settleKeys = append(settleKeys, "trade:settle:"+*l.ReferenceID)
	}
	rcur, err := ledger.Find(ctx, bson.M{"idempotencyKey": bson.M{"$in": settleKeys}},
		options.Find().SetProjection(bson.M{"idempotencyKey": 1}))
	if err != nil {
		return OrphanSweepResult{}, err
	}
	var released []struct {
		IdempotencyKey *string `bson:"idempotencyKey"`
	}
	if err := rcur.All(ctx, &released); err != nil {
		return OrphanSweepResult{}, err
	}
	releasedKeys := map[string]bool{}
	for _, r := range released {
		if r.IdempotencyKey != nil {
			releasedKeys[*r.IdempotencyKey] = true
		}
	}

	var orphans []models.WalletTransaction
	for _, l := range candidates {
		if !releasedKeys["trade:settle:"+*l.ReferenceID] {
			orphans = append(orphans, l)
		}
	}
	if len(orphans) == 0 {
		return OrphanSweepResult{Scanned: scanned}, nil
	}

	res := OrphanSweepResult{Scanned: scanned}
	for _, lock := range orphans {
		// Shares the 'trade:settle:<id>' idempotency key with settlement, so a
		// repeat pass - or a racing settlement - can only apply once.
		_, err := ReleaseTradeFunds(ctx, TradeFundsInput{
			UserID:      lock.UserID.Hex(),
			AccountType: lock.AccountType,
			Currency:    lock.Currency,
			Stake:       lock.Amount,
			TradeID:     *lock.ReferenceID,
		}, ReleaseCancelled)
		if err != nil {
			res.Failed++
			logx.Error("failed to release an orphaned stake lock", "err", err.Error(), "tradeId", *lock.ReferenceID)
			continue
		}
		res.Released++
		res.ReleasedMinor += lock.Amount
		logx.Warn("released a stake lock whose trade was never created",
			"tradeId", *lock.ReferenceID, "userId", lock.UserID.Hex(), "amount", lock.Amount)
	}

	logx.Info("orphaned stake lock sweep complete",
		"scanned", res.Scanned, "released", res.Released, "releasedMinor", res.ReleasedMinor, "failed", res.Failed)
	return res, nil
}
