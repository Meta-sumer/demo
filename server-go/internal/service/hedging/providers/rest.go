package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/util"
)

// restBase is the shared HTTP scaffolding for real execution venues.
//
// Endpoint paths and payload mapping differ per venue; what is shared is the
// part that must not vary: credentials are required, live execution is refused
// unless explicitly enabled, and every request carries the idempotency key so
// a retry cannot double-hedge at the venue.
type restBase struct {
	name      string
	baseURL   string
	apiKey    string
	apiSecret string
	client    *http.Client
}

func newRestBase(name string) restBase {
	b := restBase{name: name, client: &http.Client{Timeout: 30 * time.Second}}
	if config.E != nil {
		b.baseURL = config.E.HedgeAPIURL
		b.apiKey = config.E.HedgeAPIKey
		b.apiSecret = config.E.HedgeAPISecret
	}
	return b
}

func (b *restBase) Name() string { return b.name }
func (b *restBase) Live() bool   { return true }

// assertExecutable refuses to send an order unless the deployment is
// deliberately configured.
func (b *restBase) assertExecutable() error {
	if config.E == nil || !config.E.HedgeLiveExecutionEnabled {
		return apperr.New(apperr.LiveExecutionDisabled,
			"Live hedge execution is disabled. Set HEDGE_LIVE_EXECUTION_ENABLED=true to route orders to "+b.name+".", 503)
	}
	if b.baseURL == "" || b.apiKey == "" || b.apiSecret == "" {
		return apperr.New(apperr.HedgeNotConfigured,
			"HEDGE_API_URL, HEDGE_API_KEY and HEDGE_API_SECRET must all be set for the "+b.name+" provider", 503)
	}
	return nil
}

// request sends a JSON request and decodes the response into out (may be nil).
func (b *restBase) request(ctx context.Context, method, path string, body any, idempotencyKey string, out any) error {
	if err := b.assertExecutable(); err != nil {
		return err
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(b.baseURL, "/")+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", b.apiKey)
	if idempotencyKey != "" {
		req.Header.Set("idempotency-key", idempotencyKey)
	}

	res, err := b.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	text, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		snippet := string(text)
		if len(snippet) > 500 {
			snippet = snippet[:500]
		}
		logx.Error("hedge venue rejected request", "provider", b.name, "status", res.StatusCode, "body", snippet)
		return apperr.New(apperr.HedgeProviderError, b.name+" responded "+strconv.Itoa(res.StatusCode), 502)
	}
	if out == nil || len(text) == 0 {
		return nil
	}
	return json.Unmarshal(text, out)
}

func (b *restBase) Healthy(ctx context.Context) bool {
	if b.assertExecutable() != nil {
		return false
	}
	return b.request(ctx, http.MethodGet, "/health", nil, "", nil) == nil
}

type positionResponse struct {
	NetNotional   *float64 `json:"netNotional"`
	AveragePrice  *float64 `json:"averagePrice"`
	UnrealisedPnl *float64 `json:"unrealisedPnl"`
}

func (b *restBase) GetPosition(ctx context.Context, symbol string) (Position, error) {
	var r positionResponse
	if err := b.request(ctx, http.MethodGet, "/positions/"+encodeURIComponent(symbol), nil, "", &r); err != nil {
		return Position{}, err
	}
	return Position{Symbol: symbol, NetNotional: deref(r.NetNotional), AveragePrice: r.AveragePrice, UnrealisedPnl: deref(r.UnrealisedPnl)}, nil
}

func deref(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// Broker execution adapter.
//
// The request/response mapping below is the generic REST shape this platform
// expects. Point HEDGE_API_URL at an adapter service that speaks it, or replace
// the mapping with the broker's own contract - the engine only sees Provider.
// Until HEDGE_LIVE_EXECUTION_ENABLED is set together with credentials, every
// call fails closed and no order is transmitted.
type BrokerProvider struct{ restBase }

func NewBrokerProvider() *BrokerProvider { return &BrokerProvider{newRestBase("broker")} }

type brokerOrderResponse struct {
	OrderID        string   `json:"orderId"`
	Status         string   `json:"status"`
	FilledNotional *float64 `json:"filledNotional"`
	AvgPrice       *float64 `json:"avgPrice"`
	Message        string   `json:"message"`
}

func (r brokerOrderResponse) toResult() OrderResult {
	return OrderResult{
		ProviderOrderID:  r.OrderID,
		Status:           normalise(r.Status, "FILLED", "COMPLETE"),
		FilledNotional:   deref(r.FilledNotional),
		AverageFillPrice: r.AvgPrice,
		Error:            r.Message,
		Raw:              r,
	}
}

func (p *BrokerProvider) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error) {
	var r brokerOrderResponse
	err := p.request(ctx, http.MethodPost, "/orders", map[string]any{
		"clientOrderId":  req.Reference,
		"symbol":         req.Symbol,
		"side":           req.Side,
		"type":           "MARKET",
		"notional":       req.Notional,
		"referencePrice": req.ReferencePrice,
	}, req.IdempotencyKey, &r)
	if err != nil {
		return OrderResult{}, err
	}
	return r.toResult(), nil
}

