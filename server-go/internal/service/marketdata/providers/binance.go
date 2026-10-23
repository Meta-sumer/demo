package providers

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/platform/logx"
)

// Binance public best bid/ask stream (@bookTicker) - a real, authoritative
// market feed for crypto. No credentials are required for public market data.
//
// The pricing specification prices on the mid of a valid book (bid, ask), so
// each tick carries the best bid and ask and its price is the mid. Binance
// pushes a book update on every change of the best price or quantity - for
// BTCUSDT many per second - so updates are coalesced: every flushInterval the
// latest book of each symbol that changed is emitted, stamped with the time
// it arrived. The spot @bookTicker payload has no event time, so the server's
// receipt time is the quote's timestamp. A book that has not changed is
// re-emitted (at most every heartbeatInterval) only while the socket is
// demonstrably alive - another update arrived within liveWindow - so an
// unchanged market stays fresh but a stalled connection goes stale.
const binanceBaseURL = "wss://stream.binance.com:9443/stream"

// Coalescing of book updates (see the type comment).
const (
	binanceFlushInterval     = 50 * time.Millisecond
	binanceHeartbeatInterval = 250 * time.Millisecond
	binanceLiveWindow        = time.Second
)

type binanceBook struct {
	bid, ask  float64
	seq       int64
	at        int64 // receipt time of the latest update, epoch ms
	emittedAt int64
	changed   bool
}

type Binance struct {
	Base
	ws *wsClient

	bookMu    sync.Mutex
	books     map[string]*binanceBook
	lastMsgAt int64
	flushOnce sync.Once
	// now is the clock (tests replace it).
	now func() int64

	reqMu         sync.Mutex
	nextRequestID int64
	// Symbols carried in the URL of the socket being opened.
	urlMu      sync.Mutex
	urlSymbols []string
}

func NewBinance() *Binance {
	b := &Binance{Base: Base{name: "binance"}, nextRequestID: 1, books: map[string]*binanceBook{},
		now: func() int64 { return time.Now().UnixMilli() }}
	b.ws = &wsClient{
		base:          &b.Base,
		url:           b.streamURL,
		onOpen:        b.opened,
		onMessage:     b.handleMessage,
		announceRetry: true,
		closeDetail:   true,
	}
	return b
}

func (b *Binance) Authoritative() bool { return true }

func (b *Binance) streamURL() string {
	symbols := b.symbolList()
	b.urlMu.Lock()
	b.urlSymbols = symbols
	b.urlMu.Unlock()
	if len(symbols) == 0 {
		return binanceBaseURL
	}
	streams := make([]string, len(symbols))
	for i, s := range symbols {
		streams[i] = s + binanceStream
	}
	return binanceBaseURL + "?streams=" + strings.Join(streams, "/")
}

func (b *Binance) opened() {
	logx.Info("market feed connected", "provider", b.name, "symbols", b.symbolList())
	// Symbols added while the handshake was in flight are not in the URL; the
	// TS provider drops them until the next reconnect, here they are
	// subscribed as soon as the socket opens.
	b.urlMu.Lock()
	inURL := map[string]bool{}
	for _, s := range b.urlSymbols {
		inURL[s] = true
	}
	b.urlMu.Unlock()
	var missing []string
	for _, s := range b.symbolList() {
		if !inURL[s] {
			missing = append(missing, s+binanceStream)
		}
	}
	if len(missing) > 0 {
		b.send("SUBSCRIBE", missing)
	}
}

// binanceStream is the per-symbol stream: best bid/ask.
const binanceStream = "@bookTicker"

// binanceBookMessage is a combined-stream @bookTicker message:
// {"stream":"btcusdt@bookTicker","data":{"u":400900217,"s":"BTCUSDT","b":"...","B":"...","a":"...","A":"..."}}
// The payload is read by exact key: encoding/json matches field names
// case-insensitively, and "B"/"A" (quantities) would overwrite "b"/"a"
// (prices).
type binanceBookMessage struct {
	Stream string                     `json:"stream"`
	Data   map[string]json.RawMessage `json:"data"`
}

