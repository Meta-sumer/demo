package marketdata

import (
	"math"
	"testing"
)

// Market-data normalisation (owner's specification section 4) and the
// volatility estimator (section 5).

func q(mid float64, ts int64) NormQuote {
	half := mid * 0.5e-4 / 2 // 0.5 bps spread
	return NormQuote{Bid: mid - half, Ask: mid + half, Ts: ts}
}

func check(t *testing.T, n *Normalizer, quote NormQuote, now int64, want string) NormResult {
	t.Helper()
	res := n.Check(quote, now, DefaultNormParams())
	got := "accepted"
	if !res.Accepted {
		got = res.Reason
	}
	if got != want {
		t.Fatalf("quote %+v at %d: %s, want %s", quote, now, got, want)
	}
	return res
}

func TestNormalizationChecksInOrder(t *testing.T) {
	var n Normalizer
	check(t, &n, q(100, 1000), 1000, "accepted")
	check(t, &n, NormQuote{Bid: 0, Ask: 1, Ts: 1100}, 1100, RejectInvalidBook)
	check(t, &n, NormQuote{Bid: 101, Ask: 100, Ts: 1100}, 1100, RejectInvalidBook)
	check(t, &n, NormQuote{Bid: math.NaN(), Ask: 100, Ts: 1100}, 1100, RejectInvalidBook)
	check(t, &n, q(100, 2101), 1100, RejectFutureTimestamp)                        // ts > now + 1 s
	check(t, &n, q(100, 2100), 1100, "accepted")                                   // exactly now + 1 s
	check(t, &n, q(100, 1000), 3101, RejectStale)                                  // now - ts > 2 s
	check(t, &n, NormQuote{Bid: 99.95, Ask: 100.05, Ts: 2200}, 2200, RejectSpread) // 10 bps > 8
	check(t, &n, NormQuote{Bid: 99.961, Ask: 100.039, Ts: 2200}, 2200, "accepted") // 7.8 bps
	check(t, &n, q(100, 2200), 2300, RejectOutOfOrder)                             // duplicate ts
	check(t, &n, q(100, 2150), 2300, RejectOutOfOrder)                             // older
}

func TestRejectedQuoteNeverChangesState(t *testing.T) {
	var n Normalizer
	check(t, &n, q(100, 1000), 1000, "accepted")
	before := *n.Last()
	for _, bad := range []NormQuote{{Bid: -1, Ask: 1, Ts: 1100}, q(100, 999), q(100, 5000), {Bid: 90, Ask: 110, Ts: 1100}} {
		n.Check(bad, 1100, DefaultNormParams())
	}
	if *n.Last() != before || n.PendingCount() != 0 {
		t.Fatalf("state changed: %+v pending %d", *n.Last(), n.PendingCount())
	}
}

func TestOutlierFilter(t *testing.T) {
	var n Normalizer
	check(t, &n, q(100, 1000), 1000, "accepted")
	// A single bad tick (+2%) is held and rejected; the level is unchanged
	// and the next normal quote clears it.
	check(t, &n, q(102, 1100), 1100, RejectOutlierUnconfirmed)
	if n.Last().Mid() != 100 || n.PendingCount() != 1 {
		t.Fatal("outlier changed state")
	}
	check(t, &n, q(100.01, 1200), 1200, "accepted")
	if n.PendingCount() != 0 {
		t.Fatal("a normal quote clears pending outliers")
	}
	// A real gap: three agreeing quotes confirm the new level, re-baselined.
	check(t, &n, q(103, 1300), 1300, RejectOutlierUnconfirmed)
	check(t, &n, q(103.1, 1400), 1400, RejectOutlierUnconfirmed)
	res := check(t, &n, q(103.05, 1500), 1500, "accepted")
	if !res.Rebaselined || n.Last().Mid() != 103.05 {
		t.Fatalf("confirmation %+v", res)
	}
	// Disagreeing outliers keep only the latest.
	check(t, &n, q(110, 1600), 1600, RejectOutlierUnconfirmed)
	check(t, &n, q(96, 1700), 1700, RejectOutlierUnconfirmed)
	check(t, &n, q(111, 1800), 1800, RejectOutlierInconsistent)
	if n.PendingCount() != 1 {
		t.Fatalf("pending %d after inconsistent", n.PendingCount())
	}
}

