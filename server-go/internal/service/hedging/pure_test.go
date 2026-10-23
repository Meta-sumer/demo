package hedging

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/service/hedging/providers"
)

var btcRules = models.HedgeRules{StepE8: 100_000, MinQtyE8: 100_000, MaxQtyE8: 120 * models.QtyScale, MinNotional: 100, TickSize: 0.1}

func TestPlanExecution(t *testing.T) {
	// INR 2,32,320 at 60,000 USDT and 88 INR/USDT = 0.044 BTC exactly.
	target := int64(23_232_000)
	p := PlanExecution(target, 0, 60_000, 88, btcRules, 50_000, 1_000_00)
	if p.TargetQtyE8 != 4_400_000 || p.ExecutableQtyE8 != 4_400_000 || p.Side != providers.SideBuy || p.ResidualQtyE8 != 0 || p.ReduceOnly {
		t.Fatalf("plan %+v", p)
	}
	// Step rounding leaves a residual, never rounds up.
	p = PlanExecution(target+10_000, 0, 60_000, 88, btcRules, 50_000, 1_000_00)
	if p.ExecutableQtyE8 != 4_400_000 || p.ResidualQtyE8 <= 0 {
		t.Fatalf("rounding %+v", p)
	}
	// Inside the rebalance band: nothing.
	p = PlanExecution(target, 4_390_000, 60_000, 88, btcRules, 50_000, 1_000_00)
	if p.ExecutableQtyE8 != 0 || !strings.Contains(p.Reason, "rebalance band") {
		t.Fatalf("band %+v", p)
	}
	// A reduction is sent reduce-only, which lifts the minimum notional.
	p = PlanExecution(0, 100_000, 60_000, 88, btcRules, 50_000, 0)
	if p.ExecutableQtyE8 != 100_000 || !p.ReduceOnly || p.Side != providers.SideSell {
		t.Fatalf("reduce %+v", p)
	}
	// An opening order below the minimum notional is not sent.
	p = PlanExecution(QtyNotional(100_000), 0, 60_000, 88, models.HedgeRules{StepE8: 100_000, MinQtyE8: 100_000, MaxQtyE8: 1e10, MinNotional: 100}, 50_000, 0)
	if p.ExecutableQtyE8 != 0 || p.Reason != "below the venue minimum notional" {
		t.Fatalf("min notional %+v", p)
	}
	// Big targets are worked in clips of HEDGE_MAX_ORDER_USDT.
	p = PlanExecution(int64(1_000_000_000_00), 0, 60_000, 88, btcRules, 50_000, 0)
	if p.ExecutableQtyE8 != 83_300_000 || !strings.Contains(p.Reason, "clipped") {
		t.Fatalf("clip %+v", p)
	}
	// Flipping is not reduce-only.
	p = PlanExecution(-target, 4_400_000, 60_000, 88, btcRules, 1e9, 0)
	if p.ReduceOnly || p.ExecutableQtyE8 != 8_800_000 || p.Side != providers.SideSell {
		t.Fatalf("flip %+v", p)
	}
	if p := PlanExecution(target, 0, 0, 88, btcRules, 0, 0); p.Reason != "no venue price" {
		t.Fatalf("no price %+v", p)
	}
}

// QtyNotional is the INR notional of a quantity at 60,000 and 88 (test aid).
func QtyNotional(qtyE8 int64) int64 { return NotionalForQty(qtyE8, 60_000, 88) }

