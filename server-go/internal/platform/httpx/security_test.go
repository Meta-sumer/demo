package httpx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
)

func prefixes(t *testing.T, raw string) []netip.Prefix {
	t.Helper()
	p, err := config.ParseTrustedProxies(raw)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestClientIPIgnoresSpoofedForwardedForFromUntrustedPeer(t *testing.T) {
	logx.Init("silent", "test")
	trusted := prefixes(t, config.DefaultTrustedProxies)
	h := http.Header{}
	h.Set("X-Forwarded-For", "1.2.3.4")
	h.Set("X-Real-IP", "5.6.7.8")
	if got := ClientIPFrom("203.0.113.9:5555", h, trusted); got != "203.0.113.9" {
		t.Fatalf("untrusted peer: got %q, want the peer address", got)
	}
	// The same headers from the loopback proxy are believed.
	if got := ClientIPFrom("127.0.0.1:5555", h, trusted); got != "1.2.3.4" {
		t.Fatalf("trusted peer: got %q", got)
	}
}

func TestClientIPTakesRightmostUntrustedHop(t *testing.T) {
	trusted := prefixes(t, "127.0.0.1/32, 172.16.0.0/12")
	cases := []struct {
		name, peer, xff, want string
	}{
		// The client prepends a fake hop; the proxy appends the real peer.
		{"spoof prepended", "172.18.0.1:40000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		// Two trusted proxies in a row are skipped.
		{"proxy chain", "127.0.0.1:1", "198.51.100.7, 172.18.0.5", "198.51.100.7"},
		// Every hop trusted: the left-most is the origin.
		{"all trusted", "127.0.0.1:1", "172.18.0.9, 172.18.0.5", "172.18.0.9"},
		// Garbage left of the last trusted hop is ignored.
		{"garbage", "127.0.0.1:1", "not-an-ip, 172.18.0.5", "172.18.0.5"},
		{"ipv6 with port", "127.0.0.1:1", "[2001:db8::1]:443", "2001:db8::1"},
		{"ipv4-mapped", "127.0.0.1:1", "::ffff:198.51.100.7", "198.51.100.7"},
		{"no header", "172.18.0.1:40000", "", "172.18.0.1"},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.xff != "" {
			h.Set("X-Forwarded-For", c.xff)
		}
		if got := ClientIPFrom(c.peer, h, trusted); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestClientIPUsesConfiguredProxies(t *testing.T) {
	prev := config.E
	t.Cleanup(func() { config.E = prev })
	config.E = &config.Env{NodeEnv: "test", TrustedProxies: prefixes(t, "10.0.0.0/8")}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "10.1.2.3:999"
	r.Header.Set("X-Forwarded-For", "198.51.100.20")
	if got := ClientIP(r); got != "198.51.100.20" {
		t.Fatalf("configured proxy: got %q", got)
	}
	// Loopback is not trusted unless listed.
	r.RemoteAddr = "127.0.0.1:999"
	if got := ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("unlisted loopback: got %q", got)
	}
}

// limitedHandler wraps an OK handler in a 3-per-minute IP limit with a bucket
// of its own.
func limitedHandler(bucket string) http.Handler {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	return RateLimit(RateLimitOptions{Bucket: bucket, Limit: 3, Window: time.Minute, By: "ip"})(ok)
}

func hit(h http.Handler, peer, xff string) int {
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.RemoteAddr = peer
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code
}

func resetLimiter(t *testing.T) {
	t.Helper()
	prevCounter := rateCounter
	t.Cleanup(func() {
		rateCounter = prevCounter
		memLimiter.reset()
		limiterSkipUntil.Store(0)
		limiterDegraded.Store(false)
	})
	memLimiter.reset()
	limiterSkipUntil.Store(0)
	limiterDegraded.Store(false)
}

func TestRateLimitRedisDownStillLimits(t *testing.T) {
	logx.Init("silent", "test")
	resetLimiter(t)
	calls := 0
	rateCounter = func(context.Context, string, time.Duration) (int64, error) {
		calls++
		return 0, errors.New("dial tcp: connection refused")
	}
	h := limitedHandler("test:outage:" + bson.NewObjectID().Hex())
	for i := 1; i <= 3; i++ {
		if code := hit(h, "198.51.100.1:1000", ""); code != http.StatusNoContent {
			t.Fatalf("request %d: %d, want 204", i, code)
		}
	}
	if code := hit(h, "198.51.100.1:1000", ""); code != http.StatusTooManyRequests {
		t.Fatalf("4th request with Redis down: %d, want 429 (the limiter must not fail open)", code)
	}
	// Another client has its own window.
	if code := hit(h, "198.51.100.2:1000", ""); code != http.StatusNoContent {
		t.Fatalf("other client: %d", code)
	}
	st := RateLimiterStatus()
	if !st.Degraded || st.FallbackRequests < 5 || st.DegradedSince == 0 {
		t.Fatalf("status %+v, want degraded with the fallback counted", st)
	}
	// While Redis is in back-off it is not retried on every request.
	if calls != 1 {
		t.Fatalf("Redis tried %d times during the back-off, want 1", calls)
	}
}

func TestRateLimitSpoofedForwardedForDoesNotEscapeLimit(t *testing.T) {
	logx.Init("silent", "test")
	resetLimiter(t)
	rateCounter = func(context.Context, string, time.Duration) (int64, error) { return 0, errors.New("down") }
	h := limitedHandler("test:spoof:" + bson.NewObjectID().Hex())
	codes := []int{}
	for i := 0; i < 5; i++ {
		// A fresh fake address on every request, from an untrusted peer.
		codes = append(codes, hit(h, "203.0.113.50:4000", "10.0.0."+strconv.Itoa(i)))
	}
	if codes[3] != http.StatusTooManyRequests || codes[4] != http.StatusTooManyRequests {
		t.Fatalf("codes %v: rotating X-Forwarded-For bypassed the IP limit", codes)
	}
}

// TestRateLimitFallsBackWhenRedisUnreachable uses a real client pointed at a
// closed port rather than a stub.
func TestRateLimitFallsBackWhenRedisUnreachable(t *testing.T) {
	logx.Init("silent", "test")
	resetLimiter(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := redisx.Connect(ctx, "redis://127.0.0.1:1/0"); err == nil {
		t.Skip("something answers on 127.0.0.1:1")
	}
	t.Cleanup(func() { _ = redisx.Disconnect() })
	h := limitedHandler("test:dead:" + bson.NewObjectID().Hex())
	got := []int{}
	for i := 0; i < 4; i++ {
		got = append(got, hit(h, "198.51.100.9:1", ""))
	}
	if got[0] != http.StatusNoContent || got[3] != http.StatusTooManyRequests {
		t.Fatalf("codes %v, want 204 x3 then 429", got)
	}
	if !RateLimiterStatus().Degraded {
		t.Fatal("limiter should report degraded mode")
	}
}

func TestMemoryWindowExpires(t *testing.T) {
	m := &memoryWindows{counts: map[string]memoryCount{}, maxKeys: 10}
	if n := m.incr("k", 1000, 2000); n != 1 {
		t.Fatal(n)
	}
	if n := m.incr("k", 1500, 2000); n != 2 {
		t.Fatal(n)
	}
	// A new window starts once the old one has expired.
	if n := m.incr("k", 2000, 3000); n != 1 {
		t.Fatalf("after expiry: %d", n)
	}
}
