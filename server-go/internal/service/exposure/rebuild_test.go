package exposure

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
)

// seed writes open LIVE trades straight to MongoDB with distinct stakes.
func seed(f *fixture, assets, users []string, n int) {
	f.t.Helper()
	base := time.Now().Add(time.Minute).UnixMilli()
	for i := 0; i < n; i++ {
		d := models.DirectionUp
		if i%3 == 0 {
			d = models.DirectionDown
		}
		stake := stakeS + int64(i)*10
		f.insertTrade(tradeSpec{
			asset: assets[i%len(assets)], user: users[i%len(users)], dir: d,
			stake: stake, profit: stake * 85 / 100, expiry: base + int64(i%4)*LiabilityBucketMs,
			createdAgo: time.Minute,
		})
	}
}

// Deleted or evicted counters put the book in safe mode (LIVE reservations
// refused) instead of being read as an empty book; the rebuild restores it
// exactly from MongoDB.
func TestSafeModeWhenCountersAreDeleted(t *testing.T) {
	f := newFixture(t)
	assets := []string{f.asset("S1"), f.asset("S2"), f.asset("S3")}
	users := []string{f.user(), f.user(), f.user(), f.user()}
	seed(f, assets, users, 12)
	f.rebuild()
	f.assertExact("after the first rebuild")

	rdb := redisx.R()
	scenarios := []struct {
		name string
		user string
		del  func()
	}{
		{"asset counters", users[1], func() { rdb.Del(f.ctx, "exp:asset:"+assets[0]) }},
		{"user counters", users[0], func() { rdb.Del(f.ctx, "exp:user:"+users[0]) }},
		{"user trade records", users[2], func() { rdb.Del(f.ctx, f.ns+":ot:"+users[2]) }},
		{"expiry buckets", users[1], func() { rdb.Del(f.ctx, f.ns+":bkt:"+assets[1]) }},
		{"user mirror", users[3], func() { rdb.Del(f.ctx, f.ns+":users") }},
		{"asset registry", users[3], func() { rdb.Del(f.ctx, f.ns+":assets") }},
		{"state", users[3], func() { rdb.Del(f.ctx, f.ns+":state") }},
		{"every key", users[3], func() {
			f.cleanup()
		}},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			sc.del()
			in := input(assets[2], sc.user, models.DirectionUp, time.Now().Add(time.Minute).UnixMilli(), unlimited())
			r, out, err := Reserve(f.ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != ReserveUncertain || r != nil {
				t.Fatalf("reserve with %s deleted = %+v, want UNCERTAIN", sc.name, out)
			}
			if st := f.status(); st.Status == BookReady {
				t.Fatalf("book still READY with %s deleted", sc.name)
			}
			if err := EnsureBookReady(f.ctx, 5*time.Second); err != nil {
				t.Fatal(err)
			}
			f.assertExact("after recovering from deleted " + sc.name)
			r, out, err = Reserve(f.ctx, in)
			if err != nil || out.Status != ReserveOK {
				t.Fatalf("reserve after recovery = %+v %v", out, err)
			}
			_ = r.Release(f.ctx)
		})
	}
}

