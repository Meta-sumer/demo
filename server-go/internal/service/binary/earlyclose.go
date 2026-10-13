package binary

import (
	"math"
	"math/big"
	"strconv"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
)

// Early close valuation (the "Sell now" price).
//
// A binary option before expiry is worth its expected payout:
//
//	value = stake x (1 + payout%) x P(win) x (1 - fee)
//
// P(win) comes from a driftless random walk:
//
//	expectedMove = price x volatility x sqrt(secondsRemaining)
//	z            = signedDistanceInTheMoney / expectedMove
//	P(win)       = Phi(z)
//
// Selling a winning position early pays LESS than letting it expire (the
// platform is buying uncertainty back), and selling early costs the disclosed
// fee. Pure and deterministic, which is what makes it auditable.

type EarlyCloseInput struct {
	// Minor units.
	Stake            int64
	PayoutPercent    float64
	Direction        models.Direction
	EntryPrice       float64
	CurrentPrice     float64
	SecondsRemaining float64
	// Standard deviation of price returns per second, as a fraction.
	VolatilityPerSecond float64
	// Platform spread on an early close, percent of the computed value.
	FeePercent float64
}

type EarlyCloseQuote struct {
	// What the customer receives, minor units.
	Value int64 `json:"value"`
	// value - stake, minor units. Negative is a loss.
	CustomerResult int64 `json:"customerResult"`
	// Probability the trade would finish in the money, 0..1.
	WinProbability float64 `json:"winProbability"`
	// True when the position is currently in the money.
	InTheMoney bool `json:"inTheMoney"`
}

// Probability that the trade finishes in the money, clamped away from certainty.
const (
	minProbability = 0.01
	maxProbability = 0.99
)

func QuoteEarlyClose(in EarlyCloseInput) EarlyCloseQuote {
	move := in.CurrentPrice - in.EntryPrice
	signedMove := move
	if in.Direction != models.DirectionUp {
		signedMove = -move
	}
	inTheMoney := signedMove > 0

	winProbability := clamp(
		estimateWinProbability(signedMove, in.CurrentPrice, in.SecondsRemaining, in.VolatilityPerSecond),
		minProbability, maxProbability,
	)

	fullPayout := float64(in.Stake) * (1 + in.PayoutPercent/100)
	fee := clamp(in.FeePercent, 0, 100) / 100
	value := int64(math.Max(0, util.RoundHalfUp(fullPayout*winProbability*(1-fee))))

	return EarlyCloseQuote{
		Value:          value,
		CustomerResult: value - in.Stake,
		WinProbability: roundTo(winProbability, 4),
		InTheMoney:     inTheMoney,
	}
}

func estimateWinProbability(signedMove, price, secondsRemaining, volatilityPerSecond float64) float64 {
	// At or past expiry there is no uncertainty left to price.
	if secondsRemaining <= 0 {
		if signedMove > 0 {
			return 1
		}
		return 0
	}

	expectedMove := price * volatilityPerSecond * math.Sqrt(secondsRemaining)

	// A still market: without an expected move the position is worth its
	// current state - except exactly at entry, which is a genuine coin flip.
	if math.IsNaN(expectedMove) || math.IsInf(expectedMove, 0) || expectedMove <= 0 {
		if signedMove == 0 {
			return 0.5
		}
		if signedMove > 0 {
			return 1
		}
		return 0
	}

	return StandardNormalCdf(signedMove / expectedMove)
}

// StandardNormalCdf is Phi(z), via the Abramowitz and Stegun 7.1.26
// approximation of erf.
func StandardNormalCdf(z float64) float64 {
	return 0.5 * (1 + erf(z/math.Sqrt2))
}

func erf(x float64) float64 {
	sign := 1.0
	if x < 0 {
		sign = -1
	}
	absolute := math.Abs(x)

	t := 1 / (1 + 0.3275911*absolute)
	series := t * (0.254829592 + t*(-0.284496736+t*(1.421413741+t*(-1.453152027+t*1.061405429))))

	return sign * (1 - series*math.Exp(-absolute*absolute))
}

func clamp(value, min, max float64) float64 {
	return math.Min(max, math.Max(min, value))
}

// toFixed reproduces JS Number.prototype.toFixed: rounds the exact binary
// value, ties away from zero (Go's strconv rounds ties to even).
func toFixed(x float64, digits int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 1e21 {
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	neg := x < 0
	if neg {
		x = -x
	}
	scaled := new(big.Float).SetPrec(256).SetFloat64(x)
	scaled.Mul(scaled, new(big.Float).SetPrec(256).SetFloat64(math.Pow10(digits)))
	scaled.Add(scaled, big.NewFloat(0.5).SetPrec(256))
	n, _ := scaled.Int(nil)
	s := n.String()
	if digits > 0 {
		for len(s) <= digits {
			s = "0" + s
		}
		s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	if neg && n.Sign() != 0 {
		s = "-" + s
	}
	return s
}

// roundTo is JS Number(x.toFixed(digits)).
func roundTo(x float64, digits int) float64 {
	f, _ := strconv.ParseFloat(toFixed(x, digits), 64)
	return f
}
