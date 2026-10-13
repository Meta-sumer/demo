// Package audit writes the audit trail (spec section 30). A failed write is
// logged at error level rather than swallowed: a missing audit row is an
// incident.
package audit

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

const (
	UserRegister      = "user.register"
	UserLogin         = "user.login"
	UserLoginFailed   = "user.login_failed"
	UserLogout        = "user.logout"
	UserTokenRefresh  = "user.token_refresh"
	UserUpdated       = "user.updated"
	UserStatusChanged = "user.status_changed"

	AdminLogin       = "admin.login"
	AdminLoginFailed = "admin.login_failed"

	TradeCreated   = "trade.created"
	TradeRejected  = "trade.rejected"
	TradeCancelled = "trade.cancelled"
	TradeSettled   = "trade.settled"

	WalletCredit   = "wallet.credit"
	WalletDebit    = "wallet.debit"
	WalletLock     = "wallet.lock"
	WalletUnlock   = "wallet.unlock"
	WalletAdjusted = "wallet.adjusted"

	DepositCreated       = "deposit.created"
	DepositApproved      = "deposit.approved"
	DepositRejected      = "deposit.rejected"
	WithdrawalCreated    = "withdrawal.created"
	WithdrawalApproved   = "withdrawal.approved"
	WithdrawalRejected   = "withdrawal.rejected"
	PaymentMethodUpdated = "payment_method.updated"

	AssetCreated      = "asset.created"
	AssetUpdated      = "asset.updated"
	PayoutUpdated     = "payout.updated"
	RiskConfigUpdated = "risk_config.updated"

	HedgeOrderPlaced    = "hedge.order_placed"
	HedgeOrderUpdated   = "hedge.order_updated"
	HedgeOrderCancelled = "hedge.order_cancelled"

	ReconciliationAlert    = "reconciliation.alert"
	ReconciliationResolved = "reconciliation.resolved"

	SupportConversationUpdated = "support.conversation_updated"
)

// Entry is one audit row. Success defaults to true unless Failed is set.
type Entry struct {
	ActorID    string // hex; "" = none (system)
	ActorRole  string
	ActorEmail string
	IP         string
	UserAgent  *string
	RequestID  string

	Action     string
	Resource   string
	ResourceID string
	Before     any
	After      any
	Failed     bool
	Reason     string
}

// FromRequest fills the actor and request metadata from an HTTP request.
func FromRequest(actor *httpx.Actor, meta httpx.Meta) Entry {
	e := Entry{IP: meta.IP, UserAgent: meta.UserAgent, RequestID: meta.RequestID}
	if actor != nil {
		e.ActorID = actor.UserID
		e.ActorRole = actor.Role
	}
	return e
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Write stores the row and waits for it. Admin configuration changes await
// this; everything else uses Log.
func Write(ctx context.Context, e Entry) {
	doc := models.AuditLog{
		ActorRole:  e.ActorRole,
		ActorEmail: strPtr(e.ActorEmail),
		Action:     e.Action,
		Resource:   e.Resource,
		ResourceID: strPtr(e.ResourceID),
		Before:     e.Before,
		After:      e.After,
		IP:         strPtr(e.IP),
		UserAgent:  e.UserAgent,
		RequestID:  strPtr(e.RequestID),
		Success:    !e.Failed,
		Reason:     strPtr(e.Reason),
		CreatedAt:  mongox.Now(),
	}
	if id, ok := mongox.ParseID(e.ActorID); ok {
		doc.ActorID = &id
	}
	if _, err := mongox.C(models.CollAuditLogs).InsertOne(ctx, doc); err != nil {
		logx.Error("AUDIT WRITE FAILED", "action", e.Action, "resource", e.Resource, "err", err.Error())
	}
}

// Log is the fire-and-forget variant for hot paths.
func Log(e Entry) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		Write(ctx, e)
	}()
}

type Query struct {
	ActorID    *bson.ObjectID
	Action     *string
	Resource   *string
	ResourceID *string
	From       *time.Time
	To         *time.Time
	Page       int64
	Limit      int64
}

// List returns audit rows newest first, as JSON-ready maps (the same shape
// as Mongoose lean() output: _id and ObjectIDs as strings, dates as ISO).
func List(ctx context.Context, q Query) ([]map[string]any, int64, error) {
	filter := bson.M{}
	if q.ActorID != nil {
		filter["actorId"] = *q.ActorID
	}
	if q.Action != nil {
		filter["action"] = *q.Action
	}
	if q.Resource != nil {
		filter["resource"] = *q.Resource
	}
	if q.ResourceID != nil {
		filter["resourceId"] = *q.ResourceID
	}
	if q.From != nil || q.To != nil {
		rng := bson.M{}
		if q.From != nil {
			rng["$gte"] = *q.From
		}
		if q.To != nil {
			rng["$lte"] = *q.To
		}
		filter["createdAt"] = rng
	}
	coll := mongox.C(models.CollAuditLogs)
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip((q.Page-1)*q.Limit).
		SetLimit(q.Limit))
	if err != nil {
		return nil, 0, err
	}
	var raw []bson.M
	if err := cur.All(ctx, &raw); err != nil {
		return nil, 0, err
	}
	items := make([]map[string]any, 0, len(raw))
	for _, doc := range raw {
		items = append(items, mongox.JSONValue(doc).(map[string]any))
	}
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}
