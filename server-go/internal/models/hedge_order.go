package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollHedgeOrders: hedge orders sent to the hedge venue (Binance USDⓈ-M
// futures, or the paper venue). docs/audit/hedging/hedge-lifecycle.md.
// Hedging manages the platform's aggregate market exposure only; the
// settlement engine never reads this collection.
const CollHedgeOrders = "hedge_orders"

// QtyScale: quantities (base asset) and USDT amounts are stored as int64 in
// units of 1e-8, so every amount Binance reports (at most 8 decimals) is held
// exactly and fills can be summed without float drift.
const QtyScale int64 = 100_000_000

// HedgeLeg is one open trade as the hedge target saw it: d is +1 for UP (the
// platform is short a digital call) and -1 for DOWN, p the payout on a win
// (stake + profit, minor units), k the strike (entry price), e the expiry
// (epoch ms).
type HedgeLeg struct {
	Dir      int8    `bson:"d" json:"d"`
	Payout   int64   `bson:"p" json:"p"`
	Strike   float64 `bson:"k" json:"k"`
	ExpiryMs int64   `bson:"e" json:"e"`
}

// HedgeTargetDetail is the delta-based hedge target with every input behind
// it (risk.ComputeHedgeTarget). Money is minor units (INR paise), signed:
// positive means the platform needs to be LONG the underlying.
type HedgeTargetDetail struct {
	// Sum over eligible trades of d * p * phi(d2) / (sigma * sqrt(tau)).
	BookDelta int64 `bson:"bookDelta" json:"bookDelta"`
	// h x BookDelta, before caps.
	RawTarget int64 `bson:"rawTarget" json:"rawTarget"`
	// The target after caps.
	TargetSigned int64 `bson:"targetSigned" json:"targetSigned"`
	// min(asset max hedge notional, liability multiple x expiry liability).
	Cap      int64  `bson:"cap" json:"cap"`
	CappedBy string `bson:"cappedBy" json:"cappedBy"`
	// h = min(band ratio + volatility boost, asset ceiling).
	HedgeRatio      float64 `bson:"hedgeRatio" json:"hedgeRatio"`
	BandRatio       float64 `bson:"bandRatio" json:"bandRatio"`
	VolatilityBoost float64 `bson:"volatilityBoost" json:"volatilityBoost"`
	AssetCeiling    float64 `bson:"assetCeiling" json:"assetCeiling"`
	// Volatility per sqrt(second) used by the delta, and where it came from.
	Sigma       float64 `bson:"sigma" json:"sigma"`
	SigmaSource string  `bson:"sigmaSource" json:"sigmaSource"`
	// Per-tick realised volatility (the risk score input) for the boost.
	TickVolatility     float64 `bson:"tickVolatility" json:"tickVolatility"`
	ReferencePrice     float64 `bson:"referencePrice" json:"referencePrice"`
	DecidedAt          int64   `bson:"decidedAt" json:"decidedAt"`
	EligibleTrades     int64   `bson:"eligibleTrades" json:"eligibleTrades"`
	ExcludedTrades     int64   `bson:"excludedTrades" json:"excludedTrades"`
	GammaWindowSeconds int64   `bson:"gammaWindowSeconds" json:"gammaWindowSeconds"`
	MaxHedgeNotional   int64   `bson:"maxHedgeNotional" json:"maxHedgeNotional"`
	LiabilityMultiple  float64 `bson:"liabilityMultiple" json:"liabilityMultiple"`
	ExpiryLiability    int64   `bson:"expiryLiability" json:"expiryLiability"`
	// Why the target is zero, when it is ("" otherwise).
	Reason string `bson:"reason" json:"reason"`
}

// HedgeDecisionSnapshot records every decision input, so the target and the
// order can be recomputed from the stored row alone (rule 9; replay with
// hedging.ReplayDecision).
type HedgeDecisionSnapshot struct {
	NetExposureAtDecision   int64     `bson:"netExposureAtDecision"`
	GrossExposureAtDecision int64     `bson:"grossExposureAtDecision"`
	UtilisationPercent      float64   `bson:"utilisationPercent"`
	RiskLevel               RiskLevel `bson:"riskLevel"`
	HedgeLevel              string    `bson:"hedgeLevel"`
	TargetHedgeRatio        float64   `bson:"targetHedgeRatio"`
	// |target|, minor units.
	TargetHedgeNotional int64 `bson:"targetHedgeNotional"`
	// |filled position| at decision, minor units at the venue price.
	ExistingHedgeNotional  int64    `bson:"existingHedgeNotional"`
	RiskConfigVersion      int64    `bson:"riskConfigVersion"`
	Volatility             float64  `bson:"volatility"`
	SecondsToNearestExpiry *float64 `bson:"secondsToNearestExpiry"`

	Target HedgeTargetDetail `bson:"target"`
	// The open trades the target was computed from (at most MaxDecisionLegs;
	// LegsTruncated says some were left out, LegsCount how many there were).
	Legs          []HedgeLeg `bson:"legs"`
	LegsCount     int64      `bson:"legsCount"`
	LegsTruncated bool       `bson:"legsTruncated"`

	// Conversion and execution inputs.
	VenuePrice    float64 `bson:"venuePrice"`
	InrPerUsdt    float64 `bson:"inrPerUsdt"`
	FxAsOf        int64   `bson:"fxAsOf"`
	TargetQtyE8   int64   `bson:"targetQtyE8"`
	PositionQtyE8 int64   `bson:"positionQtyE8"`
	// target - position, signed, before exchange rules.
	OrderQtyE8 int64 `bson:"orderQtyE8"`
	// What the exchange rules allow (unsigned), and what is left over.
	ExecutableQtyE8  int64        `bson:"executableQtyE8"`
	ResidualQtyE8    int64        `bson:"residualQtyE8"`
	Rules            HedgeRules   `bson:"rules"`
	MinHedgeNotional int64        `bson:"minHedgeNotional"`
	MaxOrderUSDT     float64      `bson:"maxOrderUsdt"`
	EstimatedCost    HedgeCostBps `bson:"estimatedCost"`
}

// MaxDecisionLegs bounds the legs stored on one decision.
const MaxDecisionLegs = 2000

// HedgeRules are the venue's order filters for one symbol (exchangeInfo).
type HedgeRules struct {
	StepE8      int64   `bson:"stepE8" json:"stepE8"`
	MinQtyE8    int64   `bson:"minQtyE8" json:"minQtyE8"`
	MaxQtyE8    int64   `bson:"maxQtyE8" json:"maxQtyE8"`
	MinNotional float64 `bson:"minNotional" json:"minNotional"`
	TickSize    float64 `bson:"tickSize" json:"tickSize"`
}

// HedgeCostBps is the cost model, basis points of notional.
type HedgeCostBps struct {
	TakerFeeBps   float64 `bson:"takerFeeBps" json:"takerFeeBps"`
	HalfSpreadBps float64 `bson:"halfSpreadBps" json:"halfSpreadBps"`
	SlippageBps   float64 `bson:"slippageBps" json:"slippageBps"`
	FundingBps    float64 `bson:"fundingBps" json:"fundingBps"`
	PerSideBps    float64 `bson:"perSideBps" json:"perSideBps"`
	RoundTripBps  float64 `bson:"roundTripBps" json:"roundTripBps"`
}

type HedgeOrder struct {
	ID        bson.ObjectID `bson:"_id,omitempty"`
	Reference string        `bson:"reference"`
	AssetID   bson.ObjectID `bson:"assetId"`
	Symbol    string        `bson:"symbol"`
	// Provider name ("paper" | "binance") and the exact venue
	// ("paper" | "binance-testnet" | "binance-live").
	Provider    string `bson:"provider"`
	Venue       string `bson:"venue"`
	VenueSymbol string `bson:"venueSymbol"`
	// Our deterministic id, sent as Binance newClientOrderId: every retry
	// and every status query uses it, so the order exists at most once.
	ClientOrderID string `bson:"clientOrderId"`
	// Market side the platform takes: UP = BUY, DOWN = SELL.
	Side Direction `bson:"side"`
	// Sent reduce-only: the order can only shrink the position.
	ReduceOnly bool `bson:"reduceOnly"`
	// Requested and filled quantity (base asset, 1e-8 units). Filled is the
	// venue's executedQty; FillsQtyE8 is what the fills ledger holds.
	TargetQtyE8 int64 `bson:"targetQtyE8"`
	FilledQtyE8 int64 `bson:"filledQtyE8"`
	FillsQtyE8  int64 `bson:"fillsQtyE8"`
	FillCount   int64 `bson:"fillCount"`
	// Venue cumQuote (USDT, 1e-8) and the fees of the recorded fills.
	CumQuoteE8 int64  `bson:"cumQuoteE8"`
	FeeE8      int64  `bson:"feeE8"`
	FeeAsset   string `bson:"feeAsset"`
	FeeInr     int64  `bson:"feeInr"`
	// Requested / filled notional in minor units (INR paise) at the decision
	// price and at the fill prices, both at the decision's INR rate.
	RequestedNotional int64    `bson:"requestedNotional"`
	FilledNotional    int64    `bson:"filledNotional"`
	AverageFillPrice  *float64 `bson:"averageFillPrice"`
	// Venue price the order was sized at (mid), for realised cost.
	RequestPrice    *float64              `bson:"requestPrice"`
	Status          HedgeOrderStatus      `bson:"status"`
	VenueStatus     string                `bson:"venueStatus"`
	ProviderOrderID *string               `bson:"providerOrderId"`
	Decision        HedgeDecisionSnapshot `bson:"decision"`
	// Realised hedge P&L booked on this order's fills (average cost, net of
	// nothing - fees are separate), minor units. Unrealised P&L is a
	// position figure (hedge_positions), so it stays 0 here.
	RealisedPnl   int64 `bson:"realisedPnl"`
	UnrealisedPnl int64 `bson:"unrealisedPnl"`
	// Realised execution cost vs the decision mid, bps (fills only).
	RealisedCostBps *float64 `bson:"realisedCostBps"`
	// Lifecycle bookkeeping.
	SendAttempts int64      `bson:"sendAttempts"`
	SentAt       *time.Time `bson:"sentAt"`
	AcceptedAt   *time.Time `bson:"acceptedAt"`
	FinalAt      *time.Time `bson:"finalAt"`
	// Last time the venue confirmed this order's state.
	ReconciledAt *time.Time `bson:"reconciledAt"`
	// assetId hex while the order is working, absent once final: a unique
	// index on it allows one working order per asset.
	WorkingSlot      *string    `bson:"workingSlot,omitempty"`
	ClosedAt         *time.Time `bson:"closedAt"`
	Error            *string    `bson:"error"`
	RequestLatencyMs *int64     `bson:"requestLatencyMs"`
	FillLatencyMs    *int64     `bson:"fillLatencyMs"`
	IdempotencyKey   string     `bson:"idempotencyKey"`
	CreatedAt        time.Time  `bson:"createdAt"`
	UpdatedAt        time.Time  `bson:"updatedAt"`
	V                int64      `bson:"__v"`
}

// Working reports whether the order can still change at the venue.
func (o *HedgeOrder) Working() bool { return HedgeOrderWorking(o.Status) }

// HedgeOrderWorking: PENDING, ACCEPTED and PARTIALLY_FILLED orders are
// contingent - only their fills count as protection.
func HedgeOrderWorking(s HedgeOrderStatus) bool {
	return s == HedgeOrderStatusPending || s == HedgeOrderStatusAccepted || s == HedgeOrderStatusPartiallyFilled
}

func init() {
	c := CollHedgeOrders
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "idempotencyKey", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "status", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "status", Value: 1}, {Key: "createdAt", Value: -1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "workingSlot", Value: 1}},
			Options:    options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "workingSlot", Value: bson.D{{Key: "$type", Value: "string"}}}}),
		},
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "clientOrderId", Value: 1}},
			Options:    options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "clientOrderId", Value: bson.D{{Key: "$type", Value: "string"}}}}),
		},
	)
}

