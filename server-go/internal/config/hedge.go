package config

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Hedge venue configuration (docs/audit/hedging/hedging-spec.md, section 9).
//
// Hedging runs on Binance USDⓈ-M perpetual futures only. The paper venue
// (HEDGE_PROVIDER=paper, the default) books simulated fills in process;
// HEDGE_PROVIDER=binance talks to the testnet or the live exchange. Orders
// reach the LIVE exchange only with HEDGE_LIVE_EXECUTION_ENABLED=true and
// both credentials set; the testnet needs only the credentials.

// Binance USDⓈ-M futures REST base URLs.
const (
	BinanceFuturesTestnetURL = "https://testnet.binancefuture.com"
	BinanceFuturesLiveURL    = "https://fapi.binance.com"
)

// DefaultHedgeSymbols: the launch assets and their Binance perpetuals. EUR/USD,
// GBP/USD and XAU/USD have no entry, so they are never hedged.
const DefaultHedgeSymbols = "BTC/USD=BTCUSDT,ETH/USD=ETHUSDT"

var venueSymbolRe = regexp.MustCompile(`^[A-Z0-9]{2,16}USDT$`)

// HedgeBaseURL is the REST base URL the binance venue uses.
func (e *Env) HedgeBaseURL() string {
	if e.HedgeAPIURL != "" {
		return strings.TrimRight(e.HedgeAPIURL, "/")
	}
	if e.HedgeBinanceEnv == "live" {
		return BinanceFuturesLiveURL
	}
	return BinanceFuturesTestnetURL
}

// HedgeVenueSymbol maps a platform asset symbol to its venue symbol; "" when
// the asset is not hedgeable.
func (e *Env) HedgeVenueSymbol(assetSymbol string) string {
	if e == nil {
		return ""
	}
	return e.HedgeSymbols[strings.ToUpper(strings.TrimSpace(assetSymbol))]
}

func (l *loader) float(key string, def, min, max float64) float64 {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return def
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		l.fail(key, "Expected number, received '"+raw+"'")
		return def
	}
	if f < min || f > max {
		l.fail(key, "Number must be between "+strconv.FormatFloat(min, 'f', -1, 64)+" and "+strconv.FormatFloat(max, 'f', -1, 64))
		return def
	}
	return f
}

// ParseHedgeSymbols parses "BTC/USD=BTCUSDT,ETH/USD=ETHUSDT".
func ParseHedgeSymbols(raw string) (map[string]string, bool) {
	out := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, v, ok := strings.Cut(part, "=")
		k, v = strings.ToUpper(strings.TrimSpace(k)), strings.ToUpper(strings.TrimSpace(v))
		if !ok || k == "" || !venueSymbolRe.MatchString(v) {
			return nil, false
		}
		out[k] = v
	}
	return out, true
}

func loadHedge(l *loader, e *Env) {
	e.HedgeBinanceEnv = l.enum("HEDGE_BINANCE_ENV", "testnet", "testnet", "live")
	symbols, ok := ParseHedgeSymbols(l.str("HEDGE_SYMBOLS", DefaultHedgeSymbols))
	if !ok {
		l.fail("HEDGE_SYMBOLS", "Expected ASSET=VENUESYMBOL pairs separated by commas, venue symbols USDⓈ-M (e.g. BTC/USD=BTCUSDT)")
		symbols, _ = ParseHedgeSymbols(DefaultHedgeSymbols)
	}
	e.HedgeSymbols = symbols
	e.HedgeINRPerUSDT = l.float("HEDGE_INR_PER_USDT", 0, 0, 1000)
	if raw := strings.TrimSpace(l.str("HEDGE_INR_PER_USDT_AS_OF", "")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			l.fail("HEDGE_INR_PER_USDT_AS_OF", "Expected an RFC 3339 time such as 2026-10-21T09:00:00Z")
		} else {
			e.HedgeINRPerUSDTAsOf = t.UnixMilli()
		}
	}
	if e.HedgeINRPerUSDT > 0 && e.HedgeINRPerUSDTAsOf == 0 {
		l.fail("HEDGE_INR_PER_USDT_AS_OF", "Required when HEDGE_INR_PER_USDT is set: a rate without its time cannot be checked for staleness")
	}
	e.HedgeFXMaxAgeMs = l.integer("HEDGE_FX_MAX_AGE_MS", 86_400_000, 60_000)
	e.HedgeTakerFeeBps = l.float("HEDGE_TAKER_FEE_BPS", 5, 0, 100)
	e.HedgeSlippageBps = l.float("HEDGE_SLIPPAGE_BPS", 2, 0, 500)
	e.HedgeFallbackSpreadBps = l.float("HEDGE_FALLBACK_SPREAD_BPS", 1, 0, 500)
	e.HedgeMaxCostBps = l.float("HEDGE_MAX_COST_BPS", 30, 0, 10_000)
	e.HedgeMaxLiabilityMultiple = l.float("HEDGE_MAX_LIABILITY_MULTIPLE", 100, 0, 1e6)
	e.HedgeMaxOrderUSDT = l.float("HEDGE_MAX_ORDER_USDT", 50_000, 100, 1e9)
	e.HedgeReconcileIntervalMs = l.integer("HEDGE_RECONCILE_INTERVAL_MS", 60_000, 5_000)
	e.HedgeRecvWindowMs = l.integer("HEDGE_RECV_WINDOW_MS", 5_000, 1_000)
	e.HedgeRequestTimeoutMs = l.integer("HEDGE_REQUEST_TIMEOUT_MS", 10_000, 500)
	e.HedgeVenueMaxAgeMs = l.integer("HEDGE_VENUE_MAX_AGE_MS", 15_000, 1_000)
	if e.HedgeRecvWindowMs > 60_000 {
		l.fail("HEDGE_RECV_WINDOW_MS", "Binance accepts at most 60000")
	}

	switch e.HedgeProvider {
	case "broker", "lp":
		l.fail("HEDGE_PROVIDER", "The generic broker and lp adapters were removed: hedging runs on Binance (binance) or the in-process paper venue (paper)")
		return
	case "binance":
	default:
		return
	}
	if e.HedgeAPIURL != "" {
		u := strings.ToLower(e.HedgeAPIURL)
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			l.fail("HEDGE_API_URL", "Invalid url")
		}
		if e.HedgeBinanceEnv == "live" {
			if !strings.HasPrefix(u, "https://") {
				l.fail("HEDGE_API_URL", "The live Binance venue must use https")
			}
			if strings.Contains(u, "testnet") {
				l.fail("HEDGE_API_URL", "HEDGE_BINANCE_ENV=live with a testnet URL: set both to the same environment")
			}
		} else if strings.Contains(u, "fapi.binance.com") {
			l.fail("HEDGE_API_URL", "HEDGE_BINANCE_ENV=testnet with the live URL: set both to the same environment")
		}
	}
	// Live mode refuses to start without credentials rather than boot a hedger
	// whose every order fails. The testnet without keys boots with hedging
	// unavailable (pricing then widens or refuses, see the spec).
	if e.HedgeBinanceEnv == "live" && e.HedgeLiveExecutionEnabled && (e.HedgeAPIKey == "" || e.HedgeAPISecret == "") {
		l.fail("HEDGE_API_KEY", "HEDGE_API_KEY and HEDGE_API_SECRET are required for live Binance execution (HEDGE_BINANCE_ENV=live with HEDGE_LIVE_EXECUTION_ENABLED=true)")
	}
	if e.HedgeAPISecret != "" && len(e.HedgeAPISecret) < 16 {
		l.fail("HEDGE_API_SECRET", "Too short to be a Binance API secret")
	}
}
