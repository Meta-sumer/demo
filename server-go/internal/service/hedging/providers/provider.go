// Package providers is the hedge venue contract (docs/audit/hedging/
// hedge-lifecycle.md). The hedging engine is coupled to Venue, never to an
// exchange SDK.
//
// Two venues exist: Binance USDⓈ-M perpetual futures (binance.go; testnet or
// live) and an in-process paper venue (paper.go). Quantities and USDT amounts
// cross this boundary as int64 in 1e-8 units (models.QtyScale), parsed from
// the venue's decimal strings without floats, so a fill is recorded exactly.
package providers

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
)

type HedgeSide string

const (
	SideBuy  HedgeSide = "BUY"
	SideSell HedgeSide = "SELL"
)

// Venue order statuses (Binance's own names; the paper venue uses them too).
const (
	VenueNew             = "NEW"
	VenuePartiallyFilled = "PARTIALLY_FILLED"
	VenueFilled          = "FILLED"
	VenueCanceled        = "CANCELED"
	VenueRejected        = "REJECTED"
	VenueExpired         = "EXPIRED"
	VenueExpiredInMatch  = "EXPIRED_IN_MATCH"
)

// Quote is the top of the venue's book.
type Quote struct {
	Bid, Ask       float64
	BidQty, AskQty float64
	Time           int64
}

// Mid of the book; 0 when one side is missing.
func (q Quote) Mid() float64 {
	if q.Bid <= 0 || q.Ask <= 0 {
		return 0
	}
	return (q.Bid + q.Ask) / 2
}

// Mark is the venue's mark price and funding state.
type Mark struct {
	MarkPrice       float64
	IndexPrice      float64
	FundingRate     float64
	NextFundingTime int64
	Time            int64
}

// Commission is the account's fee rate (fractions, e.g. 0.0005 = 5 bps).
type Commission struct {
	MakerRate, TakerRate float64
}

// OrderRequest is a MARKET order of QtyE8 (base asset, 1e-8 units).
type OrderRequest struct {
	Symbol        string
	Side          HedgeSide
	QtyE8         int64
	ClientOrderID string
	ReduceOnly    bool
}

// OrderState is the venue's view of one order.
type OrderState struct {
	Symbol        string
	ClientOrderID string
	VenueOrderID  string
	Status        string
	Side          HedgeSide
	OrigQtyE8     int64
	ExecutedQtyE8 int64
	CumQuoteE8    int64
	AvgPrice      float64
	UpdateTime    int64
}

// Final reports whether the venue will not change the order any more.
func (s OrderState) Final() bool {
	switch s.Status {
	case VenueFilled, VenueCanceled, VenueRejected, VenueExpired, VenueExpiredInMatch:
		return true
	}
	return false
}

// Fill is one execution (Binance userTrades).
type Fill struct {
	TradeID         int64
	VenueOrderID    string
	Symbol          string
	Side            HedgeSide
	Price           float64
	QtyE8           int64
	QuoteE8         int64
	CommissionE8    int64
	CommissionAsset string
	RealizedPnlE8   int64
	Maker           bool
	Time            int64
}

// Position is the venue's position (one-way mode: + long, - short).
type Position struct {
	Symbol       string
	QtyE8        int64
	EntryPrice   float64
	MarkPrice    float64
	UnrealizedE8 int64
}

// Income is a funding payment (+ received, - paid).
type Income struct {
	TranID   int64
	Symbol   string
	Type     string
	Asset    string
	AmountE8 int64
	Time     int64
}

// Venue is the hedge execution contract.
type Venue interface {
	// Name is the provider ("paper" | "binance"); ID the exact venue
	// ("paper" | "binance-testnet" | "binance-live").
	Name() string
	ID() string
	// Live is true when orders move real money.
	Live() bool
	// CanTrade is nil when orders may be sent now (credentials present and,
	// for the live exchange, HEDGE_LIVE_EXECUTION_ENABLED).
	CanTrade() error
	Ping(ctx context.Context) error
	Rules(ctx context.Context, symbol string) (models.HedgeRules, error)
	Quote(ctx context.Context, symbol string) (Quote, error)
	Mark(ctx context.Context, symbol string) (Mark, error)
	Commission(ctx context.Context, symbol string) (Commission, error)
	PlaceOrder(ctx context.Context, req OrderRequest) (OrderState, error)
	// QueryOrder looks the order up by our client order id; ErrOrderNotFound
	// when the venue has no such order.
	QueryOrder(ctx context.Context, symbol, clientOrderID string) (OrderState, error)
	// CancelOrder: ErrOrderNotFound when the order is unknown or already
	// final at the venue (it may have filled - query it).
	CancelOrder(ctx context.Context, symbol, clientOrderID string) (OrderState, error)
	Fills(ctx context.Context, symbol, venueOrderID string) ([]Fill, error)
	// Position: ErrUnsupported on the paper venue.
	Position(ctx context.Context, symbol string) (Position, error)
	Funding(ctx context.Context, symbol string, fromMs int64) ([]Income, error)
}

var (
	// ErrOrderNotFound: the venue says the order does not exist.
	ErrOrderNotFound = errors.New("order not found at the venue")
	// ErrUnsupported: the venue has no such operation.
	ErrUnsupported = errors.New("not supported by this venue")
)

// VenueError is an error the venue returned or a transport failure.
// Unknown means the request may or may not have been executed (timeout,
// 5xx, Binance -1007): never resend before querying.
type VenueError struct {
	HTTPStatus int
	Code       int
	Msg        string
	Unknown    bool
}

func (e *VenueError) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("venue error %d (HTTP %d): %s", e.Code, e.HTTPStatus, e.Msg)
	}
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("venue HTTP %d: %s", e.HTTPStatus, e.Msg)
	}
	return "venue unreachable: " + e.Msg
}

// IsUnknownOutcome reports an error after which the request's effect is
// unknown.
func IsUnknownOutcome(err error) bool {
	var ve *VenueError
	return errors.As(err, &ve) && ve.Unknown
}

// IsRejected reports a definitive refusal: the venue did not act.
func IsRejected(err error) bool {
	var ve *VenueError
	return errors.As(err, &ve) && !ve.Unknown && ve.HTTPStatus >= 400 && ve.HTTPStatus < 500
}

var (
	mu       sync.Mutex
	instance Venue
)

// CreateVenue builds the venue named by HEDGE_PROVIDER ("" = from config).
func CreateVenue(name string) Venue {
	if name == "" && config.E != nil {
		name = config.E.HedgeProvider
	}
	if name == "binance" && config.E != nil {
		return NewBinance(BinanceConfigFromEnv(config.E))
	}
	return NewPaper(PaperOptions{})
}

// GetVenue returns the process-wide venue, creating it on first use.
func GetVenue() Venue {
	mu.Lock()
	defer mu.Unlock()
	if instance == nil {
		instance = CreateVenue("")
		if err := instance.CanTrade(); err != nil {
			logx.Warn("hedge venue cannot take orders - hedging is unavailable until it can", "venue", instance.ID(), "reason", err.Error())
		}
		if !instance.Live() && config.IsProduction() {
			logx.Warn("hedging is not on the live exchange - no real protection is being bought", "venue", instance.ID())
		}
	}
	return instance
}

// SetVenue is a test hook; nil resets to lazy creation.
func SetVenue(v Venue) {
	mu.Lock()
	instance = v
	mu.Unlock()
}
