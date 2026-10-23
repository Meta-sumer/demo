package alerts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/testsupport"
)

// Store behaviour against a throwaway MongoDB database: one open alert per
// key, counters on repeats, escalation, and which events reach the outbox.

func setup(t *testing.T, mutate func(*config.AlertSettings)) context.Context {
	t.Helper()
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	s := config.DefaultAlertSettings()
	s.EmailEnabled, s.SMTPHost, s.From, s.To = true, "127.0.0.1", "alerts@wintratrade.test", []string{"ops@wintratrade.test"}
	if mutate != nil {
		mutate(&s)
	}
	saved := config.E
	config.E = &config.Env{Alerts: s, WorkersEnabled: true}
	t.Cleanup(func() { config.E = saved })
	return ctx
}

func openAlerts(t *testing.T, ctx context.Context, key string) []models.Alert {
	t.Helper()
	cur, err := alertsColl().Find(ctx, bson.M{"dedupKey": key})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.Alert
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func outbox(t *testing.T, ctx context.Context, filter bson.M) []models.AlertNotification {
	t.Helper()
	cur, err := notificationsColl().Find(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.AlertNotification
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestRaiseDeduplicatesOpenAlerts(t *testing.T) {
	ctx := setup(t, nil)
	a := Alert{Code: "feed.stale", Severity: "warning", Title: "Feed STALE: BTC/USD", Detail: "first", DedupKey: "feed:x", Source: models.AlertSourceFeed}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); Raise(ctx, a) }()
	}
	wg.Wait()
	a.Detail = "latest"
	Raise(ctx, a)

	rows := openAlerts(t, ctx, "feed:x")
	if len(rows) != 1 {
		t.Fatalf("want one alert for the key, got %d", len(rows))
	}
	r := rows[0]
	if r.Status != models.AlertOpen || r.Occurrences != 9 || r.Detail != "latest" || r.Source != models.AlertSourceFeed || !r.Notified {
		t.Fatalf("alert %+v", r)
	}
	if n := outbox(t, ctx, bson.M{"dedupKey": "feed:x"}); len(n) != 1 || n[0].Kind != models.AlertNotifyOpened || n[0].Status != models.AlertNotifyPending {
		t.Fatalf("repeats must not queue more email: %+v", n)
	}
}

func TestEscalationQueuesAnotherEmailButNeverDowngrades(t *testing.T) {
	ctx := setup(t, nil)
	Raise(ctx, Alert{Code: "feed.stale", Severity: "warning", DedupKey: "feed:y", Title: "stale"})
	Raise(ctx, Alert{Code: "feed.offline", Severity: "critical", DedupKey: "feed:y", Title: "offline"})
	Raise(ctx, Alert{Code: "feed.stale", Severity: "warning", DedupKey: "feed:y", Title: "stale again"})
	rows := openAlerts(t, ctx, "feed:y")
	if len(rows) != 1 || rows[0].Severity != "critical" || rows[0].SeverityRank != 2 {
		t.Fatalf("severity: %+v", rows)
	}
	n := outbox(t, ctx, bson.M{"dedupKey": "feed:y"})
	if len(n) != 2 || n[1].Kind != models.AlertNotifyEscalated || n[1].Severity != "critical" {
		t.Fatalf("outbox %+v", n)
	}
}

func TestBelowMinSeverityOrDisabledIsStoredNotEmailed(t *testing.T) {
	ctx := setup(t, func(s *config.AlertSettings) { s.MinSeverity = "critical" })
	Raise(ctx, Alert{Code: "x.warn", Severity: "warning", DedupKey: "k:warn"})
	if rows := openAlerts(t, ctx, "k:warn"); len(rows) != 1 || rows[0].Notified {
		t.Fatalf("stored: %+v", rows)
	}
	if n := outbox(t, ctx, bson.M{"dedupKey": "k:warn"}); len(n) != 0 {
		t.Fatalf("a warning below ALERT_MIN_SEVERITY=critical was queued: %+v", n)
	}
	config.E.Alerts.EmailEnabled = false
	Raise(ctx, Alert{Code: "x.crit", Severity: "critical", DedupKey: "k:crit"})
	if n := outbox(t, ctx, bson.M{"dedupKey": "k:crit"}); len(n) != 0 {
		t.Fatalf("queued with email disabled: %+v", n)
	}
	// Unknown severities count as warning; the title defaults to the code.
	Raise(ctx, Alert{Code: "x.odd", Severity: "loud"})
	if rows := openAlerts(t, ctx, "x.odd"); len(rows) != 1 || rows[0].Severity != "warning" || rows[0].Title != "x.odd" {
		t.Fatalf("normalised: %+v", rows)
	}
}

