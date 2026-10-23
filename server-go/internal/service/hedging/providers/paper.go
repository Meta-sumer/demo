package providers

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
)

// Paper venue (spec rule 14): orders fill in process against the platform's
// own price, so the whole pipeline - target, order, fill, position, P&L - runs
// end to end without a venue. It speaks the same quantities and statuses as
// Binance. Nothing leaves the server; state lives in memory, so after a
// restart it knows no orders (recovery then marks a working order FAILED) and
// it reports no venue position (reconciliation: NOT_APPLICABLE).

// PriceFunc returns the bid and ask of a venue symbol.
type PriceFunc func(ctx context.Context, symbol string) (bid, ask float64, ok bool)

type PaperOptions struct {
	// Default 2 when nil.
	SlippageBps *float64
	// Taker fee, default 5 bps when nil.
	FeeBps *float64
	// 1 = always fill fully; 0.5 = fill half of each order then expire.
	PartialFillRatio *float64
	// Probability [0..1] that an order is rejected outright.
	FailureRate float64
	Price       PriceFunc
}

type paperOrder struct {
	state OrderState
	fills []Fill
}

type PaperVenue struct {
	opts PaperOptions

	mu        sync.Mutex
	orders    map[string]*paperOrder
	nextOrder int64
	nextTrade int64
	positions map[string]int64
}

func NewPaper(opts PaperOptions) *PaperVenue {
	return &PaperVenue{opts: opts, orders: map[string]*paperOrder{}, positions: map[string]int64{}, nextOrder: 1, nextTrade: 1}
}

// SetPrice installs the price source (the hedging package wires the
// platform's last tick).
func (p *PaperVenue) SetPrice(f PriceFunc) {
	p.mu.Lock()
	p.opts.Price = f
	p.mu.Unlock()
}

func (p *PaperVenue) Name() string    { return "paper" }
func (p *PaperVenue) ID() string      { return "paper" }
func (p *PaperVenue) Live() bool      { return false }
func (p *PaperVenue) CanTrade() error { return nil }

func (p *PaperVenue) Ping(context.Context) error { return nil }

// paperRules mirror the Binance USDⓈ-M filters of the launch symbols.
func (p *PaperVenue) Rules(_ context.Context, symbol string) (models.HedgeRules, error) {
	switch symbol {
	case "BTCUSDT":
		return models.HedgeRules{StepE8: 100_000, MinQtyE8: 100_000, MaxQtyE8: 120 * models.QtyScale, MinNotional: 100, TickSize: 0.1}, nil
	case "ETHUSDT":
		return models.HedgeRules{StepE8: 100_000, MinQtyE8: 100_000, MaxQtyE8: 2000 * models.QtyScale, MinNotional: 20, TickSize: 0.01}, nil
	}
	return models.HedgeRules{StepE8: 100_000, MinQtyE8: 100_000, MaxQtyE8: 1000 * models.QtyScale, MinNotional: 5, TickSize: 0.01}, nil
}

func (p *PaperVenue) Quote(ctx context.Context, symbol string) (Quote, error) {
	p.mu.Lock()
	price := p.opts.Price
	p.mu.Unlock()
	if price == nil {
		return Quote{}, &VenueError{Msg: "paper venue has no price source"}
	}
	bid, ask, ok := price(ctx, symbol)
	if !ok || bid <= 0 || ask <= 0 {
		return Quote{}, &VenueError{Msg: "no price for " + symbol}
	}
	return Quote{Bid: bid, Ask: ask, Time: util.NowMs()}, nil
}

func (p *PaperVenue) Mark(ctx context.Context, symbol string) (Mark, error) {
	q, err := p.Quote(ctx, symbol)
	if err != nil {
		return Mark{}, err
	}
	return Mark{MarkPrice: q.Mid(), IndexPrice: q.Mid(), Time: q.Time}, nil
}

func (p *PaperVenue) Commission(context.Context, string) (Commission, error) {
	return Commission{MakerRate: p.feeBps() / 10_000, TakerRate: p.feeBps() / 10_000}, nil
}

