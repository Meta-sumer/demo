// Package beex is the BeeX Payments gateway: the HTTP client, a mock for
// tests and local development, webhook signatures and the provider error
// taxonomy.
//
// BeeX amounts are always in the coin (USDT), never fiat. They arrive as
// decimal strings ("100.000000") and are converted to integer micro-USDT with
// exact string arithmetic (platform/micro); no float64 ever holds one.
package beex

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"binarytrading/internal/platform/micro"
)

// Amount is a provider amount as BeeX sent it: a JSON string or number,
// kept verbatim.
type Amount struct {
	Raw string
	Set bool
}

func (a *Amount) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" {
		*a = Amount{}
		return nil
	}
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*a = Amount{Raw: strings.TrimSpace(s), Set: strings.TrimSpace(s) != ""}
		return nil
	}
	*a = Amount{Raw: string(b), Set: len(b) > 0}
	return nil
}

func (a Amount) MarshalJSON() ([]byte, error) {
	if !a.Set {
		return []byte("null"), nil
	}
	return json.Marshal(a.Raw)
}

// Micro converts to micro-USDT, flooring beyond 6 decimals (exact=false then).
func (a Amount) Micro() (v int64, exact bool, err error) {
	if !a.Set {
		return 0, true, nil
	}
	return micro.ParseLenient(a.Raw)
}

// AmountOf builds an Amount from micro-USDT ("100.5").
func AmountOf(v int64) Amount { return Amount{Raw: micro.Format(v), Set: true} }

// Time accepts an RFC 3339 string or epoch seconds/milliseconds.
type Time struct {
	T   time.Time
	Set bool
}

func (t *Time) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if string(b) == "null" || len(b) == 0 {
		*t = Time{}
		return nil
	}
	var s string
	if b[0] == '"' {
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
	} else {
		s = string(b)
	}
	s = strings.TrimSpace(s)
	if s == "" {
		*t = Time{}
		return nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			*t = Time{T: time.UnixMilli(n).UTC(), Set: true}
		} else {
			*t = Time{T: time.Unix(n, 0).UTC(), Set: true}
		}
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z07:00", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			*t = Time{T: parsed.UTC(), Set: true}
			return nil
		}
	}
	// An unparseable optional timestamp is not worth failing a payment over.
	*t = Time{}
	return nil
}

func (t Time) MarshalJSON() ([]byte, error) {
	if !t.Set {
		return []byte("null"), nil
	}
	return json.Marshal(t.T.UTC().Format(time.RFC3339Nano))
}

// Ptr returns nil when unset.
func (t Time) Ptr() *time.Time {
	if !t.Set {
		return nil
	}
	v := t.T.UTC().Truncate(time.Millisecond)
	return &v
}

// ── invoices ────────────────────────────────────────────────────────────

// CreateInvoiceRequest is POST /invoices. Amount is a JSON number in the
// coin, written from the exact decimal string (never through a float).
type CreateInvoiceRequest struct {
	OrderID     string      `json:"orderId"`
	Amount      json.Number `json:"amount"`
	PayCurrency string      `json:"payCurrency"`
	Description string      `json:"description,omitempty"`
	SuccessURL  string      `json:"successUrl,omitempty"`
	CancelURL   string      `json:"cancelUrl,omitempty"`
}

// Invoice is BeeX's invoice. Fields BeeX may omit stay zero.
type Invoice struct {
	ID             string `json:"id"`
	Reference      string `json:"reference"`
	OrderID        string `json:"orderId"`
	Mode           string `json:"mode"`
	Status         string `json:"status"`
	PayAmount      Amount `json:"payAmount"`
	PayCurrency    string `json:"payCurrency"`
	PayAddress     string `json:"payAddress"`
	ExpiresAt      Time   `json:"expiresAt"`
	AmountReceived Amount `json:"amountReceived"`
	FeeAmount      Amount `json:"feeAmount"`
	NetAmount      Amount `json:"netAmount"`
	// The response body as received (for the record; never shown to users).
	Raw json.RawMessage `json:"-"`
}

// ── payouts ─────────────────────────────────────────────────────────────

// CreatePayoutRequest is POST /payouts. Reference is ours and is reused on
// every retry: BeeX answers a repeat with created:false and the existing
// payout instead of sending again.
type CreatePayoutRequest struct {
	Address   string `json:"address"`
	Currency  string `json:"currency"`
	Amount    string `json:"amount"`
	Reference string `json:"reference,omitempty"`
}

// BeeX payout statuses.
const (
	PayoutRequested        = "requested"
	PayoutAwaitingApproval = "awaiting_approval"
	PayoutSending          = "sending"
	PayoutCompleted        = "completed"
	PayoutFailed           = "failed"
	PayoutRejected         = "rejected"
)

type Payout struct {
	// BeeX's system reference (OUT2026-...).
	Reference string `json:"reference"`
	// Ours.
	ClientReference string `json:"clientReference"`
	Status          string `json:"status"`
	Amount          Amount `json:"amount"`
	FeeAmount       Amount `json:"feeAmount"`
	NetAmount       Amount `json:"netAmount"`
	Currency        string `json:"currency"`
	Address         string `json:"address"`
	TxHash          string `json:"txHash"`
	// false: the reference already existed and nothing new was sent.
	Created *bool           `json:"created"`
	Raw     json.RawMessage `json:"-"`
}

// ── limits & balance ────────────────────────────────────────────────────

type PayoutLimits struct {
	Currencies         []string `json:"currencies"`
	MinWithdrawal      Amount   `json:"minWithdrawal"`
	MaxWithdrawal      Amount   `json:"maxWithdrawal"`
	DailyWithdrawalCap Amount   `json:"dailyWithdrawalCap"`
	ReviewedAbove      Amount   `json:"reviewedAbove"`
	Fee                struct {
		Percent Amount `json:"percent"`
		Fixed   Amount `json:"fixed"`
	} `json:"fee"`
}

// Balance is GET /balance as BeeX sent it. Its shape is not documented, so it
// is passed through rather than interpreted.
type Balance = any

// ── webhooks ────────────────────────────────────────────────────────────

// WebhookPayload is the union of the invoice and payout event bodies. Unknown
// fields are allowed and missing optional ones are tolerated.
type WebhookPayload struct {
	Event           string `json:"event"`
	Mode            string `json:"mode"`
	Reference       string `json:"reference"`
	OrderID         string `json:"orderId"`
	ClientReference string `json:"clientReference"`
	Status          string `json:"status"`
	PayCurrency     string `json:"payCurrency"`
	AmountReceived  Amount `json:"amountReceived"`
	FeeAmount       Amount `json:"feeAmount"`
	NetAmount       Amount `json:"netAmount"`
	Amount          Amount `json:"amount"`
	Currency        string `json:"currency"`
	Address         string `json:"address"`
	TxHash          string `json:"txHash"`
}

// Webhook event names.
const (
	EventInvoicePaid          = "invoice.paid"
	EventInvoiceOverpaid      = "invoice.overpaid"
	EventInvoicePartiallyPaid = "invoice.partially_paid"
	EventInvoiceUnderpaid     = "invoice.underpaid"
	EventInvoiceExpired       = "invoice.expired"
	EventInvoiceFailed        = "invoice.failed"
	EventPayoutCompleted      = "payout.completed"
	EventPayoutFailed         = "payout.failed"
)

// EventKind is "invoice", "payout" or "unknown".
func EventKind(event string) string {
	switch {
	case strings.HasPrefix(event, "invoice."):
		return "invoice"
	case strings.HasPrefix(event, "payout."):
		return "payout"
	}
	return "unknown"
}
