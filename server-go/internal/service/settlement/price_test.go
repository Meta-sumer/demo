package settlement

import (
	"testing"

	"binarytrading/internal/models"
)

// Pure tests of the expiry price policy (F-11).

const expiryAt = int64(1_800_000_000_000)

func tk(price float64, ts, receivedAt int64) models.NormalisedTick {
	if receivedAt == 0 {
		receivedAt = ts
	}
	pt := ts
	return models.NormalisedTick{MarketTick: models.MarketTick{Price: price, Timestamp: ts, ProviderTimestamp: &pt, ReceivedAt: receivedAt}}
}

func confirmed(ticks ...models.NormalisedTick) ExpiryCandidates {
	return ExpiryCandidates{Ticks: ticks, PostExpiryTickSeen: true}
}

func TestDecideExpiryPrice_LatestTickAtOrBeforeExpiryRegardlessOfArrivalOrder(t *testing.T) {
	p := DefaultPolicy()
	// Arrival order scrambled; the post-expiry tick is in the input on purpose.
	c := confirmed(tk(101, expiryAt-300, 0), tk(105, expiryAt+50, 0), tk(102, expiryAt-100, 0), tk(100, expiryAt-900, 0))
	d := DecideExpiryPrice(expiryAt, expiryAt+100, c, p)
	if d.Kind != DecisionPriced || d.Tick.Price != 102 || d.Rule != models.SettlementRuleTickAtOrBeforeExpiry {
		t.Fatalf("got %+v", d)
	}
	if d.Candidates != 3 {
		t.Fatalf("candidates %d", d.Candidates)
	}
}

