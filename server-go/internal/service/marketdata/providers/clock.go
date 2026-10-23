package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
)

// ClockSource is implemented by providers that can report their own clock,
// so the feed leader can check the server clock against it (feed data rules,
// docs/audit/operations/feed-rules.md). Optional: a provider without one
// reports the check as not supported.
type ClockSource interface {
	// ServerTime is the provider's current time, epoch ms.
	ServerTime(ctx context.Context) (int64, error)
}

// BinanceTimeURL is Binance's public server-time endpoint (no key needed).
// Tests point it at a local server.
var BinanceTimeURL = "https://api.binance.com/api/v3/time"

var clockHTTP = &http.Client{Timeout: 5 * time.Second}

// ServerTime implements ClockSource: GET /api/v3/time -> {"serverTime": ms}.
func (b *Binance) ServerTime(ctx context.Context) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BinanceTimeURL, nil)
	if err != nil {
		return 0, err
	}
	res, err := clockHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4096))
	if err != nil {
		return 0, err
	}
	if res.StatusCode != http.StatusOK {
		return 0, errors.New("binance time: HTTP " + strconv.Itoa(res.StatusCode))
	}
	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ServerTime <= 0 {
		return 0, errors.New("binance time: unexpected response")
	}
	return out.ServerTime, nil
}