func (b *Binance) handleMessage(payload []byte) {
	var m binanceBookMessage
	if err := json.Unmarshal(payload, &m); err != nil || m.Data == nil {
		return
	}
	var symbol string
	if raw, ok := m.Data["s"]; !ok || json.Unmarshal(raw, &symbol) != nil || symbol == "" {
		return
	}
	rawBid, okB := m.Data["b"]
	rawAsk, okA := m.Data["a"]
	if !okB || !okA {
		return
	}
	bid, okB := jsNumber(rawBid)
	ask, okA := jsNumber(rawAsk)
	if !okB || !okA {
		// Not a number: pass it on as NaN, so the normalisation rejects it
		// (invalid book) and counts it.
		bid, ask = math.NaN(), math.NaN()
	}
	var seq *float64
	if raw, ok := m.Data["u"]; ok {
		var u float64
		if json.Unmarshal(raw, &u) == nil {
			seq = &u
		}
	}
	d := struct {
		U *float64
		S string
	}{seq, symbol}
	now := b.now()
	sym := strings.ToLower(d.S)
	b.bookMu.Lock()
	b.lastMsgAt = now
	bk := b.books[sym]
	if bk == nil {
		bk = &binanceBook{}
		b.books[sym] = bk
	}
	if d.U != nil {
		bk.seq = int64(*d.U)
	}
	bk.bid, bk.ask, bk.at, bk.changed = bid, ask, now, true
	b.bookMu.Unlock()
}

// flush emits the coalesced books (see the type comment).
func (b *Binance) flush() {
	now := b.now()
	var out []Tick
	b.bookMu.Lock()
	live := now-b.lastMsgAt <= binanceLiveWindow.Milliseconds()
	for sym, bk := range b.books {
		ts := bk.at
		switch {
		case bk.changed:
		case live && now-bk.emittedAt >= binanceHeartbeatInterval.Milliseconds():
			ts = now
		default:
			continue
		}
		// Strictly increasing per symbol (the ordering check rejects equal
		// timestamps).
		if ts <= bk.emittedAt {
			ts = bk.emittedAt + 1
		}
		bk.changed, bk.emittedAt = false, ts
		bid, ask := bk.bid, bk.ask
		t := Tick{Symbol: sym, Price: (bid + ask) / 2, Bid: ptr(bid), Ask: ptr(ask), ProviderTimestamp: ptr(ts)}
		if bk.seq > 0 {
			t.Sequence = ptr(bk.seq)
		}
		out = append(out, t)
	}
	b.bookMu.Unlock()
	for _, t := range out {
		b.emitTick(t)
	}
}

func (b *Binance) startFlusher() {
	b.flushOnce.Do(func() {
		go func() {
			t := time.NewTicker(binanceFlushInterval)
			defer t.Stop()
			for range t.C {
				b.flush()
			}
		}()
	})
}

func (b *Binance) Connect(context.Context) error {
	b.startFlusher()
	b.ws.connect()
	return nil
}

func (b *Binance) Disconnect(context.Context) error {
	b.ws.disconnect()
	return nil
}

func (b *Binance) Subscribe(_ context.Context, symbols []string) error {
	b.smu.Lock()
	var added []string
	for _, s := range symbols {
		s = strings.ToLower(s)
		if b.addSymbol(s) {
			added = append(added, s)
		}
	}
	b.smu.Unlock()
	if len(added) == 0 {
		return nil
	}
	if b.ws.isOpen() {
		b.send("SUBSCRIBE", withSuffix(added))
	} else if !b.ws.hasSocket() {
		b.ws.open()
	}
	return nil
}

func (b *Binance) Unsubscribe(_ context.Context, symbols []string) error {
	b.smu.Lock()
	var removed []string
	for _, s := range symbols {
		s = strings.ToLower(s)
		if b.removeSymbol(s) {
			removed = append(removed, s)
		}
	}
	b.smu.Unlock()
	b.bookMu.Lock()
	for _, s := range removed {
		delete(b.books, s)
	}
	b.bookMu.Unlock()
	if len(removed) > 0 && b.ws.isOpen() {
		b.send("UNSUBSCRIBE", withSuffix(removed))
	}
	return nil
}

func withSuffix(symbols []string) []string {
	out := make([]string, len(symbols))
	for i, s := range symbols {
		out[i] = s + binanceStream
	}
	return out
}

func (b *Binance) send(method string, params []string) {
	b.reqMu.Lock()
	id := b.nextRequestID
	b.nextRequestID++
	b.reqMu.Unlock()
	b.ws.send(struct {
		Method string   `json:"method"`
		Params []string `json:"params"`
		ID     int64    `json:"id"`
	}{method, params, id})
}

func (b *Binance) IsConnected() bool { return b.ws.isOpen() }

// jsNumber is JavaScript Number(x) for a JSON string or number. ok is false
// for anything that is not a finite number.
func jsNumber(raw json.RawMessage) (float64, bool) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, false
	}
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			f = 0
		} else {
			p, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return 0, false
			}
			f = p
		}
	case bool:
		if x {
			f = 1
		}
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
