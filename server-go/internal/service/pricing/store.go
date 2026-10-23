package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// The pricing model store. MongoDB (pricing_models) is the record; versions
// are append-only and exactly one is active. The calibration shipped with the
// code (default_model.json, written by cmd/calibrate) is published on first
// use, and replaces the active version when it is newer, so a release carries
// its calibration; `go run ./cmd/calibrate -publish` publishes one directly.

//go:embed default_model.json
var embeddedModel []byte

// EmbeddedModel is the calibration compiled into this binary.
func EmbeddedModel() (*models.PricingModel, error) {
	var m models.PricingModel
	if err := json.Unmarshal(embeddedModel, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

const modelCacheTTL = 10 * time.Second

// ErrNoModel means no model is published and none is embedded: every
// DYNAMIC price is refused (MODEL_NOT_AVAILABLE).
var ErrNoModel = errors.New("no pricing model is published")

var (
	modelMu     sync.Mutex
	modelCache  *models.PricingModel
	modelAt     time.Time
	modelForced *models.PricingModel
)

// InvalidateModelCache drops the cached active model.
func InvalidateModelCache() {
	modelMu.Lock()
	modelCache = nil
	modelMu.Unlock()
}

// SetModelForTest makes ActiveModel return m (nil: back to the store).
func SetModelForTest(m *models.PricingModel) {
	modelMu.Lock()
	modelForced = m
	modelCache = nil
	modelMu.Unlock()
}

// ActiveModel returns the active model (shared, read-only), cached for 10 s.
func ActiveModel(ctx context.Context) (*models.PricingModel, error) {
	modelMu.Lock()
	if modelForced != nil {
		m := modelForced
		modelMu.Unlock()
		return m, nil
	}
	if modelCache != nil && time.Since(modelAt) < modelCacheTTL {
		m := modelCache
		modelMu.Unlock()
		return m, nil
	}
	modelMu.Unlock()

	m, err := loadActiveModel(ctx)
	if err != nil {
		return nil, err
	}
	modelMu.Lock()
	modelCache, modelAt = m, time.Now()
	modelMu.Unlock()
	return m, nil
}

func loadActiveModel(ctx context.Context) (*models.PricingModel, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	coll := mongox.C(models.CollPricingModels)
	var active models.PricingModel
	err := coll.FindOne(ctx, bson.M{"active": true}).Decode(&active)
	if err != nil && !mongox.IsNotFound(err) {
		return nil, err
	}
	embedded, eerr := EmbeddedModel()
	if eerr != nil {
		logx.Error("embedded pricing model is unreadable", "err", eerr.Error())
	}
	shipNewer := eerr == nil && len(embedded.Entries) > 0 &&
		(mongox.IsNotFound(err) || embedded.Version > active.Version)
	if !shipNewer {
		if mongox.IsNotFound(err) {
			return nil, ErrNoModel
		}
		return &active, nil
	}
	if _, perr := PublishModel(ctx, *embedded, "embedded:"+embedded.Hash); perr != nil {
		// Another instance may have published it first.
		var again models.PricingModel
		if ferr := coll.FindOne(ctx, bson.M{"active": true}).Decode(&again); ferr == nil {
			return &again, nil
		}
		return nil, perr
	}
	if err := coll.FindOne(ctx, bson.M{"active": true}).Decode(&active); err != nil {
		return nil, err
	}
	return &active, nil
}

// PublishModel inserts m as the next active version (its own version when
// that is free and higher, otherwise latest + 1) and closes the previous one.
// Returns the version published. If the insert fails the previous version is
// re-activated.
func PublishModel(ctx context.Context, m models.PricingModel, by string) (int64, error) {
	ctx = context.WithoutCancel(ctx)
	coll := mongox.C(models.CollPricingModels)
	var latest models.PricingModel
	err := coll.FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})).Decode(&latest)
	if err != nil && !mongox.IsNotFound(err) {
		return 0, err
	}
	if m.Version <= latest.Version {
		m.Version = latest.Version + 1
	}
	var current models.PricingModel
	hadActive := coll.FindOne(ctx, bson.M{"active": true}).Decode(&current) == nil
	now := mongox.Now()
	if hadActive {
		if _, err := coll.UpdateOne(ctx, bson.M{"_id": current.ID, "active": true},
			bson.M{"$set": bson.M{"active": false, "updatedAt": now}}); err != nil {
			return 0, err
		}
	}
	m.ID = bson.NewObjectID()
	m.Active = true
	m.PublishedBy = by
	m.CreatedAt, m.UpdatedAt = now, now
	if m.GeneratedAt.IsZero() {
		m.GeneratedAt = now
	}
	if m.Entries == nil {
		m.Entries = []models.PricingModelEntry{}
	}
	if _, err := coll.InsertOne(ctx, m); err != nil {
		if hadActive {
			_, _ = coll.UpdateOne(ctx, bson.M{"_id": current.ID}, bson.M{"$set": bson.M{"active": true, "updatedAt": mongox.Now()}})
		}
		return 0, err
	}
	InvalidateModelCache()
	logx.Warn("pricing model published", "version", m.Version, "by", by, "entries", len(m.Entries), "hash", m.Hash)
	return m.Version, nil
}

// ListModelVersions returns the newest versions first, without entries.
func ListModelVersions(ctx context.Context, limit int64) ([]models.PricingModel, error) {
	if limit <= 0 {
		limit = 20
	}
	cur, err := mongox.C(models.CollPricingModels).Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "version", Value: -1}}).SetLimit(limit).SetProjection(bson.M{"entries": 0}))
	if err != nil {
		return nil, err
	}
	out := []models.PricingModel{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// FindEntry returns the entry for a provider symbol and expiry, or nil.
func FindEntry(m *models.PricingModel, providerSymbol string, expirySeconds int64) *models.PricingModelEntry {
	if m == nil {
		return nil
	}
	sym := strings.ToLower(strings.TrimSpace(providerSymbol))
	for i := range m.Entries {
		e := &m.Entries[i]
		if e.ProviderSymbol == sym && e.ExpirySeconds == expirySeconds {
			return e
		}
	}
	return nil
}
