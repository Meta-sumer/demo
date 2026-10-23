// Package health is liveness and readiness (spec section 40), mounted at the
// root.
//
// /health answers "is this process up"; /ready answers "should traffic be
// sent here", which additionally requires MongoDB and Redis to respond. The
// feed is reported but does not fail readiness: an instance with a degraded
// feed still serves history and settles existing trades, and the trade path
// refuses new business on its own.
package health

import (
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/workers"
)

// Mount registers /health and /ready.
func Mount(r chi.Router) {
	startedAt := time.Now()

	r.Get("/health", httpx.H(func(w http.ResponseWriter, r *http.Request) error {
		version := os.Getenv("npm_package_version")
		if version == "" {
			version = "1.0.0"
		}
		return httpx.OK(w, struct {
			Status        string `json:"status"`
			UptimeSeconds int64  `json:"uptimeSeconds"`
			Env           string `json:"env"`
			Version       string `json:"version"`
		}{"ok", int64(util.RoundHalfUp(float64(time.Since(startedAt).Milliseconds()) / 1000)), config.E.NodeEnv, version})
	}))

	r.Get("/ready", httpx.H(ready))
}

func ready(w http.ResponseWriter, r *http.Request) error {
	redisOK := redisx.Healthy(r.Context())
	feeds, err := marketdata.GetAllFeedHealth(r.Context())
	if err != nil {
		return err
	}
	// Only assets that are offered count: a switched-off asset has no feed by
	// design and would read as OFFLINE here.
	if active, err := assets.ListAssetDocs(r.Context(), true); err == nil {
		offered := make(map[string]bool, len(active))
		for _, a := range active {
			offered[a.Symbol] = true
		}
		kept := feeds[:0]
		for _, f := range feeds {
			if offered[f.Symbol] {
				kept = append(kept, f)
			}
		}
		feeds = kept
	}
	mongoOK := mongox.Healthy(r.Context())
	provider := marketdata.GetProviderStatus()

	providerName := config.E.MarketDataProvider
	state := "UNKNOWN"
	if provider != nil {
		providerName = provider.Provider
		state = string(provider.State)
	}
	healthy := 0
	for _, f := range feeds {
		if f.Status == models.FeedStatus("HEALTHY") {
			healthy++
		}
	}

	type mongoCheck struct {
		OK           bool `json:"ok"`
		Transactions bool `json:"transactions"`
	}
	type redisCheck struct {
		OK bool `json:"ok"`
	}
	// Monitoring only: neither fails readiness. LIVE trading refuses on its
	// own while the book is not READY; the limiter never fails open. No
	// reasons or ids are shown here (this route is public).
	type bookCheck struct {
		Status              string `json:"status"`
		PendingReservations int64  `json:"pendingReservations"`
	}
	type limiterCheck struct {
		// "redis" (limits shared by every instance) or "memory" (fallback).
		Mode          string `json:"mode"`
		DegradedSince int64  `json:"degradedSince"`
	}
	book := bookCheck{Status: "UNKNOWN"}
	if redisOK {
		if b, err := exposure.GetBookStatus(r.Context()); err == nil {
			book = bookCheck{Status: b.Status, PendingReservations: b.PendingReservations}
		}
	}
	lim := httpx.RateLimiterStatus()
	limiter := limiterCheck{Mode: "redis", DegradedSince: lim.DegradedSince}
	if lim.Degraded {
		limiter.Mode = "memory"
	}
	type feedCheck struct {
		Provider      string `json:"provider"`
		State         string `json:"state"`
		HealthyAssets int    `json:"healthyAssets"`
		TotalAssets   int    `json:"totalAssets"`
	}
	checks := struct {
		MongoDB    mongoCheck           `json:"mongodb"`
		Redis      redisCheck           `json:"redis"`
		MarketFeed feedCheck            `json:"marketFeed"`
		Workers    workers.WorkerStatus `json:"workers"`
		Websocket  ws.Stats             `json:"websocket"`
		Book       bookCheck            `json:"exposureBook"`
		Limiter    limiterCheck         `json:"rateLimiter"`
	}{
		mongoCheck{mongoOK, mongox.TransactionsAvailable()},
		redisCheck{redisOK},
		feedCheck{providerName, state, healthy, len(feeds)},
		workers.Status(),
		ws.GetStats(),
		book,
		limiter,
	}
	isReady := mongoOK && redisOK
	status := http.StatusOK
	if !isReady {
		status = http.StatusServiceUnavailable
	}
	return httpx.Send(w, status, struct {
		Ready  bool `json:"ready"`
		Checks any  `json:"checks"`
	}{isReady, checks}, nil)
}
