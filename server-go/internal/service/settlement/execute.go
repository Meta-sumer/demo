package settlement

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/notifications"
	"binarytrading/internal/service/wallets"
)

// Test seams. Production never sets testHook; notify is asynchronous.
var (
	// testHook runs at named points of Execute ("after_wallet", "after_row");
	// an error simulates the process dying there.
	testHook func(stage string) error

	notify = func(userID string, in notifications.Input) {
		go func() {
			nctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := notifications.CreateNotification(nctx, userID, in); err != nil {
				logx.Debug("failed to write settlement notification", "err", err.Error())
			}
		}()
	}

	writeAudit = audit.Write
)

func hook(stage string) error {
	if testHook != nil {
		return testHook(stage)
	}
	return nil
}

func redisZ(score int64, member string) redis.Z {
	return redis.Z{Score: float64(score), Member: member}
}

// SettleKey is the wallet idempotency key every outcome of a trade uses.
func SettleKey(tradeID string) string { return "trade:settle:" + tradeID }

func finalStatusFor(result models.TradeResult) models.TradeStatus {
	switch result {
	case models.TradeResultWin:
		return models.TradeStatusWon
	case models.TradeResultLoss:
		return models.TradeStatusLost
	case models.TradeResultEarlyClose:
		return models.TradeStatusClosedEarly
	}
	return models.TradeStatusDraw
}

// expiryIntent is the outcome of an expiry settlement. A nil price is a void
// (result DRAW, stake returned).
func expiryIntent(trade *models.BinaryTrade, result models.TradeResult, price *float64,
	ev models.SettlementEvidence, settledBy string) models.SettlementIntent {
	in := models.SettlementIntent{
		ID: uuid.NewString(), Kind: models.SettlementKindExpiry, Result: result, FinalStatus: finalStatusFor(result),
		ExpiryPrice: price, SettledBy: settledBy, Evidence: ev, CreatedAt: mongox.Now(),
	}
	// Platform accounting (spec section 41).
	//   WIN  - the stake is released and the profit is paid: the platform is
	//          out `profit`, the user receives `stake + profit` back.
	//   LOSS - the reserved stake leaves the wallet: the platform gains `stake`.
	//   DRAW - the stake is released untouched: neither side gains.
	switch result {
	case models.TradeResultWin:
		in.WalletOp, in.WalletAmount = models.SettlementWalletWin, trade.Profit
		in.PayoutAmount, in.PlatformResult, in.SettlementAmount = trade.Stake+trade.Profit, -trade.Profit, trade.Profit
	case models.TradeResultLoss:
		in.WalletOp, in.WalletAmount = models.SettlementWalletLoss, trade.Stake
		in.PayoutAmount, in.PlatformResult, in.SettlementAmount = 0, trade.Stake, -trade.Stake
	default:
		in.Result, in.FinalStatus = models.TradeResultDraw, models.TradeStatusDraw
		in.WalletOp, in.WalletAmount = models.SettlementWalletRefund, trade.Stake
		in.PayoutAmount, in.PlatformResult, in.SettlementAmount = trade.Stake, 0, 0
	}
	return in
}

// EarlyCloseIntent is the outcome of a customer close at value (minor units).
func EarlyCloseIntent(trade *models.BinaryTrade, value int64, price *float64,
	ev models.SettlementEvidence, settledBy string) models.SettlementIntent {
	return models.SettlementIntent{
		ID: uuid.NewString(), Kind: models.SettlementKindEarlyClose,
		Result: models.TradeResultEarlyClose, FinalStatus: models.TradeStatusClosedEarly,
		WalletOp: models.SettlementWalletEarlyClose, WalletAmount: value,
		ExpiryPrice: price, PayoutAmount: value, PlatformResult: trade.Stake - value, SettlementAmount: value - trade.Stake,
		SettledBy: settledBy, Evidence: ev, CreatedAt: mongox.Now(),
	}
}