func TestResolveByKeyEmailsRecoveryOnlyWhenNotified(t *testing.T) {
	ctx := setup(t, nil)
	Raise(ctx, Alert{Code: "exposure.book_unavailable", Severity: "critical", DedupKey: "exposure:book"})
	if !ResolveByKey(ctx, "exposure:book", "READY again") {
		t.Fatal("not resolved")
	}
	if ResolveByKey(ctx, "exposure:book", "again") {
		t.Fatal("resolved twice")
	}
	rows := openAlerts(t, ctx, "exposure:book")
	if len(rows) != 1 || rows[0].Status != models.AlertResolved || !rows[0].ResolvedBySystem || *rows[0].ResolutionNote != "READY again" {
		t.Fatalf("%+v", rows)
	}
	n := outbox(t, ctx, bson.M{"dedupKey": "exposure:book", "kind": models.AlertNotifyResolved})
	if len(n) != 1 || n[0].Detail != "READY again" {
		t.Fatalf("recovery email: %+v", n)
	}

	// Never emailed (below the threshold) -> its recovery is not emailed.
	config.E.Alerts.MinSeverity = "critical"
	Raise(ctx, Alert{Code: "w", Severity: "warning", DedupKey: "quiet"})
	ResolveByKey(ctx, "quiet", "gone")
	if n := outbox(t, ctx, bson.M{"dedupKey": "quiet"}); len(n) != 0 {
		t.Fatalf("recovery of an alert nobody was told about: %+v", n)
	}
}

func TestOperatorResolveThenReopen(t *testing.T) {
	ctx := setup(t, nil)
	Raise(ctx, Alert{Code: "reconciliation.mismatch", Severity: "critical", DedupKey: "rec:a"})
	first := openAlerts(t, ctx, "rec:a")[0]
	admin := bson.NewObjectID()
	a, err := Resolve(ctx, first.ID, admin.Hex(), "checked: bank delay")
	if err != nil || a.Status != models.AlertResolved || a.ResolvedBy == nil || *a.ResolvedBy != admin || a.ResolvedBySystem {
		t.Fatalf("resolve: %+v %v", a, err)
	}
	if _, err := Resolve(ctx, first.ID, admin.Hex(), "again"); err == nil {
		t.Fatal("resolved twice")
	} else if ae, ok := apperr.As(err); !ok || ae.StatusCode != 409 {
		t.Fatalf("second resolve: %v", err)
	}
	if _, err := Resolve(ctx, bson.NewObjectID(), admin.Hex(), "nope"); err == nil {
		t.Fatal("unknown id resolved")
	} else if ae, ok := apperr.As(err); !ok || ae.StatusCode != 404 {
		t.Fatalf("unknown id: %v", err)
	}
	// The same problem later is a new alert (and a new email).
	Raise(ctx, Alert{Code: "reconciliation.mismatch", Severity: "critical", DedupKey: "rec:a"})
	rows := openAlerts(t, ctx, "rec:a")
	if len(rows) != 2 {
		t.Fatalf("reopen: %d rows", len(rows))
	}
	if n := outbox(t, ctx, bson.M{"dedupKey": "rec:a", "kind": models.AlertNotifyOpened}); len(n) != 2 {
		t.Fatalf("opened emails: %d", len(n))
	}
	items, total, err := List(ctx, Query{Page: 1, Limit: 10})
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("list: %d %d %v", total, len(items), err)
	}
	open := models.AlertOpen
	if _, total, _ := List(ctx, Query{Status: &open, Page: 1, Limit: 10}); total != 1 {
		t.Fatalf("open list: %d", total)
	}
}

