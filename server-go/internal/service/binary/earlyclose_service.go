package binary

import (
	"context"
	"math"
	"strconv"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
)

// Early close ("Sell now").
//
// A customer can hand an open position back before expiry for what it is
// currently worth. The price is computed here, from the server's own tick
// data - never taken from the request - and the buy-back settles through the
// same single-settlement guarantees as a normal expiry.

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

// QuoteTrade prices a position without closing it, so the button can show a
// live figure. Never fails for an un-closeable trade: the screen needs to
// explain why the button is disabled.
func QuoteTrade(ctx context.Context, tradeID, userID string) (EarlyCloseQuoteView, error) {
	trade, err := loadOwnTrade(ctx, tradeID, userID)
	if err != nil {
		return EarlyCloseQuoteView{}, err
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return EarlyCloseQuoteView{}, err
	}

	secondsRemaining := math.Max(0, float64(trade.ExpiryTimestamp-util.NowMs())/1000)
	assetHex := trade.AssetID.Hex()
	tick, err := marketdata.GetLastTick(ctx, assetHex)
	if err != nil {
		return EarlyCloseQuoteView{}, err
	}

	unavailable := describeUnavailability(trade, cfg, secondsRemaining, tick != nil)
	currentPrice := trade.EntryPrice
	if tick != nil {
		currentPrice = tick.Price
	}

	vol, err := volatilityPerSecond(ctx, assetHex)
	if err != nil {
		return EarlyCloseQuoteView{}, err
	}
	quote := QuoteEarlyClose(EarlyCloseInput{
		Stake:               trade.Stake,
		PayoutPercent:       trade.PayoutPercent,
		Direction:           trade.Direction,
		EntryPrice:          trade.EntryPrice,
		CurrentPrice:        currentPrice,
		SecondsRemaining:    secondsRemaining,
		VolatilityPerSecond: vol,
		FeePercent:          cfg.EarlyCloseFeePercent,
	})

	return EarlyCloseQuoteView{
		EarlyCloseQuote:   quote,
		TradeID:           tradeID,
		CurrentPrice:      currentPrice,
		SecondsRemaining:  int64(util.RoundHalfUp(secondsRemaining)),
		Available:         unavailable == nil,
		UnavailableReason: unavailable,
	}, nil
}

