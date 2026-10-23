package binary

import (
	"math"
	"math/rand/v2"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/pricing"
)

// The calibrated Sell-now correction (audit F-12,
// docs/audit/pricing/early-close.md).

func TestEarlyCloseCorrectionLookup(t *testing.T) {
	rows := []models.EarlyCloseCalibrationRow{
		{SigmaScale: 1.1},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 15, MaxVolRatio: 0.33, SigmaScale: 2.5, Stickiness: 0.6},
		{Symbol: "BTC/USD", MaxSecondsRemaining: 15, SigmaScale: 1.5, Stickiness: 0.1},
		{Symbol: "BTC/USD", MaxVolRatio: 0.33, SigmaScale: 3, MinVolRatio: 0.75},
	}
	cases := []struct {
		symbol       string
		seconds, vol float64
		want         float64 // SigmaScale identifies the row
	}{
		{"BTC/USD", 10, 0.2, 2.5},
		{"BTC/USD", 15, 0.33, 2.5}, // bounds are inclusive
		{"BTC/USD", 16, 0.2, 3},
		{"BTC/USD", 10, 2, 1.5},
		{"BTC/USD", 100, 2, 1.1}, // no BTC row matches: the row for any symbol
		{"BTC/USD", 10, 0, 1.5},  // baseline unknown: rows with a ratio bound are skipped
		{"ETH/USD", 10, 0.2, 1.1},
	}
	for _, c := range cases {
		if got := EarlyCloseCorrection(rows, c.symbol, c.seconds, c.vol); got.SigmaScale != c.want {
			t.Errorf("%s %gs ratio %g: row %+v, want scale %g", c.symbol, c.seconds, c.vol, got, c.want)
		}
	}
	if id := EarlyCloseCorrection(nil, "BTC/USD", 10, 0.2); id != (models.EarlyCloseCalibrationRow{SigmaScale: 1}) {
		t.Fatalf("empty table: %+v, want the identity", id)
	}
}

// caseFor builds a position whose uncorrected z is z.
func caseFor(z, ratio, seconds float64, dir models.Direction) EarlyCloseInput {
	const baseline = 4.45e-5
	sigma := ratio * baseline
	k := z * sigma * math.Sqrt(seconds)
	cur := 100 / (1 - k) // (cur - 100) / (cur sigma sqrt(seconds)) = z
	if dir == models.DirectionDown {
		cur = 100 / (1 + k)
	}
	return EarlyCloseInput{Stake: 1000_00, PayoutPercent: 80, Direction: dir, EntryPrice: 100, CurrentPrice: cur,
		SecondsRemaining: seconds, VolatilityPerSecond: sigma, FeePercent: 5}
}

// The service's probability and the copy the fit and the back-test use are
// the same function of (z, ratio, row), for both directions, with the
// shipped table.
func TestEarlyCloseCorrectionMatchesTheFit(t *testing.T) {
	const baseline = 4.45e-5
	minP := models.DefaultEarlyCloseMinProbabilityPercent / 100
	rng := rand.New(rand.NewPCG(11, 12))
	rows := models.DefaultEarlyCloseCalibration()
	for i := 0; i < 5000; i++ {
		z := rng.NormFloat64() * 2
		ratio := math.Exp(rng.NormFloat64() * 0.8)
		seconds := 10 + rng.Float64()*1500
		dir := models.DirectionUp
		if i%2 == 1 {
			dir = models.DirectionDown
		}
		row := EarlyCloseCorrection(rows, "BTC/USD", seconds, ratio)
		in := caseFor(z, ratio, seconds, dir)
		in.SigmaScale, in.MinVolatilityPerSecond, in.Stickiness = row.SigmaScale, row.MinVolRatio*baseline, row.Stickiness
		in.MinProbabilityPercent = models.DefaultEarlyCloseMinProbabilityPercent
		got := QuoteEarlyClose(in).WinProbability
		want := math.Min(0.99, math.Max(minP, pricing.EarlyCloseWinProbability(z, ratio, row)))
		if math.Abs(got-want) > 1.5e-4 {
			t.Fatalf("z %.3f ratio %.3f %.0fs %s row %+v: service %v, fit %v", z, ratio, seconds, dir, row, got, want)
		}
	}
}

