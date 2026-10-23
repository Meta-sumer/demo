// Package alerts is the platform's operational alerting: one store of open
// problems, and email to the operators.
//
// Raise records an alert in the alerts collection. While an alert is open,
// another Raise with the same DedupKey only bumps its counter (and raises
// its severity if the new one is higher), so a condition that persists does
// not flood anyone. An email is queued in an outbox (alert_notifications)
// when an alert opens, when it escalates, and when a condition alert that was
// emailed clears by itself. A leader-only worker sends the outbox over SMTP
// with a rate limit, retries with backoff, and a digest when several fire at
// once (worker.go, mailer.go).
//
// Sources: the USDT and hedge alerts (mirrored from their own stores),
// reconciliation runs, and the monitor (service/alerts/monitor) that watches
// the exposure book, the market feed, stuck settlements and the rate
// limiter. The API cannot email while it is down, so the host also runs
// deploy/vps/monitor.sh against /ready.
//
// Raise and ResolveByKey never fail the caller: an alert that cannot be
// stored is still logged.
package alerts

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// Alert is one problem as a source reports it.
type Alert struct {
	// Code is the machine name, e.g. "exposure.book_unavailable".
	Code string
	// Severity: info, warning or critical (anything else counts as warning).
	Severity string
	Title    string
	Detail   string
	// DedupKey identifies the problem; defaults to Code.
	DedupKey string
	// Source is one of models.AlertSource* (default system).
	Source string
}

const (
	maxTitle  = 200
	maxDetail = 4000
)

func alertsColl() *mongo.Collection        { return mongox.C(models.CollAlerts) }
func notificationsColl() *mongo.Collection { return mongox.C(models.CollAlertNotifications) }

func normalise(a Alert) Alert {
	a.Code = strings.TrimSpace(a.Code)
	if a.Code == "" {
		a.Code = "unknown"
	}
	switch a.Severity {
	case config.AlertSeverityInfo, config.AlertSeverityWarning, config.AlertSeverityCritical:
	default:
		a.Severity = config.AlertSeverityWarning
	}
	if a.DedupKey == "" {
		a.DedupKey = a.Code
	}
	if a.Source == "" {
		a.Source = models.AlertSourceSystem
	}
	if strings.TrimSpace(a.Title) == "" {
		a.Title = a.Code
	}
	a.Title = clip(oneLine(a.Title), maxTitle)
	a.Detail = clip(a.Detail, maxDetail)
	return a
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// emailWorthy: email is on and the severity reaches ALERT_MIN_SEVERITY.
func emailWorthy(severity string) bool {
	s := config.Alerts()
	return s.EmailEnabled && config.AlertSeverityRank(severity) >= config.AlertSeverityRank(s.MinSeverity)
}

// Raise records a, or updates the open alert with the same DedupKey.
func Raise(ctx context.Context, a Alert) {
	a = normalise(a)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := mongox.Now()
	rank := int64(config.AlertSeverityRank(a.Severity))
	update := bson.M{
		"$setOnInsert": bson.M{
			"_id": bson.NewObjectID(), "dedupKey": a.DedupKey, "code": a.Code, "source": a.Source, "severity": a.Severity,
			"status": models.AlertOpen, "firstSeenAt": now, "createdAt": now, "notified": false,
			"resolvedBy": nil, "resolvedAt": nil, "resolutionNote": nil, "resolvedBySystem": false,
		},
		"$set": bson.M{"title": a.Title, "detail": a.Detail, "lastSeenAt": now, "updatedAt": now},
		"$max": bson.M{"severityRank": rank},
		"$inc": bson.M{"occurrences": int64(1)},
	}
	var prev models.Alert
	var err error
	inserted := false
	for attempt := 0; attempt < 2; attempt++ {
		err = alertsColl().FindOneAndUpdate(ctx, bson.M{"dedupKey": a.DedupKey, "status": models.AlertOpen}, update,
			options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.Before)).Decode(&prev)
		if mongox.IsNotFound(err) {
			inserted, err = true, nil
			break
		}
		// Two first sightings at once: the loser's upsert hits the unique
		// index; the retry updates the winner's row.
		if err == nil || !mongox.IsDuplicateKey(err) {
			break
		}
	}
	if err != nil {
		logx.Error("ALERT (not stored)", "code", a.Code, "key", a.DedupKey, "severity", a.Severity, "title", a.Title, "err", err.Error())
		return
	}

	if inserted {
		logAlert("ALERT", a)
		if emailWorthy(a.Severity) {
			stored, ferr := findOpen(ctx, a.DedupKey)
			if ferr == nil && stored != nil {
				queue(ctx, stored, models.AlertNotifyOpened, a)
			}
		}
		return
	}
	if rank > prev.SeverityRank {
		// Escalated (for example a stale feed that went offline).
		if _, uerr := alertsColl().UpdateOne(ctx, bson.M{"_id": prev.ID, "status": models.AlertOpen},
			bson.M{"$set": bson.M{"severity": a.Severity}}); uerr != nil {
			logx.Warn("could not escalate an alert", "key", a.DedupKey, "err", uerr.Error())
		}
		logAlert("ALERT escalated", a)
		if emailWorthy(a.Severity) {
			prev.Severity = a.Severity
			queue(ctx, &prev, models.AlertNotifyEscalated, a)
		}
		return
	}
	logx.Debug("ALERT repeated", "code", a.Code, "key", a.DedupKey, "occurrences", prev.Occurrences+1)
}

func logAlert(msg string, a Alert) {
	args := []any{"code", a.Code, "key", a.DedupKey, "source", a.Source, "severity", a.Severity, "title", a.Title}
	if a.Severity == config.AlertSeverityInfo {
		logx.Info(msg, args...)
		return
	}
	logx.Error(msg, args...)
}

