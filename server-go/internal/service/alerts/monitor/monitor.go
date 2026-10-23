// Package monitor watches conditions that no request reports and turns them
// into alerts (service/alerts) once they have lasted long enough:
//
//   - the exposure book is not READY (LIVE trades refused) for longer than
//     ALERT_BOOK_UNAVAILABLE_SECONDS (critical);
//   - an active asset inside its trading hours has a STALE (warning) or
//     OFFLINE (critical) feed for longer than ALERT_FEED_STALE_SECONDS;
//   - trades stuck in settlement (SETTLING, or OPEN past expiry, for longer
//     than ALERT_SETTLEMENT_STUCK_MINUTES; critical);
//   - this instance's rate limiter runs on its in-memory fallback (Redis
//     unreachable) for longer than ALERT_RATE_LIMITER_SECONDS (warning).
//
// A condition that clears resolves its alert automatically (and, if it was
// emailed, the recovery is emailed). The first three are cluster-wide and
// checked by the leader only; the rate limiter is per process and checked by
// every worker instance under its own key. The module only reads the
// services it watches.
package monitor

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/alerts"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/reconciliation"
)

// FeedState is one tradable asset's feed health.
type FeedState struct {
	AssetID string
	Symbol  string
	Status  models.FeedStatus
	AgeMs   *int64
}

// Sources are the read-only probes; tests replace them.
type Sources struct {
	BookStatus func(ctx context.Context) (exposure.BookStatus, error)
	Feeds      func(ctx context.Context) ([]FeedState, error)
	Limiter    func() httpx.RateLimiterState
	Stuck      func(ctx context.Context, now time.Time, olderThan time.Duration) (int64, []reconciliation.StuckTrade, error)
}

// DefaultSources reads the live services.
func DefaultSources() Sources {
	return Sources{
		BookStatus: exposure.GetBookStatus,
		Feeds:      tradableFeeds,
		Limiter:    httpx.RateLimiterStatus,
		Stuck: func(ctx context.Context, now time.Time, olderThan time.Duration) (int64, []reconciliation.StuckTrade, error) {
			return reconciliation.FindStuckSettlements(ctx, now, olderThan, 10)
		},
	}
}

// tradableFeeds: active assets inside their trading hours (a market that is
// closed, or an asset switched off, has no ticks by design).
func tradableFeeds(ctx context.Context) ([]FeedState, error) {
	docs, err := assets.ListAssetDocs(ctx, true)
	if err != nil {
		return nil, err
	}
	now := util.NowMs()
	out := []FeedState{}
	for _, a := range docs {
		if !util.IsWithinTradingHours(a.TradingWindows, now) {
			continue
		}
		h, err := marketdata.GetFeedHealth(ctx, a.ID.Hex(), a.Symbol)
		if err != nil {
			return nil, err
		}
		out = append(out, FeedState{AssetID: a.ID.Hex(), Symbol: a.Symbol, Status: h.Status, AgeMs: h.AgeMs})
	}
	return out, nil
}

// Monitor keeps, per condition key, when it was first seen bad.
type Monitor struct {
	src      Sources
	instance string
	mu       sync.Mutex
	since    map[string]time.Time
	// lastStuckCheck spaces the database query for stuck settlements.
	lastStuckCheck time.Time
}

// New returns a monitor over src.
func New(src Sources) *Monitor {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	return &Monitor{src: src, instance: host, since: map[string]time.Time{}}
}

const (
	keyBook    = "exposure:book"
	keyStuck   = "settlement:stuck"
	feedPrefix = "feed:"
)

// observe records whether key is bad at now and reports whether it has been
// bad for at least after.
func (m *Monitor) observe(key string, bad bool, now time.Time, after time.Duration) (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !bad {
		delete(m.since, key)
		return false, time.Time{}
	}
	first, ok := m.since[key]
	if !ok {
		m.since[key] = now
		first = now
	}
	return now.Sub(first) >= after, first
}

