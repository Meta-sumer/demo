package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// GenericWebsocket is the adapter for a vendor that publishes JSON ticks over
// a websocket. It expects one of these shapes per message (single object or
// array):
//
//	{ "symbol": "BTCUSD", "price": 104250.5, "timestamp": 1730000000000 }
//	{ "s": "BTCUSD", "p": "104250.5", "t": 1730000000000, "b": ..., "a": ... }
//
// A vendor whose payload differs gets its own provider rather than options
// bolted onto this one - the interface is the seam, not this parser.
type GenericWebsocket struct {
	Base
	ws *wsClient
}

// NewGenericWebsocket fails when url is empty.
func NewGenericWebsocket(url, apiKey string) (*GenericWebsocket, error) {
	if url == "" {
		return nil, errors.New("MARKET_DATA_WS_URL is required for the generic-ws provider")
	}
	g := &GenericWebsocket{Base: Base{name: "generic-ws"}}
	var header http.Header
	if apiKey != "" {
		header = http.Header{"Authorization": []string{"Bearer " + apiKey}}
	}
	g.ws = &wsClient{
		base:      &g.Base,
		url:       func() string { return url },
		header:    header,
		onOpen:    g.opened,
		onMessage: g.handleMessage,
	}
	return g, nil
}

func (g *GenericWebsocket) Authoritative() bool { return true }

type subscribeMsg struct {
	Action  string   `json:"action"`
	Symbols []string `json:"symbols"`
}

func (g *GenericWebsocket) opened() {
	if symbols := g.symbolList(); len(symbols) > 0 {
		g.ws.send(subscribeMsg{"subscribe", symbols})
	}
}

func (g *GenericWebsocket) handleMessage(payload []byte) {
	var parsed json.RawMessage
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(parsed, &items); err != nil {
		items = []json.RawMessage{parsed}
	}
	for _, item := range items {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(item, &m); err != nil {
			continue
		}
		g.emitParsed(m)
	}
}

// pick is `a ?? b`: the first key present with a non-null value.
func pick(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if v, ok := m[k]; ok && string(v) != "null" {
			return v
		}
	}
	return nil
}

func (g *GenericWebsocket) emitParsed(m map[string]json.RawMessage) {
	var symbol string
	if raw := pick(m, "symbol", "s"); raw != nil {
		_ = json.Unmarshal(raw, &symbol)
	}
	rawPrice := pick(m, "price", "p")
	if symbol == "" || rawPrice == nil {
		return
	}
	price, ok := jsNumber(rawPrice)
	if !ok || price <= 0 {
		return
	}

	tick := Tick{Symbol: strings.ToLower(symbol), Price: price}
	if raw := pick(m, "bid", "b"); raw != nil {
		if v, ok := jsNumber(raw); ok {
			tick.Bid = &v
		}
	}
	if raw := pick(m, "ask", "a"); raw != nil {
		if v, ok := jsNumber(raw); ok {
			tick.Ask = &v
		}
	}
	ts := time.Now().UnixMilli()
	if raw := pick(m, "timestamp", "t"); raw != nil {
		if v, ok := jsNumber(raw); ok {
			ts = int64(v)
		}
	}
	tick.ProviderTimestamp = &ts
	if raw := pick(m, "sequence", "seq"); raw != nil {
		if v, ok := jsNumber(raw); ok {
			tick.Sequence = ptr(int64(v))
		}
	}
	g.emitTick(tick)
}

func (g *GenericWebsocket) Connect(context.Context) error {
	g.ws.connect()
	return nil
}

func (g *GenericWebsocket) Disconnect(context.Context) error {
	g.ws.disconnect()
	return nil
}

func (g *GenericWebsocket) Subscribe(_ context.Context, symbols []string) error {
	g.smu.Lock()
	for _, s := range symbols {
		g.addSymbol(strings.ToLower(s))
	}
	g.smu.Unlock()
	if g.ws.isOpen() {
		g.ws.send(subscribeMsg{"subscribe", nonNil(symbols)})
	}
	return nil
}

func (g *GenericWebsocket) Unsubscribe(_ context.Context, symbols []string) error {
	g.smu.Lock()
	for _, s := range symbols {
		g.removeSymbol(strings.ToLower(s))
	}
	g.smu.Unlock()
	if g.ws.isOpen() {
		g.ws.send(subscribeMsg{"unsubscribe", nonNil(symbols)})
	}
	return nil
}

func (g *GenericWebsocket) IsConnected() bool { return g.ws.isOpen() }

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
