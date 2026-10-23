package hedging

import (
	"context"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
)

// INR per USDT (hedging-spec.md section 6).
//
// The book is in INR; Binance trades in USDT. Every hedge is sized, and every
// fill and funding payment converted, at an INR-per-USDT rate that carries the
// time it was observed. Two sources: the environment (HEDGE_INR_PER_USDT +
// HEDGE_INR_PER_USDT_AS_OF) and rates an operator enters on the Hedging page
// (hedge_fx_rates, append-only, audited). The newest observation wins. Older
// than HEDGE_FX_MAX_AGE_MS, the rate is STALE: no hedge is placed and the cost
// estimate reports FX_STALE - the platform never guesses a rate.

// FxRate is the rate in force.
type FxRate struct {
	Rate     float64 `json:"inrPerUsdt"`
	AsOf     int64   `json:"asOf"`
	Source   string  `json:"source"`
	AgeMs    int64   `json:"ageMs"`
	MaxAgeMs int64   `json:"maxAgeMs"`
	Missing  bool    `json:"missing"`
	Stale    bool    `json:"stale"`
}

// Usable reports a rate that may size an order.
func (r FxRate) Usable() bool { return !r.Missing && !r.Stale && r.Rate > 0 }

// Reason is FX_MISSING / FX_STALE / "".
func (r FxRate) Reason() string {
	switch {
	case r.Missing:
		return "FX_MISSING"
	case r.Stale:
		return "FX_STALE"
	}
	return ""
}

var (
	fxMu      sync.Mutex
	fxCached  *models.HedgeFxRate
	fxCacheAt time.Time
	fxLoaded  bool
)

const fxCacheTTL = 5 * time.Second

func latestStoredFx(ctx context.Context) (*models.HedgeFxRate, error) {
	fxMu.Lock()
	if fxLoaded && time.Since(fxCacheAt) < fxCacheTTL {
		r := fxCached
		fxMu.Unlock()
		return r, nil
	}
	fxMu.Unlock()
	var row models.HedgeFxRate
	err := mongox.C(models.CollHedgeFxRates).FindOne(ctx, bson.M{},
		options.FindOne().SetSort(bson.D{{Key: "asOf", Value: -1}, {Key: "_id", Value: -1}})).Decode(&row)
	var out *models.HedgeFxRate
	switch {
	case err == nil:
		out = &row
	case !mongox.IsNotFound(err):
		return nil, err
	}
	fxMu.Lock()
	fxCached, fxCacheAt, fxLoaded = out, time.Now(), true
	fxMu.Unlock()
	return out, nil
}

func invalidateFxCache() {
	fxMu.Lock()
	fxLoaded = false
	fxMu.Unlock()
}

func fxMaxAge() int64 {
	if config.E != nil && config.E.HedgeFXMaxAgeMs > 0 {
		return config.E.HedgeFXMaxAgeMs
	}
	return 86_400_000
}

// SelectFxRate is the pure choice between the environment's rate and the
// newest stored one, and the staleness verdict at nowMs.
func SelectFxRate(envRate float64, envAsOf int64, stored *models.HedgeFxRate, nowMs, maxAgeMs int64) FxRate {
	r := FxRate{MaxAgeMs: maxAgeMs, Missing: true}
	if envRate > 0 && envAsOf > 0 {
		r = FxRate{Rate: envRate, AsOf: envAsOf, Source: "env", MaxAgeMs: maxAgeMs}
	}
	if stored != nil && stored.Rate > 0 && (r.Missing || stored.AsOf.UnixMilli() > r.AsOf) {
		r = FxRate{Rate: stored.Rate, AsOf: stored.AsOf.UnixMilli(), Source: "admin:" + stored.Source, MaxAgeMs: maxAgeMs}
	}
	if r.Missing {
		return r
	}
	r.AgeMs = max(0, nowMs-r.AsOf)
	r.Stale = r.AgeMs > maxAgeMs
	return r
}

// CurrentFX is the rate in force now.
func CurrentFX(ctx context.Context) (FxRate, error) {
	stored, err := latestStoredFx(ctx)
	if err != nil {
		return FxRate{}, err
	}
	var envRate float64
	var envAsOf int64
	if config.E != nil {
		envRate, envAsOf = config.E.HedgeINRPerUSDT, config.E.HedgeINRPerUSDTAsOf
	}
	return SelectFxRate(envRate, envAsOf, stored, util.NowMs(), fxMaxAge()), nil
}

// SetFxRateInput is an operator's rate entry.
type SetFxRateInput struct {
	Rate   float64
	Source string
	// Observation time; zero = now. Never in the future.
	AsOf  time.Time
	SetBy *bson.ObjectID
	Note  *string
}

// SetFxRate records a new INR-per-USDT rate.
func SetFxRate(ctx context.Context, in SetFxRateInput) (models.HedgeFxRate, error) {
	if !(in.Rate > 0) || in.Rate > 1000 {
		return models.HedgeFxRate{}, apperr.Validation("inrPerUsdt must be a positive rate (at most 1000)", nil)
	}
	now := mongox.Now()
	asOf := in.AsOf.UTC().Truncate(time.Millisecond)
	if in.AsOf.IsZero() {
		asOf = now
	}
	if asOf.After(now.Add(time.Minute)) {
		return models.HedgeFxRate{}, apperr.Validation("asOf cannot be in the future", nil)
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	row := models.HedgeFxRate{Rate: in.Rate, Source: in.Source, AsOf: asOf, SetBy: in.SetBy, Note: in.Note, CreatedAt: now}
	res, err := mongox.C(models.CollHedgeFxRates).InsertOne(ctx, row)
	if err != nil {
		return models.HedgeFxRate{}, err
	}
	row.ID = res.InsertedID.(bson.ObjectID)
	invalidateFxCache()
	return row, nil
}

// inrFromUsdtE8 converts 1e-8 USDT to INR minor units at rate.
func inrFromUsdtE8(usdtE8 int64, rate float64) int64 {
	return int64(util.RoundHalfUp(float64(usdtE8) / float64(models.QtyScale) * rate * 100))
}
