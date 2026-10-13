// Package leader runs a periodic task on exactly one server instance.
//
// Leadership is a renewable Redis lock: whoever holds it does the work, and if
// that process dies the lock lapses and another instance takes over within one
// TTL. Ticks never overlap - the next is scheduled after the previous finishes.
package leader

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
)

type Options struct {
	Name     string
	Interval time.Duration
	// LockTTL must comfortably exceed the slowest expected tick. Default:
	// max(5s, 5 x Interval).
	LockTTL   time.Duration
	OnTick    func(ctx context.Context) error
	OnAcquire func(ctx context.Context) error
	OnRelease func(ctx context.Context) error
}

type Loop struct {
	opts     Options
	mu       sync.Mutex
	handle   *redisx.Lock
	isLeader atomic.Bool
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(opts Options) *Loop { return &Loop{opts: opts} }

func (l *Loop) IsLeader() bool { return l.isLeader.Load() }

func (l *Loop) Start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.done = make(chan struct{})
	go l.run(ctx)
	logx.Info("worker started", "worker", l.opts.Name)
}

func (l *Loop) Stop() {
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.cancel = nil
	l.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
	l.surrender()
	logx.Info("worker stopped", "worker", l.opts.Name)
}

func (l *Loop) ttl() time.Duration {
	if l.opts.LockTTL > 0 {
		return l.opts.LockTTL
	}
	t := l.opts.Interval * 5
	if t < 5*time.Second {
		t = 5 * time.Second
	}
	return t
}

func (l *Loop) run(ctx context.Context) {
	defer close(l.done)
	delay := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		l.tick(ctx)
		delay = l.opts.Interval
	}
}

func (l *Loop) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logx.Error("worker tick panicked", "worker", l.opts.Name, "panic", r)
		}
	}()
	ttl := l.ttl()

	if l.handle != nil {
		renewed, err := redisx.Renew(ctx, l.handle, ttl)
		if err != nil {
			logx.Error("worker tick failed", "worker", l.opts.Name, "err", err.Error())
			return
		}
		if !renewed {
			logx.Warn("lost worker leadership", "worker", l.opts.Name)
			l.handle = nil
			l.isLeader.Store(false)
			if l.opts.OnRelease != nil {
				_ = l.opts.OnRelease(ctx)
			}
		}
	}

	if l.handle == nil {
		h, err := redisx.Acquire(ctx, "worker:"+l.opts.Name, ttl)
		if err != nil {
			logx.Error("worker tick failed", "worker", l.opts.Name, "err", err.Error())
			return
		}
		if h != nil {
			l.handle = h
			l.isLeader.Store(true)
			logx.Info("acquired worker leadership", "worker", l.opts.Name)
			if l.opts.OnAcquire != nil {
				if err := l.opts.OnAcquire(ctx); err != nil {
					logx.Error("worker acquire hook failed", "worker", l.opts.Name, "err", err.Error())
				}
			}
		}
	}

	if l.handle != nil && l.opts.OnTick != nil {
		if err := l.opts.OnTick(ctx); err != nil && ctx.Err() == nil {
			logx.Error("worker tick failed", "worker", l.opts.Name, "err", err.Error())
		}
	}
}

func (l *Loop) surrender() {
	if l.handle == nil {
		return
	}
	h := l.handle
	l.handle = nil
	l.isLeader.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if l.opts.OnRelease != nil {
		_ = l.opts.OnRelease(ctx)
	}
	_, _ = redisx.Release(ctx, h)
}
