package redisx

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
)

// Distributed locks make wallet mutation, settlement and worker leadership
// safe across instances. Release is compare-and-delete, so a lock whose TTL
// lapsed is never deleted by its previous owner.

var releaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
else
  return 0
end`)

var renewScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
else
  return 0
end`)

type Lock struct {
	Key   string
	Token string
}

// Acquire tries once; nil means someone else holds it.
func Acquire(ctx context.Context, name string, ttl time.Duration) (*Lock, error) {
	key := config.RedisKeys.Lock(name)
	token := uuid.NewString()
	ok, err := client.SetNX(ctx, key, token, ttl).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &Lock{Key: key, Token: token}, nil
}

func Release(ctx context.Context, l *Lock) (bool, error) {
	n, err := releaseScript.Run(ctx, client, []string{l.Key}, l.Token).Int()
	return n == 1, err
}

func Renew(ctx context.Context, l *Lock, ttl time.Duration) (bool, error) {
	n, err := renewScript.Run(ctx, client, []string{l.Key}, l.Token, ttl.Milliseconds()).Int()
	return n == 1, err
}

// AcquireWithRetry waits up to timeout (default 5s), polling with jitter so
// queued waiters do not retry in lockstep.
func AcquireWithRetry(ctx context.Context, name string, ttl, timeout, poll time.Duration) (*Lock, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if poll <= 0 {
		poll = 15 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		l, err := Acquire(ctx, name, ttl)
		if err != nil {
			return nil, err
		}
		if l != nil {
			return l, nil
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		wait := poll + time.Duration(rand.Int64N(int64(poll)))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// ErrLockTimeout is returned by WithLock when the lock stays busy.
type ErrLockTimeout struct{ Name string }

func (e *ErrLockTimeout) Error() string { return "Could not acquire lock: " + e.Name }

func IsLockTimeout(err error) bool {
	var e *ErrLockTimeout
	return errors.As(err, &e)
}

// WithLock runs fn while holding name. timeout 0 means 5s.
func WithLock(ctx context.Context, name string, ttl, timeout time.Duration, fn func() error) error {
	l, err := AcquireWithRetry(ctx, name, ttl, timeout, 0)
	if err != nil {
		return err
	}
	if l == nil {
		return &ErrLockTimeout{Name: name}
	}
	defer func() {
		if _, err := Release(context.Background(), l); err != nil {
			logx.Warn("failed to release lock", "lock", name, "err", err.Error())
		}
	}()
	return fn()
}
