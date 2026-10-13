// Package providers is the market data provider contract (spec section 5).
//
// The engine never talks to a vendor SDK directly: it consumes this interface,
// so a real provider can be swapped in without touching pricing, expiry or
// settlement. Providers emit raw ticks; normalisation, latency measurement and
// health classification all happen in the marketdata package.
package providers

import (
	"context"
	"sync"
	"time"
)

// Tick is a raw provider tick.
type Tick struct {
	// Provider's own symbol, matched back to an asset by providerSymbol.
	Symbol string
	Price  float64
	Bid    *float64
	Ask    *float64
	// Event time reported by the provider, epoch ms.
	ProviderTimestamp *int64
	// Monotonic sequence from the provider, when it publishes one.
	Sequence *int64
}

type ConnectionState string

const (
	StateConnecting   ConnectionState = "CONNECTING"
	StateConnected    ConnectionState = "CONNECTED"
	StateReconnecting ConnectionState = "RECONNECTING"
	StateDisconnected ConnectionState = "DISCONNECTED"
	StateFailed       ConnectionState = "FAILED"
)

// Status is a connection state change. JSON matches the TS ProviderStatus.
type Status struct {
	Provider string          `json:"provider"`
	State    ConnectionState `json:"state"`
	Message  string          `json:"message,omitempty"`
	At       int64           `json:"at"`
}

type TickHandler func(Tick)
type StatusHandler func(Status)

// Provider is a market data feed.
type Provider interface {
	Name() string
	// Authoritative is false for feeds that must never price a real trade.
	Authoritative() bool

	Connect(ctx context.Context) error
	Disconnect(ctx context.Context) error
	Subscribe(ctx context.Context, symbols []string) error
	Unsubscribe(ctx context.Context, symbols []string) error
	IsConnected() bool

	OnTick(h TickHandler)
	OnStatus(h StatusHandler)
}

// Base is the shared part: handler registries, status fan-out and the
// ordered symbol set.
type Base struct {
	name string

	hmu            sync.RWMutex
	tickHandlers   []TickHandler
	statusHandlers []StatusHandler

	smu     sync.Mutex
	symbols []string // insertion order, like a JS Set
}

func (b *Base) Name() string { return b.name }

func (b *Base) OnTick(h TickHandler) {
	b.hmu.Lock()
	b.tickHandlers = append(b.tickHandlers, h)
	b.hmu.Unlock()
}

func (b *Base) OnStatus(h StatusHandler) {
	b.hmu.Lock()
	b.statusHandlers = append(b.statusHandlers, h)
	b.hmu.Unlock()
}

func (b *Base) emitTick(t Tick) {
	b.hmu.RLock()
	hs := b.tickHandlers
	b.hmu.RUnlock()
	for _, h := range hs {
		h(t)
	}
}

func (b *Base) emitStatus(state ConnectionState, message string) {
	s := Status{Provider: b.name, State: state, At: time.Now().UnixMilli(), Message: message}
	b.hmu.RLock()
	hs := b.statusHandlers
	b.hmu.RUnlock()
	for _, h := range hs {
		h(s)
	}
}

func (b *Base) hasSymbol(s string) bool {
	for _, x := range b.symbols {
		if x == s {
			return true
		}
	}
	return false
}

// addSymbol returns true when s was not yet present. Caller holds smu.
func (b *Base) addSymbol(s string) bool {
	if b.hasSymbol(s) {
		return false
	}
	b.symbols = append(b.symbols, s)
	return true
}

// removeSymbol returns true when s was present. Caller holds smu.
func (b *Base) removeSymbol(s string) bool {
	for i, x := range b.symbols {
		if x == s {
			b.symbols = append(b.symbols[:i], b.symbols[i+1:]...)
			return true
		}
	}
	return false
}

func (b *Base) symbolList() []string {
	b.smu.Lock()
	defer b.smu.Unlock()
	return append([]string(nil), b.symbols...)
}

func ptr[T any](v T) *T { return &v }
