package providers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
)

// Binance USDⓈ-M perpetual futures REST adapter.
//
// Signed endpoints (TRADE / USER_DATA) carry timestamp and recvWindow and an
// HMAC-SHA256 signature of the exact query string, keyed with the API
// secret; the API key travels in X-MBX-APIKEY. Binance refuses a signed
// request whose timestamp is older than recvWindow, which is what makes the
// "query before resend" rule safe: once recvWindow has passed, an order the
// venue does not know can no longer appear (hedge-lifecycle.md section 4).
//
// Order placement is refused unless CanTrade passes: credentials present and,
// on the live exchange, HEDGE_LIVE_EXECUTION_ENABLED=true.

// BinanceConfig configures the adapter.
type BinanceConfig struct {
	// "testnet" | "live"
	Env         string
	BaseURL     string
	APIKey      string
	APISecret   string
	LiveEnabled bool
	RecvWindow  time.Duration
	Timeout     time.Duration
}

// BinanceConfigFromEnv reads the HEDGE_* settings.
func BinanceConfigFromEnv(e *config.Env) BinanceConfig {
	return BinanceConfig{
		Env: e.HedgeBinanceEnv, BaseURL: e.HedgeBaseURL(), APIKey: e.HedgeAPIKey, APISecret: e.HedgeAPISecret,
		LiveEnabled: e.HedgeLiveExecutionEnabled,
		RecvWindow:  time.Duration(e.HedgeRecvWindowMs) * time.Millisecond,
		Timeout:     time.Duration(e.HedgeRequestTimeoutMs) * time.Millisecond,
	}
}

type Binance struct {
	cfg    BinanceConfig
	client *http.Client
	// Now is the clock requests are stamped with (a test hook).
	Now func() time.Time

	rulesMu   sync.Mutex
	rules     map[string]models.HedgeRules
	rulesTime time.Time
}

