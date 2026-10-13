// Package riskconfig is the live platform risk configuration (spec sections
// 13, 15, 17).
//
// Read on every trade, so it is cached for a short TTL. Writes are versioned:
// the active row is superseded rather than edited, which is what lets a past
// risk decision be replayed against the configuration that produced it.
package riskconfig

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/sync/singleflight"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

const cacheTTL = 2 * time.Second

var (
	cacheMu     sync.Mutex
	cacheAt     time.Time
	cacheConfig *models.RiskConfig

	// In-flight read, shared by concurrent callers. Without this, a burst of
	// trades against a cold cache would each issue their own query and - on a
	// fresh database - each try to bootstrap version 1.
	inFlight singleflight.Group
)

// InvalidateRiskConfigCache drops the cached configuration.
func InvalidateRiskConfigCache() {
	cacheMu.Lock()
	cacheConfig = nil
	cacheMu.Unlock()
}

// clone returns a copy the caller may mutate without touching the cache.
func clone(c *models.RiskConfig) *models.RiskConfig {
	out := *c
	out.HedgeLevels = append([]models.HedgeLevel(nil), c.HedgeLevels...)
	return &out
}

// GetRiskConfig returns the active configuration (cached for 2s). The result
// is a private copy.
func GetRiskConfig(ctx context.Context) (*models.RiskConfig, error) {
	cacheMu.Lock()
	if cacheConfig != nil && time.Since(cacheAt) < cacheTTL {
		c := clone(cacheConfig)
		cacheMu.Unlock()
		return c, nil
	}
	cacheMu.Unlock()

	v, err, _ := inFlight.Do("load", func() (any, error) {
		return loadRiskConfig(context.WithoutCancel(ctx))
	})
	if err != nil {
		return nil, err
	}
	return clone(v.(*models.RiskConfig)), nil
}

func loadRiskConfig(ctx context.Context) (*models.RiskConfig, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// Decoded over the schema defaults rather than into a zero value,
	// deliberately: a field added to the schema since the stored version was
	// written then comes back with its default. A zero value would silently
	// mean "off" for a boolean - which is how a feature ships disabled on
	// every existing deployment.
	doc := models.DefaultRiskConfig()
	err := mongox.C(models.CollRiskConfigs).
		FindOne(ctx, bson.M{"scope": models.RiskScopeGlobal, "active": true}).
		Decode(&doc)
	var cfg *models.RiskConfig
	switch {
	case err == nil:
		cfg = &doc
	case mongox.IsNotFound(err):
		cfg, err = bootstrapDefaultConfig(ctx)
		if err != nil {
			return nil, err
		}
	default:
		return nil, err
	}

	cacheMu.Lock()
	cacheConfig = cfg
	cacheAt = time.Now()
	cacheMu.Unlock()
	return cfg, nil
}

// bootstrapDefaultConfig creates version 1 from the environment defaults on
// first boot. Two processes can reach this at the same moment on a fresh
// database; the unique index on the active row makes one of them lose, and the
// loser reads what the winner wrote rather than failing the request.
func bootstrapDefaultConfig(ctx context.Context) (*models.RiskConfig, error) {
	doc := models.DefaultRiskConfig()
	if config.E != nil {
		doc.FeedHealthyMaxMs = config.E.FeedHealthyMaxMs
		doc.FeedDegradedMaxMs = config.E.FeedDegradedMaxMs
		doc.FeedStaleMaxMs = config.E.FeedStaleMaxMs
		doc.FeedTickMaxAgeMs = config.E.FeedTickMaxAgeMs
	}
	doc.Note = models.Str("Bootstrapped from environment defaults")
	now := mongox.Now()
	doc.CreatedAt, doc.UpdatedAt = now, now

	res, err := mongox.C(models.CollRiskConfigs).InsertOne(ctx, doc)
	if err != nil {
		if mongox.IsDuplicateKey(err) {
			var existing models.RiskConfig
			ferr := mongox.C(models.CollRiskConfigs).
				FindOne(ctx, bson.M{"scope": models.RiskScopeGlobal, "active": true}).
				Decode(&existing)
			if ferr == nil {
				return &existing, nil
			}
		}
		return nil, err
	}
	doc.ID = res.InsertedID.(bson.ObjectID)
	logx.Info("bootstrapped default risk configuration")
	return &doc, nil
}

