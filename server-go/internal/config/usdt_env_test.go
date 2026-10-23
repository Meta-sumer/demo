package config

import (
	"strings"
	"testing"
)

// validateUsdt is the boot-time guard of the USDT wallet: live mode never
// boots without credentials, and production runs the sandbox only when told
// to, explicitly.

func usdtIssues(e Env) string {
	if e.BeexAPIBaseURL == "" {
		e.BeexAPIBaseURL = "https://beexpayments.com/api/v1"
	}
	if e.BeexPayURLBase == "" {
		e.BeexPayURLBase = "https://beexpayments.com/pay/"
	}
	if e.BeexProvider == "" {
		e.BeexProvider = "beex"
	}
	l := &loader{}
	validateUsdt(l, &e)
	return strings.Join(l.issues, "\n")
}

const goodSecret = "whsec_0123456789abcdef"

func TestUsdtLiveModeRefusesToBootWithoutKeys(t *testing.T) {
	for _, env := range []string{"development", "production"} {
		got := usdtIssues(Env{NodeEnv: env, UsdtEnabled: true})
		if !strings.Contains(got, "BEEX_API_KEY") || !strings.Contains(got, "BEEX_WEBHOOK_SECRET") {
			t.Fatalf("%s without keys: %q", env, got)
		}
	}
	if got := usdtIssues(Env{NodeEnv: "production", UsdtEnabled: true, BeexAPIKey: "ck_live_abc", BeexWebhookSecret: goodSecret}); got != "" {
		t.Fatalf("production with live keys: %q", got)
	}
	if got := usdtIssues(Env{NodeEnv: "production", UsdtEnabled: true, BeexAPIKey: "ck_live_abc", BeexWebhookSecret: "short"}); !strings.Contains(got, "BEEX_WEBHOOK_SECRET") {
		t.Fatalf("short secret accepted: %q", got)
	}
	if got := usdtIssues(Env{NodeEnv: "production", UsdtEnabled: true, BeexAPIKey: "ck_live_abc", BeexWebhookSecret: goodSecret,
		BeexAPIBaseURL: "http://beex.local/api"}); !strings.Contains(got, "https") {
		t.Fatalf("plain http accepted in production: %q", got)
	}
	if got := usdtIssues(Env{NodeEnv: "production", UsdtEnabled: true, BeexAPIKey: "sk_whatever", BeexWebhookSecret: goodSecret}); !strings.Contains(got, "ck_live_") {
		t.Fatalf("unknown key prefix accepted: %q", got)
	}
	// Disabled: nothing is required.
	if got := usdtIssues(Env{NodeEnv: "production"}); got != "" {
		t.Fatalf("disabled: %q", got)
	}
}

func TestUsdtProductionSandboxNeedsTheExplicitSwitch(t *testing.T) {
	mock := Env{NodeEnv: "production", UsdtEnabled: true, BeexProvider: "mock", BeexAcceptTestMode: true}
	if got := usdtIssues(mock); !strings.Contains(got, "BEEX_PROVIDER") || !strings.Contains(got, "BEEX_ACCEPT_TEST_MODE") {
		t.Fatalf("mock in production without the switch: %q", got)
	}
	testKey := Env{NodeEnv: "production", UsdtEnabled: true, BeexAPIKey: "ck_test_abc", BeexWebhookSecret: goodSecret}
	if got := usdtIssues(testKey); !strings.Contains(got, "ck_live_") {
		t.Fatalf("test key in production without the switch: %q", got)
	}
	mock.AllowBeexSandboxInProduction = true
	testKey.AllowBeexSandboxInProduction = true
	if got := usdtIssues(mock); got != "" {
		t.Fatalf("mock sandbox with the switch: %q", got)
	}
	if got := usdtIssues(testKey); got != "" {
		t.Fatalf("test-key sandbox with the switch: %q", got)
	}
	// The switch never stands in for credentials on the real gateway.
	if got := usdtIssues(Env{NodeEnv: "production", UsdtEnabled: true, AllowBeexSandboxInProduction: true}); !strings.Contains(got, "BEEX_API_KEY") {
		t.Fatalf("sandbox switch without a key: %q", got)
	}
}

func TestUsdtDevelopmentMockNeedsNoKeys(t *testing.T) {
	if got := usdtIssues(Env{NodeEnv: "development", UsdtEnabled: true, BeexProvider: "mock", BeexAcceptTestMode: true}); got != "" {
		t.Fatalf("development mock: %q", got)
	}
}
