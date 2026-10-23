package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
)

// CollPricingModels holds the versioned win-probability model the pricing
// engine prices entries with (docs/audit/pricing/pricing-spec.md). Versions
// are append-only: publishing a calibration inserts the next version and
// closes the previous one, and every trade records the version it was priced
// under, so a payout can always be recomputed from the exact model.
const CollPricingModels = "pricing_models"

// Pricing modes (RiskConfig.PricingMode).
const (
	PricingModeDynamic = "DYNAMIC"
	PricingModeFixed   = "FIXED"
)

var PricingModeValues = []string{PricingModeDynamic, PricingModeFixed}

// What the engine does when an input is outside its calibrated range
// (volatility) or a hedge it relies on is unavailable.
const (
	PricingActionWiden  = "WIDEN"
	PricingActionRefuse = "REFUSE"
)

var PricingActionValues = []string{PricingActionWiden, PricingActionRefuse}

// Model entry statuses. Only CALIBRATED entries price trades.
const (
	PricingEntryCalibrated       = "CALIBRATED"
	PricingEntryInsufficientData = "INSUFFICIENT_DATA"
	PricingEntryNotPriced        = "NOT_PRICED"
)

// PricingFeatureSpec fixes how a price window becomes model inputs. It is
// stored with each entry so the engine builds exactly the features the entry
// was calibrated on.
type PricingFeatureSpec struct {
	// Grid step: 1 (built from ticks) or 60 (built from 1-minute candles).
	ResolutionSeconds int64 `bson:"resolutionSeconds" json:"resolutionSeconds"`
	// Completed steps before "now" whose log returns give the volatility.
	WindowSteps int `bson:"windowSteps" json:"windowSteps"`
	// Steps the momentum feature looks back.
	MomentumSteps int `bson:"momentumSteps" json:"momentumSteps"`
	// Fewer observed (traded) steps than this in the window: refuse.
	MinObservedSteps int `bson:"minObservedSteps" json:"minObservedSteps"`
	// Lag (steps) of the local draw rate feature: the share of pairs of
	// window prices this many steps apart that were exactly equal. Set to the
	// expiry when it fits in half the window, otherwise 0 (feature unused).
	DrawLagSteps int `bson:"drawLagSteps" json:"drawLagSteps"`
}

// PricingRegime is one volatility tercile of an entry. SigmaUpTo is the
// inclusive upper bound of the per-step volatility; 0 means unbounded (the
// top regime). The add-ons are measured on the holdout period, after the
// parameters were fitted on the training period.
type PricingRegime struct {
	Name      string  `bson:"name" json:"name"`
	SigmaUpTo float64 `bson:"sigmaUpTo" json:"sigmaUpTo"`
	// Training draw rate of the regime (informational: the engine uses the
	// continuous draw model of the entry).
	DrawProb float64 `bson:"drawProb" json:"drawProb"`
	// Added to the modelled draw probability where a larger one hurts the
	// platform (refunded draws): one-sided holdout bias + 95% sampling margin.
	DrawUncertainty float64 `bson:"drawUncertainty" json:"drawUncertainty"`
	// Added to the customer's win probability before pricing: the worst
	// one-sided holdout bias over directions and customer-selectable subsets,
	// plus a 95% sampling margin (spec section 4.6).
	Uncertainty      float64 `bson:"uncertainty" json:"uncertainty"`
	TrainSamples     int     `bson:"trainSamples" json:"trainSamples"`
	HoldoutSamples   int     `bson:"holdoutSamples" json:"holdoutSamples"`
	HoldoutEffective float64 `bson:"holdoutEffective" json:"holdoutEffective"`
}

