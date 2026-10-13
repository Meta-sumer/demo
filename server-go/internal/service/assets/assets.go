// Package assets is the asset registry (spec section 25).
//
// Every hot path (tick ingestion, trade validation) needs the asset row, so the
// whole - deliberately small - table is cached in process for a short TTL and
// invalidated on write. The TTL is what keeps other instances converging.
package assets

import (
	"context"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
)

const cacheTTL = 2 * time.Second

var (
	cacheMu     sync.Mutex
	cacheAt     time.Time
	cacheAssets []*models.Asset
)

// InvalidateAssetCache drops the in-process asset table.
func InvalidateAssetCache() {
	cacheMu.Lock()
	cacheAssets = nil
	cacheMu.Unlock()
}

// loadAssets returns the cached table. The returned rows are shared: callers
// must treat them as read-only.
func loadAssets(ctx context.Context) ([]*models.Asset, error) {
	cacheMu.Lock()
	if cacheAssets != nil && time.Since(cacheAt) < cacheTTL {
		a := cacheAssets
		cacheMu.Unlock()
		return a, nil
	}
	cacheMu.Unlock()

	cur, err := mongox.C(models.CollAssets).Find(ctx, bson.M{},
		options.Find().SetSort(bson.D{{Key: "displayOrder", Value: 1}, {Key: "symbol", Value: 1}}))
	if err != nil {
		return nil, err
	}
	rows := []*models.Asset{}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}

	cacheMu.Lock()
	cacheAssets = rows
	cacheAt = time.Now()
	cacheMu.Unlock()
	return rows, nil
}

// ListAssetDocs returns asset rows (shared, read-only), optionally only active ones.
func ListAssetDocs(ctx context.Context, activeOnly bool) ([]*models.Asset, error) {
	all, err := loadAssets(ctx)
	if err != nil {
		return nil, err
	}
	if !activeOnly {
		return all, nil
	}
	out := make([]*models.Asset, 0, len(all))
	for _, a := range all {
		if a.Active {
			out = append(out, a)
		}
	}
	return out, nil
}

// ListAssets returns views, optionally only active assets.
func ListAssets(ctx context.Context, activeOnly bool) ([]models.AssetView, error) {
	docs, err := ListAssetDocs(ctx, activeOnly)
	if err != nil {
		return nil, err
	}
	at := util.NowMs()
	out := make([]models.AssetView, 0, len(docs))
	for _, a := range docs {
		out = append(out, models.ToAssetView(a, at))
	}
	return out, nil
}

// FindAssetDoc returns nil (no error) for an unknown or malformed id.
func FindAssetDoc(ctx context.Context, assetID string) (*models.Asset, error) {
	id, ok := mongox.ParseID(assetID)
	if !ok {
		return nil, nil
	}
	all, err := loadAssets(ctx)
	if err != nil {
		return nil, err
	}
	for _, a := range all {
		if a.ID == id {
			return a, nil
		}
	}
	return nil, nil
}

