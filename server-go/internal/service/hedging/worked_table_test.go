package hedging

import (
	"context"
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/hedging/binancefake"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/risk"
)

// Worked hedge table (hedging-spec.md section 8; audit F-07): target,
// executable quantity, estimated cost, the fills the (fake) venue actually
// gave, and the residual, for several prices, expiries, payout rates and
// volatility states. Run with -v to print the markdown pasted in the spec:
//
//	go test ./internal/service/hedging/ -run TestWorkedHedgeTable -v -timeout 5m

const (
	tableRate     = 88.0         // INR per USDT
	tableSpot     = 60_000.0     // BTCUSDT
	tableMaxHedge = 1_000_000_00 // asset maxHedgeNotional, INR 10 lakh
	tableMultiple = 100.0        // HEDGE_MAX_LIABILITY_MULTIPLE
	tableH        = 0.95         // MAX_ALLOWED_HEDGE band
	tableGamma    = int64(10)    // minSecondsToExpiryForHedge
	yearSeconds   = 31_536_000.0 // sigma per sqrt(second) = annual / sqrt(year)
	tableNow      = int64(1_800_000_000_000)
)

func annualToSecond(a float64) float64 { return a / math.Sqrt(yearSeconds) }

type tableRow struct {
	label  string
	spot   float64
	sigma  float64
	legs   []models.HedgeLeg
	trades []exposure.LiabilityTrade
}

type tableResult struct {
	delta, liability, raw, target, capV int64
	cappedBy                            string
	plan                                Plan
	estCost                             int64
	estPerSideBps                       float64
	fillAvg                             float64
	actualCost                          int64
	actualPerSideBps                    float64
	held, residualDelta                 int64
}

// runRow computes the row and executes its order against the fake venue.
func runRow(t *testing.T, ctx context.Context, b *providers.Binance, f *binancefake.Fake, r tableRow, n int) tableResult {
	t.Helper()
	var out tableResult
	d, _, _ := risk.BookDelta(r.legs, r.spot, r.sigma, tableNow, tableGamma)
	out.delta = int64(util.RoundHalfUp(d))
	_, out.liability = exposure.ComputeLiability(r.trades)
	raw := tableH * float64(out.delta)
	out.raw = int64(util.RoundHalfUp(raw))
	out.target, out.capV, out.cappedBy = risk.CapTarget(raw, tableMaxHedge, tableMultiple, out.liability)

	bid, ask := r.spot, r.spot+0.1
	f.SetBook("BTCUSDT", bid, ask)
	snap := VenueSnapshot{Bid: bid, Ask: ask, TakerRate: 0.0005, FundingRate: 0.0001}
	mid := snap.Mid()
	out.plan = PlanExecution(out.target, 0, mid, tableRate, btcRules, 50_000, 1_000_00)
	cost, _, _ := ComputeCostBps(snap, 0, 5, 2, 1)
	out.estPerSideBps = cost.PerSideBps
	out.estCost = int64(util.RoundHalfUp(float64(NotionalForQty(out.plan.ExecutableQtyE8, mid, tableRate)) * cost.PerSideBps / 10_000))
	if out.plan.ExecutableQtyE8 > 0 {
		st, err := b.PlaceOrder(ctx, providers.OrderRequest{Symbol: "BTCUSDT", Side: out.plan.Side, QtyE8: out.plan.ExecutableQtyE8,
			ClientOrderID: fmt.Sprintf("tbl-%d", n)})
		if err != nil {
			t.Fatal(err)
		}
		fills, err := b.Fills(ctx, "BTCUSDT", st.VenueOrderID)
		if err != nil {
			t.Fatal(err)
		}
		var qty, quote, fee int64
		for _, fl := range fills {
			qty, quote, fee = qty+fl.QtyE8, quote+fl.QuoteE8, fee+fl.CommissionE8
		}
		out.fillAvg = float64(quote) / float64(qty)
		slipUsdt := math.Abs(out.fillAvg-mid) * providers.E8ToFloat(qty)
		costUsdt := slipUsdt + providers.E8ToFloat(fee)
		out.actualCost = int64(util.RoundHalfUp(costUsdt * tableRate * 100))
		out.actualPerSideBps = costUsdt / (providers.E8ToFloat(qty) * mid) * 10_000
		out.held = sgn(out.plan.OrderQtyE8) * NotionalForQty(qty, mid, tableRate)
	}
	out.residualDelta = out.delta - out.held
	return out
}