// Trading continues while the book is rebuilt over and over: commits and
// releases (with and without trade ids, duplicated, and a release racing
// ahead of its commit) land before, during and after each rebuild. With no
// reconciliation in between, the counters still equal MongoDB exactly.
func TestRebuildDuringConcurrentTradingIsExact(t *testing.T) {
	f := newFixture(t)
	assets := []string{f.asset("C1"), f.asset("C2"), f.asset("C3")}
	users := make([]string, 8)
	for i := range users {
		users[i] = f.user()
	}
	seed(f, assets, users, 30)
	f.rebuild()

	type live struct {
		id   string
		spec tradeSpec
	}
	var (
		mu       sync.Mutex
		open     []live
		stakeSeq int64 = 10_000
		stop           = make(chan struct{})
		wg       sync.WaitGroup
		errs     = make(chan error, 64)
	)
	// Seeded trades can be closed too.
	cur, _ := mongox.C(models.CollBinaryTrades).Find(f.ctx, openTradeFilter())
	var seeded []models.BinaryTrade
	_ = cur.All(f.ctx, &seeded)
	for _, s := range seeded {
		open = append(open, live{s.ID.Hex(), tradeSpec{asset: s.AssetID.Hex(), user: s.UserID.Hex(), dir: s.Direction, stake: s.Stake, profit: s.Profit, expiry: s.ExpiryTimestamp}})
	}
	fail := func(err error) {
		select {
		case errs <- err:
		default:
		}
	}

	settle := func(id string) error {
		oid, _ := bson.ObjectIDFromHex(id)
		now := time.Now().UTC()
		_, err := mongox.C(models.CollBinaryTrades).UpdateOne(context.Background(), bson.M{"_id": oid},
			bson.M{"$set": bson.M{"status": string(models.TradeStatusWon), "settledAt": now, "updatedAt": now}})
		return err
	}
	release := func(l live, withID bool) error {
		d := ExposureDelta{AssetID: l.spec.asset, Symbol: "TEST", UserID: l.spec.user, Direction: l.spec.dir,
			Stake: l.spec.stake, Profit: l.spec.profit, Sign: -1}
		if withID {
			d.TradeID = l.id
		}
		_, err := ApplyExposureDelta(context.Background(), d)
		return err
	}

	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(uint64(w), 7))
			for {
				select {
				case <-stop:
					return
				default:
				}
				mu.Lock()
				n := len(open)
				mu.Unlock()
				if n < 5 || rng.IntN(10) < 6 {
					// Open: reserve -> insert -> commit, as acceptTrade does.
					mu.Lock()
					stakeSeq++
					stake := stakeSeq
					mu.Unlock()
					d := models.DirectionUp
					if rng.IntN(2) == 0 {
						d = models.DirectionDown
					}
					spec := tradeSpec{asset: assets[rng.IntN(len(assets))], user: users[rng.IntN(len(users))], dir: d,
						stake: stake, profit: stake * 9 / 10, expiry: time.Now().Add(time.Minute).UnixMilli() + int64(rng.IntN(5))*LiabilityBucketMs}
					oid := bson.NewObjectID()
					in := ReserveInput{TradeID: oid.Hex(), AssetID: spec.asset, Symbol: "TEST", UserID: spec.user, Direction: d,
						Stake: spec.stake, Profit: spec.profit, ExpiryTimestamp: spec.expiry, Limits: unlimited()}
					r, out, err := Reserve(context.Background(), in)
					if err != nil {
						fail(err)
						return
					}
					if out.Status != ReserveOK {
						time.Sleep(time.Millisecond) // safe mode while rebuilding
						continue
					}
					now := time.Now().UTC()
					uid, _ := bson.ObjectIDFromHex(spec.user)
					aid, _ := bson.ObjectIDFromHex(spec.asset)
					if _, err := mongox.C(models.CollBinaryTrades).InsertOne(context.Background(), models.BinaryTrade{
						ID: oid, Reference: "BT_c" + oid.Hex(), UserID: uid, AssetID: aid, Symbol: "TEST", Direction: d,
						AccountType: models.AccountLive, Stake: spec.stake, Currency: "INR", Profit: spec.profit,
						ExpiryTimestamp: spec.expiry, Status: models.TradeStatusOpen, CreatedAt: now, UpdatedAt: now,
					}); err != nil {
						fail(err)
						return
					}
					l := live{oid.Hex(), spec}
					if rng.IntN(15) == 0 {
						// Settled and released before the commit lands.
						if err := settle(l.id); err != nil {
							fail(err)
							return
						}
						if err := release(l, true); err != nil {
							fail(err)
							return
						}
						if c, err := r.Commit(context.Background()); err != nil || c.Result != CommitClosed {
							fail(fmt.Errorf("commit after release = %+v, %v", c, err))
							return
						}
						continue
					}
					if _, err := r.Commit(context.Background()); err != nil {
						fail(err)
						return
					}
					mu.Lock()
					open = append(open, l)
					mu.Unlock()
					continue
				}
				// Close: MongoDB first, then the release - sometimes twice.
				mu.Lock()
				if len(open) == 0 {
					mu.Unlock()
					continue
				}
				i := rng.IntN(len(open))
				l := open[i]
				open = append(open[:i], open[i+1:]...)
				mu.Unlock()
				if err := settle(l.id); err != nil {
					fail(err)
					return
				}
				withID := rng.IntN(2) == 0
				times := 1 + rng.IntN(2)
				for k := 0; k < times; k++ {
					if err := release(l, withID); err != nil {
						fail(err)
						return
					}
				}
			}
		}(w)
	}

	rebuilds, journalled := 0, 0
	for i := 0; i < 6; i++ {
		time.Sleep(30 * time.Millisecond)
		res, err := RebuildBook(f.ctx, RebuildOptions{Reason: "test", Force: true, Wait: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		if res.Rebuilt {
			rebuilds++
		}
		journalled += res.Journal
	}
	time.Sleep(30 * time.Millisecond)
	close(stop)
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatal(err)
	default:
	}
	if rebuilds != 6 {
		t.Fatalf("%d of 6 rebuilds completed", rebuilds)
	}
	// Commits and releases really did land while rebuilds were running.
	if journalled == 0 {
		t.Fatal("no mutation was journalled during any rebuild; the race was not exercised")
	}
	t.Logf("%d mutations replayed from rebuild journals", journalled)
	f.assertExact("after trading through 6 rebuilds")

	n, _ := mongox.C(models.CollBinaryTrades).CountDocuments(f.ctx, bson.M{})
	if n < 60 {
		t.Fatalf("only %d trades were written; the test did not trade through the rebuilds", n)
	}
}

