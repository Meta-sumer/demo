package expiry

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/settlement"
	"binarytrading/internal/service/wallets"
	"binarytrading/internal/testsupport"
)

// Expiry worker coordination (F-14): fenced hand-off, leader loss mid-tick,
// and queues rebuilt from durable state. Needs MongoDB and Redis.

const (
	balance = int64(1_000_00)
	stake   = int64(100_00)
	profit  = int64(80_00)
)

type env struct {
	t      *testing.T
	ctx    context.Context
	keys   []string
	userID bson.ObjectID
}

func newEnv(t *testing.T) *env {
	t.Helper()
	testsupport.LiveStores(t)
	models.EnsureIndexes(context.Background())
	e := &env{t: t, ctx: context.Background(), userID: bson.NewObjectID()}
	ns := "test:expiry:" + bson.NewObjectID().Hex()
	saved := config.RedisKeys
	config.RedisKeys.ExpiryQueue = ns + ":expiry:queue"
	config.RedisKeys.SettlementQueue = ns + ":settlement:queue"
	config.RedisKeys.ExposureAssets = ns + ":exp:assets"
	e.keys = append(e.keys, config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue,
		config.RedisKeys.ExposureAssets, config.RedisKeys.UserExposure(e.userID.Hex()))
	riskconfig.InvalidateRiskConfigCache()
	t.Cleanup(func() {
		_ = redisx.R().Del(context.Background(), e.keys...).Err()
		config.RedisKeys = saved
		riskconfig.InvalidateRiskConfigCache()
		time.Sleep(100 * time.Millisecond) // let fire-and-forget writes drain
	})
	if _, err := wallets.GetOrCreateWallet(e.ctx, e.userID.Hex(), models.AccountDemo, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := wallets.CreditWallet(e.ctx, wallets.AdjustmentInput{
		UserID: e.userID.Hex(), AccountType: models.AccountDemo, Amount: balance, Type: models.WalletTxAdjustmentCredit,
		IdempotencyKey: "test:credit:" + e.userID.Hex(),
	}); err != nil {
		t.Fatal(err)
	}
	return e
}

// DEMO trades: the queues and the state machine are identical, and DEMO
// keeps these tests off the shared exposure counters.
func (e *env) trade(entry float64, expiry int64) *models.BinaryTrade {
	e.t.Helper()
	now := mongox.Now()
	tr := &models.BinaryTrade{
		ID: bson.NewObjectID(), Reference: "T-" + bson.NewObjectID().Hex(), UserID: e.userID, AssetID: bson.NewObjectID(),
		Symbol: "TESTUSD", Direction: models.DirectionUp, AccountType: models.AccountDemo, Stake: stake, Currency: "INR",
		PayoutPercent: 80, Profit: profit, EntryPrice: entry, EntryTimestamp: expiry - 60_000, ExpiryTimestamp: expiry,
		Status: models.TradeStatusOpen, CreatedAt: now, UpdatedAt: now,
	}
	a := tr.AssetID.Hex()
	e.keys = append(e.keys, config.RedisKeys.TickHistory(a), config.RedisKeys.LastTick(a))
	if _, err := wallets.LockTradeFunds(e.ctx, wallets.TradeFundsInput{
		UserID: e.userID.Hex(), AccountType: models.AccountDemo, Stake: stake, TradeID: tr.ID.Hex(),
	}); err != nil {
		e.t.Fatal(err)
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(e.ctx, tr); err != nil {
		e.t.Fatal(err)
	}
	if err := settlement.Reschedule(e.ctx, tr.ID.Hex(), expiry); err != nil {
		e.t.Fatal(err)
	}
	return tr
}

func (e *env) tick(tr *models.BinaryTrade, price float64, ts int64) {
	e.t.Helper()
	pt := ts
	member, _ := json.Marshal(models.NormalisedTick{
		MarketTick: models.MarketTick{Asset: tr.Symbol, Price: price, Timestamp: ts, ProviderTimestamp: &pt, ReceivedAt: ts},
		AssetID:    tr.AssetID.Hex(), Symbol: tr.Symbol, ProcessedAt: ts,
	})
	if err := redisx.R().ZAdd(e.ctx, config.RedisKeys.TickHistory(tr.AssetID.Hex()), redis.Z{Score: float64(ts), Member: string(member)}).Err(); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) inExpiryQueue(id string) bool {
	_, err := redisx.R().ZScore(e.ctx, config.RedisKeys.ExpiryQueue, id).Result()
	return err == nil
}

func (e *env) settlementQueue() []string {
	e.t.Helper()
	items, err := redisx.R().LRange(e.ctx, config.RedisKeys.SettlementQueue, 0, -1).Result()
	if err != nil {
		e.t.Fatal(err)
	}
	return items
}

func (e *env) load(id bson.ObjectID) *models.BinaryTrade {
	e.t.Helper()
	var tr models.BinaryTrade
	if err := mongox.C(models.CollBinaryTrades).FindOne(e.ctx, bson.M{"_id": id}).Decode(&tr); err != nil {
		e.t.Fatal(err)
	}
	return &tr
}

func (e *env) settleQueued() {
	e.t.Helper()
	for {
		id, err := redisx.R().RPop(e.ctx, config.RedisKeys.SettlementQueue).Result()
		if redisx.IsNil(err) {
			return
		}
		if err != nil {
			e.t.Fatal(err)
		}
		if _, err := settlement.SettleTrade(e.ctx, id, ""); err != nil {
			e.t.Fatalf("settle %s: %v", id, err)
		}
	}
}

func TestProcessExpiriesWaitsForPostExpiryTick(t *testing.T) {
	e := newEnv(t)
	exp := util.NowMs() - 300
	tr := e.trade(100, exp)
	e.tick(tr, 101, exp-20)

	res, err := ProcessExpiries(e.ctx, util.NowMs(), DefaultDueLimit)
	if err != nil || res.Waiting != 1 || res.Queued != 0 {
		t.Fatalf("res %+v err %v", res, err)
	}
	e.tick(tr, 102, exp+1)
	res, err = ProcessExpiries(e.ctx, util.NowMs(), DefaultDueLimit)
	if err != nil || res.Queued != 1 {
		t.Fatalf("res %+v err %v", res, err)
	}
	if e.inExpiryQueue(tr.ID.Hex()) || len(e.settlementQueue()) != 1 {
		t.Fatal("hand-off was not a move")
	}
}

// Leader loss mid-tick: the tick reads due trades, then (while it is paused)
// its lock lapses and another instance becomes leader. Its hand-off must be
// refused atomically; the new leader hands the trade off exactly once.
func TestLeaderLossMidTickHandOffIsFenced(t *testing.T) {
	e := newEnv(t)
	exp := util.NowMs() - 5000
	tr := e.trade(100, exp)
	e.tick(tr, 101, exp-10)
	e.tick(tr, 101, exp+10)

	name := "test-expiry-" + bson.NewObjectID().Hex()
	lockKey := config.RedisKeys.Lock("worker:" + name)
	e.keys = append(e.keys, lockKey, lockKey+":epoch")

	paused, resume := make(chan struct{}), make(chan struct{})
	result := make(chan error, 1)
	var ticks atomic.Int64
	loop := leader.New(leader.Options{Name: name, Interval: 20 * time.Millisecond, LockTTL: 5 * time.Second,
		OnTick: func(ctx context.Context) error {
			if ticks.Add(1) != 1 {
				return nil
			}
			close(paused)
			<-resume
			_, err := ProcessExpiries(ctx, util.NowMs(), DefaultDueLimit)
			result <- err
			return err
		}})
	loop.Start()
	defer loop.Stop()
	<-paused

	// Lock lapsed; a new leader holds it.
	if err := redisx.R().Set(e.ctx, lockKey, "new-leader", 5*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	close(resume)
	if err := <-result; !errors.Is(err, leader.ErrLeadershipLost) {
		t.Fatalf("stale leader's tick: %v", err)
	}
	if !e.inExpiryQueue(tr.ID.Hex()) || len(e.settlementQueue()) != 0 {
		t.Fatal("stale leader moved the trade")
	}

	// The new leader, fenced by its own token, hands it off once.
	newLeader := leader.WithFence(e.ctx, &leader.Fence{Name: name, Key: lockKey, Token: "new-leader", Epoch: 99, LeaseUntil: time.Now().Add(5 * time.Second)})
	for i := 0; i < 3; i++ {
		if _, err := ProcessExpiries(newLeader, util.NowMs(), DefaultDueLimit); err != nil {
			t.Fatal(err)
		}
	}
	if q := e.settlementQueue(); len(q) != 1 || q[0] != tr.ID.Hex() {
		t.Fatalf("settlement queue %v", q)
	}
	if _, err := SweepMissedExpiries(leader.WithFence(e.ctx, &leader.Fence{Key: lockKey, Token: "old", LeaseUntil: time.Now().Add(time.Second)}),
		util.NowMs(), DefaultSweepLimit); !errors.Is(err, leader.ErrLeadershipLost) {
		t.Fatalf("stale sweep: %v", err)
	}
	e.settleQueued()
	if got := e.load(tr.ID); got.Status != models.TradeStatusWon {
		t.Fatalf("status %s", got.Status)
	}
}

// An entry settlement rescheduled after it was read is not taken by the
// hand-off (score changed), so a deferral cannot be lost.
func TestHandOffSkipsEntryRescheduledMeanwhile(t *testing.T) {
	e := newEnv(t)
	id := bson.NewObjectID().Hex()
	if err := redisx.R().ZAdd(e.ctx, config.RedisKeys.ExpiryQueue, redis.Z{Score: 2000, Member: id}).Err(); err != nil {
		t.Fatal(err)
	}
	res, err := handOffScript.Run(e.ctx, redisx.R(),
		[]string{config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue, config.RedisKeys.ExpiryQueue},
		"0", "", id, strconv.Itoa(1000)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if moved, _ := res.([]any); len(moved) != 0 || !e.inExpiryQueue(id) || len(e.settlementQueue()) != 0 {
		t.Fatalf("moved %v", res)
	}
}

// Queue loss: both queues are wiped. The database sweep recovers every trade
// that is due or stuck from MongoDB, a not-yet-due trade is put back on the
// expiry queue rather than settled, and trade, settlement row and wallet
// reconcile - including an early close that crashed after moving money.
func TestQueueLossRebuiltFromDurableState(t *testing.T) {
	e := newEnv(t)
	now := util.NowMs()

	due := e.trade(100, now-10_000) // OPEN, expired: should WIN
	e.tick(due, 101, due.ExpiryTimestamp-10)
	e.tick(due, 101, due.ExpiryTimestamp+10)

	// Early close claimed and paid, then the process died.
	closing := e.trade(100, now+120_000)
	value := int64(115_00)
	intent := settlement.EarlyCloseIntent(closing, value, nil,
		models.SettlementEvidence{Rule: models.SettlementRuleEarlyCloseAtMarket, Kind: models.SettlementKindEarlyClose}, "customer.early_close")
	if claimed, err := settlement.ClaimEarlyClose(e.ctx, closing.ID, e.userID, &intent, settlement.EarlyCloseHardFloorMs); err != nil || claimed == nil {
		t.Fatalf("claim %v %v", claimed, err)
	}
	if _, err := wallets.SettleTradeEarlyClose(e.ctx, wallets.TradeFundsInput{
		UserID: e.userID.Hex(), AccountType: models.AccountDemo, Stake: stake, TradeID: closing.ID.Hex(),
	}, value); err != nil {
		t.Fatal(err)
	}

	// SETTLING left by an earlier version, past expiry: should LOSE.
	legacy := e.trade(100, now-50_000)
	e.tick(legacy, 99, legacy.ExpiryTimestamp-10)
	e.tick(legacy, 99, legacy.ExpiryTimestamp+10)

	future := e.trade(100, now+300_000) // not due: must stay OPEN

	stale := time.UnixMilli(now - settlement.StaleSettlingMs - 5000).UTC()
	for _, id := range []bson.ObjectID{closing.ID, legacy.ID} {
		set := bson.M{"updatedAt": stale}
		if id == legacy.ID {
			set["status"] = string(models.TradeStatusSettling)
		}
		if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(e.ctx, bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
			t.Fatal(err)
		}
	}
	if err := redisx.R().Del(e.ctx, config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue).Err(); err != nil {
		t.Fatal(err)
	}

	// A stray settlement-queue entry for the trade that is not due yet: it
	// must go back on the expiry queue for its expiry, not settle early.
	if err := redisx.R().LPush(e.ctx, config.RedisKeys.SettlementQueue, future.ID.Hex()).Err(); err != nil {
		t.Fatal(err)
	}
	if n, err := SweepMissedExpiries(e.ctx, util.NowMs(), DefaultSweepLimit); err != nil || n != 3 {
		t.Fatalf("sweep %d %v", n, err)
	}
	e.settleQueued()
	// Run the expiry pass and the sweep again: duplicates change nothing.
	if _, err := ProcessExpiries(e.ctx, util.NowMs(), DefaultDueLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := SweepMissedExpiries(e.ctx, util.NowMs(), DefaultSweepLimit); err != nil {
		t.Fatal(err)
	}
	e.settleQueued()

	want := map[bson.ObjectID]models.TradeStatus{
		due.ID: models.TradeStatusWon, closing.ID: models.TradeStatusClosedEarly,
		legacy.ID: models.TradeStatusLost, future.ID: models.TradeStatusOpen,
	}
	for id, status := range want {
		if got := e.load(id); got.Status != status {
			t.Fatalf("%s: %s, want %s", id.Hex(), got.Status, status)
		}
	}
	if score, err := redisx.R().ZScore(e.ctx, config.RedisKeys.ExpiryQueue, future.ID.Hex()).Result(); err != nil || int64(score) != future.ExpiryTimestamp {
		t.Fatalf("open trade not back on the expiry queue at its expiry: %v %v", score, err)
	}
	settled := bson.A{due.ID, closing.ID, legacy.ID}
	rows, _ := mongox.C(models.CollSettlements).CountDocuments(e.ctx, bson.M{"tradeId": bson.M{"$in": settled}})
	keys := bson.A{}
	for _, id := range settled {
		keys = append(keys, settlement.SettleKey(id.(bson.ObjectID).Hex()))
	}
	ledger, _ := mongox.C(models.CollWalletTransactions).CountDocuments(e.ctx, bson.M{"idempotencyKey": bson.M{"$in": keys}})
	if rows != 3 || ledger != 3 {
		t.Fatalf("rows %d ledger %d", rows, ledger)
	}
	w, err := wallets.GetWallet(e.ctx, e.userID.Hex(), models.AccountDemo, "")
	if err != nil {
		t.Fatal(err)
	}
	if w.Balance != balance+profit+(value-stake)-stake || w.LockedBalance != stake {
		t.Fatalf("wallet %+v", w)
	}
	// Each trade's stored outcome matches its ledger row.
	for _, id := range settled {
		tr := e.load(id.(bson.ObjectID))
		var row models.Settlement
		if err := mongox.C(models.CollSettlements).FindOne(e.ctx, bson.M{"tradeId": tr.ID}).Decode(&row); err != nil {
			t.Fatal(err)
		}
		if *tr.Result != row.Result || *tr.SettlementAmount != row.PayoutAmount-stake {
			t.Fatalf("trade %s vs row %+v", *tr.Result, row)
		}
	}
}