func inr(paise int64) string {
	neg := paise < 0
	if neg {
		paise = -paise
	}
	s := fmt.Sprintf("%d", (paise+50)/100)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func singleTrade(label string, payoutPct float64, expiry int64, annualVol, moneyness float64) tableRow {
	stake := int64(1_000_00)
	profit := util.CalculateProfit(stake, payoutPct)
	return tableRow{
		label: label, spot: tableSpot * (1 + moneyness), sigma: annualToSecond(annualVol),
		legs:   []models.HedgeLeg{{Dir: 1, Payout: stake + profit, Strike: tableSpot, ExpiryMs: tableNow + expiry*1000}},
		trades: []exposure.LiabilityTrade{{Direction: models.DirectionUp, Stake: stake, Profit: profit, ExpiryTimestamp: tableNow + expiry*1000}},
	}
}

// seededBook is 60 open trades, 60% UP, stakes INR 500-5,000, strikes within
// +-0.15% of 60,000, expiries 30 s - 30 min, payout 85%.
func seededBook(spot, sigma float64, label string) tableRow {
	rng := rand.New(rand.NewPCG(7, 11))
	r := tableRow{label: label, spot: spot, sigma: sigma}
	expiries := []int64{30, 60, 300, 900, 1800}
	for i := 0; i < 60; i++ {
		stake := int64(500_00 + rng.IntN(46)*100_00)
		profit := util.CalculateProfit(stake, 85)
		strike := tableSpot * (1 + (rng.Float64()*2-1)*0.0015)
		exp := tableNow + (expiries[rng.IntN(len(expiries))]-int64(rng.IntN(25)))*1000
		dir, d := int8(1), models.DirectionUp
		if rng.Float64() >= 0.6 {
			dir, d = -1, models.DirectionDown
		}
		r.legs = append(r.legs, models.HedgeLeg{Dir: dir, Payout: stake + profit, Strike: strike, ExpiryMs: exp})
		r.trades = append(r.trades, exposure.LiabilityTrade{Direction: d, Stake: stake, Profit: profit, ExpiryTimestamp: exp})
	}
	return r
}

func TestWorkedHedgeTable(t *testing.T) {
	f := binancefake.New()
	defer f.Close()
	f.Set(func(f *binancefake.Fake) { f.Splits = 2; f.Clock = time.Now })
	b := providers.NewBinance(providers.BinanceConfig{Env: "testnet", BaseURL: f.URL(), APIKey: f.APIKey, APISecret: f.Secret,
		RecvWindow: 5 * time.Second, Timeout: 5 * time.Second})
	ctx := context.Background()

	normal, calm, stressed := 0.5, 0.3, 1.0
	single := []tableRow{
		singleTrade("A1 30 s, 85%, 50%/yr, at the money", 85, 30, normal, 0),
		singleTrade("A2 60 s, 85%, 50%/yr, at the money", 85, 60, normal, 0),
		singleTrade("A3 5 min, 85%, 50%/yr, at the money", 85, 300, normal, 0),
		singleTrade("A4 30 min, 85%, 50%/yr, at the money", 85, 1800, normal, 0),
		singleTrade("B1 60 s, 85%, calm 30%/yr", 85, 60, calm, 0),
		singleTrade("B2 60 s, 85%, stressed 100%/yr", 85, 60, stressed, 0),
		singleTrade("C1 60 s, payout 80%", 80, 60, normal, 0),
		singleTrade("C2 60 s, payout 87%", 87, 60, normal, 0),
		singleTrade("D1 60 s, price 0.1% below strike", 85, 60, normal, -0.001),
		singleTrade("D2 60 s, price 0.1% above strike", 85, 60, normal, 0.001),
		singleTrade("E1 8 s to expiry (gamma window)", 85, 8, normal, 0),
	}
	var md strings.Builder
	md.WriteString("\nTable 1 - one UP trade, stake INR 1,000 (h = 0.95, cap = min(INR 10 lakh, 100 x liability), INR 88/USDT, BTCUSDT 60,000.0/60,000.1)\n\n")
	md.WriteString("| Scenario | S | Delta-equiv. D (INR) | Liability L (INR) | h x D (INR) | Target (INR) | Capped by | Target qty | Executable qty | Est. cost (INR, bps/side) | Fill avg | Actual cost (INR, bps/side) | Residual delta (INR) |\n")
	md.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	n := 0
	for _, r := range single {
		n++
		res := runRow(t, ctx, b, f, r, n)
		writeRow(&md, r, res)
		// The executable quantity never exceeds the target's quantity, and
		// a capped target is exactly the cap.
		if absI(res.plan.ExecutableQtyE8) > absI(res.plan.TargetQtyE8) {
			t.Errorf("%s: executable above target", r.label)
		}
		if res.cappedBy != "" && absI(res.target) != res.capV {
			t.Errorf("%s: capped target %d cap %d", r.label, res.target, res.capV)
		}
	}
	// The gamma window: a trade 8 s from expiry carries no target.
	if res := runRow(t, ctx, b, f, single[len(single)-1], 99); res.delta != 0 || res.plan.ExecutableQtyE8 != 0 {
		t.Fatalf("gamma window row %+v", res)
	}
	// Shorter expiry, larger delta (the gamma problem of short binaries).
	r30, r1800 := runRow(t, ctx, b, f, single[0], 100), runRow(t, ctx, b, f, single[3], 101)
	if !(r30.delta > r1800.delta*5) {
		t.Fatalf("delta 30 s %d vs 30 min %d", r30.delta, r1800.delta)
	}

	md.WriteString("\nTable 2 - a 60-trade book (60% UP, stakes INR 500-5,000, strikes within +-0.15%, expiries 30 s-30 min), same caps\n\n")
	md.WriteString("| Scenario | S | Delta-equiv. D (INR) | Liability L (INR) | h x D (INR) | Target (INR) | Capped by | Target qty | Executable qty | Est. cost (INR, bps/side) | Fill avg | Actual cost (INR, bps/side) | Residual delta (INR) |\n")
	md.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	books := []tableRow{
		seededBook(tableSpot*0.998, annualToSecond(normal), "F1 book, price -0.2%, 50%/yr"),
		seededBook(tableSpot, annualToSecond(normal), "F2 book, at 60,000, 50%/yr"),
		seededBook(tableSpot*1.002, annualToSecond(normal), "F3 book, price +0.2%, 50%/yr"),
		seededBook(tableSpot, annualToSecond(stressed), "F4 book, at 60,000, stressed 100%/yr"),
		seededBook(tableSpot, annualToSecond(calm), "F5 book, at 60,000, calm 30%/yr"),
	}
	for _, r := range books {
		n++
		res := runRow(t, ctx, b, f, r, n)
		writeRow(&md, r, res)
		if res.liability <= 0 {
			t.Fatalf("%s: liability %d", r.label, res.liability)
		}
	}
	md.WriteString("\nResidual delta = D - filled hedge notional: what stays unhedged by policy (1 - h, the caps) and by the venue's quantity step.\n")
	// Residual risk at expiry: the book's payoff (every trade settles at
	// S_T) against the hedge's P&L, with the default caps and with the
	// uncapped delta, for A2 (one 60 s trade) and F2 (the book).
	md.WriteString("\nTable 3 - platform result at expiry for a move of the price to S_T (INR; book = binary payoffs, hedge = held notional x move)\n\n")
	md.WriteString("| Case | Move | Book | Hedge (default caps) | Net (default caps) | Hedge (uncapped h x D) | Net (uncapped) |\n|---|---|---|---|---|---|---|\n")
	for _, c := range []struct {
		name string
		row  tableRow
	}{{"A2 one 60 s trade", single[1]}, {"F2 60-trade book", books[1]}} {
		n++
		res := runRow(t, ctx, b, f, c.row, n)
		for _, move := range []float64{-0.01, -0.005, -0.001, 0.001, 0.005, 0.01} {
			st := c.row.spot * (1 + move)
			var book int64
			for i, tr := range c.row.trades {
				win := st > c.row.legs[i].Strike
				if tr.Direction != models.DirectionUp {
					win = st < c.row.legs[i].Strike
				}
				if win {
					book -= tr.Profit
				} else {
					book += tr.Stake
				}
			}
			hedge := int64(util.RoundHalfUp(float64(res.held) * move))
			full := int64(util.RoundHalfUp(float64(res.raw) * move))
			fmt.Fprintf(&md, "| %s | %+.1f%% | %s | %s | %s | %s | %s |\n", c.name, move*100, inr(book), inr(hedge), inr(book+hedge), inr(full), inr(book+full))
			// With the target at most M x L, a move m changes the hedge by at
			// most M x L x |m|: the caps bound the hedge's own risk.
			if bound := int64(math.Ceil(tableMultiple*float64(res.liability)*math.Abs(move))) + 1; c.row.label[0] == 'A' && absI(hedge) > bound {
				t.Errorf("%s %v: capped hedge P&L %d beyond %d", c.name, move, hedge, bound)
			}
		}
	}
	t.Log(md.String())
}

func writeRow(md *strings.Builder, r tableRow, res tableResult) {
	capped := res.cappedBy
	if capped == "" {
		capped = "-"
	}
	fill, actual := "-", "-"
	if res.fillAvg > 0 {
		fill = fmt.Sprintf("%.2f", res.fillAvg)
		actual = fmt.Sprintf("%s (%.2f)", inr(res.actualCost), res.actualPerSideBps)
	}
	fmt.Fprintf(md, "| %s | %.1f | %s | %s | %s | %s | %s | %s | %s | %s (%.2f) | %s | %s | %s |\n",
		r.label, r.spot, inr(res.delta), inr(res.liability), inr(res.raw), inr(res.target), capped,
		providers.FormatE8(res.plan.TargetQtyE8), providers.FormatE8(res.plan.ExecutableQtyE8),
		inr(res.estCost), res.estPerSideBps, fill, actual, inr(res.residualDelta))
}
