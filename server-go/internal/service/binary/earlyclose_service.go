package binary

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/settlement"
)

// Early close ("Sell now").
//
// A customer can hand an open position back before expiry for what it is
// currently worth. The price is computed here, from the server's own tick
// data - never taken from the request - and only from a feed that is
// tradable right now: the last tick must pass the same feed-health gate as a
// new trade and be fresh (policy.EarlyCloseMaxTickAgeMs). A position cannot
// be valued without realised volatility, so a zero/unknown volatility refuses
// the close rather than pricing at the 1%/99% clamps.
//
// Quote validity. GET /quote records the quote it showed (Redis, TTL =
// policy.EarlyCloseQuoteMaxAgeMs). The close re-prices on a fresh tick and
// requires that the customer was shown a quote within that window, and that
// the fresh value is not more than policy.EarlyCloseQuoteTolerancePercent
// below it; otherwise it is refused (PRICE_MOVED) and the customer sees the
// new price. A better fresh value is paid as is.
//
// Lifecycle. The close goes through the settlement state machine:
// settlement.ClaimEarlyClose moves OPEN -> SETTLING together with the
// immutable outcome, conditional on expiry being more than the floor away by
// the database clock (so it can never happen at or after expiry and can never
// race an expiry settlement into two outcomes), and settlement.Execute books
// it - one wallet movement, one settlement row, CLOSED_EARLY. A crash after
// the claim is finished by the settlement worker as CLOSED_EARLY, never as a
// draw or a second settlement.

// Volatility is measured over this window and converted to a per-second figure.
const volatilityWindowMs = 60_000

type EarlyCloseQuoteView struct {
	EarlyCloseQuote
	TradeID           string  `json:"tradeId"`
	CurrentPrice      float64 `json:"currentPrice"`
	SecondsRemaining  int64   `json:"secondsRemaining"`
	Available         bool    `json:"available"`
	UnavailableReason *string `json:"unavailableReason"`
}

// issuedQuoteKey holds the last quote shown for a trade.
func issuedQuoteKey(tradeID string) string { return "ec:quote:" + tradeID }

// issuedQuote is what GET /quote showed the customer.
type issuedQuote struct {
	UserID        string  `json:"userId"`
	Value         int64   `json:"value"`
	Price         float64 `json:"price"`
	TickTimestamp int64   `json:"tickTimestamp"`
	IssuedAt      int64   `json:"issuedAt"`
}

// earlyClosePricing is the shared valuation of QuoteTrade and CloseTradeEarly.
type earlyClosePricing struct {
	cfg              *models.RiskConfig
	policy           models.SettlementPolicy
	floorMs          int64
	tick             *models.NormalisedTick
	feedStatus       models.FeedStatus
	feedAgeMs        int64
	vol              float64
	secondsRemaining float64
	currentPrice     float64
	quote            EarlyCloseQuote
	// First reason the trade cannot be closed now, and the error the close
	// path returns for it (both nil when it can).
	reason *string
	err    error
}

func (p *earlyClosePricing) refuse(reason string, err error) {
	if p.reason == nil {
		p.reason, p.err = &reason, err
	}
}