// workedFills is the F-09 worked example of hedge-lifecycle.md section 7:
// two opening fills, a partial close, a close that flips short, and the
// buy-back - at two INR rates. Fees are 5 bps of each fill's quote.
func workedFills() []FoldFill {
	fee := func(price float64, qtyE8 int64) int64 {
		return providers.FloatToE8(price * providers.E8ToFloat(qtyE8) * 0.0005)
	}
	return []FoldFill{
		{TradeTime: 1, TradeID: 101, Side: providers.SideBuy, QtyE8: 5_000_000, Price: 60_000, FeeUsdtE8: fee(60_000, 5_000_000), InrPerUsdt: 88},
		{TradeTime: 2, TradeID: 102, Side: providers.SideBuy, QtyE8: 3_000_000, Price: 60_100, FeeUsdtE8: fee(60_100, 3_000_000), InrPerUsdt: 88},
		{TradeTime: 3, TradeID: 103, Side: providers.SideSell, QtyE8: 2_000_000, Price: 60_300, FeeUsdtE8: fee(60_300, 2_000_000), InrPerUsdt: 88.5},
		{TradeTime: 4, TradeID: 104, Side: providers.SideSell, QtyE8: 8_000_000, Price: 59_900, FeeUsdtE8: fee(59_900, 8_000_000), InrPerUsdt: 88.5},
		{TradeTime: 5, TradeID: 105, Side: providers.SideBuy, QtyE8: 2_000_000, Price: 59_800, FeeUsdtE8: fee(59_800, 2_000_000), InrPerUsdt: 88.5},
	}
}

func TestRealisedPnlWorkedExample(t *testing.T) {
	fills := workedFills()
	// Delivered out of order: the fold sorts by venue trade time and id.
	shuffled := []FoldFill{fills[3], fills[0], fills[4], fills[2], fills[1]}
	SortFoldFills(shuffled)
	steps, total := FoldFills(shuffled)

	want := []struct {
		realisedE8, realisedInr, feeInr, posAfter int64
		avg                                       float64
	}{
		{0, 0, 13_200, 5_000_000, 60_000},
		{0, 0, 7_933, 8_000_000, 60_037.5},
		{525_000_000, 46_463, 5_337, 6_000_000, 60_037.5},
		{-825_000_000, -73_012, 21_205, -2_000_000, 59_900},
		{200_000_000, 17_700, 5_292, 0, 0},
	}
	var b strings.Builder
	b.WriteString("\n| # | Fill | Qty | Price | INR/USDT | Fee (USDT) | Position after | Avg after | Realised (USDT) | Realised (INR) | Fee (INR) |\n|---|---|---|---|---|---|---|---|---|---|---|\n")
	for i, s := range steps {
		w := want[i]
		if s.RealisedE8 != w.realisedE8 || s.RealisedInr != w.realisedInr || s.FeeInr != w.feeInr || s.PositionAfterE8 != w.posAfter || s.AvgPriceAfter != w.avg {
			t.Errorf("fill %d: %+v, want %+v", i+1, s, w)
		}
		f := shuffled[i]
		fmt.Fprintf(&b, "| %d | %s | %s | %.1f | %.2f | %s | %s | %.1f | %s | %.2f | %.2f |\n", i+1, f.Side,
			providers.FormatE8(f.QtyE8), f.Price, f.InrPerUsdt, providers.FormatE8(f.FeeUsdtE8),
			providers.FormatE8(s.PositionAfterE8), s.AvgPriceAfter, providers.FormatE8(s.RealisedE8),
			float64(s.RealisedInr)/100, float64(s.FeeInr)/100)
	}
	if total.QtyE8 != 0 || total.RealisedE8 != -100_000_000 || total.RealisedInr != -8_849 || total.FeesInr != 52_967 ||
		total.FeesE8 != 599_850_000 || total.FillCount != 5 {
		t.Fatalf("totals %+v", total)
	}
	// Funding paid at 88.5 and the realised hedge result of the window.
	funding := inrFromUsdtE8(-75_000_000, 88.5)
	if funding != -6_637 {
		t.Fatalf("funding %d", funding)
	}
	net := total.RealisedInr - total.FeesInr + funding
	if net != -68_453 {
		t.Fatalf("net %d", net)
	}
	fmt.Fprintf(&b, "\nTrading P&L %s USDT = INR %.2f; fees %s USDT = INR %.2f; funding -0.75 USDT = INR %.2f; realised hedge result INR %.2f\n",
		providers.FormatE8(total.RealisedE8), float64(total.RealisedInr)/100, providers.FormatE8(total.FeesE8),
		float64(total.FeesInr)/100, float64(funding)/100, float64(net)/100)
	t.Log(b.String())

	// A duplicate report of a fill never reaches the fold twice (the ledger's
	// unique key drops it); if it did, the result would differ - which is
	// why the key exists.
	dup := append(append([]FoldFill(nil), shuffled[:3]...), shuffled[2:]...)
	if _, d := FoldFills(dup); d.RealisedInr == total.RealisedInr {
		t.Fatal("a duplicated fill should change the fold; the ledger key is what prevents it")
	}
	// Unrealised is kept apart: short 0.02 at 59,900 marked at 59,700.
	if u := UnrealisedE8(-2_000_000, 59_900, 59_700); u != 400_000_000 {
		t.Fatalf("unrealised %d", u)
	}
}

