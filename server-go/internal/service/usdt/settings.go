// Package usdt is the customer's USDT wallet: deposits through BeeX
// invoices, withdrawals through BeeX payouts, transfers to the trading
// account, and the webhook, retry and reconciliation machinery behind them.
//
// Money: integer micro-USDT everywhere (1 USDT = 1_000_000). The balance is
// the customer's FUNDING/USDT row in `wallets`, changed only through the
// wallet service, so every movement is guarded, ledgered and idempotent.
//
// Exactly-once rules:
//   - a deposit is credited once: ledger key usdt:deposit:<depositId> plus a
//     conditional credited:false -> true update in the same transaction;
//   - a withdrawal locks its gross before BeeX is contacted, and is settled
//     (usdt:withdrawal:settle:<id>) or released (usdt:withdrawal:unlock:<id>)
//     exactly once, again with a conditional status update in the same
//     transaction;
//   - our payout reference (withdrawal-<id>) is reused on every retry, so
//     BeeX can never send a payout twice.
package usdt

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/service/beex"
)

// Ledger reference types.
const (
	RefDeposit    = "UsdtDeposit"
	RefWithdrawal = "UsdtWithdrawal"
	RefTransfer   = "UsdtTransfer"
)

const (
	defaultPayURLBase = "https://beexpayments.com/pay/"
	CreditBasisNet    = "net"
	CreditBasisRecv   = "received"
)

func env() *config.Env {
	if config.E != nil {
		return config.E
	}
	return &config.Env{}
}

// Enabled reports USDT_ENABLED.
func Enabled() bool { return env().UsdtEnabled }

// TransferToTradingEnabled reports USDT_TRANSFER_TO_TRADING_ENABLED.
func TransferToTradingEnabled() bool { return env().UsdtTransferToTradingEnabled }

func requireEnabled() error {
	if !Enabled() {
		return apperr.New(apperr.UsdtDisabled, "USDT payments are not available", 403)
	}
	return nil
}

func creditBasis() string {
	if env().UsdtDepositCreditBasis == CreditBasisNet {
		return CreditBasisNet
	}
	return CreditBasisRecv
}

// reviewAbove: withdrawals above it wait for an operator; 0 = all of them.
func reviewAbove() int64 { return env().UsdtWithdrawalReviewAbove }

func needsReview(amount int64) bool {
	limit := reviewAbove()
	return limit <= 0 || amount > limit
}

func paymentURL(invoiceID string) string {
	base := env().BeexPayURLBase
	if base == "" {
		base = defaultPayURLBase
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	return base + invoiceID
}

func successURL(depositID string) string {
	u := env().UsdtSuccessURL
	if u == "" {
		u = strings.TrimRight(env().ClientURL, "/") + "/wallet/usdt/success"
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
	}
	return u + sep + "deposit=" + depositID
}

func cancelURL() string {
	u := env().UsdtCancelURL
	if u == "" {
		u = strings.TrimRight(env().ClientURL, "/") + "/wallet/usdt"
	}
	return u
}

// ModeAccepted decides whether money reported in this BeeX mode may be
// credited. Production credits live only; development also accepts test
// when BEEX_ACCEPT_TEST_MODE=true (which config refuses in production).
func ModeAccepted(mode string) error {
	switch mode {
	case "live":
		return nil
	case "test":
		if !config.IsProduction() && env().BeexAcceptTestMode {
			return nil
		}
		if config.IsProduction() {
			return errors.New("test-mode payment refused: production credits live payments only")
		}
		return errors.New("test-mode payment refused: set BEEX_ACCEPT_TEST_MODE=true to accept test payments in development")
	case "":
		return errors.New("payment has no mode")
	}
	return errors.New("unknown payment mode " + mode)
}

func provider() (beex.Provider, error) {
	p, err := beex.Get()
	if err != nil {
		return nil, apperr.New(apperr.ProviderUnavailable, "USDT payments are temporarily unavailable. Please try again later.", 503)
	}
	return p, nil
}

// requestHash fingerprints a request so a reused idempotency key with a
// different body is detected.
func requestHash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(sum[:])
}

func payloadMismatch() error {
	return apperr.New(apperr.IdempotencyPayloadMismatch,
		"This idempotency key was already used for a different request", 422)
}

func ptr[T any](v T) *T { return &v }

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func strOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
