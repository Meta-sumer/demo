package reconciliation

import (
	"context"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
)

// Periodic ledger reconciliation (spec section 42).

var (
	workerMu sync.Mutex
	recLoop  *leader.Loop
)

func tick(ctx context.Context) error {
	run, err := RunReconciliation(ctx, 0)
	if err != nil {
		return err
	}
	if run.Status != "OK" {
		logx.Error("RECONCILIATION_ALERT", "reference", run.Reference, "alerts", run.Alerts)
	}
	return nil
}

// Start runs the reconciliation leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	workerMu.Lock()
	defer workerMu.Unlock()
	if recLoop == nil {
		interval := int64(300_000)
		if config.E != nil {
			interval = config.E.ReconciliationIntervalMs
		}
		recLoop = leader.New(leader.Options{
			Name:     "reconciliation",
			Interval: time.Duration(interval) * time.Millisecond,
			LockTTL:  time.Duration(max(60_000, interval)) * time.Millisecond,
			OnTick:   tick,
		})
	}
	recLoop.Start()
}

// Stop ends the loop and releases leadership.
func Stop(ctx context.Context) {
	workerMu.Lock()
	l := recLoop
	workerMu.Unlock()
	if l != nil {
		l.Stop()
	}
}