func TestDecideExpiryPrice_TickExactlyAtExpiryQualifies(t *testing.T) {
	d := DecideExpiryPrice(expiryAt, expiryAt+10, confirmed(tk(99, expiryAt, 0), tk(98, expiryAt-1, 0)), DefaultPolicy())
	if d.Kind != DecisionPriced || d.Tick.Price != 99 {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideExpiryPrice_NeverUsesPostExpiryTick(t *testing.T) {
	p := DefaultPolicy()
	p.NoPriceRule = models.NoPriceRuleStaleTickThenVoid
	post := confirmed(tk(110, expiryAt+1, 0), tk(111, expiryAt+5000, 0))
	for _, at := range []int64{expiryAt + 10, expiryAt + p.DeferWindowMs + 1, expiryAt + 3_600_000} {
		d := DecideExpiryPrice(expiryAt, at, post, p)
		if d.Tick != nil {
			t.Fatalf("at %d used a post-expiry tick: %+v", at, d)
		}
	}
	if d := DecideExpiryPrice(expiryAt, expiryAt+p.DeferWindowMs, post, p); d.Kind != DecisionVoid {
		t.Fatalf("after the defer window: %+v", d)
	}
}

func TestDecideExpiryPrice_AwaitsConfirmationWithinWindow(t *testing.T) {
	p := DefaultPolicy()
	c := ExpiryCandidates{Ticks: []models.NormalisedTick{tk(100, expiryAt-50, 0)}}
	d := DecideExpiryPrice(expiryAt, expiryAt+500, c, p)
	if d.Kind != DecisionAwaitConfirmation || d.RetryAt != expiryAt+500+p.RetryDelayMs {
		t.Fatalf("got %+v", d)
	}
	// Retry never later than the end of the confirmation window.
	d = DecideExpiryPrice(expiryAt, expiryAt+1500, c, p)
	if d.RetryAt != expiryAt+p.ConfirmationWindowMs {
		t.Fatalf("retryAt %d", d.RetryAt)
	}
	// Window over: settles on what it has.
	d = DecideExpiryPrice(expiryAt, expiryAt+p.ConfirmationWindowMs, c, p)
	if d.Kind != DecisionPriced || d.Tick.Price != 100 {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideExpiryPrice_StaleTickDefersThenVoids(t *testing.T) {
	p := DefaultPolicy()
	c := confirmed(tk(100, expiryAt-p.MaxTickAgeMs-1, 0))
	d := DecideExpiryPrice(expiryAt, expiryAt+3000, c, p)
	if d.Kind != DecisionDeferNoTick || d.RetryAt != expiryAt+3000+p.RetryDelayMs {
		t.Fatalf("got %+v", d)
	}
	d = DecideExpiryPrice(expiryAt, expiryAt+p.DeferWindowMs, c, p)
	if d.Kind != DecisionVoid || d.Rule != models.SettlementRuleNoPriceVoidRefund || d.Tick != nil {
		t.Fatalf("got %+v", d)
	}
	// A tick exactly MaxTickAgeMs old still qualifies.
	d = DecideExpiryPrice(expiryAt, expiryAt+3000, confirmed(tk(100, expiryAt-p.MaxTickAgeMs, 0)), p)
	if d.Kind != DecisionPriced {
		t.Fatalf("boundary: %+v", d)
	}
}

func TestDecideExpiryPrice_MissingTicksDeferThenVoid(t *testing.T) {
	p := DefaultPolicy()
	if d := DecideExpiryPrice(expiryAt, expiryAt+p.ConfirmationWindowMs, ExpiryCandidates{}, p); d.Kind != DecisionDeferNoTick {
		t.Fatalf("got %+v", d)
	}
	if d := DecideExpiryPrice(expiryAt, expiryAt+p.DeferWindowMs+1, ExpiryCandidates{}, p); d.Kind != DecisionVoid {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideExpiryPrice_StaleTickRuleUsesOldTickButNeverPostExpiry(t *testing.T) {
	p := DefaultPolicy()
	p.NoPriceRule = models.NoPriceRuleStaleTickThenVoid
	c := confirmed(tk(97, expiryAt-60_000, 0), tk(96, expiryAt-90_000, 0), tk(120, expiryAt+1, 0))
	d := DecideExpiryPrice(expiryAt, expiryAt+p.DeferWindowMs, c, p)
	if d.Kind != DecisionPriced || d.Rule != models.SettlementRuleStaleTickBeforeExpiry || d.Tick.Price != 97 {
		t.Fatalf("got %+v", d)
	}
	// Still deferred inside the window.
	if d := DecideExpiryPrice(expiryAt, expiryAt+5000, c, p); d.Kind != DecisionDeferNoTick {
		t.Fatalf("got %+v", d)
	}
}

func TestDecideExpiryPrice_ClockSkewedTickIgnored(t *testing.T) {
	p := DefaultPolicy()
	// Stamped 2.9s ahead of when the server received it: provider clock ahead.
	skewed := tk(150, expiryAt-100, expiryAt-3000)
	honest := tk(101, expiryAt-400, expiryAt-390)
	d := DecideExpiryPrice(expiryAt, expiryAt+100, confirmed(skewed, honest), p)
	if d.Kind != DecisionPriced || d.Tick.Price != 101 {
		t.Fatalf("got %+v", d)
	}
	// Late delivery (stamped before expiry, received after) is not skew.
	late := tk(103, expiryAt-200, expiryAt+1500)
	d = DecideExpiryPrice(expiryAt, expiryAt+1600, confirmed(late, honest), p)
	if d.Kind != DecisionPriced || d.Tick.Price != 103 {
		t.Fatalf("late tick: %+v", d)
	}
	// Disabled check accepts it.
	p.MaxClockSkewMs = 0
	if d := DecideExpiryPrice(expiryAt, expiryAt+100, confirmed(skewed, honest), p); d.Tick.Price != 150 {
		t.Fatalf("skew check disabled: %+v", d)
	}
}

func TestDecideExpiryPrice_TiesAreDeterministic(t *testing.T) {
	a, b, c := tk(100, expiryAt-10, expiryAt-5), tk(101, expiryAt-10, expiryAt-5), tk(102, expiryAt-10, expiryAt-8)
	s1, s2 := int64(7), int64(9)
	a.Sequence, b.Sequence = &s2, &s1
	for _, order := range [][]models.NormalisedTick{{a, b, c}, {c, b, a}, {b, c, a}} {
		d := DecideExpiryPrice(expiryAt, expiryAt+10, confirmed(order...), DefaultPolicy())
		if d.Tick.Price != 100 {
			t.Fatalf("order %v picked %v", order, d.Tick.Price)
		}
	}
}

func TestDecideExpiryPrice_IgnoresUnusablePrices(t *testing.T) {
	d := DecideExpiryPrice(expiryAt, expiryAt+10, confirmed(tk(0, expiryAt-1, 0), tk(-3, expiryAt-2, 0), tk(99, expiryAt-3, 0)), DefaultPolicy())
	if d.Tick == nil || d.Tick.Price != 99 {
		t.Fatalf("got %+v", d)
	}
}

func TestEqualityGoesThroughDrawPolicy(t *testing.T) {
	ev := &models.SettlementEvidence{Price: f64p(100), DrawPolicy: models.DrawPolicyRefund}
	if r := ReplayExpiryResult(models.DirectionUp, 100, ev); *r != models.TradeResultDraw {
		t.Fatalf("refund: %s", *r)
	}
	ev.DrawPolicy = models.DrawPolicyLoss
	if r := ReplayExpiryResult(models.DirectionDown, 100, ev); *r != models.TradeResultLoss {
		t.Fatalf("loss: %s", *r)
	}
	if ReplayExpiryResult(models.DirectionUp, 100, &models.SettlementEvidence{}) != nil {
		t.Fatal("void evidence must not replay to a result")
	}
}

func TestNormalisePolicyKeepsHardRules(t *testing.T) {
	p := NormalisePolicy(models.SettlementPolicy{EarlyCloseFloorMs: 0, NoPriceRule: "WHATEVER", DeferWindowMs: 10, ConfirmationWindowMs: 5000})
	if p.EarlyCloseFloorMs < EarlyCloseHardFloorMs || p.NoPriceRule != models.NoPriceRuleVoidRefund || p.DeferWindowMs < p.ConfirmationWindowMs {
		t.Fatalf("%+v", p)
	}
	cfg := &models.RiskConfig{MinSecondsRemainingForEarlyClose: 0}
	if EarlyCloseFloorMsFor(cfg, p) != EarlyCloseHardFloorMs {
		t.Fatal("floor below the hard minimum")
	}
	cfg.MinSecondsRemainingForEarlyClose = 10
	if EarlyCloseFloorMsFor(cfg, p) != 10_000 {
		t.Fatal("configured minimum ignored")
	}
}

func TestFinalStatusForEarlyClose(t *testing.T) {
	if finalStatusFor(models.TradeResultEarlyClose) != models.TradeStatusClosedEarly {
		t.Fatal("EARLY_CLOSE must finish as CLOSED_EARLY, never DRAW")
	}
}

func f64p(v float64) *float64 { return &v }
