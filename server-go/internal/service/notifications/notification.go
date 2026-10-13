// Package notifications stores in-app notifications and pushes them to the
// owner's websocket channel.
package notifications

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/ws"
)

// Input is a notification to create. Data nil is stored as {}.
type Input struct {
	Type  string
	Title string
	Body  string
	Data  map[string]any
}

// CreateNotification stores a notification and emits notification.created.
func CreateNotification(ctx context.Context, userID string, in Input) (models.NotificationView, error) {
	uid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return models.NotificationView{}, err
	}
	data := in.Data
	if data == nil {
		data = map[string]any{}
	}
	doc := models.Notification{
		UserID:    uid,
		Type:      in.Type,
		Title:     in.Title,
		Body:      in.Body,
		Data:      data,
		Read:      false,
		CreatedAt: mongox.Now(),
	}
	res, err := mongox.C(models.CollNotifications).InsertOne(ctx, doc)
	if err != nil {
		return models.NotificationView{}, err
	}
	doc.ID, _ = res.InsertedID.(bson.ObjectID)
	view := models.ToNotificationView(&doc)
	ws.Emit([]string{config.ChUser(userID)}, config.EvNotificationCreated, view)
	return view, nil
}

// List is one page of a user's notifications plus their unread count.
type List struct {
	Items  []models.NotificationView
	Total  int64
	Unread int64
}

// ListNotifications returns notifications newest first.
func ListNotifications(ctx context.Context, userID string, page, limit int64) (List, error) {
	uid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return List{}, err
	}
	coll := mongox.C(models.CollNotifications)
	filter := bson.M{"userId": uid}
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip((page-1)*limit).
		SetLimit(limit))
	if err != nil {
		return List{}, err
	}
	var rows []models.Notification
	if err := cur.All(ctx, &rows); err != nil {
		return List{}, err
	}
	items := make([]models.NotificationView, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToNotificationView(&rows[i]))
	}
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return List{}, err
	}
	unread, err := coll.CountDocuments(ctx, bson.M{"userId": uid, "read": false})
	if err != nil {
		return List{}, err
	}
	return List{Items: items, Total: total, Unread: unread}, nil
}

// MarkNotificationRead marks one of the user's notifications read. Unknown
// ids are silently ignored.
func MarkNotificationRead(ctx context.Context, userID, notificationID string) error {
	uid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return err
	}
	nid, err := bson.ObjectIDFromHex(notificationID)
	if err != nil {
		return err
	}
	// Notifications have no updatedAt (timestamps: createdAt only).
	_, err = mongox.C(models.CollNotifications).UpdateOne(ctx,
		bson.M{"_id": nid, "userId": uid}, bson.M{"$set": bson.M{"read": true}})
	return err
}

// MarkAllNotificationsRead returns how many were changed.
func MarkAllNotificationsRead(ctx context.Context, userID string) (int64, error) {
	uid, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return 0, err
	}
	res, err := mongox.C(models.CollNotifications).UpdateMany(ctx,
		bson.M{"userId": uid, "read": false}, bson.M{"$set": bson.M{"read": true}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}
