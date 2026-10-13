// Package settlement is the settlement engine (spec section 18).
//
// A trade settles exactly once. Three independent guards make that structural
// rather than hopeful:
//
//  1. the OPEN -> SETTLING claim is a conditional atomic update, so only one
//     worker can take a trade;
//  2. the wallet mutation is keyed `trade:settle:<id>` against a unique index,
//     so the money moves at most once no matter how often this runs;
//  3. `settlements.tradeId` is unique, so the ledger row cannot be duplicated.
//
// Everything is therefore safe to retry after a crash at any point.
package settlement

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/notifications"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
)

// StaleSettlingMs: a trade stuck in SETTLING longer than this is assumed to be
// an orphan.
const StaleSettlingMs = 30_000

// DefaultSettledBy is settleTrade's default settledBy.
const DefaultSettledBy = "settlement.worker"

// Outcome statuses.
const (
	StatusSettled          = "SETTLED"
	StatusAlreadySettled   = "ALREADY_SETTLED"
	StatusSkippedNoPrice   = "SKIPPED_NO_PRICE"
	StatusClaimedElsewhere = "CLAIMED_ELSEWHERE"
)

type Outcome struct {
	TradeID        string              `json:"tradeId"`
	Status         string              `json:"status"`
	Result         *models.TradeResult `json:"result,omitempty"`
	ExpiryPrice    *float64            `json:"expiryPrice,omitempty"`
	PayoutAmount   *int64              `json:"payoutAmount,omitempty"`
	PlatformResult *int64              `json:"platformResult,omitempty"`
}

// DetermineResult is the pure comparison rule (spec section 17).
func DetermineResult(direction models.Direction, entryPrice, expiryPrice float64) models.TradeResult {
	if expiryPrice == entryPrice {
		return models.TradeResultDraw
	}
	wentUp := expiryPrice > entryPrice
	if direction == models.DirectionUp {
		if wentUp {
			return models.TradeResultWin
		}
		return models.TradeResultLoss
	}
	if wentUp {
		return models.TradeResultLoss
	}
	return models.TradeResultWin
}

// ApplyDrawPolicy: REFUND returns the stake, LOSS and WIN treat an exact match
// as the corresponding outcome. Whichever is configured is disclosed to users:
// GET /assets sends it as drawPolicy, and the client words ties by it.
func ApplyDrawPolicy(result models.TradeResult, policy models.DrawPolicy) models.TradeResult {
	if result != models.TradeResultDraw {
		return result
	}
	switch policy {
	case models.DrawPolicyLoss:
		return models.TradeResultLoss
	case models.DrawPolicyWin:
		return models.TradeResultWin
	}
	return models.TradeResultDraw
}

// SettleTrade settles one trade; settledBy "" means DefaultSettledBy.
func SettleTrade(ctx context.Context, tradeID, settledBy string) (Outcome, error) {
	if settledBy == "" {
		settledBy = DefaultSettledBy
	}
	startedAt := time.Now()

	oid, err := bson.ObjectIDFromHex(tradeID)
	if err != nil {
		return Outcome{}, err
	}

	var existing models.Settlement
	err = mongox.C(models.CollSettlements).FindOne(ctx, bson.M{"tradeId": oid}).Decode(&existing)
	if err == nil {
		if err := ensureTradeFinalised(ctx, oid, tradeID, &existing); err != nil {
			return Outcome{}, err
		}
		r := existing.Result
		return Outcome{TradeID: tradeID, Status: StatusAlreadySettled, Result: &r}, nil
	}
	if !mongox.IsNotFound(err) {
		return Outcome{}, err
	}

	claimed, err := claimTrade(ctx, oid)
	if err != nil {
		return Outcome{}, err
	}
	if claimed == nil {
		return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
	}

	priceResult, err := marketdata.GetPriceAtOrBefore(ctx, claimed.AssetID.Hex(), claimed.ExpiryTimestamp)
	if err != nil {
		return Outcome{}, err
	}
	if priceResult == nil {
		// No price at all: leave the trade claimed and retry later rather than
		// settling on a guess. The stale-settling sweeper will pick it up again.
		logx.Error("no expiry price available - settlement deferred", "tradeId", tradeID, "symbol", claimed.Symbol)
		return Outcome{TradeID: tradeID, Status: StatusSkippedNoPrice}, nil
	}

	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return Outcome{}, err
	}
	rawResult := DetermineResult(claimed.Direction, claimed.EntryPrice, priceResult.Price)
	result := ApplyDrawPolicy(rawResult, cfg.DrawPolicy)

	outcome, err := applySettlement(ctx, claimed, result, priceResult.Price, priceResult.Source, settledBy, startedAt)
	if err != nil {
		return Outcome{}, err
	}
	metrics.Since(config.LatSettlement, startedAt)
	return outcome, nil
}

