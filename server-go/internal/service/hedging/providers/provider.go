// Package providers is the external hedge execution contract (spec section 16).
//
// The hedging engine is coupled to this interface, never to a broker SDK, so a
// venue can be added or replaced without touching risk logic. Live execution is
// gated behind explicit configuration: a provider that would send real orders
// refuses to do so unless HEDGE_LIVE_EXECUTION_ENABLED is set.
package providers

import (
	"context"
	"strings"
	"sync"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
)

type HedgeSide string

const (
	SideBuy  HedgeSide = "BUY"
	SideSell HedgeSide = "SELL"
)

type OrderRequest struct {
	// Platform-side reference, echoed back for reconciliation.
	Reference string
	Symbol    string
	Side      HedgeSide
	// Order size in minor units of the account currency.
	Notional int64
	// Market price the decision was taken at, for slippage measurement.
	ReferencePrice float64
	IdempotencyKey string
}

type OrderState string

const (
	StatePending         OrderState = "PENDING"
	StatePartiallyFilled OrderState = "PARTIALLY_FILLED"
	StateFilled          OrderState = "FILLED"
	StateFailed          OrderState = "FAILED"
	StateCancelled       OrderState = "CANCELLED"
)

// OrderResult is what a venue reports. FilledNotional is float64 because a
// venue may report fractional minor units; the engine rounds before storing.
type OrderResult struct {
	ProviderOrderID  string
	Status           OrderState
	FilledNotional   float64
	AverageFillPrice *float64
	// Error is "" when the venue reported none.
	Error string
	Raw   any
}

type Position struct {
	Symbol string
	// Signed exposure held at the venue: positive is long, negative is short.
	NetNotional   float64
	AveragePrice  *float64
	UnrealisedPnl float64
}

type Provider interface {
	Name() string
	// Live is true when orders leave this process and reach a real venue.
	Live() bool
	PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error)
	CancelOrder(ctx context.Context, providerOrderID string) error
	GetOrder(ctx context.Context, providerOrderID string) (OrderResult, error)
	GetPosition(ctx context.Context, symbol string) (Position, error)
	Healthy(ctx context.Context) bool
}

var (
	mu       sync.Mutex
	instance Provider
)

// CreateHedgeProvider builds a provider by name ("" = HEDGE_PROVIDER).
func CreateHedgeProvider(name string) Provider {
	if name == "" && config.E != nil {
		name = config.E.HedgeProvider
	}
	switch name {
	case "broker":
		return NewBrokerProvider()
	case "lp":
		return NewLPProvider()
	default:
		return NewPaperProvider(PaperOptions{})
	}
}

// GetHedgeProvider returns the process-wide provider, creating it on first use.
func GetHedgeProvider() Provider {
	mu.Lock()
	defer mu.Unlock()
	if instance == nil {
		instance = CreateHedgeProvider("")
		liveEnabled := config.E != nil && config.E.HedgeLiveExecutionEnabled
		if instance.Live() && !liveEnabled {
			logx.Warn("live hedge provider selected but execution is disabled - orders will be refused until HEDGE_LIVE_EXECUTION_ENABLED=true",
				"provider", instance.Name())
		}
		if !instance.Live() && config.IsProduction() {
			logx.Warn("hedging is running on the paper provider - no external orders are being placed")
		}
	}
	return instance
}

// SetHedgeProvider is a test hook; nil resets to lazy creation.
func SetHedgeProvider(p Provider) {
	mu.Lock()
	instance = p
	mu.Unlock()
}

func normalise(status string, filled ...string) OrderState {
	upper := strings.ToUpper(status)
	for _, f := range filled {
		if upper == f {
			return StateFilled
		}
	}
	switch upper {
	case "PARTIAL", "PARTIALLY_FILLED":
		return StatePartiallyFilled
	case "CANCELLED", "CANCELED":
		return StateCancelled
	case "REJECTED", "FAILED":
		return StateFailed
	}
	return StatePending
}
