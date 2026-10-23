package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// Hedge execution ledger (docs/audit/hedging/hedge-lifecycle.md, F-08/F-09).
//
// hedge_fills is the record: one row per venue execution, unique on
// (venue, venueSymbol, tradeId), so a fill reported twice is stored once and
// can never be counted twice. Positions and realised P&L are a deterministic
// fold over it (average cost, in trade order); hedge_positions is that fold's
// cached result plus the venue reconciliation state.
const (
	CollHedgeFills       = "hedge_fills"
	CollHedgeFunding     = "hedge_funding"
	CollHedgePositions   = "hedge_positions"
	CollHedgeAlerts      = "hedge_alerts"
	CollHedgeFxRates     = "hedge_fx_rates"
	CollHedgeAdjustments = "hedge_adjustments"
)

// HedgeFill is one venue execution. Quantities and USDT amounts are 1e-8
// units; INR amounts are minor units at the fill's stored rate.
type HedgeFill struct {
	ID            bson.ObjectID  `bson:"_id,omitempty"`
	Venue         string         `bson:"venue"`
	VenueSymbol   string         `bson:"venueSymbol"`
	TradeID       int64          `bson:"tradeId"`
	VenueOrderID  string         `bson:"venueOrderId"`
	ClientOrderID string         `bson:"clientOrderId"`
	HedgeOrderID  *bson.ObjectID `bson:"hedgeOrderId"`
	AssetID       bson.ObjectID  `bson:"assetId"`
	Symbol        string         `bson:"symbol"`
	// BUY or SELL.
	Side    string  `bson:"side"`
	QtyE8   int64   `bson:"qtyE8"`
	Price   float64 `bson:"price"`
	QuoteE8 int64   `bson:"quoteE8"`
	// Commission as the venue charged it, and its USDT value (equal when
	// the commission asset is USDT; 0 with FeeConverted false otherwise).
	FeeE8        int64  `bson:"feeE8"`
	FeeAsset     string `bson:"feeAsset"`
	FeeUsdtE8    int64  `bson:"feeUsdtE8"`
	FeeConverted bool   `bson:"feeConverted"`
	// Binance's own realizedPnl for the fill (cross-check only).
	VenueRealizedPnlE8 int64 `bson:"venueRealizedPnlE8"`
	Maker              bool  `bson:"maker"`
	// Venue execution time, epoch ms: the fold orders fills by (time, id).
	TradeTime int64 `bson:"tradeTime"`
	// INR per USDT this fill is converted at, fixed when it is recorded.
	InrPerUsdt float64 `bson:"inrPerUsdt"`
	FxAsOf     int64   `bson:"fxAsOf"`
	FxStale    bool    `bson:"fxStale"`
	// Mid the order was sized at, for the realised cost of this fill.
	ReferenceMid float64 `bson:"referenceMid"`
	// Fold results: gross P&L realised by the closing part of this fill
	// (average cost), the position after it, and the INR values.
	RealisedE8      int64     `bson:"realisedE8"`
	RealisedInr     int64     `bson:"realisedInr"`
	FeeInr          int64     `bson:"feeInr"`
	PositionAfterE8 int64     `bson:"positionAfterE8"`
	AvgPriceAfter   float64   `bson:"avgPriceAfter"`
	CreatedAt       time.Time `bson:"createdAt"`
}

// HedgeFunding is one funding payment (Binance income FUNDING_FEE), unique
// on (venue, tranId). Positive = received.
type HedgeFunding struct {
	ID          bson.ObjectID `bson:"_id,omitempty"`
	Venue       string        `bson:"venue"`
	VenueSymbol string        `bson:"venueSymbol"`
	TranID      int64         `bson:"tranId"`
	AssetID     bson.ObjectID `bson:"assetId"`
	AmountE8    int64         `bson:"amountE8"`
	Asset       string        `bson:"asset"`
	Time        int64         `bson:"time"`
	InrPerUsdt  float64       `bson:"inrPerUsdt"`
	AmountInr   int64         `bson:"amountInr"`
	FxStale     bool          `bson:"fxStale"`
	CreatedAt   time.Time     `bson:"createdAt"`
}

