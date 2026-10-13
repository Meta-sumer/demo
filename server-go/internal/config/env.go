// Package config holds process configuration, shared constants and RBAC.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

// Env mirrors the Node server's environment schema one to one, so the same
// .env file configures either implementation.
type Env struct {
	NodeEnv  string
	Port     int
	Host     string
	LogLevel string

	MongoURI                string
	RedisURL                string
	EnableMongoTransactions bool

	JWTSecret        string
	JWTRefreshSecret string
	JWTAccessTTL     string
	JWTRefreshTTL    string
	BcryptRounds     int

	MarketDataProvider             string
	MarketDataAPIKey               string
	MarketDataAPISecret            string
	MarketDataWSURL                string
	AllowSimulatedFeedInProduction bool

	FeedHealthyMaxMs  int64
	FeedDegradedMaxMs int64
	FeedStaleMaxMs    int64
	FeedTickMaxAgeMs  int64

	HedgeProvider             string
	HedgeAPIKey               string
	HedgeAPISecret            string
	HedgeAPIURL               string
	HedgeLiveExecutionEnabled bool

	WorkersEnabled           bool
	ExpiryTickMs             int64
	SettlementBatchSize      int
	RiskIntervalMs           int64
	HedgeIntervalMs          int64
	ReconciliationIntervalMs int64

	ClientURL string
	AdminURL  string

	MaxRequestBodyBytes     int64
	UploadDir               string
	MaxUploadBytes          int64
	WSMaxConnectionsPerUser int
	WSMaxConnectionsTotal   int

	SeedSuperAdminEmail    string
	SeedSuperAdminPassword string

	// USDT wallet through BeeX Payments. Amounts are micro-USDT (1 USDT =
	// 1_000_000).
	UsdtEnabled        bool
	BeexProvider       string // "beex" | "mock"
	BeexAPIBaseURL     string
	BeexAPIKey         string
	BeexWebhookSecret  string
	BeexAcceptTestMode bool
	BeexPayURLBase     string
	UsdtSuccessURL     string
	UsdtCancelURL      string
	// "received" credits what the customer sent; "net" credits it after
	// BeeX's fee.
	UsdtDepositCreditBasis string
	// Withdrawals above this (micro) wait for an operator; 0 = every one does.
	UsdtWithdrawalReviewAbove    int64
	UsdtTransferToTradingEnabled bool
	// Per-customer rolling 24h withdrawal total (micro); 0 = BeeX's
	// dailyWithdrawalCap.
	UsdtUserDailyWithdrawalLimit int64
}

// BeexKeyMode is "live" for a ck_live_ key, "test" for ck_test_, "" otherwise.
func BeexKeyMode(key string) string {
	switch {
	case strings.HasPrefix(key, "ck_live_"):
		return "live"
	case strings.HasPrefix(key, "ck_test_"):
		return "test"
	}
	return ""
}

// E is the loaded configuration. Load must run before anything reads it.
var E *Env

func IsProduction() bool { return E != nil && E.NodeEnv == "production" }
func IsTest() bool       { return E != nil && E.NodeEnv == "test" }

var durationRe = regexp.MustCompile(`^\d+(ms|s|m|h|d)$`)

type loader struct{ issues []string }

func (l *loader) fail(key, msg string) { l.issues = append(l.issues, "  - "+key+": "+msg) }

func (l *loader) str(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func (l *loader) required(key string) string {
	v := os.Getenv(key)
	if v == "" {
		l.fail(key, "Required")
	}
	return v
}

func (l *loader) enum(key, def string, allowed ...string) string {
	v := l.str(key, def)
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	l.fail(key, "Invalid enum value. Expected "+strings.Join(allowed, " | ")+", received '"+v+"'")
	return def
}

func (l *loader) integer(key string, def int64, min int64) int64 {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		l.fail(key, "Expected integer, received '"+raw+"'")
		return def
	}
	if n < min {
		l.fail(key, fmt.Sprintf("Number must be greater than or equal to %d", min))
	}
	return n
}

