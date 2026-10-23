package marketdata

import (
	"context"
	"testing"
	"time"

	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/util"
)

// TestFeedLeaseGatesIngestion: ticks are written only while this process
// leads the feed on a lease it renewed and that has not run out, so a former
// leader stops writing before anyone else can take the lock; and the lease is
// short enough that a crashed leader is replaced within a few seconds.
func TestFeedLeaseGatesIngestion(t *testing.T) {
	prevLeader, prevLease := feedLeader.Load(), feedLeaseUntil.Load()
	t.Cleanup(func() { feedLeader.Store(prevLeader); feedLeaseUntil.Store(prevLease) })

	now := time.Now()
	ctx := leader.WithFence(context.Background(), &leader.Fence{Name: "market-feed", LeaseUntil: now.Add(time.Second)})
	feedLeader.Store(true)
	noteFeedLease(ctx)
	if feedLeaseUntil.Load() != now.Add(time.Second).UnixMilli() || !feedLeaseValid() {
		t.Fatalf("a renewed lease must allow ingestion (until %d, now %d)", feedLeaseUntil.Load(), util.NowMs())
	}
	feedLeaseUntil.Store(util.NowMs() - 1)
	if feedLeaseValid() {
		t.Fatal("an expired lease allowed ingestion")
	}
	feedLeaseUntil.Store(util.NowMs() + 1000)
	feedLeader.Store(false)
	if feedLeaseValid() {
		t.Fatal("a process that does not lead the feed allowed ingestion")
	}
	noteFeedLease(context.Background()) // outside a loop: unchanged
	if feedLeaseUntil.Load() == 0 {
		t.Fatal("a tick without a fence cleared the lease")
	}

	// A crashed leader's lock lapses after the TTL; followers retry every
	// interval: the feed is silent for about TTL + interval at most.
	if feedLeaderTTL+feedLeaderInterval > 3500*time.Millisecond || feedLeaderInterval*3 > feedLeaderTTL {
		t.Fatalf("feed lease %v / interval %v: takeover too slow or renewal too sparse", feedLeaderTTL, feedLeaderInterval)
	}
}
