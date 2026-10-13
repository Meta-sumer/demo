package binary

import (
	"math"
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
)

var ecBase = EarlyCloseInput{
	Stake:               util.ToMinor(1_000),
	PayoutPercent:       82,
	Direction:           models.DirectionUp,
	EntryPrice:          100,
	CurrentPrice:        100,
	SecondsRemaining:    30,
	VolatilityPerSecond: 0.001,
	FeePercent:          5,
}

func with(f func(*EarlyCloseInput)) EarlyCloseInput {
	in := ecBase
	f(&in)
	return in
}

func closeTo(t *testing.T, got, want float64, digits int) {
	t.Helper()
	if math.Abs(got-want) >= math.Pow10(-digits)/2 {
		t.Fatalf("got %v, want %v (to %d digits)", got, want, digits)
	}
}

func TestStandardNormalCdf(t *testing.T) {
	closeTo(t, StandardNormalCdf(0), 0.5, 5)
	closeTo(t, StandardNormalCdf(1)+StandardNormalCdf(-1), 1, 4)
	closeTo(t, StandardNormalCdf(1), 0.8413, 3)
	closeTo(t, StandardNormalCdf(1.96), 0.975, 3)
	closeTo(t, StandardNormalCdf(-2), 0.0228, 3)
}

func TestCoinFlipAtEntry(t *testing.T) {
	q := QuoteEarlyClose(ecBase)
	closeTo(t, q.WinProbability, 0.5, 2)
	want := int64(util.RoundHalfUp(float64(util.ToMinor(1_820)) * 0.5 * 0.95))
	if q.Value != want {
		t.Fatalf("value %d, want %d", q.Value, want)
	}
}

func TestPaysMoreInTheMoney(t *testing.T) {
	base := QuoteEarlyClose(ecBase).Value
	slightly := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 100.2 }))
	deeply := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 101 }))
	if !(slightly.Value > base && deeply.Value > slightly.Value && deeply.InTheMoney) {
		t.Fatalf("base %d slightly %+v deeply %+v", base, slightly, deeply)
	}
}

func TestPaysLessOutOfTheMoney(t *testing.T) {
	out := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 99 }))
	if !(out.Value < QuoteEarlyClose(ecBase).Value && !out.InTheMoney && out.CustomerResult < 0) {
		t.Fatalf("%+v", out)
	}
}

func TestDownMirrorsUp(t *testing.T) {
	up := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 101 }))
	down := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.Direction = models.DirectionDown; i.CurrentPrice = 99 }))
	if !(float64(down.Value) > float64(up.Value)*0.99 && float64(down.Value) < float64(up.Value)*1.01) {
		t.Fatalf("up %d down %d", up.Value, down.Value)
	}
	wrongWay := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.Direction = models.DirectionDown; i.CurrentPrice = 101 }))
	if wrongWay.Value >= down.Value {
		t.Fatal("wrong-way DOWN should be worth less")
	}
}

func TestNeverPaysFullPayout(t *testing.T) {
	full := ecBase.Stake + int64(util.RoundHalfUp(float64(ecBase.Stake)*ecBase.PayoutPercent/100))
	for _, p := range []float64{101, 105, 150, 1000} {
		if q := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = p })); q.Value >= full {
			t.Fatalf("price %v value %d >= %d", p, q.Value, full)
		}
	}
}

func TestNeverNegative(t *testing.T) {
	for _, p := range []float64{99, 50, 1, 0.01} {
		if q := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = p })); q.Value < 0 {
			t.Fatalf("price %v value %d", p, q.Value)
		}
	}
}

func TestTimeDecay(t *testing.T) {
	early := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 99.8; i.SecondsRemaining = 60 }))
	late := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 99.8; i.SecondsRemaining = 5 }))
	if late.Value >= early.Value {
		t.Fatal("losing position should lose value as expiry approaches")
	}
	early = QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 100.2; i.SecondsRemaining = 60 }))
	late = QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.CurrentPrice = 100.2; i.SecondsRemaining = 5 }))
	if late.Value <= early.Value {
		t.Fatal("winning position should gain value as expiry approaches")
	}
}

func TestAppliesFee(t *testing.T) {
	free := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.FeePercent = 0 }))
	charged := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.FeePercent = 20 }))
	if charged.Value != int64(util.RoundHalfUp(float64(free.Value)*0.8)) {
		t.Fatalf("free %d charged %d", free.Value, charged.Value)
	}
}

func TestStillMarket(t *testing.T) {
	cases := map[float64]float64{101: 0.99, 99: 0.01, 100: 0.5}
	for price, want := range cases {
		q := QuoteEarlyClose(with(func(i *EarlyCloseInput) { i.VolatilityPerSecond = 0; i.CurrentPrice = price }))
		if q.WinProbability != want {
			t.Fatalf("price %v: p=%v want %v", price, q.WinProbability, want)
		}
	}
}

func TestToFixedMatchesJS(t *testing.T) {
	cases := map[float64]string{0.03125: "0.0313", 1.00005: "1.0001", -0.00001: "0.0000", 0.21978021978: "0.2198"}
	for in, want := range cases {
		if got := toFixed(in, 4); got != want {
			t.Fatalf("toFixed(%v) = %s, want %s", in, got, want)
		}
	}
}
