package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollNotifications: timestamps { createdAt: true, updatedAt: false }.
const CollNotifications = "notifications"

type Notification struct {
	ID     bson.ObjectID `bson:"_id,omitempty"`
	UserID bson.ObjectID `bson:"userId"`
	Type   string        `bson:"type"`
	Title  string        `bson:"title"`
	Body   string        `bson:"body"`
	// Mixed, default {}.
	Data      map[string]any `bson:"data"`
	Read      bool           `bson:"read"`
	CreatedAt time.Time      `bson:"createdAt"`
	V         int64          `bson:"__v"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollNotifications, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollNotifications, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "read", Value: 1}}},
	)
}

type NotificationView struct {
	ID        string   `json:"id"`
	Type      string   `json:"type"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Data      any      `json:"data"`
	Read      bool     `json:"read"`
	CreatedAt iso.Time `json:"createdAt"`
}

// ToNotificationView is the mapping notification.service.ts inlines in both
// createNotification and listNotifications (data ?? {}).
func ToNotificationView(n *Notification) NotificationView {
	var data any = map[string]any{}
	if n.Data != nil {
		data = mongox.JSONValue(n.Data)
	}
	return NotificationView{
		ID:        n.ID.Hex(),
		Type:      n.Type,
		Title:     n.Title,
		Body:      n.Body,
		Data:      data,
		Read:      n.Read,
		CreatedAt: iso.From(n.CreatedAt),
	}
}