func TestComputeCostBps(t *testing.T) {
	snap := VenueSnapshot{Bid: 60_000, Ask: 60_000.1, FundingRate: 0.0001, TakerRate: 0.0005}
	c, fee, spread := ComputeCostBps(snap, 300, 5, 2, 1)
	if fee != "venue" || spread != "venue" || c.TakerFeeBps != 5 || c.SlippageBps != 2 {
		t.Fatalf("%+v %s %s", c, fee, spread)
	}
	// half spread = 0.1 / 60000.05 / 2 = 0.0083 bps; funding 1 bp x 300/28800.
	if c.HalfSpreadBps != 0.0083 || c.FundingBps != 0.0104 || c.PerSideBps != 7.0083 || c.RoundTripBps != 14.0271 {
		t.Fatalf("cost %+v", c)
	}
	// No venue fee and no book: configured fallbacks.
	c, fee, spread = ComputeCostBps(VenueSnapshot{}, 0, 4, 2, 1)
	if fee != "config" || spread != "config" || c.PerSideBps != 6.5 || c.RoundTripBps != 13 {
		t.Fatalf("fallback %+v", c)
	}
}

func TestSelectFxRate(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	day := int64(86_400_000)
	r := SelectFxRate(0, 0, nil, now.UnixMilli(), day)
	if !r.Missing || r.Usable() || r.Reason() != "FX_MISSING" {
		t.Fatalf("missing %+v", r)
	}
	r = SelectFxRate(88, now.Add(-2*time.Hour).UnixMilli(), nil, now.UnixMilli(), day)
	if !r.Usable() || r.Source != "env" || r.AgeMs != 2*3_600_000 {
		t.Fatalf("env %+v", r)
	}
	// The newer observation wins, from either source.
	stored := &models.HedgeFxRate{Rate: 88.4, Source: "manual", AsOf: now.Add(-time.Hour)}
	r = SelectFxRate(88, now.Add(-2*time.Hour).UnixMilli(), stored, now.UnixMilli(), day)
	if r.Rate != 88.4 || r.Source != "admin:manual" {
		t.Fatalf("stored %+v", r)
	}
	// Stale: hedging stops rather than guessing.
	r = SelectFxRate(88, now.Add(-25*time.Hour).UnixMilli(), nil, now.UnixMilli(), day)
	if r.Usable() || !r.Stale || r.Reason() != "FX_STALE" {
		t.Fatalf("stale %+v", r)
	}
}

func TestEntryDelta(t *testing.T) {
	// P = 1,850 INR, sigma 8.904e-5/sqrt(s) (50% a year), 60 s.
	d := EntryDelta(185_000, 8.904e-5, 60, 10)
	if d < 106_000_000 || d > 108_000_000 {
		t.Fatalf("entry delta %d", d)
	}
	// Inside the gamma window tau is floored at the window.
	if EntryDelta(185_000, 8.904e-5, 5, 10) != EntryDelta(185_000, 8.904e-5, 10, 10) {
		t.Fatal("gamma window floor")
	}
}
