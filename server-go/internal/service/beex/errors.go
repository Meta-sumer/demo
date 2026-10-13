package beex

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"binarytrading/internal/platform/apperr"
)

// BeeX error codes.
const (
	CodeMissingAPIKey            = "missing_api_key"
	CodeInvalidAPIKey            = "invalid_api_key"
	CodeAPIKeyRevoked            = "api_key_revoked"
	CodeMerchantNotActive        = "merchant_not_active"
	CodeCurrencyNotEnabled       = "currency_not_enabled"
	CodeFiatPricingNotSupported  = "fiat_pricing_not_supported"
	CodeBelowNetworkMinimum      = "below_network_minimum"
	CodeAmountBelowMinimum       = "amount_below_minimum"
	CodeLiveKeyRequired          = "live_key_required"
	CodeInvalidAddress           = "invalid_address"
	CodeBelowMinimum             = "below_minimum"
	CodeAboveMaximum             = "above_maximum"
	CodeDailyCapExceeded         = "daily_cap_exceeded"
	CodeInsufficientBalance      = "insufficient_balance"
	CodeRateLimited              = "rate_limited"
	CodeProviderError            = "provider_error"
	CodeNotFound                 = "not_found"
	codeTimeout                  = "timeout"
	codeNetwork                  = "network_error"
	codeDecode                   = "invalid_response"
	codeNotConfigured            = "not_configured"
	codeHTTPPrefix               = "http_"
	maxStoredErrorBodyCharacters = 2000
)

// Error is a failed BeeX call. Message and Raw are the provider's words: they
// are logged and stored for operators but never shown to a customer - use
// SafeMessage / ToAppError for that.
type Error struct {
	// HTTP status; 0 when no response arrived.
	Status  int
	Code    string
	Message string
	// Response body, truncated.
	Raw string
	// The outcome is unknown or transient (timeout, network, 5xx, 429): the
	// request may be retried, and a POST may or may not have taken effect.
	Temporary bool
}