// RiskConfigPatch: every field optional (nil = unchanged).
type RiskConfigPatch struct {
	MaxAssetExposure                 *int64              `json:"maxAssetExposure"`
	MaxUserExposure                  *int64              `json:"maxUserExposure"`
	MaxGrossExposure                 *int64              `json:"maxGrossExposure"`
	MinTradeAmount                   *int64              `json:"minTradeAmount"`
	MaxTradeAmount                   *int64              `json:"maxTradeAmount"`
	MaxOpenTradesPerUser             *int64              `json:"maxOpenTradesPerUser"`
	MaxUserAssetConcentrationPercent *float64            `json:"maxUserAssetConcentrationPercent"`
	HedgeThresholdPercent            *float64            `json:"hedgeThresholdPercent"`
	CriticalExposurePercent          *float64            `json:"criticalExposurePercent"`
	HedgeLevels                      []models.HedgeLevel `json:"hedgeLevels"` // nil = unchanged
	MinSecondsToExpiryForHedge       *int64              `json:"minSecondsToExpiryForHedge"`
	VolatilityHedgeBoost             *float64            `json:"volatilityHedgeBoost"`
	SlippageTolerancePercent         *float64            `json:"slippageTolerancePercent"`
	EarlyCloseEnabled                *bool               `json:"earlyCloseEnabled"`
	EarlyCloseFeePercent             *float64            `json:"earlyCloseFeePercent"`
	MinSecondsRemainingForEarlyClose *int64              `json:"minSecondsRemainingForEarlyClose"`
	DrawPolicy                       *models.DrawPolicy  `json:"drawPolicy"`
	FeedHealthyMaxMs                 *int64              `json:"feedHealthyMaxMs"`
	FeedDegradedMaxMs                *int64              `json:"feedDegradedMaxMs"`
	FeedStaleMaxMs                   *int64              `json:"feedStaleMaxMs"`
	FeedTickMaxAgeMs                 *int64              `json:"feedTickMaxAgeMs"`
	HaltTradingOnStaleFeed           *bool               `json:"haltTradingOnStaleFeed"`
	HaltTradingOnDegradedFeed        *bool               `json:"haltTradingOnDegradedFeed"`
	TradingHalted                    *bool               `json:"tradingHalted"`
}

func setIf[T any](dst *T, v *T) {
	if v != nil {
		*dst = *v
	}
}

// Apply overlays the patch onto c.
func (p *RiskConfigPatch) Apply(c *models.RiskConfig) {
	setIf(&c.MaxAssetExposure, p.MaxAssetExposure)
	setIf(&c.MaxUserExposure, p.MaxUserExposure)
	setIf(&c.MaxGrossExposure, p.MaxGrossExposure)
	setIf(&c.MinTradeAmount, p.MinTradeAmount)
	setIf(&c.MaxTradeAmount, p.MaxTradeAmount)
	setIf(&c.MaxOpenTradesPerUser, p.MaxOpenTradesPerUser)
	setIf(&c.MaxUserAssetConcentrationPercent, p.MaxUserAssetConcentrationPercent)
	setIf(&c.HedgeThresholdPercent, p.HedgeThresholdPercent)
	setIf(&c.CriticalExposurePercent, p.CriticalExposurePercent)
	if p.HedgeLevels != nil {
		c.HedgeLevels = append([]models.HedgeLevel(nil), p.HedgeLevels...)
	}
	setIf(&c.MinSecondsToExpiryForHedge, p.MinSecondsToExpiryForHedge)
	setIf(&c.VolatilityHedgeBoost, p.VolatilityHedgeBoost)
	setIf(&c.SlippageTolerancePercent, p.SlippageTolerancePercent)
	setIf(&c.EarlyCloseEnabled, p.EarlyCloseEnabled)
	setIf(&c.EarlyCloseFeePercent, p.EarlyCloseFeePercent)
	setIf(&c.MinSecondsRemainingForEarlyClose, p.MinSecondsRemainingForEarlyClose)
	setIf(&c.DrawPolicy, p.DrawPolicy)
	setIf(&c.FeedHealthyMaxMs, p.FeedHealthyMaxMs)
	setIf(&c.FeedDegradedMaxMs, p.FeedDegradedMaxMs)
	setIf(&c.FeedStaleMaxMs, p.FeedStaleMaxMs)
	setIf(&c.FeedTickMaxAgeMs, p.FeedTickMaxAgeMs)
	setIf(&c.HaltTradingOnStaleFeed, p.HaltTradingOnStaleFeed)
	setIf(&c.HaltTradingOnDegradedFeed, p.HaltTradingOnDegradedFeed)
	setIf(&c.TradingHalted, p.TradingHalted)
}

