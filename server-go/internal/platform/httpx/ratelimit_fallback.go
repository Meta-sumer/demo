package httpx

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
)

// Rate limiting when Redis is unavailable.
//
// The limiter never fails open: without Redis, every limited route falls back
// to a per-instance in-memory fixed window with the same limits. Across N
// instances a client can then get up to N times the limit, which is still a
// limit; failing open would be none (login brute force, trade floods). While
// Redis is failing it is skipped for a short back-off, so a dead Redis costs
// one timeout per back-off rather than one per request.

const (
	// redisLimiterTimeout caps how long a request waits for Redis.
	redisLimiterTimeout = 250 * time.Millisecond
	// redisLimiterBackoff is how long Redis is skipped after it fails.
	redisLimiterBackoff = 5 * time.Second
	// degradedLogEvery throttles the "still degraded" log line.
	degradedLogEvery = 30 * time.Second
)

// rateCounter increments key within its window and returns the new count.
// A package variable so tests can simulate an outage.
var rateCounter = redisRateCounter

func redisRateCounter(ctx context.Context, key string, window time.Duration) (int64, error) {
	rc := redisx.R()
	if rc == nil {
		return 0, errNoRedis
	}
	ctx, cancel := context.WithTimeout(ctx, redisLimiterTimeout)
	defer cancel()
	pipe := rc.Pipeline()
	incr := pipe.Incr(ctx, key)
	pipe.PExpire(ctx, key, window)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return incr.Val(), nil
}

type limiterError string

func (e limiterError) Error() string { return string(e) }

const errNoRedis = limiterError("redis is not connected")

// memoryWindows is the in-process fixed-window counter.
type memoryWindows struct {
	mu      sync.Mutex
	counts  map[string]memoryCount
	nextGC  int64
	maxKeys int
}

type memoryCount struct {
	n         int64
	expiresAt int64 // unix ms
}

var memLimiter = &memoryWindows{counts: map[string]memoryCount{}, maxKeys: 200_000}

func (m *memoryWindows) incr(key string, nowMs, expiresAt int64) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if nowMs >= m.nextGC || len(m.counts) >= m.maxKeys {
		for k, c := range m.counts {
			if c.expiresAt <= nowMs {
				delete(m.counts, k)
			}
		}
		m.nextGC = nowMs + 10_000
	}
	c := m.counts[key]
	if c.expiresAt <= nowMs {
		c = memoryCount{expiresAt: expiresAt}
	}
	c.n++
	m.counts[key] = c
	return c.n
}

func (m *memoryWindows) reset() {
	m.mu.Lock()
	m.counts = map[string]memoryCount{}
	m.nextGC = 0
	m.mu.Unlock()
}

// Degraded-mode state, exported through RateLimiterStatus.
var (
	limiterDegraded      atomic.Bool
	limiterSkipUntil     atomic.Int64 // unix ms; Redis is not tried before this
	limiterDegradedSince atomic.Int64
	limiterFallbackHits  atomic.Int64
	limiterLastLog       atomic.Int64
)

// RateLimiterState reports whether the limiter is running on its in-memory
// fallback. Exposed for /ready and monitoring.
type RateLimiterState struct {
	Degraded bool `json:"degraded"`
	// FallbackRequests counts requests limited in memory since the process
	// started.
	FallbackRequests int64 `json:"fallbackRequests"`
	// DegradedSince is unix ms, 0 when not degraded.
	DegradedSince int64 `json:"degradedSince"`
}

func RateLimiterStatus() RateLimiterState {
	s := RateLimiterState{Degraded: limiterDegraded.Load(), FallbackRequests: limiterFallbackHits.Load()}
	if s.Degraded {
		s.DegradedSince = limiterDegradedSince.Load()
	}
	return s
}

// countRequest returns the request's count in its window, from Redis when it
// is healthy, otherwise from the in-memory fallback. It never errors.
func countRequest(ctx context.Context, bucket, key string, window time.Duration, windowEnd int64) int64 {
	nowMs := time.Now().UnixMilli()
	if nowMs >= limiterSkipUntil.Load() {
		n, err := rateCounter(ctx, key, window)
		if err == nil {
			if limiterDegraded.CompareAndSwap(true, false) {
				logx.Info("rate limiter recovered - Redis is back, limits are shared across instances again",
					"fallbackRequests", limiterFallbackHits.Load())
			}
			return n
		}
		limiterSkipUntil.Store(nowMs + redisLimiterBackoff.Milliseconds())
		if limiterDegraded.CompareAndSwap(false, true) {
			limiterDegradedSince.Store(nowMs)
			limiterLastLog.Store(nowMs)
			logx.Error("rate limiter DEGRADED - Redis unavailable, enforcing per-instance in-memory limits",
				"bucket", bucket, "err", err.Error())
		}
	}
	hits := limiterFallbackHits.Add(1)
	if last := limiterLastLog.Load(); nowMs-last >= degradedLogEvery.Milliseconds() && limiterLastLog.CompareAndSwap(last, nowMs) {
		logx.Error("rate limiter still degraded - per-instance in-memory limits in force",
			"fallbackRequests", hits, "degradedForSeconds", strconv.FormatInt((nowMs-limiterDegradedSince.Load())/1000, 10))
	}
	return memLimiter.incr(key, nowMs, windowEnd)
}
