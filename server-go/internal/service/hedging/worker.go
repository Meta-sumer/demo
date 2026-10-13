package hedging

import (
	"context"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
)

// Hedge worker (spec section 14).
//
// Two triggers: assets whose exposure just changed (pushed onto a Redis queue
// by the exposure engine) and a periodic full pass, so an asset whose exposure
// is static but whose risk inputs moved - volatility, configuration - is still
// re-evaluated. Leader-elected: two instances hedging the same book would
// double the position.

var (
	workerMu          sync.Mutex
	hedgeLoop         *leader.Loop
	ticksSinceFull    int64
	fullPassEveryTick int64 = 1
)

func hedgeIntervalMs() int64 {
	if config.E != nil {
		return config.E.HedgeIntervalMs
	}
	return 1000
}

func hedgeTick(ctx context.Context) error {
	dirty, order, err := drainDirtyAssets(ctx, 200)
	if err != nil {
		return err
	}
	for _, assetID := range order {
		evaluateSafely(ctx, assetID)
	}

	ticksSinceFull++
	if ticksSinceFull >= fullPassEveryTick {
		ticksSinceFull = 0
		docs, err := assets.ListAssetDocs(ctx, true)
		if err != nil {
			return err
		}
		for _, a := range docs {
			if !a.HedgeEnabled {
				continue
			}
			if _, seen := dirty[a.ID.Hex()]; seen {
				continue
			}
			evaluateSafely(ctx, a.ID.Hex())
		}
		if _, err := ReconcilePendingHedgeOrders(ctx); err != nil {
			return err
		}
		if err := MarkHedgePositions(ctx); err != nil {
			return err
		}
	}
	return nil
}

// drainDirtyAssets collapses the queue into a unique set so one asset is
// evaluated once; order preserves first-seen order.
func drainDirtyAssets(ctx context.Context, limit int) (map[string]struct{}, []string, error) {
	set := map[string]struct{}{}
	order := []string{}
	r := redisx.R()
	for i := 0; i < limit; i++ {
		assetID, err := r.RPop(ctx, config.RedisKeys.HedgeQueue).Result()
		if err != nil {
			if redisx.IsNil(err) {
				break
			}
			return set, order, err
		}
		if assetID == "" {
			break
		}
		if _, ok := set[assetID]; !ok {
			set[assetID] = struct{}{}
			order = append(order, assetID)
		}
	}
	return set, order, nil
}

func evaluateSafely(ctx context.Context, assetID string) {
	ev, err := EvaluateAndHedge(ctx, assetID)
	if err != nil {
		// A venue failure must never stop the loop or affect customer trades.
		logx.Error("hedge evaluation failed", "err", err.Error(), "assetId", assetID)
		return
	}
	if ev.Order != nil {
		logx.Info("hedge evaluated", "symbol", ev.Symbol, "status", string(ev.Order.Status),
			"requested", ev.Order.RequestedNotional, "filled", ev.Order.FilledNotional)
	}
}

// Start runs the hedge worker leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	workerMu.Lock()
	defer workerMu.Unlock()
	if hedgeLoop == nil {
		interval := hedgeIntervalMs()
		fullPassEveryTick = max(1, int64(util.RoundHalfUp(15_000/float64(max(1, interval)))))
		hedgeLoop = leader.New(leader.Options{
			Name:     "hedge",
			Interval: time.Duration(interval) * time.Millisecond,
			OnTick:   hedgeTick,
		})
	}
	hedgeLoop.Start()
}

// Stop ends the loop and releases leadership.
func Stop(ctx context.Context) {
	workerMu.Lock()
	l := hedgeLoop
	workerMu.Unlock()
	if l != nil {
		l.Stop()
	}
}
