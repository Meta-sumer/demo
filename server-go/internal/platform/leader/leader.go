// Package leader runs a periodic task on exactly one server instance.
//
// Leadership is a renewable Redis lock: whoever holds it does the work, and if
// that process dies the lock lapses and another instance takes over within one
// TTL. Ticks never overlap - the next is scheduled after the previous finishes.
//
// Fencing. A lock alone cannot stop a paused or partitioned former leader from
// acting after its lock lapsed and a new leader took over. Every tick therefore
// runs under a Fence (FenceFromContext): the lock key, the token this instance
// holds, a per-acquisition epoch and the local lease deadline.
//
//   - Side effects in Redis should be made by a script that first checks
//     GET(fence.Key) == fence.Token, so the check and the write are one atomic
//     step (see service/expiry). That is a true fence: a stale leader's write
//     is refused even if it was paused for an arbitrary time.
//   - Other side effects should call fence.Check (local lease + Redis token)
//     right before acting, or carry fence.Epoch into a conditional write.
//   - The local lease (last successful renewal + TTL - margin) is what makes a
//     leader that cannot reach Redis stop by itself: a renewal error past the
//     lease ends leadership locally, and with Options.BoundTickToLease the
//     tick's context is cancelled at the lease deadline.
//
// Loops that ignore the fence behave exactly as before.
package leader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"binarytrading/internal/config"
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
	// BoundTickToLease cancels OnTick's context when the lease this instance
	// holds could have lapsed, so a tick that outlives its leadership stops
	// instead of acting as a second leader. Off by default, because cancelling
	// some loops mid-tick (an order being placed, a provider connection) would
	// do more harm than the overlap it prevents.
	BoundTickToLease bool
}

// ErrLeadershipLost is returned by Fence.Check, and by fenced side effects,
// when this instance no longer holds the lock it started the tick with.
var ErrLeadershipLost = errors.New("worker leadership lost")

// Fence is the leadership a tick runs under.
type Fence struct {
	Name string
	// Redis lock key and the token this instance holds in it.
	Key   string
	Token string
	// Increases by one on every acquisition of this lock by any instance, so a
	// write stamped with an epoch can be refused once a newer leader exists.
	Epoch int64
	// Local estimate of the moment the lock lapses without another renewal.
	LeaseUntil time.Time
}

// LeaseValid is the local half of the fence: no Redis round trip.
func (f *Fence) LeaseValid() bool { return f != nil && time.Now().Before(f.LeaseUntil) }

// Check verifies the lease locally and the token in Redis.
func (f *Fence) Check(ctx context.Context) error {
	if !f.LeaseValid() {
		return ErrLeadershipLost
	}
	v, err := redisx.R().Get(ctx, f.Key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return ErrLeadershipLost
		}
		return err
	}
	if v != f.Token {
		return ErrLeadershipLost
	}
	return nil
}

type fenceKey struct{}

// WithFence attaches a fence to ctx (the loop does this for every tick).
func WithFence(ctx context.Context, f *Fence) context.Context {
	return context.WithValue(ctx, fenceKey{}, f)
}

// FenceFromContext returns the fence a tick runs under, or nil outside a loop.
func FenceFromContext(ctx context.Context) *Fence {
	f, _ := ctx.Value(fenceKey{}).(*Fence)
	return f
}

type Loop struct {
	opts     Options
	mu       sync.Mutex
	handle   *redisx.Lock
	epoch    int64
	lease    time.Time
	fence    atomic.Pointer[Fence]
	isLeader atomic.Bool
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(opts Options) *Loop { return &Loop{opts: opts} }

func (l *Loop) IsLeader() bool { return l.isLeader.Load() }

// Fence returns the current fence, or nil when this instance is not leader.
func (l *Loop) Fence() *Fence { return l.fence.Load() }

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

// leaseMargin absorbs clock drift between this process and Redis.
func (l *Loop) leaseMargin() time.Duration {
	m := l.ttl() / 10
	if m > time.Second {
		m = time.Second
	}
	return m
}

func (l *Loop) lockName() string { return "worker:" + l.opts.Name }

func (l *Loop) epochKey() string { return config.RedisKeys.Lock(l.lockName() + ":epoch") }

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

// loseLeadership drops leadership locally (the lock is no longer ours, or may
// have lapsed).
func (l *Loop) loseLeadership(ctx context.Context, why string) {
	logx.Warn("lost worker leadership", "worker", l.opts.Name, "reason", why)
	l.handle = nil
	l.fence.Store(nil)
	l.isLeader.Store(false)
	if l.opts.OnRelease != nil {
		_ = l.opts.OnRelease(ctx)
	}
}

func (l *Loop) publishFence() *Fence {
	f := &Fence{Name: l.opts.Name, Key: l.handle.Key, Token: l.handle.Token, Epoch: l.epoch, LeaseUntil: l.lease}
	l.fence.Store(f)
	return f
}

func (l *Loop) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			logx.Error("worker tick panicked", "worker", l.opts.Name, "panic", r)
		}
	}()
	ttl := l.ttl()

	if l.handle != nil {
		started := time.Now()
		renewed, err := redisx.Renew(ctx, l.handle, ttl)
		if err != nil {
			// Unknown whether the renewal landed. Past the lease the lock may
			// already belong to someone else, so stop acting as leader.
			if !time.Now().Before(l.lease) {
				l.loseLeadership(ctx, "lease expired while redis was unreachable")
			}
			logx.Error("worker tick failed", "worker", l.opts.Name, "err", err.Error())
			return
		}
		if !renewed {
			l.loseLeadership(ctx, "lock no longer held")
		} else {
			l.lease = started.Add(ttl - l.leaseMargin())
			l.publishFence()
		}
	}

	if l.handle == nil {
		started := time.Now()
		h, err := redisx.Acquire(ctx, l.lockName(), ttl)
		if err != nil {
			logx.Error("worker tick failed", "worker", l.opts.Name, "err", err.Error())
			return
		}
		if h != nil {
			epoch, err := redisx.R().Incr(ctx, l.epochKey()).Result()
			if err != nil {
				// Without an epoch the fence is incomplete: give the lock back
				// and try again next tick.
				logx.Error("worker epoch increment failed", "worker", l.opts.Name, "err", err.Error())
				_, _ = redisx.Release(context.WithoutCancel(ctx), h)
				return
			}
			l.handle, l.epoch = h, epoch
			l.lease = started.Add(ttl - l.leaseMargin())
			l.publishFence()
			l.isLeader.Store(true)
			logx.Info("acquired worker leadership", "worker", l.opts.Name, "epoch", epoch)
			if l.opts.OnAcquire != nil {
				if err := l.opts.OnAcquire(WithFence(ctx, l.fence.Load())); err != nil {
					logx.Error("worker acquire hook failed", "worker", l.opts.Name, "err", err.Error())
				}
			}
		}
	}

	if l.handle != nil && l.opts.OnTick != nil {
		f := l.fence.Load()
		tctx := WithFence(ctx, f)
		if l.opts.BoundTickToLease {
			var cancel context.CancelFunc
			tctx, cancel = context.WithDeadline(tctx, f.LeaseUntil)
			defer cancel()
		}
		if err := l.opts.OnTick(tctx); err != nil && ctx.Err() == nil {
			if errors.Is(err, ErrLeadershipLost) {
				logx.Warn("worker tick stopped: leadership lost", "worker", l.opts.Name)
				return
			}
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
	l.fence.Store(nil)
	l.isLeader.Store(false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if l.opts.OnRelease != nil {
		_ = l.opts.OnRelease(ctx)
	}
	_, _ = redisx.Release(ctx, h)
}
