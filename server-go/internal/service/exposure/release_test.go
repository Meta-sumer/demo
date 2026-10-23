package exposure

import (
	"strings"
	"testing"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/redisx"
)

// recordIDs lists the per-trade record keys of a user in the book.
func (f *fixture) recordIDs(user string) map[string]bool {
	f.t.Helper()
	keys, err := redisx.R().HKeys(f.ctx, f.ns+":ot:"+user).Result()
	if err != nil {
		f.t.Fatal(err)
	}
	out := map[string]bool{}
	for _, k := range keys {
		out[k] = true
	}
	return out
}

func (f *fixture) release(id string, s tradeSpec) string {
	f.t.Helper()
	if _, err := ApplyExposureDelta(f.ctx, ExposureDelta{AssetID: s.asset, Symbol: "TEST", UserID: s.user, Direction: s.dir,
		Stake: s.stake, Profit: s.profit, Sign: -1, TradeID: id, ExpiryTimestamp: s.expiry}); err != nil {
		f.t.Fatal(err)
	}
	res, err := ReleaseTrade(f.ctx, f.open(id, s)) // a repeat: must change nothing
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// TestReleaseIsByTradeID: settlement and Sell now release a trade's exposure
// by its id. Of two open trades identical in every field, releasing one
// never touches the other's record, and a repeated release is a no-op.
func TestReleaseIsByTradeID(t *testing.T) {
	f := newFixture(t)
	a, u := f.asset("REL"), f.user()
	f.rebuild()
	expiry := time.Now().Add(time.Minute).UnixMilli()
	s := tradeSpec{asset: a, user: u, dir: models.DirectionUp, stake: 100_00, profit: 85_00, expiry: expiry}
	first, second := f.insertTrade(s), f.insertTrade(s)
	for _, id := range []string{first, second} {
		if res, err := CommitTrade(f.ctx, f.open(id, s)); err != nil || res.Result != CommitOpened {
			t.Fatalf("commit %s: %+v %v", id, res, err)
		}
	}
	f.assertExact("two identical trades")

	// The SECOND trade settles: its own record goes, the first stays.
	f.settleInMongo(second, 0)
	if res := f.release(second, s); res != ReleaseDone {
		t.Fatalf("repeat release: %q, want %q", res, ReleaseDone)
	}
	recs := f.recordIDs(u)
	if recs[second] || !recs[first] || len(recs) != 1 {
		t.Fatalf("records after releasing %s: %v", second, recs)
	}
	f.assertExact("after one release and its repeat")

	// A release that arrives before the commit is remembered: the late
	// commit is ignored.
	early := f.insertTrade(s)
	f.settleInMongo(early, 0)
	if res, err := ReleaseTrade(f.ctx, f.open(early, s)); err != nil || res != CloseMissing {
		t.Fatalf("early release %q %v", res, err)
	}
	if res, err := CommitTrade(f.ctx, f.open(early, s)); err != nil || res.Result != CommitClosed {
		t.Fatalf("late commit %+v %v", res, err)
	}
	f.assertExact("release before commit")
}

// TestReleaseOfALegacyTrade: a trade counted before the book keyed records
// by trade id sits under an anonymous record. Released by its id, that
// legacy record is closed exactly once; a trade with the same fields that
// was committed by id is never taken in its place.
func TestReleaseOfALegacyTrade(t *testing.T) {
	f := newFixture(t)
	a, u := f.asset("LEG"), f.user()
	f.rebuild()
	expiry := time.Now().Add(time.Minute).UnixMilli()
	s := tradeSpec{asset: a, user: u, dir: models.DirectionDown, stake: 250_00, profit: 205_00, expiry: expiry}

	// The legacy trade: opened through the old path, without its id.
	legacy := f.insertTrade(s)
	if _, err := ApplyExposureDelta(f.ctx, ExposureDelta{AssetID: a, Symbol: "TEST", UserID: u, Direction: s.dir,
		Stake: s.stake, Profit: s.profit, Sign: 1, ExpiryTimestamp: expiry}); err != nil {
		t.Fatal(err)
	}
	// A current trade with identical fields, committed by id.
	current := f.insertTrade(s)
	if _, err := CommitTrade(f.ctx, f.open(current, s)); err != nil {
		t.Fatal(err)
	}
	var anon string
	for id := range f.recordIDs(u) {
		if strings.HasPrefix(id, "anon-") {
			anon = id
		}
	}
	if anon == "" {
		t.Fatal("the legacy trade has no anonymous record")
	}
	f.assertExact("legacy and current trade")

	f.settleInMongo(legacy, 0)
	res, err := ReleaseTrade(f.ctx, f.open(legacy, s))
	if err != nil || res != ReleaseLegacy {
		t.Fatalf("legacy release %q %v", res, err)
	}
	if res := f.release(legacy, s); res != ReleaseDone {
		t.Fatalf("repeat legacy release %q", res)
	}
	recs := f.recordIDs(u)
	if recs[anon] || !recs[current] || len(recs) != 1 {
		t.Fatalf("records after the legacy release: %v", recs)
	}
	f.assertExact("after the legacy release and its repeat")

	// The current trade settles normally, by its own id.
	f.settleInMongo(current, 0)
	if res, err := ReleaseTrade(f.ctx, f.open(current, s)); err != nil || res != CloseClosed {
		t.Fatalf("current release %q %v", res, err)
	}
	if len(f.recordIDs(u)) != 0 {
		t.Fatal("records left")
	}
	f.assertExact("all settled")

	// A trade that was never counted and has no legacy twin: nothing moves.
	stray := f.insertTrade(s)
	f.settleInMongo(stray, 0)
	if res, err := ReleaseTrade(f.ctx, f.open(stray, s)); err != nil || res != CloseMissing {
		t.Fatalf("stray release %q %v", res, err)
	}
	f.assertExact("stray release")
}
