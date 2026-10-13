package beex

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Webhook signatures: header X-Signature "t=<unix seconds>,v1=<hex>", where
// v1 = hex(HMAC-SHA256(secret, t + "." + rawBody)). The raw bytes are what is
// signed, so the body must be verified before anything parses it.

// SignatureHeader is the header BeeX signs webhooks with.
const SignatureHeader = "X-Signature"

// SignatureTolerance: older (or further in the future) than this is refused,
// so a captured delivery cannot be replayed later.
const SignatureTolerance = 5 * time.Minute

var (
	ErrSignatureMissing   = errors.New("signature header missing")
	ErrSignatureMalformed = errors.New("signature header malformed")
	ErrSignatureMismatch  = errors.New("signature does not match")
	ErrSignatureStale     = errors.New("signature timestamp too old")
	ErrSignatureFuture    = errors.New("signature timestamp in the future")
	ErrNoSecret           = errors.New("webhook secret not configured")
)

// Sign returns the hex v1 signature of body at unix time ts.
func Sign(secret string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// SignatureHeaderValue builds the X-Signature value (for tests and beexsim).
func SignatureHeaderValue(secret string, ts int64, body []byte) string {
	return "t=" + strconv.FormatInt(ts, 10) + ",v1=" + Sign(secret, ts, body)
}

// VerifySignature checks header against body and returns the signed
// timestamp. Comparison is constant time; any v1 may match (rotation).
func VerifySignature(header string, body []byte, secret string, now time.Time) (int64, error) {
	if secret == "" {
		return 0, ErrNoSecret
	}
	header = strings.TrimSpace(header)
	if header == "" {
		return 0, ErrSignatureMissing
	}
	var ts int64
	tsSet := false
	var sigs [][]byte
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(k) {
		case "t":
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			if err != nil || tsSet {
				return 0, ErrSignatureMalformed
			}
			ts, tsSet = n, true
		case "v1":
			b, err := hex.DecodeString(strings.TrimSpace(v))
			if err == nil && len(b) == sha256.Size {
				sigs = append(sigs, b)
			}
		}
	}
	if !tsSet || len(sigs) == 0 {
		return 0, ErrSignatureMalformed
	}
	signed := time.Unix(ts, 0)
	if now.Sub(signed) > SignatureTolerance {
		return ts, ErrSignatureStale
	}
	if signed.Sub(now) > SignatureTolerance {
		return ts, ErrSignatureFuture
	}
	expected, _ := hex.DecodeString(Sign(secret, ts, body))
	for _, s := range sigs {
		if hmac.Equal(s, expected) {
			return ts, nil
		}
	}
	return ts, ErrSignatureMismatch
}
