package hedging

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	opsalerts "binarytrading/internal/service/alerts"
)

// Hedge alerts: a disagreement with the venue (position drift, an order
// whose outcome stays unknown, a fee that cannot be valued, a stale rate) is
// never corrected silently. It becomes one open alert row per problem (a
// repeat sighting bumps its counter), an error log line HEDGE_ALERT, and a
// badge on the admin Hedging page; operators resolve it with a note.

type hedgeAlert struct {
	Code     string
	Severity string
	// Subject identifies the problem together with Code (a symbol, an
	// order reference).
	Subject string
	Message string
	Details map[string]any
	AssetID *bson.ObjectID
}

func alerts() *mongoColl { return mongox.C(models.CollHedgeAlerts) }

func raiseAlert(ctx context.Context, a hedgeAlert) {
	ctx = context.WithoutCancel(ctx)
	if a.Severity == "" {
		a.Severity = models.AlertWarning
	}
	if a.Details == nil {
		a.Details = map[string]any{}
	}
	key := a.Code + "|" + a.Subject
	now := mongox.Now()
	// Mirrored into the platform alerts, which email the operators.
	opsalerts.Raise(ctx, opsalerts.Alert{Code: "hedge." + a.Code, Severity: a.Severity, Source: models.AlertSourceHedging,
		DedupKey: "hedge|" + key, Title: "Hedging: " + a.Code + " " + a.Subject, Detail: a.Message})
	logx.Error("HEDGE_ALERT", "code", a.Code, "subject", a.Subject, "message", a.Message, "details", a.Details)
	_, err := alerts().UpdateOne(ctx, bson.M{"key": key, "status": models.AlertOpen}, bson.M{
		"$setOnInsert": bson.M{"key": key, "code": a.Code, "status": models.AlertOpen, "assetId": a.AssetID,
			"firstSeenAt": now, "createdAt": now, "resolvedBy": nil, "resolvedAt": nil, "resolutionNote": nil},
		"$set": bson.M{"severity": a.Severity, "message": a.Message, "details": a.Details, "lastSeenAt": now, "updatedAt": now},
		"$inc": bson.M{"occurrences": 1},
	}, options.UpdateOne().SetUpsert(true))
	if err != nil && !mongox.IsDuplicateKey(err) {
		logx.Error("could not store hedge alert", "err", err.Error(), "code", a.Code)
	}
}

// clearAlert resolves an open alert automatically once the condition is gone
// (the resolution note says so).
func clearAlert(ctx context.Context, code, subject string) {
	now := mongox.Now()
	note := "condition cleared automatically"
	opsalerts.ResolveByKey(ctx, "hedge|"+code+"|"+subject, note)
	_, _ = alerts().UpdateOne(context.WithoutCancel(ctx), bson.M{"key": code + "|" + subject, "status": models.AlertOpen},
		bson.M{"$set": bson.M{"status": models.AlertResolved, "resolvedAt": now, "resolutionNote": note, "updatedAt": now}})
}

// ListAlerts returns alerts newest first ("" = any status).
func ListAlerts(ctx context.Context, status string, limit int64) ([]models.HedgeAlertView, error) {
	filter := bson.M{}
	if status != "" {
		filter["status"] = status
	}
	cur, err := alerts().Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "lastSeenAt", Value: -1}}).SetLimit(max(1, limit)))
	if err != nil {
		return nil, err
	}
	var rows []models.HedgeAlert
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]models.HedgeAlertView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToHedgeAlertView(&rows[i]))
	}
	return out, nil
}

// CountOpenAlerts is the number of open hedge alerts.
func CountOpenAlerts(ctx context.Context) (int64, error) {
	return alerts().CountDocuments(ctx, bson.M{"status": models.AlertOpen})
}

// ResolveAlert closes an open alert with an operator's note.
func ResolveAlert(ctx context.Context, id bson.ObjectID, by *bson.ObjectID, note string) (models.HedgeAlertView, error) {
	now := mongox.Now()
	var a models.HedgeAlert
	err := alerts().FindOneAndUpdate(ctx, bson.M{"_id": id, "status": models.AlertOpen}, bson.M{"$set": bson.M{
		"status": models.AlertResolved, "resolvedBy": by, "resolvedAt": now, "resolutionNote": note, "updatedAt": now,
	}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&a)
	if mongox.IsNotFound(err) {
		return models.HedgeAlertView{}, apperr.NotFoundErr(apperr.NotFound, "Open hedge alert not found")
	}
	if err != nil {
		return models.HedgeAlertView{}, err
	}
	opsalerts.ResolveByKey(ctx, "hedge|"+a.Key, "resolved on the Hedging page: "+note)
	return models.ToHedgeAlertView(&a), nil
}
