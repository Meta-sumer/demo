package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func marshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestViewsNullsAndSlices(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 4, 5, 0, time.UTC)
	dob := time.Date(1990, 1, 2, 0, 0, 0, 0, time.UTC)

	u := ToUserView(&User{ID: bson.NewObjectID(), Role: "USER", DateOfBirth: &dob, CreatedAt: now})
	s := marshal(t, u)
	for _, want := range []string{`"fullName":null`, `"dateOfBirth":"1990-01-02"`, `"lastLoginAt":null`, `"createdAt":"2026-09-28T10:04:05.000Z"`} {
		if !strings.Contains(s, want) {
			t.Errorf("user view missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, `"permissions":null`) {
		t.Errorf("permissions must be an array: %s", s)
	}

	a := marshal(t, ToAssetView(&Asset{ID: bson.NewObjectID(), Active: true}, now.UnixMilli()))
	for _, want := range []string{`"supportedExpiries":[]`, `"tradingWindows":[]`, `"hedgeThresholdPercent":null`, `"marketOpen":true`, `"nextOpenAt":null`} {
		if !strings.Contains(a, want) {
			t.Errorf("asset view missing %s: %s", want, a)
		}
	}

	tr := marshal(t, ToTradeView(&BinaryTrade{Stake: 100, Profit: 82, CreatedAt: now}))
	for _, want := range []string{`"potentialReturn":182`, `"result":null`, `"settledAt":null`} {
		if !strings.Contains(tr, want) {
			t.Errorf("trade view missing %s: %s", want, tr)
		}
	}

	c := marshal(t, ToConversationView(&SupportConversation{CreatedAt: now}, nil))
	if strings.Contains(c, `"user"`) || !strings.Contains(c, `"assignedTo":null`) {
		t.Errorf("conversation view: %s", c)
	}

	n := marshal(t, ToNotificationView(&Notification{CreatedAt: now}))
	if !strings.Contains(n, `"data":{}`) {
		t.Errorf("notification view: %s", n)
	}

	h := ToHedgeView(&HedgeOrder{RequestedNotional: 100, FilledNotional: 150})
	if h.RemainingNotional != 0 {
		t.Errorf("remainingNotional = %d", h.RemainingNotional)
	}

	r := marshal(t, ToReconciliationRunLean(&ReconciliationRun{}))
	if !strings.Contains(r, `"checks":[]`) || !strings.Contains(r, `"alerts":[]`) {
		t.Errorf("reconciliation lean: %s", r)
	}
}

func TestAssetViewNextOpenAt(t *testing.T) {
	// Monday 2026-09-28 10:04:05 UTC; the market trades weekdays 12:00-13:00.
	now := time.Date(2026, 9, 28, 10, 4, 5, 0, time.UTC).UnixMilli()
	noon := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).UnixMilli()
	windows := []TradingWindow{{Days: []int{1, 2, 3, 4, 5}, StartMinuteUtc: 720, EndMinuteUtc: 780}}

	closed := ToAssetView(&Asset{Active: true, TradingWindows: windows}, now)
	if closed.MarketOpen || closed.NextOpenAt == nil || *closed.NextOpenAt != noon {
		t.Fatalf("closed market: open=%v nextOpenAt=%v", closed.MarketOpen, closed.NextOpenAt)
	}
	if s := marshal(t, closed); !strings.Contains(s, `"nextOpenAt":1790596800000`) {
		t.Fatalf("nextOpenAt must be epoch ms: %s", s)
	}
	if open := ToAssetView(&Asset{Active: true, TradingWindows: windows}, noon); !open.MarketOpen || open.NextOpenAt != nil {
		t.Fatalf("open market: open=%v nextOpenAt=%v", open.MarketOpen, open.NextOpenAt)
	}
	if inactive := ToAssetView(&Asset{Active: false, TradingWindows: windows}, now); inactive.NextOpenAt != nil {
		t.Fatalf("inactive asset: nextOpenAt=%v", *inactive.NextOpenAt)
	}
	never := []TradingWindow{{Days: []int{}, StartMinuteUtc: 720, EndMinuteUtc: 780}}
	if v := ToAssetView(&Asset{Active: true, TradingWindows: never}, now); v.MarketOpen || v.NextOpenAt != nil {
		t.Fatalf("never opens: open=%v nextOpenAt=%v", v.MarketOpen, v.NextOpenAt)
	}
}
