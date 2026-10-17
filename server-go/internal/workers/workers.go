// Package workers owns the background worker lifecycle.
//
// Before any worker starts, the Redis view of the world is checked against
// MongoDB: exposure counters and the expiry queue are caches, and starting a
// settlement loop against a stale cache is how trades get missed. Both checks
// are safe to run while other instances trade: the exposure book is verified
// and, only if needed, rebuilt by the coordinated (locked, journalled,
// atomically swapped) rebuild; the expiry queue is only ever added to.
package workers

import (
	"context"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/binary"
	"binarytrading/internal/service/expiry"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/hedging"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/settlement"
	"binarytrading/internal/service/wallets"
)

var (
	mu      sync.Mutex
	started bool
)

func enabled() bool { return config.E != nil && config.E.WorkersEnabled }

// Start rebuilds the Redis caches and starts every worker. With
// WORKERS_ENABLED=false the instance serves API traffic only.
func Start(ctx context.Context) error {
	mu.Lock()
	defer mu.Unlock()
	if started {
		return nil
	}
	started = true

	if !enabled() {
		logx.Warn("WORKERS_ENABLED=false - this instance serves API traffic only")
		return nil
	}

	// Verify the exposure book against MongoDB; rebuild it (coordinated with
	// every other instance) only when it is missing or inconsistent.
	if err := exposure.EnsureBookOnBoot(ctx); err != nil {
		return err
	}
	if _, err := RebuildExpiryQueue(ctx); err != nil {
		return err
	}
	// A crash between reserving a stake and writing its trade leaves the
	// customer's funds locked against nothing. Nothing else recovers it.
	if _, err := wallets.ReleaseOrphanedTradeLocks(ctx, time.Now()); err != nil {
		return err
	}

	marketdata.Start()
	// Expiry is leader-elected and fences its queue writes with the leader
	// token; settlement runs on every instance and is made safe by the trade's
	// durable state machine (one claim + intent, idempotent execution), so
	// overlapping leaders, duplicate or lost queue entries and crashes can
	// delay an outcome but never duplicate or change it. See the package
	// comments of service/settlement and service/expiry.
	expiry.Start()
	settlement.Start()
	risk.Start()
	hedging.Start()
	reconciliation.Start()
	exposure.StartReconciler()

	logx.Info("all workers started")
	return nil
}

// RebuildExpiryQueue puts every OPEN/SETTLING trade back on the expiry queue.
//
// It only adds (ZADD is idempotent) and never deletes: deleting and refilling
// the queue while another instance accepts trades drops whatever that
// instance queued in between. Ids of trades that have since finished are
// harmless - the expiry worker drops queue entries that no longer match an
// open trade. One instance at a time (lock); a second booting instance skips
// it rather than repeat the same writes.
func RebuildExpiryQueue(ctx context.Context) (int, error) {
	lock, err := redisx.Acquire(ctx, "expiry-queue-rebuild:"+mongox.DB().Name(), 2*time.Minute)
	if err != nil {
		return 0, err
	}
	if lock == nil {
		logx.Info("expiry queue rebuild already running on another instance")
		return 0, nil
	}
	defer func() { _, _ = redisx.Release(context.WithoutCancel(ctx), lock) }()

	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
		bson.M{"status": bson.M{"$in": binary.LiveStatuses}},
		options.Find().SetProjection(bson.M{"expiryTimestamp": 1}))
	if err != nil {
		return 0, err
	}
	defer cur.Close(ctx)
	n := 0
	pipe := redisx.R().Pipeline()
	flush := func() error {
		if pipe.Len() == 0 {
			return nil
		}
		_, err := pipe.Exec(ctx)
		return err
	}
	for cur.Next(ctx) {
		var t struct {
			ID              bson.ObjectID `bson:"_id"`
			ExpiryTimestamp int64         `bson:"expiryTimestamp"`
		}
		if err := cur.Decode(&t); err != nil {
			return n, err
		}
		pipe.ZAdd(ctx, config.RedisKeys.ExpiryQueue, redis.Z{Score: float64(t.ExpiryTimestamp), Member: t.ID.Hex()})
		n++
		if pipe.Len() >= 1000 {
			if err := flush(); err != nil {
				return n, err
			}
		}
	}
	if err := cur.Err(); err != nil {
		return n, err
	}
	if err := flush(); err != nil {
		return n, err
	}
	logx.Info("expiry queue rebuilt from the trade ledger", "trades", n)
	return n, nil
}

// Stop stops the workers in reverse start order, concurrently (the TS uses
// Promise.allSettled), and waits for all of them.
func Stop(ctx context.Context) {
	mu.Lock()
	defer mu.Unlock()
	if !started {
		return
	}
	started = false
	if !enabled() {
		// Nothing was started; TS still logs the stop.
		logx.Info("all workers stopped")
		return
	}
	stops := []func(context.Context){
		exposure.StopReconciler, reconciliation.Stop, hedging.Stop, risk.Stop, settlement.Stop, expiry.Stop, marketdata.Stop,
	}
	var wg sync.WaitGroup
	for _, stop := range stops {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					logx.Error("worker stop panicked", "panic", rec)
				}
			}()
			stop(ctx)
		}()
	}
	wg.Wait()
	logx.Info("all workers stopped")
}

// WorkerStatus is the /ready "workers" block.
type WorkerStatus struct {
	Enabled          bool `json:"enabled"`
	Started          bool `json:"started"`
	MarketFeedLeader bool `json:"marketFeedLeader"`
}

func Status() WorkerStatus {
	mu.Lock()
	s := started
	mu.Unlock()
	return WorkerStatus{Enabled: enabled(), Started: s, MarketFeedLeader: marketdata.IsLeader()}
}
