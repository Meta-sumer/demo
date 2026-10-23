package usdt

import (
	"context"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/alerts"
	"binarytrading/internal/service/audit"
)

// Alerts. A disagreement between BeeX, our records and the ledger is never
// corrected silently: it becomes an alert row (one per problem while it is
// open), an error log line USDT_ALERT and a system audit row, and shows on
// the admin USDT dashboard and in the reconciliation run. Operators resolve
// alerts with a note.

// Alert codes.
const (
	AlertWebhookUnmatched           = "webhook_unmatched"
	AlertWebhookMismatch            = "webhook_mismatch"
	AlertWebhookIgnored             = "webhook_ignored"
	AlertWebhookFailed              = "webhook_failed"
	AlertWebhookReplay              = "webhook_replay"
	AlertDepositPaidNotCredited     = "deposit_paid_not_credited"
	AlertDepositCreditedUnpaid      = "deposit_credited_provider_unpaid"
	AlertDepositReceivedBeyond      = "deposit_received_beyond_credit"
	AlertDepositProviderMismatch    = "deposit_provider_mismatch"
	AlertDepositMissingAtProvider   = "deposit_missing_at_provider"
	AlertWithdrawalCompletedAfter   = "withdrawal_completed_after_release"
	AlertWithdrawalSettledFailed    = "withdrawal_settled_provider_failed"
	AlertWithdrawalProviderMismatch = "withdrawal_provider_mismatch"
	AlertWithdrawalStateMismatch    = "withdrawal_state_mismatch"
	AlertWithdrawalMissing          = "withdrawal_missing_at_provider"
	AlertWithdrawalSubmissionStuck  = "withdrawal_submission_stuck"
	AlertLedgerMismatch             = "ledger_mismatch"
	AlertProviderUnreachable        = "provider_unreachable"
)

func alertsColl() *mongo.Collection { return mongox.C(models.CollUsdtAlerts) }

// Alert describes one problem.
type Alert struct {
	Kind, Code, Severity string
	// Subject is what the problem is about (a record id); with Kind and Code
	// it forms the alert's identity.
	Subject      string
	Message      string
	Details      map[string]any
	DepositID    *bson.ObjectID
	WithdrawalID *bson.ObjectID
	EventID      *bson.ObjectID
}

func (a Alert) key() string { return a.Kind + "|" + a.Subject + "|" + a.Code }

// RaiseAlert records an alert, or updates the open one with the same
// identity. It never fails the caller: an alert that cannot be stored is
// still logged.
func RaiseAlert(ctx context.Context, a Alert) {
	ctx = context.WithoutCancel(ctx)
	if a.Severity == "" {
		a.Severity = models.AlertWarning
	}
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	now := mongox.Now()
	key := a.key()
	// Mirrored into the platform alerts, which email the operators.
	alerts.Raise(ctx, alerts.Alert{Code: "usdt." + a.Code, Severity: a.Severity, Source: models.AlertSourceUsdt,
		DedupKey: "usdt|" + key, Title: "USDT " + a.Kind + ": " + a.Code, Detail: a.Message})
	update := bson.M{
		"$setOnInsert": bson.M{
			"key": key, "kind": a.Kind, "code": a.Code, "status": models.AlertOpen,
			"depositId": a.DepositID, "withdrawalId": a.WithdrawalID, "eventId": a.EventID,
			"firstSeenAt": now, "createdAt": now, "resolvedBy": nil, "resolvedAt": nil, "resolutionNote": nil,
		},
		"$set": bson.M{"severity": a.Severity, "message": a.Message, "details": a.Details, "lastSeenAt": now, "updatedAt": now},
		"$inc": bson.M{"occurrences": int64(1)},
	}
	var res *mongo.UpdateResult
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		res, err = alertsColl().UpdateOne(ctx, bson.M{"key": key, "status": models.AlertOpen}, update, options.UpdateOne().SetUpsert(true))
		// Two concurrent first sightings: the loser's upsert hits the unique
		// index; the retry then updates the winner's row.
		if err == nil || !mongox.IsDuplicateKey(err) {
			break
		}
	}
	if err != nil {
		logx.Error("USDT_ALERT (not stored)", "code", a.Code, "key", key, "message", a.Message, "err", err.Error())
		return
	}
	if res.UpsertedCount == 0 {
		logx.Warn("USDT_ALERT repeated", "code", a.Code, "key", key, "severity", a.Severity)
		return
	}
	logx.Error("USDT_ALERT", "code", a.Code, "key", key, "severity", a.Severity, "message", a.Message, "details", a.Details)
	audit.Log(audit.Entry{
		ActorRole: "SYSTEM", Action: AuditAlertRaised, Resource: "UsdtAlert", ResourceID: key,
		After: map[string]any{"code": a.Code, "severity": a.Severity, "message": a.Message}, Failed: true, Reason: a.Message,
	})
}

// resolveAlertsBySystem closes open alerts of one identity when the
// condition is gone by itself (only used for the provider being reachable
// again: a money mismatch is always left to an operator).
func resolveAlertsBySystem(ctx context.Context, a Alert, note string) {
	alerts.ResolveByKey(ctx, "usdt|"+a.key(), note)
	now := mongox.Now()
	if _, err := alertsColl().UpdateMany(ctx, bson.M{"key": a.key(), "status": models.AlertOpen}, bson.M{"$set": bson.M{
		"status": models.AlertResolved, "resolvedAt": now, "resolutionNote": note, "updatedAt": now,
	}}); err != nil {
		logx.Warn("could not close a USDT alert", "key", a.key(), "err", err.Error())
	}
}