// HedgeAdjustment is a manual correction of hedge P&L (a venue correction,
// a fee rebate): never an edit of a fill. Minor units, signed (+ = gain).
type HedgeAdjustment struct {
	ID        bson.ObjectID  `bson:"_id,omitempty"`
	Reference string         `bson:"reference"`
	AssetID   bson.ObjectID  `bson:"assetId"`
	AmountInr int64          `bson:"amountInr"`
	Reason    string         `bson:"reason"`
	CreatedBy *bson.ObjectID `bson:"createdBy"`
	CreatedAt time.Time      `bson:"createdAt"`
}

// Reconciliation states of a hedge position.
const (
	HedgeReconOK            = "OK"
	HedgeReconDrift         = "DRIFT"
	HedgeReconUnknown       = "UNKNOWN"
	HedgeReconNotApplicable = "NOT_APPLICABLE"
	HedgeReconNever         = "NEVER"
)

// HedgePosition is the per-asset hedge position: the fold of its fills plus
// funding, the latest mark, and the last comparison with the venue.
type HedgePosition struct {
	ID          bson.ObjectID `bson:"_id,omitempty"`
	AssetID     bson.ObjectID `bson:"assetId"`
	Symbol      string        `bson:"symbol"`
	Venue       string        `bson:"venue"`
	VenueSymbol string        `bson:"venueSymbol"`
	// Signed quantity from fills (+ long), average entry price.
	QtyE8    int64   `bson:"qtyE8"`
	AvgPrice float64 `bson:"avgPrice"`
	// Cumulative realised figures (USDT 1e-8, and INR minor units at the
	// stored rates). Realised is gross of fees; fees and funding are separate.
	RealisedE8  int64 `bson:"realisedE8"`
	FeesE8      int64 `bson:"feesE8"`
	FundingE8   int64 `bson:"fundingE8"`
	RealisedInr int64 `bson:"realisedInr"`
	FeesInr     int64 `bson:"feesInr"`
	FundingInr  int64 `bson:"fundingInr"`
	FillCount   int64 `bson:"fillCount"`
	LastTradeID int64 `bson:"lastTradeId"`
	// Mark-to-market.
	MarkPrice      float64    `bson:"markPrice"`
	MarkInrPerUsdt float64    `bson:"markInrPerUsdt"`
	MarkedAt       *time.Time `bson:"markedAt"`
	UnrealisedE8   int64      `bson:"unrealisedE8"`
	UnrealisedInr  int64      `bson:"unrealisedInr"`
	// |qty| x mark x rate, minor units: the protection actually held.
	NotionalInr int64 `bson:"notionalInr"`
	// Venue reconciliation.
	VenueQtyE8         *int64     `bson:"venueQtyE8"`
	ReconcileStatus    string     `bson:"reconcileStatus"`
	ReconciledAt       *time.Time `bson:"reconciledAt"`
	ReconcileCheckedAt *time.Time `bson:"reconcileCheckedAt"`
	DriftE8            int64      `bson:"driftE8"`
	ReconcileError     *string    `bson:"reconcileError"`
	// Funding income has been read up to this venue time (epoch ms).
	FundingSyncedTo int64     `bson:"fundingSyncedTo"`
	CreatedAt       time.Time `bson:"createdAt"`
	UpdatedAt       time.Time `bson:"updatedAt"`
}

// Hedge alert codes.
const (
	HedgeAlertPositionDrift    = "position_drift"
	HedgeAlertLedgerMismatch   = "ledger_mismatch"
	HedgeAlertVenueUnreachable = "venue_unreachable"
	HedgeAlertFxStale          = "fx_stale"
	HedgeAlertOrderUnknown     = "order_unknown"
	HedgeAlertFeeAsset         = "fee_not_usdt"
	HedgeAlertFillsIncomplete  = "fills_incomplete"
)

