package reconciliation

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/wallets"
)

// TestStakeLocksHaveOpenTradesCheck: an unreleased stake lock whose trade
// does not exist (an orphan) or is finished fails the check; a lock of an
// open trade does not; once the orphan sweep released the orphan only the
// finished trade's lock is still reported.
func TestStakeLocksHaveOpenTradesCheck(t *testing.T) {
	f := newFixture(t)
	uid := f.uid.Hex()
	if _, err := wallets.GetOrCreateWallet(f.ctx, uid, models.AccountLive, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := wallets.CreditWallet(f.ctx, wallets.AdjustmentInput{UserID: uid, Amount: 1_000_00, Type: models.WalletTxDeposit,
		IdempotencyKey: "seed:" + uid}); err != nil {
		t.Fatal(err)
	}
	lock := func(id bson.ObjectID) {
		if _, err := wallets.LockTradeFunds(f.ctx, wallets.TradeFundsInput{UserID: uid, Stake: 10_00, TradeID: id.Hex()}); err != nil {
			t.Fatal(err)
		}
		if _, err := mongox.C(models.CollWalletTransactions).UpdateOne(f.ctx, bson.M{"idempotencyKey": "trade:lock:" + id.Hex()},
			bson.M{"$set": bson.M{"createdAt": time.Now().Add(-5 * time.Minute).UTC()}}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	open := f.trade(models.TradeStatusOpen, now.Add(time.Minute), now)
	lock(open)
	c, err := checkStakeLocksHaveOpenTrades(f.ctx)
	if err != nil || !c.Passed || c.Name != "stake_locks_have_open_trades" {
		t.Fatalf("open trade only: %+v %v", c, err)
	}

	orphan, finished := bson.NewObjectID(), f.trade(models.TradeStatusLost, now.Add(-time.Hour), now.Add(-time.Hour))
	lock(orphan)
	lock(finished)
	c, err = checkStakeLocksHaveOpenTrades(f.ctx)
	if err != nil || c.Passed || c.Actual != 2 || c.Details["orphans"] != int64(1) || c.Details["finished"] != int64(1) ||
		c.Details["lockedMinor"] != int64(20_00) {
		t.Fatalf("orphan and finished: %+v %v", c, err)
	}

	if res, err := wallets.ReleaseOrphanedTradeLocks(f.ctx, time.Now()); err != nil || res.Released != 1 {
		t.Fatalf("sweep %+v %v", res, err)
	}
	c, err = checkStakeLocksHaveOpenTrades(f.ctx)
	locks, _ := c.Details["locks"].([]map[string]any)
	if err != nil || c.Passed || c.Actual != 1 || len(locks) != 1 || locks[0]["tradeId"] != finished.Hex() || locks[0]["kind"] != "finished_trade" {
		t.Fatalf("after the sweep: %+v %v", c, err)
	}
}