type HedgeOrderView struct {
	ID                  string           `json:"id"`
	Reference           string           `json:"reference"`
	AssetID             string           `json:"assetId"`
	Symbol              string           `json:"symbol"`
	Provider            string           `json:"provider"`
	Side                Direction        `json:"side"`
	RequestedNotional   int64            `json:"requestedNotional"`
	FilledNotional      int64            `json:"filledNotional"`
	RemainingNotional   int64            `json:"remainingNotional"`
	AverageFillPrice    *float64         `json:"averageFillPrice"`
	RequestPrice        *float64         `json:"requestPrice"`
	Status              HedgeOrderStatus `json:"status"`
	ProviderOrderID     *string          `json:"providerOrderId"`
	RiskLevel           RiskLevel        `json:"riskLevel"`
	HedgeLevel          string           `json:"hedgeLevel"`
	TargetHedgeNotional int64            `json:"targetHedgeNotional"`
	RealisedPnl         int64            `json:"realisedPnl"`
	UnrealisedPnl       int64            `json:"unrealisedPnl"`
	Error               *string          `json:"error"`
	RequestLatencyMs    *int64           `json:"requestLatencyMs"`
	CreatedAt           iso.Time         `json:"createdAt"`
	UpdatedAt           iso.Time         `json:"updatedAt"`

	// Quantity lifecycle (base asset units, decimals).
	Venue            string    `json:"venue"`
	VenueSymbol      string    `json:"venueSymbol"`
	ClientOrderID    string    `json:"clientOrderId"`
	VenueStatus      string    `json:"venueStatus"`
	TargetQty        float64   `json:"targetQty"`
	FilledQty        float64   `json:"filledQty"`
	RemainingQty     float64   `json:"remainingQty"`
	FillCount        int64     `json:"fillCount"`
	Fee              float64   `json:"fee"`
	FeeAsset         string    `json:"feeAsset"`
	FeeInr           int64     `json:"feeInr"`
	Working          bool      `json:"working"`
	SendAttempts     int64     `json:"sendAttempts"`
	EstimatedCostBps float64   `json:"estimatedCostBps"`
	RealisedCostBps  *float64  `json:"realisedCostBps"`
	InrPerUsdt       float64   `json:"inrPerUsdt"`
	ReconciledAt     *iso.Time `json:"reconciledAt"`
	FinalAt          *iso.Time `json:"finalAt"`
}