// claimTrade is the atomic claim. It also re-claims trades orphaned by a
// crashed worker. The claim refreshes updatedAt, which is what the staleness
// test reads.
func claimTrade(ctx context.Context, oid bson.ObjectID) (*models.BinaryTrade, error) {
	now := mongox.Now()
	staleBefore := time.UnixMilli(util.NowMs() - StaleSettlingMs).UTC()
	var claimed models.BinaryTrade
	err := mongox.C(models.CollBinaryTrades).FindOneAndUpdate(ctx,
		bson.M{
			"_id": oid,
			"$or": bson.A{
				bson.M{"status": string(models.TradeStatusOpen)},
				bson.M{"status": string(models.TradeStatusSettling), "updatedAt": bson.M{"$lt": staleBefore}},
			},
		},
		bson.M{"$set": bson.M{"status": string(models.TradeStatusSettling), "updatedAt": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&claimed)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &claimed, nil
}

func finalStatusFor(result models.TradeResult) models.TradeStatus {
	switch result {
	case models.TradeResultWin:
		return models.TradeStatusWon
	case models.TradeResultLoss:
		return models.TradeStatusLost
	}
	return models.TradeStatusDraw
}

func applySettlement(ctx context.Context, trade *models.BinaryTrade, result models.TradeResult, expiryPrice float64,
	priceSource, settledBy string, startedAt time.Time) (Outcome, error) {
	tradeID := trade.ID.Hex()
	userID := trade.UserID.Hex()

	// Platform accounting (spec section 41).
	//   WIN  - the stake is released and the profit is paid: the platform is
	//          out `profit`, the user receives `stake + profit` back.
	//   LOSS - the reserved stake leaves the wallet: the platform gains `stake`.
	//   DRAW - the stake is released untouched: neither side gains.
	var payoutAmount, platformResult, settlementAmount int64
	switch result {
	case models.TradeResultWin:
		payoutAmount, platformResult, settlementAmount = trade.Stake+trade.Profit, -trade.Profit, trade.Profit
	case models.TradeResultLoss:
		payoutAmount, platformResult, settlementAmount = 0, trade.Stake, -trade.Stake
	default:
		if result == models.TradeResultDraw {
			payoutAmount = trade.Stake
		}
	}

	// 1. Money first - keyed so it can only ever be applied once.
	live := models.NormAccountType(trade.AccountType) == models.AccountLive
	funds := wallets.TradeFundsInput{UserID: userID, AccountType: trade.AccountType, Stake: trade.Stake, TradeID: tradeID}
	var (
		mutation wallets.MutationResult
		err      error
	)
	switch result {
	case models.TradeResultWin:
		mutation, err = wallets.SettleTradeWin(ctx, funds, trade.Profit)
	case models.TradeResultLoss:
		mutation, err = wallets.SettleTradeLoss(ctx, funds)
	default:
		mutation, err = wallets.ReleaseTradeFunds(ctx, funds, wallets.ReleaseDraw)
	}
	if err != nil {
		return Outcome{}, err
	}

	// 2. Settlement ledger row - the unique index is the final duplicate guard.
	var walletTxID *bson.ObjectID
	if mutation.TransactionID != "" {
		id, err := bson.ObjectIDFromHex(mutation.TransactionID)
		if err != nil {
			return Outcome{}, err
		}
		walletTxID = &id
	}
	currency := trade.Currency
	if currency == "" {
		currency = models.BinaryTradeDefaultCurrency
	}
	latencyMs := int64(util.RoundHalfUp(float64(time.Since(startedAt).Microseconds()) / 1000))
	row := models.Settlement{
		TradeID:             trade.ID,
		UserID:              trade.UserID,
		AssetID:             trade.AssetID,
		Symbol:              trade.Symbol,
		Result:              result,
		AccountType:         models.NormAccountType(trade.AccountType),
		EntryPrice:          trade.EntryPrice,
		ExpiryPrice:         expiryPrice,
		ExpiryTimestamp:     trade.ExpiryTimestamp,
		Stake:               trade.Stake,
		PayoutPercent:       trade.PayoutPercent,
		PayoutAmount:        payoutAmount,
		PlatformResult:      platformResult,
		Currency:            currency,
		WalletTransactionID: walletTxID,
		SettledBy:           settledBy,
		LatencyMs:           &latencyMs,
		CreatedAt:           mongox.Now(),
	}
	if _, err := mongox.C(models.CollSettlements).InsertOne(ctx, row); err != nil {
		if mongox.IsDuplicateKey(err) {
			logx.Warn("settlement row already existed - treating as already settled", "tradeId", tradeID)
			r := result
			return Outcome{TradeID: tradeID, Status: StatusAlreadySettled, Result: &r}, nil
		}
		return Outcome{}, err
	}

	// 3. Final trade state.
	finalStatus := finalStatusFor(result)
	settledAt := mongox.Now()
	settlementLatencyMs := util.NowMs() - trade.ExpiryTimestamp
	var updated models.BinaryTrade
	err = mongox.C(models.CollBinaryTrades).FindOneAndUpdate(ctx,
		bson.M{"_id": trade.ID},
		bson.M{"$set": bson.M{
			"status":              string(finalStatus),
			"result":              string(result),
			"expiryPrice":         expiryPrice,
			"expiryPriceSource":   priceSource,
			"settlementAmount":    settlementAmount,
			"platformResult":      platformResult,
			"settledAt":           settledAt,
			"settlementLatencyMs": settlementLatencyMs,
			"updatedAt":           settledAt,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	var view models.BinaryTradeView
	switch {
	case err == nil:
		view = models.ToTradeView(&updated)
	case mongox.IsNotFound(err):
		fallback := *trade
		r, ep, sa := result, expiryPrice, settlementAmount
		fallback.Status, fallback.Result, fallback.ExpiryPrice, fallback.SettlementAmount = finalStatus, &r, &ep, &sa
		view = models.ToTradeView(&fallback)
	default:
		return Outcome{}, err
	}

	// 4. Release the exposure this trade contributed and drop it from the queue.
	if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err(); err != nil {
		return Outcome{}, err
	}
	// DEMO trades never entered the book.
	if live {
		if _, err := exposure.ApplyExposureDelta(ctx, exposure.ExposureDelta{
			AssetID:   trade.AssetID.Hex(),
			Symbol:    trade.Symbol,
			UserID:    userID,
			Direction: trade.Direction,
			Stake:     trade.Stake,
			Profit:    trade.Profit,
			Sign:      -1,
		}); err != nil {
			return Outcome{}, err
		}
	}

	ws.Emit([]string{config.ChUser(userID), config.ChAdminOps}, config.EvTradeSettled, view)

	// Fire and forget; CreateNotification emits notification.created itself.
	lower := strings.ToLower(string(result))
	notification := notifications.Input{
		Type:  "TRADE_SETTLED",
		Title: "Trade " + lower,
		Body:  trade.Symbol + " " + string(trade.Direction) + " settled as " + string(result),
		Data:  map[string]any{"tradeId": tradeID, "result": string(result), "payoutAmount": payoutAmount},
	}
	go func() {
		nctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := notifications.CreateNotification(nctx, userID, notification); err != nil {
			logx.Debug("failed to write settlement notification", "err", err.Error())
		}
	}()

	metrics.IncrementCounter("settlements.total", 1, 0)
	metrics.IncrementCounter("settlements."+lower, 1, 0)
	if live {
		metrics.IncrementCounter("platform.gross_result", float64(platformResult), 0)
		metrics.IncrementCounter("customer.payout", float64(payoutAmount), 0)
	}

	logx.Info("trade settled",
		"tradeId", tradeID, "symbol", trade.Symbol, "result", result, "entryPrice", trade.EntryPrice,
		"expiryPrice", expiryPrice, "platformResult", platformResult)

	r, ep, pa, pr := result, expiryPrice, payoutAmount, platformResult
	return Outcome{TradeID: tradeID, Status: StatusSettled, Result: &r, ExpiryPrice: &ep, PayoutAmount: &pa, PlatformResult: &pr}, nil
}

// ensureTradeFinalised repairs a trade whose settlement row exists but whose
// status never updated.
func ensureTradeFinalised(ctx context.Context, oid bson.ObjectID, tradeID string, s *models.Settlement) error {
	result := s.Result
	finalStatus := finalStatusFor(result)
	var settlementAmount int64
	switch result {
	case models.TradeResultWin:
		settlementAmount = s.PayoutAmount - s.Stake
	case models.TradeResultLoss:
		settlementAmount = -s.Stake
	}

	now := mongox.Now()
	_, err := mongox.C(models.CollBinaryTrades).UpdateOne(ctx,
		bson.M{"_id": oid, "status": bson.M{"$in": bson.A{string(models.TradeStatusOpen), string(models.TradeStatusSettling)}}},
		bson.M{"$set": bson.M{
			"status":           string(finalStatus),
			"result":           string(result),
			"expiryPrice":      s.ExpiryPrice,
			"platformResult":   s.PlatformResult,
			"settlementAmount": settlementAmount,
			"settledAt":        now,
			"updatedAt":        now,
		}})
	if err != nil {
		return err
	}
	return redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err()
}
