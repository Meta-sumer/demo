// Package binancefake is an in-memory Binance USDⓈ-M futures REST server
// (httptest) for tests: no test ever reaches the real exchange.
//
// It checks what Binance checks - the API key header, the HMAC-SHA256
// signature over the exact query string, the timestamp against recvWindow,
// quantity step / min / max, minimum notional, duplicate client order ids
// among open orders - and fills MARKET orders at the top of its book. Its
// knobs reproduce the failure modes the hedge lifecycle must survive:
// partial fills, resting orders, duplicate fill reports, a response lost
// after the order was accepted, a request lost before it was, a cancel that
// races a fill, outages, and external position changes (drift).
package binancefake

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const scale = 100_000_000

type symState struct {
	bid, ask, mark, fundingRate float64
	stepE8, minQtyE8, maxQtyE8  int64
	minNotional, tick           float64
	posE8                       int64
	entry                       float64
}

type order struct {
	ID            int64
	Symbol        string
	ClientOrderID string
	Side          string
	Status        string
	OrigE8        int64
	ExecE8        int64
	CumQuoteE8    int64
	UpdateTime    int64
}

type trade struct {
	ID, OrderID  int64
	Symbol, Side string
	Price        float64
	QtyE8        int64
	QuoteE8      int64
	FeeE8        int64
	RealizedE8   int64
	Time         int64
}

type income struct {
	TranID   int64
	Symbol   string
	AmountE8 int64
	Time     int64
}

// Fake is the server. Set the exported knobs before (or between) requests;
// they are read under the lock.
type Fake struct {
	Server *httptest.Server
	APIKey string
	Secret string

	mu      sync.Mutex
	syms    map[string]*symState
	orders  []*order
	trades  []trade
	incomes []income
	nextID  int64
	nextTID int64
	nextTr  int64
	// Requests counts calls per "METHOD path".
	Requests map[string]int

	// TakerRate is the commission charged on every fill (default 0.0005).
	TakerRate float64
	// FillRatio fills that share of each new order and expires the rest.
	FillRatio float64
	// Splits executes each order in that many fills, walking the book one
	// tick per fill.
	Splits int
	// RestNew leaves new orders NEW with nothing filled (see FillResting).
	RestNew bool
	// RejectCode refuses the next order with this Binance error code.
	RejectCode int
	// DelayAfterAccept accepts and fills the order, then holds the response
	// this long (a client with a shorter timeout loses the answer).
	DelayAfterAccept time.Duration
	// DropBeforeAccept holds the request this long and never accepts it.
	DropBeforeAccept time.Duration
	// Down answers every request 503.
	Down bool
	// DuplicateTrades lists every fill twice in userTrades.
	DuplicateTrades bool
	// FillOnCancel fills an open order completely just before a cancel
	// reaches it, so the cancel answers -2011 (the race a cancel can lose).
	FillOnCancel bool
	// Placed counts accepted orders.
	Placed int
	// Clock is the server's notion of now (default time.Now).
	Clock func() time.Time
}

// New starts a fake with BTCUSDT and ETHUSDT listed.
func New() *Fake {
	f := &Fake{
		APIKey: "test-api-key", Secret: "test-api-secret-0123456789",
		syms: map[string]*symState{
			"BTCUSDT": {bid: 60_000, ask: 60_000.1, mark: 60_000.05, fundingRate: 0.0001,
				stepE8: 100_000, minQtyE8: 100_000, maxQtyE8: 120 * scale, minNotional: 100, tick: 0.1},
			"ETHUSDT": {bid: 3_000, ask: 3_000.01, mark: 3_000.005, fundingRate: 0.0001,
				stepE8: 100_000, minQtyE8: 100_000, maxQtyE8: 2_000 * scale, minNotional: 20, tick: 0.01},
		},
		nextID: 1000, nextTID: 5000, nextTr: 9000, Requests: map[string]int{},
		TakerRate: 0.0005, FillRatio: 1, Splits: 1, Clock: time.Now,
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	return f
}

// URL is the base URL to point the adapter at.
func (f *Fake) URL() string { return f.Server.URL }

// Close stops the server.
func (f *Fake) Close() { f.Server.Close() }

// Set changes a knob under the lock.
func (f *Fake) Set(fn func(f *Fake)) {
	f.mu.Lock()
	fn(f)
	f.mu.Unlock()
}

// SetBook sets the top of book and the mark (the mid).
func (f *Fake) SetBook(symbol string, bid, ask float64) {
	f.mu.Lock()
	s := f.syms[symbol]
	s.bid, s.ask, s.mark = bid, ask, (bid+ask)/2
	f.mu.Unlock()
}

// SetFundingRate sets lastFundingRate.
func (f *Fake) SetFundingRate(symbol string, rate float64) {
	f.mu.Lock()
	f.syms[symbol].fundingRate = rate
	f.mu.Unlock()
}

// AddFunding books a funding payment (amount "+ received / - paid").
func (f *Fake) AddFunding(symbol, amount string, atMs int64) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTr++
	f.incomes = append(f.incomes, income{TranID: f.nextTr, Symbol: symbol, AmountE8: parseE8(amount), Time: atMs})
	return f.nextTr
}

