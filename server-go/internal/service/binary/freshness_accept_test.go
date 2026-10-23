package binary

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/expiry"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/pricing"
)

// The production feed bands: STALE above 300 ms for the monitoring view.
func (e *prEnv) productionBands() {
	e.setRiskConfig(bson.M{"feedHealthyMaxMs": int64(100), "feedDegradedMaxMs": int64(300), "feedTickMaxAgeMs": int64(5000),
		"haltTradingOnStaleFeed": true, "haltTradingOnDegradedFeed": false, "pricingStaleAfterMs": int64(2000)})
}

// lastTick writes the last accepted quote: received ageMs ago, delivered
// latencyMs after the provider stamped it.
func (e *prEnv) lastTick(price float64, ageMs, latencyMs int64) {
	e.t.Helper()
	at := util.NowMs() - ageMs
	s := func(n int64) string { return strconv.FormatInt(n, 10) }
	if err := redisx.R().HSet(e.ctx, config.RedisKeys.LastTick(e.asset.ID), map[string]any{
		"assetId": e.asset.ID, "symbol": e.asset.Symbol, "price": strconv.FormatFloat(price, 'f', -1, 64), "bid": "", "ask": "",
		"timestamp": s(at - latencyMs), "providerTimestamp": s(at - latencyMs), "receivedAt": s(at), "processedAt": s(at),
		"sequence": "", "feedLatencyMs": s(latencyMs), "processingLatencyMs": "0", "totalLatencyMs": s(latencyMs),
		"provider": "test-feed", "spreadBps": "0.5",
	}).Err(); err != nil {
		e.t.Fatal(err)
	}
}

func requoteOf(t *testing.T, err error) pricing.QuoteView {
	t.Helper()
	q, ok := detailsOf(t, err)["quote"].(pricing.QuoteView)
	if !ok {
		t.Fatalf("refusal without a fresh quote: %v", err)
	}
	return q
}

// TestOneFreshnessRuleForQuotesAndAcceptance: quoting and accepting apply
// the same freshness rule (pricing specification 8.1 step 5 and 8.2:
// stale_after 2 s). A price 1 s old - STALE in the 300 ms monitoring band -
// is quoted AND accepted; one older than 2 s is neither; a price delivered
// late beyond the bands is neither while the halt switch asks for it.
func TestOneFreshnessRuleForQuotesAndAcceptance(t *testing.T) {
	e := newPricingEnv(t)
	e.productionBands()

	// 1 s old: offered, and the offer is accepted (it used to be refused
	// FEED_UNHEALTHY by the 300 ms band at acceptance).
	e.lastTick(100, 1000, 5)
	q := e.quote(models.AccountLive)
	if !q.Up.Available {
		t.Fatalf("1 s old price not quoted: %+v", q.Up)
	}
	if _, err := e.create(e.withOffer(models.DirectionUp, q.Up)); err != nil {
		t.Fatalf("offer on a 1 s old price refused at acceptance: %v", err)
	}
	// An app without offers is priced under the same rule.
	e.lastTick(100, 1000, 5)
	if _, err := e.create(e.input(models.DirectionDown)); err != nil {
		t.Fatalf("inline trade on a 1 s old price: %v", err)
	}

	// Older than 2 s: no offer, no trade.
	e.lastTick(100, 2500, 5)
	q = e.quote(models.AccountLive)
	if q.Up.Available || *q.Up.Code != pricing.CodeStaleMarketData {
		t.Fatalf("2.5 s old price quoted: %+v", q.Up)
	}
	_, err := e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.FeedUnhealthy, 503)
	if detailsOf(t, err)["reason"] != "STALE_MARKET_DATA" {
		t.Fatalf("details %v", detailsOf(t, err))
	}

	// Delivered 500 ms late (beyond the 300 ms band, halt switch on): no
	// offer, no trade - both sides read the same rule.
	e.lastTick(100, 0, 500)
	if q = e.quote(models.AccountLive); q.Up.Available {
		t.Fatalf("late price quoted: %+v", q.Up)
	}
	_, err = e.create(e.input(models.DirectionUp))
	wantCode(t, err, apperr.FeedUnhealthy, 503)
	e.setRiskConfig(bson.M{"haltTradingOnStaleFeed": false})
	e.lastTick(100, 0, 500)
	if q = e.quote(models.AccountLive); !q.Up.Available {
		t.Fatalf("switch off, late price not quoted: %+v", q.Up)
	}
	if _, err := e.create(e.withOffer(models.DirectionUp, q.Up)); err != nil {
		t.Fatalf("switch off, late price refused at acceptance: %v", err)
	}
}

