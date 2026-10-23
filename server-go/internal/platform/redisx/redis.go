// Package redisx holds hot state: ticks, exposure, queues, locks and pub/sub
// (spec section 27). It is deliberately not the accounting store.
package redisx

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"binarytrading/internal/platform/logx"
)

var (
	client *redis.Client
	opts   *redis.Options
)

// Command retry policy. A transient failure (a dropped or reset connection,
// a Redis restart or LOADING, a pool timeout) is retried on a fresh
// connection instead of failing the command, as the Node server's ioredis
// client did. The budget is bounded so a real outage still surfaces within
// about two seconds and the callers' own fallbacks take over (rate-limit
// memory window, session state from MongoDB, the next worker tick).
//
// In go-redis v9, MaxRetries -1 DISABLES retries (0 means the default of 3),
// which is why an earlier "-1 = never give up" setting left every command
// with no retry at all.
//
// A command whose reply was lost may run twice. Every script and key this
// server writes tolerates that: the exposure book and the expiry queue use
// idempotent per-trade scripts, a lock SET NX that is replayed finds its own
// key and the waiter retries until the TTL, and a doubled rate-limit INCR
// only errs on the strict side.
const (
	MaxRetries      = 3
	MinRetryBackoff = 100 * time.Millisecond
	MaxRetryBackoff = time.Second
	PoolSize        = 64
)

// ClientOptions parses url and applies the server's pool and retry policy.
func ClientOptions(url string) (*redis.Options, error) {
	o, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	o.MaxRetries = MaxRetries
	o.MinRetryBackoff = MinRetryBackoff
	o.MaxRetryBackoff = MaxRetryBackoff
	o.PoolSize = PoolSize
	return o, nil
}

// Connect opens the shared pool. go-redis multiplexes a pool, so unlike
// ioredis there is no separate publisher connection; subscriptions and
// blocking commands take their own connection from the pool automatically.
func Connect(ctx context.Context, url string) error {
	o, err := ClientOptions(url)
	if err != nil {
		return err
	}
	opts = o
	client = redis.NewClient(o)
	if err := client.Ping(ctx).Err(); err != nil {
		return err
	}
	logx.Info("redis connected")
	return nil
}

// R is the shared client.
func R() *redis.Client { return client }

// NewConnection returns a dedicated client for a consumer that blocks
// (BRPOP), so it cannot starve the shared pool. It keeps the retry policy.
func NewConnection() *redis.Client {
	o := *opts
	o.PoolSize = 2
	return redis.NewClient(&o)
}

func Disconnect() error {
	if client == nil {
		return nil
	}
	return client.Close()
}

func Healthy(ctx context.Context) bool {
	if client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	pong, err := client.Ping(ctx).Result()
	return err == nil && pong == "PONG"
}

// IsNil reports a missing key.
func IsNil(err error) bool { return err == redis.Nil }