func (p *BrokerProvider) CancelOrder(ctx context.Context, providerOrderID string) error {
	return p.request(ctx, http.MethodDelete, "/orders/"+encodeURIComponent(providerOrderID), nil, "", nil)
}

func (p *BrokerProvider) GetOrder(ctx context.Context, providerOrderID string) (OrderResult, error) {
	var r brokerOrderResponse
	if err := p.request(ctx, http.MethodGet, "/orders/"+encodeURIComponent(providerOrderID), nil, "", &r); err != nil {
		return OrderResult{}, err
	}
	return r.toResult(), nil
}

// Liquidity provider adapter.
//
// An LP typically quotes then fills, rather than accepting a market order, so
// this adapter asks for a quote and executes against it. Like the broker
// adapter it fails closed until live execution is explicitly enabled.
type LPProvider struct{ restBase }

func NewLPProvider() *LPProvider { return &LPProvider{newRestBase("lp")} }

type lpQuoteResponse struct {
	QuoteID     string  `json:"quoteId"`
	Price       float64 `json:"price"`
	MaxNotional float64 `json:"maxNotional"`
	ExpiresAt   float64 `json:"expiresAt"`
}

type lpFillResponse struct {
	ExecutionID    string   `json:"executionId"`
	Status         string   `json:"status"`
	FilledNotional *float64 `json:"filledNotional"`
	Price          *float64 `json:"price"`
	Reason         string   `json:"reason"`
}

func (p *LPProvider) PlaceOrder(ctx context.Context, req OrderRequest) (OrderResult, error) {
	var quote lpQuoteResponse
	err := p.request(ctx, http.MethodPost, "/quotes", map[string]any{
		"symbol": req.Symbol, "side": req.Side, "notional": req.Notional,
	}, req.IdempotencyKey+":quote", &quote)
	if err != nil {
		return OrderResult{}, err
	}

	if quote.ExpiresAt <= float64(util.NowMs()) {
		return OrderResult{
			ProviderOrderID: quote.QuoteID,
			Status:          StateFailed,
			Error:           "Quote expired before it could be executed",
		}, nil
	}

	// Never execute more than the LP is willing to show.
	notional := min(float64(req.Notional), quote.MaxNotional)

	var fill lpFillResponse
	err = p.request(ctx, http.MethodPost, "/executions", map[string]any{
		"quoteId": quote.QuoteID, "clientOrderId": req.Reference, "notional": notional,
	}, req.IdempotencyKey, &fill)
	if err != nil {
		return OrderResult{}, err
	}

	price := fill.Price
	if price == nil {
		qp := quote.Price
		price = &qp
	}
	return OrderResult{
		ProviderOrderID:  fill.ExecutionID,
		Status:           normalise(fill.Status, "FILLED", "EXECUTED"),
		FilledNotional:   deref(fill.FilledNotional),
		AverageFillPrice: price,
		Error:            fill.Reason,
		Raw:              map[string]any{"quote": quote, "fill": fill},
	}, nil
}

func (p *LPProvider) CancelOrder(ctx context.Context, providerOrderID string) error {
	return p.request(ctx, http.MethodPost, "/executions/"+encodeURIComponent(providerOrderID)+"/cancel", nil, "", nil)
}

func (p *LPProvider) GetOrder(ctx context.Context, providerOrderID string) (OrderResult, error) {
	var fill lpFillResponse
	if err := p.request(ctx, http.MethodGet, "/executions/"+encodeURIComponent(providerOrderID), nil, "", &fill); err != nil {
		return OrderResult{}, err
	}
	return OrderResult{
		ProviderOrderID:  providerOrderID,
		Status:           normalise(fill.Status, "FILLED", "EXECUTED"),
		FilledNotional:   deref(fill.FilledNotional),
		AverageFillPrice: fill.Price,
		Error:            fill.Reason,
	}, nil
}

// encodeURIComponent approximates the JS function (which also leaves !'()*).
func encodeURIComponent(s string) string {
	r := strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
	for _, c := range []string{"!", "'", "(", ")", "*"} {
		r = strings.ReplaceAll(r, url.QueryEscape(c), c)
	}
	return r
}
