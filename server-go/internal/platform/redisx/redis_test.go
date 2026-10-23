package redisx_test

import (
	"context"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"binarytrading/internal/platform/redisx"
)

// The retry policy (audit: MaxRetries = -1 disabled retries in go-redis v9,
// the opposite of its comment). The first test is pure; the others put a
// proxy that drops a connection mid-command between the client and the test
// Redis (REDIS_URL), and skip when it is not reachable.

func TestClientOptionsEnableRetries(t *testing.T) {
	o, err := redisx.ClientOptions("redis://127.0.0.1:6379/0")
	if err != nil {
		t.Fatal(err)
	}
	if o.MaxRetries <= 0 {
		t.Fatalf("MaxRetries = %d: go-redis v9 treats -1 as no retry and 0 as its default; want an explicit positive budget", o.MaxRetries)
	}
	if o.MaxRetries != redisx.MaxRetries || o.MinRetryBackoff != redisx.MinRetryBackoff || o.MaxRetryBackoff != redisx.MaxRetryBackoff {
		t.Fatalf("options %d %v %v", o.MaxRetries, o.MinRetryBackoff, o.MaxRetryBackoff)
	}
	if o.PoolSize != redisx.PoolSize {
		t.Fatalf("pool %d", o.PoolSize)
	}
	// Worst case added latency stays bounded (3 retries, capped backoff).
	if worst := time.Duration(o.MaxRetries) * o.MaxRetryBackoff; worst > 5*time.Second {
		t.Fatalf("retry budget too long: %v", worst)
	}
}

// dropProxy forwards TCP to Redis; when armed it closes the next connection
// that sends a command, after the command was written by the client and
// before Redis sees it.
type dropProxy struct {
	ln      net.Listener
	target  string
	armed   atomic.Bool
	dropped atomic.Int32
}

func newDropProxy(t *testing.T, target string) *dropProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &dropProxy{ln: ln, target: target}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return p
}

func (p *dropProxy) handle(c net.Conn) {
	up, err := net.DialTimeout("tcp", p.target, 2*time.Second)
	if err != nil {
		_ = c.Close()
		return
	}
	go func() {
		_, _ = io.Copy(c, up)
		_ = c.Close()
	}()
	buf := make([]byte, 32<<10)
	for {
		n, err := c.Read(buf)
		if n > 0 {
			if p.armed.CompareAndSwap(true, false) {
				p.dropped.Add(1)
				break
			}
			if _, werr := up.Write(buf[:n]); werr != nil {
				break
			}
		}
		if err != nil {
			break
		}
	}
	_ = up.Close()
	_ = c.Close()
}

func proxiedClient(t *testing.T, mutate func(*redis.Options)) (*redis.Client, *dropProxy) {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set")
	}
	o, err := redisx.ClientOptions(url)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", o.Addr, time.Second)
	if err != nil {
		t.Skipf("Redis not reachable: %v", err)
	}
	_ = conn.Close()
	p := newDropProxy(t, o.Addr)
	o.Addr = p.ln.Addr().String()
	o.PoolSize = 1
	if mutate != nil {
		mutate(o)
	}
	c := redis.NewClient(o)
	t.Cleanup(func() { _ = c.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.Ping(ctx).Err(); err != nil {
		t.Fatalf("warm-up ping: %v", err)
	}
	return c, p
}

func TestDroppedConnectionIsRetried(t *testing.T) {
	c, p := proxiedClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	key := "test:redisx:retry:" + uuid.NewString()
	t.Cleanup(func() { _ = c.Del(context.Background(), key).Err() })

	p.armed.Store(true)
	if err := c.Set(ctx, key, "v", time.Minute).Err(); err != nil {
		t.Fatalf("a dropped connection failed the command instead of being retried: %v", err)
	}
	if p.dropped.Load() != 1 {
		t.Fatalf("the proxy did not drop a connection (%d)", p.dropped.Load())
	}
	if v, err := c.Get(ctx, key).Result(); err != nil || v != "v" {
		t.Fatalf("value after retry: %q %v", v, err)
	}
}

// The control: the setting the audit found (-1) gives up on the first drop.
func TestDroppedConnectionFailsWithRetriesDisabled(t *testing.T) {
	c, p := proxiedClient(t, func(o *redis.Options) { o.MaxRetries = -1 })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p.armed.Store(true)
	if err := c.Ping(ctx).Err(); err == nil {
		t.Fatal("expected the command to fail without retries")
	}
	if p.dropped.Load() != 1 {
		t.Fatalf("the proxy did not drop a connection (%d)", p.dropped.Load())
	}
}
