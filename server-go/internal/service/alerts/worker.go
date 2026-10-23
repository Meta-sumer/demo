package alerts

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// The mailer: a leader-only loop that sends the outbox.
//
//   - Gathering: nothing is sent until the oldest waiting event is
//     gatherWindow old, so a burst (Redis down: book, limiter and feed at
//     once) goes out together.
//   - Digest: DigestThreshold or more events (or the last email the hourly
//     budget allows) go out as one digest; fewer go one email each.
//   - Rate limit: at most MaxEmailsPerHour messages in any rolling hour (a
//     digest counts once). Over the budget, events wait and leave as a digest
//     when the budget frees up. Nothing is dropped.
//   - Retries: a failed send is retried with exponential backoff (30 s,
//     doubling, at most 15 min apart) and given up after maxAttempts, with an
//     error log line ALERT_EMAIL_FAILED.
//
// Every attempt is recorded on the outbox rows (attempts, lastError,
// sentAt, batchId), so the admin console shows what was sent.

// Tunables, variables so tests can shorten them.
var (
	gatherWindow   = 15 * time.Second
	retryBase      = 30 * time.Second
	retryMax       = 15 * time.Minute
	maxAttempts    = int64(8)
	maxPerDigest   = int64(100)
	mailerInterval = 5 * time.Second
)

func backoff(attempts int64) time.Duration {
	d := retryBase
	for i := int64(1); i < attempts && d < retryMax; i++ {
		d *= 2
	}
	return min(d, retryMax)
}

// Deliver sends what is due at now. It returns the number of emails sent.
func Deliver(ctx context.Context, now time.Time) (int, error) {
	s := config.Alerts()
	if !s.EmailEnabled {
		return 0, nil
	}
	return deliver(ctx, now, s, newSender(s))
}

func deliver(ctx context.Context, now time.Time, s config.AlertSettings, sender Sender) (int, error) {
	cur, err := notificationsColl().Find(ctx, bson.M{"status": models.AlertNotifyPending, "nextAttemptAt": bson.M{"$lte": now}},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(maxPerDigest*2))
	if err != nil {
		return 0, err
	}
	var due []models.AlertNotification
	if err := cur.All(ctx, &due); err != nil {
		return 0, err
	}
	if len(due) == 0 {
		return 0, nil
	}
	if now.Sub(due[0].CreatedAt) < gatherWindow {
		return 0, nil
	}
	used, err := sentInLastHour(ctx, now)
	if err != nil {
		return 0, err
	}
	budget := int64(s.MaxEmailsPerHour) - used
	if budget <= 0 {
		logx.Warn("alert email rate limit reached - events wait for a digest", "waiting", len(due), "maxPerHour", s.MaxEmailsPerHour)
		return 0, nil
	}
	threshold := max(s.DigestThreshold, 2)
	sent := 0
	if len(due) >= threshold || budget == 1 {
		batch := due[:min(int64(len(due)), maxPerDigest)]
		if send(ctx, now, s, sender, batch) {
			sent++
		}
		return sent, nil
	}
	for i := range due {
		if budget <= 0 {
			break
		}
		if send(ctx, now, s, sender, due[i:i+1]) {
			sent++
		}
		budget--
	}
	return sent, nil
}