// RequireAssetDoc is FindAssetDoc that fails with ASSET_NOT_FOUND (404).
func RequireAssetDoc(ctx context.Context, assetID string) (*models.Asset, error) {
	a, err := FindAssetDoc(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if a == nil {
		return nil, apperr.NotFoundErr(apperr.AssetNotFound, "Asset not found")
	}
	return a, nil
}

func GetAsset(ctx context.Context, assetID string) (models.AssetView, error) {
	a, err := RequireAssetDoc(ctx, assetID)
	if err != nil {
		return models.AssetView{}, err
	}
	return models.ToAssetView(a, util.NowMs()), nil
}

func FindAssetBySymbol(ctx context.Context, symbol string) (*models.Asset, error) {
	all, err := loadAssets(ctx)
	if err != nil {
		return nil, err
	}
	wanted := strings.ToUpper(symbol)
	for _, a := range all {
		if a.Symbol == wanted {
			return a, nil
		}
	}
	return nil, nil
}

// FindAssetByProviderSymbol maps a provider symbol back to an asset (tick
// pipeline). Case-insensitive.
func FindAssetByProviderSymbol(ctx context.Context, providerSymbol string) (*models.Asset, error) {
	all, err := loadAssets(ctx)
	if err != nil {
		return nil, err
	}
	wanted := strings.ToLower(providerSymbol)
	for _, a := range all {
		if strings.ToLower(a.ProviderSymbol) == wanted {
			return a, nil
		}
	}
	return nil, nil
}

// CreateAssetInput: optional fields are pointers (nil = schema default).
type CreateAssetInput struct {
	Symbol                string                 `json:"symbol"`
	Name                  string                 `json:"name"`
	Category              models.AssetCategory   `json:"category"`
	ProviderSymbol        string                 `json:"providerSymbol"`
	Provider              *string                `json:"provider"`
	PricePrecision        *int64                 `json:"pricePrecision"`
	MinStake              *int64                 `json:"minStake"`
	MaxStake              *int64                 `json:"maxStake"`
	SupportedExpiries     []int64                `json:"supportedExpiries"`
	ExposureLimit         *int64                 `json:"exposureLimit"`
	NetExposureLimit      *int64                 `json:"netExposureLimit"`
	HedgeEnabled          *bool                  `json:"hedgeEnabled"`
	TargetHedgeRatio      *float64               `json:"targetHedgeRatio"`
	HedgeThresholdPercent *float64               `json:"hedgeThresholdPercent"`
	MinHedgeNotional      *int64                 `json:"minHedgeNotional"`
	MaxHedgeNotional      *int64                 `json:"maxHedgeNotional"`
	TradingWindows        []models.TradingWindow `json:"tradingWindows"`
	DisplayOrder          *int64                 `json:"displayOrder"`
	Active                *bool                  `json:"active"`
}

// UpdateAssetInput: nil = unchanged. HedgeThresholdPercent may be explicitly
// cleared: set HedgeThresholdPercentSet with a nil HedgeThresholdPercent.
type UpdateAssetInput struct {
	Symbol                   *string
	Name                     *string
	Category                 *models.AssetCategory
	ProviderSymbol           *string
	Provider                 *string
	PricePrecision           *int64
	MinStake                 *int64
	MaxStake                 *int64
	SupportedExpiries        []int64
	ExposureLimit            *int64
	NetExposureLimit         *int64
	HedgeEnabled             *bool
	TargetHedgeRatio         *float64
	HedgeThresholdPercentSet bool
	HedgeThresholdPercent    *float64
	MinHedgeNotional         *int64
	MaxHedgeNotional         *int64
	TradingWindows           []models.TradingWindow
	DisplayOrder             *int64
	Active                   *bool
}

func or[T any](v *T, def T) T {
	if v != nil {
		return *v
	}
	return def
}

func normaliseWindows(w []models.TradingWindow) []models.TradingWindow {
	if w == nil {
		return []models.TradingWindow{}
	}
	for i := range w {
		if w[i].Days == nil {
			w[i].Days = []int{}
		}
	}
	return w
}

// CreateAsset inserts an asset with the schema defaults applied.
func CreateAsset(ctx context.Context, in CreateAssetInput) (models.AssetView, error) {
	symbol := strings.ToUpper(strings.TrimSpace(in.Symbol))
	coll := mongox.C(models.CollAssets)
	n, err := coll.CountDocuments(ctx, bson.M{"symbol": symbol}, options.Count().SetLimit(1))
	if err != nil {
		return models.AssetView{}, err
	}
	if n > 0 {
		return models.AssetView{}, apperr.ConflictErr(apperr.Conflict, "An asset with that symbol already exists")
	}

	expiries := in.SupportedExpiries
	if expiries == nil {
		expiries = models.AssetDefaultSupportedExpiries()
	}
	now := mongox.Now()
	doc := models.Asset{
		Symbol:                symbol,
		Name:                  strings.TrimSpace(in.Name),
		Category:              in.Category,
		Active:                or(in.Active, true),
		ProviderSymbol:        in.ProviderSymbol,
		Provider:              or(in.Provider, models.AssetDefaultProvider),
		PricePrecision:        or(in.PricePrecision, models.AssetDefaultPricePrecision),
		MinStake:              or(in.MinStake, models.AssetDefaultMinStake),
		MaxStake:              or(in.MaxStake, models.AssetDefaultMaxStake),
		SupportedExpiries:     expiries,
		ExposureLimit:         or(in.ExposureLimit, models.AssetDefaultExposureLimit),
		NetExposureLimit:      or(in.NetExposureLimit, models.AssetDefaultNetExposureLimit),
		HedgeEnabled:          or(in.HedgeEnabled, false),
		TargetHedgeRatio:      or(in.TargetHedgeRatio, models.AssetDefaultTargetHedgeRatio),
		HedgeThresholdPercent: in.HedgeThresholdPercent,
		MinHedgeNotional:      or(in.MinHedgeNotional, models.AssetDefaultMinHedgeNotional),
		MaxHedgeNotional:      or(in.MaxHedgeNotional, models.AssetDefaultMaxHedgeNotional),
		TradingWindows:        normaliseWindows(in.TradingWindows),
		DisplayOrder:          or(in.DisplayOrder, 0),
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	res, err := coll.InsertOne(ctx, doc)
	if err != nil {
		return models.AssetView{}, err
	}
	doc.ID = res.InsertedID.(bson.ObjectID)
	InvalidateAssetCache()
	return models.ToAssetView(&doc, util.NowMs()), nil
}

// UpdateAsset applies a partial update and returns the before/after views.
func UpdateAsset(ctx context.Context, assetID string, in UpdateAssetInput) (before, after models.AssetView, err error) {
	current, err := RequireAssetDoc(ctx, assetID)
	if err != nil {
		return before, after, err
	}

	set := bson.M{}
	put := func(key string, present bool, v any) {
		if present {
			set[key] = v
		}
	}
	if in.Symbol != nil {
		set["symbol"] = strings.ToUpper(strings.TrimSpace(*in.Symbol))
	}
	if in.Name != nil {
		set["name"] = strings.TrimSpace(*in.Name)
	}
	put("category", in.Category != nil, deref(in.Category))
	put("providerSymbol", in.ProviderSymbol != nil, deref(in.ProviderSymbol))
	put("provider", in.Provider != nil, deref(in.Provider))
	put("pricePrecision", in.PricePrecision != nil, deref(in.PricePrecision))
	put("minStake", in.MinStake != nil, deref(in.MinStake))
	put("maxStake", in.MaxStake != nil, deref(in.MaxStake))
	put("supportedExpiries", in.SupportedExpiries != nil, in.SupportedExpiries)
	put("exposureLimit", in.ExposureLimit != nil, deref(in.ExposureLimit))
	put("netExposureLimit", in.NetExposureLimit != nil, deref(in.NetExposureLimit))
	put("hedgeEnabled", in.HedgeEnabled != nil, deref(in.HedgeEnabled))
	put("targetHedgeRatio", in.TargetHedgeRatio != nil, deref(in.TargetHedgeRatio))
	if in.HedgeThresholdPercentSet || in.HedgeThresholdPercent != nil {
		set["hedgeThresholdPercent"] = in.HedgeThresholdPercent
	}
	put("minHedgeNotional", in.MinHedgeNotional != nil, deref(in.MinHedgeNotional))
	put("maxHedgeNotional", in.MaxHedgeNotional != nil, deref(in.MaxHedgeNotional))
	if in.TradingWindows != nil {
		set["tradingWindows"] = normaliseWindows(in.TradingWindows)
	}
	put("displayOrder", in.DisplayOrder != nil, deref(in.DisplayOrder))
	put("active", in.Active != nil, deref(in.Active))
	set["updatedAt"] = mongox.Now()

	var updated models.Asset
	err = mongox.C(models.CollAssets).FindOneAndUpdate(ctx, bson.M{"_id": current.ID}, bson.M{"$set": set},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if err != nil {
		if mongox.IsNotFound(err) {
			return before, after, apperr.NotFoundErr(apperr.AssetNotFound, "Asset not found")
		}
		return before, after, err
	}
	InvalidateAssetCache()
	at := util.NowMs()
	return models.ToAssetView(current, at), models.ToAssetView(&updated, at), nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// AssertAssetTradable is the trade-time gate for one asset. Feed health is
// checked separately, by marketdata, because it changes on every tick.
func AssertAssetTradable(asset *models.Asset, atMs int64) error {
	if !asset.Active {
		return apperr.New(apperr.AssetInactive, "Trading is disabled for "+asset.Symbol, 409)
	}
	if !util.IsWithinTradingHours(asset.TradingWindows, atMs) {
		return apperr.New(apperr.MarketClosed, "Market for "+asset.Symbol+" is closed", 409)
	}
	return nil
}
