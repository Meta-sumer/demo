// Package public is the read-only surface a signed-out page may call,
// mounted at /public. Nothing here needs a session, reads the caller's
// identity or returns user data.
package public

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/pricing"
)

// marketsLimit is per source address. A landing page polls every 10-15 s
// (4-6 a minute), so this leaves room for many visitors behind one carrier
// NAT while a flood still stops at the limiter rather than the board.
var marketsLimit = httpx.RateLimitOptions{Bucket: "public:markets", Limit: 300, Window: time.Minute, By: "ip"}

// Mount registers GET /markets.
func Mount(r chi.Router) {
	r.With(httpx.RateLimit(marketsLimit)).Get("/markets", httpx.H(markets))
}

// markets is the landing page's market list: the active, priced instruments
// and the pricing engine's current indicative payout per expiry (the lower
// of UP and DOWN), from a snapshot rebuilt at most every few seconds.
func markets(w http.ResponseWriter, r *http.Request) error {
	m, err := pricing.GetPublicMarkets(r.Context())
	if err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "public, max-age="+strconv.FormatInt(m.MaxAgeSeconds, 10))
	return httpx.OK(w, m)
}
