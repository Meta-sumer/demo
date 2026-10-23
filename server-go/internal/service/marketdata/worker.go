package marketdata

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
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
//
// Failover. The feed leadership is short-lived on purpose: it is renewed
// every feedLeaderInterval and lapses feedLeaderTTL after the last renewal,
// and followers try to take it every feedLeaderInterval, connecting and
// subscribing in the same step. A crashed leader therefore leaves the feed
// silent for about feedLeaderTTL + feedLeaderInterval (about 3 s), instead of
// the 25 s a 5 s interval gave - and trades expiring in a gap are voided when
// no price lies within the settlement tolerance (2 s) of their expiry.
//
// One consistent tick history, one writer: a batch is ingested only while
// this process holds a valid lease on the feed (feedLeaseValid, a local check
// against the lease the loop last renewed, which ends before the Redis lock
// can be taken by anyone else), so a paused or partitioned former leader
// stops writing before a new leader can start. A new leader also drops its
// in-process normaliser state on taking over, so it continues from the last
// accepted quote and volatility state in Redis (written by the previous
// leader): the out-of-order check rejects anything not newer than the last
// stored quote, and no EWMA observation is counted twice.

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

// Feed leadership timing (see the package comment above).
const (
	feedLeaderInterval = 500 * time.Millisecond
	feedLeaderTTL      = 2500 * time.Millisecond
)

var marketLoop = leader.New(leader.Options{
	Name:      "market-feed",
	Interval:  feedLeaderInterval,
	LockTTL:   feedLeaderTTL,
	OnAcquire: onAcquire,
	OnRelease: onRelease,
	OnTick:    onTick,
})

// feedLeaseUntil is the end (epoch ms) of the feed lease this process last
// renewed, 0 when it does not lead. Kept here rather than read from
// marketLoop, which would be an initialisation cycle.
var feedLeaseUntil atomic.Int64

func noteFeedLease(ctx context.Context) {
	if f := leader.FenceFromContext(ctx); f != nil {
		feedLeaseUntil.Store(f.LeaseUntil.UnixMilli())
	}
}

// feedLeaseValid: this process leads the feed and its lease has not run out.
func feedLeaseValid() bool {
	return feedLeader.Load() && util.NowMs() < feedLeaseUntil.Load()
}

// maxIngestBatch bounds one batch: a backlog drains in a few round trips
// without one pipeline growing without limit.
const maxIngestBatch = 512

func startIngestion() {
	startFeedQuality()
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
				if !feedLeaseValid() {
					// Not (or no longer surely) the feed leader: another process may
					// lead now, and a second writer would fork the tick history.
					logx.Warn("dropping ticks received without a valid feed lease", "ticks", len(batch))
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				ingestTicks(ctx, batch)
				cancel()
			}
		}()
	})
}

func enqueueTick(t providers.Tick) { enqueueInto(tickQueue, t) }

// enqueueInto hands a tick to the ingestion goroutine. A full buffer drops
// the tick: the provider's reader must never block. Every drop is counted
// (feed data rules: the asset's feed is STALE for feedSaturationHoldMs after
// it, and an alert is raised).
func enqueueInto(q chan queuedTick, t providers.Tick) {
	now := util.NowMs()
	select {
	case q <- queuedTick{tick: t, receivedAt: now}:
		if depth := len(q); depth > cap(q)/2 {
			noteQueueDepth(depth)
		}
	default:
		recordDrop(t.Symbol, now, len(q))
		logx.Warn("tick ingestion queue full - dropping tick", "symbol", t.Symbol)
	}
}

func onAcquire(ctx context.Context) error {
	noteFeedLease(ctx)
	// Continue from the state the previous leader left in Redis, never from
	// what this process held the last time it led.
	ResetNormalizationState()
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
	// Leader from here on: the first ticks after the connection are ingested.
	feedLeader.Store(true)
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
	feedLeader.Store(false)
	feedLeaseUntil.Store(0)
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
	noteFeedLease(ctx)
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