// With no table (and the 1% clamp) the price is the formula as it was.
func TestEarlyCloseNoTableIsTheUncorrectedFormula(t *testing.T) {
	for _, z := range []float64{-3, -0.4, 0, 0.2, 2.5} {
		in := caseFor(z, 0.5, 30, models.DirectionUp)
		before := QuoteEarlyClose(in)
		row := EarlyCloseCorrection(nil, "BTC/USD", 30, 0.5)
		in.SigmaScale, in.Stickiness, in.MinVolatilityPerSecond = row.SigmaScale, row.Stickiness, row.MinVolRatio
		in.MinProbabilityPercent = 1
		if after := QuoteEarlyClose(in); after != before {
			t.Fatalf("z %g: %+v, want %+v", z, after, before)
		}
		want := math.Min(0.99, math.Max(0.01, StandardNormalCdf(z)))
		if math.Abs(before.WinProbability-want) > 1e-4 {
			t.Fatalf("z %g: P %v, want Phi(z) %v", z, before.WinProbability, want)
		}
	}
}

// The live volatility (volatilityPerSecond over the tick history) is the one
// the back-test replays (pricing.EarlyCloseVolatility over the window's
// prices): ticks older than 60 s are left out, the rest are used as they are.
func TestEarlyCloseBacktestVolatilityIsTheLiveOne(t *testing.T) {
	e := newECEnv(t)
	asset := bson.NewObjectID().Hex()
	e.keys = append(e.keys, config.RedisKeys.TickHistory(asset), config.RedisKeys.LastTick(asset))
	rng := rand.New(rand.NewPCG(5, 6))
	now := util.NowMs()
	price := 60_000.0
	var inWindow []float64
	for i := 0; i < 140; i++ {
		ts := now - 75_000 + int64(i)*500
		price *= math.Exp(5e-5 * rng.NormFloat64())
		if ts > now-62_000 && ts < now-58_000 {
			continue // keep clear of the window's edge
		}
		e.writeTick(asset, "BTC/USD", price, ts, i == 139)
		if ts >= now-58_000 {
			inWindow = append(inWindow, price)
		}
	}
	got, err := volatilityPerSecond(e.ctx, asset)
	if err != nil {
		t.Fatal(err)
	}
	want := pricing.EarlyCloseVolatility(inWindow)
	if want <= 0 || math.Abs(got-want) > 1e-12*want {
		t.Fatalf("live %v, back-test %v (%d ticks)", got, want, len(inWindow))
	}
}

// The quote applies the configured table: with a row of stickiness 0.5 and
// sigma scale 2, an in-the-money position's P(win) is 0.5 + 0.5 x Phi(z / 2)
// instead of Phi(z), z from the live volatility.
func TestQuoteAppliesTheEarlyCloseCalibration(t *testing.T) {
	e := newECEnv(t)
	tr := e.trade(util.NowMs() + 120_000)
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(e.ctx, bson.M{"_id": tr.ID},
		bson.M{"$set": bson.M{"symbol": "BTC/USD"}}); err != nil {
		t.Fatal(err)
	}
	tr.Symbol = "BTC/USD"
	e.market(tr, 100.3, 0, false)
	vol := pricing.EarlyCloseVolatility([]float64{100.1, 100.4, 99.9, 100.3, 100.0, 100.6, 100.2, 100.3})
	z := func(q EarlyCloseQuoteView) float64 {
		return 0.3 / (100.3 * vol * math.Sqrt(float64(tr.ExpiryTimestamp-util.NowMs())/1000))
	}

	e.setRiskConfig(bson.M{"earlyCloseCalibration": bson.A{}, "earlyCloseMinProbabilityPercent": 1.0})
	plain := e.quote(tr)
	if want := StandardNormalCdf(z(plain)); !plain.Available || math.Abs(plain.WinProbability-want) > 3e-4 {
		t.Fatalf("no table: %+v, want P %v", plain, want)
	}

	e.setRiskConfig(bson.M{"earlyCloseCalibration": bson.A{bson.M{"symbol": "", "maxSecondsRemaining": int64(0),
		"maxVolRatio": 0.0, "sigmaScale": 2.0, "minVolRatio": 0.0, "stickiness": 0.5}}})
	corrected := e.quote(tr)
	if want := 0.5 + 0.5*StandardNormalCdf(z(corrected)/2); math.Abs(corrected.WinProbability-want) > 3e-4 ||
		corrected.Value <= plain.Value {
		t.Fatalf("with the table: %+v, want P %v and more than %d", corrected, want, plain.Value)
	}
}