// PricingEntryMetrics are the out-of-sample figures behind an entry.
type PricingEntryMetrics struct {
	TrainFrom            int64   `bson:"trainFrom" json:"trainFrom"`
	TrainTo              int64   `bson:"trainTo" json:"trainTo"`
	HoldoutFrom          int64   `bson:"holdoutFrom" json:"holdoutFrom"`
	HoldoutTo            int64   `bson:"holdoutTo" json:"holdoutTo"`
	TestFrom             int64   `bson:"testFrom" json:"testFrom"`
	TestTo               int64   `bson:"testTo" json:"testTo"`
	StrideSeconds        int64   `bson:"strideSeconds" json:"strideSeconds"`
	TrainSamples         int     `bson:"trainSamples" json:"trainSamples"`
	HoldoutSamples       int     `bson:"holdoutSamples" json:"holdoutSamples"`
	HoldoutEffective     float64 `bson:"holdoutEffective" json:"holdoutEffective"`
	TestSamples          int     `bson:"testSamples" json:"testSamples"`
	TestEffectiveSamples float64 `bson:"testEffectiveSamples" json:"testEffectiveSamples"`
	Excluded             int     `bson:"excluded" json:"excluded"`
	BrierTest            float64 `bson:"brierTest" json:"brierTest"`
	BrierBaselineTest    float64 `bson:"brierBaselineTest" json:"brierBaselineTest"`
	LogLossTest          float64 `bson:"logLossTest" json:"logLossTest"`
	LogLossBaselineTest  float64 `bson:"logLossBaselineTest" json:"logLossBaselineTest"`
	ECETest              float64 `bson:"eceTest" json:"eceTest"`
	DrawRateTest         float64 `bson:"drawRateTest" json:"drawRateTest"`
}

// PricingModelEntry is the model for one (instrument, expiry, price source).
type PricingModelEntry struct {
	// Lower-case provider symbol (btcusdt) and the platform symbol (BTC/USD).
	ProviderSymbol string `bson:"providerSymbol" json:"providerSymbol"`
	Symbol         string `bson:"symbol" json:"symbol"`
	ExpirySeconds  int64  `bson:"expirySeconds" json:"expirySeconds"`
	// The market-data provider whose prices the entry was calibrated on
	// ("binance"). The engine refuses to price a feed from another source.
	PriceSource  string             `bson:"priceSource" json:"priceSource"`
	Status       string             `bson:"status" json:"status"`
	StatusReason string             `bson:"statusReason" json:"statusReason"`
	Features     PricingFeatureSpec `bson:"features" json:"features"`
	// P(UP | no draw) = 1 / (1 + exp(-(Intercept + MomentumCoef * z))).
	Intercept    float64 `bson:"intercept" json:"intercept"`
	MomentumCoef float64 `bson:"momentumCoef" json:"momentumCoef"`
	// False when the momentum term did not earn its place on the validation
	// split; MomentumCoef is then 0. Intercept is 0 when a drift did not.
	MomentumUsed bool `bson:"momentumUsed" json:"momentumUsed"`
	// P(draw) = 1 / (1 + exp(-(DrawIntercept + DrawSigmaCoef * ln sigma
	//                          + DrawLocalCoef * logit(local draw rate)))).
	DrawIntercept float64 `bson:"drawIntercept" json:"drawIntercept"`
	DrawSigmaCoef float64 `bson:"drawSigmaCoef" json:"drawSigmaCoef"`
	DrawLocalCoef float64 `bson:"drawLocalCoef" json:"drawLocalCoef"`
	// Calibrated per-step volatility range (0.5th to 99.5th percentile of the
	// training set). Outside it the engine widens or refuses (config).
	SigmaMin float64             `bson:"sigmaMin" json:"sigmaMin"`
	SigmaMax float64             `bson:"sigmaMax" json:"sigmaMax"`
	Regimes  []PricingRegime     `bson:"regimes" json:"regimes"`
	Metrics  PricingEntryMetrics `bson:"metrics" json:"metrics"`
}

