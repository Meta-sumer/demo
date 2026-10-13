package hedging

import (
	"testing"

	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/hedging/providers"
)

// Port of tests/unit/hedge.test.ts (hedge sizing).

var minNotional = util.ToMinor(1_000)

func m(v float64) int64 { return util.ToMinor(v) }

func TestBuysWhenBookLeansUp(t *testing.T) {
	// The platform pays out if the price rises, so it hedges by going long.
	a := ComputeHedgeAdjustment(m(10_000), m(5_000), 0, minNotional)
	if a == nil || a.Side != providers.SideBuy || a.Notional != m(5_000) {
		t.Fatalf("got %+v", a)
	}
}

func TestSellsWhenBookLeansDown(t *testing.T) {
	a := ComputeHedgeAdjustment(m(-8_000), m(4_000), 0, minNotional)
	if a == nil || a.Side != providers.SideSell || a.Notional != m(4_000) {
		t.Fatalf("got %+v", a)
	}
}

func TestOnlyTradesDifference(t *testing.T) {
	a := ComputeHedgeAdjustment(m(10_000), m(5_000), m(3_000), minNotional)
	if a == nil || a.Side != providers.SideBuy || a.Notional != m(2_000) {
		t.Fatalf("got %+v", a)
	}
}

func TestUnwindsOvershoot(t *testing.T) {
	a := ComputeHedgeAdjustment(m(10_000), m(2_000), m(6_000), minNotional)
	if a == nil || a.Side != providers.SideSell || a.Notional != m(4_000) {
		t.Fatalf("got %+v", a)
	}
}

func TestWithinTolerance(t *testing.T) {
	if a := ComputeHedgeAdjustment(m(10_000), m(5_000), m(4_500), minNotional); a != nil {
		t.Fatalf("expected nil, got %+v", a)
	}
	if a := ComputeHedgeAdjustment(m(10_000), m(5_000), m(5_000), minNotional); a != nil {
		t.Fatalf("expected nil, got %+v", a)
	}
}

func TestBelowMinimumSize(t *testing.T) {
	if a := ComputeHedgeAdjustment(m(10_000), m(500), 0, minNotional); a != nil {
		t.Fatalf("expected nil, got %+v", a)
	}
}

func TestClosesHedgeWhenExposureDisappears(t *testing.T) {
	a := ComputeHedgeAdjustment(0, 0, m(5_000), minNotional)
	if a == nil || a.Side != providers.SideSell || a.Notional != m(5_000) {
		t.Fatalf("got %+v", a)
	}
}
