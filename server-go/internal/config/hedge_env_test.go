package config

import (
	"strings"
	"testing"
)

// loadHedge is the boot-time guard of hedging: live Binance never boots
// without credentials, testnet and live URLs cannot be mixed, a configured
// INR rate needs its observation time, and the removed adapters are refused.

func hedgeIssues(t *testing.T, e Env, vars map[string]string) (Env, string) {
	t.Helper()
	for _, k := range []string{"HEDGE_BINANCE_ENV", "HEDGE_SYMBOLS", "HEDGE_INR_PER_USDT", "HEDGE_INR_PER_USDT_AS_OF"} {
		t.Setenv(k, "")
	}
	for k, v := range vars {
		t.Setenv(k, v)
	}
	l := &loader{}
	loadHedge(l, &e)
	return e, strings.Join(l.issues, "\n")
}

const hedgeSecret = "binance-secret-0123456789abcdef"

func TestHedgeDefaults(t *testing.T) {
	e, got := hedgeIssues(t, Env{HedgeProvider: "paper"}, nil)
	if got != "" || e.HedgeBinanceEnv != "testnet" || e.HedgeBaseURL() != BinanceFuturesTestnetURL {
		t.Fatalf("defaults: %q %+v", got, e.HedgeBinanceEnv)
	}
	if e.HedgeVenueSymbol("btc/usd") != "BTCUSDT" || e.HedgeVenueSymbol("ETH/USD") != "ETHUSDT" {
		t.Fatalf("symbols %v", e.HedgeSymbols)
	}
	// The assets off at launch are never hedgeable.
	for _, s := range []string{"EUR/USD", "GBP/USD", "XAU/USD"} {
		if e.HedgeVenueSymbol(s) != "" {
			t.Fatalf("%s is hedgeable", s)
		}
	}
	if e.HedgeTakerFeeBps != 5 || e.HedgeMaxCostBps != 30 || e.HedgeMaxLiabilityMultiple != 100 || e.HedgeFXMaxAgeMs != 86_400_000 {
		t.Fatalf("cost defaults %+v", e)
	}
	live, _ := hedgeIssues(t, Env{HedgeProvider: "paper"}, map[string]string{"HEDGE_BINANCE_ENV": "live"})
	if live.HedgeBaseURL() != BinanceFuturesLiveURL {
		t.Fatalf("live URL %s", live.HedgeBaseURL())
	}
}

func TestHedgeLiveRefusesToBootWithoutKeys(t *testing.T) {
	_, got := hedgeIssues(t, Env{HedgeProvider: "binance", HedgeLiveExecutionEnabled: true}, map[string]string{"HEDGE_BINANCE_ENV": "live"})
	if !strings.Contains(got, "HEDGE_API_KEY and HEDGE_API_SECRET are required") {
		t.Fatalf("live without keys: %q", got)
	}
	_, got = hedgeIssues(t, Env{HedgeProvider: "binance", HedgeLiveExecutionEnabled: true, HedgeAPIKey: "k", HedgeAPISecret: hedgeSecret},
		map[string]string{"HEDGE_BINANCE_ENV": "live"})
	if got != "" {
		t.Fatalf("live with keys: %q", got)
	}
	// Testnet without keys boots (hedging is then unavailable, not broken).
	if _, got = hedgeIssues(t, Env{HedgeProvider: "binance"}, nil); got != "" {
		t.Fatalf("testnet without keys: %q", got)
	}
}

func TestHedgeEnvironmentsCannotBeMixed(t *testing.T) {
	_, got := hedgeIssues(t, Env{HedgeProvider: "binance", HedgeAPIURL: "https://testnet.binancefuture.com"},
		map[string]string{"HEDGE_BINANCE_ENV": "live"})
	if !strings.Contains(got, "testnet URL") {
		t.Fatalf("live + testnet URL: %q", got)
	}
	_, got = hedgeIssues(t, Env{HedgeProvider: "binance", HedgeAPIURL: "https://fapi.binance.com"}, nil)
	if !strings.Contains(got, "live URL") {
		t.Fatalf("testnet + live URL: %q", got)
	}
	_, got = hedgeIssues(t, Env{HedgeProvider: "binance", HedgeAPIURL: "http://fapi.example"}, map[string]string{"HEDGE_BINANCE_ENV": "live"})
	if !strings.Contains(got, "https") {
		t.Fatalf("plain http live: %q", got)
	}
}

func TestHedgeRateNeedsItsTime(t *testing.T) {
	_, got := hedgeIssues(t, Env{HedgeProvider: "paper"}, map[string]string{"HEDGE_INR_PER_USDT": "88.2"})
	if !strings.Contains(got, "HEDGE_INR_PER_USDT_AS_OF") {
		t.Fatalf("rate without time: %q", got)
	}
	e, got := hedgeIssues(t, Env{HedgeProvider: "paper"}, map[string]string{"HEDGE_INR_PER_USDT": "88.2", "HEDGE_INR_PER_USDT_AS_OF": "2026-10-21T09:00:00Z"})
	if got != "" || e.HedgeINRPerUSDT != 88.2 || e.HedgeINRPerUSDTAsOf != 1_792_573_200_000 {
		t.Fatalf("rate: %q %+v", got, e.HedgeINRPerUSDTAsOf)
	}
}

func TestRemovedHedgeAdaptersAreRefused(t *testing.T) {
	for _, p := range []string{"broker", "lp"} {
		if _, got := hedgeIssues(t, Env{HedgeProvider: p}, nil); !strings.Contains(got, "removed") {
			t.Fatalf("%s: %q", p, got)
		}
	}
	if _, got := hedgeIssues(t, Env{HedgeProvider: "paper"}, map[string]string{"HEDGE_SYMBOLS": "BTC/USD=BTC-PERP"}); !strings.Contains(got, "HEDGE_SYMBOLS") {
		t.Fatalf("bad symbols: %q", got)
	}
}
