package beex

import (
	"context"
	"errors"
	"sync"

	"binarytrading/internal/config"
)

// Provider is everything the USDT service asks of BeeX.
type Provider interface {
	// Name is "beex" or "mock".
	Name() string
	// Mode is "live", "test" (from the API key prefix) or "" when unconfigured.
	Mode() string
	// Configured reports whether calls can be made at all.
	Configured() bool

	CreateInvoice(ctx context.Context, req CreateInvoiceRequest) (*Invoice, error)
	// GetInvoice accepts our orderId, BeeX's reference or BeeX's id.
	GetInvoice(ctx context.Context, id string) (*Invoice, error)
	CreatePayout(ctx context.Context, req CreatePayoutRequest) (*Payout, error)
	// GetPayout accepts our reference or BeeX's.
	GetPayout(ctx context.Context, reference string) (*Payout, error)
	GetPayoutLimits(ctx context.Context) (*PayoutLimits, error)
	GetBalance(ctx context.Context) (Balance, error)
}

var (
	mu       sync.Mutex
	instance Provider
)

// New builds the provider BEEX_PROVIDER names. The mock is refused in
// production (config.Load refuses it too; this is the second line).
func New(cfg *config.Env) (Provider, error) {
	if cfg == nil {
		return nil, errors.New("beex: configuration not loaded")
	}
	if cfg.BeexProvider == "mock" {
		if cfg.NodeEnv == "production" {
			return nil, errors.New("beex: the mock provider cannot be used in production")
		}
		return NewMock(), nil
	}
	return NewClient(cfg.BeexAPIBaseURL, cfg.BeexAPIKey), nil
}

// Get returns the process-wide provider, building it on first use.
func Get() (Provider, error) {
	mu.Lock()
	defer mu.Unlock()
	if instance != nil {
		return instance, nil
	}
	p, err := New(config.E)
	if err != nil {
		return nil, err
	}
	instance = p
	return p, nil
}

// Set installs a provider (tests, or main after validating it).
func Set(p Provider) {
	mu.Lock()
	instance = p
	mu.Unlock()
}
