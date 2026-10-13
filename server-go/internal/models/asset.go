package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
)

// CollAssets is the asset registry (spec section 25).
const CollAssets = "assets"

type AssetCategory string

const (
	AssetCategoryCrypto    AssetCategory = "CRYPTO"
	AssetCategoryForex     AssetCategory = "FOREX"
	AssetCategoryStock     AssetCategory = "STOCK"
	AssetCategoryCommodity AssetCategory = "COMMODITY"
	AssetCategoryIndex     AssetCategory = "INDEX"
)

var AssetCategoryValues = []string{
	string(AssetCategoryCrypto), string(AssetCategoryForex), string(AssetCategoryStock),
	string(AssetCategoryCommodity), string(AssetCategoryIndex),
}

// TradingWindow: ISO weekdays (1 = Monday ... 7 = Sunday), UTC minutes 0..1440.
type TradingWindow = util.TradingWindow

// Asset schema defaults (Mongoose applies them on create; the service must).
const (
	AssetDefaultProvider         = "simulated"
	AssetDefaultPricePrecision   = 2
	AssetDefaultMinStake         = 10_00
	AssetDefaultMaxStake         = 100_000_00
	AssetDefaultExposureLimit    = 5_000_000_00
	AssetDefaultNetExposureLimit = 2_000_000_00
	AssetDefaultTargetHedgeRatio = 0.5
	AssetDefaultMinHedgeNotional = 1_000_00
	AssetDefaultMaxHedgeNotional = 1_000_000_00
)

// AssetDefaultSupportedExpiries returns a fresh copy of the default expiries.
func AssetDefaultSupportedExpiries() []int64 { return []int64{30, 60, 300} }

// Asset: all monetary fields are minor units. symbol is uppercased and
// trimmed, name trimmed (Mongoose setters - apply in the service).
type Asset struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	Symbol   string        `bson:"symbol"`
	Name     string        `bson:"name"`
	Category AssetCategory `bson:"category"`
	Active   bool          `bson:"active"`

	// Symbol as the market data provider names it, e.g. `btcusdt` on Binance.
	ProviderSymbol string `bson:"providerSymbol"`
	Provider       string `bson:"provider"`
	PricePrecision int64  `bson:"pricePrecision"`

	MinStake          int64   `bson:"minStake"`
	MaxStake          int64   `bson:"maxStake"`
	SupportedExpiries []int64 `bson:"supportedExpiries"`

	// Maximum gross exposure tolerated on this asset.
	ExposureLimit int64 `bson:"exposureLimit"`
	// Maximum net (one-sided) exposure tolerated.
	NetExposureLimit int64 `bson:"netExposureLimit"`

	HedgeEnabled bool `bson:"hedgeEnabled"`
	// Fraction of net exposure the platform aims to hedge, 0..1.
	TargetHedgeRatio float64 `bson:"targetHedgeRatio"`
	// Exposure utilisation (%) above which hedging starts. Null = use global.
	HedgeThresholdPercent *float64 `bson:"hedgeThresholdPercent"`
	MinHedgeNotional      int64    `bson:"minHedgeNotional"`
	MaxHedgeNotional      int64    `bson:"maxHedgeNotional"`

	TradingWindows []TradingWindow `bson:"tradingWindows"`
	DisplayOrder   int64           `bson:"displayOrder"`
	CreatedAt      time.Time       `bson:"createdAt"`
	UpdatedAt      time.Time       `bson:"updatedAt"`
	V              int64           `bson:"__v"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollAssets, Keys: bson.D{{Key: "active", Value: 1}}},
		mongox.Index{Collection: CollAssets, Keys: bson.D{{Key: "symbol", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollAssets, Keys: bson.D{{Key: "active", Value: 1}, {Key: "displayOrder", Value: 1}}},
		mongox.Index{Collection: CollAssets, Keys: bson.D{{Key: "provider", Value: 1}, {Key: "providerSymbol", Value: 1}}},
	)
}

type AssetView struct {
	ID                    string          `json:"id"`
	Symbol                string          `json:"symbol"`
	Name                  string          `json:"name"`
	Category              AssetCategory   `json:"category"`
	Active                bool            `json:"active"`
	ProviderSymbol        string          `json:"providerSymbol"`
	Provider              string          `json:"provider"`
	PricePrecision        int64           `json:"pricePrecision"`
	MinStake              int64           `json:"minStake"`
	MaxStake              int64           `json:"maxStake"`
	SupportedExpiries     []int64         `json:"supportedExpiries"`
	ExposureLimit         int64           `json:"exposureLimit"`
	NetExposureLimit      int64           `json:"netExposureLimit"`
	HedgeEnabled          bool            `json:"hedgeEnabled"`
	TargetHedgeRatio      float64         `json:"targetHedgeRatio"`
	HedgeThresholdPercent *float64        `json:"hedgeThresholdPercent"`
	MinHedgeNotional      int64           `json:"minHedgeNotional"`
	MaxHedgeNotional      int64           `json:"maxHedgeNotional"`
	TradingWindows        []TradingWindow `json:"tradingWindows"`
	DisplayOrder          int64           `json:"displayOrder"`
	MarketOpen            bool            `json:"marketOpen"`
	// When a closed market next opens, epoch ms. Null while it is open, for
	// an inactive asset and for one without trading windows (always open).
	NextOpenAt *int64 `json:"nextOpenAt"`
}

// ToAssetView maps an asset; atMs is the instant marketOpen and nextOpenAt
// are evaluated at (TS defaults it to Date.now() - pass util.NowMs()).
func ToAssetView(a *Asset, atMs int64) AssetView {
	open := a.Active && util.IsWithinTradingHours(a.TradingWindows, atMs)
	var nextOpenAt *int64
	if a.Active && !open && len(a.TradingWindows) > 0 {
		if at, ok := util.NextTradingOpen(a.TradingWindows, atMs); ok {
			nextOpenAt = &at
		}
	}
	// Copied, not patched in place: the asset row may be shared by a cache.
	windows := make([]TradingWindow, len(a.TradingWindows))
	for i, w := range a.TradingWindows {
		if w.Days == nil {
			w.Days = []int{}
		}
		windows[i] = w
	}
	expiries := a.SupportedExpiries
	if expiries == nil {
		expiries = []int64{}
	}
	return AssetView{
		ID:                    a.ID.Hex(),
		Symbol:                a.Symbol,
		Name:                  a.Name,
		Category:              a.Category,
		Active:                a.Active,
		ProviderSymbol:        a.ProviderSymbol,
		Provider:              a.Provider,
		PricePrecision:        a.PricePrecision,
		MinStake:              a.MinStake,
		MaxStake:              a.MaxStake,
		SupportedExpiries:     expiries,
		ExposureLimit:         a.ExposureLimit,
		NetExposureLimit:      a.NetExposureLimit,
		HedgeEnabled:          a.HedgeEnabled,
		TargetHedgeRatio:      a.TargetHedgeRatio,
		HedgeThresholdPercent: a.HedgeThresholdPercent,
		MinHedgeNotional:      a.MinHedgeNotional,
		MaxHedgeNotional:      a.MaxHedgeNotional,
		TradingWindows:        windows,
		DisplayOrder:          a.DisplayOrder,
		MarketOpen:            open,
		NextOpenAt:            nextOpenAt,
	}
}