func (e *Error) Error() string {
	s := "beex: " + e.Code
	if e.Status != 0 {
		s += " (HTTP " + strconv.Itoa(e.Status) + ")"
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// AsError extracts a *Error.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsTemporary: the call's outcome is unknown or transient - retry it.
func IsTemporary(err error) bool {
	e, ok := AsError(err)
	return !ok || e.Temporary
}

// IsNotFound: BeeX answered that the resource does not exist.
func IsNotFound(err error) bool {
	e, ok := AsError(err)
	return ok && !e.Temporary && (e.Status == http.StatusNotFound || e.Code == CodeNotFound)
}

// IsDefinite: BeeX answered and refused - the request had no effect.
func IsDefinite(err error) bool {
	e, ok := AsError(err)
	return ok && !e.Temporary && e.Status >= 400 && e.Status < 500
}

// IsConfigError: our credentials or account are the problem, not the request.
func IsConfigError(err error) bool {
	e, ok := AsError(err)
	if !ok {
		return false
	}
	switch e.Code {
	case CodeMissingAPIKey, CodeInvalidAPIKey, CodeAPIKeyRevoked, CodeMerchantNotActive, CodeLiveKeyRequired, codeNotConfigured:
		return true
	}
	return false
}

func notConfigured(msg string) *Error {
	return &Error{Code: codeNotConfigured, Message: msg, Temporary: true}
}

// parseErrorBody reads the (undocumented) error body defensively:
// {error:{code,message}}, {code,message}, {error:"code",message}.
func parseErrorBody(status int, body []byte) *Error {
	e := &Error{Status: status, Raw: truncate(string(body), maxStoredErrorBodyCharacters)}
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) == nil {
		if raw, ok := top["error"]; ok {
			var nested struct {
				Code    string `json:"code"`
				Message string `json:"message"`
				Type    string `json:"type"`
			}
			var s string
			switch {
			case json.Unmarshal(raw, &nested) == nil && (nested.Code != "" || nested.Message != ""):
				e.Code, e.Message = nested.Code, nested.Message
				if e.Code == "" {
					e.Code = nested.Type
				}
			case json.Unmarshal(raw, &s) == nil:
				e.Code = s
			}
		}
		if e.Code == "" {
			_ = json.Unmarshal(top["code"], &e.Code)
		}
		if e.Message == "" {
			_ = json.Unmarshal(top["message"], &e.Message)
		}
	}
	e.Code = strings.ToLower(strings.TrimSpace(e.Code))
	if e.Code == "" {
		switch {
		case status == http.StatusNotFound:
			e.Code = CodeNotFound
		case status == http.StatusTooManyRequests:
			e.Code = CodeRateLimited
		case status >= 500:
			e.Code = CodeProviderError
		default:
			e.Code = codeHTTPPrefix + strconv.Itoa(status)
		}
	}
	e.Message = truncate(e.Message, 300)
	// 408/429/5xx: BeeX did not (or may not have) acted - retry.
	e.Temporary = status >= 500 || status == http.StatusTooManyRequests || status == http.StatusRequestTimeout ||
		e.Code == CodeRateLimited || e.Code == CodeProviderError
	return e
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// SafeMessage is what a customer may read about a provider failure.
func SafeMessage(err error) string {
	e, ok := AsError(err)
	if !ok {
		return "The payment provider is not reachable right now. Please try again later."
	}
	switch e.Code {
	case CodeMissingAPIKey, CodeInvalidAPIKey, CodeAPIKeyRevoked, CodeMerchantNotActive, CodeLiveKeyRequired, codeNotConfigured:
		return "USDT payments are temporarily unavailable. Please try again later."
	case CodeCurrencyNotEnabled:
		return "This network is not available right now. Please choose another network."
	case CodeFiatPricingNotSupported:
		return "This amount could not be processed. Enter the amount in USDT."
	case CodeBelowNetworkMinimum, CodeAmountBelowMinimum, CodeBelowMinimum:
		return "The amount is below the minimum for this network."
	case CodeAboveMaximum:
		return "The amount is above the maximum allowed for a single withdrawal."
	case CodeInvalidAddress:
		return "The address is not valid for the selected network."
	case CodeDailyCapExceeded:
		return "The daily withdrawal limit has been reached. Please try again later."
	case CodeInsufficientBalance:
		// The merchant's balance at BeeX: an operational problem, not the customer's.
		return "Withdrawals are temporarily unavailable. Please try again later."
	case CodeRateLimited:
		return "The payment provider is busy. Please try again in a moment."
	}
	if e.Temporary {
		return "The payment provider is not reachable right now. Please try again later."
	}
	return "The payment provider could not process this request. Please try again later."
}

// ToAppError maps a provider failure to the API's error taxonomy with a safe
// message.
func ToAppError(err error) *apperr.Error {
	msg := SafeMessage(err)
	e, ok := AsError(err)
	if !ok {
		return apperr.New(apperr.ProviderUnavailable, msg, 503)
	}
	switch e.Code {
	case CodeMissingAPIKey, CodeInvalidAPIKey, CodeAPIKeyRevoked, CodeMerchantNotActive, CodeLiveKeyRequired, codeNotConfigured:
		return apperr.New(apperr.ProviderUnavailable, msg, 503)
	case CodeCurrencyNotEnabled:
		return apperr.New(apperr.InvalidNetwork, msg, 400)
	case CodeBelowNetworkMinimum, CodeAmountBelowMinimum, CodeFiatPricingNotSupported:
		return apperr.New(apperr.InvalidAmount, msg, 400)
	case CodeBelowMinimum:
		return apperr.New(apperr.MinWithdrawalNotMet, msg, 400)
	case CodeAboveMaximum:
		return apperr.New(apperr.MaxWithdrawalExceeded, msg, 400)
	case CodeInvalidAddress:
		return apperr.New(apperr.InvalidAddress, msg, 400)
	case CodeDailyCapExceeded:
		return apperr.New(apperr.DailyCapExceeded, msg, 400)
	case CodeInsufficientBalance:
		return apperr.New(apperr.ProviderUnavailable, msg, 503)
	case CodeRateLimited:
		return apperr.New(apperr.RateLimited, msg, 429)
	}
	if e.Temporary {
		return apperr.New(apperr.ProviderUnavailable, msg, 503)
	}
	return apperr.New(apperr.ProviderError, msg, 502)
}
