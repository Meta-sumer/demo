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

// Connect opens the shared pool. go-redis multiplexes a pool, so unlike
// ioredis there is no separate publisher connection; subscriptions and
// blocking commands take their own connection from the pool automatically.
func Connect(ctx context.Context, url string) error {
	o, err := redis.ParseURL(url)
	if err != nil {
		return err
	}
	o.MaxRetries = -1 // never fail a command because of a retry budget
	o.MinRetryBackoff = 200 * time.Millisecond
	o.MaxRetryBackoff = 5 * time.Second
	o.PoolSize = 64
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
// (BRPOP), so it cannot starve the shared pool.
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
