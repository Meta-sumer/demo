package beex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
)

const (
	// requestTimeout bounds one call. A payout POST that times out is not a
	// failure - its outcome is unknown and the retry loop resolves it.
	requestTimeout = 20 * time.Second
	maxResponseLen = 1 << 20
)

// Client is the real BeeX HTTP API. The API key goes in the x-api-key
// header and nowhere else: it is never logged or stored.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// NewClient: baseURL like https://beexpayments.com/api/v1.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		http: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				Proxy:                 http.ProxyFromEnvironment,
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout:   10 * time.Second,
				ResponseHeaderTimeout: requestTimeout,
				MaxIdleConnsPerHost:   8,
				IdleConnTimeout:       90 * time.Second,
			},
		},
	}
}

func (c *Client) Name() string     { return "beex" }
func (c *Client) Mode() string     { return config.BeexKeyMode(c.apiKey) }
func (c *Client) Configured() bool { return c.apiKey != "" && c.baseURL != "" }

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (json.RawMessage, error) {
	if !c.Configured() {
		return nil, notConfigured("BEEX_API_KEY is not set")
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("User-Agent", "binary-trading-server/1 (+beex-client)")

	started := time.Now()
	res, err := c.http.Do(req)
	if err != nil {
		code := codeNetwork
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			code = codeTimeout
		}
		// url.Error carries the URL, never the header, so this is key-free.
		logx.Warn("beex request failed", "method", method, "path", path, "code", code,
			"durationMs", time.Since(started).Milliseconds(), "err", err.Error())
		return nil, &Error{Code: code, Message: err.Error(), Temporary: true}
	}
	defer res.Body.Close()
	text, err := io.ReadAll(io.LimitReader(res.Body, maxResponseLen))
	if err != nil {
		return nil, &Error{Status: res.StatusCode, Code: codeNetwork, Message: err.Error(), Temporary: true}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		e := parseErrorBody(res.StatusCode, text)
		logx.Warn("beex request rejected", "method", method, "path", path, "status", res.StatusCode,
			"code", e.Code, "durationMs", time.Since(started).Milliseconds(), "body", truncate(string(text), 500))
		return nil, e
	}
	payload := unwrapData(text)
	if out != nil {
		if err := json.Unmarshal(payload, out); err != nil {
			logx.Error("beex response could not be decoded", "method", method, "path", path, "err", err.Error(),
				"body", truncate(string(text), 500))
			// The request was accepted; only our reading of the answer failed.
			return nil, &Error{Status: res.StatusCode, Code: codeDecode, Message: err.Error(), Raw: truncate(string(text), maxStoredErrorBodyCharacters), Temporary: true}
		}
	}
	return payload, nil
}

// unwrapData accepts both a bare object and {data:{...}}: the response
// envelope is not documented for every endpoint.
func unwrapData(body []byte) json.RawMessage {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return body
	}
	data, ok := top["data"]
	if !ok {
		return body
	}
	_, hasID := top["id"]
	_, hasRef := top["reference"]
	trimmed := bytes.TrimSpace(data)
	if !hasID && !hasRef && len(trimmed) > 0 && trimmed[0] == '{' {
		return data
	}
	return body
}

func (c *Client) CreateInvoice(ctx context.Context, req CreateInvoiceRequest) (*Invoice, error) {
	var inv Invoice
	raw, err := c.do(ctx, http.MethodPost, "/invoices", req, &inv)
	if err != nil {
		return nil, err
	}
	inv.Raw = raw
	return &inv, nil
}

func (c *Client) GetInvoice(ctx context.Context, id string) (*Invoice, error) {
	var inv Invoice
	raw, err := c.do(ctx, http.MethodGet, "/invoices/"+url.PathEscape(id), nil, &inv)
	if err != nil {
		return nil, err
	}
	inv.Raw = raw
	return &inv, nil
}

func (c *Client) CreatePayout(ctx context.Context, req CreatePayoutRequest) (*Payout, error) {
	var p Payout
	raw, err := c.do(ctx, http.MethodPost, "/payouts", req, &p)
	if err != nil {
		return nil, err
	}
	p.Raw = raw
	return &p, nil
}

func (c *Client) GetPayout(ctx context.Context, reference string) (*Payout, error) {
	var p Payout
	raw, err := c.do(ctx, http.MethodGet, "/payouts/"+url.PathEscape(reference), nil, &p)
	if err != nil {
		return nil, err
	}
	p.Raw = raw
	return &p, nil
}

func (c *Client) GetPayoutLimits(ctx context.Context) (*PayoutLimits, error) {
	var l PayoutLimits
	if _, err := c.do(ctx, http.MethodGet, "/payout-limits", nil, &l); err != nil {
		return nil, err
	}
	return &l, nil
}

func (c *Client) GetBalance(ctx context.Context) (Balance, error) {
	var b any
	if _, err := c.do(ctx, http.MethodGet, "/balance", nil, &b); err != nil {
		return nil, err
	}
	return b, nil
}
