// Package payouts is the payout configuration (spec section 24).
//
// A change never mutates the live row: the current version is closed off and a
// new one is inserted. Trades store the payout percent and config version they
// were accepted under, so changing a payout can never revalue open or
// historical trades (rule 11).
package payouts

import (
	"context"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/assets"
)

const cacheTTL = 2 * time.Second

var (
	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheRows []*models.PayoutConfig
)

// InvalidatePayoutCache drops the cached active rows.
func InvalidatePayoutCache() {
	cacheMu.Lock()
	cacheRows = nil
	cacheMu.Unlock()
}

func loadActive(ctx context.Context) ([]*models.PayoutConfig, error) {
	cacheMu.Lock()
	if cacheRows != nil && time.Since(cacheAt) < cacheTTL {
		r := cacheRows
		cacheMu.Unlock()
		return r, nil
	}
	cacheMu.Unlock()

	cur, err := mongox.C(models.CollPayoutConfigs).Find(ctx, bson.M{"active": true})
	if err != nil {
		return nil, err
	}
	rows := []*models.PayoutConfig{}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	cacheMu.Lock()
	cacheRows = rows
	cacheAt = time.Now()
	cacheMu.Unlock()
	return rows, nil
}

// FindActivePayout returns the active row (shared, read-only) or nil.
func FindActivePayout(ctx context.Context, assetID string, expirySeconds int64) (*models.PayoutConfig, error) {
	rows, err := loadActive(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.AssetID.Hex() == assetID && r.ExpirySeconds == expirySeconds {
			return r, nil
		}
	}
	return nil, nil
}

// RequireActivePayout fails with PAYOUT_NOT_CONFIGURED (409).
func RequireActivePayout(ctx context.Context, assetID string, expirySeconds int64) (*models.PayoutConfig, error) {
	p, err := FindActivePayout(ctx, assetID, expirySeconds)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperr.New(apperr.PayoutNotConfigured, "No payout is configured for that expiry", 409)
	}
	return p, nil
}

// ListActivePayouts: assetID "" lists every asset. Sorted by expiry.
func ListActivePayouts(ctx context.Context, assetID string) ([]models.PayoutView, error) {
	rows, err := loadActive(ctx)
	if err != nil {
		return nil, err
	}
	picked := make([]*models.PayoutConfig, 0, len(rows))
	for _, r := range rows {
		if assetID == "" || r.AssetID.Hex() == assetID {
			picked = append(picked, r)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].ExpirySeconds < picked[j].ExpirySeconds })
	out := make([]models.PayoutView, 0, len(picked))
	for _, r := range picked {
		out = append(out, models.ToPayoutView(r))
	}
	return out, nil
}

// ListPayoutVersions returns every version for an asset, optionally one expiry.
func ListPayoutVersions(ctx context.Context, assetID string, expirySeconds *int64) ([]models.PayoutView, error) {
	out := []models.PayoutView{}
	id, ok := mongox.ParseID(assetID)
	if !ok {
		return nil, apperr.Validation("Invalid asset id", nil)
	}
	filter := bson.M{"assetId": id}
	if expirySeconds != nil {
		filter["expirySeconds"] = *expirySeconds
	}
	cur, err := mongox.C(models.CollPayoutConfigs).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "expirySeconds", Value: 1}, {Key: "version", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var rows []models.PayoutConfig
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for i := range rows {
		out = append(out, models.ToPayoutView(&rows[i]))
	}
	return out, nil
}

type UpsertPayoutInput struct {
	AssetID       string
	ExpirySeconds int64
	PayoutPercent float64
	MinStake      *int64
	MaxStake      *int64
	Note          *string
}

// UpsertPayout supersedes the active row and inserts the next version.
// before is nil when no version was active.
func UpsertPayout(ctx context.Context, in UpsertPayoutInput, actorID string) (before *models.PayoutView, after models.PayoutView, err error) {
	asset, err := assets.RequireAssetDoc(ctx, in.AssetID)
	if err != nil {
		return nil, after, err
	}
	if !slices.Contains(asset.SupportedExpiries, in.ExpirySeconds) {
		return nil, after, apperr.Validation("Expiry "+strconv.FormatInt(in.ExpirySeconds, 10)+
			"s is not in the supported expiries for "+asset.Symbol, nil)
	}
	if in.PayoutPercent < 0 || in.PayoutPercent > 500 {
		return nil, after, apperr.Validation("payoutPercent must be between 0 and 500", nil)
	}

	coll := mongox.C(models.CollPayoutConfigs)
	var current *models.PayoutConfig
	{
		var c models.PayoutConfig
		err := coll.FindOne(ctx, bson.M{"assetId": asset.ID, "expirySeconds": in.ExpirySeconds, "active": true}).Decode(&c)
		if err == nil {
			current = &c
		} else if !mongox.IsNotFound(err) {
			return nil, after, err
		}
	}
	var latestVersion int64
	{
		var l models.PayoutConfig
		err := coll.FindOne(ctx, bson.M{"assetId": asset.ID, "expirySeconds": in.ExpirySeconds},
			options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})).Decode(&l)
		if err == nil {
			latestVersion = l.Version
		} else if !mongox.IsNotFound(err) {
			return nil, after, err
		}
	}

	now := mongox.Now()
	// Close the current version first: the partial unique index allows only
	// one active row per (asset, expiry).
	if current != nil {
		if _, err := coll.UpdateOne(ctx, bson.M{"_id": current.ID},
			bson.M{"$set": bson.M{"active": false, "effectiveTo": now, "updatedAt": now}}); err != nil {
			return nil, after, err
		}
	}

	var createdBy *bson.ObjectID
	if actor, ok := mongox.ParseID(actorID); ok {
		createdBy = &actor
	}
	doc := models.PayoutConfig{
		AssetID:       asset.ID,
		ExpirySeconds: in.ExpirySeconds,
		PayoutPercent: in.PayoutPercent,
		MinStake:      in.MinStake,
		MaxStake:      in.MaxStake,
		Version:       latestVersion + 1,
		Active:        true,
		EffectiveFrom: now,
		EffectiveTo:   nil,
		CreatedBy:     createdBy,
		Note:          in.Note,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	res, err := coll.InsertOne(ctx, doc)
	if err != nil {
		return nil, after, err
	}
	doc.ID = res.InsertedID.(bson.ObjectID)

	InvalidatePayoutCache()
	logx.Info("payout configuration versioned", "symbol", asset.Symbol,
		"expirySeconds", in.ExpirySeconds, "payoutPercent", in.PayoutPercent)

	if current != nil {
		v := models.ToPayoutView(current)
		before = &v
	}
	return before, models.ToPayoutView(&doc), nil
}

// DeactivatePayout closes a payout row.
func DeactivatePayout(ctx context.Context, payoutID string) (models.PayoutView, error) {
	notFound := apperr.NotFoundErr(apperr.NotFound, "Payout configuration not found")
	id, ok := mongox.ParseID(payoutID)
	if !ok {
		return models.PayoutView{}, notFound
	}
	now := mongox.Now()
	var row models.PayoutConfig
	err := mongox.C(models.CollPayoutConfigs).FindOneAndUpdate(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"active": false, "effectiveTo": now, "updatedAt": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&row)
	if err != nil {
		if mongox.IsNotFound(err) {
			return models.PayoutView{}, notFound
		}
		return models.PayoutView{}, err
	}
	InvalidatePayoutCache()
	return models.ToPayoutView(&row), nil
}
