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

type mongoColl = mongo.Collection

// ActiveHedgeStatuses are the working (contingent) statuses: an order in one
// of them may still fill, but only its fills count as protection.
var ActiveHedgeStatuses = []models.HedgeOrderStatus{
	models.HedgeOrderStatusPending, models.HedgeOrderStatusAccepted, models.HedgeOrderStatusPartiallyFilled,
}

func orders() *mongo.Collection { return mongox.C(models.CollHedgeOrders) }

// CreateHedgeOrder inserts the order, stamping timestamps. A second working
// order for the same asset fails with a duplicate key (workingSlot).
func CreateHedgeOrder(ctx context.Context, o *models.HedgeOrder) (*models.HedgeOrder, error) {
	now := mongox.Now()
	o.CreatedAt, o.UpdatedAt = now, now
	if o.ID.IsZero() {
		o.ID = bson.NewObjectID()
	}
	if o.Decision.Legs == nil {
		o.Decision.Legs = []models.HedgeLeg{}
	}
	if _, err := orders().InsertOne(ctx, o); err != nil {
		return nil, err
	}
	return o, nil
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

// FindByIdempotencyKey looks an order up by its idempotency key (which is
// its client order id).
func FindByIdempotencyKey(ctx context.Context, key string) (*models.HedgeOrder, error) {
	return findOne(ctx, bson.M{"idempotencyKey": key})
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
		return id, apperr.Internal("input must be a 24 character hex string, 12 byte Uint8Array, or an integer")
	}
	return id, nil
}

// GetSignedHedgeNotional is the signed filled hedge of an asset at the last
// mark, minor units (+ long). Working orders do not count.
func GetSignedHedgeNotional(ctx context.Context, assetID string) (int64, error) {
	id, err := parseAssetID(assetID)
	if err != nil {
		return 0, err
	}
	p, err := GetPosition(ctx, id)
	if err != nil || p == nil {
		return 0, err
	}
	return sgn(p.QtyE8) * p.NotionalInr, nil
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

// FindWorkingOrders returns up to limit working orders, oldest first.
func FindWorkingOrders(ctx context.Context, limit int64) ([]models.HedgeOrder, error) {
	if limit <= 0 {
		limit = 200
	}
	cur, err := orders().Find(ctx, bson.M{"status": bson.M{"$in": ActiveHedgeStatuses}},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	items := []models.HedgeOrder{}
	return items, cur.All(ctx, &items)
}

// FindPendingOrders is FindWorkingOrders (kept for callers of the old name).
func FindPendingOrders(ctx context.Context, limit int64) ([]models.HedgeOrder, error) {
	return FindWorkingOrders(ctx, limit)
}

// findOrdersMissingFills: final orders whose fills the ledger does not hold
// yet (fills can lag the order state).
func findOrdersMissingFills(ctx context.Context, limit int64) ([]models.HedgeOrder, error) {
	cur, err := orders().Find(ctx, bson.M{
		"status":        bson.M{"$in": bson.A{models.HedgeOrderStatusFilled, models.HedgeOrderStatusCancelled}},
		"$expr":         bson.M{"$lt": bson.A{"$fillsQtyE8", "$filledQtyE8"}},
		"venue":         bson.M{"$exists": true},
		"finalAt":       bson.M{"$ne": nil},
		"clientOrderId": bson.M{"$type": "string"},
	}, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	items := []models.HedgeOrder{}
	return items, cur.All(ctx, &items)
}

func findWorkingOrderFor(ctx context.Context, assetID bson.ObjectID) (*models.HedgeOrder, error) {
	return findOne(ctx, bson.M{"assetId": assetID, "status": bson.M{"$in": ActiveHedgeStatuses}})
}

// FindHedgeOrderByID returns nil for an unknown or malformed id.
func FindHedgeOrderByID(ctx context.Context, id string) (*models.HedgeOrder, error) {
	oid, ok := mongox.ParseID(id)
	if !ok {
		return nil, nil
	}
	return findOne(ctx, bson.M{"_id": oid})
}

// ListFills returns ledger rows newest first.
func ListFills(ctx context.Context, assetID string, page, limit int64) ([]models.HedgeFill, int64, error) {
	filter := bson.M{}
	if assetID != "" {
		id, err := parseAssetID(assetID)
		if err != nil {
			return nil, 0, err
		}
		filter["assetId"] = id
	}
	page, limit = max(page, 1), max(limit, 1)
	cur, err := fills().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "tradeTime", Value: -1}, {Key: "tradeId", Value: -1}}).SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	items := []models.HedgeFill{}
	if err := cur.All(ctx, &items); err != nil {
		return nil, 0, err
	}
	total, err := fills().CountDocuments(ctx, filter)
	return items, total, err
}