func TestResolveStaleOnlyTouchesOldAlerts(t *testing.T) {
	ctx := setup(t, nil)
	Raise(ctx, Alert{Code: "ratelimit.memory_fallback", Source: models.AlertSourceRateLimiter, DedupKey: "rate-limiter:old"})
	if _, err := alertsColl().UpdateOne(ctx, bson.M{"dedupKey": "rate-limiter:old"},
		bson.M{"$set": bson.M{"lastSeenAt": time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	Raise(ctx, Alert{Code: "ratelimit.memory_fallback", Source: models.AlertSourceRateLimiter, DedupKey: "rate-limiter:new"})
	if n := ResolveStale(ctx, models.AlertSourceRateLimiter, time.Now().Add(-time.Minute), "stale"); n != 1 {
		t.Fatalf("resolved %d", n)
	}
	keys, err := OpenKeys(ctx, models.AlertSourceRateLimiter)
	if err != nil || len(keys) != 1 || !keys["rate-limiter:new"] {
		t.Fatalf("open keys %v %v", keys, err)
	}
}

// recordingSender is a Sender that records messages and can fail.
type recordingSender struct {
	mu   sync.Mutex
	msgs []Message
	fail int
}

func (r *recordingSender) Send(_ context.Context, m Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail > 0 {
		r.fail--
		return errors.New("451 4.3.0 mailbox busy")
	}
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recordingSender) sent() []Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Message(nil), r.msgs...)
}

func TestDeliverWaitsToGatherThenSendsADigest(t *testing.T) {
	ctx := setup(t, nil)
	for _, k := range []string{"exposure:book", "feed:a", "rate-limiter:h"} {
		Raise(ctx, Alert{Code: k, Severity: "critical", DedupKey: k, Title: "title " + k})
	}
	rec := &recordingSender{}
	s := config.Alerts()
	if n, err := deliver(ctx, time.Now(), s, rec); err != nil || n != 0 {
		t.Fatalf("sent inside the gather window: %d %v", n, err)
	}
	n, err := deliver(ctx, time.Now().Add(gatherWindow+time.Second), s, rec)
	if err != nil || n != 1 {
		t.Fatalf("digest: %d %v", n, err)
	}
	msgs := rec.sent()
	if !strings.Contains(msgs[0].Subject, "DIGEST CRITICAL: 3 alert events") {
		t.Fatalf("subject %q", msgs[0].Subject)
	}
	for _, k := range []string{"exposure:book", "feed:a", "rate-limiter:h"} {
		if !strings.Contains(msgs[0].Body, "title "+k) {
			t.Fatalf("digest misses %s: %s", k, msgs[0].Body)
		}
	}
	sent := outbox(t, ctx, bson.M{"status": models.AlertNotifySent})
	if len(sent) != 3 || sent[0].BatchID == nil || *sent[0].BatchID != *sent[2].BatchID {
		t.Fatalf("one batch for three events: %+v", sent)
	}
}

func TestDeliverSendsIndividuallyBelowTheDigestThreshold(t *testing.T) {
	ctx := setup(t, nil)
	Raise(ctx, Alert{Code: "a", Severity: "warning", DedupKey: "a", Title: "first"})
	Raise(ctx, Alert{Code: "b", Severity: "critical", DedupKey: "b", Title: "second"})
	rec := &recordingSender{}
	if n, err := deliver(ctx, time.Now().Add(time.Minute), config.Alerts(), rec); err != nil || n != 2 {
		t.Fatalf("individual: %d %v", n, err)
	}
	msgs := rec.sent()
	if !strings.Contains(msgs[0].Subject, "WARNING OPEN: first") || !strings.Contains(msgs[1].Subject, "CRITICAL OPEN: second") {
		t.Fatalf("subjects %q %q", msgs[0].Subject, msgs[1].Subject)
	}
}

func TestDeliverRetriesWithBackoffThenGivesUp(t *testing.T) {
	ctx := setup(t, nil)
	Raise(ctx, Alert{Code: "a", Severity: "critical", DedupKey: "retry", Title: "retry me"})
	s := config.Alerts()
	rec := &recordingSender{fail: 2}
	t0 := time.Now().Add(time.Minute)
	if n, _ := deliver(ctx, t0, s, rec); n != 0 {
		t.Fatal("sent despite failure")
	}
	row := outbox(t, ctx, bson.M{"dedupKey": "retry"})[0]
	if row.Attempts != 1 || row.Status != models.AlertNotifyPending || row.LastError == nil ||
		row.NextAttemptAt.Sub(t0).Round(time.Second) != retryBase {
		t.Fatalf("after first failure: %+v", row)
	}
	if n, _ := deliver(ctx, t0.Add(10*time.Second), s, rec); n != 0 || len(rec.sent()) != 0 {
		t.Fatal("retried before the backoff")
	}
	if n, _ := deliver(ctx, t0.Add(retryBase+time.Second), s, rec); n != 0 {
		t.Fatal("second attempt should fail")
	}
	row = outbox(t, ctx, bson.M{"dedupKey": "retry"})[0]
	if row.Attempts != 2 || row.NextAttemptAt.Sub(t0.Add(retryBase+time.Second)).Round(time.Second) != 2*retryBase {
		t.Fatalf("backoff did not double: %+v", row)
	}
	if n, _ := deliver(ctx, t0.Add(10*time.Minute), s, rec); n != 1 {
		t.Fatal("third attempt should succeed")
	}
	row = outbox(t, ctx, bson.M{"dedupKey": "retry"})[0]
	if row.Status != models.AlertNotifySent || row.Attempts != 3 || row.LastError != nil {
		t.Fatalf("after success: %+v", row)
	}

	saved := maxAttempts
	maxAttempts = 2
	t.Cleanup(func() { maxAttempts = saved })
	Raise(ctx, Alert{Code: "b", Severity: "critical", DedupKey: "giveup"})
	failing := &recordingSender{fail: 100}
	deliver(ctx, t0, s, failing)
	deliver(ctx, t0.Add(time.Hour), s, failing)
	row = outbox(t, ctx, bson.M{"dedupKey": "giveup"})[0]
	if row.Status != models.AlertNotifyFailed || row.Attempts != 2 {
		t.Fatalf("give up: %+v", row)
	}
	if st, err := GetStatus(ctx); err != nil || st.FailedNotifications != 1 || st.LastError == nil {
		t.Fatalf("status %+v %v", st, err)
	}
	if backoff(1) != retryBase || backoff(2) != 2*retryBase || backoff(50) != retryMax {
		t.Fatal("backoff schedule")
	}
}

func TestDeliverRespectsTheHourlyLimitAndDigestsTheBacklog(t *testing.T) {
	ctx := setup(t, func(s *config.AlertSettings) { s.MaxEmailsPerHour = 1 })
	s := config.Alerts()
	rec := &recordingSender{}
	t0 := time.Now().Add(time.Minute)
	Raise(ctx, Alert{Code: "a", Severity: "critical", DedupKey: "first", Title: "first"})
	if n, _ := deliver(ctx, t0, s, rec); n != 1 {
		t.Fatal("first email")
	}
	Raise(ctx, Alert{Code: "b", Severity: "critical", DedupKey: "second", Title: "second"})
	Raise(ctx, Alert{Code: "c", Severity: "warning", DedupKey: "third", Title: "third"})
	if n, _ := deliver(ctx, t0.Add(time.Minute), s, rec); n != 0 {
		t.Fatal("sent over the hourly limit")
	}
	if p := outbox(t, ctx, bson.M{"status": models.AlertNotifyPending}); len(p) != 2 {
		t.Fatalf("backlog must wait, not be dropped: %d", len(p))
	}
	if n, _ := deliver(ctx, t0.Add(61*time.Minute), s, rec); n != 1 {
		t.Fatal("backlog digest")
	}
	msgs := rec.sent()
	if len(msgs) != 2 || !strings.Contains(msgs[1].Subject, "DIGEST") ||
		!strings.Contains(msgs[1].Body, "second") || !strings.Contains(msgs[1].Body, "third") {
		t.Fatalf("backlog digest: %+v", msgs)
	}
}