// priceEarlyClose values a trade now and decides whether it may be closed.
func priceEarlyClose(ctx context.Context, trade *models.BinaryTrade, now int64) (*earlyClosePricing, error) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	policy, err := settlement.PolicyFor(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p := &earlyClosePricing{cfg: cfg, policy: policy, floorMs: settlement.EarlyCloseFloorMsFor(cfg, policy)}

	if !cfg.EarlyCloseEnabled {
		p.refuse("Closing early is currently disabled",
			apperr.Unavailable(apperr.ServiceUnavailable, "Closing early is currently disabled"))
	}
	if trade.Status != models.TradeStatusOpen {
		// JS String.replace with a string pattern replaces the first match only.
		p.refuse("This trade is already "+strings.Replace(strings.ToLower(string(trade.Status)), "_", " ", 1),
			apperr.ConflictErr(apperr.AlreadySettled, "This trade is no longer open"))
	}
	if cfg.TradingHalted {
		p.refuse("Trading is currently halted",
			apperr.Unavailable(apperr.TradingHalted, "Trading is currently halted. Closing early is paused."))
	}
	remainingMs := trade.ExpiryTimestamp - now
	if remainingMs < p.floorMs {
		p.refuse("Too close to expiry to close early",
			apperr.ConflictErr(apperr.Conflict, "Too close to expiry to close early. It will settle on its own shortly."))
	}
	p.secondsRemaining = math.Max(0, float64(remainingMs)/1000)

	assetHex := trade.AssetID.Hex()
	tick, err := marketdata.GetLastTick(ctx, assetHex)
	if err != nil {
		return nil, err
	}
	p.tick = tick
	p.currentPrice = trade.EntryPrice
	if tick == nil {
		p.feedStatus = models.FeedStatusOffline
		p.refuse("No live price is available for this asset",
			apperr.Unavailable(apperr.NoPriceAvailable, "No live price is available for this asset"))
	} else {
		p.currentPrice = tick.Price
		p.feedAgeMs = now - tick.ReceivedAt
		// Classified from this very tick, so the gate and the price agree.
		p.feedStatus = marketdata.ClassifyFeed(marketdata.FeedClassificationInput{
			AgeMs: p.feedAgeMs, LatencyMs: tick.TotalLatencyMs,
			HealthyMaxMs: cfg.FeedHealthyMaxMs, DegradedMaxMs: cfg.FeedDegradedMaxMs,
			StaleMaxMs: cfg.FeedStaleMaxMs, TickMaxAgeMs: cfg.FeedTickMaxAgeMs,
		})
		blocked := p.feedStatus == models.FeedStatusOffline ||
			(cfg.HaltTradingOnStaleFeed && p.feedStatus == models.FeedStatusStale) ||
			(cfg.HaltTradingOnDegradedFeed && p.feedStatus == models.FeedStatusDegraded)
		if blocked {
			msg := "Market data for " + trade.Symbol + " is " + string(p.feedStatus) + ". Closing early is paused."
			p.refuse(msg, apperr.Unavailable(apperr.FeedUnhealthy, msg))
		}
		if p.feedAgeMs > policy.EarlyCloseMaxTickAgeMs {
			msg := "The live price for " + trade.Symbol + " is stale. Closing early is paused."
			p.refuse(msg, apperr.Unavailable(apperr.FeedUnhealthy, msg))
		}
	}

	p.vol, err = volatilityPerSecond(ctx, assetHex)
	if err != nil {
		return nil, err
	}
	if p.vol <= 0 && p.secondsRemaining > 0 {
		msg := "Not enough recent market data to value this position. Try again shortly."
		p.refuse("Not enough recent market data to value this position",
			apperr.Unavailable(apperr.NoPriceAvailable, msg))
	}

	p.quote = QuoteEarlyClose(EarlyCloseInput{
		Stake:               trade.Stake,
		PayoutPercent:       trade.PayoutPercent,
		Direction:           trade.Direction,
		EntryPrice:          trade.EntryPrice,
		CurrentPrice:        p.currentPrice,
		SecondsRemaining:    p.secondsRemaining,
		VolatilityPerSecond: p.vol,
		FeePercent:          cfg.EarlyCloseFeePercent,
	})
	return p, nil
}

