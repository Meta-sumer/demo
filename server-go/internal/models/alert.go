package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// Operational alerts (service/alerts): one open row per problem (DedupKey),
// a repeat sighting only bumps its counter. Email notifications go through
// an outbox (alert_notifications) that a leader-only worker sends.

const (
	CollAlerts             = "alerts"
	CollAlertNotifications = "alert_notifications"
)

// AlertSeverityInfo is the lowest severity (warning and critical are shared
// with the USDT and hedge alerts: AlertWarning, AlertCritical).
const AlertSeverityInfo = "info"

// Where an alert comes from.
const (
	AlertSourceUsdt           = "usdt"
	AlertSourceHedging        = "hedging"
	AlertSourceReconciliation = "reconciliation"
	AlertSourceExposure       = "exposure"
	AlertSourceFeed           = "feed"
	AlertSourceSettlement     = "settlement"
	AlertSourceRateLimiter    = "rate-limiter"
	AlertSourceSystem         = "system"
)

// Notification kinds and states.
const (
	AlertNotifyOpened    = "opened"
	AlertNotifyEscalated = "escalated"
	AlertNotifyResolved  = "resolved"

	AlertNotifyPending = "pending"
	AlertNotifySent    = "sent"
	AlertNotifyFailed  = "failed"
)

type Alert struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	DedupKey string        `bson:"dedupKey"`
	Code     string        `bson:"code"`
	Source   string        `bson:"source"`
	Severity string        `bson:"severity"`
	// SeverityRank is the highest severity seen while open (0 info, 1
	// warning, 2 critical); a repeat never lowers it.
	SeverityRank int64  `bson:"severityRank"`
	Title        string `bson:"title"`
	Detail       string `bson:"detail"`
	Status       string `bson:"status"`
	// Occurrences counts sightings while open.
	Occurrences int64     `bson:"occurrences"`
	FirstSeenAt time.Time `bson:"firstSeenAt"`
	LastSeenAt  time.Time `bson:"lastSeenAt"`
	// Notified is true once an email about this alert was queued; only then
	// is its resolution emailed too.
	Notified       bool           `bson:"notified"`
	ResolvedBy     *bson.ObjectID `bson:"resolvedBy"`
	ResolvedAt     *time.Time     `bson:"resolvedAt"`
	ResolutionNote *string        `bson:"resolutionNote"`
	// ResolvedBySystem: the condition cleared by itself (no operator).
	ResolvedBySystem bool      `bson:"resolvedBySystem"`
	CreatedAt        time.Time `bson:"createdAt"`
	UpdatedAt        time.Time `bson:"updatedAt"`
}

type AlertView struct {
	ID               string    `json:"id"`
	DedupKey         string    `json:"dedupKey"`
	Code             string    `json:"code"`
	Source           string    `json:"source"`
	Severity         string    `json:"severity"`
	Title            string    `json:"title"`
	Detail           string    `json:"detail"`
	Status           string    `json:"status"`
	Occurrences      int64     `json:"occurrences"`
	FirstSeenAt      iso.Time  `json:"firstSeenAt"`
	LastSeenAt       iso.Time  `json:"lastSeenAt"`
	Notified         bool      `json:"notified"`
	ResolvedBy       *string   `json:"resolvedBy"`
	ResolvedAt       *iso.Time `json:"resolvedAt"`
	ResolutionNote   *string   `json:"resolutionNote"`
	ResolvedBySystem bool      `json:"resolvedBySystem"`
}

func ToAlertView(a *Alert) AlertView {
	return AlertView{
		ID: a.ID.Hex(), DedupKey: a.DedupKey, Code: a.Code, Source: a.Source, Severity: a.Severity, Title: a.Title,
		Detail: a.Detail, Status: a.Status, Occurrences: a.Occurrences, FirstSeenAt: iso.From(a.FirstSeenAt),
		LastSeenAt: iso.From(a.LastSeenAt), Notified: a.Notified, ResolvedBy: hexOrNil(a.ResolvedBy),
		ResolvedAt: iso.Ptr(a.ResolvedAt), ResolutionNote: a.ResolutionNote, ResolvedBySystem: a.ResolvedBySystem,
	}
}

// AlertNotification is one email-worthy event in the outbox.
type AlertNotification struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	AlertID  bson.ObjectID `bson:"alertId"`
	DedupKey string        `bson:"dedupKey"`
	Kind     string        `bson:"kind"`
	Code     string        `bson:"code"`
	Source   string        `bson:"source"`
	Severity string        `bson:"severity"`
	Title    string        `bson:"title"`
	Detail   string        `bson:"detail"`
	// OccurredAt is when the event happened (the email shows it).
	OccurredAt    time.Time  `bson:"occurredAt"`
	Status        string     `bson:"status"`
	Attempts      int64      `bson:"attempts"`
	NextAttemptAt time.Time  `bson:"nextAttemptAt"`
	SentAt        *time.Time `bson:"sentAt"`
	// BatchID groups the rows one email carried (a digest carries many).
	BatchID   *string   `bson:"batchId"`
	LastError *string   `bson:"lastError"`
	CreatedAt time.Time `bson:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

// Notifications are kept 30 days.
const alertNotificationKeep = int32(30 * 24 * 60 * 60)

func init() {
	RegisterIndexes(
		// One open alert per problem; a resolved one may be raised again.
		mongox.Index{Collection: CollAlerts, Keys: bson.D{{Key: "dedupKey", Value: 1}}, Options: options.Index().SetUnique(true).
			SetPartialFilterExpression(bson.M{"status": AlertOpen})},
		mongox.Index{Collection: CollAlerts, Keys: bson.D{{Key: "status", Value: 1}, {Key: "lastSeenAt", Value: -1}}},
		mongox.Index{Collection: CollAlerts, Keys: bson.D{{Key: "source", Value: 1}, {Key: "status", Value: 1}}},

		mongox.Index{Collection: CollAlertNotifications, Keys: bson.D{{Key: "status", Value: 1}, {Key: "nextAttemptAt", Value: 1}}},
		mongox.Index{Collection: CollAlertNotifications, Keys: bson.D{{Key: "sentAt", Value: -1}}},
		mongox.Index{Collection: CollAlertNotifications, Keys: bson.D{{Key: "createdAt", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(alertNotificationKeep)},
	)
}
