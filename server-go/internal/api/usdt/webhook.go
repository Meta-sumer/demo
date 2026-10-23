package usdt

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	usdtsvc "binarytrading/internal/service/usdt"
)

// WebhookPath is where BeeX delivers every event (invoices and payouts).
const WebhookPath = "/webhooks/beex/crypto"

// webhookLimit is per source address: generous for BeeX's own retries, and
// it keeps an unsigned flood from spending the server's HMAC time.
var webhookLimit = httpx.RateLimitOptions{Bucket: "webhook:beex", Limit: 600, Window: time.Minute, By: "ip"}

// MountWebhook registers POST /webhooks/beex/crypto on the root router. It
// is public: the signature is the authentication.
func MountWebhook(r chi.Router) {
	r.With(httpx.RateLimit(webhookLimit)).Post(WebhookPath, httpx.H(receiveWebhook))
}

// receiveWebhook hands the body to the service exactly as it arrived - the
// signature covers those bytes, so nothing may parse or re-encode them first.
// A stored delivery is answered 200 whatever processing did (the retry loop
// owns failures); only an unverifiable one is refused, so BeeX retries it.
func receiveWebhook(w http.ResponseWriter, r *http.Request) error {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, usdtsvc.MaxWebhookBody))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return apperr.New(apperr.PayloadTooLarge, "Webhook body is too large", http.StatusRequestEntityTooLarge)
		}
		return err
	}
	res, err := usdtsvc.ReceiveWebhook(r.Context(), raw, r.Header, httpx.ClientIP(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, res)
}
