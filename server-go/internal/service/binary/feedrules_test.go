package binary

import (
	"context"
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/pricing"
)

type offsetClock int64

func (o offsetClock) ServerTime(context.Context) (int64, error) { return util.NowMs() + int64(o), nil }

// A feed data rule block (here: the server clock drifting from the
// provider's) stops quotes and trades although the last tick is fresh, and
// lifts when the clocks agree again.
func TestFeedRuleBlockRefusesQuotesAndTrades(t *testing.T) {
	e := newPricingEnv(t)
	marketdata.ResetFeedQualityState()
	t.Cleanup(marketdata.ResetFeedQualityState)
	cfg := e.cfg()

	if s := marketdata.CheckClock(e.ctx, offsetClock(8000), "test", cfg); s.State != marketdata.ClockDrift {
		t.Fatalf("clock %+v", s)
	}
	e.moveTo(100)
	pricing.InvalidateBoard()
	q := e.quote(models.AccountLive)
	if q.Up.Available || q.Up.Code == nil || *q.Up.Code != pricing.CodeStaleMarketData {
		t.Fatalf("quote during drift %+v", q.Up)
	}
	_, err := e.create(e.input(models.DirectionUp))
	if !apperr.HasCode(err, apperr.FeedUnhealthy) {
		t.Fatalf("trade during drift: %v", err)
	}

	if s := marketdata.CheckClock(e.ctx, offsetClock(0), "test", cfg); s.State != marketdata.ClockOK {
		t.Fatalf("clock %+v", s)
	}
	e.moveTo(100)
	pricing.InvalidateBoard()
	if q = e.quote(models.AccountLive); !q.Up.Available {
		t.Fatalf("quote after agreement %+v", q.Up)
	}
	if _, err := e.create(e.input(models.DirectionUp)); err != nil {
		t.Fatalf("trade after agreement: %v", err)
	}
}