func describeUnavailability(trade *models.BinaryTrade, cfg *models.RiskConfig, secondsRemaining float64, hasPrice bool) *string {
	if !cfg.EarlyCloseEnabled {
		return models.Str("Closing early is currently disabled")
	}
	if trade.Status != models.TradeStatusOpen {
		// JS String.replace with a string pattern replaces the first match only.
		return models.Str("This trade is already " + strings.Replace(strings.ToLower(string(trade.Status)), "_", " ", 1))
	}
	if !hasPrice {
		return models.Str("No live price is available for this asset")
	}
	if secondsRemaining < float64(cfg.MinSecondsRemainingForEarlyClose) {
		return models.Str("Too close to expiry to close early")
	}
	return nil
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

type EarlyCloseResult struct {
	Trade models.BinaryTradeView `json:"trade"`
	Quote EarlyCloseQuote        `json:"quote"`
}

// CloseTradeEarly buys the position back.
//
// The claim, the wallet movement and the settlement row use exactly the same
// guards as an expiry settlement, so a trade cannot be both closed early and
// settled at expiry - whichever happens first wins and the other finds the
// trade already gone.
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
	if existing.Status != models.TradeStatusOpen {
		return EarlyCloseResult{}, apperr.ConflictErr(apperr.AlreadySettled, "This trade is no longer open")
	}

	secondsRemaining := float64(existing.ExpiryTimestamp-util.NowMs()) / 1000
	if secondsRemaining < float64(cfg.MinSecondsRemainingForEarlyClose) {
		return EarlyCloseResult{}, apperr.ConflictErr(apperr.Conflict,
			"Too close to expiry to close early. It will settle on its own shortly.")
	}

	assetHex := existing.AssetID.Hex()
	tick, err := marketdata.GetLastTick(ctx, assetHex)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	if tick == nil {
		return EarlyCloseResult{}, apperr.Unavailable(apperr.NoPriceAvailable, "No live price is available for this asset")
	}

	// Price first, then claim: the quote must come from the same tick the
	// customer is looking at, and claiming a trade we then refuse to price
	// would strand it.
	vol, err := volatilityPerSecond(ctx, assetHex)
	if err != nil {
		return EarlyCloseResult{}, err
	}
	quote := QuoteEarlyClose(EarlyCloseInput{
		Stake:               existing.Stake,
		PayoutPercent:       existing.PayoutPercent,
		Direction:           existing.Direction,
		EntryPrice:          existing.EntryPrice,
		CurrentPrice:        tick.Price,
		SecondsRemaining:    secondsRemaining,
		VolatilityPerSecond: vol,
		FeePercent:          cfg.EarlyCloseFeePercent,
	})

	trades := mongox.C(models.CollBinaryTrades)
	var claimed models.BinaryTrade
	err = trades.FindOneAndUpdate(ctx,
		bson.M{"_id": existing.ID, "status": string(models.TradeStatusOpen)},
		bson.M{"$set": bson.M{"status": string(models.TradeStatusSettling), "updatedAt": mongox.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&claimed)
	if err != nil {
		if mongox.IsNotFound(err) {
			return EarlyCloseResult{}, apperr.ConflictErr(apperr.AlreadySettled, "This trade is no longer open")
		}
		return EarlyCloseResult{}, err
	}

	platformResult := claimed.Stake - quote.Value

	// 1. Money, keyed so expiry settlement cannot also pay out.
	live := models.NormAccountType(claimed.AccountType) == models.AccountLive
	mutation, err := wallets.SettleTradeEarlyClose(ctx, wallets.TradeFundsInput{
		UserID: userID, AccountType: claimed.AccountType, Stake: claimed.Stake, TradeID: tradeID,
	}, quote.Value)
	if err != nil {
		return EarlyCloseResult{}, err
	}

	// 2. Settlement ledger row; the unique index is the final duplicate guard.
	var walletTxID *bson.ObjectID
	if mutation.TransactionID != "" {
		id, err := bson.ObjectIDFromHex(mutation.TransactionID)
		if err != nil {
			return EarlyCloseResult{}, err
		}
		walletTxID = &id
	}
	row := models.Settlement{
		TradeID:             claimed.ID,
		UserID:              claimed.UserID,
		AssetID:             claimed.AssetID,
		Symbol:              claimed.Symbol,
		Result:              models.TradeResultEarlyClose,
		AccountType:         models.NormAccountType(claimed.AccountType),
		EntryPrice:          claimed.EntryPrice,
		ExpiryPrice:         tick.Price,
		ExpiryTimestamp:     claimed.ExpiryTimestamp,
		Stake:               claimed.Stake,
		PayoutPercent:       claimed.PayoutPercent,
		PayoutAmount:        quote.Value,
		PlatformResult:      platformResult,
		Currency:            settlementCurrency(claimed.Currency),
		WalletTransactionID: walletTxID,
		SettledBy:           "customer.early_close",
		CreatedAt:           mongox.Now(),
	}
	if _, err := mongox.C(models.CollSettlements).InsertOne(ctx, row); err != nil {
		if !mongox.IsDuplicateKey(err) {
			return EarlyCloseResult{}, err
		}
		logx.Warn("settlement row already existed on early close", "tradeId", tradeID)
	}

	// 3. Final trade state.
	now := mongox.Now()
	var updated models.BinaryTrade
	err = trades.FindOneAndUpdate(ctx,
		bson.M{"_id": claimed.ID},
		bson.M{"$set": bson.M{
			"status":            string(models.TradeStatusClosedEarly),
			"result":            string(models.TradeResultEarlyClose),
			"expiryPrice":       tick.Price,
			"expiryPriceSource": "EARLY_CLOSE_AT_MARKET",
			"settlementAmount":  quote.CustomerResult,
			"platformResult":    platformResult,
			"settledAt":         now,
			"updatedAt":         now,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if err != nil {
		return EarlyCloseResult{}, err
	}

	// 4. Release the exposure and drop it from the expiry queue.
	if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err(); err != nil {
		return EarlyCloseResult{}, err
	}
	if live {
		if _, err := exposure.ApplyExposureDelta(ctx, exposure.ExposureDelta{
			AssetID:   assetHex,
			Symbol:    claimed.Symbol,
			UserID:    userID,
			Direction: claimed.Direction,
			Stake:     claimed.Stake,
			Profit:    claimed.Profit,
			Sign:      -1,
		}); err != nil {
			return EarlyCloseResult{}, err
		}
	}

	view := models.ToTradeView(&updated)
	ws.Emit([]string{config.ChUser(userID), config.ChAdminOps}, config.EvTradeSettled, view)

	metrics.IncrementCounter("trades.closed_early", 1, 0)
	if live {
		metrics.IncrementCounter("platform.gross_result", float64(platformResult), 0)
	}

	logx.Info("position closed early",
		"tradeId", tradeID, "symbol", claimed.Symbol, "value", quote.Value, "stake", claimed.Stake,
		"platformResult", platformResult)

	return EarlyCloseResult{Trade: view, Quote: quote}, nil
}

// settlementCurrency applies the Mongoose default ('INR') for a missing value.
func settlementCurrency(c string) string {
	if c == "" {
		return models.BinaryTradeDefaultCurrency
	}
	return c
}
