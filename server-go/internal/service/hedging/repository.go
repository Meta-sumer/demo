package hedging

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/risk"
)

// Port of hedge.repository.ts.

// ActiveHedgeStatuses are the statuses that still contribute to the
// platform's live hedge position.
var ActiveHedgeStatuses = []models.HedgeOrderStatus{
	models.HedgeOrderStatusPending, models.HedgeOrderStatusPartiallyFilled, models.HedgeOrderStatusFilled,
}

func orders() *mongo.Collection { return mongox.C(models.CollHedgeOrders) }

// CreateHedgeOrder inserts the order, stamping timestamps.
func CreateHedgeOrder(ctx context.Context, o *models.HedgeOrder) (*models.HedgeOrder, error) {
	now := mongox.Now()
	o.CreatedAt, o.UpdatedAt = now, now
	res, err := orders().InsertOne(ctx, o)
	if err != nil {
		return nil, err
	}
	o.ID = res.InsertedID.(bson.ObjectID)
	return o, nil
}

func FindByIdempotencyKey(ctx context.Context, key string) (*models.HedgeOrder, error) {
	return findOne(ctx, bson.M{"idempotencyKey": key})
}

func findOne(ctx context.Context, filter bson.M) (*models.HedgeOrder, error) {
	var o models.HedgeOrder
	if err := orders().FindOne(ctx, filter).Decode(&o); err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

// UpdateHedgeOrder $sets patch (bson field names) and returns the new
// document, or nil when it no longer exists.
func UpdateHedgeOrder(ctx context.Context, id bson.ObjectID, patch bson.M) (*models.HedgeOrder, error) {
	set := bson.M{"updatedAt": mongox.Now()}
	for k, v := range patch {
		set[k] = v
	}
	var o models.HedgeOrder
	err := orders().FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&o)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &o, nil
}

func parseAssetID(assetID string) (bson.ObjectID, error) {
	id, ok := mongox.ParseID(assetID)
	if !ok {
		// new Types.ObjectId(bad) throws a BSONError in TS (a 500).
		return id, apperr.Internal("input must be a 24 character hex string, 12 byte Uint8Array, or an integer")
	}
	return id, nil
}

// GetSignedHedgeNotional is the signed hedge notional held on an asset.
// Positive means the platform is long the underlying, negative means short.
func GetSignedHedgeNotional(ctx context.Context, assetID string) (int64, error) {
	id, err := parseAssetID(assetID)
	if err != nil {
		return 0, err
	}
	cur, err := orders().Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"assetId": id, "status": bson.M{"$in": ActiveHedgeStatuses}, "closedAt": nil}},
		bson.M{"$group": bson.M{"_id": "$side", "filled": bson.M{"$sum": "$filledNotional"}}},
	})
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Side   models.Direction `bson:"_id"`
		Filled int64            `bson:"filled"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	var signed int64
	for _, r := range rows {
		if r.Side == models.DirectionUp {
			signed += r.Filled
		} else {
			signed -= r.Filled
		}
	}
	return signed, nil
}

// GetHedgedNotionalByAsset delegates to risk, where the query lives to avoid
// an import cycle.
func GetHedgedNotionalByAsset(ctx context.Context) (map[string]int64, error) {
	return risk.GetHedgedNotionalByAsset(ctx)
}

type HedgeOrderQuery struct {
	AssetID string // "" = any
	Status  string // "" = any
	From    *time.Time
	To      *time.Time
	Page    int64
	Limit   int64
}

func ListHedgeOrders(ctx context.Context, q HedgeOrderQuery) ([]models.HedgeOrder, int64, error) {
	filter := bson.M{}
	if q.AssetID != "" {
		id, err := parseAssetID(q.AssetID)
		if err != nil {
			return nil, 0, err
		}
		filter["assetId"] = id
	}
	if q.Status != "" {
		filter["status"] = q.Status
	}
	if q.From != nil || q.To != nil {
		r := bson.M{}
		if q.From != nil {
			r["$gte"] = *q.From
		}
		if q.To != nil {
			r["$lte"] = *q.To
		}
		filter["createdAt"] = r
	}
	page, limit := max(q.Page, 1), max(q.Limit, 1)
	cur, err := orders().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	items := []models.HedgeOrder{}
	if err := cur.All(ctx, &items); err != nil {
		return nil, 0, err
	}
	total, err := orders().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// FindPendingOrders returns up to limit (default 200) non-final orders, oldest first.
func FindPendingOrders(ctx context.Context, limit int64) ([]models.HedgeOrder, error) {
	if limit <= 0 {
		limit = 200
	}
	cur, err := orders().Find(ctx,
		bson.M{"status": bson.M{"$in": bson.A{models.HedgeOrderStatusPending, models.HedgeOrderStatusPartiallyFilled}}},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	items := []models.HedgeOrder{}
	return items, cur.All(ctx, &items)
}

type HedgePnlSummary struct {
	Realised   int64 `json:"realised" bson:"realised"`
	Unrealised int64 `json:"unrealised" bson:"unrealised"`
	Orders     int64 `json:"orders" bson:"orders"`
}

func GetHedgePnlSummary(ctx context.Context, from *time.Time) (HedgePnlSummary, error) {
	match := bson.M{}
	if from != nil {
		match["createdAt"] = bson.M{"$gte": *from}
	}
	cur, err := orders().Aggregate(ctx, bson.A{
		bson.M{"$match": match},
		bson.M{"$group": bson.M{
			"_id":        nil,
			"realised":   bson.M{"$sum": "$realisedPnl"},
			"unrealised": bson.M{"$sum": "$unrealisedPnl"},
			"orders":     bson.M{"$sum": 1},
		}},
	})
	if err != nil {
		return HedgePnlSummary{}, err
	}
	var rows []HedgePnlSummary
	if err := cur.All(ctx, &rows); err != nil {
		return HedgePnlSummary{}, err
	}
	if len(rows) == 0 {
		return HedgePnlSummary{}, nil
	}
	return rows[0], nil
}

// FindHedgeOrderByID returns nil for an unknown or malformed id.
func FindHedgeOrderByID(ctx context.Context, id string) (*models.HedgeOrder, error) {
	oid, ok := mongox.ParseID(id)
	if !ok {
		return nil, nil
	}
	return findOne(ctx, bson.M{"_id": oid})
}