// QuoteTrade prices a position without closing it, so the button can show a
// live figure. Never fails for an un-closeable trade: the screen needs to
// explain why the button is disabled. A closeable quote is recorded as the
// one the customer was shown (Redis only).
func QuoteTrade(ctx context.Context, tradeID, userID string) (EarlyCloseQuoteView, error) {
	trade, err := loadOwnTrade(ctx, tradeID, userID)
	if err != nil {
		return EarlyCloseQuoteView{}, err
	}
	now := util.NowMs()
	p, err := priceEarlyClose(ctx, trade, now)
	if err != nil {
		return EarlyCloseQuoteView{}, err
	}

	if p.reason == nil {
		issued, _ := json.Marshal(issuedQuote{
			UserID: userID, Value: p.quote.Value, Price: p.tick.Price, TickTimestamp: p.tick.Timestamp, IssuedAt: now,
		})
		ttl := time.Duration(p.policy.EarlyCloseQuoteMaxAgeMs) * time.Millisecond
		if err := redisx.R().Set(ctx, issuedQuoteKey(tradeID), issued, ttl).Err(); err != nil {
			logx.Warn("failed to record the early-close quote", "tradeId", tradeID, "err", err.Error())
		}
	}

	return EarlyCloseQuoteView{
		EarlyCloseQuote:   p.quote,
		TradeID:           tradeID,
		CurrentPrice:      p.currentPrice,
		SecondsRemaining:  int64(util.RoundHalfUp(p.secondsRemaining)),
		Available:         p.reason == nil,
		UnavailableReason: p.reason,
	}, nil
}

// volatilityPerSecond rescales the per-tick realised volatility by the
// observed tick rate - otherwise a fast feed would look far more volatile
// than a slow one quoting the same instrument.
func volatilityPerSecond(ctx context.Context, assetID string) (float64, error) {
	perTick, err := marketdata.GetRealisedVolatility(ctx, assetID, volatilityWindowMs)
	if err != nil {
		return 0, err
	}
	if perTick <= 0 {
		return 0, nil
	}
	ticks, err := redisx.R().ZCount(ctx, config.RedisKeys.TickHistory(assetID),
		strconv.FormatInt(util.NowMs()-volatilityWindowMs, 10), "+inf").Result()
	if err != nil {
		return 0, err
	}
	ticksPerSecond := math.Max(0.1, float64(ticks)/(volatilityWindowMs/1000))
	return perTick * math.Sqrt(ticksPerSecond), nil
}

func loadOwnTrade(ctx context.Context, tradeID, userID string) (*models.BinaryTrade, error) {
	trade, err := findTrade(ctx, tradeID)
	if err != nil {
		return nil, err
	}
	// Not 403: confirming another customer's trade exists would leak it.
	if trade == nil || trade.UserID.Hex() != userID {
		return nil, tradeNotFound()
	}
	return trade, nil
}

func loadIssuedQuote(ctx context.Context, tradeID string) (*issuedQuote, error) {
	raw, err := redisx.R().Get(ctx, issuedQuoteKey(tradeID)).Result()
	if err != nil {
		if redisx.IsNil(err) {
			return nil, nil
		}
		return nil, err
	}
	var q issuedQuote
	if json.Unmarshal([]byte(raw), &q) != nil {
		return nil, nil
	}
	return &q, nil
}

// checkQuoteValidity enforces quote validity: the customer saw a quote for
// this trade recently, and the fresh value is within tolerance of it (only a
// worse price is bounded; a better one is simply paid).
func checkQuoteValidity(shown *issuedQuote, userID string, fresh int64, now int64, p models.SettlementPolicy) error {
	if shown == nil || shown.UserID != userID || now-shown.IssuedAt > p.EarlyCloseQuoteMaxAgeMs || now < shown.IssuedAt {
		return apperr.New(apperr.PriceMoved, "Your sell price has expired. Check the latest price and try again.", 409)
	}
	tolerance := int64(util.RoundHalfUp(float64(shown.Value) * p.EarlyCloseQuoteTolerancePercent / 100))
	if fresh < shown.Value-tolerance {
		return apperr.New(apperr.PriceMoved, "The sell price has moved. Check the new price and try again.", 409,
			map[string]any{"quotedValue": shown.Value, "currentValue": fresh})
	}
	return nil
}

type EarlyCloseResult struct {
	Trade models.BinaryTradeView `json:"trade"`
	Quote EarlyCloseQuote        `json:"quote"`
}

