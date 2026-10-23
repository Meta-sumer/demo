package exposure

import (
	"testing"
	"time"

	"binarytrading/internal/models"
)

// The pricing specification's per-side limit (7.2 final hard check),
// enforced atomically by the reservation: concurrent UP trades each pass on
// their own, but only as many as keep L(UP) = UP profits - DOWN stakes within
// the limit are granted; the opposite side is never refused for it; and the
// quote-time reader sees committed trades plus live reservations.
func TestSideLiabilityIsAtomic(t *testing.T) {
	f := newFixture(t)
	f.rebuild()
	expiry := time.Now().Add(time.Minute).UnixMilli()
	a := f.asset("SIDE")
	l := unlimited()
	l.SideLiability = 5 * profitS
	res := burst(f, 20, func(int) ReserveInput { return input(a, f.user(), models.DirectionUp, expiry, l) })
	if len(res.ok) != 5 || res.refused[RuleSideLiability] != 15 {
		t.Fatalf("granted %d (want 5), refused %v", len(res.ok), res.refused)
	}
	// Live reservations count at quote time.
	sl, err := GetSideLiability(f.ctx, a)
	if err != nil || sl.Up != 5*profitS || sl.ReservedUpProfit != 5*profitS {
		t.Fatalf("side liability with reservations %+v %v", sl, err)
	}
	commitAll(f, res.ok)
	sl, _ = GetSideLiability(f.ctx, a)
	if sl.Up != 5*profitS || sl.Down != -5*stakeS || sl.ReservedUpProfit != 0 {
		t.Fatalf("committed %+v", sl)
	}
	// The DOWN side offsets: granted although UP is at its limit.
	down := burst(f, 3, func(int) ReserveInput { return input(a, f.user(), models.DirectionDown, expiry, l) })
	if len(down.ok) != 3 {
		t.Fatalf("offsetting side refused %v", down.refused)
	}
	commitAll(f, down.ok)
	// Each DOWN stake lowered L(UP): room for UP again, exactly that much.
	sl, _ = GetSideLiability(f.ctx, a)
	if sl.Up != 5*profitS-3*stakeS {
		t.Fatalf("after offsets %+v", sl)
	}
}
