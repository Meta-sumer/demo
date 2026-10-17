package exposure

import (
	"math/rand/v2"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
)

const (
	stakeS  = int64(100_00)
	profitS = int64(85_00)
)

type burstResult struct {
	ok      []*Reservation
	refused map[string]int // rule -> count
}

// burst fires n reservations at once (each retried once with the same trade
// id, as a client retry would) and returns what was granted.
func burst(f *fixture, n int, mk func(i int) ReserveInput) burstResult {
	f.t.Helper()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		res = burstResult{refused: map[string]int{}}
	)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		in := mk(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var granted *Reservation
			for attempt := 0; attempt < 2; attempt++ {
				r, out, err := Reserve(f.ctx, in)
				if err != nil {
					f.t.Error(err)
					return
				}
				switch out.Status {
				case ReserveOK:
					if attempt == 1 && granted != nil && !out.Duplicate {
						f.t.Errorf("retry of %s was not recognised as a duplicate", in.TradeID)
					}
					granted = r
				case ReserveRefused:
					if granted != nil {
						f.t.Errorf("retry of granted %s was refused", in.TradeID)
					}
					if attempt == 0 {
						mu.Lock()
						for _, rule := range out.Rules {
							res.refused[rule]++
						}
						mu.Unlock()
					}
				default:
					f.t.Errorf("reserve %s: %+v", in.TradeID, out)
				}
			}
			if granted != nil {
				mu.Lock()
				res.ok = append(res.ok, granted)
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()
	return res
}

func commitAll(f *fixture, rs []*Reservation) {
	f.t.Helper()
	for _, r := range rs {
		c, err := r.Commit(f.ctx)
		if err != nil {
			f.t.Fatal(err)
		}
		if c.Result != CommitOpened || c.Late {
			f.t.Fatalf("commit %s = %+v", r.TradeID(), c)
		}
	}
}

func input(asset, user string, dir models.Direction, expiry int64, limits ReserveLimits) ReserveInput {
	return ReserveInput{
		TradeID: bson.NewObjectID().Hex(), AssetID: asset, Symbol: "TEST", UserID: user, Direction: dir,
		Stake: stakeS, Profit: profitS, ExpiryTimestamp: expiry, Limits: limits,
	}
}

// Parallel requests that each pass on their own but together exceed a limit:
// exactly as many as fit are granted, for every limit the book enforces, and
// committing them all never breaches it.
func TestReserveConcurrentNeverBreachesLimits(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	expiry := time.Now().Add(time.Minute).UnixMilli()

	t.Run("asset gross", func(t *testing.T) {
		a := f.asset("GROSS")
		l := unlimited()
		l.AssetGross = 10 * stakeS
		res := burst(f, 40, func(i int) ReserveInput {
			d := models.DirectionUp
			if i%2 == 1 {
				d = models.DirectionDown
			}
			return input(a, f.user(), d, expiry, l)
		})
		if len(res.ok) != 10 || res.refused[RuleAssetGross] != 30 {
			t.Fatalf("granted %d (want 10), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		snap, _ := GetExposure(f.ctx, a)
		if snap.GrossExposure != 10*stakeS || snap.ReservedStake != 0 {
			t.Fatalf("gross %d reserved %d", snap.GrossExposure, snap.ReservedStake)
		}
	})

	t.Run("user exposure", func(t *testing.T) {
		a, u := f.asset("USER"), f.user()
		l := unlimited()
		l.UserExposure = 7 * stakeS
		res := burst(f, 30, func(i int) ReserveInput { return input(a, u, models.DirectionUp, expiry, l) })
		if len(res.ok) != 7 || res.refused[RuleUserExposure] != 23 {
			t.Fatalf("granted %d (want 7), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		ux, _ := GetUserExposure(f.ctx, u)
		if ux.Total != 7*stakeS {
			t.Fatalf("user total %d", ux.Total)
		}
	})

	t.Run("asset net, one side", func(t *testing.T) {
		a := f.asset("NET")
		l := unlimited()
		l.AssetNet = 5 * stakeS
		res := burst(f, 25, func(i int) ReserveInput { return input(a, f.user(), models.DirectionUp, expiry, l) })
		if len(res.ok) != 5 || res.refused[RuleAssetNet] != 20 {
			t.Fatalf("granted %d (want 5), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		snap, _ := GetExposure(f.ctx, a)
		if snap.NetExposure != 5*stakeS {
			t.Fatalf("net %d", snap.NetExposure)
		}
	})

	t.Run("asset net, both sides, any subset commits", func(t *testing.T) {
		a := f.asset("NET2")
		l := unlimited()
		l.AssetNet = 2 * stakeS
		res := burst(f, 40, func(i int) ReserveInput {
			d := models.DirectionUp
			if i%2 == 1 {
				d = models.DirectionDown
			}
			return input(a, f.user(), d, expiry, l)
		})
		// Commit a random subset, release the rest: the net limit holds for
		// whichever reservations finally become trades.
		rand.Shuffle(len(res.ok), func(i, j int) { res.ok[i], res.ok[j] = res.ok[j], res.ok[i] })
		keep := res.ok[:len(res.ok)/2]
		for _, r := range res.ok[len(res.ok)/2:] {
			if err := r.Release(f.ctx); err != nil {
				t.Fatal(err)
			}
		}
		commitAll(f, keep)
		snap, _ := GetExposure(f.ctx, a)
		if n := snap.NetExposure; n > l.AssetNet || n < -l.AssetNet {
			t.Fatalf("net %d beyond %d (granted %d)", n, l.AssetNet, len(res.ok))
		}
	})

	t.Run("platform gross", func(t *testing.T) {
		// Room for exactly 12 more on top of what earlier subtests committed.
		before, _ := GetPortfolioExposure(f.ctx)
		a1, a2 := f.asset("P1"), f.asset("P2")
		l := unlimited()
		l.PlatformGross = before.TotalGrossExposure + 12*stakeS
		res := burst(f, 40, func(i int) ReserveInput {
			a := a1
			if i%2 == 1 {
				a = a2
			}
			return input(a, f.user(), models.DirectionUp, expiry, l)
		})
		if len(res.ok) != 12 || res.refused[RulePlatformGross] != 28 {
			t.Fatalf("granted %d (want 12), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		p, _ := GetPortfolioExposure(f.ctx)
		if p.TotalGrossExposure != l.PlatformGross {
			t.Fatalf("platform gross %d, limit %d", p.TotalGrossExposure, l.PlatformGross)
		}
	})

	t.Run("expiry liability", func(t *testing.T) {
		a := f.asset("LIAB")
		l := unlimited()
		l.ExpiryLiability = 5 * profitS
		res := burst(f, 20, func(i int) ReserveInput { return input(a, f.user(), models.DirectionUp, expiry, l) })
		if len(res.ok) != 5 || res.refused[RuleExpiryLiability] != 15 {
			t.Fatalf("granted %d (want 5), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		snap, _ := GetExposure(f.ctx, a)
		if snap.ExpiryLiability != 5*profitS {
			t.Fatalf("expiry liability %d", snap.ExpiryLiability)
		}
	})

	t.Run("asset liability across expiries", func(t *testing.T) {
		a := f.asset("ALIAB")
		l := unlimited()
		l.AssetLiability = 3 * profitS
		res := burst(f, 20, func(i int) ReserveInput {
			// Every trade in its own bucket: nothing nets.
			return input(a, f.user(), models.DirectionUp, expiry+int64(i)*LiabilityBucketMs, l)
		})
		if len(res.ok) != 3 || res.refused[RuleAssetLiability] != 17 {
			t.Fatalf("granted %d (want 3), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		snap, _ := GetExposure(f.ctx, a)
		if snap.ExpiryLiability != 3*profitS {
			t.Fatalf("asset liability %d", snap.ExpiryLiability)
		}
	})

	t.Run("platform liability", func(t *testing.T) {
		before, _ := GetPortfolioExposure(f.ctx)
		a1, a2 := f.asset("PL1"), f.asset("PL2")
		l := unlimited()
		l.PlatformLiability = before.TotalExpiryLiability + 4*profitS
		res := burst(f, 20, func(i int) ReserveInput {
			a := a1
			if i%2 == 1 {
				a = a2
			}
			return input(a, f.user(), models.DirectionDown, expiry, l)
		})
		if len(res.ok) != 4 || res.refused[RulePlatformLiability] != 16 {
			t.Fatalf("granted %d (want 4), refused %v", len(res.ok), res.refused)
		}
		commitAll(f, res.ok)
		p, _ := GetPortfolioExposure(f.ctx)
		if p.TotalExpiryLiability != l.PlatformLiability {
			t.Fatalf("platform liability %d, limit %d", p.TotalExpiryLiability, l.PlatformLiability)
		}
	})

	// Committed reservations are counted from their trade ids; nothing was
	// written to MongoDB here, so check only the internal consistency.
	snap, err := takeSnapshot(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if drift := checkInternal(snap); len(drift) > 0 {
		t.Fatalf("book inconsistent after the bursts: %v", drift)
	}
}

// Reservations survive a rebuild (a restart of the book) and keep counting:
// what was held before the rebuild plus what is granted after never exceeds
// the limit.
func TestReservationsSurviveRebuild(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a := f.asset("SURV")
	l := unlimited()
	l.AssetGross = 6 * stakeS
	expiry := time.Now().Add(time.Minute).UnixMilli()

	first := burst(f, 4, func(i int) ReserveInput { return input(a, f.user(), models.DirectionUp, expiry, l) })
	if len(first.ok) != 4 {
		t.Fatalf("granted %d", len(first.ok))
	}
	f.rebuild()
	second := burst(f, 10, func(i int) ReserveInput { return input(a, f.user(), models.DirectionUp, expiry, l) })
	if len(second.ok) != 2 {
		t.Fatalf("after the rebuild %d were granted on top of 4 held, want 2", len(second.ok))
	}
	commitAll(f, append(first.ok, second.ok...))
	snap, _ := GetExposure(f.ctx, a)
	if snap.GrossExposure != 6*stakeS {
		t.Fatalf("gross %d", snap.GrossExposure)
	}
}

// Every per-trade operation is idempotent and order-independent.
func TestBookOperationsAreIdempotent(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	a, u := f.asset("IDEM"), f.user()
	expiry := time.Now().Add(time.Minute).UnixMilli()
	gross := func() int64 {
		snap, err := GetExposure(f.ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return snap.GrossExposure
	}

	// Reserve twice, commit twice, close twice.
	in := input(a, u, models.DirectionUp, expiry, unlimited())
	r, _, err := Reserve(f.ctx, in)
	if err != nil || r == nil {
		t.Fatal(err)
	}
	if _, out, _ := Reserve(f.ctx, in); !out.Duplicate {
		t.Fatalf("second reserve = %+v", out)
	}
	if snap, _ := GetExposure(f.ctx, a); snap.ReservedStake != stakeS || snap.ReservedTrades != 1 {
		t.Fatalf("reserved %d/%d", snap.ReservedStake, snap.ReservedTrades)
	}
	if c, _ := r.Commit(f.ctx); c.Result != CommitOpened {
		t.Fatalf("commit = %+v", c)
	}
	if c, _ := r.Commit(f.ctx); c.Result != CommitDup {
		t.Fatalf("second commit = %+v", c)
	}
	if _, out, _ := Reserve(f.ctx, in); out.Status != ReserveConflict {
		t.Fatalf("reserve after commit = %+v", out)
	}
	if gross() != stakeS {
		t.Fatalf("gross %d", gross())
	}
	if res, _ := CloseTrade(f.ctx, in.TradeID, u); res != CloseClosed {
		t.Fatalf("close = %s", res)
	}
	if res, _ := CloseTrade(f.ctx, in.TradeID, u); res != CloseMissing {
		t.Fatalf("second close = %s", res)
	}
	if gross() != 0 {
		t.Fatalf("gross after close %d", gross())
	}

	// Reordered: the release lands before the commit.
	in2 := input(a, u, models.DirectionDown, expiry, unlimited())
	r2, _, _ := Reserve(f.ctx, in2)
	if res, _ := CloseTrade(f.ctx, in2.TradeID, u); res != CloseMissing {
		t.Fatalf("early close = %s", res)
	}
	if c, _ := r2.Commit(f.ctx); c.Result != CommitClosed {
		t.Fatalf("commit after close = %+v", c)
	}
	if snap, _ := GetExposure(f.ctx, a); snap.GrossExposure != 0 || snap.ReservedStake != 0 {
		t.Fatalf("after reordered pair: %+v", snap)
	}

	// Releases without a trade id (older callers): duplicates do not
	// double-decrement, two identical trades are released one each.
	twin := func() string {
		id := bson.NewObjectID().Hex()
		if _, err := CommitTrade(f.ctx, OpenTrade{TradeID: id, AssetID: a, UserID: u, Direction: models.DirectionUp,
			Stake: stakeS, Profit: profitS, ExpiryTimestamp: expiry}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	twin()
	twin()
	legacy := ExposureDelta{AssetID: a, Symbol: "TEST", UserID: u, Direction: models.DirectionUp, Stake: stakeS, Profit: profitS, Sign: -1}
	for i, want := range []int64{stakeS, 0, 0} {
		if _, err := ApplyExposureDelta(f.ctx, legacy); err != nil {
			t.Fatal(err)
		}
		if gross() != want {
			t.Fatalf("after legacy release %d gross %d, want %d", i+1, gross(), want)
		}
	}

	// Releases with a trade id through ApplyExposureDelta.
	id := twin()
	withID := legacy
	withID.TradeID = id
	for i := 0; i < 3; i++ {
		if _, err := ApplyExposureDelta(f.ctx, withID); err != nil {
			t.Fatal(err)
		}
	}
	if gross() != 0 {
		t.Fatalf("gross after repeated release %d", gross())
	}
	if st := f.status(); st.Status != BookReady {
		t.Fatalf("book %+v", st)
	}
}

// A request that dies holding a reservation: the reservation lapses and the
// sweeper settles it against MongoDB - committed when the trade was written,
// released when it was not.
func TestLapsedReservationsAreSweptAgainstMongo(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	prevTTL, prevMargin := ReservationTTL, ReservationReleaseMargin
	ReservationTTL, ReservationReleaseMargin = 50*time.Millisecond, 0
	t.Cleanup(func() { ReservationTTL, ReservationReleaseMargin = prevTTL, prevMargin })

	a := f.asset("SWEEP")
	written := tradeSpec{asset: a, user: f.user(), dir: models.DirectionUp, stake: stakeS, profit: profitS,
		expiry: time.Now().Add(time.Minute).UnixMilli()}
	writtenIn := input(a, written.user, written.dir, written.expiry, unlimited())
	if _, out, _ := Reserve(f.ctx, writtenIn); out.Status != ReserveOK {
		t.Fatal(out)
	}
	oid, _ := bson.ObjectIDFromHex(writtenIn.TradeID)
	f.insertTradeWithID(oid, written) // inserted, then the process "crashed" before committing

	lost := input(a, f.user(), models.DirectionDown, written.expiry, unlimited())
	lostResv, out, _ := Reserve(f.ctx, lost) // crashed before inserting
	if out.Status != ReserveOK {
		t.Fatal(out)
	}

	time.Sleep(120 * time.Millisecond)
	rep, err := SweepReservations(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Committed) != 1 || rep.Committed[0] != writtenIn.TradeID || len(rep.Released) != 1 || rep.Released[0] != lost.TradeID {
		t.Fatalf("sweep = %+v", rep)
	}
	if held, _ := lostResv.Renew(f.ctx); held {
		t.Fatal("a swept reservation must not renew")
	}
	f.assertExact("after sweep")
}
