package risk

import (
	"context"
	"errors"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/riskconfig"
)

// ReservationRequest describes a LIVE trade about to be accepted. DEMO trades
// never reach the house book and must not be reserved.
type ReservationRequest struct {
	TradeID         string
	UserID          string
	Asset           *models.Asset
	Direction       models.Direction
	Stake           int64
	Profit          int64
	ExpiryTimestamp int64
	UserMaxExposure *int64
}

// How long a trade waits for the book to become READY (a rebuild running
// here or on another instance) before it is refused.
var bookReadyWait = 3 * time.Second

func bookUnavailable(detail string) error {
	return apperr.New(apperr.ServiceUnavailable,
		"Trading is temporarily unavailable while risk limits are verified. Please try again shortly.", 503,
		map[string]any{"reason": "EXPOSURE_BOOK_UNAVAILABLE", "detail": detail})
}

// ReservationLimits are the ceilings CheckTradeLimits applies, for the
// atomic check.
func ReservationLimits(cfg *models.RiskConfig, asset *models.Asset, userMaxExposure *int64) exposure.ReserveLimits {
	userLimit := cfg.MaxUserExposure
	if userMaxExposure != nil {
		userLimit = *userMaxExposure
	}
	return exposure.ReserveLimits{
		UserExposure:         userLimit,
		AssetGross:           min(asset.ExposureLimit, cfg.MaxAssetExposure),
		AssetNet:             asset.NetExposureLimit,
		PlatformGross:        cfg.MaxGrossExposure,
		ConcentrationPercent: cfg.MaxUserAssetConcentrationPercent,
		ExpiryLiability:      cfg.MaxExpiryLiability,
		AssetLiability:       cfg.MaxAssetLiability,
		PlatformLiability:    cfg.MaxPlatformLiability,
	}
}

// ReserveTradeExposure is the authoritative pre-trade exposure check. In one
// atomic Redis step it checks every exposure limit against the committed book
// plus every reservation still in flight and holds the headroom under the
// trade id. Concurrent requests are therefore serialised: whatever their
// interleaving, the trades that get a reservation fit the limits together.
//
// The caller must Commit the reservation once the trade is written and
// Release it on any failure before that. Refusals:
//   - a limit: the rule's code (400) with every violated rule in details;
//   - the book is not exact (missing, being rebuilt, failed an integrity
//     check) or Redis is unreachable: 503 SERVICE_UNAVAILABLE. LIVE trading
//     fails closed - it is never accepted against unchecked exposure.
func ReserveTradeExposure(ctx context.Context, req ReservationRequest) (*exposure.Reservation, error) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	in := exposure.ReserveInput{
		TradeID:         req.TradeID,
		AssetID:         req.Asset.ID.Hex(),
		Symbol:          req.Asset.Symbol,
		UserID:          req.UserID,
		Direction:       req.Direction,
		Stake:           req.Stake,
		Profit:          req.Profit,
		ExpiryTimestamp: req.ExpiryTimestamp,
		Limits:          ReservationLimits(cfg, req.Asset, req.UserMaxExposure),
	}

	for attempt := 0; ; attempt++ {
		resv, out, err := exposure.Reserve(ctx, in)
		if err != nil {
			logx.Error("exposure reservation failed", "tradeId", req.TradeID, "err", err.Error())
			metrics.IncrementCounter("trades.rejected.book_unavailable", 1, 0)
			return nil, bookUnavailable("redis")
		}
		switch out.Status {
		case exposure.ReserveOK:
			return resv, nil
		case exposure.ReserveRefused:
			violations := make([]LimitViolation, 0, len(out.Rules))
			for _, rule := range out.Rules {
				violations = append(violations, violationFor(rule, req.Asset.Symbol))
			}
			first := violations[0]
			return nil, apperr.New(first.Code, first.Message, 400, map[string]any{"violations": violations})
		case exposure.ReserveConflict:
			// A fresh trade id is already in the book: never accept twice.
			return nil, apperr.ConflictErr(apperr.Conflict, "This trade was already processed")
		}

		// UNCERTAIN: the book is not exact. Try to make it exact (rebuild from
		// MongoDB, or wait for the rebuild already running) once, then refuse.
		if attempt > 0 {
			metrics.IncrementCounter("trades.rejected.book_unavailable", 1, 0)
			return nil, bookUnavailable(out.Detail)
		}
		logx.Warn("exposure book not ready for a LIVE trade", "detail", out.Detail)
		if err := exposure.EnsureBookReady(ctx, bookReadyWait); err != nil {
			if !errors.Is(err, exposure.ErrBookUnavailable) {
				logx.Error("exposure book rebuild failed", "err", err.Error())
			}
			metrics.IncrementCounter("trades.rejected.book_unavailable", 1, 0)
			return nil, bookUnavailable(out.Detail)
		}
	}
}