func claimSet(intent *models.SettlementIntent, by string) bson.M {
	now := mongox.Now()
	return bson.M{"$set": bson.M{
		"status":           string(models.TradeStatusSettling),
		"settlementClaim":  models.SettlementClaim{Token: uuid.NewString(), Kind: intent.Kind, By: by, At: now},
		"settlementIntent": intent,
		"updatedAt":        now,
	}}
}

func claimOne(ctx context.Context, filter bson.M, intent *models.SettlementIntent, by string) (*models.BinaryTrade, error) {
	var claimed models.BinaryTrade
	err := mongox.C(models.CollBinaryTrades).FindOneAndUpdate(ctx, filter, claimSet(intent, by),
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&claimed)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &claimed, nil
}

// dbNowMs is the database clock in epoch ms, the one clock both claims
// compare expiry against, so instances with skewed clocks still agree.
var dbNowMs = bson.M{"$toLong": "$$NOW"}

// claimExpiry moves an OPEN trade to SETTLING with its expiry outcome - only
// once expiry has passed by the database clock.
func claimExpiry(ctx context.Context, tradeID bson.ObjectID, intent *models.SettlementIntent, by string) (*models.BinaryTrade, error) {
	return claimOne(ctx, bson.M{
		"_id":    tradeID,
		"status": string(models.TradeStatusOpen),
		"$expr":  bson.M{"$lte": bson.A{"$expiryTimestamp", dbNowMs}},
	}, intent, by)
}

// ClaimEarlyClose moves the user's OPEN trade to SETTLING with an early-close
// outcome - only while expiry is more than floorMs away by the database
// clock. nil means the trade is no longer OPEN or is too close to expiry.
func ClaimEarlyClose(ctx context.Context, tradeID, userID bson.ObjectID, intent *models.SettlementIntent, floorMs int64) (*models.BinaryTrade, error) {
	if floorMs < EarlyCloseHardFloorMs {
		floorMs = EarlyCloseHardFloorMs
	}
	return claimOne(ctx, bson.M{
		"_id":    tradeID,
		"userId": userID,
		"status": string(models.TradeStatusOpen),
		"$expr":  bson.M{"$gt": bson.A{"$expiryTimestamp", bson.M{"$add": bson.A{dbNowMs, floorMs}}}},
	}, intent, "customer.early_close")
}

// attachIntent records a recovered outcome on a trade that has none.
func attachIntent(ctx context.Context, trade *models.BinaryTrade, intent *models.SettlementIntent,
	from models.TradeStatus, by string) (*models.BinaryTrade, error) {
	filter := bson.M{"_id": trade.ID, "status": string(from), "settlementIntent": bson.M{"$exists": false}}
	if from == models.TradeStatusSettling {
		filter["updatedAt"] = bson.M{"$lt": staleBefore()}
	}
	return claimOne(ctx, filter, intent, by)
}

// revertToOpen returns an abandoned, intent-less claim to OPEN.
func revertToOpen(ctx context.Context, trade *models.BinaryTrade) (bool, error) {
	res, err := mongox.C(models.CollBinaryTrades).UpdateOne(ctx,
		bson.M{
			"_id": trade.ID, "status": string(models.TradeStatusSettling),
			"settlementIntent": bson.M{"$exists": false}, "updatedAt": bson.M{"$lt": staleBefore()},
		},
		bson.M{
			"$set":   bson.M{"status": string(models.TradeStatusOpen), "updatedAt": mongox.Now()},
			"$unset": bson.M{"settlementClaim": ""},
		})
	if err != nil {
		return false, err
	}
	if res.ModifiedCount == 0 {
		return false, nil
	}
	logx.Warn("abandoned settlement claim returned to OPEN", "tradeId", trade.ID.Hex(), "expiry", trade.ExpiryTimestamp)
	writeAudit(context.WithoutCancel(ctx), audit.Entry{
		ActorRole: "system", Action: "settlement.claim_reverted", Resource: "BinaryTrade", ResourceID: trade.ID.Hex(),
		After:  bson.M{"status": "OPEN", "expiryTimestamp": trade.ExpiryTimestamp},
		Reason: "SETTLING without a recorded outcome and no money moved",
	})
	if trade.ExpiryTimestamp > util.NowMs() {
		_ = Reschedule(ctx, trade.ID.Hex(), trade.ExpiryTimestamp)
	}
	return true, nil
}

