package risk

// Liability model evidence for audit finding F-04
// (docs/audit/risk/liability-and-stress.md). Books are built through the
// production entry point - ReserveTradeExposure (limits from the versioned
// risk configuration, the atomic Lua reservation) and Commit - on a
// throwaway database and the configured Redis, then read back from the book
// (exposure.GetExposure, GetLiabilityBuckets, GetSideLiability,
// GetPortfolioExposure). Every figure is asserted; the tables are printed
// with -v and pasted into the document:
//
//	go test ./internal/service/risk/ -run TestLiabilityStressScenarios -v

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/testsupport"
)

// inr is whole rupees in minor units (paise).
func inr(rupees int64) int64 { return rupees * 100 }

// money formats minor units as rupees: 1,25,000 is written 125,000.
func money(minor int64) string {
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	whole, paise := minor/100, minor%100
	s := fmt.Sprint(whole)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if paise != 0 {
		s += fmt.Sprintf(".%02d", paise)
	}
	return sign + s
}

func mdTable(header []string, rows [][]string) string {
	var b strings.Builder
	b.WriteString("| " + strings.Join(header, " | ") + " |\n|")
	for range header {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range rows {
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
	return b.String()
}

type stressTrade struct {
	asset, user string
	dir         models.Direction
	stake       int64
	payout      float64 // percent
	expiryIn    int64   // seconds after the book's anchor
	entry       float64
	profit      int64
}

type stressBook struct {
	t      *testing.T
	ctx    context.Context
	anchor int64
	assets map[string]*models.Asset
	order  []string
	users  map[string]string
	trades []stressTrade
}

// newStressBook gives a fresh database (so a fresh book and platform totals)
// with the risk configuration patched.
func newStressBook(t *testing.T, patch riskconfig.RiskConfigPatch) *stressBook {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	riskconfig.InvalidateRiskConfigCache()
	assets.InvalidateAssetCache()
	b := &stressBook{t: t, ctx: context.Background(), assets: map[string]*models.Asset{}, users: map[string]string{},
		// Expiries one hour ahead, on a whole second: bucket boundaries are
		// then exactly the offsets used below.
		anchor: (util.NowMs()/1000)*1000 + 3_600_000}
	t.Cleanup(b.cleanup)
	if _, _, err := riskconfig.UpdateRiskConfig(b.ctx, patch, bson.NewObjectID().Hex(), nil); err != nil {
		t.Fatal(err)
	}
	return b
}

func (b *stressBook) cleanup() {
	rdb := redisx.R()
	ns := exposure.BookNamespace()
	keys := []string{"lock:exposure-rebuild:" + mongox.DB().Name()}
	iter := rdb.Scan(b.ctx, 0, ns+":*", 1000).Iterator()
	for iter.Next(b.ctx) {
		keys = append(keys, iter.Val())
	}
	for _, a := range b.assets {
		keys = append(keys, "exp:asset:"+a.ID.Hex())
		rdb.SRem(b.ctx, "exp:assets", a.ID.Hex())
	}
	for _, u := range b.users {
		keys = append(keys, "exp:user:"+u)
	}
	for i := 0; i < len(keys); i += 500 {
		rdb.Del(b.ctx, keys[i:min(i+500, len(keys))]...)
	}
	riskconfig.InvalidateRiskConfigCache()
	assets.InvalidateAssetCache()
}

func (b *stressBook) asset(label, symbol string) *models.Asset {
	b.t.Helper()
	now := mongox.Now()
	a := &models.Asset{ID: bson.NewObjectID(), Symbol: symbol, Name: label, Category: "CRYPTO", Active: true,
		MinStake: inr(10), MaxStake: inr(1_000_000), SupportedExpiries: []int64{30, 60, 300, 900, 1800},
		ExposureLimit: inr(10_000_000), NetExposureLimit: inr(10_000_000), CreatedAt: now, UpdatedAt: now}
	if _, err := mongox.C(models.CollAssets).InsertOne(b.ctx, a); err != nil {
		b.t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	b.assets[label] = a
	b.order = append(b.order, label)
	return a
}

func (b *stressBook) user(label string) string {
	if id, ok := b.users[label]; ok {
		return id
	}
	id := bson.NewObjectID().Hex()
	b.users[label] = id
	return id
}

// try reserves and commits one trade through the production path; the
// error is the refusal the customer would get.
func (b *stressBook) try(tr stressTrade, sideLimit int64) error {
	tr.profit = util.CalculateProfit(tr.stake, tr.payout)
	resv, err := ReserveTradeExposure(b.ctx, ReservationRequest{
		TradeID: bson.NewObjectID().Hex(), UserID: b.user(tr.user), Asset: b.assets[tr.asset], Direction: tr.dir,
		Stake: tr.stake, Profit: tr.profit, ExpiryTimestamp: b.anchor + tr.expiryIn*1000, SideLiabilityLimit: sideLimit,
	})
	if err != nil {
		return err
	}
	if _, err := resv.Commit(b.ctx); err != nil {
		b.t.Fatal(err)
	}
	b.trades = append(b.trades, tr)
	return nil
}

func (b *stressBook) place(sideLimit int64, trades ...stressTrade) {
	b.t.Helper()
	for _, tr := range trades {
		if err := b.try(tr, sideLimit); err != nil {
			b.t.Fatalf("trade %+v refused: %v", tr, err)
		}
	}
}

// n copies of a trade.
func times(n int, tr stressTrade) []stressTrade {
	out := make([]stressTrade, n)
	for i := range out {
		out[i] = tr
	}
	return out
}

// figures is everything the book says about one asset.
type figures struct {
	snap    models.ExposureSnapshot
	side    exposure.SideLiability
	buckets []exposure.LiabilityBucket
	// Worst settle price per bucket from the trades' entry prices (computed
	// here, not by the book), summed.
	ladder int64
}

func (b *stressBook) figures(label string) figures {
	b.t.Helper()
	id := b.assets[label].ID.Hex()
	snap, err := exposure.GetExposure(b.ctx, id)
	if err != nil {
		b.t.Fatal(err)
	}
	side, err := exposure.GetSideLiability(b.ctx, id)
	if err != nil {
		b.t.Fatal(err)
	}
	buckets, err := exposure.GetLiabilityBuckets(b.ctx, id)
	if err != nil {
		b.t.Fatal(err)
	}
	f := figures{snap: snap, side: side, buckets: buckets}
	byBucket := map[int64][]stressTrade{}
	for _, tr := range b.trades {
		if tr.asset == label {
			k := tr.expiryIn
			byBucket[k] = append(byBucket[k], tr)
		}
	}
	for _, trs := range byBucket {
		f.ladder += worstSettlePrice(trs)
	}
	return f
}

// worstSettlePrice is the platform's largest net loss on trades settling on
// one price, over every settle price: below all entries, at each entry (a
// draw refunds), between consecutive entries and above them all.
func worstSettlePrice(trades []stressTrade) int64 {
	var prices []float64
	seen := map[float64]bool{}
	for _, t := range trades {
		if !seen[t.entry] {
			seen[t.entry] = true
			prices = append(prices, t.entry)
		}
	}
	sort.Float64s(prices)
	candidates := []float64{prices[0] - 1}
	for i, p := range prices {
		candidates = append(candidates, p)
		if i+1 < len(prices) {
			candidates = append(candidates, (p+prices[i+1])/2)
		}
	}
	candidates = append(candidates, prices[len(prices)-1]+1)
	var worst int64
	for _, s := range candidates {
		var loss int64
		for _, t := range trades {
			switch {
			case s == t.entry:
			case (s > t.entry) == (t.dir == models.DirectionUp):
				loss += t.profit
			default:
				loss -= t.stake
			}
		}
		worst = max(worst, loss)
	}
	return worst
}

func eq(t *testing.T, what string, got, want int64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %s, want %s", what, money(got), money(want))
	}
}

func up(asset, user string, stake int64, payout float64, expiryIn int64, entry float64) stressTrade {
	return stressTrade{asset: asset, user: user, dir: models.DirectionUp, stake: stake, payout: payout, expiryIn: expiryIn, entry: entry}
}

func down(asset, user string, stake int64, payout float64, expiryIn int64, entry float64) stressTrade {
	return stressTrade{asset: asset, user: user, dir: models.DirectionDown, stake: stake, payout: payout, expiryIn: expiryIn, entry: entry}
}

// refusal unpacks a limit refusal: its code and the violated rules' codes
// and messages.
func refusal(t *testing.T, err error) (string, []LimitViolation) {
	t.Helper()
	var ae *apperr.Error
	if !errors.As(err, &ae) {
		t.Fatalf("not an application error: %v", err)
	}
	details, _ := ae.Details.(map[string]any)
	v, _ := details["violations"].([]LimitViolation)
	return ae.Code, v
}

func TestLiabilityStressScenarios(t *testing.T) {
	t.Run("a identical stakes, different payouts", testPayoutBooks)
	t.Run("b identical stakes and payouts, different expiries", testExpiryBooks)
	t.Run("c stress scenarios on a mixed book", testStressBook)
	t.Run("d BTC and ETH do not offset", testCrossAsset)
	t.Run("e limits refuse the breaching trade atomically", testLimitRefusals)
}

// (a) Identical stakes, different payout rates: the stake measures agree,
// the liability does not.
func testPayoutBooks(t *testing.T) {
	b := newStressBook(t, riskconfig.RiskConfigPatch{})
	rows := [][]string{}
	for _, c := range []struct {
		label  string
		payout float64
		lUp    int64
	}{{"A1", 65, inr(25_000)}, {"A2", 76.5, inr(36_500)}, {"A3", 85, inr(45_000)}} {
		b.asset(c.label, "BTC/USD")
		b.place(0, times(10, up(c.label, "u1", inr(10_000), c.payout, 60, 60_000))...)
		b.place(0, times(4, down(c.label, "u2", inr(10_000), c.payout, 60, 60_000))...)
		f := b.figures(c.label)
		eq(t, c.label+" gross", f.snap.GrossExposure, inr(140_000))
		eq(t, c.label+" net", f.snap.NetExposure, inr(60_000))
		eq(t, c.label+" L(UP)", f.side.Up, c.lUp)
		eq(t, c.label+" expiry liability", f.snap.ExpiryLiability, c.lUp)
		rows = append(rows, []string{c.label, fmt.Sprintf("%g%%", c.payout), "10 UP + 4 DOWN x 10,000, one expiry",
			money(f.snap.GrossExposure), money(f.snap.NetExposure), money(f.side.Up), money(f.side.Down),
			money(f.snap.ExpiryLiability), money(f.snap.MaxPayoutLiability)})
	}
	t.Log("\n(a) Identical stakes, different payouts (INR)\n" + mdTable([]string{"Book", "Payout", "Trades",
		"Gross stake", "Net stake", "L(UP)", "L(DOWN)", "Expiry liability", "Max payout liability"}, rows))
}

// (b) Identical stakes and payouts, different expiries: UP and DOWN offset
// only when they settle on the same price (the same 1 s expiry bucket).
func testExpiryBooks(t *testing.T) {
	b := newStressBook(t, riskconfig.RiskConfigPatch{})
	type book struct {
		label, layout string
		upAt, downAt  []int64 // expiry offsets, seconds; one trade each
		want          int64
	}
	five := []int64{60, 60, 120, 120, 180, 180, 240, 240, 300, 300}
	books := []book{
		{"B1", "all at +60 s", repeat(60, 10), repeat(60, 10), 0},
		{"B2", "UP at +60 s, DOWN at +300 s", repeat(60, 10), repeat(300, 10), inr(160_000)},
		{"B3", "2 UP + 2 DOWN at each of +60 ... +300 s", five, five, 0},
		{"B4", "UP at +60 s, DOWN at +61 s", repeat(60, 10), repeat(61, 10), inr(160_000)},
	}
	rows := [][]string{}
	for _, bk := range books {
		b.asset(bk.label, "BTC/USD")
		for _, e := range bk.upAt {
			b.place(0, up(bk.label, "u1", inr(10_000), 80, e, 60_000))
		}
		for _, e := range bk.downAt {
			b.place(0, down(bk.label, "u2", inr(10_000), 80, e, 60_000))
		}
		f := b.figures(bk.label)
		eq(t, bk.label+" gross", f.snap.GrossExposure, inr(200_000))
		eq(t, bk.label+" net", f.snap.NetExposure, 0)
		eq(t, bk.label+" netted across expiries", f.snap.PotentialLiability, 0)
		eq(t, bk.label+" expiry liability", f.snap.ExpiryLiability, bk.want)
		rows = append(rows, []string{bk.label, bk.layout, money(f.snap.GrossExposure), money(f.snap.NetExposure),
			money(f.snap.PotentialLiability), fmt.Sprint(len(f.buckets)), money(f.snap.ExpiryLiability), money(f.snap.MaxPayoutLiability)})
	}
	t.Log("\n(b) 10 UP + 10 DOWN x 10,000 at 80%, different expiries (INR)\n" + mdTable([]string{"Book", "Expiries",
		"Gross stake", "Net stake", "Netted across expiries (not a limit measure)", "Buckets", "Expiry liability", "Max payout liability"}, rows))
}

func repeat(v int64, n int) []int64 {
	out := make([]int64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// (c) Stress scenarios on a book of two assets, five expiry buckets, five
// customers, three payout rates and different entry prices.
func testStressBook(t *testing.T) {
	b := newStressBook(t, riskconfig.RiskConfigPatch{})
	b.asset("BTC", "BTC/USD")
	b.asset("ETH", "ETH/USD")
	b.place(0,
		up("BTC", "c1", inr(20_000), 76.5, 60, 60_000.00),
		up("BTC", "c2", inr(10_000), 76.5, 60, 60_005.00),
		down("BTC", "c3", inr(15_000), 76.5, 60, 60_002.00),
		up("BTC", "c1", inr(25_000), 80.5, 300, 60_010.00),
		down("BTC", "c4", inr(30_000), 80.5, 300, 59_990.00),
		down("BTC", "c2", inr(10_000), 80.5, 300, 60_000.00),
		up("BTC", "c5", inr(50_000), 79.5, 900, 60_000.00),
		up("ETH", "c3", inr(20_000), 75.5, 60, 2_400.00),
		down("ETH", "c4", inr(20_000), 75.5, 60, 2_401.00),
		down("ETH", "c1", inr(40_000), 78.5, 900, 2_405.00),
	)
	btc, eth := b.figures("BTC"), b.figures("ETH")
	portfolio, err := exposure.GetPortfolioExposure(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	bucketBad := func(f figures) int64 {
		var s int64
		for _, bk := range f.buckets {
			s += max(0, bk.UpWinNet, bk.DownWinNet)
		}
		return s
	}
	// Every figure as the platform's loss (+) or gain (-).
	type scen struct {
		name     string
		btc, eth int64
		note     string
	}
	scens := []scen{
		{"Gross stake (not a loss measure)", btc.snap.GrossExposure, eth.snap.GrossExposure, "what is at risk, not what can be lost"},
		{"Net stake UP - DOWN", btc.snap.NetExposure, eth.snap.NetExposure, "which way the book leans"},
		{"All UP trades win (price up past every entry)", btc.snap.UpWinNet, eth.snap.UpWinNet, "sum of UP profits - DOWN stakes"},
		{"All DOWN trades win", btc.snap.DownWinNet, eth.snap.DownWinNet, "sum of DOWN profits - UP stakes"},
		{"Netted across expiries (asset potentialLiability)", btc.snap.PotentialLiability, eth.snap.PotentialLiability, "worse of the two lines above; assumes one price for all expiries - not used for limits"},
		{"Every bucket goes its bad way (expiry liability)", btc.snap.ExpiryLiability, eth.snap.ExpiryLiability, "the limit measure"},
		{"Worst settle price per bucket (entries differ)", btc.ladder, eth.ladder, "price lands between entries: both sides can win"},
		{"Every trade wins (max payout liability)", btc.snap.MaxPayoutLiability, eth.snap.MaxPayoutLiability, "no netting at all: the hard bound"},
	}
	rows := [][]string{}
	for _, s := range scens {
		rows = append(rows, []string{s.name, money(s.btc), money(s.eth), money(s.btc + s.eth), s.note})
	}
	t.Log("\n(c) Stress scenarios, platform loss (+) or gain (-) (INR)\n" +
		mdTable([]string{"Scenario", "BTC/USD", "ETH/USD", "Platform", "Reading"}, rows))

	brows := [][]string{}
	for _, l := range []string{"BTC", "ETH"} {
		f := b.figures(l)
		for _, bk := range f.buckets {
			brows = append(brows, []string{l, fmt.Sprintf("+%d s", (bk.ExpiryFrom-b.anchor)/1000), money(bk.UpStake), money(bk.UpProfit),
				money(bk.DownStake), money(bk.DownProfit), money(bk.UpWinNet), money(bk.DownWinNet), money(bk.Liability)})
		}
	}
	t.Log("\n(c) The book by expiry bucket (INR)\n" + mdTable([]string{"Asset", "Expiry", "UP stake", "UP profit",
		"DOWN stake", "DOWN profit", "If UP wins", "If DOWN wins", "Bucket liability"}, brows))

	// Exact figures (worked by hand in liability-and-stress.md).
	eq(t, "BTC all UP win", btc.snap.UpWinNet, inr(27_825))
	eq(t, "BTC all DOWN win", btc.snap.DownWinNet, inr(-61_325))
	eq(t, "BTC expiry liability", btc.snap.ExpiryLiability, inr(54_900))
	eq(t, "BTC bucket bad way", bucketBad(btc), btc.snap.ExpiryLiability)
	eq(t, "BTC worst settle price", btc.ladder, inr(63_725))
	eq(t, "BTC max payout", btc.snap.MaxPayoutLiability, inr(126_500))
	eq(t, "ETH expiry liability", eth.snap.ExpiryLiability, inr(31_400))
	eq(t, "ETH worst settle price", eth.ladder, inr(61_600))
	eq(t, "ETH max payout", eth.snap.MaxPayoutLiability, inr(61_600))
	eq(t, "platform expiry liability", portfolio.TotalExpiryLiability, btc.snap.ExpiryLiability+eth.snap.ExpiryLiability)
	eq(t, "platform max payout", portfolio.TotalMaxPayoutLiability, btc.snap.MaxPayoutLiability+eth.snap.MaxPayoutLiability)
	if !(btc.snap.PotentialLiability <= btc.snap.ExpiryLiability && btc.snap.ExpiryLiability <= btc.ladder &&
		btc.ladder <= btc.snap.MaxPayoutLiability) {
		t.Errorf("BTC ordering netted <= expiry <= ladder <= max payout broken: %+v", btc)
	}

	// One customer concentrated: c5 holds the largest BTC position.
	ux, err := exposure.GetUserExposure(b.ctx, b.user("c5"))
	if err != nil {
		t.Fatal(err)
	}
	c5Stake := ux.PerAsset[b.assets["BTC"].ID.Hex()]
	var c5Profit int64
	for _, tr := range b.trades {
		if tr.user == "c5" {
			c5Profit += tr.profit
		}
	}
	t.Logf("\n(c) Concentration: customer c5 holds %s of %s BTC/USD stake (%.1f%%). If c5 wins the platform pays %s "+
		"profit; c5's bucket (+900 s) holds nothing to offset it, so that bucket's liability is the same %s.",
		money(c5Stake), money(btc.snap.GrossExposure), float64(c5Stake)/float64(btc.snap.GrossExposure)*100,
		money(c5Profit), money(c5Profit))
	eq(t, "c5 profit", c5Profit, inr(39_750))
}

// burst fires n copies of a trade at once through ReserveTradeExposure (each
// with its own trade id) and commits what is granted.
func (b *stressBook) burst(n int, tr stressTrade, sideLimit int64) (granted int, refused map[string]int) {
	b.t.Helper()
	tr.profit = util.CalculateProfit(tr.stake, tr.payout)
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		resvs []*exposure.Reservation
	)
	refused = map[string]int{}
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			resv, err := ReserveTradeExposure(b.ctx, ReservationRequest{
				TradeID: bson.NewObjectID().Hex(), UserID: b.user(tr.user), Asset: b.assets[tr.asset], Direction: tr.dir,
				Stake: tr.stake, Profit: tr.profit, ExpiryTimestamp: b.anchor + tr.expiryIn*1000, SideLiabilityLimit: sideLimit,
			})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				var ae *apperr.Error
				if errors.As(err, &ae) {
					refused[ae.Message]++
				} else {
					refused[err.Error()]++
				}
				return
			}
			resvs = append(resvs, resv)
		}()
	}
	close(start)
	wg.Wait()
	for _, r := range resvs {
		if _, err := r.Commit(b.ctx); err != nil {
			b.t.Fatal(err)
		}
		b.trades = append(b.trades, tr)
	}
	return len(resvs), refused
}

// refuse asserts that the trade is refused with the code and message, and
// that the refusal held nothing: no reservation left, the book unchanged.
func (b *stressBook) refuse(tr stressTrade, sideLimit int64, code, message string) string {
	b.t.Helper()
	before := b.figures(tr.asset)
	err := b.try(tr, sideLimit)
	if err == nil {
		b.t.Fatalf("trade %+v accepted, want %s", tr, message)
	}
	got, violations := refusal(b.t, err)
	found := false
	for _, v := range violations {
		found = found || strings.Contains(v.Message, message)
	}
	if got != code || !found {
		b.t.Fatalf("refusal %s %+v, want %s %q", got, violations, code, message)
	}
	after := b.figures(tr.asset)
	st, _ := exposure.GetBookStatus(b.ctx)
	if after.snap.GrossExposure != before.snap.GrossExposure || after.snap.ExpiryLiability != before.snap.ExpiryLiability ||
		after.side != before.side || st.PendingReservations != 0 {
		b.t.Fatalf("a refused trade changed the book: before %+v after %+v pending %d", before.snap, after.snap, st.PendingReservations)
	}
	return got
}

func describe(tr stressTrade) string {
	return fmt.Sprintf("%s %s %s %s at %g%%, +%d s", tr.user, tr.asset, tr.dir, money(tr.stake), tr.payout, tr.expiryIn)
}

// (e) For each limit: the next trade that would breach it is refused in the
// atomic reservation (nothing is held), a trade that lowers the measure is
// accepted, and a burst of concurrent trades is cut exactly at the limit.
func testLimitRefusals(t *testing.T) {
	var rows [][]string
	row := func(limit, setting, before string, next stressTrade, code, other string, burst string) {
		rows = append(rows, []string{limit, setting, before, describe(next), "refused " + code, other, burst})
	}
	big := inr(100_000_000)
	unlimited := riskconfig.RiskConfigPatch{MaxUserExposure: &big}
	pct100 := 100.0

	t.Run("side L(D)", func(t *testing.T) {
		b := newStressBook(t, unlimited)
		b.asset("BTC", "BTC/USD")
		side := inr(50_000)
		b.place(side, times(5, up("BTC", "u1", inr(10_000), 85, 60, 60_000))...)
		eq(t, "L(UP)", b.figures("BTC").side.Up, inr(42_500))
		next := up("BTC", "u2", inr(10_000), 85, 60, 60_000)
		code := b.refuse(next, side, apperr.ExposureLimitExceeded, "Exposure limit reached on the UP side")
		b.place(side, down("BTC", "u3", inr(10_000), 85, 60, 60_000))
		eq(t, "L(UP) after a DOWN", b.figures("BTC").side.Up, inr(32_500))
		b.place(side, next)
		eq(t, "L(UP)", b.figures("BTC").side.Up, inr(41_000))
		n, refused := b.burst(20, up("BTC", "u4", inr(1_000), 85, 60, 60_000), side)
		if n != 10 || b.figures("BTC").side.Up != inr(49_500) {
			t.Fatalf("burst granted %d (want 10), L(UP) %s, refused %v", n, money(b.figures("BTC").side.Up), refused)
		}
		row("Per side L(UP) = UP profits - DOWN stakes (SIDE_LIABILITY)", "50,000 (pricing rule maxExposure)",
			"5 UP x 10,000 at 85%: L(UP) 42,500", next, code,
			"u3 DOWN 10,000 accepted: L(UP) 32,500; then the refused UP fits (41,000)",
			"20 concurrent UP 1,000: 10 granted, L(UP) 49,500")
	})

	t.Run("expiry bucket", func(t *testing.T) {
		limit := inr(30_000)
		p := unlimited
		p.MaxExpiryLiability = &limit
		b := newStressBook(t, p)
		b.asset("BTC", "BTC/USD")
		b.place(0, times(3, up("BTC", "u1", inr(10_000), 80, 60, 60_000))...)
		next := up("BTC", "u2", inr(10_000), 80, 60, 60_000)
		code := b.refuse(next, 0, apperr.ExposureLimitExceeded, "at this expiry")
		b.place(0, up("BTC", "u2", inr(10_000), 80, 300, 60_000))
		b.place(0, down("BTC", "u3", inr(10_000), 80, 60, 60_000))
		b.place(0, next)
		f := b.figures("BTC")
		eq(t, "asset expiry liability", f.snap.ExpiryLiability, inr(22_000+8_000))
		n, refused := b.burst(10, up("BTC", "u4", inr(10_000), 80, 120, 60_000), 0)
		if n != 3 {
			t.Fatalf("burst granted %d (want 3), refused %v", n, refused)
		}
		for _, bk := range b.figures("BTC").buckets {
			if bk.Liability > limit {
				t.Fatalf("bucket %d liability %s over the limit", bk.ExpiryFrom, money(bk.Liability))
			}
		}
		row("Expiry bucket (EXPIRY_LIABILITY)", "maxExpiryLiability 30,000", "3 UP x 10,000 at 80%, +60 s: bucket 24,000",
			next, code, "the same trade at +300 s accepted (another bucket, 8,000); u3 DOWN at +60 s accepted (bucket 14,000), then the refused UP fits (22,000)",
			"10 concurrent UP 10,000 at +120 s: 3 granted (24,000)")
	})

	t.Run("asset", func(t *testing.T) {
		limit := inr(40_000)
		p := unlimited
		p.MaxAssetLiability = &limit
		b := newStressBook(t, p)
		b.asset("BTC", "BTC/USD")
		b.place(0, times(3, up("BTC", "u1", inr(10_000), 80, 60, 60_000))...)
		b.place(0, times(2, up("BTC", "u1", inr(10_000), 80, 300, 60_000))...)
		eq(t, "asset liability", b.figures("BTC").snap.ExpiryLiability, inr(40_000))
		next := up("BTC", "u2", inr(10_000), 80, 900, 60_000)
		code := b.refuse(next, 0, apperr.ExposureLimitExceeded, "Liability limit reached for BTC/USD")
		b.place(0, down("BTC", "u3", inr(10_000), 80, 60, 60_000))
		b.place(0, next)
		eq(t, "asset liability", b.figures("BTC").snap.ExpiryLiability, inr(38_000))
		n, refused := b.burst(10, up("BTC", "u4", inr(1_000), 80, 1800, 60_000), 0)
		if n != 2 || b.figures("BTC").snap.ExpiryLiability != inr(39_600) {
			t.Fatalf("burst granted %d (want 2), liability %s, refused %v", n, money(b.figures("BTC").snap.ExpiryLiability), refused)
		}
		row("Asset, sum of its buckets (ASSET_LIABILITY)", "maxAssetLiability 40,000",
			"UP x 10,000 at 80%: 3 at +60 s, 2 at +300 s: 40,000", next, code,
			"u3 DOWN at +60 s accepted (asset 30,000), then the refused UP fits (38,000)",
			"10 concurrent UP 1,000 at +1800 s: 2 granted (39,600)")
	})

	t.Run("platform", func(t *testing.T) {
		limit := inr(50_000)
		p := unlimited
		p.MaxPlatformLiability = &limit
		b := newStressBook(t, p)
		b.asset("BTC", "BTC/USD")
		b.asset("ETH", "ETH/USD")
		b.place(0, times(5, up("BTC", "u1", inr(10_000), 80, 60, 60_000))...)
		b.place(0, up("ETH", "u2", inr(10_000), 80, 60, 2_400))
		next := up("ETH", "u2", inr(10_000), 80, 60, 2_400)
		code := b.refuse(next, 0, apperr.RiskLimitExceeded, "Platform liability limit reached")
		b.place(0, down("ETH", "u3", inr(10_000), 80, 60, 2_400))
		b.place(0, next)
		portfolio, _ := exposure.GetPortfolioExposure(b.ctx)
		eq(t, "platform liability", portfolio.TotalExpiryLiability, inr(46_000))
		n, refused := b.burst(10, up("ETH", "u4", inr(1_000), 80, 120, 2_400), 0)
		portfolio, _ = exposure.GetPortfolioExposure(b.ctx)
		if n != 5 || portfolio.TotalExpiryLiability != inr(50_000) {
			t.Fatalf("burst granted %d (want 5), platform %s, refused %v", n, money(portfolio.TotalExpiryLiability), refused)
		}
		row("Platform, sum over assets (PLATFORM_LIABILITY)", "maxPlatformLiability 50,000",
			"BTC 5 UP x 10,000 at 80% (40,000) + ETH UP 10,000 (8,000): 48,000", next, code,
			"u3 ETH DOWN accepted (platform 40,000), then the refused UP fits (46,000)",
			"10 concurrent ETH UP 1,000 at +120 s: 5 granted (50,000)")
	})

	t.Run("user", func(t *testing.T) {
		limit := inr(50_000)
		b := newStressBook(t, riskconfig.RiskConfigPatch{MaxUserExposure: &limit, MaxUserAssetConcentrationPercent: &pct100})
		b.asset("BTC", "BTC/USD")
		b.place(0, times(5, up("BTC", "u1", inr(10_000), 80, 60, 60_000))...)
		next := up("BTC", "u1", inr(10_000), 80, 60, 60_000)
		code := b.refuse(next, 0, apperr.UserLimitExceeded, "exceed your open exposure limit")
		b.refuse(down("BTC", "u1", inr(10_000), 80, 60, 60_000), 0, apperr.UserLimitExceeded, "exceed your open exposure limit")
		b.place(0, down("BTC", "u2", inr(10_000), 80, 60, 60_000))
		n, refused := b.burst(10, up("BTC", "u3", inr(10_000), 80, 60, 60_000), 0)
		if n != 5 {
			t.Fatalf("burst granted %d (want 5), refused %v", n, refused)
		}
		row("Customer open stake (USER_EXPOSURE, a stake limit)", "maxUserExposure 50,000", "u1 holds 5 UP x 10,000",
			next, code, "u1's own DOWN is refused too (it adds stake); u2 DOWN accepted",
			"10 concurrent u3 trades of 10,000: 5 granted")
	})

	t.Run("concentration", func(t *testing.T) {
		limit := inr(50_000)
		b := newStressBook(t, riskconfig.RiskConfigPatch{MaxUserExposure: &limit})
		b.asset("BTC", "BTC/USD")
		for range 2 {
			b.place(0, up("BTC", "a", inr(10_000), 80, 60, 60_000), down("BTC", "b", inr(10_000), 80, 60, 60_000),
				up("BTC", "c", inr(10_000), 80, 300, 60_000))
		}
		b.place(0, times(4, up("BTC", "u", inr(10_000), 80, 60, 60_000))...)
		next := up("BTC", "u", inr(10_000), 80, 60, 60_000)
		code := b.refuse(next, 0, apperr.RiskLimitExceeded, "Your share of the open book")
		b.place(0, times(3, down("BTC", "d", inr(10_000), 80, 300, 60_000))...)
		b.place(0, next)
		ux, _ := exposure.GetUserExposure(b.ctx, b.user("u"))
		eq(t, "u stake", ux.Total, inr(50_000))
		eq(t, "gross", b.figures("BTC").snap.GrossExposure, inr(140_000))
		row("Concentration (CONCENTRATION, share of the asset's stake)", "40%, applies once the asset's stake exceeds maxUserExposure 50,000",
			"a, b, c 20,000 each; u 40,000 (40.0% of 100,000)", next, code+" (u would hold 45.5%)",
			"d adds 30,000 (DOWN), then u's trade fits (35.7%)", "-")
	})

	t.Log("\n(e) Limits: the breaching trade is refused atomically; a reducing trade is accepted (INR)\n" + mdTable(
		[]string{"Limit", "Setting", "Book before", "Next trade", "Result", "Reducing or other trade", "Concurrent burst"}, rows))
}

// (d) BTC and ETH never offset: a long BTC book and a short ETH book are
// two liabilities, whatever the stakes net to across assets.
func testCrossAsset(t *testing.T) {
	b := newStressBook(t, riskconfig.RiskConfigPatch{})
	b.asset("BTC", "BTC/USD")
	b.asset("ETH", "ETH/USD")
	b.place(0, up("BTC", "u1", inr(50_000), 76.5, 60, 60_000), down("ETH", "u2", inr(50_000), 75.5, 60, 2_400))
	btc, eth := b.figures("BTC"), b.figures("ETH")
	portfolio, err := exposure.GetPortfolioExposure(b.ctx)
	if err != nil {
		t.Fatal(err)
	}
	upBTC, dnETH := b.trades[0], b.trades[1]
	joint := []struct {
		name     string
		btc, eth int64
	}{
		{"BTC up, ETH up (they move together)", upBTC.profit, -dnETH.stake},
		{"BTC up, ETH down (they decouple)", upBTC.profit, dnETH.profit},
		{"BTC down, ETH up", -upBTC.stake, -dnETH.stake},
		{"BTC down, ETH down", -upBTC.stake, dnETH.profit},
	}
	rows := [][]string{}
	var worst int64
	for _, j := range joint {
		rows = append(rows, []string{j.name, money(j.btc), money(j.eth), money(j.btc + j.eth)})
		worst = max(worst, j.btc+j.eth)
	}
	rows = append(rows, []string{"Platform expiry liability (limit measure)", money(btc.snap.ExpiryLiability),
		money(eth.snap.ExpiryLiability), money(portfolio.TotalExpiryLiability)})
	t.Logf("\n(d) BTC UP 50,000 at 76.5%% and ETH DOWN 50,000 at 75.5%%, same expiry; net stake across assets %s. "+
		"Platform loss (+) or gain (-) by joint outcome (INR)\n%s", money(btc.snap.NetExposure+eth.snap.NetExposure),
		mdTable([]string{"Joint outcome", "BTC/USD", "ETH/USD", "Platform"}, rows))
	eq(t, "net stake across assets", btc.snap.NetExposure+eth.snap.NetExposure, 0)
	eq(t, "platform liability", portfolio.TotalExpiryLiability, inr(76_000))
	eq(t, "worst joint outcome", worst, portfolio.TotalExpiryLiability)
}
