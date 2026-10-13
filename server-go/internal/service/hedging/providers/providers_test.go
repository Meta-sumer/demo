package providers

import (
	"context"
	"testing"

	"binarytrading/internal/platform/apperr"
)

func req(key string) OrderRequest {
	return OrderRequest{Reference: "HDG-1", Symbol: "BTCUSD", Side: SideBuy, Notional: 100_000, ReferencePrice: 100, IdempotencyKey: key}
}

func TestPaperFillsAndIsIdempotent(t *testing.T) {
	p := NewPaperProvider(PaperOptions{})
	ctx := context.Background()
	a, _ := p.PlaceOrder(ctx, req("k"))
	if a.Status != StateFilled || a.FilledNotional != 100_000 || a.AverageFillPrice == nil || *a.AverageFillPrice != 100.02 {
		t.Fatalf("got %+v", a)
	}
	b, _ := p.PlaceOrder(ctx, req("k"))
	if b.ProviderOrderID != a.ProviderOrderID {
		t.Fatal("idempotency key not honoured")
	}
	pos, _ := p.GetPosition(ctx, "BTCUSD")
	if pos.NetNotional != 100_000 {
		t.Fatalf("position %+v", pos)
	}
}

func TestPaperPartialAndFailure(t *testing.T) {
	half := 0.5
	p := NewPaperProvider(PaperOptions{PartialFillRatio: &half})
	r, _ := p.PlaceOrder(context.Background(), req("a"))
	if r.Status != StatePartiallyFilled || r.FilledNotional != 50_000 {
		t.Fatalf("got %+v", r)
	}
	f := NewPaperProvider(PaperOptions{FailureRate: 1})
	r, _ = f.PlaceOrder(context.Background(), req("b"))
	if r.Status != StateFailed || r.Error == "" {
		t.Fatalf("got %+v", r)
	}
}

func TestLiveProvidersFailClosed(t *testing.T) {
	for _, p := range []Provider{NewBrokerProvider(), NewLPProvider()} {
		_, err := p.PlaceOrder(context.Background(), req("x"))
		if !apperr.HasCode(err, apperr.LiveExecutionDisabled) {
			t.Fatalf("%s: expected LIVE_EXECUTION_DISABLED, got %v", p.Name(), err)
		}
		if !p.Live() || p.Healthy(context.Background()) {
			t.Fatalf("%s: expected live and unhealthy", p.Name())
		}
	}
}