// CheckCluster runs the cluster-wide checks (leader only).
func (m *Monitor) CheckCluster(ctx context.Context, now time.Time) {
	s := config.Alerts()
	open, err := alerts.OpenKeys(ctx, models.AlertSourceExposure, models.AlertSourceFeed, models.AlertSourceSettlement)
	if err != nil {
		logx.Warn("alert monitor: cannot read open alerts", "err", err.Error())
		open = map[string]bool{}
	}
	resolve := func(key, note string) {
		if open[key] {
			alerts.ResolveByKey(ctx, key, note)
		}
	}

	// Exposure book.
	st, berr := m.src.BookStatus(ctx)
	bad := berr != nil || st.Status != exposure.BookReady
	if fire, first := m.observe(keyBook, bad, now, time.Duration(s.BookUnavailableSeconds)*time.Second); fire {
		detail := fmt.Sprintf("Book status %s since %s UTC (%s). LIVE trades are refused with EXPOSURE_BOOK_UNAVAILABLE; DEMO trading continues.",
			st.Status, first.UTC().Format(time.RFC3339), since(now, first))
		if berr != nil {
			detail = "The book state cannot be read (Redis): " + berr.Error() + ". LIVE trades are refused."
		} else if st.Reason != "" {
			detail += "\nReason: " + st.Reason
		}
		alerts.Raise(ctx, alerts.Alert{Code: "exposure.book_unavailable", Severity: config.AlertSeverityCritical,
			Source: models.AlertSourceExposure, DedupKey: keyBook, Title: "Exposure book not READY - LIVE trading refused",
			Detail: detail + "\nRunbook: production-readiness.md section 6.3."})
	} else if !bad {
		resolve(keyBook, "the exposure book is READY again")
	}

	// Market feed, per tradable asset.
	feeds, ferr := m.src.Feeds(ctx)
	if ferr != nil {
		logx.Warn("alert monitor: cannot read feed health", "err", ferr.Error())
	} else {
		seen := map[string]bool{}
		for _, f := range feeds {
			key := feedPrefix + f.AssetID
			seen[key] = true
			badFeed := f.Status == models.FeedStatusStale || f.Status == models.FeedStatusOffline
			fire, first := m.observe(key, badFeed, now, time.Duration(s.FeedStaleSeconds)*time.Second)
			switch {
			case fire:
				sev := config.AlertSeverityWarning
				if f.Status == models.FeedStatusOffline {
					sev = config.AlertSeverityCritical
				}
				age := "no tick at all"
				if f.AgeMs != nil {
					age = fmt.Sprintf("last tick %.1f s ago", float64(*f.AgeMs)/1000)
				}
				alerts.Raise(ctx, alerts.Alert{Code: "feed." + strings.ToLower(string(f.Status)), Severity: sev,
					Source: models.AlertSourceFeed, DedupKey: key, Title: "Market feed " + string(f.Status) + ": " + f.Symbol,
					Detail: fmt.Sprintf("%s feed has been %s or worse since %s UTC (%s); %s. New trades on it are refused (FEED_UNHEALTHY).\nRunbook: production-readiness.md section 6.2.",
						f.Symbol, f.Status, first.UTC().Format(time.RFC3339), since(now, first), age)})
			case !badFeed:
				resolve(key, f.Symbol+" feed is healthy again")
			}
		}
		// Assets no longer tradable (switched off, market closed) clear.
		for key := range open {
			if strings.HasPrefix(key, feedPrefix) && !seen[key] {
				m.observe(key, false, now, 0)
				alerts.ResolveByKey(ctx, key, "asset no longer tradable (inactive or outside its trading hours)")
			}
		}
	}

	// Per-instance alerts that are no longer re-raised.
	alerts.ResolveStale(ctx, models.AlertSourceRateLimiter, now.Add(-staleLocalAfter()),
		"not reported for a while: Redis is back for that instance, or the instance was replaced")

	// Stuck settlements: a database query, so at most once a minute.
	if m.lastStuckCheck.IsZero() || now.Sub(m.lastStuckCheck) >= time.Minute {
		m.lastStuckCheck = now
		after := time.Duration(s.SettlementStuckMinutes) * time.Minute
		n, sample, err := m.src.Stuck(ctx, now, after)
		switch {
		case err != nil:
			logx.Warn("alert monitor: cannot check stuck settlements", "err", err.Error())
		case n > 0:
			lines := make([]string, 0, len(sample))
			for _, t := range sample {
				lines = append(lines, fmt.Sprintf("%s %s %s %s expiry %s updated %s", t.TradeID, t.Reference, t.Symbol, t.Status,
					time.UnixMilli(t.ExpiryTimestamp).UTC().Format(time.RFC3339), t.UpdatedAt))
			}
			alerts.Raise(ctx, alerts.Alert{Code: "settlement.stuck", Severity: config.AlertSeverityCritical,
				Source: models.AlertSourceSettlement, DedupKey: keyStuck,
				Title: fmt.Sprintf("%d trade(s) not settled %d+ minutes after expiry", n, s.SettlementStuckMinutes),
				Detail: "Trades still SETTLING, or OPEN past expiry, for longer than the threshold (oldest first):\n" +
					strings.Join(lines, "\n") + "\nRunbook: production-readiness.md section 6.10."})
		default:
			resolve(keyStuck, "no trade is stuck in settlement any more")
		}
	}
}