// UpdateRiskConfig publishes the next version and returns (before, after).
//
// The active row is closed off BEFORE the new one is inserted: the partial
// unique index allows only one active configuration, so inserting first would
// collide with the row still marked active. If the insert then fails, the
// previous version is reinstated rather than leaving the platform with none.
func UpdateRiskConfig(ctx context.Context, patch RiskConfigPatch, actorID string, note *string) (before, after *models.RiskConfig, err error) {
	before, err = GetRiskConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	next := clone(before)
	patch.Apply(next)
	if err := ValidateConfig(next); err != nil {
		return nil, nil, err
	}

	nextVersion := before.Version + 1
	coll := mongox.C(models.CollRiskConfigs)

	if _, err := coll.UpdateOne(ctx, bson.M{"_id": before.ID},
		bson.M{"$set": bson.M{"active": false, "updatedAt": mongox.Now()}}); err != nil {
		return nil, nil, err
	}

	actor, _ := mongox.ParseID(actorID)
	now := mongox.Now()
	next.ID = bson.NilObjectID
	next.Scope = models.RiskScopeGlobal
	next.Version = nextVersion
	next.Active = true
	next.UpdatedBy = &actor
	next.Note = note
	next.CreatedAt, next.UpdatedAt = now, now

	res, err := coll.InsertOne(ctx, next)
	if err != nil {
		_, _ = coll.UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": before.ID},
			bson.M{"$set": bson.M{"active": true, "updatedAt": mongox.Now()}})
		InvalidateRiskConfigCache()
		return nil, nil, err
	}
	next.ID = res.InsertedID.(bson.ObjectID)

	InvalidateRiskConfigCache()
	logx.Warn("risk configuration updated", "version", nextVersion, "actorId", actorID)
	return before, next, nil
}

// ValidateConfig checks the cross-field rules a patched configuration must
// satisfy (the TS validatePatch).
func ValidateConfig(c *models.RiskConfig) error {
	if !(c.FeedHealthyMaxMs < c.FeedDegradedMaxMs && c.FeedDegradedMaxMs < c.FeedStaleMaxMs) {
		return apperr.Validation("Feed thresholds must satisfy healthy < degraded < stale", nil)
	}
	if c.MinTradeAmount > c.MaxTradeAmount {
		return apperr.Validation("minTradeAmount cannot exceed maxTradeAmount", nil)
	}
	if c.HedgeThresholdPercent < 0 || c.HedgeThresholdPercent > 100 {
		return apperr.Validation("hedgeThresholdPercent must be between 0 and 100", nil)
	}
	sorted := append([]models.HedgeLevel(nil), c.HedgeLevels...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].FromPercent < sorted[j].FromPercent })
	for i, level := range sorted {
		if level.FromPercent >= level.ToPercent {
			return apperr.Validation("Hedge level "+level.Name+" has an empty band", nil)
		}
		if i > 0 && level.FromPercent < sorted[i-1].ToPercent {
			return apperr.Validation("Hedge levels overlap around "+strconv.FormatFloat(level.FromPercent, 'f', -1, 64)+"%", nil)
		}
	}
	return nil
}

// ListRiskConfigVersions returns the newest versions first (TS default limit 50).
func ListRiskConfigVersions(ctx context.Context, limit int64) ([]models.RiskConfig, error) {
	if limit <= 0 {
		limit = 50
	}
	cur, err := mongox.C(models.CollRiskConfigs).Find(ctx, bson.M{"scope": models.RiskScopeGlobal},
		options.Find().SetSort(bson.D{{Key: "version", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	out := []models.RiskConfig{}
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AssertTradingNotHalted is the global kill switch gate.
func AssertTradingNotHalted(ctx context.Context) error {
	c, err := GetRiskConfig(ctx)
	if err != nil {
		return err
	}
	if c.TradingHalted {
		return apperr.New(apperr.TradingHalted, "Trading is currently halted by the platform", 503)
	}
	return nil
}
