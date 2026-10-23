package risk

import (
	"context"
	"math"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/marketdata"
)

// Hedge target: liability sensitivity of the short binary book
// (docs/audit/hedging/hedging-spec.md, sections 2-4; audit finding F-07).
//
// Each open LIVE trade is a cash-or-nothing option the platform has SOLD:
// an UP trade pays P = stake + profit if the expiry price is above the
// strike K (the entry price), a DOWN trade if it is below. With r = 0 over
// horizons of seconds to minutes, the value of the UP contract is
// P * N(d2) and its sensitivity to a relative price move is
//
//	dV / d ln S = P * phi(d2) / (sigma * sqrt(tau)),
//	d2          = ln(S / K) / (sigma * sqrt(tau)) - sigma * sqrt(tau) / 2
//
// (a DOWN contract has the same magnitude, opposite sign). The platform's
// liability therefore rises with S by
//
//	D = sum over trades of dir * P * phi(d2) / (sigma * sqrt(tau))   (minor units)
//
// and holding D of the underlying (long when D > 0) makes the book's value
// insensitive to small moves. D is the book delta-equivalent notional; the
// target is a capped fraction of it:
//
//	target = clamp(h * D, -cap, +cap)
//	h      = min(band.hedgeRatio + volatility boost, asset.targetHedgeRatio)
//	cap    = min(asset.maxHedgeNotional, liabilityMultiple * expiryLiability)
//
// Trades within the gamma window (risk config minSecondsToExpiryForHedge) are
// left out: their delta swings between 0 and P / (sigma sqrt(tau)) faster
// than an order can be placed, and their outcome is effectively decided.

// HedgeBook is the book data the target needs. Nil on RiskInputs means "not
// gathered" and yields a zero target.
type HedgeBook struct {
	Legs []models.HedgeLeg
	// Platform reference price S of the underlying.
	Price float64
	// Volatility per sqrt(second), and its source.
	Sigma       float64
	SigmaSource string
	NowMs       int64
	// Hedge never exceeds this multiple of the asset's expiry liability (0 =
	// no such cap).
	LiabilityMultiple float64
}

// SigmaSourceFunc returns the volatility per sqrt(second) of an asset.
type SigmaSourceFunc func(ctx context.Context, assetID string) (sigma float64, source string, ok bool)

// HedgeSigmaSource, when set at startup, supplies the pricing engine's
// volatility estimate (the integrator wires it); otherwise the realised
// volatility of the tick history is used.
var HedgeSigmaSource SigmaSourceFunc

// HedgeAvailability, when set (the hedging package installs it), says whether
// a hedge can be placed for the asset now; a required but unavailable hedge
// adds a reason to the assessment.
var HedgeAvailability func(ctx context.Context, assetID string) (available bool, reason string)

const invSqrt2Pi = 0.3989422804014327

// NormPDF is the standard normal density.
func NormPDF(x float64) float64 { return invSqrt2Pi * math.Exp(-x*x/2) }

// LegDelta is one trade's delta-equivalent notional (minor units; + means the
// platform needs to be long). eligible is false for a trade inside the gamma
// window, past expiry, or with unusable inputs.
func LegDelta(leg models.HedgeLeg, price, sigma float64, nowMs, gammaWindowSec int64) (float64, bool) {
	tau := float64(leg.ExpiryMs-nowMs) / 1000
	if tau <= 0 || tau < float64(gammaWindowSec) || !(price > 0) || !(leg.Strike > 0) || !(sigma > 0) || leg.Payout <= 0 {
		return 0, false
	}
	v := sigma * math.Sqrt(tau)
	d2 := math.Log(price/leg.Strike)/v - v/2
	delta := float64(leg.Dir) * float64(leg.Payout) * NormPDF(d2) / v
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return 0, false
	}
	return delta, true
}

// BookDelta sums LegDelta over the book.
func BookDelta(legs []models.HedgeLeg, price, sigma float64, nowMs, gammaWindowSec int64) (delta float64, eligible, excluded int64) {
	for _, l := range legs {
		d, ok := LegDelta(l, price, sigma, nowMs, gammaWindowSec)
		if !ok {
			excluded++
			continue
		}
		eligible++
		delta += d
	}
	return delta, eligible, excluded
}