func (l *loader) boolean(key string, def bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func (l *loader) duration(key, def string) string {
	v := l.str(key, def)
	if !durationRe.MatchString(v) {
		l.fail(key, "Invalid")
	}
	return v
}

// Load reads .env (server-local first, then the monorepo root) and validates
// it. A misconfigured trading server must not boot, so every problem is
// collected and returned together.
func Load() (*Env, error) {
	cwd, _ := os.Getwd()
	_ = godotenv.Load(filepath.Join(cwd, ".env"))
	_ = godotenv.Load(filepath.Join(cwd, "..", ".env"))

	l := &loader{}
	e := &Env{
		NodeEnv:  l.enum("NODE_ENV", "development", "development", "test", "production"),
		Port:     int(l.integer("PORT", 4000, 1)),
		Host:     l.str("HOST", "0.0.0.0"),
		LogLevel: l.enum("LOG_LEVEL", "info", "fatal", "error", "warn", "info", "debug", "trace", "silent"),

		MongoURI:                l.required("MONGO_URI"),
		RedisURL:                l.required("REDIS_URL"),
		EnableMongoTransactions: l.boolean("ENABLE_MONGO_TRANSACTIONS", true),

		JWTSecret:        l.str("JWT_SECRET", ""),
		JWTRefreshSecret: l.str("JWT_REFRESH_SECRET", ""),
		JWTAccessTTL:     l.duration("JWT_ACCESS_TTL", "15m"),
		JWTRefreshTTL:    l.duration("JWT_REFRESH_TTL", "30d"),
		BcryptRounds:     int(l.integer("BCRYPT_ROUNDS", 12, 8)),

		MarketDataProvider:             l.enum("MARKET_DATA_PROVIDER", "simulated", "simulated", "binance", "generic-ws"),
		MarketDataAPIKey:               l.str("MARKET_DATA_API_KEY", ""),
		MarketDataAPISecret:            l.str("MARKET_DATA_API_SECRET", ""),
		MarketDataWSURL:                l.str("MARKET_DATA_WS_URL", ""),
		AllowSimulatedFeedInProduction: l.boolean("ALLOW_SIMULATED_FEED_IN_PRODUCTION", false),

		FeedHealthyMaxMs:  l.integer("FEED_HEALTHY_MAX_MS", 100, 1),
		FeedDegradedMaxMs: l.integer("FEED_DEGRADED_MAX_MS", 300, 1),
		FeedStaleMaxMs:    l.integer("FEED_STALE_MAX_MS", 1000, 1),
		FeedTickMaxAgeMs:  l.integer("FEED_TICK_MAX_AGE_MS", 5000, 1),

		HedgeProvider:             l.enum("HEDGE_PROVIDER", "paper", "paper", "broker", "lp"),
		HedgeAPIKey:               l.str("HEDGE_API_KEY", ""),
		HedgeAPISecret:            l.str("HEDGE_API_SECRET", ""),
		HedgeAPIURL:               l.str("HEDGE_API_URL", ""),
		HedgeLiveExecutionEnabled: l.boolean("HEDGE_LIVE_EXECUTION_ENABLED", false),

		WorkersEnabled:           l.boolean("WORKERS_ENABLED", true),
		ExpiryTickMs:             l.integer("EXPIRY_TICK_MS", 100, 10),
		SettlementBatchSize:      int(l.integer("SETTLEMENT_BATCH_SIZE", 200, 1)),
		RiskIntervalMs:           l.integer("RISK_INTERVAL_MS", 1000, 100),
		HedgeIntervalMs:          l.integer("HEDGE_INTERVAL_MS", 1000, 100),
		ReconciliationIntervalMs: l.integer("RECONCILIATION_INTERVAL_MS", 300_000, 10_000),

		ClientURL: l.str("CLIENT_URL", "http://localhost:5173"),
		AdminURL:  l.str("ADMIN_URL", "http://localhost:5174"),

		MaxRequestBodyBytes:     l.integer("MAX_REQUEST_BODY_BYTES", 262_144, 1),
		UploadDir:               l.str("UPLOAD_DIR", "./uploads"),
		MaxUploadBytes:          l.integer("MAX_UPLOAD_BYTES", 5_242_880, 1),
		WSMaxConnectionsPerUser: int(l.integer("WS_MAX_CONNECTIONS_PER_USER", 5, 1)),
		WSMaxConnectionsTotal:   int(l.integer("WS_MAX_CONNECTIONS_TOTAL", 10_000, 1)),

		SeedSuperAdminEmail:    l.str("SEED_SUPER_ADMIN_EMAIL", "admin@binary.local"),
		SeedSuperAdminPassword: l.str("SEED_SUPER_ADMIN_PASSWORD", "Admin@12345"),

		UsdtEnabled:                  l.boolean("USDT_ENABLED", false),
		BeexProvider:                 l.enum("BEEX_PROVIDER", "beex", "beex", "mock"),
		BeexAPIBaseURL:               strings.TrimRight(l.str("BEEX_API_BASE_URL", "https://beexpayments.com/api/v1"), "/"),
		BeexAPIKey:                   strings.TrimSpace(l.str("BEEX_API_KEY", "")),
		BeexWebhookSecret:            l.str("BEEX_WEBHOOK_SECRET", ""),
		BeexAcceptTestMode:           l.boolean("BEEX_ACCEPT_TEST_MODE", false),
		BeexPayURLBase:               l.str("BEEX_PAY_URL_BASE", "https://beexpayments.com/pay/"),
		UsdtDepositCreditBasis:       l.enum("USDT_DEPOSIT_CREDIT_BASIS", "received", "received", "net"),
		UsdtWithdrawalReviewAbove:    l.integer("USDT_WITHDRAWAL_REVIEW_ABOVE", 0, 0),
		UsdtTransferToTradingEnabled: l.boolean("USDT_TRANSFER_TO_TRADING_ENABLED", false),
		UsdtUserDailyWithdrawalLimit: l.integer("USDT_USER_DAILY_WITHDRAWAL_LIMIT", 0, 0),
	}
	e.UsdtSuccessURL = l.str("USDT_SUCCESS_URL", strings.TrimRight(e.ClientURL, "/")+"/wallet/usdt/success")
	e.UsdtCancelURL = l.str("USDT_CANCEL_URL", strings.TrimRight(e.ClientURL, "/")+"/wallet/usdt")
	validateUsdt(l, e)

	if e.BcryptRounds > 15 {
		l.fail("BCRYPT_ROUNDS", "Number must be less than or equal to 15")
	}
	if len(e.JWTSecret) < 32 {
		l.fail("JWT_SECRET", "JWT_SECRET must be at least 32 characters")
	}
	if len(e.JWTRefreshSecret) < 32 {
		l.fail("JWT_REFRESH_SECRET", "JWT_REFRESH_SECRET must be at least 32 characters")
	}
	if !(e.FeedHealthyMaxMs < e.FeedDegradedMaxMs && e.FeedDegradedMaxMs < e.FeedStaleMaxMs) {
		l.fail("FEED_HEALTHY_MAX_MS", "Feed thresholds must satisfy healthy < degraded < stale")
	}
	if e.NodeEnv == "production" {
		if e.MarketDataProvider == "simulated" && !e.AllowSimulatedFeedInProduction {
			l.fail("MARKET_DATA_PROVIDER", "The simulated market feed is not an authoritative price source. Configure a real MARKET_DATA_PROVIDER or set ALLOW_SIMULATED_FEED_IN_PRODUCTION=true deliberately.")
		}
		if e.JWTSecret == e.JWTRefreshSecret {
			l.fail("JWT_REFRESH_SECRET", "JWT_SECRET and JWT_REFRESH_SECRET must differ")
		}
		if strings.HasPrefix(e.JWTSecret, "change-me") {
			l.fail("JWT_SECRET", "Default development secrets cannot be used in production")
		}
	}
	if e.MarketDataProvider == "generic-ws" && e.MarketDataWSURL == "" {
		l.fail("MARKET_DATA_WS_URL", "MARKET_DATA_WS_URL is required for the generic-ws provider")
	}

	if len(l.issues) > 0 {
		return nil, errors.New("Invalid environment configuration:\n" + strings.Join(l.issues, "\n"))
	}
	E = e
	return e, nil
}

// validateUsdt: a misconfigured payment gateway must not boot in production -
// a test key there would hand out addresses whose payments never settle, and
// the mock provider would invent them.
func validateUsdt(l *loader, e *Env) {
	prod := e.NodeEnv == "production"
	if e.BeexAPIKey != "" && BeexKeyMode(e.BeexAPIKey) == "" {
		l.fail("BEEX_API_KEY", "BEEX_API_KEY must start with ck_live_ or ck_test_")
	}
	if !strings.HasPrefix(e.BeexAPIBaseURL, "https://") && !strings.HasPrefix(e.BeexAPIBaseURL, "http://") {
		l.fail("BEEX_API_BASE_URL", "Invalid url")
	}
	if !strings.HasPrefix(e.BeexPayURLBase, "https://") && !strings.HasPrefix(e.BeexPayURLBase, "http://") {
		l.fail("BEEX_PAY_URL_BASE", "Invalid url")
	}
	if !prod {
		return
	}
	if e.BeexProvider == "mock" {
		l.fail("BEEX_PROVIDER", "The mock BeeX provider simulates payments and cannot be used in production")
	}
	if e.BeexAcceptTestMode {
		l.fail("BEEX_ACCEPT_TEST_MODE", "Test-mode payments can never be credited in production; remove BEEX_ACCEPT_TEST_MODE")
	}
	if !e.UsdtEnabled {
		return
	}
	if e.BeexAPIKey == "" {
		l.fail("BEEX_API_KEY", "BEEX_API_KEY is required when USDT_ENABLED=true in production")
	} else if BeexKeyMode(e.BeexAPIKey) != "live" {
		l.fail("BEEX_API_KEY", "Production requires a live BeeX key (ck_live_...)")
	}
	if len(e.BeexWebhookSecret) < 16 {
		l.fail("BEEX_WEBHOOK_SECRET", "BEEX_WEBHOOK_SECRET (at least 16 characters) is required when USDT_ENABLED=true in production")
	}
	if !strings.HasPrefix(e.BeexAPIBaseURL, "https://") {
		l.fail("BEEX_API_BASE_URL", "BEEX_API_BASE_URL must use https in production")
	}
}
