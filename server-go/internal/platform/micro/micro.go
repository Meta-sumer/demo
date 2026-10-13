// Package micro converts between decimal strings and integer micro units
// (6 decimal places: 1 USDT = 1_000_000 micro-USDT).
//
// Everything is string and integer arithmetic. A float64 cannot hold every
// 6-decimal amount exactly (0.1 is already inexact), so no float ever touches
// a USDT amount on its way in or out.
package micro

import (
	"errors"
	"math/big"
	"strconv"
	"strings"
)

// Decimals is the number of decimal places a micro amount carries.
const Decimals = 6

// Scale is one whole unit in micro units.
const Scale int64 = 1_000_000

var (
	ErrSyntax    = errors.New("not a decimal number")
	ErrPrecision = errors.New("more than 6 decimal places")
	ErrNegative  = errors.New("negative amount")
	ErrRange     = errors.New("amount out of range")
)

// maxDigits keeps a parsed integer inside int64 (9.2e18) with room to spare.
const maxDigits = 18

// ParseStrict parses customer input: an unsigned decimal ("100", "100.5",
// "0.000001") with at most 6 decimal places. No sign, exponent or spaces.
func ParseStrict(s string) (int64, error) {
	intPart, fracPart, err := split(s)
	if err != nil {
		return 0, err
	}
	if len(fracPart) > Decimals {
		return 0, ErrPrecision
	}
	return assemble(intPart, fracPart+strings.Repeat("0", Decimals-len(fracPart)))
}

// ParseLenient parses a provider amount. It tolerates surrounding spaces, a
// leading '+' and an exponent ("1e2"), and floors anything beyond the sixth
// decimal place; exact reports whether nothing was dropped. A negative
// amount is an error: no provider amount this platform reads can be one.
func ParseLenient(s string) (v int64, exact bool, err error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "+")
	if strings.HasPrefix(s, "-") {
		return 0, false, ErrNegative
	}
	exp := 0
	if i := strings.IndexAny(s, "eE"); i >= 0 {
		e, perr := strconv.Atoi(strings.TrimPrefix(s[i+1:], "+"))
		if perr != nil || e > 40 || e < -40 {
			return 0, false, ErrSyntax
		}
		exp, s = e, s[:i]
	}
	intPart, fracPart, err := split(s)
	if err != nil {
		return 0, false, err
	}
	digits := intPart + fracPart
	// The decimal point sits after point digits of `digits`; scaling by 10^6
	// moves it six places right.
	point := len(intPart) + exp + Decimals
	switch {
	case point <= 0:
		return 0, strings.Trim(digits, "0") == "", nil
	case point >= len(digits):
		v, err = assemble(digits+strings.Repeat("0", point-len(digits)), "")
		return v, true, err
	default:
		dropped := digits[point:]
		v, err = assemble(digits[:point], "")
		return v, strings.Trim(dropped, "0") == "", err
	}
}

// split checks the shape [digits][.digits] with at least one digit.
func split(s string) (string, string, error) {
	if s == "" {
		return "", "", ErrSyntax
	}
	if s[0] == '-' {
		return "", "", ErrNegative
	}
	intPart, fracPart, hasPoint := strings.Cut(s, ".")
	if hasPoint && strings.Contains(fracPart, ".") {
		return "", "", ErrSyntax
	}
	if intPart == "" && fracPart == "" {
		return "", "", ErrSyntax
	}
	if !allDigits(intPart) || !allDigits(fracPart) {
		return "", "", ErrSyntax
	}
	return intPart, fracPart, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// assemble joins the whole-number digit string a+b into an int64.
func assemble(a, b string) (int64, error) {
	d := strings.TrimLeft(a+b, "0")
	if d == "" {
		return 0, nil
	}
	if len(d) > maxDigits {
		return 0, ErrRange
	}
	n, err := strconv.ParseInt(d, 10, 64)
	if err != nil {
		return 0, ErrRange
	}
	return n, nil
}

// Format renders micro units as the shortest exact decimal: 100000000 ->
// "100", 100500000 -> "100.5", 1 -> "0.000001".
func Format(v int64) string {
	neg := v < 0
	u := uint64(v)
	if neg {
		u = uint64(-(v + 1)) + 1 // safe for MinInt64
	}
	whole := strconv.FormatUint(u/uint64(Scale), 10)
	frac := u % uint64(Scale)
	out := whole
	if frac != 0 {
		fs := strconv.FormatUint(frac, 10)
		fs = strings.Repeat("0", Decimals-len(fs)) + fs
		out += "." + strings.TrimRight(fs, "0")
	}
	if neg {
		return "-" + out
	}
	return out
}

// FormatFixed renders exactly 6 decimals: 100000000 -> "100.000000".
func FormatFixed(v int64) string {
	s := Format(v)
	whole, frac, _ := strings.Cut(s, ".")
	return whole + "." + frac + strings.Repeat("0", Decimals-len(frac))
}

// PercentFloor returns floor(amount * percent / 100) for a non-negative
// amount and a non-negative decimal percent string ("0.5" = 0.5%).
func PercentFloor(amount int64, percent string) (int64, error) {
	if amount < 0 {
		return 0, ErrNegative
	}
	percent = strings.TrimSpace(percent)
	if percent == "" {
		return 0, nil
	}
	intPart, fracPart, err := split(percent)
	if err != nil {
		return 0, err
	}
	if len(intPart)+len(fracPart) > 30 {
		return 0, ErrRange
	}
	num, ok := new(big.Int).SetString("0"+intPart+fracPart, 10)
	if !ok {
		return 0, ErrSyntax
	}
	den := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(len(fracPart))), nil)
	den.Mul(den, big.NewInt(100))
	r := new(big.Int).Mul(big.NewInt(amount), num)
	r.Quo(r, den) // truncation == floor for non-negative operands
	if !r.IsInt64() {
		return 0, ErrRange
	}
	return r.Int64(), nil
}
