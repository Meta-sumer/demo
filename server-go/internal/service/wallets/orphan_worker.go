package wallets

import (
	"context"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/leader"
)

// The orphaned-stake-lock sweep runs on one instance (leader-elected). Every
// OrphanSweepInterval it examines the locks of the last OrphanRecentWindow
// (the createdAt index: cheap), so an orphan is released about a minute after
// the crash that made it; when an instance takes the lead, and every
// orphanFullEvery-th tick, it examines every lock. Each batch checks the
// loop's fence and every release is keyed (trade:settle:<id>), so a former
// leader overlapping the new one can only repeat a no-op.
var (
	OrphanSweepInterval = 15 * time.Second
	OrphanRecentWindow  = 30 * time.Minute

	orphanMu   sync.Mutex
	orphanLoop *leader.Loop
	orphanTick int
)

// orphanFullEvery: a full pass every 120 ticks (30 minutes).
const orphanFullEvery = 120

func orphanSweepTick(ctx context.Context) error {
	now := time.Now()
	from := now.Add(-OrphanRecentWindow)
	if orphanTick%orphanFullEvery == 0 {
		from = time.Time{}
	}
	orphanTick++
	_, err := releaseOrphanedTradeLocks(ctx, from, now)
	return err
}

// StartOrphanLockSweeper starts the leader loop (no-op with
// WORKERS_ENABLED=false).
func StartOrphanLockSweeper() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	orphanMu.Lock()
	defer orphanMu.Unlock()
	if orphanLoop == nil {
		orphanLoop = leader.New(leader.Options{
			Name:     "stake-lock-orphans",
			Interval: OrphanSweepInterval,
			// A full pass on a long ledger can take a while; a crashed leader
			// is still replaced within a minute.
			LockTTL:          3 * OrphanSweepInterval,
			BoundTickToLease: true,
			// The first tick after taking the lead is a full pass.
			// (OnAcquire and OnTick run on the loop's goroutine, never at once.)
			OnAcquire: func(context.Context) error {
				orphanTick = 0
				return nil
			},
			OnTick: orphanSweepTick,
		})
	}
	orphanLoop.Start()
}

// StopOrphanLockSweeper stops the loop and releases leadership.
func StopOrphanLockSweeper(ctx context.Context) {
	orphanMu.Lock()
	l := orphanLoop
	orphanMu.Unlock()
	if l != nil {
		l.Stop()
	}
}
