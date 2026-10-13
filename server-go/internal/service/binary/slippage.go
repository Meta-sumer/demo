package binary

import (
	"math"

	"binarytrading/internal/models"
)

// Slippage guard.
//
// The customer's screen shows a price; by the time their click reaches the
// server, the market has moved. This decides whether the move is small enough
// to fill at.
//
// It is a GUARD, never a price source. The fill is always the server's latest
// stored tick - the client's number is consulted only to answer "has this moved
// against them by more than they agreed to tolerate?". There is no path by
// which a value the client sent can become the entry price.
//
// A move in the customer's FAVOUR is never refused.

type SlippageInput struct {
	// The price the client displayed when the trader committed.
	QuotedPrice float64
	// The server's latest stored tick - the price that will actually fill.
	ServerPrice float64
	Direction   models.Direction
	// Maximum adverse move, in percent of the quoted price. 0 disables the guard.
	TolerancePercent float64
}

type SlippageAssessment struct {
	// False means the trade must be refused with PRICE_MOVED.
	Acceptable bool `json:"acceptable"`
	// Signed move as a percentage of the quoted price; positive means the price rose.
	MovePercent float64 `json:"movePercent"`
	// True when the move makes this direction worse for the customer.
	Adverse bool `json:"adverse"`
}

// AssessSlippage: is the move from QuotedPrice to ServerPrice acceptable for
// this direction? "Adverse" is direction-specific: a HIGHER entry is worse for
// UP, a LOWER entry is worse for DOWN.
func AssessSlippage(in SlippageInput) SlippageAssessment {
	// A non-positive quote is not a quote. Treat it as "no opinion" rather than
	// dividing by it.
	if math.IsNaN(in.QuotedPrice) || math.IsInf(in.QuotedPrice, 0) || in.QuotedPrice <= 0 {
		return SlippageAssessment{Acceptable: true, MovePercent: 0, Adverse: false}
	}

	movePercent := ((in.ServerPrice - in.QuotedPrice) / in.QuotedPrice) * 100
	adverse := movePercent < 0
	if in.Direction == models.DirectionUp {
		adverse = movePercent > 0
	}

	// Zero tolerance disables the guard entirely rather than refusing every
	// trade, restoring the behaviour from before the guard existed.
	if in.TolerancePercent <= 0 {
		return SlippageAssessment{Acceptable: true, MovePercent: movePercent, Adverse: adverse}
	}
	if !adverse {
		return SlippageAssessment{Acceptable: true, MovePercent: movePercent, Adverse: false}
	}
	return SlippageAssessment{
		Acceptable:  math.Abs(movePercent) <= in.TolerancePercent,
		MovePercent: movePercent,
		Adverse:     true,
	}
}
