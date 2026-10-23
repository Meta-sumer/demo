package binary

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/service/wallets"
)

// cancelTrade opens a LIVE trade the way acceptance leaves it: stake locked,
// exposure committed under the trade id, expiry queued.
func (e *ecEnv) cancelTrade() *models.BinaryTrade {
	e.t.Helper()
	now := mongox.Now()
	tr := &models.BinaryTrade{
		ID: bson.NewObjectID(), Reference: "T-" + bson.NewObjectID().Hex(), UserID: e.userID, AssetID: bson.NewObjectID(),
		Symbol: "TESTUSD", Direction: models.DirectionUp, AccountType: models.AccountLive, Stake: ecStake, Currency: "INR",
		PayoutPercent: 82, Profit: ecProfit, EntryPrice: 100, EntryTimestamp: util.NowMs(), ExpiryTimestamp: util.NowMs() + 300_000,
		Status: models.TradeStatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	a := tr.AssetID.Hex()
	e.keys = append(e.keys, config.RedisKeys.Exposure(a))
	if _, err := wallets.LockTradeFunds(e.ctx, wallets.TradeFundsInput{UserID: e.userID.Hex(), Stake: ecStake, TradeID: tr.ID.Hex()}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(e.ctx, tr); err != nil {
		e.t.Fatal(err)
	}
	if _, err := exposure.ApplyExposureDelta(e.ctx, exposure.ExposureDelta{AssetID: a, Symbol: tr.Symbol, UserID: e.userID.Hex(),
		Direction: tr.Direction, Stake: ecStake, Profit: ecProfit, Sign: 1, TradeID: tr.ID.Hex(), ExpiryTimestamp: tr.ExpiryTimestamp}); err != nil {
		e.t.Fatal(err)
	}
	if err := redisx.R().ZAdd(e.ctx, config.RedisKeys.ExpiryQueue, redis.Z{Score: float64(tr.ExpiryTimestamp), Member: tr.ID.Hex()}).Err(); err != nil {
		e.t.Fatal(err)
	}
	return tr
}

func (e *ecEnv) lockedBalance() (int64, int64) {
	e.t.Helper()
	w, err := wallets.GetWallet(e.ctx, e.userID.Hex(), models.AccountLive, "")
	if err != nil {
		e.t.Fatal(err)
	}
	return w.Balance, w.LockedBalance
}

func (e *ecEnv) openOnBook(tr *models.BinaryTrade) int64 {
	e.t.Helper()
	v, err := redisx.R().HGet(e.ctx, config.RedisKeys.Exposure(tr.AssetID.Hex()), "openTrades").Int64()
	if err != nil && !redisx.IsNil(err) {
		e.t.Fatal(err)
	}
	return v
}

func (e *ecEnv) releaseRows(tr *models.BinaryTrade) int64 {
	e.t.Helper()
	n, err := mongox.C(models.CollWalletTransactions).CountDocuments(e.ctx, bson.M{"idempotencyKey": "trade:settle:" + tr.ID.Hex()})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *ecEnv) reload(tr *models.BinaryTrade) *models.BinaryTrade {
	e.t.Helper()
	got, err := findTrade(e.ctx, tr.ID.Hex())
	if err != nil || got == nil {
		e.t.Fatalf("reload: %v", err)
	}
	return got
}

// cancelledCheck runs reconciliation and returns its cancelled-trades check.
func (e *ecEnv) cancelledCheck() reconciliation.CheckView {
	e.t.Helper()
	res, err := reconciliation.RunReconciliation(e.ctx, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	for _, c := range res.Checks {
		if c.Name == "cancelled_trades_released" {
			return c
		}
	}
	e.t.Fatal("no cancelled_trades_released check")
	return reconciliation.CheckView{}
}

func TestCancelTradeReleasesOnceAndIsIdempotent(t *testing.T) {
	e := newECEnv(t)
	tr := e.cancelTrade()
	if e.openOnBook(tr) != 1 {
		t.Fatal("trade not on the book")
	}
	view, err := CancelTrade(e.ctx, tr.ID.Hex(), "operator test")
	if err != nil || view.Status != models.TradeStatusCancelled {
		t.Fatalf("cancel %+v %v", view, err)
	}
	balance, locked := e.lockedBalance()
	if balance != ecBalance || locked != 0 || e.releaseRows(tr) != 1 || e.openOnBook(tr) != 0 {
		t.Fatalf("after cancel: balance %d locked %d releases %d book %d", balance, locked, e.releaseRows(tr), e.openOnBook(tr))
	}
	got := e.reload(tr)
	if c := got.Cancellation; c == nil || !c.FundsReleased || !c.ExposureReleased || c.FundsReleasedAt == nil {
		t.Fatalf("cancellation %+v", got.Cancellation)
	}
	if _, err := redisx.R().ZScore(e.ctx, config.RedisKeys.ExpiryQueue, tr.ID.Hex()).Result(); !redisx.IsNil(err) {
		t.Fatal("still on the expiry queue")
	}

	// Repeating the cancel is not an error and moves nothing.
	again, err := CancelTrade(e.ctx, tr.ID.Hex(), "operator test")
	if err != nil || again.Status != models.TradeStatusCancelled {
		t.Fatalf("repeat %+v %v", again, err)
	}
	if b, l := e.lockedBalance(); b != ecBalance || l != 0 || e.releaseRows(tr) != 1 || e.openOnBook(tr) != 0 {
		t.Fatal("a repeated cancel moved money or exposure")
	}
	if c := e.cancelledCheck(); !c.Passed {
		t.Fatalf("check %+v", c)
	}

	// A settled trade cannot be cancelled.
	won := e.cancelTrade()
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(e.ctx, bson.M{"_id": won.ID},
		bson.M{"$set": bson.M{"status": models.TradeStatusWon}}); err != nil {
		t.Fatal(err)
	}
	if _, err := CancelTrade(e.ctx, won.ID.Hex(), "operator test"); !apperr.HasCode(err, apperr.Conflict) {
		t.Fatalf("cancel of a WON trade: %v", err)
	}
}

func TestCancelTradeWalletFailureIsFlaggedAndRepaired(t *testing.T) {
	e := newECEnv(t)
	saved := releaseCancelledFunds
	t.Cleanup(func() { releaseCancelledFunds = saved })
	releaseCancelledFunds = func(context.Context, wallets.TradeFundsInput, wallets.ReleaseReason) (wallets.MutationResult, error) {
		return wallets.MutationResult{}, errors.New("wallet unavailable")
	}

	first, second := e.cancelTrade(), e.cancelTrade()
	for _, tr := range []*models.BinaryTrade{first, second} {
		v, err := CancelTrade(e.ctx, tr.ID.Hex(), "operator test")
		if err == nil {
			t.Fatal("cancel succeeded with the wallet down")
		}
		// The cancelled trade comes back with the error, so it can be audited.
		if v.ID != tr.ID.Hex() || v.Status != models.TradeStatusCancelled {
			t.Fatalf("view with the error %+v", v)
		}
		got := e.reload(tr)
		if got.Status != models.TradeStatusCancelled || got.Cancellation == nil || got.Cancellation.FundsReleased ||
			got.Cancellation.Attempts != 1 || got.Cancellation.LastError != "wallet unavailable" {
			t.Fatalf("after a failed release %+v %+v", got.Status, got.Cancellation)
		}
		// The trade cannot be settled or cancelled into another outcome, and
		// its exposure is still on the book until the cancel completes.
		if e.openOnBook(tr) != 1 {
			t.Fatal("exposure released before the stake")
		}
	}
	if _, locked := e.lockedBalance(); locked != 2*ecStake {
		t.Fatalf("locked %d", locked)
	}
	// Reconciliation names both trades.
	c := e.cancelledCheck()
	if c.Passed || c.Actual != 2 {
		t.Fatalf("check %+v", c)
	}

	// The wallet is back: a repeated cancel finishes the first, the repair
	// the second.
	releaseCancelledFunds = saved
	if v, err := CancelTrade(e.ctx, first.ID.Hex(), "operator test"); err != nil || v.Status != models.TradeStatusCancelled {
		t.Fatalf("retry %v", err)
	}
	res, err := RepairCancelledTrades(e.ctx)
	if err != nil || res.Repaired != 1 || res.Failed != 0 || len(res.TradeIDs) != 1 || res.TradeIDs[0] != second.ID.Hex() {
		t.Fatalf("repair %+v %v", res, err)
	}
	for _, tr := range []*models.BinaryTrade{first, second} {
		got := e.reload(tr)
		if !got.Cancellation.FundsReleased || !got.Cancellation.ExposureReleased || got.Cancellation.LastError != "" ||
			e.releaseRows(tr) != 1 || e.openOnBook(tr) != 0 {
			t.Fatalf("not completed %+v", got.Cancellation)
		}
	}
	if b, l := e.lockedBalance(); b != ecBalance || l != 0 {
		t.Fatalf("balance %d locked %d", b, l)
	}
	if c := e.cancelledCheck(); !c.Passed {
		t.Fatalf("check after repair %+v", c)
	}
	// Nothing left to do.
	if res, _ := RepairCancelledTrades(e.ctx); res.Scanned != 0 {
		t.Fatalf("second pass %+v", res)
	}
}

// A trade cancelled by an earlier version (no cancellation record) whose
// release failed is found and finished; one whose release succeeded is only
// marked complete - no money moves twice.
func TestRepairFinishesLegacyCancels(t *testing.T) {
	e := newECEnv(t)
	locked, released := e.cancelTrade(), e.cancelTrade()
	if _, err := wallets.ReleaseTradeFunds(e.ctx, wallets.TradeFundsInput{UserID: e.userID.Hex(), Stake: ecStake,
		TradeID: released.ID.Hex()}, wallets.ReleaseCancelled); err != nil {
		t.Fatal(err)
	}
	for _, tr := range []*models.BinaryTrade{locked, released} {
		if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(e.ctx, bson.M{"_id": tr.ID},
			bson.M{"$set": bson.M{"status": models.TradeStatusCancelled, "cancelReason": "legacy"}}); err != nil {
			t.Fatal(err)
		}
	}
	if c := e.cancelledCheck(); c.Passed || c.Actual != 1 {
		t.Fatalf("check %+v", c)
	}
	res, err := RepairCancelledTrades(e.ctx)
	if err != nil || res.Repaired != 2 {
		t.Fatalf("repair %+v %v", res, err)
	}
	if b, l := e.lockedBalance(); b != ecBalance || l != 0 || e.releaseRows(locked) != 1 || e.releaseRows(released) != 1 {
		t.Fatalf("balance %d locked %d", b, l)
	}
	if c := e.cancelledCheck(); !c.Passed {
		t.Fatalf("check after repair %+v", c)
	}
}