// TestPlatformRefusalOfAnOfferCarriesAFreshQuote: an offer refused for a
// reason that is not the customer's - the feed went stale, the symbol or the
// platform was halted - is answered with a fresh quote, so the app shows the
// new state (and a usable offer as soon as there is one) instead of holding
// a used offer.
func TestPlatformRefusalOfAnOfferCarriesAFreshQuote(t *testing.T) {
	e := newPricingEnv(t)
	e.productionBands()

	// The feed goes stale between the quote and the acceptance.
	e.lastTick(100, 0, 5)
	q := e.quote(models.AccountLive)
	e.lastTick(100, 2500, 5)
	_, err := e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.FeedUnhealthy, 503)
	fresh := requoteOf(t, err)
	if fresh.Up.Available || *fresh.Up.Code != pricing.CodeStaleMarketData {
		t.Fatalf("re-quote during the stale feed %+v", fresh.Up)
	}
	// The feed recovers: the next quote trades.
	e.lastTick(100, 0, 5)
	if _, err := e.create(e.withOffer(models.DirectionUp, e.quote(models.AccountLive).Up)); err != nil {
		t.Fatalf("after recovery: %v", err)
	}

	// The symbol is halted while the offer is outstanding.
	q = e.quote(models.AccountLive)
	if _, _, err := pricing.Halt(e.ctx, e.asset.Symbol, "exchange outage", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	_, err = e.create(e.withOffer(models.DirectionUp, q.Up))
	wantCode(t, err, apperr.TradingHalted, 503)
	if fresh = requoteOf(t, err); fresh.Up.Available || *fresh.Up.Code != pricing.CodeSymbolHalted {
		t.Fatalf("re-quote during the symbol halt %+v", fresh.Up)
	}
	if _, _, err := pricing.Resume(e.ctx, e.asset.Symbol, e.userID.Hex()); err != nil {
		t.Fatal(err)
	}

	// The platform is halted (refused before the offer is even looked at).
	q = e.quote(models.AccountLive)
	if _, _, err := pricing.Halt(e.ctx, "", "maintenance", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	_, err = e.create(e.withOffer(models.DirectionDown, q.Down))
	wantCode(t, err, apperr.TradingHalted, 503)
	if fresh = requoteOf(t, err); fresh.Down.Available || *fresh.Down.Code != pricing.CodeTradingHalted {
		t.Fatalf("re-quote during the global halt %+v", fresh.Down)
	}
	if _, _, err := pricing.Resume(e.ctx, "", e.userID.Hex()); err != nil {
		t.Fatal(err)
	}
	// After the resume the halted acceptance's offer is still usable, or
	// the fresh one is: the app is never stuck.
	e.lastTick(100, 0, 5)
	if _, err := e.create(e.withOffer(models.DirectionDown, e.quote(models.AccountLive).Down)); err != nil {
		t.Fatalf("after the resume: %v", err)
	}

	// The customer's own mistakes are not re-quoted this way (an unknown
	// offer already carries one; a stake out of range does not need one).
	in := e.withOffer(models.DirectionUp, e.quote(models.AccountLive).Up)
	in.Amount = 1
	_, err = e.create(in)
	if ae, ok := apperr.As(err); !ok || ae.Code == apperr.FeedUnhealthy || ae.Code == apperr.TradingHalted {
		t.Fatalf("stake below the minimum: %v", err)
	}
}

// TestRedisFailureAfterTheInsertReturnsTheTrade: once the trade is written,
// a Redis failure (exposure commit, expiry queue, idempotency commit) does
// not report a failure: the trade is accepted, a retry with the same key
// returns the same trade, and the repairs that work from MongoDB finish what
// Redis missed - the reservation sweeper counts its exposure, the expiry
// worker's database sweep queues it for settlement once due.
func TestRedisFailureAfterTheInsertReturnsTheTrade(t *testing.T) {
	e := newPricingEnv(t)
	savedCommit, savedQueue, savedIdem := commitReservation, queueExpiry, commitIdempotency
	savedTTL := exposure.ReservationTTL
	t.Cleanup(func() {
		commitReservation, queueExpiry, commitIdempotency = savedCommit, savedQueue, savedIdem
		exposure.ReservationTTL = savedTTL
	})
	down := errors.New("redis: connection reset by peer")
	commitReservation = func(context.Context, *exposure.Reservation) error { return down }
	queueExpiry = func(context.Context, string, int64) error { return down }
	commitIdempotency = func(context.Context, string, string, string, string) error { return down }
	exposure.ReservationTTL = 300 * time.Millisecond

	before := e.tradeCount()
	key := "post-insert-" + e.userID.Hex()
	in := e.withOffer(models.DirectionUp, e.quote(models.AccountLive).Up)
	in.IdempotencyKey = &key
	res, err := e.create(in)
	if err != nil {
		t.Fatalf("a written trade was reported as failed: %v", err)
	}
	if res.Replayed || !slices.Equal(res.Degraded, []string{"exposure_commit", "expiry_queue", "idempotency_commit"}) {
		t.Fatalf("result %+v", res)
	}
	tr := e.stored(res.Trade.ID)
	if tr.Status != models.TradeStatusOpen || e.tradeCount() != before+1 {
		t.Fatalf("trade %s, %d new trades", tr.Status, e.tradeCount()-before)
	}

	// The same key (a retry after the response was lost): the same trade.
	again := e.withOffer(models.DirectionUp, e.quote(models.AccountLive).Up)
	again.IdempotencyKey = &key
	res2, err := e.create(again)
	if err != nil || !res2.Replayed || res2.Trade.ID != res.Trade.ID || e.tradeCount() != before+1 {
		t.Fatalf("retry %+v %v (%d new trades)", res2, err, e.tradeCount()-before)
	}

	// The exposure: the reservation lapses and the sweeper commits it from
	// MongoDB.
	time.Sleep(400 * time.Millisecond)
	sw, err := exposure.SweepReservations(e.ctx)
	if err != nil || !slices.Contains(sw.Committed, res.Trade.ID) {
		t.Fatalf("sweep %+v %v", sw, err)
	}
	if n, _ := redisx.R().HExists(e.ctx, exposure.BookNamespace()+":ot:"+e.userID.Hex(), res.Trade.ID).Result(); !n {
		t.Fatal("the trade's exposure is not counted after the sweep")
	}

	// The expiry: not on the queue; once due, the database sweep queues it
	// for settlement.
	if _, err := redisx.R().ZScore(e.ctx, config.RedisKeys.ExpiryQueue, res.Trade.ID).Result(); !redisx.IsNil(err) {
		t.Fatalf("expected the trade off the expiry queue: %v", err)
	}
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(e.ctx, bson.M{"_id": tr.ID},
		bson.M{"$set": bson.M{"expiryTimestamp": util.NowMs() - 10_000}}); err != nil {
		t.Fatal(err)
	}
	if n, err := expiry.SweepMissedExpiries(e.ctx, util.NowMs(), expiry.DefaultSweepLimit); err != nil || n < 1 {
		t.Fatalf("expiry sweep %d %v", n, err)
	}
	queued, _ := redisx.R().LRange(e.ctx, config.RedisKeys.SettlementQueue, 0, -1).Result()
	if !slices.Contains(queued, res.Trade.ID) {
		t.Fatalf("not queued for settlement: %v", queued)
	}
}