func TestPendingOutliersAgeOut(t *testing.T) {
	var n Normalizer
	check(t, &n, q(100, 1000), 1000, "accepted")
	check(t, &n, q(103, 1100), 1100, RejectOutlierUnconfirmed)
	check(t, &n, q(103, 1200), 1200, RejectOutlierUnconfirmed)
	// The third agreeing quote arrives after the first two aged out (5 s):
	// it starts over instead of confirming against stale evidence.
	n.Seed(NormQuote{Bid: 99.99, Ask: 100.01, Ts: 6000})
	check(t, &n, q(103, 6100), 6100, RejectOutlierUnconfirmed)
	check(t, &n, q(103, 6200), 6200, RejectOutlierUnconfirmed)
	if n.PendingCount() != 2 {
		t.Fatalf("pending %d", n.PendingCount())
	}
	// Ageing without a reseed.
	check(t, &n, q(103, 12000), 12000, RejectOutlierUnconfirmed)
	if n.PendingCount() != 1 {
		t.Fatalf("aged pending %d", n.PendingCount())
	}
}

// TestDuplicateAndOutOfOrderFloods: a flood of copies of one bad print (or
// replays of older quotes) can neither confirm a level nor move state.
func TestDuplicateAndOutOfOrderFloods(t *testing.T) {
	var n Normalizer
	check(t, &n, q(100, 1000), 1000, "accepted")
	check(t, &n, q(105, 1100), 1100, RejectOutlierUnconfirmed)
	for i := 0; i < 50; i++ {
		check(t, &n, q(105, 1100), 1100+int64(i), RejectOutOfOrder)
	}
	if n.PendingCount() != 1 || n.Last().Mid() != 100 {
		t.Fatalf("flood confirmed a level: pending %d last %v", n.PendingCount(), n.Last().Mid())
	}
	for i := int64(0); i < 50; i++ {
		check(t, &n, q(100, 900-i), 1200, RejectOutOfOrder)
	}
	check(t, &n, q(100.02, 1300), 1300, "accepted")
}

// TestProviderOutage: after a silence, quotes stamped in the past are stale
// and rejected; the feed recovers with the first fresh quote.
func TestProviderOutage(t *testing.T) {
	var n Normalizer
	check(t, &n, q(100, 1000), 1000, "accepted")
	// A burst of buffered quotes delivered 10 s late.
	for ts := int64(1100); ts < 2000; ts += 100 {
		check(t, &n, q(100, ts), 11000, RejectStale)
	}
	check(t, &n, q(100.01, 11000), 11000, "accepted")
}

func TestEWMAVolatility(t *testing.T) {
	const lambda = 0.97
	v := NewVolState(1e-4)
	near := func(name string, got, want float64) {
		t.Helper()
		if math.Abs(got-want) > 1e-15 {
			t.Fatalf("%s = %.17g, want %.17g", name, got, want)
		}
	}
	near("var0", v.Var, 1e-8)
	if v.Observe(1000, 100, lambda, 0) {
		t.Fatal("the first quote only anchors")
	}
	// r = ln(100.1 / 100), dt = 2 s: var = 0.97 * 1e-8 + 0.03 * r^2 / 2.
	r := math.Log(100.1 / 100)
	v.Observe(3000, 100.1, lambda, 0)
	near("var1", v.Var, 0.97*1e-8+0.03*r*r/2)
	// Spaced observations: a quote 500 ms after the anchor is not one.
	w := NewVolState(1e-4)
	w.Observe(0, 100, lambda, 1000)
	if w.Observe(500, 101, lambda, 1000) || w.N != 0 {
		t.Fatal("observation inside the sample interval")
	}
	if !w.Observe(1000, 100.05, lambda, 1000) {
		t.Fatal("observation at the interval")
	}
	r2 := math.Log(100.05 / 100)
	near("spaced", w.Var, 0.97*1e-8+0.03*r2*r2/1)
	// A confirmed level shift is not a return.
	before := w.Var
	w.Rebaseline(2000, 110)
	w.Observe(3000, 110.011, lambda, 1000)
	r3 := math.Log(110.011 / 110)
	near("after rebaseline", w.Var, 0.97*before+0.03*r3*r3)
	if w.Sigma() != math.Sqrt(w.Var) {
		t.Fatal("sigma")
	}
}
