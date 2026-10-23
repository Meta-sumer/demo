package providers

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"binarytrading/internal/models"
)

// ErrDecimal is a venue number that is not a plain decimal.
var ErrDecimal = errors.New("not a decimal number")

// ParseE8 parses a signed decimal string ("-0.00123", "65000.1") into 1e-8
// units with string arithmetic. Digits beyond the 8th decimal are rounded
// half away from zero (Binance reports at most 8).
func ParseE8(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, ErrDecimal
	}
	neg := false
	switch s[0] {
	case '-':
		neg, s = true, s[1:]
	case '+':
		s = s[1:]
	}
	intPart, frac, _ := strings.Cut(s, ".")
	if intPart == "" && frac == "" {
		return 0, ErrDecimal
	}
	for _, part := range []string{intPart, frac} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, ErrDecimal
			}
		}
	}
	roundUp := false
	if len(frac) > 8 {
		roundUp = frac[8] >= '5'
		frac = frac[:8]
	}
	frac += strings.Repeat("0", 8-len(frac))
	intPart = strings.TrimLeft(intPart, "0")
	if len(intPart) > 10 {
		return 0, ErrDecimal
	}
	if intPart == "" {
		intPart = "0"
	}
	v, err := strconv.ParseInt(intPart+frac, 10, 64)
	if err != nil {
		return 0, ErrDecimal
	}
	if roundUp {
		v++
	}
	if neg {
		v = -v
	}
	return v, nil
}

// FormatE8 renders 1e-8 units as a plain decimal without trailing zeros.
func FormatE8(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	scale := models.QtyScale
	whole, frac := v/scale, v%scale
	if frac == 0 {
		return sign + strconv.FormatInt(whole, 10)
	}
	f := strings.TrimRight(strconv.FormatInt(frac+scale, 10)[1:], "0")
	return sign + strconv.FormatInt(whole, 10) + "." + f
}

// FloatToE8 converts a float (a price product, a rate) to 1e-8 units,
// rounding half away from zero.
func FloatToE8(f float64) int64 {
	return int64(math.Round(f * float64(models.QtyScale)))
}

// E8ToFloat is the inverse, for display and products with prices.
func E8ToFloat(v int64) float64 { return float64(v) / float64(models.QtyScale) }

func parseFloat(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSpace(s), 64)
	return f
}
