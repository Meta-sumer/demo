package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// Operational records of the USDT wallet: every signed webhook delivery (the
// replay guard) and the alerts the provider reconciliation raises.
const (
	CollBeexWebhookDeliveries = "beex_webhook_deliveries"
	CollUsdtAlerts            = "usdt_alerts"
)

// webhookDeliveryKeep: a delivery older than the signature tolerance can no
// longer verify, so a day of history is plenty to recognise a replay.
const webhookDeliveryKeep = 7 * 24 * 60 * 60

// BeexWebhookDelivery is one verified webhook request. Signature is the
// HMAC of "t.body" and is unique: the same signed request arriving twice is
// a replay (BeeX signs every retry afresh).
type BeexWebhookDelivery struct {
	ID                 bson.ObjectID  `bson:"_id,omitempty"`
	Signature          string         `bson:"signature"`
	SignatureTimestamp int64          `bson:"signatureTimestamp"`
	EventKey           string         `bson:"eventKey"`
	EventID            *bson.ObjectID `bson:"eventId"`
	RemoteIP           string         `bson:"remoteIp"`
	ReceivedAt         time.Time      `bson:"receivedAt"`
}

// Alert severities and statuses.
const (
	AlertWarning  = "warning"
	AlertCritical = "critical"

	AlertOpen     = "open"
	AlertResolved = "resolved"
)

// Alert kinds: what the problem is about.
const (
	AlertKindDeposit    = "deposit"
	AlertKindWithdrawal = "withdrawal"
	AlertKindWebhook    = "webhook"
	AlertKindProvider   = "provider"
)

// UsdtAlert is a mismatch between BeeX, our records and the ledger that an
// operator has to look at. Nothing is corrected automatically: an alert says
// what disagrees. Key identifies the problem; while one is open, a repeat
// sighting only updates it.
type UsdtAlert struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	Key      string        `bson:"key"`
	Kind     string        `bson:"kind"`
	Code     string        `bson:"code"`
	Severity string        `bson:"severity"`
	Message  string        `bson:"message"`
	// Mixed: the figures that disagree.
	Details        any            `bson:"details"`
	DepositID      *bson.ObjectID `bson:"depositId"`
	WithdrawalID   *bson.ObjectID `bson:"withdrawalId"`
	EventID        *bson.ObjectID `bson:"eventId"`
	Status         string         `bson:"status"`
	Occurrences    int64          `bson:"occurrences"`
	FirstSeenAt    time.Time      `bson:"firstSeenAt"`
	LastSeenAt     time.Time      `bson:"lastSeenAt"`
	ResolvedBy     *bson.ObjectID `bson:"resolvedBy"`
	ResolvedAt     *time.Time     `bson:"resolvedAt"`
	ResolutionNote *string        `bson:"resolutionNote"`
	CreatedAt      time.Time      `bson:"createdAt"`
	UpdatedAt      time.Time      `bson:"updatedAt"`
}

type UsdtAlertView struct {
	ID             string    `json:"id"`
	Key            string    `json:"key"`
	Kind           string    `json:"kind"`
	Code           string    `json:"code"`
	Severity       string    `json:"severity"`
	Message        string    `json:"message"`
	Details        any       `json:"details"`
	DepositID      *string   `json:"depositId"`
	WithdrawalID   *string   `json:"withdrawalId"`
	EventID        *string   `json:"eventId"`
	Status         string    `json:"status"`
	Occurrences    int64     `json:"occurrences"`
	FirstSeenAt    iso.Time  `json:"firstSeenAt"`
	LastSeenAt     iso.Time  `json:"lastSeenAt"`
	ResolvedBy     *string   `json:"resolvedBy"`
	ResolvedAt     *iso.Time `json:"resolvedAt"`
	ResolutionNote *string   `json:"resolutionNote"`
}

func hexOrNil(id *bson.ObjectID) *string {
	if id == nil {
		return nil
	}
	return Str(id.Hex())
}

func ToUsdtAlertView(a *UsdtAlert) UsdtAlertView {
	v := UsdtAlertView{
		ID: a.ID.Hex(), Key: a.Key, Kind: a.Kind, Code: a.Code, Severity: a.Severity, Message: a.Message,
		DepositID: hexOrNil(a.DepositID), WithdrawalID: hexOrNil(a.WithdrawalID), EventID: hexOrNil(a.EventID),
		Status: a.Status, Occurrences: a.Occurrences, FirstSeenAt: iso.From(a.FirstSeenAt), LastSeenAt: iso.From(a.LastSeenAt),
		ResolvedBy: hexOrNil(a.ResolvedBy), ResolvedAt: iso.Ptr(a.ResolvedAt), ResolutionNote: a.ResolutionNote,
	}
	if a.Details != nil {
		v.Details = mongox.JSONValue(a.Details)
	} else {
		v.Details = map[string]any{}
	}
	return v
}

func init() {
	RegisterIndexes(
		// The replay guard: one row per signed request.
		mongox.Index{Collection: CollBeexWebhookDeliveries, Keys: bson.D{{Key: "signature", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollBeexWebhookDeliveries, Keys: bson.D{{Key: "receivedAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(webhookDeliveryKeep)},

		// One open alert per problem; a resolved one may be raised again.
		mongox.Index{Collection: CollUsdtAlerts, Keys: bson.D{{Key: "key", Value: 1}}, Options: options.Index().SetUnique(true).
			SetPartialFilterExpression(bson.M{"status": AlertOpen})},
		mongox.Index{Collection: CollUsdtAlerts, Keys: bson.D{{Key: "status", Value: 1}, {Key: "severity", Value: 1}, {Key: "lastSeenAt", Value: -1}}},
		mongox.Index{Collection: CollUsdtAlerts, Keys: bson.D{{Key: "depositId", Value: 1}}},
		mongox.Index{Collection: CollUsdtAlerts, Keys: bson.D{{Key: "withdrawalId", Value: 1}}},
	)
}
