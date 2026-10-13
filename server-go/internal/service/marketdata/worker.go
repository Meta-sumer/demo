package marketdata

import (
	"context"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata/providers"
)

// Market data worker (spec section 5).
//
// Exactly one instance owns the provider connection: running two would double
// every tick and distort the latency statistics. The loop keeps the provider's
// subscription list in step with the active assets.

type queuedTick struct {
	tick       providers.Tick
	receivedAt int64
}

var (
	workerMu   sync.Mutex
	connected  bool
	subscribed []string

	// Handlers are registered once per provider instance. (The TS worker adds
	// them on every leadership acquisition, which would double ingestion
	// after a lost-and-regained lock.)
	wired providers.Provider

	// Ticks are handed to a single ingestion goroutine so the provider's
	// reader never blocks on Redis and ticks are processed in arrival order
	// (candle open/close depend on it). receivedAt is stamped on arrival.
	tickQueue     = make(chan queuedTick, 8192)
	ingestStarted sync.Once
)

var marketLoop = leader.New(leader.Options{
	Name:      "market-feed",
	Interval:  5 * time.Second,
	OnAcquire: onAcquire,
	OnRelease: onRelease,
	OnTick:    onTick,
})

// maxIngestBatch bounds one batch: a backlog drains in a few round trips
// without one pipeline growing without limit.
const maxIngestBatch = 512

func startIngestion() {
	ingestStarted.Do(func() {
		go func() {
			batch := make([]queuedTick, 0, maxIngestBatch)
			for q := range tickQueue {
				// Everything that queued up while the previous batch was being
				// written goes into this one: one Redis round trip for all of it.
				batch = append(batch[:0], q)
			drain:
				for len(batch) < maxIngestBatch {
					select {
					case next := <-tickQueue:
						batch = append(batch, next)
					default:
						break drain
					}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				ingestTicks(ctx, batch)
				cancel()
			}
		}()
	})
}

func enqueueTick(t providers.Tick) {
	select {
	case tickQueue <- queuedTick{tick: t, receivedAt: util.NowMs()}:
	default:
		logx.Warn("tick ingestion queue full - dropping tick", "symbol", t.Symbol)
	}
}

func onAcquire(ctx context.Context) error {
	provider, err := providers.GetMarketDataProvider()
	if err != nil {
		return err
	}
	SetProviderName(provider.Name())
	startIngestion()
	workerMu.Lock()
	if wired != provider {
		wired = provider
		provider.OnStatus(RecordProviderStatus)
		provider.OnTick(enqueueTick)
	}
	workerMu.Unlock()
	if err := provider.Connect(ctx); err != nil {
		return err
	}
	workerMu.Lock()
	connected = true
	workerMu.Unlock()
	logx.Info("market feed worker is leader", "provider", provider.Name(), "authoritative", provider.Authoritative())
	return nil
}

func onRelease(ctx context.Context) error {
	provider, err := providers.GetMarketDataProvider()
	if err != nil {
		return err
	}
	FlushOpenCandles(ctx)
	if err := provider.Disconnect(ctx); err != nil {
		return err
	}
	workerMu.Lock()
	connected = false
	subscribed = nil
	workerMu.Unlock()
	return nil
}

func onTick(ctx context.Context) error {
	provider, err := providers.GetMarketDataProvider()
	if err != nil {
		return err
	}
	docs, err := assets.ListAssetDocs(ctx, true)
	if err != nil {
		return err
	}
	var wanted []string
	seen := map[string]bool{}
	for _, a := range docs {
		s := strings.ToLower(a.ProviderSymbol)
		if !seen[s] {
			seen[s] = true
			wanted = append(wanted, s)
		}
	}

	workerMu.Lock()
	had := map[string]bool{}
	for _, s := range subscribed {
		had[s] = true
	}
	var toAdd, toRemove []string
	for _, s := range wanted {
		if !had[s] {
			toAdd = append(toAdd, s)
		}
	}
	for _, s := range subscribed {
		if !seen[s] {
			toRemove = append(toRemove, s)
		}
	}
	workerMu.Unlock()

	if len(toAdd) > 0 {
		if err := provider.Subscribe(ctx, toAdd); err != nil {
			return err
		}
	}
	if len(toRemove) > 0 {
		if err := provider.Unsubscribe(ctx, toRemove); err != nil {
			return err
		}
	}
	if len(toAdd) > 0 || len(toRemove) > 0 {
		logx.Info("market subscriptions updated", "added", toAdd, "removed", toRemove)
	}

	workerMu.Lock()
	subscribed = wanted
	wasConnected := connected
	workerMu.Unlock()

	if !provider.IsConnected() && wasConnected {
		logx.Warn("provider reported disconnected - reconnecting", "provider", provider.Name())
		return provider.Connect(ctx)
	}
	return nil
}

// Start runs the market feed leader loop (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	marketLoop.Start()
}

// Stop releases leadership (disconnecting the provider) and flushes candles.
func Stop(ctx context.Context) {
	marketLoop.Stop()
	FlushOpenCandles(ctx)
}

// IsLeader reports whether this instance owns the provider connection.
func IsLeader() bool { return marketLoop.IsLeader() }
