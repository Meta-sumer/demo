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

// Binance public trade stream - a real, authoritative market feed for crypto.
//
// No credentials are required for public market data. Each message carries the
// exchange's own event time, which is what the latency measurement in
// marketdata compares against the server clock.
const binanceBaseURL = "wss://stream.binance.com:9443/stream"

type Binance struct {
	Base
	ws *wsClient

	reqMu         sync.Mutex
	nextRequestID int64
	// Symbols carried in the URL of the socket being opened.
	urlMu      sync.Mutex
	urlSymbols []string
}

func NewBinance() *Binance {
	b := &Binance{Base: Base{name: "binance"}, nextRequestID: 1}
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
		streams[i] = s + "@trade"
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
			missing = append(missing, s+"@trade")
		}
	}
	if len(missing) > 0 {
		b.send("SUBSCRIBE", missing)
	}
}

type binanceTradeMessage struct {
	Stream string `json:"stream"`
	Data   *struct {
		E  string          `json:"e"`
		ET *float64        `json:"E"`
		T  *float64        `json:"T"`
		S  string          `json:"s"`
		P  json.RawMessage `json:"p"`
		ID *float64        `json:"t"`
	} `json:"data"`
}

func (b *Binance) handleMessage(payload []byte) {
	var m binanceTradeMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return
	}
	d := m.Data
	if d == nil || d.E != "trade" || d.S == "" || len(d.P) == 0 {
		return
	}
	price, ok := jsNumber(d.P)
	if !ok {
		// Number() of garbage is NaN; ingestion logs and discards it.
		price = math.NaN()
	}
	var ts int64
	switch {
	case d.T != nil:
		ts = int64(*d.T)
	case d.ET != nil:
		ts = int64(*d.ET)
	default:
		ts = time.Now().UnixMilli()
	}
	tick := Tick{Symbol: strings.ToLower(d.S), Price: price, ProviderTimestamp: &ts}
	if d.ID != nil {
		tick.Sequence = ptr(int64(*d.ID))
	}
	b.emitTick(tick)
}

func (b *Binance) Connect(context.Context) error {
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
	if len(removed) > 0 && b.ws.isOpen() {
		b.send("UNSUBSCRIBE", withSuffix(removed))
	}
	return nil
}

func withSuffix(symbols []string) []string {
	out := make([]string, len(symbols))
	for i, s := range symbols {
		out[i] = s + "@trade"
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