// CloseTradeEarly buys the position back.
func CloseTradeEarly(ctx context.Context, tradeID, userID string) (EarlyCloseResult, error) {
	ctx = context.WithoutCancel(ctx)
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	if !cfg.EarlyCloseEnabled {
		return EarlyCloseResult{}, apperr.Unavailable(apperr.ServiceUnavailable, "Closing early is currently disabled")
	}

	existing, err := loadOwnTrade(ctx, tradeID, userID)
	if err != nil {
		return EarlyCloseResult{}, err
	}

	// Re-price now, on a fresh tick, and refuse if anything about the market
	// or the trade rules the close out.
	now := util.NowMs()
	p, err := priceEarlyClose(ctx, existing, now)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	if p.err != nil {
		return EarlyCloseResult{}, p.err
	}
	shown, err := loadIssuedQuote(ctx, tradeID)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	if err := checkQuoteValidity(shown, userID, p.quote.Value, now, p.policy); err != nil {
		return EarlyCloseResult{}, err
	}

	assetHex := existing.AssetID.Hex()
	ev := models.SettlementEvidence{
		Rule:              models.SettlementRuleEarlyCloseAtMarket,
		Kind:              models.SettlementKindEarlyClose,
		ExpiryTimestamp:   existing.ExpiryTimestamp,
		DecidedAt:         now,
		RiskConfigVersion: p.cfg.Version,
		Policy:            p.policy,
		EarlyClose: &models.EarlyCloseEvidence{
			QuotedValue:         &shown.Value,
			QuotedAt:            &shown.IssuedAt,
			QuotedPrice:         &shown.Price,
			Value:               p.quote.Value,
			WinProbability:      p.quote.WinProbability,
			VolatilityPerSecond: p.vol,
			SecondsRemaining:    p.secondsRemaining,
			FeePercent:          p.cfg.EarlyCloseFeePercent,
			PayoutPercent:       existing.PayoutPercent,
			FeedStatus:          p.feedStatus,
			FeedAgeMs:           p.feedAgeMs,
		},
	}
	settlement.EvidenceFromTick(&ev, p.tick, now, settlement.FeedProvider(ctx, assetHex), models.SettlementEvidenceSourceTick)
	price := p.tick.Price
	intent := settlement.EarlyCloseIntent(existing, p.quote.Value, &price, ev, "customer.early_close")

	claimed, err := settlement.ClaimEarlyClose(ctx, existing.ID, existing.UserID, &intent, p.floorMs)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	if claimed == nil {
		current, err := findTrade(ctx, tradeID)
		if err != nil {
			return EarlyCloseResult{}, err
		}
		if current == nil || current.Status != models.TradeStatusOpen {
			return EarlyCloseResult{}, apperr.ConflictErr(apperr.AlreadySettled, "This trade is no longer open")
		}
		return EarlyCloseResult{}, apperr.ConflictErr(apperr.Conflict,
			"Too close to expiry to close early. It will settle on its own shortly.")
	}
	_ = redisx.R().Del(ctx, issuedQuoteKey(tradeID)).Err()

	if _, err := settlement.Execute(ctx, claimed); err != nil {
		// The outcome is recorded with the claim; a settlement worker will
		// finish booking it as CLOSED_EARLY. Hand it one now.
		logx.Error("early close booked but not finished - handing to settlement", "tradeId", tradeID, "err", err.Error())
		_ = redisx.R().LPush(ctx, config.RedisKeys.SettlementQueue, tradeID).Err()
		return EarlyCloseResult{}, err
	}

	final, err := findTrade(ctx, tradeID)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	if final == nil {
		return EarlyCloseResult{}, tradeNotFound()
	}
	logx.Info("position closed early",
		"tradeId", tradeID, "symbol", final.Symbol, "value", p.quote.Value, "quoted", shown.Value, "stake", final.Stake)
	return EarlyCloseResult{Trade: models.ToTradeView(final), Quote: p.quote}, nil
}
