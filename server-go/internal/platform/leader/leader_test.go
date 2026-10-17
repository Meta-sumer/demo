package leader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/testsupport"
)

// Leader election and fencing (F-14). Needs Redis (via LiveStores); every
// lock name is unique to the test and deleted afterwards.

func testLoopName(t *testing.T) string {
	t.Helper()
	testsupport.LiveStores(t)
	name := "test-leader-" + bson.NewObjectID().Hex()
	t.Cleanup(func() {
		_ = redisx.R().Del(context.Background(),
			config.RedisKeys.Lock("worker:"+name), config.RedisKeys.Lock("worker:"+name+":epoch")).Err()
	})
	return name
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTickRunsUnderAValidFence(t *testing.T) {
	name := testLoopName(t)
	var seen atomic.Pointer[Fence]
	l := New(Options{Name: name, Interval: 20 * time.Millisecond, LockTTL: 2 * time.Second,
		OnTick: func(ctx context.Context) error {
			f := FenceFromContext(ctx)
			if f == nil {
				return errors.New("no fence")
			}
			if err := f.Check(ctx); err != nil {
				return err
			}
			seen.Store(f)
			return nil
		}})
	l.Start()
	defer l.Stop()
	waitFor(t, "a fenced tick", func() bool { return seen.Load() != nil })
	f := seen.Load()
	if f.Epoch < 1 || f.Token == "" || f.Key != config.RedisKeys.Lock("worker:"+name) || !f.LeaseValid() {
		t.Fatalf("fence %+v", f)
	}
	if l.Fence() == nil || !l.IsLeader() {
		t.Fatal("loop does not report leadership")
	}
}

func TestEpochIncreasesWithEveryAcquisition(t *testing.T) {
	name := testLoopName(t)
	epochs := []int64{}
	for i := 0; i < 2; i++ {
		var got atomic.Int64
		l := New(Options{Name: name, Interval: 20 * time.Millisecond, LockTTL: 2 * time.Second,
			OnTick: func(ctx context.Context) error { got.Store(FenceFromContext(ctx).Epoch); return nil }})
		l.Start()
		waitFor(t, "leadership", func() bool { return got.Load() > 0 })
		l.Stop() // releases the lock
		epochs = append(epochs, got.Load())
	}
	if epochs[1] <= epochs[0] {
		t.Fatalf("epochs %v", epochs)
	}
}

// Leader loss mid-tick: while a tick is in progress the lock lapses and
// another instance takes it. The old tick's fence check fails, and the next
// tick notices and gives up leadership.
func TestLeaderLossMidTickIsFenced(t *testing.T) {
	name := testLoopName(t)
	release := make(chan struct{})
	checked := make(chan error, 1)
	var released atomic.Bool
	var ticks atomic.Int64
	l := New(Options{Name: name, Interval: 20 * time.Millisecond, LockTTL: 2 * time.Second,
		OnRelease: func(context.Context) error { released.Store(true); return nil },
		OnTick: func(ctx context.Context) error {
			if ticks.Add(1) == 1 {
				<-release
				checked <- FenceFromContext(ctx).Check(ctx)
			}
			return nil
		}})
	l.Start()
	defer l.Stop()
	waitFor(t, "first tick", func() bool { return ticks.Load() == 1 })

	// Another instance now holds the lock.
	if err := redisx.R().Set(context.Background(), config.RedisKeys.Lock("worker:"+name), "someone-else", 2*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-checked; !errors.Is(err, ErrLeadershipLost) {
		t.Fatalf("stale leader's fence check: %v", err)
	}
	waitFor(t, "leadership to be dropped", func() bool { return released.Load() && !l.IsLeader() && l.Fence() == nil })
}

// A tick bound to its lease is cancelled when the lease could have lapsed.
func TestBoundTickIsCancelledAtLeaseExpiry(t *testing.T) {
	name := testLoopName(t)
	var once sync.Once
	result := make(chan time.Duration, 1)
	l := New(Options{Name: name, Interval: 20 * time.Millisecond, LockTTL: 600 * time.Millisecond, BoundTickToLease: true,
		OnTick: func(ctx context.Context) error {
			once.Do(func() {
				started := time.Now()
				<-ctx.Done()
				result <- time.Since(started)
			})
			return nil
		}})
	l.Start()
	defer l.Stop()
	select {
	case d := <-result:
		if d > 600*time.Millisecond {
			t.Fatalf("tick outlived its lease: %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("bounded tick was never cancelled")
	}
}

// Without a fence (direct calls, other loops' old behaviour) nothing changes.
func TestNoFenceOutsideALoop(t *testing.T) {
	if FenceFromContext(context.Background()) != nil {
		t.Fatal("fence from nowhere")
	}
	var f *Fence
	if f.LeaseValid() {
		t.Fatal("nil fence is valid")
	}
	stale := &Fence{LeaseUntil: time.Now().Add(-time.Second)}
	if err := stale.Check(context.Background()); !errors.Is(err, ErrLeadershipLost) {
		t.Fatalf("expired lease: %v", err)
	}
}