// CheckLocal runs the per-process checks (every worker instance). It only
// raises: while the condition lasts it re-raises every interval, and the
// leader resolves an instance's alert once it has not been seen for a while
// (staleLocalAfter), which also covers an instance that was replaced.
func (m *Monitor) CheckLocal(ctx context.Context, now time.Time) {
	s := config.Alerts()
	key := "rate-limiter:" + m.instance
	st := m.src.Limiter()
	if fire, first := m.observe(key, st.Degraded, now, time.Duration(s.RateLimiterSeconds)*time.Second); fire {
		alerts.Raise(ctx, alerts.Alert{Code: "ratelimit.memory_fallback", Severity: config.AlertSeverityWarning,
			Source: models.AlertSourceRateLimiter, DedupKey: key, Title: "Rate limiter on in-memory fallback (" + m.instance + ")",
			Detail: fmt.Sprintf("Redis has been unusable for rate limiting since %s UTC (%s); limits are enforced per instance in memory (%d requests so far). Check Redis.",
				first.UTC().Format(time.RFC3339), since(now, first), st.FallbackRequests)})
	}
}

// staleLocalAfter: a per-instance alert not re-raised for this long has
// cleared (or its instance is gone).
func staleLocalAfter() time.Duration {
	return 3*time.Duration(config.Alerts().MonitorIntervalSeconds)*time.Second + 30*time.Second
}

func since(now, first time.Time) string {
	d := now.Sub(first).Round(time.Second)
	return d.String()
}

// ── loops ───────────────────────────────────────────────────────────────

var (
	loopMu    sync.Mutex
	clusterLp *leader.Loop
	localStop context.CancelFunc
	localDone chan struct{}
	shared    *Monitor
)

// Start runs the monitor (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	loopMu.Lock()
	defer loopMu.Unlock()
	if shared == nil {
		shared = New(DefaultSources())
	}
	interval := time.Duration(config.Alerts().MonitorIntervalSeconds) * time.Second
	if clusterLp == nil {
		clusterLp = leader.New(leader.Options{
			Name:     "alert-monitor",
			Interval: interval,
			LockTTL:  time.Minute,
			OnTick: func(ctx context.Context) error {
				shared.CheckCluster(ctx, time.Now())
				return nil
			},
			// A new leader starts its timers from zero.
			OnAcquire: func(ctx context.Context) error {
				shared.mu.Lock()
				for k := range shared.since {
					if !strings.HasPrefix(k, "rate-limiter:") {
						delete(shared.since, k)
					}
				}
				shared.lastStuckCheck = time.Time{}
				shared.mu.Unlock()
				return nil
			},
		})
	}
	clusterLp.Start()
	if localStop == nil {
		ctx, cancel := context.WithCancel(context.Background())
		localStop, localDone = cancel, make(chan struct{})
		go func() {
			defer close(localDone)
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case now := <-t.C:
					func() {
						defer func() {
							if r := recover(); r != nil {
								logx.Error("alert monitor panicked", "panic", r)
							}
						}()
						cctx, cancel := context.WithTimeout(ctx, interval)
						defer cancel()
						shared.CheckLocal(cctx, now)
					}()
				}
			}
		}()
	}
}

// Stop ends both loops.
func Stop(ctx context.Context) {
	loopMu.Lock()
	lp, stop, done := clusterLp, localStop, localDone
	localStop = nil
	loopMu.Unlock()
	if lp != nil {
		lp.Stop()
	}
	if stop != nil {
		stop()
		<-done
	}
}

// OpenConditions lists the condition keys currently bad (for the admin
// status view), sorted.
func OpenConditions() []string {
	loopMu.Lock()
	m := shared
	loopMu.Unlock()
	if m == nil {
		return []string{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for k := range m.since {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