// Audit actions of the USDT wallet.
const (
	AuditAlertRaised         = "usdt.alert.raised"
	AuditAlertResolved       = "usdt.alert.resolved"
	AuditDepositCreated      = "usdt.deposit.created"
	AuditDepositRefreshed    = "usdt.deposit.refreshed"
	AuditDepositManualCredit = "usdt.deposit.manual_credit"
	AuditWithdrawalRequested = "usdt.withdrawal.requested"
	AuditWithdrawalApproved  = "usdt.withdrawal.approved"
	AuditWithdrawalRejected  = "usdt.withdrawal.rejected"
	AuditWithdrawalRefreshed = "usdt.withdrawal.refreshed"
	// An operator settled (completed) or released (failed) a withdrawal whose
	// BeeX answer did not match, after checking it with BeeX.
	AuditWithdrawalResolvedCompleted = "usdt.withdrawal.resolved_completed"
	AuditWithdrawalResolvedFailed    = "usdt.withdrawal.resolved_failed"
	AuditTransferToTrading           = "usdt.transfer.created"
	AuditWebhookReprocessed          = "usdt.webhook.reprocessed"
	AuditNetworkUpdated              = "usdt.network.updated"
	AuditReconciliationRun           = "usdt.reconciliation.run"
)

// AlertQuery filters alerts.
type AlertQuery struct {
	Status       *string
	Severity     *string
	Kind         *string
	Code         *string
	DepositID    *bson.ObjectID
	WithdrawalID *bson.ObjectID
	Page, Limit  int64
}

// ListAlerts returns alerts, most recently seen first.
func ListAlerts(ctx context.Context, q AlertQuery) ([]models.UsdtAlertView, int64, error) {
	filter := bson.M{}
	if q.Status != nil {
		filter["status"] = *q.Status
	}
	if q.Severity != nil {
		filter["severity"] = *q.Severity
	}
	if q.Kind != nil {
		filter["kind"] = *q.Kind
	}
	if q.Code != nil && *q.Code != "" {
		filter["code"] = strings.TrimSpace(*q.Code)
	}
	if q.DepositID != nil {
		filter["depositId"] = *q.DepositID
	}
	if q.WithdrawalID != nil {
		filter["withdrawalId"] = *q.WithdrawalID
	}
	page, limit := max(q.Page, 1), max(q.Limit, 1)
	cur, err := alertsColl().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "lastSeenAt", Value: -1}, {Key: "_id", Value: -1}}).SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.UsdtAlert
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	total, err := alertsColl().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	out := make([]models.UsdtAlertView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToUsdtAlertView(&rows[i]))
	}
	return out, total, nil
}

// GetAlert returns one alert.
func GetAlert(ctx context.Context, id bson.ObjectID) (*models.UsdtAlert, error) {
	var a models.UsdtAlert
	if err := alertsColl().FindOne(ctx, bson.M{"_id": id}).Decode(&a); err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.NotFoundErr(apperr.NotFound, "Alert not found")
		}
		return nil, err
	}
	return &a, nil
}

// ResolveAlert closes an open alert with the operator's note.
func ResolveAlert(ctx context.Context, id bson.ObjectID, adminID, note string) (*models.UsdtAlert, error) {
	admin, ok := mongox.ParseID(adminID)
	if !ok {
		return nil, apperr.Unauth()
	}
	now := mongox.Now()
	var a models.UsdtAlert
	err := alertsColl().FindOneAndUpdate(ctx, bson.M{"_id": id, "status": models.AlertOpen}, bson.M{"$set": bson.M{
		"status": models.AlertResolved, "resolvedBy": admin, "resolvedAt": now, "resolutionNote": note, "updatedAt": now,
	}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&a)
	if err != nil {
		if mongox.IsNotFound(err) {
			if _, gerr := GetAlert(ctx, id); gerr != nil {
				return nil, gerr
			}
			return nil, apperr.ConflictErr(apperr.Conflict, "This alert is already resolved")
		}
		return nil, err
	}
	alerts.ResolveByKey(ctx, "usdt|"+a.Key, "resolved on the USDT page: "+note)
	return &a, nil
}

// AlertSummary is the dashboard's view of open alerts.
type AlertSummary struct {
	Open     int64                  `json:"open"`
	Critical int64                  `json:"critical"`
	Latest   []models.UsdtAlertView `json:"latest"`
}

func alertSummary(ctx context.Context) (AlertSummary, error) {
	s := AlertSummary{Latest: []models.UsdtAlertView{}}
	var err error
	if s.Open, err = alertsColl().CountDocuments(ctx, bson.M{"status": models.AlertOpen}); err != nil {
		return s, err
	}
	if s.Critical, err = alertsColl().CountDocuments(ctx, bson.M{"status": models.AlertOpen, "severity": models.AlertCritical}); err != nil {
		return s, err
	}
	open := models.AlertOpen
	latest, _, err := ListAlerts(ctx, AlertQuery{Status: &open, Page: 1, Limit: 10})
	if err != nil {
		return s, err
	}
	s.Latest = latest
	return s, nil
}

// classifyIgnored turns the reason an update was not applied into an alert
// code and severity ("" = not worth an alert, e.g. an event type we do not
// use).
func classifyIgnored(reason string) (code, severity string) {
	switch {
	case strings.HasPrefix(reason, "unknown event"):
		return "", ""
	case strings.Contains(reason, "CONFLICT"):
		return AlertWebhookMismatch, models.AlertCritical
	case strings.Contains(reason, "mismatch"):
		return AlertWebhookMismatch, models.AlertCritical
	case strings.HasPrefix(reason, "no deposit"), strings.HasPrefix(reason, "no withdrawal"):
		return AlertWebhookUnmatched, models.AlertWarning
	}
	return AlertWebhookIgnored, models.AlertWarning
}
