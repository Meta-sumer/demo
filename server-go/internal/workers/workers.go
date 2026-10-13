// Package workers owns the background worker lifecycle.
//
// Before any worker starts, the Redis view of the world is rebuilt from
// MongoDB: exposure counters and the expiry queue are caches, and starting a
// settlement loop against a stale cache is how trades get missed.
package workers

import (
	"context"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
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

	if _, err := exposure.RebuildExposureFromDatabase(ctx); err != nil {
		return err
	}
	if _, err := binary.RebuildExpiryQueue(ctx); err != nil {
		return err
	}
	// A crash between reserving a stake and writing its trade leaves the
	// customer's funds locked against nothing. Nothing else recovers it.
	if _, err := wallets.ReleaseOrphanedTradeLocks(ctx, time.Now()); err != nil {
		return err
	}

	marketdata.Start()
	expiry.Start()
	settlement.Start()
	risk.Start()
	hedging.Start()
	reconciliation.Start()

	logx.Info("all workers started")
	return nil
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
		reconciliation.Stop, hedging.Stop, risk.Stop, settlement.Stop, expiry.Stop, marketdata.Stop,
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
