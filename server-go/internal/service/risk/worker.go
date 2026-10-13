package risk

import (
	"context"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/service/exposure"
)

// Risk worker (spec section 13).
//
// Re-evaluates every asset on a fixed cadence, pushes the result to the admin
// risk channel and writes the exposure snapshot through to MongoDB, so the
// durable record never lags the cache by more than one interval.
var riskLoop *leader.Loop

func newLoop() *leader.Loop {
	interval := int64(1000)
	if config.E != nil {
		interval = config.E.RiskIntervalMs
	}
	return leader.New(leader.Options{
		Name:     "risk",
		Interval: time.Duration(interval) * time.Millisecond,
		OnTick:   tick,
	})
}

func tick(ctx context.Context) error {
	assessments, err := AssessAllAssets(ctx)
	if err != nil {
		return err
	}
	hedged, err := GetHedgedNotionalByAsset(ctx)
	if err != nil {
		return err
	}
	if err := exposure.PersistAllExposures(ctx, hedged); err != nil {
		return err
	}

	critical := []string{}
	for _, a := range assessments {
		if a.Level == models.RiskLevelCritical {
			critical = append(critical, a.Symbol)
		}
	}
	if len(critical) > 0 {
		logx.Warn("assets at CRITICAL risk level", "assets", critical)
	}
	return nil
}

// Start runs the risk worker leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	if riskLoop == nil {
		riskLoop = newLoop()
	}
	riskLoop.Start()
}

// Stop ends the loop and releases leadership.
func Stop(ctx context.Context) {
	if riskLoop != nil {
		riskLoop.Stop()
	}
}

// IsLeader reports whether this instance runs the risk loop.
func IsLeader() bool { return riskLoop != nil && riskLoop.IsLeader() }