// sentInLastHour counts emails (distinct batches) sent in the rolling hour.
func sentInLastHour(ctx context.Context, now time.Time) (int64, error) {
	cur, err := notificationsColl().Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"status": models.AlertNotifySent, "sentAt": bson.M{"$gt": now.Add(-time.Hour)}}},
		bson.M{"$group": bson.M{"_id": "$batchId"}},
		bson.M{"$count": "n"},
	})
	if err != nil {
		return 0, err
	}
	var rows []struct {
		N int64 `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].N, nil
}

func send(ctx context.Context, now time.Time, s config.AlertSettings, sender Sender, batch []models.AlertNotification) bool {
	ids := make([]bson.ObjectID, len(batch))
	attempts := int64(0)
	for i, n := range batch {
		ids[i] = n.ID
		attempts = max(attempts, n.Attempts)
	}
	attempts++
	msg := compose(s, batch, now)
	sctx, cancel := context.WithTimeout(ctx, time.Duration(max(s.SMTPTimeoutSeconds, 5)+5)*time.Second)
	err := sender.Send(sctx, msg)
	cancel()
	stamp := mongox.Now()
	if err == nil {
		batchID := uuid.NewString()
		if _, uerr := notificationsColl().UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}}, bson.M{"$set": bson.M{
			"status": models.AlertNotifySent, "sentAt": stamp, "batchId": batchID, "attempts": attempts, "lastError": nil, "updatedAt": stamp,
		}}); uerr != nil {
			logx.Error("alert email sent but not recorded", "batch", batchID, "err", uerr.Error())
		}
		logx.Info("alert email sent", "events", len(batch), "batch", batchID, "subject", msg.Subject)
		return true
	}
	reason := clip(err.Error(), 500)
	set := bson.M{"attempts": attempts, "lastError": reason, "updatedAt": stamp}
	if attempts >= maxAttempts {
		set["status"] = models.AlertNotifyFailed
		logx.Error("ALERT_EMAIL_FAILED - giving up", "events", len(batch), "attempts", attempts, "err", reason)
	} else {
		set["nextAttemptAt"] = now.Add(backoff(attempts))
		logx.Warn("alert email failed - will retry", "events", len(batch), "attempts", attempts, "retryIn", backoff(attempts).String(), "err", reason)
	}
	if _, uerr := notificationsColl().UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}}, bson.M{"$set": set}); uerr != nil {
		logx.Error("could not record an alert email failure", "err", uerr.Error())
	}
	return false
}

func kindLabel(kind string) string {
	switch kind {
	case models.AlertNotifyResolved:
		return "RESOLVED"
	case models.AlertNotifyEscalated:
		return "ESCALATED"
	}
	return "OPEN"
}

// compose writes the subject and plain-text body of one email.
func compose(s config.AlertSettings, batch []models.AlertNotification, now time.Time) Message {
	prefix := strings.TrimSpace(s.EmailSubjectPrefix)
	if prefix != "" {
		prefix += " "
	}
	if len(batch) == 1 {
		n := batch[0]
		subject := prefix + strings.ToUpper(n.Severity) + " " + kindLabel(n.Kind) + ": " + n.Title
		if n.Kind == models.AlertNotifyResolved {
			subject = prefix + "RESOLVED: " + n.Title
		}
		var b strings.Builder
		writeEvent(&b, n)
		b.WriteString("\nOpen alerts are listed in the admin console under Governance > Alerts.\n")
		return Message{Subject: subject, Body: b.String()}
	}
	counts := map[string]int{}
	worst := config.AlertSeverityInfo
	for _, n := range batch {
		counts[n.Severity]++
		if n.Kind != models.AlertNotifyResolved && config.AlertSeverityRank(n.Severity) > config.AlertSeverityRank(worst) {
			worst = n.Severity
		}
	}
	parts := []string{}
	for _, sev := range []string{config.AlertSeverityCritical, config.AlertSeverityWarning, config.AlertSeverityInfo} {
		if counts[sev] > 0 {
			parts = append(parts, strconv.Itoa(counts[sev])+" "+sev)
		}
	}
	subject := fmt.Sprintf("%sDIGEST %s: %d alert events (%s)", prefix, strings.ToUpper(worst), len(batch), strings.Join(parts, ", "))
	sorted := append([]models.AlertNotification(nil), batch...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return config.AlertSeverityRank(sorted[i].Severity) > config.AlertSeverityRank(sorted[j].Severity)
	})
	var b strings.Builder
	fmt.Fprintf(&b, "%d alert events at %s (UTC). Most severe first.\n\n", len(batch), now.UTC().Format(time.RFC3339))
	for i, n := range sorted {
		fmt.Fprintf(&b, "%d. ", i+1)
		writeEvent(&b, n)
		b.WriteString("\n")
	}
	b.WriteString("Open alerts are listed in the admin console under Governance > Alerts.\n")
	return Message{Subject: subject, Body: b.String()}
}

func writeEvent(b *strings.Builder, n models.AlertNotification) {
	fmt.Fprintf(b, "[%s] %s %s\n", strings.ToUpper(n.Severity), kindLabel(n.Kind), n.Title)
	fmt.Fprintf(b, "   code: %s   source: %s   at: %s UTC\n", n.Code, n.Source, n.OccurredAt.UTC().Format(time.RFC3339))
	if strings.TrimSpace(n.Detail) != "" {
		for _, line := range strings.Split(strings.TrimSpace(n.Detail), "\n") {
			b.WriteString("   " + line + "\n")
		}
	}
}

// ── loop ────────────────────────────────────────────────────────────────

var (
	workerMu sync.Mutex
	mailLoop *leader.Loop
)

// Start runs the mailer on the leader (no-op when WORKERS_ENABLED=false).
func Start() {
	if config.E != nil && !config.E.WorkersEnabled {
		return
	}
	workerMu.Lock()
	defer workerMu.Unlock()
	if mailLoop == nil {
		mailLoop = leader.New(leader.Options{
			Name:     "alert-mailer",
			Interval: mailerInterval,
			LockTTL:  2 * time.Minute,
			OnTick: func(ctx context.Context) error {
				_, err := Deliver(ctx, time.Now())
				return err
			},
		})
	}
	mailLoop.Start()
	s := config.Alerts()
	if s.EmailEnabled {
		logx.Info("alert email enabled", "smtpHost", s.SMTPHost, "smtpPort", s.SMTPPort, "recipients", len(s.To), "minSeverity", s.MinSeverity)
	} else {
		logx.Warn("alert email disabled (ALERT_EMAIL_ENABLED=false) - alerts are stored and logged only")
	}
}

// Stop ends the loop.
func Stop(ctx context.Context) {
	workerMu.Lock()
	l := mailLoop
	workerMu.Unlock()
	if l != nil {
		l.Stop()
	}
}

// Status is the admin view of the alerting pipeline (no addresses, no
// credentials).
type Status struct {
	EmailEnabled         bool    `json:"emailEnabled"`
	MinSeverity          string  `json:"minSeverity"`
	Recipients           int     `json:"recipients"`
	MaxEmailsPerHour     int     `json:"maxEmailsPerHour"`
	Open                 int64   `json:"open"`
	OpenCritical         int64   `json:"openCritical"`
	PendingNotifications int64   `json:"pendingNotifications"`
	FailedNotifications  int64   `json:"failedNotifications"`
	SentLastHour         int64   `json:"sentLastHour"`
	LastSentAt           *string `json:"lastSentAt"`
	LastError            *string `json:"lastError"`
}

// GetStatus summarises open alerts and the outbox.
func GetStatus(ctx context.Context) (Status, error) {
	s := config.Alerts()
	st := Status{EmailEnabled: s.EmailEnabled, MinSeverity: s.MinSeverity, Recipients: len(s.To), MaxEmailsPerHour: s.MaxEmailsPerHour}
	var err error
	if st.Open, err = alertsColl().CountDocuments(ctx, bson.M{"status": models.AlertOpen}); err != nil {
		return st, err
	}
	if st.OpenCritical, err = alertsColl().CountDocuments(ctx, bson.M{"status": models.AlertOpen, "severity": config.AlertSeverityCritical}); err != nil {
		return st, err
	}
	if st.PendingNotifications, err = notificationsColl().CountDocuments(ctx, bson.M{"status": models.AlertNotifyPending}); err != nil {
		return st, err
	}
	if st.FailedNotifications, err = notificationsColl().CountDocuments(ctx, bson.M{"status": models.AlertNotifyFailed}); err != nil {
		return st, err
	}
	if st.SentLastHour, err = sentInLastHour(ctx, time.Now()); err != nil {
		return st, err
	}
	var last models.AlertNotification
	if err := notificationsColl().FindOne(ctx, bson.M{"status": models.AlertNotifySent},
		options.FindOne().SetSort(bson.D{{Key: "sentAt", Value: -1}})).Decode(&last); err == nil && last.SentAt != nil {
		v := last.SentAt.UTC().Format(time.RFC3339)
		st.LastSentAt = &v
	}
	var failed models.AlertNotification
	if err := notificationsColl().FindOne(ctx, bson.M{"lastError": bson.M{"$ne": nil}, "status": bson.M{"$ne": models.AlertNotifySent}},
		options.FindOne().SetSort(bson.D{{Key: "updatedAt", Value: -1}})).Decode(&failed); err == nil {
		st.LastError = failed.LastError
	}
	return st, nil
}
