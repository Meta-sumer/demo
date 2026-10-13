// Package marketdata serves read-only market data for the client chart and
// status indicators (mounted at /market-data).
package marketdata

import (
	"math"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/assets"
	md "binarytrading/internal/service/marketdata"
)

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	read := r.With(httpx.RateLimit(httpx.RateLimits.Read))

	read.Get("/health", httpx.H(feedHealth))
	read.Get("/{id}/price", httpx.H(price))
	read.Get("/{id}/candles", httpx.H(candles))
	read.Get("/{id}/ticks", httpx.H(ticks))
}

func feedHealth(w http.ResponseWriter, r *http.Request) error {
	feeds, err := md.GetAllFeedHealth(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, feeds)
}

func price(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	asset, err := assets.RequireAssetDoc(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	tick, err := md.GetLastTick(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	feed, err := md.GetFeedHealth(r.Context(), id.Hex(), asset.Symbol)
	if err != nil {
		return err
	}
	return httpx.OK(w, struct {
		Tick *models.NormalisedTick    `json:"tick"`
		Feed models.FeedHealthSnapshot `json:"feed"`
	}{tick, feed})
}

func candles(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	limit := q.Int("limit", 300, 10, 1000)
	// Constrained to the supported set so a caller cannot ask the server to
	// fold an unbounded number of stored minutes into one bucket.
	interval := q.Int("interval", 60, math.MinInt64, math.MaxInt64)
	// The refinement only runs once the value parsed as an integer.
	probe := httpx.Query(r)
	probe.Int("interval", 60, math.MinInt64, math.MaxInt64)
	if q.Has("interval") && probe.V.OK() {
		q.V.Check(md.IsSupportedCandleInterval(interval), "interval", "Invalid input")
	}
	// Optional end of the series (epoch ms or ISO date): the candles whose
	// openTime <= to. Absent, the series ends now.
	to := epochMs(q.OptDate("to"))
	if err := q.Err(); err != nil {
		return err
	}
	out, err := md.GetCandles(r.Context(), id.Hex(), limit, interval, to)
	if err != nil {
		return err
	}
	return httpx.OK(w, out)
}

// ticks returns raw ticks in a window: [from, to] when both are given (from
// before to, at most the 15 minutes the history retains), [from, now] with
// only from, the windowMs before to with only to, and the last windowMs
// without either. A window older than the retention is not an error; it
// returns what is still kept, possibly nothing.
func ticks(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	windowMs := q.Int("windowMs", md.DefaultRecentTicksWindowMs, 1000, config.TickHistoryRetentionMs)
	from, to := epochMs(q.OptDate("from")), epochMs(q.OptDate("to"))
	if from != nil && to != nil {
		if q.V.Check(*from < *to, "from", "from must be before to") {
			q.V.Check(*to-*from <= config.TickHistoryRetentionMs, "to", "The window from..to must not exceed 15 minutes")
		}
	}
	if err := q.Err(); err != nil {
		return err
	}

	var out []models.NormalisedTick
	switch {
	case from != nil:
		out, err = md.GetTicksBetween(r.Context(), id.Hex(), *from, to)
	case to != nil:
		out, err = md.GetTicksBetween(r.Context(), id.Hex(), *to-windowMs, to)
	default:
		out, err = md.GetRecentTicks(r.Context(), id.Hex(), windowMs)
	}
	if err != nil {
		return err
	}
	return httpx.OK(w, out)
}

// epochMs converts a coerced date parameter to epoch milliseconds.
func epochMs(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	ms := t.UnixMilli()
	return &ms
}