// The periodic reconciliation compares the book with MongoDB and repairs
// what a crash leaves behind, rebuilds after tampering, and audits it.
func TestReconcileRepairsDrift(t *testing.T) {
	f := newFixture(t)
	f.setGrace(0)
	a, u := f.asset("R1"), f.user()
	f.rebuild()
	expiry := time.Now().Add(time.Minute).UnixMilli()

	// Committed normally.
	ok := tradeSpec{asset: a, user: u, dir: models.DirectionUp, stake: 100_00, profit: 85_00, expiry: expiry, createdAgo: time.Minute}
	okID := f.insertTrade(ok)
	if _, err := CommitTrade(f.ctx, f.open(okID, ok)); err != nil {
		t.Fatal(err)
	}
	// Written, but the process died before committing (reservation lost).
	lostCommit := f.insertTrade(tradeSpec{asset: a, user: u, dir: models.DirectionDown, stake: 70_00, profit: 60_00, expiry: expiry, createdAgo: time.Minute})
	// Settled in MongoDB, but the release never ran.
	settled := tradeSpec{asset: a, user: u, dir: models.DirectionDown, stake: 50_00, profit: 40_00, expiry: expiry, createdAgo: time.Minute}
	settledID := f.insertTrade(settled)
	if _, err := CommitTrade(f.ctx, f.open(settledID, settled)); err != nil {
		t.Fatal(err)
	}
	f.settleInMongo(settledID, time.Minute)
	// Counted, but no such trade exists.
	ghost := bson.NewObjectID().Hex()
	if _, err := CommitTrade(f.ctx, OpenTrade{TradeID: ghost, AssetID: a, UserID: u, Direction: models.DirectionUp, Stake: 10_00, Profit: 9_00, ExpiryTimestamp: expiry}); err != nil {
		t.Fatal(err)
	}

	rep, err := Reconcile(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rebuilt || len(rep.Drift) > 0 {
		t.Fatalf("per-trade drift must be repaired in place: %+v", rep)
	}
	if len(rep.Opened) != 1 || rep.Opened[0] != lostCommit {
		t.Fatalf("opened %v, want [%s]", rep.Opened, lostCommit)
	}
	if len(rep.Closed) != 2 {
		t.Fatalf("closed %v, want the settled trade and the ghost", rep.Closed)
	}
	f.assertExact("after repairing per-trade drift")

	// A counter changed outside the scripts: rebuilt.
	redisx.R().HIncrBy(f.ctx, "exp:asset:"+a, "upStake", 777)
	rep, err = Reconcile(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Rebuilt || len(rep.Drift) == 0 {
		t.Fatalf("tampered counter: %+v", rep)
	}
	f.assertExact("after rebuilding a tampered book")

	// A disagreement younger than the grace period may be in flight.
	f.setGrace(time.Hour)
	young := f.insertTrade(tradeSpec{asset: a, user: u, dir: models.DirectionUp, stake: 20_00, profit: 17_00, expiry: expiry})
	if rep, _ = Reconcile(f.ctx); len(rep.Opened) != 0 {
		t.Fatalf("a trade inside the grace period was repaired: %+v", rep)
	}
	f.setGrace(0)
	if rep, _ = Reconcile(f.ctx); len(rep.Opened) != 1 || rep.Opened[0] != young {
		t.Fatalf("opened %v, want [%s]", rep.Opened, young)
	}
	f.assertExact("after the grace period")

	if rep, _ = Reconcile(f.ctx); rep.Repaired() || len(rep.Drift) > 0 {
		t.Fatalf("a consistent book was repaired: %+v", rep)
	}
	n, err := mongox.C(models.CollAuditLogs).CountDocuments(f.ctx, bson.M{"action": AuditBookReconciled})
	if err != nil || n < 3 {
		t.Fatalf("%d reconciliation audit rows (%v)", n, err)
	}
	if st := f.status(); st.LastReconciledAt == nil {
		t.Fatal("lastReconciledAt not recorded")
	}
}

// A rebuild that lost its generation (its lock lapsed and another rebuild
// took over) cannot swap its stale copy in; a rebuild that died mid-way
// leaves the book in safe mode until the next one completes.
func TestSupersededAndAbandonedRebuilds(t *testing.T) {
	f := newFixture(t)
	assets := []string{f.asset("G1"), f.asset("G2")}
	users := []string{f.user(), f.user()}
	seed(f, assets, users, 8)
	f.rebuild()

	stale := bson.NewObjectID().Hex()
	if err := rebuildBeginScript.Run(f.ctx, redisx.R(), nil, f.ns, stale, nowArg()).Err(); err != nil {
		t.Fatal(err)
	}
	// The stale rebuilder "died" here: the book stays in safe mode.
	if _, out, _ := Reserve(f.ctx, input(assets[0], users[0], models.DirectionUp, time.Now().Add(time.Minute).UnixMilli(), unlimited())); out.Status != ReserveUncertain {
		t.Fatalf("reserve during an abandoned rebuild = %+v", out)
	}
	// Nobody holds the lock, so the next caller rebuilds.
	if err := EnsureBookReady(f.ctx, time.Second); err != nil {
		t.Fatal(err)
	}
	res, err := rebuildSwapScript.Run(f.ctx, redisx.R(), nil, f.ns, stale, nowArg()).Slice()
	if err != nil {
		t.Fatal(err)
	}
	if parts := strs(res); parts[0] != "ABORTED" {
		t.Fatalf("stale swap = %v", parts)
	}
	f.assertExact("after a superseded rebuild")
}

// Booting an instance verifies a READY book instead of wiping it: counters
// and in-flight reservations of instances already trading are untouched.
func TestBootCheckKeepsAConsistentBook(t *testing.T) {
	f := newFixture(t)
	assets := []string{f.asset("B1")}
	users := []string{f.user(), f.user()}
	seed(f, assets, users, 5)
	if err := EnsureBookOnBoot(f.ctx); err != nil { // first boot: no book yet
		t.Fatal(err)
	}
	f.assertExact("after the first boot")
	gen := f.status().Generation

	inFlight, out, _ := Reserve(f.ctx, input(assets[0], users[0], models.DirectionUp, time.Now().Add(time.Minute).UnixMilli(), unlimited()))
	if out.Status != ReserveOK {
		t.Fatal(out)
	}
	if err := EnsureBookOnBoot(f.ctx); err != nil { // a second instance boots
		t.Fatal(err)
	}
	if st := f.status(); st.Generation != gen || st.PendingReservations != 1 {
		t.Fatalf("second boot rebuilt or dropped reservations: %+v", st)
	}
	if held, _ := inFlight.Renew(f.ctx); !held {
		t.Fatal("in-flight reservation lost on boot")
	}
	_ = inFlight.Release(f.ctx)

	redisx.R().HIncrBy(f.ctx, "exp:asset:"+assets[0], "downProfit", 5)
	if err := EnsureBookOnBoot(f.ctx); err != nil {
		t.Fatal(err)
	}
	if f.status().Generation == gen {
		t.Fatal("an inconsistent book was not rebuilt at boot")
	}
	f.assertExact("after the boot rebuild")
}
