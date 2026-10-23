package binary

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/wallets"
)

// Administrative cancellation.
//
// A cancel is a small state machine on the trade:
//
//	OPEN --claim--> CANCELLED{cancellation: fundsReleased=false, exposureReleased=false}
//	     --release the stake (ledger key trade:settle:<id>)--> fundsReleased=true
//	     --drop from the expiry queue, release exposure (per-trade, idempotent)--> exposureReleased=true
//
// The claim is one conditional update, so a trade is cancelled once and never
// also settled. Every later step is idempotent and its completion is recorded
// on the trade, so a cancel interrupted between the claim and the releases
// (the wallet unavailable, a crash) is finished by repeating the cancel or by
// RepairCancelledTrades (at boot and from the admin console), and the
// reconciliation check "cancelled trades released" flags it until then. The
// stake can never stay locked unnoticed.

// releaseCancelledFunds is wallets.ReleaseTradeFunds (tests replace it).
var releaseCancelledFunds = wallets.ReleaseTradeFunds

// CancelTrade cancels an OPEN trade and returns the stake in full - a
// cancelled trade never produces a win or a loss for either side. Repeating
// it on a CANCELLED trade is not an error: it finishes whatever an earlier
// attempt left undone and returns the trade. When the trade was cancelled
// but a release step failed, the view of the (CANCELLED) trade is returned
// together with the error, so the caller can still audit the cancel.
func CancelTrade(ctx context.Context, tradeID, reason string) (models.BinaryTradeView, error) {
	ctx = context.WithoutCancel(ctx)
	id, ok := mongox.ParseID(tradeID)
	if !ok {
		return models.BinaryTradeView{}, tradeNotFound()
	}
	now := mongox.Now()
	var trade models.BinaryTrade
	err := mongox.C(models.CollBinaryTrades).FindOneAndUpdate(ctx,
		bson.M{"_id": id, "status": string(models.TradeStatusOpen)},
		bson.M{"$set": bson.M{
			"status":           string(models.TradeStatusCancelled),
			"cancelReason":     reason,
			"settledAt":        now,
			"settlementAmount": 0,
			"platformResult":   0,
			"cancellation":     models.TradeCancellation{RequestedAt: now},
			"updatedAt":        now,
		}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&trade)
	if err != nil {
		if !mongox.IsNotFound(err) {
			return models.BinaryTradeView{}, err
		}
		existing, err := findTrade(ctx, tradeID)
		if err != nil {
			return models.BinaryTradeView{}, err
		}
		if existing == nil {
			return models.BinaryTradeView{}, tradeNotFound()
		}
		if existing.Status != models.TradeStatusCancelled {
			return models.BinaryTradeView{}, apperr.ConflictErr(apperr.Conflict,
				"Only an open trade can be cancelled (status is "+string(existing.Status)+")")
		}
		// Already cancelled: finish it (idempotent).
		trade = *existing
	}
	if err := completeCancellation(ctx, &trade); err != nil {
		return models.ToTradeView(&trade), err
	}
	view := models.ToTradeView(&trade)
	ws.Emit([]string{config.ChUser(view.UserID), config.ChAdminOps}, config.EvTradeUpdated, view)
	return view, nil
}

// cancellationDone reports whether every step of a cancel has completed.
func cancellationDone(t *models.BinaryTrade) bool {
	c := t.Cancellation
	return c != nil && c.FundsReleased && c.ExposureReleased
}

// completeCancellation runs the steps of a CANCELLED trade that have not
// completed yet; trade is updated in place with what was recorded.
func completeCancellation(ctx context.Context, trade *models.BinaryTrade) error {
	if trade.Status != models.TradeStatusCancelled || cancellationDone(trade) {
		return nil
	}
	coll := mongox.C(models.CollBinaryTrades)
	tradeHex, userHex := trade.ID.Hex(), trade.UserID.Hex()
	// A trade cancelled before the steps were recorded replays them; each
	// step is a no-op where it already happened.
	if trade.Cancellation == nil {
		trade.Cancellation = &models.TradeCancellation{RequestedAt: trade.UpdatedAt}
	}
	c := trade.Cancellation

	if !c.FundsReleased {
		_, err := releaseCancelledFunds(ctx, wallets.TradeFundsInput{
			UserID: userHex, AccountType: trade.AccountType, Currency: trade.Currency, Stake: trade.Stake, TradeID: tradeHex,
		}, wallets.ReleaseCancelled)
		if err != nil {
			c.Attempts++
			c.LastError = err.Error()
			if _, uerr := coll.UpdateOne(ctx, bson.M{"_id": trade.ID}, bson.M{
				"$set": bson.M{"cancellation.requestedAt": c.RequestedAt, "cancellation.lastError": c.LastError, "updatedAt": mongox.Now()},
				"$inc": bson.M{"cancellation.attempts": 1},
			}); uerr != nil {
				logx.Error("failed to record a cancel attempt", "tradeId", tradeHex, "err", uerr.Error())
			}
			logx.Error("cancelled trade: stake release failed - retried by the repair, flagged by reconciliation",
				"tradeId", tradeHex, "err", err.Error())
			return err
		}
		at := mongox.Now()
		c.FundsReleased, c.FundsReleasedAt, c.LastError = true, &at, ""
		if _, err := coll.UpdateOne(ctx, bson.M{"_id": trade.ID}, bson.M{"$set": bson.M{
			"cancellation.requestedAt": c.RequestedAt, "cancellation.fundsReleased": true,
			"cancellation.fundsReleasedAt": at, "cancellation.lastError": "", "updatedAt": at,
		}}); err != nil {
			return err
		}
	}

	if !c.ExposureReleased {
		if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeHex).Err(); err != nil {
			return err
		}
		// Per trade and idempotent: a repeated release is a no-op, so a retry
		// can never take the trade off the book twice. DEMO trades never
		// entered it.
		if models.NormAccountType(trade.AccountType) == models.AccountLive {
			if _, err := exposure.ApplyExposureDelta(ctx, exposure.ExposureDelta{
				AssetID: trade.AssetID.Hex(), Symbol: trade.Symbol, UserID: userHex, Direction: trade.Direction,
				Stake: trade.Stake, Profit: trade.Profit, Sign: -1, TradeID: tradeHex, ExpiryTimestamp: trade.ExpiryTimestamp,
			}); err != nil {
				return err
			}
		}
		at := mongox.Now()
		c.ExposureReleased, c.ExposureReleasedAt = true, &at
		if _, err := coll.UpdateOne(ctx, bson.M{"_id": trade.ID}, bson.M{"$set": bson.M{
			"cancellation.exposureReleased": true, "cancellation.exposureReleasedAt": at, "updatedAt": at,
		}}); err != nil {
			return err
		}
	}
	return nil
}

