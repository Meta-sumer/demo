package monitor

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/testsupport"
)

// The monitor with fake probes against a throwaway MongoDB database: a
// condition alerts only after its threshold, clears itself, and the
// per-instance limiter alert is resolved by the leader once stale.

type fake struct {
	mu      sync.Mutex
	book    exposure.BookStatus
	bookErr error
	feeds   []FeedState
	limiter httpx.RateLimiterState
	stuck   int64
}

func (f *fake) sources() Sources {
	return Sources{
		BookStatus: func(context.Context) (exposure.BookStatus, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.book, f.bookErr
		},
		Feeds: func(context.Context) ([]FeedState, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]FeedState(nil), f.feeds...), nil
		},
		Limiter: func() httpx.RateLimiterState {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.limiter
		},
		Stuck: func(context.Context, time.Time, time.Duration) (int64, []reconciliation.StuckTrade, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			sample := []reconciliation.StuckTrade{}
			if f.stuck > 0 {
				sample = append(sample, reconciliation.StuckTrade{TradeID: "t1", Reference: "BT-1", Symbol: "BTC/USD", Status: "SETTLING", UpdatedAt: "x"})
			}
			return f.stuck, sample, nil
		},
	}
}

func setup(t *testing.T) (context.Context, *fake, *Monitor) {
	t.Helper()
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	s := config.DefaultAlertSettings()
	s.EmailEnabled, s.SMTPHost, s.From, s.To = true, "127.0.0.1", "a@wintratrade.test", []string{"b@wintratrade.test"}
	saved := config.E
	config.E = &config.Env{Alerts: s, WorkersEnabled: true}
	t.Cleanup(func() { config.E = saved })
	f := &fake{book: exposure.BookStatus{Status: exposure.BookReady}}
	m := New(f.sources())
	m.instance = "test-host"
	return ctx, f, m
}

