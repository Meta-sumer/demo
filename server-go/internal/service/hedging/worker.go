package hedging

import (
	"context"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/assets"
)

// Hedge worker. Leader-elected: two instances hedging the same book would
// double the position. Each tick:
//
//   - refresh working orders and pull missing fills (recovery; also run the
//     moment this instance becomes leader, i.e. after a restart);
//   - evaluate the assets whose exposure just changed (Redis queue from the
//     exposure engine);
//   - every venueRefresh: refresh the venue snapshots (book, mark, funding
//     rate, fee rate, filters) that cost estimates read;
//   - every fullPass: evaluate every hedgeable asset (volatility and time
//     move the delta even when the book does not) and mark positions;
//   - every HEDGE_RECONCILE_INTERVAL_MS: reconcile positions and funding.

var (
	workerMu        sync.Mutex
	hedgeLoop       *leader.Loop
	lastVenue       time.Time
	lastFull        time.Time
	lastReconcile   time.Time
	venueRefreshGap = 3 * time.Second
	fullPassGap     = 15 * time.Second
)

func hedgeIntervalMs() int64 {
	if config.E != nil {
		return config.E.HedgeIntervalMs
	}
	return 1000
}

func reconcileIntervalMs() int64 {
	if config.E != nil && config.E.HedgeReconcileIntervalMs > 0 {
		return config.E.HedgeReconcileIntervalMs
	}
	return 60_000
}

// hedgeableAssets lists active assets with a venue symbol.
func hedgeableAssets(ctx context.Context) (map[string]string, error) {
	docs, err := assets.ListAssetDocs(ctx, true)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, a := range docs {
		if sym := config.E.HedgeVenueSymbol(a.Symbol); sym != "" {
			out[a.ID.Hex()] = sym
			rememberVenueSymbol(sym, a.ID.Hex())
		}
	}
	return out, nil
}

func refreshVenues(ctx context.Context, symbols map[string]string) {
	v := venue()
	done := map[string]bool{}
	for _, sym := range symbols {
		if done[sym] {
			continue
		}
		done[sym] = true
		if snap := RefreshVenueSnapshot(ctx, v, sym); !snap.OK {
			logx.Warn("hedge venue snapshot failed", "symbol", sym, "err", snap.Error)
		}
	}
	lastVenue = time.Now()
}

// RunOnce is one worker tick (exported for tests).
func RunOnce(ctx context.Context) error {
	if _, err := RefreshWorkingOrders(ctx); err != nil {
		return err
	}
	hedgeable, err := hedgeableAssets(ctx)
	if err != nil {
		return err
	}
	if time.Since(lastVenue) >= venueRefreshGap {
		refreshVenues(ctx, hedgeable)
	}
	dirty, order, err := drainDirtyAssets(ctx, 200)
	if err != nil {
		return err
	}
	for _, assetID := range order {
		if _, ok := hedgeable[assetID]; ok {
			evaluateSafely(ctx, assetID)
		}
	}
	if time.Since(lastFull) >= fullPassGap {
		lastFull = time.Now()
		for assetID := range hedgeable {
			if _, seen := dirty[assetID]; !seen {
				evaluateSafely(ctx, assetID)
			}
		}
		if err := MarkHedgePositions(ctx); err != nil {
			return err
		}
	}
	if time.Since(lastReconcile) >= time.Duration(reconcileIntervalMs())*time.Millisecond {
		lastReconcile = time.Now()
		if _, err := ReconcilePositions(ctx); err != nil {
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
	if ev.Order != nil && ev.SkippedReason == "" {
		logx.Info("hedge evaluated", "symbol", ev.Symbol, "status", string(ev.Order.Status))
	}
}

// RecoverOnBoot resolves every order left working by a previous process
// (queried by client order id, never re-sent) and refreshes the venue
// snapshots.
func RecoverOnBoot(ctx context.Context) error {
	n, err := RefreshWorkingOrders(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		logx.Info("hedge orders recovered", "orders", n)
	}
	hedgeable, err := hedgeableAssets(ctx)
	if err != nil {
		return err
	}
	refreshVenues(ctx, hedgeable)
	return nil
}

// Start runs the hedge worker leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	workerMu.Lock()
	defer workerMu.Unlock()
	if hedgeLoop == nil {
		hedgeLoop = leader.New(leader.Options{
			Name:      "hedge",
			Interval:  time.Duration(hedgeIntervalMs()) * time.Millisecond,
			OnTick:    RunOnce,
			OnAcquire: RecoverOnBoot,
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
