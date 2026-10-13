package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/mongox"
)

// CollAuditLogs is the append-only audit trail (spec section 30). Nothing in
// the application updates or deletes it.
const CollAuditLogs = "audit_logs"

type AuditLog struct {
	ID         bson.ObjectID  `bson:"_id,omitempty"`
	ActorID    *bson.ObjectID `bson:"actorId"`
	ActorRole  string         `bson:"actorRole"`
	ActorEmail *string        `bson:"actorEmail"`
	Action     string         `bson:"action"`
	Resource   string         `bson:"resource"`
	ResourceID *string        `bson:"resourceId"`
	Before     any            `bson:"before"`
	After      any            `bson:"after"`
	IP         *string        `bson:"ip"`
	UserAgent  *string        `bson:"userAgent"`
	RequestID  *string        `bson:"requestId"`
	Success    bool           `bson:"success"`
	Reason     *string        `bson:"reason"`
	CreatedAt  time.Time      `bson:"createdAt"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollAuditLogs, Keys: bson.D{{Key: "action", Value: 1}}},
		mongox.Index{Collection: CollAuditLogs, Keys: bson.D{{Key: "actorId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollAuditLogs, Keys: bson.D{{Key: "resource", Value: 1}, {Key: "resourceId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollAuditLogs, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}
