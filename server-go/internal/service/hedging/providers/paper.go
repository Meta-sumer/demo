package providers

import (
	"context"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"binarytrading/internal/platform/util"
)

// Paper / sandbox execution (spec section 16, rule 14).
//
// Orders are booked into an in-process ledger. Nothing leaves the server, so
// the whole hedging pipeline - decision, order, fill, position, P&L - can be
// exercised end to end before any venue is connected.
//
// SlippageBps, PartialFillRatio and FailureRate exist so failure modes can be
// reproduced in tests rather than only discovered in production.

type PaperOptions struct {
	// Default 2 when nil.
	SlippageBps *float64
	// 1 = always fill fully; 0.5 = fill half of each order. Default 1 when nil.
	PartialFillRatio *float64
	// Probability [0..1] that an order is rejected outright.
	FailureRate float64
	LatencyMs   int64
}

type paperOrder struct {
	OrderResult
	symbol            string
	side              HedgeSide
	requestedNotional int64
	createdAt         int64
}

type paperPosition struct {
	netNotional  float64
	averagePrice *float64
}

type PaperProvider struct {
	options PaperOptions

	mu               sync.Mutex
	orders           map[string]*paperOrder
	byIdempotencyKey map[string]string
	positions        map[string]paperPosition
}

func NewPaperProvider(opts PaperOptions) *PaperProvider {
	p := &PaperProvider{options: opts}
	p.Reset()
	return p
}

func (p *PaperProvider) Name() string { return "paper" }
func (p *PaperProvider) Live() bool   { return false }

func (p *PaperProvider) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error) {
	p.mu.Lock()
	if existingID, ok := p.byIdempotencyKey[req.IdempotencyKey]; ok {
		res := toResult(p.orders[existingID])
		p.mu.Unlock()
		return res, nil
	}
	p.mu.Unlock()

	if p.options.LatencyMs > 0 {
		select {
		case <-time.After(time.Duration(p.options.LatencyMs) * time.Millisecond):
		case <-ctx.Done():
			return OrderResult{}, ctx.Err()
		}
	}

	providerOrderID := "paper_" + util.UUID()

	if p.options.FailureRate > 0 && rand.Float64() < p.options.FailureRate {
		failed := &paperOrder{
			OrderResult: OrderResult{
				ProviderOrderID: providerOrderID,
				Status:          StateFailed,
				Error:           "Simulated venue rejection",
			},
			symbol: req.Symbol, side: req.Side, requestedNotional: req.Notional, createdAt: util.NowMs(),
		}
		p.mu.Lock()
		p.orders[providerOrderID] = failed
		p.byIdempotencyKey[req.IdempotencyKey] = providerOrderID
		p.mu.Unlock()
		return toResult(failed), nil
	}

	fillRatio := 1.0
	if p.options.PartialFillRatio != nil {
		fillRatio = math.Min(1, math.Max(0, *p.options.PartialFillRatio))
	}
	filled := util.RoundHalfUp(float64(req.Notional) * fillRatio)
	slippageBps := 2.0
	if p.options.SlippageBps != nil {
		slippageBps = *p.options.SlippageBps
	}
	slippage := slippageBps / 10_000
	fillPrice := req.ReferencePrice * (1 - slippage)
	if req.Side == SideBuy {
		fillPrice = req.ReferencePrice * (1 + slippage)
	}

	status := StateFailed
	if filled >= float64(req.Notional) {
		status = StateFilled
	} else if filled > 0 {
		status = StatePartiallyFilled
	}
	var avg *float64
	if filled > 0 {
		// Number(fillPrice.toFixed(8))
		v, _ := strconv.ParseFloat(strconv.FormatFloat(fillPrice, 'f', 8, 64), 64)
		avg = &v
	}
	order := &paperOrder{
		OrderResult: OrderResult{
			ProviderOrderID:  providerOrderID,
			Status:           status,
			FilledNotional:   filled,
			AverageFillPrice: avg,
		},
		symbol: req.Symbol, side: req.Side, requestedNotional: req.Notional, createdAt: util.NowMs(),
	}

	p.mu.Lock()
	p.orders[providerOrderID] = order
	p.byIdempotencyKey[req.IdempotencyKey] = providerOrderID
	p.applyToPosition(order)
	p.mu.Unlock()
	return toResult(order), nil
}

func (p *PaperProvider) CancelOrder(_ context.Context, providerOrderID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	order, ok := p.orders[providerOrderID]
	if !ok || order.Status == StateFilled {
		return nil
	}
	order.Status = StateCancelled
	return nil
}

func (p *PaperProvider) GetOrder(_ context.Context, providerOrderID string) (OrderResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	order, ok := p.orders[providerOrderID]
	if !ok {
		return OrderResult{ProviderOrderID: providerOrderID, Status: StateFailed, Error: "Unknown order"}, nil
	}
	return toResult(order), nil
}

func (p *PaperProvider) GetPosition(_ context.Context, symbol string) (Position, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pos := p.positions[symbol]
	return Position{Symbol: symbol, NetNotional: pos.netNotional, AveragePrice: pos.averagePrice}, nil
}

func (p *PaperProvider) Healthy(context.Context) bool { return true }

// applyToPosition must be called with p.mu held.
func (p *PaperProvider) applyToPosition(order *paperOrder) {
	if order.FilledNotional <= 0 || order.AverageFillPrice == nil {
		return
	}
	signed := order.FilledNotional
	if order.side != SideBuy {
		signed = -signed
	}
	current := p.positions[order.symbol]
	next := current.netNotional + signed

	// Volume-weighted average while the position keeps its direction; a flip
	// resets the basis to the price that flipped it.
	flipped := sign(next) != sign(current.netNotional)
	var avg float64
	if current.averagePrice == nil || flipped {
		avg = *order.AverageFillPrice
	} else {
		avg = (*current.averagePrice*math.Abs(current.netNotional) + *order.AverageFillPrice*math.Abs(signed)) /
			math.Max(1, math.Abs(current.netNotional)+math.Abs(signed))
	}
	p.positions[order.symbol] = paperPosition{netNotional: next, averagePrice: &avg}
}

// Reset is a test hook: drop all simulated state.
func (p *PaperProvider) Reset() {
	p.mu.Lock()
	p.orders = map[string]*paperOrder{}
	p.byIdempotencyKey = map[string]string{}
	p.positions = map[string]paperPosition{}
	p.mu.Unlock()
}

func toResult(o *paperOrder) OrderResult {
	r := o.OrderResult
	r.Raw = nil
	return r
}

func sign(v float64) float64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}
