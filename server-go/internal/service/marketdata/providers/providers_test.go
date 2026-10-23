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

// TestBinanceBookTicker: best bid/ask updates are coalesced into ticks
// priced at the mid, stamped with their arrival, strictly increasing; an
// unchanged book is re-sent only while the socket is demonstrably alive.
func TestBinanceBookTicker(t *testing.T) {
	b := NewBinance()
	now := int64(1_730_000_000_000)
	b.now = func() int64 { return now }
	got := collect(&b.Base)
	b.handleMessage([]byte(`{"stream":"btcusdt@bookTicker","data":{"u":101,"s":"BTCUSDT","b":"104250.00","B":"1.5","a":"104250.02","A":"2"}}`))
	now += 10
	b.handleMessage([]byte(`{"stream":"btcusdt@bookTicker","data":{"u":102,"s":"BTCUSDT","b":"104250.10","B":"1","a":"104250.12","A":"1"}}`))
	b.handleMessage([]byte(`{"data":{"e":"trade","s":"BTCUSDT","p":"1"}}`))
	b.handleMessage([]byte(`not json`))
	b.flush()
	if len(*got) != 1 {
		t.Fatalf("coalesced: want 1 tick, got %d", len(*got))
	}
	tk := (*got)[0]
	if tk.Symbol != "btcusdt" || *tk.Bid != 104250.10 || *tk.Ask != 104250.12 || tk.Price != (104250.10+104250.12)/2 ||
		*tk.ProviderTimestamp != now || *tk.Sequence != 102 {
		t.Fatalf("bad tick %+v", tk)
	}
	// Unchanged and the socket alive: a heartbeat after 250 ms, not before.
	now += 100
	b.flush()
	if len(*got) != 1 {
		t.Fatal("heartbeat too early")
	}
	now += 200
	b.handleMessage([]byte(`{"stream":"ethusdt@bookTicker","data":{"u":7,"s":"ETHUSDT","b":"3280.00","B":"1","a":"3280.01","A":"1"}}`))
	b.flush()
	if len(*got) != 3 || (*got)[1].Symbol == (*got)[2].Symbol {
		t.Fatalf("heartbeat + eth: %d ticks", len(*got))
	}
	// A stalled socket (no message for over a second): nothing re-sent, so
	// the feed goes stale.
	now += 2000
	b.flush()
	if len(*got) != 3 {
		t.Fatal("a stalled stream must not heartbeat")
	}
	// Garbage numbers reach the normaliser as NaN (it rejects and counts them).
	b.handleMessage([]byte(`{"data":{"u":103,"s":"BTCUSDT","b":"x","a":"1"}}`))
	b.flush()
	var garbage *Tick
	for i := 3; i < len(*got); i++ {
		if (*got)[i].Symbol == "btcusdt" {
			garbage = &(*got)[i]
		}
	}
	if garbage == nil || !math.IsNaN(*garbage.Bid) {
		t.Fatalf("garbage %+v", garbage)
	}
	if b.streamURL() != binanceBaseURL || len(withSuffix([]string{"btcusdt"})) != 1 || withSuffix([]string{"btcusdt"})[0] != "btcusdt@bookTicker" {
		t.Fatal("stream names")
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
