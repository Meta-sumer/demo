// Package riskconfig is the live platform risk configuration (spec sections
// 13, 15, 17).
//
// Read on every trade, so it is cached for a short TTL. Writes are versioned:
// the active row is superseded rather than edited, which is what lets a past
// risk decision be replayed against the configuration that produced it.
package riskconfig

import (
	"context"
	"math"
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
	out.PricingExpiryFactors = append([]models.PricingExpiryFactor(nil), c.PricingExpiryFactors...)
	out.PricingSymbolRules = append([]models.PricingSymbolRule(nil), c.PricingSymbolRules...)
	out.PricingHalts = append([]models.PricingHalt(nil), c.PricingHalts...)
	out.EarlyCloseCalibration = append([]models.EarlyCloseCalibrationRow(nil), c.EarlyCloseCalibration...)
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
// first boot. The FEED_*_MS variables only seed this first version: from then
// on the active risk configuration is the one authority for the feed
// thresholds (PUT /admin/settings/risk), whatever the environment says. Two processes can reach this at the same moment on a fresh
// database; the unique index on the active row makes one of them lose, and the
// loser reads what the winner wrote rather than failing the request.
func bootstrapDefaultConfig(ctx context.Context) (*models.RiskConfig, error) {
	doc := models.DefaultRiskConfig()
	if config.E != nil {
		doc.FeedHealthyMaxMs = config.E.FeedHealthyMaxMs
		doc.FeedDegradedMaxMs = config.E.FeedDegradedMaxMs
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
	MaxExpiryLiability               *int64              `json:"maxExpiryLiability"`
	MaxAssetLiability                *int64              `json:"maxAssetLiability"`
	MaxPlatformLiability             *int64              `json:"maxPlatformLiability"`
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
	FeedTickMaxAgeMs                 *int64              `json:"feedTickMaxAgeMs"`
	HaltTradingOnStaleFeed           *bool               `json:"haltTradingOnStaleFeed"`
	HaltTradingOnDegradedFeed        *bool               `json:"haltTradingOnDegradedFeed"`
	TradingHalted                    *bool               `json:"tradingHalted"`

	// Feed data rules and the settlement policy (models.RiskConfig).
	FeedTickHistoryRetentionMs      *int64   `json:"feedTickHistoryRetentionMs"`
	FeedGapMinMs                    *int64   `json:"feedGapMinMs"`
	FeedGapIntervalMultiple         *float64 `json:"feedGapIntervalMultiple"`
	FeedSaturationHoldMs            *int64   `json:"feedSaturationHoldMs"`
	FeedMaxClockDriftMs             *int64   `json:"feedMaxClockDriftMs"`
	FeedClockCheckIntervalMs        *int64   `json:"feedClockCheckIntervalMs"`
	SettlementMaxTickAgeMs          *int64   `json:"settlementMaxTickAgeMs"`
	SettlementConfirmationWindowMs  *int64   `json:"settlementConfirmationWindowMs"`
	SettlementDeferWindowMs         *int64   `json:"settlementDeferWindowMs"`
	SettlementRetryDelayMs          *int64   `json:"settlementRetryDelayMs"`
	SettlementMaxClockSkewMs        *int64   `json:"settlementMaxClockSkewMs"`
	SettlementNoPriceRule           *string  `json:"settlementNoPriceRule"`
	EarlyCloseFloorMs               *int64   `json:"earlyCloseFloorMs"`
	EarlyCloseMaxTickAgeMs          *int64   `json:"earlyCloseMaxTickAgeMs"`
	EarlyCloseQuoteMaxAgeMs         *int64   `json:"earlyCloseQuoteMaxAgeMs"`
	EarlyCloseQuoteTolerancePercent *float64 `json:"earlyCloseQuoteTolerancePercent"`
	// Calibrated Sell-now correction (audit F-12): nil = unchanged, [] =
	// the uncorrected formula.
	EarlyCloseCalibration           []models.EarlyCloseCalibrationRow `json:"earlyCloseCalibration"`
	EarlyCloseMinProbabilityPercent *float64                          `json:"earlyCloseMinProbabilityPercent"`

	TradingHaltReason              *string                      `json:"tradingHaltReason"`
	PricingMode                    *string                      `json:"pricingMode"`
	PricingRuleLabel               *string                      `json:"pricingRuleLabel"`
	PricingStaleAfterMs            *int64                       `json:"pricingStaleAfterMs"`
	PricingMaxSpreadBps            *float64                     `json:"pricingMaxSpreadBps"`
	PricingMaxJumpBps              *float64                     `json:"pricingMaxJumpBps"`
	PricingJumpConfirmations       *int64                       `json:"pricingJumpConfirmations"`
	PricingPendingOutlierMaxAgeMs  *int64                       `json:"pricingPendingOutlierMaxAgeMs"`
	PricingEwmaLambda              *float64                     `json:"pricingEwmaLambda"`
	PricingVolSampleIntervalMs     *int64                       `json:"pricingVolSampleIntervalMs"`
	PricingKVol                    *float64                     `json:"pricingKVol"`
	PricingMaxVolAdj               *float64                     `json:"pricingMaxVolAdj"`
	PricingVolBlockRatio           *float64                     `json:"pricingVolBlockRatio"`
	PricingKSpread                 *float64                     `json:"pricingKSpread"`
	PricingMaxSpreadAdj            *float64                     `json:"pricingMaxSpreadAdj"`
	PricingKExposure               *float64                     `json:"pricingKExposure"`
	PricingMaxExposureAdj          *float64                     `json:"pricingMaxExposureAdj"`
	PricingExpiryFactors           []models.PricingExpiryFactor `json:"pricingExpiryFactors"` // nil = unchanged
	PricingPayoutStep              *float64                     `json:"pricingPayoutStep"`
	PricingOfferTTLMs              *int64                       `json:"pricingOfferTtlMs"`
	PricingMaxSlippageBps          *float64                     `json:"pricingMaxSlippageBps"`
	PricingDisplayTolerancePercent *float64                     `json:"pricingDisplayTolerancePercent"`
	PricingGuardEnabled            *bool                        `json:"pricingGuardEnabled"`
	PricingGuardMinEdgePercent     *float64                     `json:"pricingGuardMinEdgePercent"`
	PricingGuardConservative       *bool                        `json:"pricingGuardConservative"`
	PricingHedgeUnavailableAction  *string                      `json:"pricingHedgeUnavailableAction"`
	PricingHedgeUnavailableAdj     *float64                     `json:"pricingHedgeUnavailableAdj"`
	PricingHedgeTakerFeeBps        *float64                     `json:"pricingHedgeTakerFeeBps"`
	PricingHedgeSpreadBps          *float64                     `json:"pricingHedgeSpreadBps"`
	PricingHedgeSlippageBps        *float64                     `json:"pricingHedgeSlippageBps"`
	PricingSymbolRules             []models.PricingSymbolRule   `json:"pricingSymbolRules"` // nil = unchanged
	PricingHalts                   []models.PricingHalt         `json:"pricingHalts"`       // nil = unchanged
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
	setIf(&c.MaxExpiryLiability, p.MaxExpiryLiability)
	setIf(&c.MaxAssetLiability, p.MaxAssetLiability)
	setIf(&c.MaxPlatformLiability, p.MaxPlatformLiability)
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
	setIf(&c.FeedTickMaxAgeMs, p.FeedTickMaxAgeMs)
	setIf(&c.HaltTradingOnStaleFeed, p.HaltTradingOnStaleFeed)
	setIf(&c.HaltTradingOnDegradedFeed, p.HaltTradingOnDegradedFeed)
	setIf(&c.TradingHalted, p.TradingHalted)
	setIf(&c.FeedTickHistoryRetentionMs, p.FeedTickHistoryRetentionMs)
	setIf(&c.FeedGapMinMs, p.FeedGapMinMs)
	setIf(&c.FeedGapIntervalMultiple, p.FeedGapIntervalMultiple)
	setIf(&c.FeedSaturationHoldMs, p.FeedSaturationHoldMs)
	setIf(&c.FeedMaxClockDriftMs, p.FeedMaxClockDriftMs)
	setIf(&c.FeedClockCheckIntervalMs, p.FeedClockCheckIntervalMs)
	setIf(&c.SettlementMaxTickAgeMs, p.SettlementMaxTickAgeMs)
	setIf(&c.SettlementConfirmationWindowMs, p.SettlementConfirmationWindowMs)
	setIf(&c.SettlementDeferWindowMs, p.SettlementDeferWindowMs)
	setIf(&c.SettlementRetryDelayMs, p.SettlementRetryDelayMs)
	setIf(&c.SettlementMaxClockSkewMs, p.SettlementMaxClockSkewMs)
	setIf(&c.SettlementNoPriceRule, p.SettlementNoPriceRule)
	setIf(&c.EarlyCloseFloorMs, p.EarlyCloseFloorMs)
	setIf(&c.EarlyCloseMaxTickAgeMs, p.EarlyCloseMaxTickAgeMs)
	setIf(&c.EarlyCloseQuoteMaxAgeMs, p.EarlyCloseQuoteMaxAgeMs)
	setIf(&c.EarlyCloseQuoteTolerancePercent, p.EarlyCloseQuoteTolerancePercent)
	setIf(&c.TradingHaltReason, p.TradingHaltReason)
	setIf(&c.PricingMode, p.PricingMode)
	setIf(&c.PricingRuleLabel, p.PricingRuleLabel)
	setIf(&c.PricingStaleAfterMs, p.PricingStaleAfterMs)
	setIf(&c.PricingMaxSpreadBps, p.PricingMaxSpreadBps)
	setIf(&c.PricingMaxJumpBps, p.PricingMaxJumpBps)
	setIf(&c.PricingJumpConfirmations, p.PricingJumpConfirmations)
	setIf(&c.PricingPendingOutlierMaxAgeMs, p.PricingPendingOutlierMaxAgeMs)
	setIf(&c.PricingEwmaLambda, p.PricingEwmaLambda)
	setIf(&c.PricingVolSampleIntervalMs, p.PricingVolSampleIntervalMs)
	setIf(&c.PricingKVol, p.PricingKVol)
	setIf(&c.PricingMaxVolAdj, p.PricingMaxVolAdj)
	setIf(&c.PricingVolBlockRatio, p.PricingVolBlockRatio)
	setIf(&c.PricingKSpread, p.PricingKSpread)
	setIf(&c.PricingMaxSpreadAdj, p.PricingMaxSpreadAdj)
	setIf(&c.PricingKExposure, p.PricingKExposure)
	setIf(&c.PricingMaxExposureAdj, p.PricingMaxExposureAdj)
	if p.PricingExpiryFactors != nil {
		c.PricingExpiryFactors = append([]models.PricingExpiryFactor(nil), p.PricingExpiryFactors...)
	}
	setIf(&c.PricingPayoutStep, p.PricingPayoutStep)
	setIf(&c.PricingOfferTTLMs, p.PricingOfferTTLMs)
	setIf(&c.PricingMaxSlippageBps, p.PricingMaxSlippageBps)
	setIf(&c.PricingDisplayTolerancePercent, p.PricingDisplayTolerancePercent)
	setIf(&c.PricingGuardEnabled, p.PricingGuardEnabled)
	setIf(&c.PricingGuardMinEdgePercent, p.PricingGuardMinEdgePercent)
	setIf(&c.PricingGuardConservative, p.PricingGuardConservative)
	setIf(&c.PricingHedgeUnavailableAction, p.PricingHedgeUnavailableAction)
	setIf(&c.PricingHedgeUnavailableAdj, p.PricingHedgeUnavailableAdj)
	setIf(&c.PricingHedgeTakerFeeBps, p.PricingHedgeTakerFeeBps)
	setIf(&c.PricingHedgeSpreadBps, p.PricingHedgeSpreadBps)
	setIf(&c.PricingHedgeSlippageBps, p.PricingHedgeSlippageBps)
	if p.PricingSymbolRules != nil {
		c.PricingSymbolRules = append([]models.PricingSymbolRule(nil), p.PricingSymbolRules...)
	}
	if p.PricingHalts != nil {
		c.PricingHalts = append([]models.PricingHalt(nil), p.PricingHalts...)
	}
	if p.EarlyCloseCalibration != nil {
		c.EarlyCloseCalibration = append([]models.EarlyCloseCalibrationRow{}, p.EarlyCloseCalibration...)
	}
	setIf(&c.EarlyCloseMinProbabilityPercent, p.EarlyCloseMinProbabilityPercent)
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
	if !(c.FeedHealthyMaxMs < c.FeedDegradedMaxMs && c.FeedDegradedMaxMs < c.FeedTickMaxAgeMs) {
		return apperr.Validation("Feed thresholds must satisfy healthy < degraded < tick max age", nil)
	}
	if err := validateSettlement(c); err != nil {
		return err
	}
	if err := validateFeedRules(c); err != nil {
		return err
	}
	if c.MinTradeAmount > c.MaxTradeAmount {
		return apperr.Validation("minTradeAmount cannot exceed maxTradeAmount", nil)
	}
	// Liability ceilings: 0 means "no liability limit", never "nothing may trade".
	if c.MaxExpiryLiability < 0 || c.MaxAssetLiability < 0 || c.MaxPlatformLiability < 0 {
		return apperr.Validation("Liability limits cannot be negative", nil)
	}
	if c.HedgeThresholdPercent < 0 || c.HedgeThresholdPercent > 100 {
		return apperr.Validation("hedgeThresholdPercent must be between 0 and 100", nil)
	}
	if err := validatePricing(c); err != nil {
		return err
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

// validatePricing checks the pricing-engine fields (the admin API bounds them
// one by one; these are the cross-field rules).
func validatePricing(c *models.RiskConfig) error {
	if c.PricingMode != models.PricingModeDynamic && c.PricingMode != models.PricingModeFixed {
		return apperr.Validation("pricingMode must be DYNAMIC or FIXED", nil)
	}
	if c.PricingHedgeUnavailableAction != models.PricingActionWiden && c.PricingHedgeUnavailableAction != models.PricingActionRefuse {
		return apperr.Validation("pricingHedgeUnavailableAction must be WIDEN or REFUSE", nil)
	}
	if !(c.PricingEwmaLambda > 0 && c.PricingEwmaLambda < 1) {
		return apperr.Validation("pricingEwmaLambda must be between 0 and 1", nil)
	}
	step := c.PricingPayoutStep
	if !(step > 0 && step <= 0.1) {
		return apperr.Validation("pricingPayoutStep must be greater than 0 and at most 0.1", nil)
	}
	if c.PricingVolBlockRatio <= 1 {
		return apperr.Validation("pricingVolBlockRatio must be above 1", nil)
	}
	if c.PricingJumpConfirmations < 1 || c.PricingStaleAfterMs < 100 || c.PricingOfferTTLMs < 500 {
		return apperr.Validation("pricingJumpConfirmations must be at least 1, pricingStaleAfterMs at least 100 and pricingOfferTtlMs at least 500", nil)
	}
	seenExpiry := map[int64]bool{}
	for _, f := range c.PricingExpiryFactors {
		if f.ExpirySeconds <= 0 || !(f.Factor > 0) || seenExpiry[f.ExpirySeconds] {
			return apperr.Validation("pricingExpiryFactors: each expiry once, with expirySeconds and factor above 0", nil)
		}
		seenExpiry[f.ExpirySeconds] = true
	}
	onStep := func(x float64) bool {
		n := x / step
		return math.Abs(n-math.Round(n)) < 1e-6
	}
	seenSym := map[string]bool{}
	for _, r := range c.PricingSymbolRules {
		if r.Symbol == "" || seenSym[r.Symbol] {
			return apperr.Validation("pricingSymbolRules: each symbol once", nil)
		}
		seenSym[r.Symbol] = true
		if !(r.MinPayout > 0 && r.MinPayout <= r.MaxPayout && r.MaxPayout <= 5) || !(r.BasePayout > 0 && r.BasePayout <= 5) {
			return apperr.Validation("pricingSymbolRules "+r.Symbol+": need 0 < minPayout <= maxPayout <= 5 and 0 < basePayout <= 5", nil)
		}
		// On the step grid, so rounding down can never leave the bounds.
		if !onStep(r.MinPayout) || !onStep(r.MaxPayout) {
			return apperr.Validation("pricingSymbolRules "+r.Symbol+": minPayout and maxPayout must be multiples of pricingPayoutStep", nil)
		}
		if !(r.BaselineVol > 0) || r.HedgeCost < 0 || r.Margin < 0 || r.MaxExposure <= 0 {
			return apperr.Validation("pricingSymbolRules "+r.Symbol+": need baselineVol > 0, hedgeCost >= 0, margin >= 0, maxExposure > 0", nil)
		}
	}
	for _, h := range c.PricingHalts {
		if h.Symbol == "" || h.Reason == "" {
			return apperr.Validation("pricingHalts: each halt needs a symbol and a reason", nil)
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
