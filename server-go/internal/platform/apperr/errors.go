// Package apperr is the application error taxonomy. Every error surfaced to
// an API client is an *Error; anything else becomes INTERNAL_ERROR so driver
// messages and stack traces never reach a client.
package apperr

import "errors"

const (
	Unauthenticated    = "UNAUTHENTICATED"
	InvalidCredentials = "INVALID_CREDENTIALS"
	TokenExpired       = "TOKEN_EXPIRED"
	TokenInvalid       = "TOKEN_INVALID"
	Forbidden          = "FORBIDDEN"
	AccountDisabled    = "ACCOUNT_DISABLED"
	EmailTaken         = "EMAIL_TAKEN"
	UsernameTaken      = "USERNAME_TAKEN"

	ValidationError    = "VALIDATION_ERROR"
	NotFound           = "NOT_FOUND"
	Conflict           = "CONFLICT"
	RateLimited        = "RATE_LIMITED"
	InternalError      = "INTERNAL_ERROR"
	ServiceUnavailable = "SERVICE_UNAVAILABLE"
	PayloadTooLarge    = "PAYLOAD_TOO_LARGE"

	InsufficientBalance = "INSUFFICIENT_BALANCE"
	WalletNotFound      = "WALLET_NOT_FOUND"
	InvalidAmount       = "INVALID_AMOUNT"
	LockedFundsMismatch = "LOCKED_FUNDS_MISMATCH"

	AssetNotFound              = "ASSET_NOT_FOUND"
	AssetInactive              = "ASSET_INACTIVE"
	MarketClosed               = "MARKET_CLOSED"
	FeedUnhealthy              = "FEED_UNHEALTHY"
	NoPriceAvailable           = "NO_PRICE_AVAILABLE"
	PriceMoved                 = "PRICE_MOVED"
	ExpiryNotSupported         = "EXPIRY_NOT_SUPPORTED"
	PayoutNotConfigured        = "PAYOUT_NOT_CONFIGURED"
	StakeBelowMin              = "STAKE_BELOW_MIN"
	StakeAboveMax              = "STAKE_ABOVE_MAX"
	UserLimitExceeded          = "USER_LIMIT_EXCEEDED"
	RiskLimitExceeded          = "RISK_LIMIT_EXCEEDED"
	ExposureLimitExceeded      = "EXPOSURE_LIMIT_EXCEEDED"
	TradingHalted              = "TRADING_HALTED"
	TradeNotFound              = "TRADE_NOT_FOUND"
	IdempotencyInProgress      = "IDEMPOTENCY_IN_PROGRESS"
	IdempotencyPayloadMismatch = "IDEMPOTENCY_PAYLOAD_MISMATCH"

	AlreadySettled        = "ALREADY_SETTLED"
	SettlementFailed      = "SETTLEMENT_FAILED"
	HedgeProviderError    = "HEDGE_PROVIDER_ERROR"
	HedgeNotConfigured    = "HEDGE_NOT_CONFIGURED"
	LiveExecutionDisabled = "LIVE_EXECUTION_DISABLED"

	// USDT wallet and the BeeX Payments gateway.
	UsdtDisabled                = "USDT_DISABLED"
	InvalidNetwork              = "INVALID_NETWORK"
	InvalidAddress              = "INVALID_ADDRESS"
	MinWithdrawalNotMet         = "MIN_WITHDRAWAL_NOT_MET"
	MaxWithdrawalExceeded       = "MAX_WITHDRAWAL_EXCEEDED"
	DailyCapExceeded            = "DAILY_CAP_EXCEEDED"
	TransactionNotFound         = "TRANSACTION_NOT_FOUND"
	TransactionAlreadyProcessed = "TRANSACTION_ALREADY_PROCESSED"
	WithdrawalAlreadyProcessed  = "WITHDRAWAL_ALREADY_PROCESSED"
	ProviderError               = "PROVIDER_ERROR"
	ProviderUnavailable         = "PROVIDER_UNAVAILABLE"
	UsdtTransferDisabled        = "USDT_TRANSFER_DISABLED"
)

// Error is an error safe to show to an API client.
type Error struct {
	Code       string
	Message    string
	StatusCode int
	Details    any
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// New builds an error; status defaults to 400 when 0.
func New(code, message string, status int, details ...any) *Error {
	if status == 0 {
		status = 400
	}
	e := &Error{Code: code, Message: message, StatusCode: status}
	if len(details) > 0 {
		e.Details = details[0]
	}
	return e
}

func Unauth(message ...string) *Error {
	return New(Unauthenticated, first(message, "Authentication required"), 401)
}

func Forbid(message ...string) *Error {
	return New(Forbidden, first(message, "Insufficient permissions"), 403)
}

// NotFoundErr: code defaults to NOT_FOUND, message to "Resource not found".
func NotFoundErr(code, message string) *Error {
	if code == "" {
		code = NotFound
	}
	if message == "" {
		message = "Resource not found"
	}
	return New(code, message, 404)
}

func ConflictErr(code, message string) *Error { return New(code, message, 409) }

// Validation is a 422 with optional details (usually []FieldIssue).
func Validation(message string, details any) *Error {
	return New(ValidationError, message, 422, details)
}

func Unavailable(code, message string) *Error { return New(code, message, 503) }

func Internal(message ...string) *Error {
	return New(InternalError, first(message, "Internal server error"), 500)
}

// FieldIssue is one entry of a validation error's details, matching the Node
// server's {path, message} shape.
type FieldIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// As extracts an *Error from err.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// HasCode reports whether err is an *Error with the given code.
func HasCode(err error, code string) bool {
	e, ok := As(err)
	return ok && e.Code == code
}

func first(values []string, def string) string {
	if len(values) > 0 && values[0] != "" {
		return values[0]
	}
	return def
}