// HedgeRatioFor is h: the band's ratio plus the volatility boost, never
// above the asset's ceiling (asset.targetHedgeRatio) or 1. This replaces the
// earlier band x asset x 2 scaling.
func HedgeRatioFor(band *models.HedgeLevel, volBoost, assetCeiling float64) float64 {
	if band == nil || band.HedgeRatio <= 0 {
		return 0
	}
	h := math.Min(1, math.Max(0, band.HedgeRatio+volBoost))
	return math.Min(h, math.Min(1, math.Max(0, assetCeiling)))
}

// CapTarget clamps a raw target to +-cap and says which cap bound.
func CapTarget(raw float64, maxNotional int64, multiple float64, liability int64) (target, capValue int64, cappedBy string) {
	capValue, cappedBy = max(0, maxNotional), "MAX_HEDGE_NOTIONAL"
	if multiple > 0 {
		if byLiability := int64(util.RoundHalfUp(multiple * float64(max(0, liability)))); byLiability < capValue {
			capValue, cappedBy = byLiability, "LIABILITY_MULTIPLE"
		}
	}
	r := int64(util.RoundHalfUp(raw))
	switch {
	case r > capValue:
		return capValue, capValue, cappedBy
	case r < -capValue:
		return -capValue, capValue, cappedBy
	}
	return r, capValue, ""
}

// loadHedgeLegs reads the open LIVE trades of an asset. OPEN only: a SETTLING
// trade is past its expiry and has no delta left.
func loadHedgeLegs(ctx context.Context, assetID bson.ObjectID) ([]models.HedgeLeg, error) {
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
		bson.M{"assetId": assetID, "status": string(models.TradeStatusOpen), "accountType": models.NotDemo()},
		options.Find().SetProjection(bson.M{"direction": 1, "stake": 1, "profit": 1, "entryPrice": 1, "expiryTimestamp": 1}).
			SetSort(bson.D{{Key: "expiryTimestamp", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Direction       models.Direction `bson:"direction"`
		Stake           int64            `bson:"stake"`
		Profit          int64            `bson:"profit"`
		EntryPrice      float64          `bson:"entryPrice"`
		ExpiryTimestamp int64            `bson:"expiryTimestamp"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	legs := make([]models.HedgeLeg, 0, len(rows))
	for _, r := range rows {
		dir := int8(-1)
		if r.Direction == models.DirectionUp {
			dir = 1
		}
		legs = append(legs, models.HedgeLeg{Dir: dir, Payout: r.Stake + r.Profit, Strike: r.EntryPrice, ExpiryMs: r.ExpiryTimestamp})
	}
	return legs, nil
}

// realisedSigmaPerSecond rescales the per-tick realised volatility by the
// observed tick rate (as the early-close quote does), so a fast feed does not
// look more volatile than a slow one.
func realisedSigmaPerSecond(ctx context.Context, assetID string, perTick float64) (float64, error) {
	if perTick <= 0 {
		return 0, nil
	}
	window := int64(marketdata.DefaultVolatilityWindowMs)
	ticks, err := redisx.R().ZCount(ctx, config.RedisKeys.TickHistory(assetID),
		strconv.FormatInt(util.NowMs()-window, 10), "+inf").Result()
	if err != nil {
		return 0, err
	}
	perSecond := math.Max(0.1, float64(ticks)/(float64(window)/1000))
	return perTick * math.Sqrt(perSecond), nil
}

// hedgeSigma is the delta's volatility: the installed source when it has an
// estimate, else the realised one.
func hedgeSigma(ctx context.Context, assetID string, perTick float64) (float64, string, error) {
	if HedgeSigmaSource != nil {
		if s, src, ok := HedgeSigmaSource(ctx, assetID); ok && s > 0 {
			return s, src, nil
		}
	}
	s, err := realisedSigmaPerSecond(ctx, assetID, perTick)
	return s, "realised", err
}

func hedgeLiabilityMultiple() float64 {
	if config.E != nil {
		return config.E.HedgeMaxLiabilityMultiple
	}
	return 0
}
