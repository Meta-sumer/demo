package usdt

import (
	"errors"

	"binarytrading/internal/service/beex"
)

// ApplyResult reports what applying a provider update did.
type ApplyResult struct {
	Changed  bool   `json:"changed"`
	Credited bool   `json:"credited"`
	Note     string `json:"note"`
}

// IgnoredError: the update was understood and deliberately not applied
// (wrong mode, unknown record, mismatch). Retrying it would not help.
type IgnoredError struct{ Reason string }

func (e *IgnoredError) Error() string { return e.Reason }

func ignored(reason string) error { return &IgnoredError{Reason: reason} }

// AsIgnored extracts an IgnoredError.
func AsIgnored(err error) (*IgnoredError, bool) {
	var e *IgnoredError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

var (
	errAlreadyCredited = errors.New("deposit already credited")
	errAlreadyFinal    = errors.New("withdrawal already final")
	// errNeedAmounts: a paid report without the amounts the credit needs;
	// the caller asks BeeX for the full invoice instead.
	errNeedAmounts = errors.New("paid invoice reported without the amounts needed to credit it")
)

// providerAmount is a converted provider amount; Raw is BeeX's exact string,
// kept when it carried more than 6 decimals (Exact=false, V floored).
type providerAmount struct {
	V     int64
	Raw   string
	Exact bool
}

func convertAmount(a beex.Amount) (*providerAmount, error) {
	if !a.Set {
		return nil, nil
	}
	v, exact, err := a.Micro()
	if err != nil {
		return nil, ignored("malformed amount " + a.Raw)
	}
	return &providerAmount{V: v, Raw: a.Raw, Exact: exact}, nil
}

func amountValue(a *providerAmount) *int64 {
	if a == nil {
		return nil
	}
	v := a.V
	return &v
}

func providerRaw(err error) *string {
	if e, ok := beex.AsError(err); ok {
		s := e.Code
		if e.Status != 0 {
			s += " (HTTP " + itoa(int64(e.Status)) + ")"
		}
		if e.Raw != "" {
			s += ": " + e.Raw
		} else if e.Message != "" {
			s += ": " + e.Message
		}
		return &s
	}
	if err == nil {
		return nil
	}
	s := err.Error()
	return &s
}
