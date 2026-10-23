package wallets

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/mongox"
)

const orphanStake = int64(100_00)

// lockFor reserves a stake for tradeID through the real path, then backdates
// the lock row by age (as if the process had died that long ago).
func lockFor(t *testing.T, uid, tradeID string, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	if _, err := LockTradeFunds(ctx, TradeFundsInput{UserID: uid, Stake: orphanStake, TradeID: tradeID}); err != nil {
		t.Fatal(err)
	}
	if _, err := mongox.C(models.CollWalletTransactions).UpdateOne(ctx, bson.M{"idempotencyKey": "trade:lock:" + tradeID},
		bson.M{"$set": bson.M{"createdAt": time.Now().Add(-age).UTC()}}); err != nil {
		t.Fatal(err)
	}
}

func insertTradeDoc(t *testing.T, uid string, id bson.ObjectID, status models.TradeStatus) {
	t.Helper()
	u, _ := mongox.ParseID(uid)
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(context.Background(), bson.M{
		"_id": id, "reference": "BT_o" + id.Hex(), "userId": u, "status": string(status), "stake": orphanStake,
		"accountType": models.AccountLive, "createdAt": mongox.Now(), "updatedAt": mongox.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

// TestOrphanSweepReachesEveryLock: the sweep examines every stake lock, not
// the oldest 500. With 1,100 older, settled locks in the ledger, the orphans
// written after them are still found and released - once; locks of open
// trades, of finished trades and young locks are left alone; and a sweep
// running under a lapsed leader fence releases nothing.
func TestOrphanSweepReachesEveryLock(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := newUser(t)
	if _, err := CreditWallet(ctx, AdjustmentInput{UserID: uid, Amount: 10_000_00, Type: models.WalletTxDeposit,
		IdempotencyKey: "seed:" + uid}); err != nil {
		t.Fatal(err)
	}
	u, _ := mongox.ParseID(uid)

	// History: 1,100 old locks, each released (settled) long ago.
	old := time.Now().Add(-48 * time.Hour)
	history := make([]any, 0, 2200)
	for i := 0; i < 1100; i++ {
		id := bson.NewObjectID().Hex()
		at := old.Add(time.Duration(i) * time.Millisecond).UTC()
		row := func(typ models.WalletTransactionType, key string) models.WalletTransaction {
			return models.WalletTransaction{WalletID: bson.NewObjectID(), UserID: u, AccountType: models.AccountLive, Type: typ,
				Movement: models.MovementLock, Amount: orphanStake, Currency: "INR", ReferenceType: models.Str(refBinaryTrade),
				ReferenceID: models.Str(id), IdempotencyKey: models.Str(key), Metadata: map[string]any{}, CreatedAt: at}
		}
		history = append(history, row(models.WalletTxTradeLock, "trade:lock:"+id), row(models.WalletTxTradeLoss, "trade:settle:"+id))
	}
	if _, err := mongox.C(models.CollWalletTransactions).InsertMany(ctx, history); err != nil {
		t.Fatal(err)
	}

	// Newer than all of that: three orphans, an open trade, a finished trade
	// whose stake was never released, and an orphan still inside the grace.
	orphans := []string{bson.NewObjectID().Hex(), bson.NewObjectID().Hex(), bson.NewObjectID().Hex()}
	for _, id := range orphans {
		lockFor(t, uid, id, 2*time.Minute)
	}
	open, finished := bson.NewObjectID(), bson.NewObjectID()
	lockFor(t, uid, open.Hex(), 2*time.Minute)
	insertTradeDoc(t, uid, open, models.TradeStatusOpen)
	lockFor(t, uid, finished.Hex(), 2*time.Minute)
	insertTradeDoc(t, uid, finished, models.TradeStatusWon)
	young := bson.NewObjectID().Hex()
	lockFor(t, uid, young, 10*time.Second)
	w, _ := GetWallet(ctx, uid, "", "")
	if w.LockedBalance != 6*orphanStake {
		t.Fatalf("locked %d", w.LockedBalance)
	}

	// A former leader (lapsed lease) stops before releasing anything.
	stale := leader.WithFence(ctx, &leader.Fence{Name: "stake-lock-orphans", LeaseUntil: time.Now().Add(-time.Second)})
	if _, err := ReleaseOrphanedTradeLocks(stale, time.Now()); !errors.Is(err, leader.ErrLeadershipLost) {
		t.Fatalf("fenced sweep: %v", err)
	}
	if w, _ := GetWallet(ctx, uid, "", ""); w.LockedBalance != 6*orphanStake {
		t.Fatalf("a fenced sweep released money: locked %d", w.LockedBalance)
	}

	res, err := ReleaseOrphanedTradeLocks(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if res.Released != 3 || res.ReleasedMinor != 3*orphanStake || res.Finished != 1 || res.Failed != 0 || res.Scanned != 4 {
		t.Fatalf("sweep %+v", res)
	}
	w, _ = GetWallet(ctx, uid, "", "")
	if w.LockedBalance != 3*orphanStake || w.Balance != 10_000_00 {
		t.Fatalf("wallet after the sweep %+v", w)
	}
	for _, id := range orphans {
		if row, err := LedgerRowByKey(ctx, "trade:settle:"+id); err != nil || row == nil {
			t.Fatalf("orphan %s not released: %v", id, err)
		}
	}

	// A second pass changes nothing; what is left is the finished trade's
	// lock (reported, not released).
	again, err := ReleaseOrphanedTradeLocks(ctx, time.Now())
	if err != nil || again.Released != 0 || again.Finished != 1 {
		t.Fatalf("second pass %+v %v", again, err)
	}
	var left []UnreleasedStakeLock
	if err := ScanUnreleasedStakeLocks(ctx, time.Time{}, time.Now().Add(-OrphanGrace), 2, func(b []UnreleasedStakeLock) error {
		left = append(left, b...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].TradeID != finished.Hex() || left[0].Orphan() || left[0].TradeStatus != string(models.TradeStatusWon) {
		t.Fatalf("left %+v", left)
	}
}

// TestOrphanSweepTicks: the frequent tick examines only the recent locks
// (cheap, through the createdAt index) and releases a fresh orphan; an older
// orphan is found by the full pass, which runs on the first tick after taking
// the lead and every orphanFullEvery-th tick.
func TestOrphanSweepTicks(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := newUser(t)
	if _, err := CreditWallet(ctx, AdjustmentInput{UserID: uid, Amount: 1_000_00, Type: models.WalletTxDeposit,
		IdempotencyKey: "seed:" + uid}); err != nil {
		t.Fatal(err)
	}
	recent, old := bson.NewObjectID().Hex(), bson.NewObjectID().Hex()
	lockFor(t, uid, recent, 2*time.Minute)
	lockFor(t, uid, old, 2*time.Hour)
	saved := orphanTick
	t.Cleanup(func() { orphanTick = saved })

	orphanTick = 1 // a frequent tick
	if err := orphanSweepTick(ctx); err != nil {
		t.Fatal(err)
	}
	if row, _ := LedgerRowByKey(ctx, "trade:settle:"+recent); row == nil {
		t.Fatal("the recent orphan was not released by the frequent tick")
	}
	if row, _ := LedgerRowByKey(ctx, "trade:settle:"+old); row != nil {
		t.Fatal("the frequent tick reached an orphan older than its window")
	}
	orphanTick = 0 // the first tick after taking the lead: a full pass
	if err := orphanSweepTick(ctx); err != nil {
		t.Fatal(err)
	}
	if row, _ := LedgerRowByKey(ctx, "trade:settle:"+old); row == nil {
		t.Fatal("the full pass did not release the old orphan")
	}
	if w, _ := GetWallet(ctx, uid, "", ""); w.LockedBalance != 0 {
		t.Fatalf("locked %d", w.LockedBalance)
	}
}