// QtyFloat converts 1e-8 units to a decimal for display.
func QtyFloat(v int64) float64 { return float64(v) / float64(QtyScale) }

func ToHedgeView(o *HedgeOrder) HedgeOrderView {
	return HedgeOrderView{
		ID:                  o.ID.Hex(),
		Reference:           o.Reference,
		AssetID:             o.AssetID.Hex(),
		Symbol:              o.Symbol,
		Provider:            o.Provider,
		Side:                o.Side,
		RequestedNotional:   o.RequestedNotional,
		FilledNotional:      o.FilledNotional,
		RemainingNotional:   max(0, o.RequestedNotional-o.FilledNotional),
		AverageFillPrice:    o.AverageFillPrice,
		RequestPrice:        o.RequestPrice,
		Status:              o.Status,
		ProviderOrderID:     o.ProviderOrderID,
		RiskLevel:           o.Decision.RiskLevel,
		HedgeLevel:          o.Decision.HedgeLevel,
		TargetHedgeNotional: o.Decision.TargetHedgeNotional,
		RealisedPnl:         o.RealisedPnl,
		UnrealisedPnl:       o.UnrealisedPnl,
		Error:               o.Error,
		RequestLatencyMs:    o.RequestLatencyMs,
		CreatedAt:           iso.From(o.CreatedAt),
		UpdatedAt:           iso.From(o.UpdatedAt),

		Venue:            o.Venue,
		VenueSymbol:      o.VenueSymbol,
		ClientOrderID:    o.ClientOrderID,
		VenueStatus:      o.VenueStatus,
		TargetQty:        QtyFloat(o.TargetQtyE8),
		FilledQty:        QtyFloat(o.FilledQtyE8),
		RemainingQty:     QtyFloat(max(0, o.TargetQtyE8-o.FilledQtyE8)),
		FillCount:        o.FillCount,
		Fee:              QtyFloat(o.FeeE8),
		FeeAsset:         o.FeeAsset,
		FeeInr:           o.FeeInr,
		Working:          o.Working(),
		SendAttempts:     o.SendAttempts,
		EstimatedCostBps: o.Decision.EstimatedCost.RoundTripBps,
		RealisedCostBps:  o.RealisedCostBps,
		InrPerUsdt:       o.Decision.InrPerUsdt,
		ReconciledAt:     iso.Ptr(o.ReconciledAt),
		FinalAt:          iso.Ptr(o.FinalAt),
	}
}