func NewBinance(cfg BinanceConfig) *Binance {
	if cfg.Env != "live" {
		cfg.Env = "testnet"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = config.BinanceFuturesTestnetURL
		if cfg.Env == "live" {
			cfg.BaseURL = config.BinanceFuturesLiveURL
		}
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if cfg.RecvWindow <= 0 {
		cfg.RecvWindow = 5 * time.Second
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	return &Binance{cfg: cfg, client: &http.Client{Timeout: cfg.Timeout}, Now: time.Now}
}

func (b *Binance) Name() string { return "binance" }
func (b *Binance) ID() string   { return "binance-" + b.cfg.Env }
func (b *Binance) Live() bool   { return b.cfg.Env == "live" }

// RecvWindow is the signed-request validity window.
func (b *Binance) RecvWindow() time.Duration { return b.cfg.RecvWindow }

func (b *Binance) CanTrade() error {
	if b.cfg.APIKey == "" || b.cfg.APISecret == "" {
		return apperr.New(apperr.HedgeNotConfigured, "HEDGE_API_KEY and HEDGE_API_SECRET must be set for the Binance venue", 503)
	}
	if b.Live() && !b.cfg.LiveEnabled {
		return apperr.New(apperr.LiveExecutionDisabled,
			"Live hedge execution is disabled. Set HEDGE_LIVE_EXECUTION_ENABLED=true to route orders to Binance.", 503)
	}
	return nil
}

func (b *Binance) canRead() error {
	if b.cfg.APIKey == "" || b.cfg.APISecret == "" {
		return apperr.New(apperr.HedgeNotConfigured, "HEDGE_API_KEY and HEDGE_API_SECRET must be set for the Binance venue", 503)
	}
	return nil
}

// Sign is HMAC-SHA256(secret, payload) in lowercase hex.
func Sign(secret, payload string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(payload))
	return hex.EncodeToString(m.Sum(nil))
}

type binanceErr struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// Binance error codes after which the outcome is unknown.
var unknownCodes = map[int]bool{-1000: true, -1001: true, -1006: true, -1007: true}

func (b *Binance) do(ctx context.Context, method, path string, params url.Values, signed bool, out any) error {
	if params == nil {
		params = url.Values{}
	}
	query := params.Encode()
	if signed {
		params.Set("recvWindow", strconv.FormatInt(b.cfg.RecvWindow.Milliseconds(), 10))
		params.Set("timestamp", strconv.FormatInt(b.Now().UnixMilli(), 10))
		query = params.Encode()
		query += "&signature=" + Sign(b.cfg.APISecret, query)
	}
	u := b.cfg.BaseURL + path
	if query != "" {
		u += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return err
	}
	if signed {
		req.Header.Set("X-MBX-APIKEY", b.cfg.APIKey)
	}
	res, err := b.client.Do(req)
	if err != nil {
		// Sent or not, we cannot know: the caller must query before acting.
		return &VenueError{Unknown: true, Msg: err.Error()}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return &VenueError{HTTPStatus: res.StatusCode, Unknown: true, Msg: err.Error()}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		var be binanceErr
		_ = json.Unmarshal(body, &be)
		ve := &VenueError{HTTPStatus: res.StatusCode, Code: be.Code, Msg: be.Msg}
		if ve.Msg == "" {
			ve.Msg = strings.TrimSpace(string(body[:min(len(body), 200)]))
		}
		switch {
		case res.StatusCode >= 500 || unknownCodes[be.Code]:
			ve.Unknown = true
		case be.Code == -2013 || be.Code == -2011:
			return errors.Join(ErrOrderNotFound, ve)
		}
		return ve
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &VenueError{HTTPStatus: res.StatusCode, Unknown: true, Msg: "unreadable response: " + err.Error()}
	}
	return nil
}

func (b *Binance) Ping(ctx context.Context) error {
	return b.do(ctx, http.MethodGet, "/fapi/v1/ping", nil, false, nil)
}

type exchangeInfo struct {
	Symbols []struct {
		Symbol  string `json:"symbol"`
		Status  string `json:"status"`
		Filters []struct {
			FilterType string `json:"filterType"`
			TickSize   string `json:"tickSize"`
			StepSize   string `json:"stepSize"`
			MinQty     string `json:"minQty"`
			MaxQty     string `json:"maxQty"`
			Notional   string `json:"notional"`
		} `json:"filters"`
	} `json:"symbols"`
}

// Rules reads the symbol's filters from exchangeInfo (cached for an hour).
// MARKET orders are bounded by MARKET_LOT_SIZE, so its limits win over
// LOT_SIZE where both are present.
func (b *Binance) Rules(ctx context.Context, symbol string) (models.HedgeRules, error) {
	b.rulesMu.Lock()
	if r, ok := b.rules[symbol]; ok && time.Since(b.rulesTime) < time.Hour {
		b.rulesMu.Unlock()
		return r, nil
	}
	b.rulesMu.Unlock()
	var info exchangeInfo
	if err := b.do(ctx, http.MethodGet, "/fapi/v1/exchangeInfo", nil, false, &info); err != nil {
		return models.HedgeRules{}, err
	}
	all := map[string]models.HedgeRules{}
	for _, s := range info.Symbols {
		var r models.HedgeRules
		var lot, market models.HedgeRules
		for _, f := range s.Filters {
			switch f.FilterType {
			case "PRICE_FILTER":
				r.TickSize = parseFloat(f.TickSize)
			case "LOT_SIZE":
				lot.StepE8, _ = ParseE8(f.StepSize)
				lot.MinQtyE8, _ = ParseE8(f.MinQty)
				lot.MaxQtyE8, _ = ParseE8(f.MaxQty)
			case "MARKET_LOT_SIZE":
				market.StepE8, _ = ParseE8(f.StepSize)
				market.MinQtyE8, _ = ParseE8(f.MinQty)
				market.MaxQtyE8, _ = ParseE8(f.MaxQty)
			case "MIN_NOTIONAL":
				r.MinNotional = parseFloat(f.Notional)
			}
		}
		r.StepE8, r.MinQtyE8, r.MaxQtyE8 = lot.StepE8, lot.MinQtyE8, lot.MaxQtyE8
		if market.StepE8 > 0 {
			r.StepE8 = max(r.StepE8, market.StepE8)
			r.MinQtyE8 = max(r.MinQtyE8, market.MinQtyE8)
			if market.MaxQtyE8 > 0 && (r.MaxQtyE8 == 0 || market.MaxQtyE8 < r.MaxQtyE8) {
				r.MaxQtyE8 = market.MaxQtyE8
			}
		}
		all[s.Symbol] = r
	}
	b.rulesMu.Lock()
	b.rules, b.rulesTime = all, time.Now()
	b.rulesMu.Unlock()
	r, ok := all[symbol]
	if !ok || r.StepE8 <= 0 {
		return models.HedgeRules{}, &VenueError{HTTPStatus: 400, Msg: "symbol " + symbol + " is not listed"}
	}
	return r, nil
}

func (b *Binance) Quote(ctx context.Context, symbol string) (Quote, error) {
	var r struct {
		BidPrice string `json:"bidPrice"`
		BidQty   string `json:"bidQty"`
		AskPrice string `json:"askPrice"`
		AskQty   string `json:"askQty"`
		Time     int64  `json:"time"`
	}
	if err := b.do(ctx, http.MethodGet, "/fapi/v1/ticker/bookTicker", url.Values{"symbol": {symbol}}, false, &r); err != nil {
		return Quote{}, err
	}
	return Quote{Bid: parseFloat(r.BidPrice), Ask: parseFloat(r.AskPrice), BidQty: parseFloat(r.BidQty),
		AskQty: parseFloat(r.AskQty), Time: r.Time}, nil
}

func (b *Binance) Mark(ctx context.Context, symbol string) (Mark, error) {
	var r struct {
		MarkPrice       string `json:"markPrice"`
		IndexPrice      string `json:"indexPrice"`
		LastFundingRate string `json:"lastFundingRate"`
		NextFundingTime int64  `json:"nextFundingTime"`
		Time            int64  `json:"time"`
	}
	if err := b.do(ctx, http.MethodGet, "/fapi/v1/premiumIndex", url.Values{"symbol": {symbol}}, false, &r); err != nil {
		return Mark{}, err
	}
	return Mark{MarkPrice: parseFloat(r.MarkPrice), IndexPrice: parseFloat(r.IndexPrice),
		FundingRate: parseFloat(r.LastFundingRate), NextFundingTime: r.NextFundingTime, Time: r.Time}, nil
}

func (b *Binance) Commission(ctx context.Context, symbol string) (Commission, error) {
	if err := b.canRead(); err != nil {
		return Commission{}, err
	}
	var r struct {
		Maker string `json:"makerCommissionRate"`
		Taker string `json:"takerCommissionRate"`
	}
	if err := b.do(ctx, http.MethodGet, "/fapi/v1/commissionRate", url.Values{"symbol": {symbol}}, true, &r); err != nil {
		return Commission{}, err
	}
	return Commission{MakerRate: parseFloat(r.Maker), TakerRate: parseFloat(r.Taker)}, nil
}

type binanceOrder struct {
	OrderID       int64  `json:"orderId"`
	Symbol        string `json:"symbol"`
	Status        string `json:"status"`
	ClientOrderID string `json:"clientOrderId"`
	Side          string `json:"side"`
	OrigQty       string `json:"origQty"`
	ExecutedQty   string `json:"executedQty"`
	CumQuote      string `json:"cumQuote"`
	AvgPrice      string `json:"avgPrice"`
	UpdateTime    int64  `json:"updateTime"`
}

func (o binanceOrder) state() (OrderState, error) {
	orig, err1 := ParseE8(o.OrigQty)
	exec, err2 := ParseE8(o.ExecutedQty)
	cum, err3 := ParseE8(o.CumQuote)
	if o.CumQuote == "" {
		cum, err3 = 0, nil
	}
	if err := errors.Join(err1, err2, err3); err != nil || o.ClientOrderID == "" {
		return OrderState{}, &VenueError{Unknown: true, Msg: "unreadable order state"}
	}
	return OrderState{
		Symbol: o.Symbol, ClientOrderID: o.ClientOrderID, VenueOrderID: strconv.FormatInt(o.OrderID, 10),
		Status: o.Status, Side: HedgeSide(o.Side), OrigQtyE8: orig, ExecutedQtyE8: exec, CumQuoteE8: cum,
		AvgPrice: parseFloat(o.AvgPrice), UpdateTime: o.UpdateTime,
	}, nil
}

func (b *Binance) PlaceOrder(ctx context.Context, req OrderRequest) (OrderState, error) {
	if err := b.CanTrade(); err != nil {
		return OrderState{}, err
	}
	params := url.Values{
		"symbol":           {req.Symbol},
		"side":             {string(req.Side)},
		"type":             {"MARKET"},
		"quantity":         {FormatE8(req.QtyE8)},
		"newClientOrderId": {req.ClientOrderID},
		// RESULT waits for the matching engine, so a market order normally
		// comes back FILLED (or EXPIRED with what filled).
		"newOrderRespType": {"RESULT"},
	}
	if req.ReduceOnly {
		params.Set("reduceOnly", "true")
	}
	var o binanceOrder
	if err := b.do(ctx, http.MethodPost, "/fapi/v1/order", params, true, &o); err != nil {
		return OrderState{}, err
	}
	return o.state()
}

func (b *Binance) QueryOrder(ctx context.Context, symbol, clientOrderID string) (OrderState, error) {
	if err := b.canRead(); err != nil {
		return OrderState{}, err
	}
	var o binanceOrder
	err := b.do(ctx, http.MethodGet, "/fapi/v1/order", url.Values{"symbol": {symbol}, "origClientOrderId": {clientOrderID}}, true, &o)
	if err != nil {
		return OrderState{}, err
	}
	return o.state()
}

func (b *Binance) CancelOrder(ctx context.Context, symbol, clientOrderID string) (OrderState, error) {
	if err := b.canRead(); err != nil {
		return OrderState{}, err
	}
	var o binanceOrder
	err := b.do(ctx, http.MethodDelete, "/fapi/v1/order", url.Values{"symbol": {symbol}, "origClientOrderId": {clientOrderID}}, true, &o)
	if err != nil {
		return OrderState{}, err
	}
	return o.state()
}

func (b *Binance) Fills(ctx context.Context, symbol, venueOrderID string) ([]Fill, error) {
	if err := b.canRead(); err != nil {
		return nil, err
	}
	var rows []struct {
		ID              int64  `json:"id"`
		OrderID         int64  `json:"orderId"`
		Symbol          string `json:"symbol"`
		Side            string `json:"side"`
		Price           string `json:"price"`
		Qty             string `json:"qty"`
		QuoteQty        string `json:"quoteQty"`
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
		RealizedPnl     string `json:"realizedPnl"`
		Maker           bool   `json:"maker"`
		Time            int64  `json:"time"`
	}
	params := url.Values{"symbol": {symbol}, "orderId": {venueOrderID}, "limit": {"1000"}}
	if err := b.do(ctx, http.MethodGet, "/fapi/v1/userTrades", params, true, &rows); err != nil {
		return nil, err
	}
	out := make([]Fill, 0, len(rows))
	for _, r := range rows {
		qty, e1 := ParseE8(r.Qty)
		quote, e2 := ParseE8(r.QuoteQty)
		fee, e3 := ParseE8(r.Commission)
		pnl, e4 := ParseE8(r.RealizedPnl)
		if r.RealizedPnl == "" {
			pnl, e4 = 0, nil
		}
		if err := errors.Join(e1, e2, e3, e4); err != nil {
			return nil, &VenueError{Unknown: true, Msg: "unreadable fill " + strconv.FormatInt(r.ID, 10)}
		}
		out = append(out, Fill{
			TradeID: r.ID, VenueOrderID: strconv.FormatInt(r.OrderID, 10), Symbol: r.Symbol, Side: HedgeSide(r.Side),
			Price: parseFloat(r.Price), QtyE8: qty, QuoteE8: quote, CommissionE8: fee, CommissionAsset: r.CommissionAsset,
			RealizedPnlE8: pnl, Maker: r.Maker, Time: r.Time,
		})
	}
	return out, nil
}

func (b *Binance) Position(ctx context.Context, symbol string) (Position, error) {
	if err := b.canRead(); err != nil {
		return Position{}, err
	}
	var rows []struct {
		Symbol           string `json:"symbol"`
		PositionAmt      string `json:"positionAmt"`
		EntryPrice       string `json:"entryPrice"`
		MarkPrice        string `json:"markPrice"`
		UnRealizedProfit string `json:"unRealizedProfit"`
	}
	if err := b.do(ctx, http.MethodGet, "/fapi/v2/positionRisk", url.Values{"symbol": {symbol}}, true, &rows); err != nil {
		return Position{}, err
	}
	p := Position{Symbol: symbol}
	// One-way mode reports one BOTH row; summing also covers hedge mode.
	for _, r := range rows {
		if r.Symbol != symbol {
			continue
		}
		q, err := ParseE8(r.PositionAmt)
		if err != nil {
			return Position{}, &VenueError{Unknown: true, Msg: "unreadable position"}
		}
		u, _ := ParseE8(r.UnRealizedProfit)
		p.QtyE8 += q
		p.UnrealizedE8 += u
		if q != 0 {
			p.EntryPrice = parseFloat(r.EntryPrice)
		}
		p.MarkPrice = parseFloat(r.MarkPrice)
	}
	return p, nil
}

func (b *Binance) Funding(ctx context.Context, symbol string, fromMs int64) ([]Income, error) {
	if err := b.canRead(); err != nil {
		return nil, err
	}
	var rows []struct {
		Symbol     string `json:"symbol"`
		IncomeType string `json:"incomeType"`
		Income     string `json:"income"`
		Asset      string `json:"asset"`
		Time       int64  `json:"time"`
		TranID     int64  `json:"tranId"`
	}
	params := url.Values{"symbol": {symbol}, "incomeType": {"FUNDING_FEE"}, "limit": {"1000"}}
	if fromMs > 0 {
		params.Set("startTime", strconv.FormatInt(fromMs, 10))
	}
	if err := b.do(ctx, http.MethodGet, "/fapi/v1/income", params, true, &rows); err != nil {
		return nil, err
	}
	out := make([]Income, 0, len(rows))
	for _, r := range rows {
		amt, err := ParseE8(r.Income)
		if err != nil {
			return nil, &VenueError{Unknown: true, Msg: "unreadable income " + strconv.FormatInt(r.TranID, 10)}
		}
		out = append(out, Income{TranID: r.TranID, Symbol: r.Symbol, Type: r.IncomeType, Asset: r.Asset, AmountE8: amt, Time: r.Time})
	}
	return out, nil
}