// HedgeAlert follows the USDT alert pattern: one open row per problem (key),
// a repeat sighting only bumps it; operators resolve with a note.
type HedgeAlert struct {
	ID             bson.ObjectID  `bson:"_id,omitempty"`
	Key            string         `bson:"key"`
	Code           string         `bson:"code"`
	Severity       string         `bson:"severity"`
	Message        string         `bson:"message"`
	Details        any            `bson:"details"`
	AssetID        *bson.ObjectID `bson:"assetId"`
	Status         string         `bson:"status"`
	Occurrences    int64          `bson:"occurrences"`
	FirstSeenAt    time.Time      `bson:"firstSeenAt"`
	LastSeenAt     time.Time      `bson:"lastSeenAt"`
	ResolvedBy     *bson.ObjectID `bson:"resolvedBy"`
	ResolvedAt     *time.Time     `bson:"resolvedAt"`
	ResolutionNote *string        `bson:"resolutionNote"`
	CreatedAt      time.Time      `bson:"createdAt"`
	UpdatedAt      time.Time      `bson:"updatedAt"`
}

type HedgeAlertView struct {
	ID             string    `json:"id"`
	Code           string    `json:"code"`
	Severity       string    `json:"severity"`
	Message        string    `json:"message"`
	Details        any       `json:"details"`
	AssetID        *string   `json:"assetId"`
	Status         string    `json:"status"`
	Occurrences    int64     `json:"occurrences"`
	FirstSeenAt    iso.Time  `json:"firstSeenAt"`
	LastSeenAt     iso.Time  `json:"lastSeenAt"`
	ResolvedAt     *iso.Time `json:"resolvedAt"`
	ResolutionNote *string   `json:"resolutionNote"`
}

func ToHedgeAlertView(a *HedgeAlert) HedgeAlertView {
	v := HedgeAlertView{
		ID: a.ID.Hex(), Code: a.Code, Severity: a.Severity, Message: a.Message, Details: mongox.JSONValue(a.Details),
		AssetID: hexPtr(a.AssetID), Status: a.Status, Occurrences: a.Occurrences,
		FirstSeenAt: iso.From(a.FirstSeenAt), LastSeenAt: iso.From(a.LastSeenAt),
		ResolvedAt: iso.Ptr(a.ResolvedAt), ResolutionNote: a.ResolutionNote,
	}
	if v.Details == nil {
		v.Details = map[string]any{}
	}
	return v
}

// HedgeFxRate is an INR-per-USDT rate an operator entered, append-only: the
// newest asOf wins against the environment's rate.
type HedgeFxRate struct {
	ID        bson.ObjectID  `bson:"_id,omitempty"`
	Rate      float64        `bson:"rate"`
	Source    string         `bson:"source"`
	AsOf      time.Time      `bson:"asOf"`
	SetBy     *bson.ObjectID `bson:"setBy"`
	Note      *string        `bson:"note"`
	CreatedAt time.Time      `bson:"createdAt"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollHedgeFills,
			Keys:    bson.D{{Key: "venue", Value: 1}, {Key: "venueSymbol", Value: 1}, {Key: "tradeId", Value: 1}},
			Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollHedgeFills, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "tradeTime", Value: 1}, {Key: "tradeId", Value: 1}}},
		mongox.Index{Collection: CollHedgeFills, Keys: bson.D{{Key: "hedgeOrderId", Value: 1}}},
		mongox.Index{Collection: CollHedgeFills, Keys: bson.D{{Key: "tradeTime", Value: -1}}},
		mongox.Index{Collection: CollHedgeFunding,
			Keys:    bson.D{{Key: "venue", Value: 1}, {Key: "tranId", Value: 1}},
			Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollHedgeFunding, Keys: bson.D{{Key: "assetId", Value: 1}, {Key: "time", Value: 1}}},
		mongox.Index{Collection: CollHedgePositions, Keys: bson.D{{Key: "assetId", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollHedgeAlerts, Keys: bson.D{{Key: "key", Value: 1}},
			Options: options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "status", Value: AlertOpen}})},
		mongox.Index{Collection: CollHedgeAlerts, Keys: bson.D{{Key: "status", Value: 1}, {Key: "lastSeenAt", Value: -1}}},
		mongox.Index{Collection: CollHedgeFxRates, Keys: bson.D{{Key: "asOf", Value: -1}}},
		mongox.Index{Collection: CollHedgeAdjustments, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollHedgeAdjustments, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}