func (p *PaperVenue) feeBps() float64 {
	if p.opts.FeeBps != nil {
		return *p.opts.FeeBps
	}
	return 5
}

func (p *PaperVenue) PlaceOrder(ctx context.Context, req OrderRequest) (OrderState, error) {
	p.mu.Lock()
	if o, ok := p.orders[req.ClientOrderID]; ok && !o.state.Final() {
		p.mu.Unlock()
		return OrderState{}, &VenueError{HTTPStatus: 400, Code: -4116, Msg: "ClientOrderId is duplicated."}
	}
	p.mu.Unlock()
	q, err := p.Quote(ctx, req.Symbol)
	if err != nil {
		return OrderState{}, err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	id := p.nextOrder
	p.nextOrder++
	st := OrderState{Symbol: req.Symbol, ClientOrderID: req.ClientOrderID, VenueOrderID: "paper-" + strconv.FormatInt(id, 10),
		Side: req.Side, OrigQtyE8: req.QtyE8, UpdateTime: util.NowMs()}
	if p.opts.FailureRate > 0 && rand.Float64() < p.opts.FailureRate {
		st.Status = VenueRejected
		p.orders[req.ClientOrderID] = &paperOrder{state: st}
		return OrderState{}, &VenueError{HTTPStatus: 400, Code: -2010, Msg: "Simulated venue rejection"}
	}
	ratio := 1.0
	if p.opts.PartialFillRatio != nil {
		ratio = math.Min(1, math.Max(0, *p.opts.PartialFillRatio))
	}
	slip := 2.0
	if p.opts.SlippageBps != nil {
		slip = *p.opts.SlippageBps
	}
	price := q.Bid * (1 - slip/10_000)
	if req.Side == SideBuy {
		price = q.Ask * (1 + slip/10_000)
	}
	filled := int64(math.Floor(float64(req.QtyE8) * ratio))
	st.Status = VenueFilled
	if filled < req.QtyE8 {
		st.Status = VenueExpired
	}
	o := &paperOrder{state: st}
	if filled > 0 {
		quote := FloatToE8(price * E8ToFloat(filled))
		fee := FloatToE8(E8ToFloat(quote) * p.feeBps() / 10_000)
		tid := p.nextTrade
		p.nextTrade++
		o.fills = append(o.fills, Fill{TradeID: tid, VenueOrderID: st.VenueOrderID, Symbol: req.Symbol, Side: req.Side,
			Price: price, QtyE8: filled, QuoteE8: quote, CommissionE8: fee, CommissionAsset: "USDT", Time: time.Now().UnixMilli()})
		o.state.ExecutedQtyE8, o.state.CumQuoteE8, o.state.AvgPrice = filled, quote, price
		signed := filled
		if req.Side == SideSell {
			signed = -filled
		}
		p.positions[req.Symbol] += signed
	}
	p.orders[req.ClientOrderID] = o
	return o.state, nil
}

func (p *PaperVenue) QueryOrder(_ context.Context, _ string, clientOrderID string) (OrderState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o, ok := p.orders[clientOrderID]
	if !ok {
		return OrderState{}, ErrOrderNotFound
	}
	return o.state, nil
}

func (p *PaperVenue) CancelOrder(_ context.Context, _ string, clientOrderID string) (OrderState, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	o, ok := p.orders[clientOrderID]
	if !ok || o.state.Final() {
		return OrderState{}, ErrOrderNotFound
	}
	o.state.Status = VenueCanceled
	return o.state, nil
}

func (p *PaperVenue) Fills(_ context.Context, _ string, venueOrderID string) ([]Fill, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, o := range p.orders {
		if o.state.VenueOrderID == venueOrderID {
			return append([]Fill(nil), o.fills...), nil
		}
	}
	return []Fill{}, nil
}

func (p *PaperVenue) Position(context.Context, string) (Position, error) {
	return Position{}, ErrUnsupported
}

func (p *PaperVenue) Funding(context.Context, string, int64) ([]Income, error) {
	return []Income{}, nil
}