func findOpen(ctx context.Context, key string) (*models.Alert, error) {
	var a models.Alert
	err := alertsColl().FindOne(ctx, bson.M{"dedupKey": key, "status": models.AlertOpen}).Decode(&a)
	if mongox.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// queue puts one email-worthy event in the outbox and marks the alert as
// notified (its automatic resolution is then emailed too).
func queue(ctx context.Context, stored *models.Alert, kind string, a Alert) {
	now := mongox.Now()
	n := models.AlertNotification{
		ID: bson.NewObjectID(), AlertID: stored.ID, DedupKey: stored.DedupKey, Kind: kind, Code: a.Code, Source: a.Source,
		Severity: a.Severity, Title: a.Title, Detail: a.Detail, OccurredAt: now, Status: models.AlertNotifyPending,
		NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := notificationsColl().InsertOne(ctx, n); err != nil {
		logx.Error("could not queue an alert email", "key", stored.DedupKey, "err", err.Error())
		return
	}
	if !stored.Notified {
		_, _ = alertsColl().UpdateOne(ctx, bson.M{"_id": stored.ID}, bson.M{"$set": bson.M{"notified": true}})
	}
}

// ResolveByKey closes the open alert with this key because its condition
// cleared by itself. If the alert was emailed, the recovery is emailed too.
func ResolveByKey(ctx context.Context, key, note string) bool {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	now := mongox.Now()
	var a models.Alert
	err := alertsColl().FindOneAndUpdate(ctx, bson.M{"dedupKey": key, "status": models.AlertOpen}, bson.M{"$set": bson.M{
		"status": models.AlertResolved, "resolvedAt": now, "resolutionNote": note, "resolvedBySystem": true, "updatedAt": now,
	}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&a)
	if mongox.IsNotFound(err) {
		return false
	}
	if err != nil {
		logx.Warn("could not resolve an alert", "key", key, "err", err.Error())
		return false
	}
	logx.Info("ALERT resolved", "code", a.Code, "key", key, "note", note)
	if a.Notified && emailWorthy(a.Severity) {
		queue(ctx, &a, models.AlertNotifyResolved, Alert{Code: a.Code, Severity: a.Severity, Title: a.Title,
			Detail: note, DedupKey: a.DedupKey, Source: a.Source})
	}
	return true
}

// ResolveStale resolves the open alerts of source last seen before cutoff
// (for conditions that are re-raised on every check while they last).
func ResolveStale(ctx context.Context, source string, cutoff time.Time, note string) int {
	cur, err := alertsColl().Find(ctx, bson.M{"status": models.AlertOpen, "source": source, "lastSeenAt": bson.M{"$lt": cutoff}},
		options.Find().SetProjection(bson.M{"dedupKey": 1}))
	if err != nil {
		logx.Warn("could not read stale alerts", "source", source, "err", err.Error())
		return 0
	}
	var rows []struct {
		Key string `bson:"dedupKey"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0
	}
	n := 0
	for _, r := range rows {
		if ResolveByKey(ctx, r.Key, note) {
			n++
		}
	}
	return n
}

// Resolve closes an open alert with an operator's note (the API audits it).
func Resolve(ctx context.Context, id bson.ObjectID, adminID, note string) (*models.Alert, error) {
	admin, ok := mongox.ParseID(adminID)
	if !ok {
		return nil, apperr.Unauth()
	}
	now := mongox.Now()
	var a models.Alert
	err := alertsColl().FindOneAndUpdate(ctx, bson.M{"_id": id, "status": models.AlertOpen}, bson.M{"$set": bson.M{
		"status": models.AlertResolved, "resolvedBy": admin, "resolvedAt": now, "resolutionNote": note, "updatedAt": now,
	}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&a)
	if err != nil {
		if mongox.IsNotFound(err) {
			if _, gerr := Get(ctx, id); gerr != nil {
				return nil, gerr
			}
			return nil, apperr.ConflictErr(apperr.Conflict, "This alert is already resolved")
		}
		return nil, err
	}
	return &a, nil
}

// Get returns one alert.
func Get(ctx context.Context, id bson.ObjectID) (*models.Alert, error) {
	var a models.Alert
	if err := alertsColl().FindOne(ctx, bson.M{"_id": id}).Decode(&a); err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.NotFoundErr(apperr.NotFound, "Alert not found")
		}
		return nil, err
	}
	return &a, nil
}

// Query filters the alert list.
type Query struct {
	Status, Severity, Source *string
	Page, Limit              int64
}

// List returns alerts, most recently seen first.
func List(ctx context.Context, q Query) ([]models.AlertView, int64, error) {
	filter := bson.M{}
	if q.Status != nil {
		filter["status"] = *q.Status
	}
	if q.Severity != nil {
		filter["severity"] = *q.Severity
	}
	if q.Source != nil {
		filter["source"] = *q.Source
	}
	page, limit := max(q.Page, 1), max(q.Limit, 1)
	cur, err := alertsColl().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "lastSeenAt", Value: -1}, {Key: "_id", Value: -1}}).SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.Alert
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	total, err := alertsColl().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	out := make([]models.AlertView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToAlertView(&rows[i]))
	}
	return out, total, nil
}

// OpenKeys returns the keys of the open alerts from these sources.
func OpenKeys(ctx context.Context, sources ...string) (map[string]bool, error) {
	cur, err := alertsColl().Find(ctx, bson.M{"status": models.AlertOpen, "source": bson.M{"$in": sources}},
		options.Find().SetProjection(bson.M{"dedupKey": 1}))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Key string `bson:"dedupKey"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Key] = true
	}
	return out, nil
}