// ── recovery from the books ───────────────────────────────────────────────

func findLedgerRow(ctx context.Context, tradeID string) (*models.WalletTransaction, error) {
	var tx models.WalletTransaction
	err := mongox.C(models.CollWalletTransactions).FindOne(ctx, bson.M{"idempotencyKey": SettleKey(tradeID)}).Decode(&tx)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &tx, nil
}

func findSettlementRow(ctx context.Context, tradeID bson.ObjectID) (*models.Settlement, error) {
	var s models.Settlement
	err := mongox.C(models.CollSettlements).FindOne(ctx, bson.M{"tradeId": tradeID}).Decode(&s)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func metaInt(m map[string]any, key string) (int64, bool) {
	switch n := m[key].(type) {
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case float64:
		return int64(n), true
	}
	return 0, false
}

// ledgerCloseValue is what an early-close ledger row paid back.
func ledgerCloseValue(tx *models.WalletTransaction, stake int64) int64 {
	if v, ok := metaInt(tx.Metadata, "closeValue"); ok {
		return v
	}
	if tx.Movement == models.MovementDebit {
		return stake - tx.Amount
	}
	return stake + tx.Amount
}

// ledgerMatches reports whether the booked ledger row is the movement the
// intent would have made.
func ledgerMatches(tx *models.WalletTransaction, trade *models.BinaryTrade, in *models.SettlementIntent) bool {
	switch in.WalletOp {
	case models.SettlementWalletWin:
		return tx.Type == models.WalletTxTradeWin && tx.Amount == in.WalletAmount
	case models.SettlementWalletLoss:
		return tx.Type == models.WalletTxTradeLoss && tx.Amount == trade.Stake
	case models.SettlementWalletRefund:
		return tx.Type == models.WalletTxTradeRefund && tx.Amount == trade.Stake
	case models.SettlementWalletEarlyClose:
		return tx.Type == models.WalletTxTradeEarlyClose && ledgerCloseValue(tx, trade.Stake) == in.WalletAmount
	}
	return false
}

var errUnrecoverableLedger = errors.New("settlement: the trade's settle ledger row is not a settlement movement")

// intentFromBooks rebuilds the outcome an earlier version already booked.
// The wallet ledger is the money truth and wins over the settlement row.
func intentFromBooks(trade *models.BinaryTrade, tx *models.WalletTransaction, row *models.Settlement, settledBy string) (*models.SettlementIntent, error) {
	ev := models.SettlementEvidence{
		Rule: models.SettlementRuleLedgerRecovery, ExpiryTimestamp: trade.ExpiryTimestamp, DecidedAt: util.NowMs(),
	}
	var price *float64
	if row != nil {
		if row.Evidence != nil {
			ev = *row.Evidence
		}
		p := row.ExpiryPrice
		price = &p
		ev.Note = strings.TrimSpace(ev.Note + " recovered from settlement row " + row.ID.Hex())
	} else if trade.ExpiryPrice != nil {
		p := *trade.ExpiryPrice
		price = &p
	}
	if price != nil && ev.Price == nil {
		ev.Price = price
	}
	if row != nil && row.SettledBy != "" {
		settledBy = row.SettledBy
	}

	var in models.SettlementIntent
	if tx != nil {
		ev.Source = models.SettlementEvidenceSourceLedger
		ev.Note = strings.TrimSpace(ev.Note + " recovered from wallet transaction " + tx.ID.Hex())
		switch tx.Type {
		case models.WalletTxTradeWin:
			in = expiryIntent(trade, models.TradeResultWin, price, ev, settledBy)
			in.WalletAmount = tx.Amount
			in.PayoutAmount, in.PlatformResult, in.SettlementAmount = trade.Stake+tx.Amount, -tx.Amount, tx.Amount
		case models.WalletTxTradeLoss:
			in = expiryIntent(trade, models.TradeResultLoss, price, ev, settledBy)
		case models.WalletTxTradeRefund:
			in = expiryIntent(trade, models.TradeResultDraw, price, ev, settledBy)
		case models.WalletTxTradeEarlyClose:
			ev.Kind = models.SettlementKindEarlyClose
			in = EarlyCloseIntent(trade, ledgerCloseValue(tx, trade.Stake), price, ev, settledBy)
		default:
			return nil, errUnrecoverableLedger
		}
	} else {
		switch row.Result {
		case models.TradeResultEarlyClose:
			ev.Kind = models.SettlementKindEarlyClose
			in = EarlyCloseIntent(trade, row.PayoutAmount, price, ev, settledBy)
		case models.TradeResultWin:
			in = expiryIntent(trade, models.TradeResultWin, price, ev, settledBy)
			profit := row.PayoutAmount - row.Stake
			in.WalletAmount, in.PayoutAmount, in.PlatformResult, in.SettlementAmount = profit, row.PayoutAmount, -profit, profit
		default:
			in = expiryIntent(trade, row.Result, price, ev, settledBy)
		}
	}
	if in.Evidence.Kind == "" {
		in.Evidence.Kind = in.Kind
	}
	return &in, nil
}

// legacyIntent returns the outcome already booked for a trade that has no
// recorded intent, or nil when nothing was booked.
func legacyIntent(ctx context.Context, trade *models.BinaryTrade, settledBy string) (*models.SettlementIntent, error) {
	tx, err := findLedgerRow(ctx, trade.ID.Hex())
	if err != nil {
		return nil, err
	}
	row, err := findSettlementRow(ctx, trade.ID)
	if err != nil {
		return nil, err
	}
	if tx == nil && row == nil {
		return nil, nil
	}
	return intentFromBooks(trade, tx, row, settledBy)
}

// ── execution ─────────────────────────────────────────────────────────────

// Execute applies a SETTLING trade's recorded intent: the wallet movement,
// the settlement row and the final trade state, then (once, by whichever
// caller finalised it) exposure release, events and notification. Safe to
// call any number of times, concurrently, from any process.
func Execute(ctx context.Context, trade *models.BinaryTrade) (Outcome, error) {
	return execute(context.WithoutCancel(ctx), trade, true)
}

func execute(ctx context.Context, trade *models.BinaryTrade, mayReconcile bool) (Outcome, error) {
	intent := trade.SettlementIntent
	if intent == nil {
		return Outcome{}, errors.New("settlement: trade " + trade.ID.Hex() + " has no recorded outcome")
	}
	tradeID := trade.ID.Hex()

	// 1. Money - keyed, so it moves at most once whoever runs this.
	mutation, err := applyWallet(ctx, trade, intent)
	if err != nil {
		return Outcome{}, err
	}
	if mutation.Replayed && mayReconcile {
		adopted, err := adoptLedgerOutcome(ctx, trade, intent)
		if err != nil {
			return Outcome{}, err
		}
		if adopted != nil {
			return execute(ctx, adopted, false)
		}
	}
	if err := hook("after_wallet"); err != nil {
		return Outcome{}, err
	}

	var walletTxID *bson.ObjectID
	if id, err := bson.ObjectIDFromHex(mutation.TransactionID); err == nil {
		walletTxID = &id
	}

	// 2 + 3. Settlement row and final trade state.
	updated, finalised, err := finalise(ctx, trade, intent, walletTxID)
	if err != nil {
		return Outcome{}, err
	}
	r := intent.Result
	if !finalised {
		return Outcome{TradeID: tradeID, Status: StatusAlreadySettled, Result: &r, Rule: intent.Evidence.Rule}, nil
	}

	// 4. Once per trade: release exposure, drop from the queue, tell people.
	afterFinal(ctx, updated, intent)
	pa, pr := intent.PayoutAmount, intent.PlatformResult
	return Outcome{
		TradeID: tradeID, Status: StatusSettled, Result: &r, ExpiryPrice: intent.ExpiryPrice,
		PayoutAmount: &pa, PlatformResult: &pr, Rule: intent.Evidence.Rule,
	}, nil
}

func applyWallet(ctx context.Context, trade *models.BinaryTrade, in *models.SettlementIntent) (wallets.MutationResult, error) {
	funds := wallets.TradeFundsInput{
		UserID: trade.UserID.Hex(), AccountType: trade.AccountType, Stake: trade.Stake, TradeID: trade.ID.Hex(),
	}
	switch in.WalletOp {
	case models.SettlementWalletWin:
		return wallets.SettleTradeWin(ctx, funds, in.WalletAmount)
	case models.SettlementWalletLoss:
		return wallets.SettleTradeLoss(ctx, funds)
	case models.SettlementWalletEarlyClose:
		return wallets.SettleTradeEarlyClose(ctx, funds, in.WalletAmount)
	default:
		return wallets.ReleaseTradeFunds(ctx, funds, wallets.ReleaseDraw)
	}
}

// adoptLedgerOutcome handles a replayed wallet key whose booked movement is
// not the one the intent describes (only possible for a trade an earlier
// version half-settled): the ledger wins, so wallet, row and trade agree.
func adoptLedgerOutcome(ctx context.Context, trade *models.BinaryTrade, intent *models.SettlementIntent) (*models.BinaryTrade, error) {
	tx, err := findLedgerRow(ctx, trade.ID.Hex())
	if err != nil || tx == nil || ledgerMatches(tx, trade, intent) {
		return nil, err
	}
	row, err := findSettlementRow(ctx, trade.ID)
	if err != nil {
		return nil, err
	}
	adopted, err := intentFromBooks(trade, tx, row, intent.SettledBy)
	if err != nil {
		return nil, err
	}
	res, err := mongox.C(models.CollBinaryTrades).UpdateOne(ctx,
		bson.M{"_id": trade.ID, "status": string(models.TradeStatusSettling), "settlementIntent.id": intent.ID},
		bson.M{"$set": bson.M{"settlementIntent": adopted}})
	if err != nil {
		return nil, err
	}
	logx.Error("settlement intent disagreed with the booked ledger row - adopting the ledger outcome",
		"tradeId", trade.ID.Hex(), "intended", string(intent.Result), "booked", string(tx.Type))
	writeAudit(ctx, audit.Entry{
		ActorRole: "system", Action: "settlement.ledger_adopted", Resource: "BinaryTrade", ResourceID: trade.ID.Hex(),
		Before: bson.M{"result": intent.Result, "walletOp": intent.WalletOp, "walletAmount": intent.WalletAmount},
		After:  bson.M{"result": adopted.Result, "walletOp": adopted.WalletOp, "walletAmount": adopted.WalletAmount, "walletTransactionId": tx.ID.Hex()},
		Reason: "wallet key already applied with a different movement",
	})
	current, err := loadTrade(ctx, trade.ID)
	if err != nil {
		return nil, err
	}
	if res.ModifiedCount == 0 && current != nil && current.SettlementIntent != nil && current.SettlementIntent.ID == intent.ID {
		return nil, errors.New("settlement: could not adopt the ledger outcome for " + trade.ID.Hex())
	}
	return current, nil
}

func rowFromIntent(trade *models.BinaryTrade, in *models.SettlementIntent, walletTxID *bson.ObjectID) models.Settlement {
	currency := trade.Currency
	if currency == "" {
		currency = models.BinaryTradeDefaultCurrency
	}
	// A void compares no price; the row keeps the entry price so that
	// expiryPrice == entryPrice agrees with the stored DRAW result, and
	// evidence.price (null) says no price was used.
	expiryPrice := trade.EntryPrice
	if in.ExpiryPrice != nil {
		expiryPrice = *in.ExpiryPrice
	}
	latencyMs := max(0, util.NowMs()-in.CreatedAt.UnixMilli())
	ev := in.Evidence
	return models.Settlement{
		TradeID:             trade.ID,
		UserID:              trade.UserID,
		AssetID:             trade.AssetID,
		Symbol:              trade.Symbol,
		Result:              in.Result,
		AccountType:         models.NormAccountType(trade.AccountType),
		EntryPrice:          trade.EntryPrice,
		ExpiryPrice:         expiryPrice,
		ExpiryTimestamp:     trade.ExpiryTimestamp,
		Stake:               trade.Stake,
		PayoutPercent:       trade.PayoutPercent,
		PayoutAmount:        in.PayoutAmount,
		PlatformResult:      in.PlatformResult,
		Currency:            currency,
		WalletTransactionID: walletTxID,
		SettledBy:           in.SettledBy,
		LatencyMs:           &latencyMs,
		Evidence:            &ev,
		CreatedAt:           mongox.Now(),
	}
}

func finalFields(trade *models.BinaryTrade, in *models.SettlementIntent) bson.M {
	now := mongox.Now()
	set := bson.M{
		"status":            string(in.FinalStatus),
		"result":            string(in.Result),
		"expiryPrice":       in.ExpiryPrice,
		"expiryPriceSource": in.Evidence.Rule,
		"settlementAmount":  in.SettlementAmount,
		"platformResult":    in.PlatformResult,
		"settledAt":         now,
		"updatedAt":         now,
	}
	if in.Evidence.Sequence != nil {
		set["expiryTickSequence"] = *in.Evidence.Sequence
	}
	if in.Evidence.ProviderTimestamp != nil {
		set["expiryProviderTimestamp"] = *in.Evidence.ProviderTimestamp
	}
	if in.Kind == models.SettlementKindExpiry {
		set["settlementLatencyMs"] = util.NowMs() - trade.ExpiryTimestamp
	}
	return set
}

// finalise writes the settlement row (if absent) and moves the trade from
// SETTLING to its final state, in one transaction where the deployment has
// them; without, the order (row, then trade) plus the intent makes a crash
// in between harmless - the next Execute finds the row and finishes.
func finalise(ctx context.Context, trade *models.BinaryTrade, in *models.SettlementIntent, walletTxID *bson.ObjectID) (*models.BinaryTrade, bool, error) {
	row := rowFromIntent(trade, in, walletTxID)
	var updated models.BinaryTrade
	finalised := false
	run := func(tctx context.Context) error {
		finalised = false
		existing, err := findSettlementRow(tctx, trade.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			if _, err := mongox.C(models.CollSettlements).InsertOne(tctx, row); err != nil {
				// Without a transaction a racing executor may have inserted the
				// same row a moment ago; inside one the write must abort.
				if !mongox.IsDuplicateKey(err) || mongox.TransactionsAvailable() {
					return err
				}
			}
		} else if existing.Result != in.Result || existing.PayoutAmount != in.PayoutAmount {
			logx.Error("settlement row disagrees with the trade's outcome",
				"tradeId", trade.ID.Hex(), "rowResult", string(existing.Result), "intentResult", string(in.Result))
			writeAudit(context.WithoutCancel(ctx), audit.Entry{
				ActorRole: "system", Action: "settlement.row_mismatch", Resource: "BinaryTrade", ResourceID: trade.ID.Hex(),
				Before: bson.M{"result": existing.Result, "payoutAmount": existing.PayoutAmount},
				After:  bson.M{"result": in.Result, "payoutAmount": in.PayoutAmount},
				Reason: "settlement row written by an earlier version differs from the booked outcome", Failed: true,
			})
		}
		if err := hook("after_row"); err != nil {
			return err
		}
		err = mongox.C(models.CollBinaryTrades).FindOneAndUpdate(tctx,
			bson.M{"_id": trade.ID, "status": string(models.TradeStatusSettling), "settlementIntent.id": in.ID},
			bson.M{"$set": finalFields(trade, in)},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
		if mongox.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		finalised = true
		return nil
	}
	err := mongox.WithTransaction(ctx, run)
	if err != nil && mongox.IsDuplicateKey(err) {
		// Lost the row insert to a concurrent executor inside a transaction:
		// the row exists now, so the retry only finalises.
		err = mongox.WithTransaction(ctx, run)
	}
	if err != nil {
		return nil, false, err
	}
	return &updated, finalised, nil
}

// afterFinal runs once per trade, by the caller whose finalisation matched.
// Failures here are logged, not returned: the outcome is already final and
// retrying would not repeat these steps.
func afterFinal(ctx context.Context, trade *models.BinaryTrade, in *models.SettlementIntent) {
	tradeID, userID := trade.ID.Hex(), trade.UserID.Hex()
	live := models.NormAccountType(trade.AccountType) == models.AccountLive

	if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err(); err != nil {
		logx.Warn("failed to drop settled trade from the expiry queue", "tradeId", tradeID, "err", err.Error())
	}
	// DEMO trades never entered the book. Released by the trade id - the key
	// its reservation and commit used - so the release is exactly once and
	// can never take another trade's record (a trade counted under a legacy
	// anonymous record is released through it).
	if live {
		if _, err := exposure.ApplyExposureDelta(ctx, exposure.ExposureDelta{
			AssetID: trade.AssetID.Hex(), Symbol: trade.Symbol, UserID: userID, Direction: trade.Direction,
			Stake: trade.Stake, Profit: trade.Profit, Sign: -1, TradeID: tradeID, ExpiryTimestamp: trade.ExpiryTimestamp,
		}); err != nil {
			// The counters are a cache rebuilt from trades at boot.
			logx.Error("failed to release settled trade exposure", "tradeId", tradeID, "err", err.Error())
			metrics.IncrementCounter("settlements.exposure_release_failed", 1, 0)
		}
	}

	ws.Emit([]string{config.ChUser(userID), config.ChAdminOps}, config.EvTradeSettled, models.ToTradeView(trade))

	rule := in.Evidence.Rule
	lower := strings.ToLower(string(in.Result))
	switch in.Kind {
	case models.SettlementKindEarlyClose:
		metrics.IncrementCounter("trades.closed_early", 1, 0)
		if live {
			metrics.IncrementCounter("platform.gross_result", float64(in.PlatformResult), 0)
		}
	default:
		n := notifications.Input{
			Type:  "TRADE_SETTLED",
			Title: "Trade " + lower,
			Body:  trade.Symbol + " " + string(trade.Direction) + " settled as " + string(in.Result),
			Data:  map[string]any{"tradeId": tradeID, "result": string(in.Result), "payoutAmount": in.PayoutAmount, "rule": rule},
		}
		if rule == models.SettlementRuleNoPriceVoidRefund {
			n.Title = "Trade voided"
			n.Body = trade.Symbol + " " + string(trade.Direction) +
				": no valid expiry price was available, so the trade was voided and your stake returned"
		}
		notify(userID, n)
		metrics.IncrementCounter("settlements.total", 1, 0)
		metrics.IncrementCounter("settlements."+lower, 1, 0)
		if live {
			metrics.IncrementCounter("platform.gross_result", float64(in.PlatformResult), 0)
			metrics.IncrementCounter("customer.payout", float64(in.PayoutAmount), 0)
		}
	}

	// Anything other than the normal price rules is put on the audit trail.
	switch rule {
	case models.SettlementRuleTickAtOrBeforeExpiry, models.SettlementRuleEarlyCloseAtMarket:
	default:
		metrics.IncrementCounter("settlements.rule."+strings.ToLower(rule), 1, 0)
		action := "trade.settled_by_rule"
		if rule == models.SettlementRuleNoPriceVoidRefund {
			action = "trade.voided"
		}
		writeAudit(ctx, audit.Entry{
			ActorRole: "system", Action: action, Resource: "BinaryTrade", ResourceID: tradeID,
			After: bson.M{
				"rule": rule, "result": in.Result, "status": in.FinalStatus, "payoutAmount": in.PayoutAmount,
				"expiryTimestamp": trade.ExpiryTimestamp, "riskConfigVersion": in.Evidence.RiskConfigVersion,
				"policy": in.Evidence.Policy, "evidence": in.Evidence,
			},
			Reason: in.Evidence.Note,
		})
	}

	logx.Info("trade settled",
		"tradeId", tradeID, "symbol", trade.Symbol, "kind", string(in.Kind), "result", in.Result, "rule", rule,
		"entryPrice", trade.EntryPrice, "expiryPrice", in.ExpiryPrice, "platformResult", in.PlatformResult)
}