// PricingModel is one published calibration.
type PricingModel struct {
	ID      bson.ObjectID `bson:"_id,omitempty" json:"id"`
	Version int64         `bson:"version" json:"version"`
	Active  bool          `bson:"active" json:"active"`
	// Model family, e.g. "logit-momentum-v1".
	Name   string `bson:"name" json:"name"`
	Method string `bson:"method" json:"method"`
	// Where the calibration came from: the CLI run, its data window and the
	// SHA-256 of the entries, so the stored model can be matched to its
	// report in docs/audit/pricing.
	GeneratedAt time.Time           `bson:"generatedAt" json:"generatedAt"`
	DataFrom    int64               `bson:"dataFrom" json:"dataFrom"`
	DataTo      int64               `bson:"dataTo" json:"dataTo"`
	Source      string              `bson:"source" json:"source"`
	Hash        string              `bson:"hash" json:"hash"`
	Report      string              `bson:"report" json:"report"`
	Entries     []PricingModelEntry `bson:"entries" json:"entries"`
	PublishedBy string              `bson:"publishedBy" json:"publishedBy"`
	Note        *string             `bson:"note" json:"note"`
	CreatedAt   time.Time           `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time           `bson:"updatedAt" json:"updatedAt"`
}

func init() {
	c := CollPricingModels
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "version", Value: -1}}, Options: options.Index().SetUnique(true)},
		// At most one active model.
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "active", Value: 1}},
			Options:    options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "active", Value: true}}),
		},
	)
}

// Calibrated per-second volatility baselines (fraction per sqrt(second)):
// the root-mean-square 1-second log return of Binance BTCUSDT / ETHUSDT over
// the 14 days to 2026-10-16 (docs/audit/pricing/calibration-report.md). The
// spec's 9.0e-5 placeholder is twice the observed level, which would read a
// normal market as calm and switch the volatility deduction off.
const (
	DefaultBTCBaselineVol = 4.45e-5
	DefaultETHBaselineVol = 6.78e-5
)

// PricingComponents are every term of one payout calculation (spec 6, 7),
// so a payout can be reproduced exactly. Payout terms are fractions (0.02 =
// 2 points); money is minor units.
type PricingComponents struct {
	Base float64 `bson:"base" json:"base"`
	Min  float64 `bson:"min" json:"min"`
	Max  float64 `bson:"max" json:"max"`
	Step float64 `bson:"step" json:"step"`

	// Volatility (spec 5): EWMA sigma per sqrt(second), the baseline and
	// their ratio.
	Sigma       float64 `bson:"sigma" json:"sigma"`
	BaselineVol float64 `bson:"baselineVol" json:"baselineVol"`
	VolRatio    float64 `bson:"volRatio" json:"volRatio"`
	// Spread of the quote priced on and the expected move over the expiry.
	SpreadBps       float64 `bson:"spreadBps" json:"spreadBps"`
	ExpectedMoveBps float64 `bson:"expectedMoveBps" json:"expectedMoveBps"`
	ExpiryFactor    float64 `bson:"expiryFactor" json:"expiryFactor"`

	VolAdj      float64 `bson:"volAdj" json:"volAdj"`
	SpreadAdj   float64 `bson:"spreadAdj" json:"spreadAdj"`
	HedgeCost   float64 `bson:"hedgeCost" json:"hedgeCost"`
	HedgeAdj    float64 `bson:"hedgeAdj" json:"hedgeAdj"`
	ExposureAdj float64 `bson:"exposureAdj" json:"exposureAdj"`
	MarginAdj   float64 `bson:"marginAdj" json:"marginAdj"`
	// Added when a hedge the asset relies on is unavailable (WIDEN).
	HedgeUnavailableAdj float64 `bson:"hedgeUnavailableAdj" json:"hedgeUnavailableAdj"`
	HedgeAvailable      bool    `bson:"hedgeAvailable" json:"hedgeAvailable"`
	HedgeBasis          string  `bson:"hedgeBasis" json:"hedgeBasis"`
	// Venue estimate of the hedge cost on the Binance basis (informational).
	HedgeVenueCost float64 `bson:"hedgeVenueCost" json:"hedgeVenueCost"`

	// Exposure (spec 7): L(D) before the trade, the limit, and the
	// utilisation at the base payout (estimate) and at the final payout.
	Liability       int64   `bson:"liability" json:"liability"`
	MaxExposure     int64   `bson:"maxExposure" json:"maxExposure"`
	UtilEstimate    float64 `bson:"utilEstimate" json:"utilEstimate"`
	UtilFinal       float64 `bson:"utilFinal" json:"utilFinal"`
	Raw             float64 `bson:"raw" json:"raw"`
	Clamped         float64 `bson:"clamped" json:"clamped"`
	Payout          float64 `bson:"payout" json:"payout"`
	KVol            float64 `bson:"kVol" json:"kVol"`
	MaxVolAdj       float64 `bson:"maxVolAdj" json:"maxVolAdj"`
	VolBlockRatio   float64 `bson:"volBlockRatio" json:"volBlockRatio"`
	KSpread         float64 `bson:"kSpread" json:"kSpread"`
	MaxSpreadAdj    float64 `bson:"maxSpreadAdj" json:"maxSpreadAdj"`
	KExposure       float64 `bson:"kExposure" json:"kExposure"`
	MaxExposureAdj  float64 `bson:"maxExposureAdj" json:"maxExposureAdj"`
	QuoteTimestamp  int64   `bson:"quoteTimestamp" json:"quoteTimestamp"`
	QuoteReceivedAt int64   `bson:"quoteReceivedAt" json:"quoteReceivedAt"`
}

// PricingGuard is the calibrated safety check of one offer (audit F-02,
// F-03): the payout must leave a conservative expected platform result of
// at least the configured edge. Probabilities are for the offered direction.
type PricingGuard struct {
	Enabled bool   `bson:"enabled" json:"enabled"`
	OK      bool   `bson:"ok" json:"ok"`
	Reason  string `bson:"reason,omitempty" json:"reason,omitempty"`
	// Model provenance.
	ModelName    string `bson:"modelName" json:"modelName"`
	ModelVersion int64  `bson:"modelVersion" json:"modelVersion"`
	ModelHash    string `bson:"modelHash" json:"modelHash"`
	Regime       string `bson:"regime" json:"regime"`
	// Inputs: 1-second-grid sigma, momentum z-score, observed seconds.
	Sigma         float64 `bson:"sigma" json:"sigma"`
	Momentum      float64 `bson:"momentum" json:"momentum"`
	ObservedSteps int     `bson:"observedSteps" json:"observedSteps"`
	// Model outputs and the bounds the guard uses.
	ProbWin         float64 `bson:"probWin" json:"probWin"`
	ProbDraw        float64 `bson:"probDraw" json:"probDraw"`
	Uncertainty     float64 `bson:"uncertainty" json:"uncertainty"`
	DrawUncertainty float64 `bson:"drawUncertainty" json:"drawUncertainty"`
	// Payout at which the conservative result equals the minimum edge.
	BreakEvenPayout float64 `bson:"breakEvenPayout" json:"breakEvenPayout"`
	// Expected result per unit stake at the offered payout: model point
	// estimate and the conservative bound.
	PointEdge        float64 `bson:"pointEdge" json:"pointEdge"`
	ConservativeEdge float64 `bson:"conservativeEdge" json:"conservativeEdge"`
	MinEdge          float64 `bson:"minEdge" json:"minEdge"`
	// The edge checked was the conservative one (else the point estimate).
	Conservative   bool `bson:"conservative" json:"conservative"`
	SourceMismatch bool `bson:"sourceMismatch" json:"sourceMismatch"`
}

// TradePricing is the pricing decision frozen on a trade: the locked payout,
// the offer it came from, the rule version and every component.
type TradePricing struct {
	Mode          string  `bson:"mode" json:"mode"`
	RuleVersion   string  `bson:"ruleVersion" json:"ruleVersion"`
	RiskConfigVer int64   `bson:"riskConfigVersion" json:"riskConfigVersion"`
	PayoutCfgVer  int64   `bson:"payoutConfigVersion" json:"payoutConfigVersion"`
	PayoutPercent float64 `bson:"payoutPercent" json:"payoutPercent"`
	// The offer accepted (spec 8.2). Inline: no offer was sent (an app that
	// predates offers) and the trade was priced at acceptance.
	OfferID         string  `bson:"offerId" json:"offerId"`
	Inline          bool    `bson:"pricedAtAcceptance" json:"inline"`
	OfferCreatedAt  int64   `bson:"offerCreatedAt" json:"offerCreatedAt"`
	OfferValidUntil int64   `bson:"offerValidUntil" json:"offerValidUntil"`
	ReferencePrice  float64 `bson:"referencePrice" json:"referencePrice"`
	// Mid at acceptance and its move from the offer's reference, bps.
	AcceptMid              float64           `bson:"acceptMid" json:"acceptMid"`
	SlippageBps            float64           `bson:"slippageBps" json:"slippageBps"`
	DisplayedPayoutPercent *float64          `bson:"displayedPayoutPercent" json:"displayedPayoutPercent"`
	FeedProvider           string            `bson:"feedProvider" json:"feedProvider"`
	Components             PricingComponents `bson:"components" json:"components"`
	Guard                  PricingGuard      `bson:"guard" json:"guard"`
}

// CollPricingOffers: every offer issued or blocked (spec 10: offer_created,
// offer_blocked), kept for OfferRetention by a TTL index. The live copy that
// acceptance consumes is in Redis; this is the audit record.
const CollPricingOffers = "pricing_offers"

// OfferRetention is how long offer records are kept.
const OfferRetention = 7 * 24 * time.Hour

// Offer statuses.
const (
	OfferStatusOK      = "ok"
	OfferStatusBlocked = "blocked"
)

// PricingOffer is one offer (or refusal to offer) for one direction.
type PricingOffer struct {
	ID            string            `bson:"_id" json:"offerId"`
	Status        string            `bson:"status" json:"status"`
	Reason        string            `bson:"reason,omitempty" json:"reason,omitempty"`
	ReasonCode    string            `bson:"reasonCode,omitempty" json:"reasonCode,omitempty"`
	UserID        string            `bson:"userId" json:"userId"`
	AssetID       string            `bson:"assetId" json:"assetId"`
	Symbol        string            `bson:"symbol" json:"symbol"`
	Direction     Direction         `bson:"direction" json:"direction"`
	ExpirySeconds int64             `bson:"expirySeconds" json:"expirySeconds"`
	Stake         int64             `bson:"stake" json:"stake"`
	AccountType   string            `bson:"accountType" json:"accountType"`
	Mode          string            `bson:"mode" json:"mode"`
	Payout        float64           `bson:"payout" json:"payout"`
	PayoutPercent float64           `bson:"payoutPercent" json:"payoutPercent"`
	Reference     float64           `bson:"referencePrice" json:"referencePrice"`
	CreatedAtMs   int64             `bson:"createdAtMs" json:"createdAt"`
	ValidUntil    int64             `bson:"validUntil" json:"validUntil"`
	RuleVersion   string            `bson:"ruleVersion" json:"ruleVersion"`
	RiskConfigVer int64             `bson:"riskConfigVersion" json:"riskConfigVersion"`
	PayoutCfgID   string            `bson:"payoutConfigId" json:"payoutConfigId"`
	PayoutCfgVer  int64             `bson:"payoutConfigVersion" json:"payoutConfigVersion"`
	FeedProvider  string            `bson:"feedProvider" json:"feedProvider"`
	Inline        bool              `bson:"pricedAtAcceptance" json:"inline"`
	Components    PricingComponents `bson:"components" json:"components"`
	Guard         PricingGuard      `bson:"guard" json:"guard"`
	// TTL anchor.
	CreatedAt time.Time `bson:"createdAt" json:"-"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollPricingOffers, Keys: bson.D{{Key: "createdAt", Value: 1}},
			Options: options.Index().SetExpireAfterSeconds(int32(OfferRetention / time.Second))},
		mongox.Index{Collection: CollPricingOffers, Keys: bson.D{{Key: "userId", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: CollPricingOffers, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
	)
}