// ShiftPosition changes the venue position outside our orders (a manual
// trade or a liquidation): the drift reconciliation must catch.
func (f *Fake) ShiftPosition(symbol string, qtyE8 int64) {
	f.mu.Lock()
	f.syms[symbol].posE8 += qtyE8
	f.mu.Unlock()
}

// PositionE8 is the venue position.
func (f *Fake) PositionE8(symbol string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.syms[symbol].posE8
}

// OrderCount is the number of orders the venue holds for a client id.
func (f *Fake) OrderCount(clientOrderID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.orders {
		if o.ClientOrderID == clientOrderID {
			n++
		}
	}
	return n
}

// FillResting fills a NEW order completely.
func (f *Fake) FillResting(clientOrderID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o := f.find(clientOrderID); o != nil && !final(o.Status) {
		f.execute(o, o.OrigE8-o.ExecE8)
		o.Status = "FILLED"
	}
}

func final(s string) bool {
	return s == "FILLED" || s == "CANCELED" || s == "EXPIRED" || s == "REJECTED"
}

func (f *Fake) find(clientOrderID string) *order {
	for i := len(f.orders) - 1; i >= 0; i-- {
		if f.orders[i].ClientOrderID == clientOrderID {
			return f.orders[i]
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, status, code int, msg string) {
	writeJSON(w, status, map[string]any{"code": code, "msg": msg})
}

func (f *Fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.Requests[r.Method+" "+r.URL.Path]++
	down := f.Down
	f.mu.Unlock()
	if down {
		fail(w, http.StatusServiceUnavailable, -1001, "Internal error; unable to process your request. Please try again.")
		return
	}
	q := r.URL.Query()
	signed := strings.Contains(r.URL.RawQuery, "signature=")
	if signed {
		if code, msg := f.verify(r); code != 0 {
			fail(w, http.StatusBadRequest, code, msg)
			return
		}
	}
	switch r.Method + " " + r.URL.Path {
	case "GET /fapi/v1/ping":
		writeJSON(w, 200, map[string]any{})
	case "GET /fapi/v1/exchangeInfo":
		f.exchangeInfo(w)
	case "GET /fapi/v1/ticker/bookTicker":
		f.withSym(w, q, func(s *symState, sym string) {
			writeJSON(w, 200, map[string]any{"symbol": sym, "bidPrice": ff(s.bid), "bidQty": "5.000", "askPrice": ff(s.ask),
				"askQty": "5.000", "time": f.Clock().UnixMilli()})
		})
	case "GET /fapi/v1/premiumIndex":
		f.withSym(w, q, func(s *symState, sym string) {
			writeJSON(w, 200, map[string]any{"symbol": sym, "markPrice": ff(s.mark), "indexPrice": ff(s.mark),
				"lastFundingRate": ff(s.fundingRate), "nextFundingTime": f.Clock().Add(time.Hour).UnixMilli(), "time": f.Clock().UnixMilli()})
		})
	default:
		if !signed {
			fail(w, http.StatusBadRequest, -1102, "Mandatory parameter 'signature' was not sent.")
			return
		}
		f.serveSigned(w, r, q)
	}
}

func (f *Fake) serveSigned(w http.ResponseWriter, r *http.Request, q url.Values) {
	switch r.Method + " " + r.URL.Path {
	case "GET /fapi/v1/commissionRate":
		f.mu.Lock()
		rate := f.TakerRate
		f.mu.Unlock()
		writeJSON(w, 200, map[string]any{"symbol": q.Get("symbol"), "makerCommissionRate": "0.0002", "takerCommissionRate": ff(rate)})
	case "POST /fapi/v1/order":
		f.placeOrder(w, r, q)
	case "GET /fapi/v1/order":
		f.mu.Lock()
		o := f.find(q.Get("origClientOrderId"))
		var view map[string]any
		if o != nil {
			view = f.view(o)
		}
		f.mu.Unlock()
		if o == nil {
			fail(w, http.StatusBadRequest, -2013, "Order does not exist.")
			return
		}
		writeJSON(w, 200, view)
	case "DELETE /fapi/v1/order":
		f.cancel(w, q)
	case "GET /fapi/v1/userTrades":
		f.userTrades(w, q)
	case "GET /fapi/v2/positionRisk":
		f.withSym(w, q, func(s *symState, sym string) {
			unreal := float64(s.posE8) / scale * (s.mark - s.entry)
			writeJSON(w, 200, []map[string]any{{"symbol": sym, "positionAmt": fe8(s.posE8), "entryPrice": ff(s.entry),
				"markPrice": ff(s.mark), "unRealizedProfit": ff(unreal), "positionSide": "BOTH"}})
		})
	case "GET /fapi/v1/income":
		f.incomeList(w, q)
	default:
		fail(w, http.StatusNotFound, -1000, "unknown endpoint")
	}
}

func (f *Fake) verify(r *http.Request) (int, string) {
	if r.Header.Get("X-MBX-APIKEY") != f.APIKey {
		return -2015, "Invalid API-key, IP, or permissions for action."
	}
	raw := r.URL.RawQuery
	i := strings.LastIndex(raw, "&signature=")
	if i < 0 {
		return -1022, "Signature for this request is not valid."
	}
	payload, sig := raw[:i], raw[i+len("&signature="):]
	m := hmac.New(sha256.New, []byte(f.Secret))
	m.Write([]byte(payload))
	if !hmac.Equal([]byte(hex.EncodeToString(m.Sum(nil))), []byte(sig)) {
		return -1022, "Signature for this request is not valid."
	}
	q := r.URL.Query()
	ts, err := strconv.ParseInt(q.Get("timestamp"), 10, 64)
	if err != nil {
		return -1102, "Mandatory parameter 'timestamp' was not sent."
	}
	window := int64(5000)
	if rw, err := strconv.ParseInt(q.Get("recvWindow"), 10, 64); err == nil {
		window = rw
	}
	now := f.Clock().UnixMilli()
	if ts > now+1000 || now-ts > window {
		return -1021, "Timestamp for this request is outside of the recvWindow."
	}
	return 0, ""
}

func (f *Fake) withSym(w http.ResponseWriter, q url.Values, fn func(s *symState, sym string)) {
	f.mu.Lock()
	sym := q.Get("symbol")
	s, ok := f.syms[sym]
	if !ok {
		f.mu.Unlock()
		fail(w, http.StatusBadRequest, -1121, "Invalid symbol.")
		return
	}
	cp := *s
	f.mu.Unlock()
	fn(&cp, sym)
}

func (f *Fake) exchangeInfo(w http.ResponseWriter) {
	f.mu.Lock()
	syms := []map[string]any{}
	names := make([]string, 0, len(f.syms))
	for n := range f.syms {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		s := f.syms[n]
		syms = append(syms, map[string]any{"symbol": n, "status": "TRADING", "filters": []map[string]any{
			{"filterType": "PRICE_FILTER", "tickSize": ff(s.tick)},
			{"filterType": "LOT_SIZE", "stepSize": fe8(s.stepE8), "minQty": fe8(s.minQtyE8), "maxQty": fe8(s.maxQtyE8 * 10)},
			{"filterType": "MARKET_LOT_SIZE", "stepSize": fe8(s.stepE8), "minQty": fe8(s.minQtyE8), "maxQty": fe8(s.maxQtyE8)},
			{"filterType": "MIN_NOTIONAL", "notional": ff(s.minNotional)},
		}})
	}
	f.mu.Unlock()
	writeJSON(w, 200, map[string]any{"symbols": syms})
}

func (f *Fake) placeOrder(w http.ResponseWriter, r *http.Request, q url.Values) {
	f.mu.Lock()
	drop, delay := f.DropBeforeAccept, f.DelayAfterAccept
	f.mu.Unlock()
	if drop > 0 {
		// Lost before the matching engine saw it.
		select {
		case <-time.After(drop):
		case <-r.Context().Done():
		}
		fail(w, http.StatusServiceUnavailable, -1007, "Timeout waiting for response from backend server. Send status unknown; execution status unknown.")
		return
	}

	f.mu.Lock()
	sym := q.Get("symbol")
	s, ok := f.syms[sym]
	if !ok {
		f.mu.Unlock()
		fail(w, 400, -1121, "Invalid symbol.")
		return
	}
	if code := f.RejectCode; code != 0 {
		f.RejectCode = 0
		f.mu.Unlock()
		fail(w, 400, code, "Order rejected by the fake venue.")
		return
	}
	cid := q.Get("newClientOrderId")
	if prev := f.find(cid); prev != nil && !final(prev.Status) {
		f.mu.Unlock()
		fail(w, 400, -4116, "ClientOrderId is duplicated.")
		return
	}
	qty := parseE8(q.Get("quantity"))
	side := q.Get("side")
	price := s.ask
	if side == "SELL" {
		price = s.bid
	}
	switch {
	case qty <= 0 || qty%s.stepE8 != 0:
		f.mu.Unlock()
		fail(w, 400, -1111, "Precision is over the maximum defined for this asset.")
		return
	case qty < s.minQtyE8 || qty > s.maxQtyE8:
		f.mu.Unlock()
		fail(w, 400, -4005, "Quantity greater than max quantity or less than min quantity.")
		return
	case float64(qty)/scale*price < s.minNotional && q.Get("reduceOnly") != "true":
		f.mu.Unlock()
		fail(w, 400, -4164, "Order's notional must be no smaller than "+ff(s.minNotional))
		return
	}
	f.nextID++
	o := &order{ID: f.nextID, Symbol: sym, ClientOrderID: cid, Side: side, Status: "NEW", OrigE8: qty, UpdateTime: f.Clock().UnixMilli()}
	f.orders = append(f.orders, o)
	f.Placed++
	if !f.RestNew {
		fill := int64(math.Floor(float64(qty)*f.FillRatio/float64(s.stepE8))) * s.stepE8
		f.execute(o, fill)
		o.Status = "FILLED"
		if fill < qty {
			o.Status = "EXPIRED"
		}
	}
	view := f.view(o)
	f.mu.Unlock()

	if delay > 0 {
		// Accepted and executed; the answer is lost on the way back.
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
	}
	writeJSON(w, 200, view)
}

// execute fills qty of o at the top of the book, in f.Splits fills; caller
// holds the lock.
func (f *Fake) execute(o *order, qty int64) {
	if qty <= 0 {
		return
	}
	s := f.syms[o.Symbol]
	n := int64(max(1, f.Splits))
	steps := qty / s.stepE8
	if n > steps {
		n = steps
	}
	left := qty
	for i := int64(0); i < n; i++ {
		part := (steps / n) * s.stepE8
		if i == n-1 {
			part = left
		}
		left -= part
		price := s.ask + float64(i)*s.tick
		if o.Side == "SELL" {
			price = s.bid - float64(i)*s.tick
		}
		quote := int64(math.Round(price * float64(part)))
		fee := int64(math.Round(float64(quote) * f.TakerRate))
		signed := part
		if o.Side == "SELL" {
			signed = -part
		}
		realized := f.applyPosition(s, signed, price)
		f.nextTID++
		f.trades = append(f.trades, trade{ID: f.nextTID, OrderID: o.ID, Symbol: o.Symbol, Side: o.Side, Price: price,
			QtyE8: part, QuoteE8: quote, FeeE8: fee, RealizedE8: realized, Time: f.Clock().UnixMilli()})
		o.ExecE8 += part
		o.CumQuoteE8 += quote
	}
	o.UpdateTime = f.Clock().UnixMilli()
}

// applyPosition is one-way average cost, as Binance books realizedPnl.
func (f *Fake) applyPosition(s *symState, signed int64, price float64) int64 {
	pos := s.posE8
	var realized int64
	if pos == 0 || (pos > 0) == (signed > 0) {
		total := abs(pos) + abs(signed)
		s.entry = (s.entry*float64(abs(pos)) + price*float64(abs(signed))) / float64(total)
		s.posE8 = pos + signed
		return 0
	}
	closing := min(abs(pos), abs(signed))
	dir := 1.0
	if pos < 0 {
		dir = -1
	}
	realized = int64(math.Round((price - s.entry) * float64(closing) * dir))
	s.posE8 = pos + signed
	if s.posE8 == 0 {
		s.entry = 0
	} else if (s.posE8 > 0) != (pos > 0) {
		s.entry = price
	}
	return realized
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (f *Fake) view(o *order) map[string]any {
	avg := 0.0
	if o.ExecE8 > 0 {
		avg = float64(o.CumQuoteE8) / float64(o.ExecE8)
	}
	return map[string]any{"orderId": o.ID, "symbol": o.Symbol, "status": o.Status, "clientOrderId": o.ClientOrderID,
		"side": o.Side, "type": "MARKET", "origQty": fe8(o.OrigE8), "executedQty": fe8(o.ExecE8),
		"cumQuote": fe8(o.CumQuoteE8), "avgPrice": ff(avg), "updateTime": o.UpdateTime}
}

func (f *Fake) cancel(w http.ResponseWriter, q url.Values) {
	f.mu.Lock()
	o := f.find(q.Get("origClientOrderId"))
	if o != nil && !final(o.Status) && f.FillOnCancel {
		f.execute(o, o.OrigE8-o.ExecE8)
		o.Status = "FILLED"
	}
	if o == nil || final(o.Status) {
		f.mu.Unlock()
		fail(w, 400, -2011, "Unknown order sent.")
		return
	}
	o.Status = "CANCELED"
	o.UpdateTime = f.Clock().UnixMilli()
	view := f.view(o)
	f.mu.Unlock()
	writeJSON(w, 200, view)
}

func (f *Fake) userTrades(w http.ResponseWriter, q url.Values) {
	f.mu.Lock()
	orderID, _ := strconv.ParseInt(q.Get("orderId"), 10, 64)
	out := []map[string]any{}
	for _, t := range f.trades {
		if t.Symbol != q.Get("symbol") || (orderID != 0 && t.OrderID != orderID) {
			continue
		}
		row := map[string]any{"id": t.ID, "orderId": t.OrderID, "symbol": t.Symbol, "side": t.Side, "price": ff(t.Price),
			"qty": fe8(t.QtyE8), "quoteQty": fe8(t.QuoteE8), "commission": fe8(t.FeeE8), "commissionAsset": "USDT",
			"realizedPnl": fe8(t.RealizedE8), "maker": false, "buyer": t.Side == "BUY", "time": t.Time, "positionSide": "BOTH"}
		out = append(out, row)
		if f.DuplicateTrades {
			out = append(out, row)
		}
	}
	f.mu.Unlock()
	writeJSON(w, 200, out)
}

func (f *Fake) incomeList(w http.ResponseWriter, q url.Values) {
	f.mu.Lock()
	start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
	out := []map[string]any{}
	for _, in := range f.incomes {
		if in.Symbol != q.Get("symbol") || in.Time < start {
			continue
		}
		row := map[string]any{"symbol": in.Symbol, "incomeType": "FUNDING_FEE", "income": fe8(in.AmountE8), "asset": "USDT",
			"info": "", "time": in.Time, "tranId": in.TranID, "tradeId": ""}
		out = append(out, row)
		if f.DuplicateTrades {
			out = append(out, row)
		}
	}
	f.mu.Unlock()
	writeJSON(w, 200, out)
}

func ff(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func fe8(v int64) string {
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	frac := strconv.FormatInt(v%scale+scale, 10)[1:]
	return sign + strconv.FormatInt(v/scale, 10) + "." + frac
}

func parseE8(s string) int64 {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	whole, frac, _ := strings.Cut(s, ".")
	if len(frac) > 8 {
		frac = frac[:8]
	}
	frac += strings.Repeat("0", 8-len(frac))
	w, _ := strconv.ParseInt(whole, 10, 64)
	fr, _ := strconv.ParseInt(frac, 10, 64)
	v := w*scale + fr
	if neg {
		v = -v
	}
	return v
}