// CancelRepairResult reports one RepairCancelledTrades pass.
type CancelRepairResult struct {
	Scanned  int64    `json:"scanned"`
	Repaired int64    `json:"repaired"`
	Failed   int64    `json:"failed"`
	TradeIDs []string `json:"tradeIds"`
}

// cancelRepairBatch bounds one pass.
const cancelRepairBatch = 200

// RepairCancelledTrades finishes every cancel whose stake or exposure
// release did not complete, and records the steps on cancels made before
// they were recorded. Safe to run at any time, on any instance, concurrently
// with CancelTrade.
func RepairCancelledTrades(ctx context.Context) (CancelRepairResult, error) {
	ctx = context.WithoutCancel(ctx)
	out := CancelRepairResult{TradeIDs: []string{}}
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{
		"status": string(models.TradeStatusCancelled),
		"$or": bson.A{
			bson.M{"cancellation.fundsReleased": bson.M{"$ne": true}},
			bson.M{"cancellation.exposureReleased": bson.M{"$ne": true}},
		},
	}, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: 1}}).SetLimit(cancelRepairBatch))
	if err != nil {
		return out, err
	}
	var trades []models.BinaryTrade
	if err := cur.All(ctx, &trades); err != nil {
		return out, err
	}
	for i := range trades {
		out.Scanned++
		t := &trades[i]
		if err := completeCancellation(ctx, t); err != nil {
			out.Failed++
			continue
		}
		out.Repaired++
		out.TradeIDs = append(out.TradeIDs, t.ID.Hex())
	}
	if out.Scanned > 0 {
		logx.Info("cancelled-trade repair pass", "scanned", out.Scanned, "repaired", out.Repaired, "failed", out.Failed)
	}
	return out, nil
}

// cancelRepairTimeout bounds the boot-time pass.
const cancelRepairTimeout = 30 * time.Second

// RepairCancelledTradesAtBoot runs one bounded pass. A failure is logged,
// never fatal: the reconciliation check keeps flagging what is left.
func RepairCancelledTradesAtBoot(ctx context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelRepairTimeout)
	defer cancel()
	if _, err := RepairCancelledTrades(ctx); err != nil {
		logx.Warn("cancelled-trade repair at boot failed", "err", err.Error())
	}
}
