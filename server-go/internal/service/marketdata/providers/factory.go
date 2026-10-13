package providers

import (
	"errors"
	"sync"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
)

var (
	instanceMu sync.Mutex
	instance   Provider
)

// CreateMarketDataProvider builds a provider by name ("" = MARKET_DATA_PROVIDER).
// Unknown names fall back to the simulated feed, as in TS.
func CreateMarketDataProvider(name string) (Provider, error) {
	if name == "" && config.E != nil {
		name = config.E.MarketDataProvider
	}
	switch name {
	case "binance":
		return NewBinance(), nil
	case "generic-ws":
		var url, key string
		if config.E != nil {
			url, key = config.E.MarketDataWSURL, config.E.MarketDataAPIKey
		}
		return NewGenericWebsocket(url, key)
	default:
		return NewSimulated(0), nil
	}
}

// GetMarketDataProvider returns the process-wide provider. It refuses a
// non-authoritative feed in production.
func GetMarketDataProvider() (Provider, error) {
	instanceMu.Lock()
	defer instanceMu.Unlock()
	if instance != nil {
		return instance, nil
	}
	p, err := CreateMarketDataProvider("")
	if err != nil {
		return nil, err
	}
	allowSimulated := config.E != nil && config.E.AllowSimulatedFeedInProduction
	if !p.Authoritative() && config.IsProduction() && !allowSimulated {
		return nil, errors.New(`Provider "` + p.Name() + `" is not an authoritative price source and cannot be used in production`)
	}
	if !p.Authoritative() {
		logx.Warn("NON-AUTHORITATIVE market data provider in use - prices are generated, not observed", "provider", p.Name())
	}
	instance = p
	return p, nil
}

// ResetMarketDataProvider drops the process-wide provider (tests).
func ResetMarketDataProvider() {
	instanceMu.Lock()
	instance = nil
	instanceMu.Unlock()
}
