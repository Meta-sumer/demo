package exposure

import (
	"context"
	"errors"
	"sync"
	"time"

	"binarytrading/internal/platform/leader"
)

// Exposure reconcile worker (leader-only). Every SweepInterval it resolves
// lapsed reservations and rebuilds a book that is not READY (safe mode ends
// as soon as the book is exact again); every ReconcileEvery sweeps it runs a
// full Reconcile against MongoDB.
var (
	SweepInterval  = 2 * time.Second
	ReconcileEvery = 15

	loopMu    sync.Mutex
	loop      *leader.Loop
	tickCount int
)

func reconcileTick(ctx context.Context) error {
	tickCount++
	if tickCount%ReconcileEvery == 0 {
		_, err := Reconcile(ctx)
		return err
	}
	if _, err := SweepReservations(ctx); err != nil {
		return err
	}
	st, err := GetBookStatus(ctx)
	if err != nil {
		return err
	}
	if st.Status != BookReady {
		// Wait 0: if another instance holds the rebuild lock it is on it.
		if _, err := RebuildBook(ctx, RebuildOptions{Reason: "worker: book " + st.Status}); err != nil &&
			!errors.Is(err, ErrBookUnavailable) {
			return err
		}
	}
	return nil
}

// StartReconciler starts the leader loop.
func StartReconciler() {
	loopMu.Lock()
	defer loopMu.Unlock()
	if loop == nil {
		loop = leader.New(leader.Options{
			Name:     "exposure-reconcile",
			Interval: SweepInterval,
			// A full pass on a large book can take a while; leadership must
			// outlive it.
			LockTTL: 60 * time.Second,
			OnTick:  reconcileTick,
		})
	}
	loop.Start()
}

// StopReconciler stops the loop and releases leadership.
func StopReconciler(ctx context.Context) {
	loopMu.Lock()
	l := loop
	loopMu.Unlock()
	if l != nil {
		l.Stop()
	}
}