func alertsFor(t *testing.T, ctx context.Context, key string) []models.Alert {
	t.Helper()
	cur, err := mongox.C(models.CollAlerts).Find(ctx, bson.M{"dedupKey": key})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.Alert
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestBookAlertAfterThresholdAndAutoResolve(t *testing.T) {
	ctx, f, m := setup(t)
	t0 := time.Now()
	f.book = exposure.BookStatus{Status: exposure.BookDirty, Reason: "integrity check failed"}
	m.CheckCluster(ctx, t0)
	m.CheckCluster(ctx, t0.Add(20*time.Second))
	if rows := alertsFor(t, ctx, keyBook); len(rows) != 0 {
		t.Fatalf("alerted before the 30 s threshold: %+v", rows)
	}
	m.CheckCluster(ctx, t0.Add(31*time.Second))
	rows := alertsFor(t, ctx, keyBook)
	if len(rows) != 1 || rows[0].Severity != "critical" || rows[0].Status != models.AlertOpen || rows[0].Source != models.AlertSourceExposure {
		t.Fatalf("book alert: %+v", rows)
	}
	f.book = exposure.BookStatus{Status: exposure.BookReady}
	m.CheckCluster(ctx, t0.Add(41*time.Second))
	rows = alertsFor(t, ctx, keyBook)
	if rows[0].Status != models.AlertResolved || !rows[0].ResolvedBySystem {
		t.Fatalf("not resolved: %+v", rows[0])
	}
	// A blip shorter than the threshold never alerts.
	f.book = exposure.BookStatus{Status: exposure.BookRebuilding}
	m.CheckCluster(ctx, t0.Add(50*time.Second))
	f.book = exposure.BookStatus{Status: exposure.BookReady}
	m.CheckCluster(ctx, t0.Add(60*time.Second))
	f.book = exposure.BookStatus{Status: exposure.BookRebuilding}
	m.CheckCluster(ctx, t0.Add(70*time.Second))
	if rows := alertsFor(t, ctx, keyBook); len(rows) != 1 {
		t.Fatalf("a short rebuild alerted: %d rows", len(rows))
	}
	// Redis unreadable counts as unavailable.
	f.bookErr = errors.New("dial tcp: connection refused")
	m.CheckCluster(ctx, t0.Add(101*time.Second))
	if rows := alertsFor(t, ctx, keyBook); len(rows) != 2 || rows[1].Status != models.AlertOpen {
		t.Fatalf("redis error: %+v", rows)
	}
}

func TestFeedStaleWarnsThenOfflineEscalatesAndClears(t *testing.T) {
	ctx, f, m := setup(t)
	t0 := time.Now()
	age := int64(2500)
	f.feeds = []FeedState{
		{AssetID: "a1", Symbol: "BTC/USD", Status: models.FeedStatusStale, AgeMs: &age},
		{AssetID: "a2", Symbol: "ETH/USD", Status: models.FeedStatusHealthy},
	}
	m.CheckCluster(ctx, t0)
	m.CheckCluster(ctx, t0.Add(61*time.Second))
	rows := alertsFor(t, ctx, feedPrefix+"a1")
	if len(rows) != 1 || rows[0].Severity != "warning" {
		t.Fatalf("stale feed: %+v", rows)
	}
	if rows := alertsFor(t, ctx, feedPrefix+"a2"); len(rows) != 0 {
		t.Fatal("healthy feed alerted")
	}
	f.feeds[0].Status = models.FeedStatusOffline
	m.CheckCluster(ctx, t0.Add(71*time.Second))
	rows = alertsFor(t, ctx, feedPrefix+"a1")
	if len(rows) != 1 || rows[0].Severity != "critical" || rows[0].Occurrences != 2 {
		t.Fatalf("offline did not escalate the same alert: %+v", rows)
	}
	// The asset closes (out of hours): no longer tradable, so it clears.
	f.feeds = f.feeds[1:]
	m.CheckCluster(ctx, t0.Add(81*time.Second))
	if rows := alertsFor(t, ctx, feedPrefix+"a1"); rows[0].Status != models.AlertResolved {
		t.Fatalf("closed asset still alerting: %+v", rows[0])
	}
	// DEGRADED is not an alert.
	f.feeds = []FeedState{{AssetID: "a3", Symbol: "EUR/USD", Status: models.FeedStatusDegraded}}
	m.CheckCluster(ctx, t0.Add(100*time.Second))
	m.CheckCluster(ctx, t0.Add(200*time.Second))
	if rows := alertsFor(t, ctx, feedPrefix+"a3"); len(rows) != 0 {
		t.Fatal("degraded feed alerted")
	}
}

func TestStuckSettlementsAlertAndClear(t *testing.T) {
	ctx, f, m := setup(t)
	t0 := time.Now()
	f.stuck = 3
	m.CheckCluster(ctx, t0)
	rows := alertsFor(t, ctx, keyStuck)
	if len(rows) != 1 || rows[0].Severity != "critical" || rows[0].Source != models.AlertSourceSettlement {
		t.Fatalf("stuck: %+v", rows)
	}
	f.stuck = 0
	m.CheckCluster(ctx, t0.Add(30*time.Second))
	if rows := alertsFor(t, ctx, keyStuck); rows[0].Status != models.AlertOpen {
		t.Fatal("re-checked inside the one-minute spacing")
	}
	m.CheckCluster(ctx, t0.Add(61*time.Second))
	if rows := alertsFor(t, ctx, keyStuck); rows[0].Status != models.AlertResolved {
		t.Fatalf("not cleared: %+v", rows[0])
	}
}

func TestRateLimiterPerInstanceAlertResolvedWhenStale(t *testing.T) {
	ctx, f, m := setup(t)
	t0 := time.Now()
	key := "rate-limiter:test-host"
	f.limiter = httpx.RateLimiterState{Degraded: true, DegradedSince: t0.UnixMilli(), FallbackRequests: 42}
	m.CheckLocal(ctx, t0)
	m.CheckLocal(ctx, t0.Add(61*time.Second))
	rows := alertsFor(t, ctx, key)
	if len(rows) != 1 || rows[0].Severity != "warning" || rows[0].Source != models.AlertSourceRateLimiter {
		t.Fatalf("limiter: %+v", rows)
	}
	f.limiter = httpx.RateLimiterState{}
	m.CheckLocal(ctx, t0.Add(71*time.Second))
	// The leader resolves it once it is no longer re-raised.
	if _, err := mongox.C(models.CollAlerts).UpdateOne(ctx, bson.M{"dedupKey": key},
		bson.M{"$set": bson.M{"lastSeenAt": time.Now().Add(-10 * time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	m.CheckCluster(ctx, time.Now())
	if rows := alertsFor(t, ctx, key); rows[0].Status != models.AlertResolved {
		t.Fatalf("stale limiter alert left open: %+v", rows[0])
	}
}
