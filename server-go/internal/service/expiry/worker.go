package expiry

import (
	"context"
	"math"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/util"
)

// Expiry worker (spec section 17).
//
// Runs on a short interval - by default every 100ms - because expiry detection
// latency is part of what the platform is measured on. Trades that are due are
// handed to the settlement queue; nothing is settled here.

var (
	expiryLoop      *leader.Loop
	ticksSinceSweep int
	// Run the database safety-net sweep roughly every 30 seconds.
	sweepEveryTicks int
)

func tickMs() int64 {
	if config.E != nil && config.E.ExpiryTickMs > 0 {
		return config.E.ExpiryTickMs
	}
	return 100
}

func newLoop() *leader.Loop {
	interval := tickMs()
	sweepEveryTicks = int(math.Max(1, util.RoundHalfUp(30_000/math.Max(1, float64(interval)))))
	return leader.New(leader.Options{
		Name:     "expiry",
		Interval: time.Duration(interval) * time.Millisecond,
		LockTTL:  15 * time.Second,
		OnTick:   tick,
	})
}

func tick(ctx context.Context) error {
	result, err := ProcessExpiries(ctx, util.NowMs(), DefaultDueLimit)
	if err != nil {
		return err
	}
	if result.Queued > 0 {
		logx.Debug("expiries queued for settlement", "due", result.Due, "queued", result.Queued, "waiting", result.Waiting)
	}

	ticksSinceSweep++
	if ticksSinceSweep >= sweepEveryTicks {
		ticksSinceSweep = 0
		if _, err := SweepMissedExpiries(ctx, util.NowMs(), DefaultSweepLimit); err != nil {
			return err
		}
	}
	return nil
}

// Start runs the expiry leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	if expiryLoop == nil {
		expiryLoop = newLoop()
	}
	expiryLoop.Start()
}

// Stop ends the loop and releases leadership.
func Stop(ctx context.Context) {
	if expiryLoop != nil {
		expiryLoop.Stop()
	}
}

// IsLeader reports whether this instance runs the expiry loop.
func IsLeader() bool { return expiryLoop != nil && expiryLoop.IsLeader() }
