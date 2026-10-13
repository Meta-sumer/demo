package providers

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

func collect(b *Base) *[]Tick {
	var got []Tick
	b.OnTick(func(t Tick) { got = append(got, t) })
	return &got
}

func TestBinanceParsesTradeMessages(t *testing.T) {
	b := NewBinance()
	got := collect(&b.Base)
	b.handleMessage([]byte(`{"stream":"btcusdt@trade","data":{"e":"trade","E":1730000000001,"T":1730000000000,"s":"BTCUSDT","t":12345,"p":"104250.50","q":"0.01"}}`))
	b.handleMessage([]byte(`{"data":{"e":"aggTrade","s":"BTCUSDT","p":"1"}}`))
	b.handleMessage([]byte(`not json`))
	if len(*got) != 1 {
		t.Fatalf("want 1 tick, got %d", len(*got))
	}
	tk := (*got)[0]
	if tk.Symbol != "btcusdt" || tk.Price != 104250.5 || *tk.ProviderTimestamp != 1730000000000 || *tk.Sequence != 12345 {
		t.Fatalf("bad tick %+v", tk)
	}
}

func TestGenericParsesBothShapes(t *testing.T) {
	g, err := NewGenericWebsocket("ws://example.invalid", "")
	if err != nil {
		t.Fatal(err)
	}
	got := collect(&g.Base)
	g.handleMessage([]byte(`[{"symbol":"BTCUSD","price":104250.5,"timestamp":1730000000000},{"s":"EURUSD","p":"1.08","t":5,"b":"1.07","a":1.09,"seq":7},{"s":"X","p":0},{"p":1}]`))
	if len(*got) != 2 {
		t.Fatalf("want 2 ticks, got %d", len(*got))
	}
	a, b := (*got)[0], (*got)[1]
	if a.Symbol != "btcusd" || a.Price != 104250.5 || *a.ProviderTimestamp != 1730000000000 || a.Bid != nil {
		t.Fatalf("bad tick %+v", a)
	}
	if b.Symbol != "eurusd" || *b.Bid != 1.07 || *b.Ask != 1.09 || *b.Sequence != 7 || *b.ProviderTimestamp != 5 {
		t.Fatalf("bad tick %+v", b)
	}
	if _, err := NewGenericWebsocket("", ""); err == nil {
		t.Fatal("empty url must fail")
	}
}

func TestSimulatedWalk(t *testing.T) {
	s := NewSimulated(0)
	got := collect(&s.Base)
	_ = s.Subscribe(context.Background(), []string{"BTCUSDT", "eurusd", "unknown"})
	for i := 0; i < 100; i++ {
		s.tick()
	}
	if len(*got) != 300 {
		t.Fatalf("want 300 ticks, got %d", len(*got))
	}
	last := map[string]Tick{}
	for _, tk := range *got {
		last[tk.Symbol] = tk
		if *tk.Bid > tk.Price || *tk.Ask < tk.Price {
			t.Fatalf("bid/ask around price broken: %+v", tk)
		}
	}
	if p := last["btcusdt"].Price; math.Abs(p-104250)/104250 > 0.2 {
		t.Fatalf("btc wandered to %v", p)
	}
	if p := last["unknown"].Price; math.Abs(p-100)/100 > 0.2 {
		t.Fatalf("default seed wandered to %v", p)
	}
	if *last["eurusd"].Sequence != 100 {
		t.Fatalf("sequence %d", *last["eurusd"].Sequence)
	}
	if roundPrice(1.234567) != 1.23457 || roundPrice(104250.126) != 104250.13 {
		t.Fatal("roundPrice mismatch")
	}
}

// A day of ticks must stay near the seed - the earlier walk lost 90% of BTC's
// price in a day - and still move by about the configured amount a minute, so
// a 30-second chart is not flat.
func TestSimulatedWalkStaysNearSeedOverADay(t *testing.T) {
	s := NewSimulated(0)
	s.rng = rand.New(rand.NewPCG(1, 2))
	_ = s.Subscribe(context.Background(), []string{"BTCUSDT", "eurusd", "unknown"})
	ticksPerMinute := int(time.Minute / s.interval)
	for _, symbol := range []string{"btcusdt", "eurusd", "unknown"} {
		st := s.states[symbol]
		seed := st.price
		want, ok := minuteVol[symbol]
		if !ok {
			want = defaultMinuteVol
		}
		var sumSq, maxDev float64
		minutes := 0
		last := st.logPrice
		for i := 1; i <= 24*60*ticksPerMinute; i++ {
			s.step(st)
			maxDev = math.Max(maxDev, math.Abs(st.price-seed)/seed)
			if i%ticksPerMinute == 0 {
				move := st.logPrice - last
				sumSq += move * move
				last = st.logPrice
				minutes++
			}
		}
		perMinute := math.Sqrt(sumSq / float64(minutes))
		if perMinute < want/2 || perMinute > want*2 {
			t.Errorf("%s moved %.5f a minute, want about %.5f", symbol, perMinute, want)
		}
		if maxDev > 0.15 {
			t.Errorf("%s strayed %.1f%% from its seed within a day", symbol, maxDev*100)
		}
	}
}
