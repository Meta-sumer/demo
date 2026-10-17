package settlement

import (
	"errors"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/wallets"
)

// Integration tests of the settlement state machine (F-11, F-12, F-14).
// They need MongoDB and Redis and skip without them.

func settle(h *harness, trade *models.BinaryTrade) Outcome {
	h.t.Helper()
	out, err := SettleTrade(h.ctx, trade.ID.Hex(), "")
	if err != nil {
		h.t.Fatalf("settle: %v", err)
	}
	return out
}

// ── F-11: expiry price policy and evidence ────────────────────────────────

func TestSettleWinPersistsReproducibleEvidence(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	seq := int64(42)
	h.tick(trade, tickOpts{price: 100.5, ts: exp - 700})
	h.tick(trade, tickOpts{price: 101.25, ts: exp - 40, receivedAt: exp - 35, seq: &seq})
	h.tick(trade, tickOpts{price: 99, ts: exp + 20}) // after expiry: confirms, never used

	out := settle(h, trade)
	if out.Status != StatusSettled || *out.Result != models.TradeResultWin || out.Rule != models.SettlementRuleTickAtOrBeforeExpiry {
		t.Fatalf("outcome %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusWon, testProfit)
	ev := row.Evidence
	if ev == nil || ev.Price == nil || *ev.Price != 101.25 || *ev.TickTimestamp != exp-40 || *ev.ReceivedAt != exp-35 ||
		*ev.ProviderTimestamp != exp-40 || *ev.Sequence != 42 || *ev.TickAgeMs != 40 || ev.Provider != "test-feed" ||
		ev.Source != models.SettlementEvidenceSourceHist || !ev.PostExpiryTickSeen || *ev.PostExpiryTickTimestamp != exp+20 {
		t.Fatalf("evidence %+v", ev)
	}
	if ev.RiskConfigVersion < 1 || ev.Policy.MaxTickAgeMs != DefaultPolicy().MaxTickAgeMs || ev.DrawPolicy != models.DrawPolicyRefund {
		t.Fatalf("config evidence %+v", ev)
	}
	// Reproducible from the row alone: the comparison and the selection.
	if r := ReplayExpiryResult(trade.Direction, row.EntryPrice, ev); r == nil || *r != row.Result {
		t.Fatalf("replay %v vs %s", r, row.Result)
	}
	replayed := DecideExpiryPrice(ev.ExpiryTimestamp, ev.DecidedAt, ExpiryCandidates{
		Ticks:              []models.NormalisedTick{tk(*ev.Price, *ev.TickTimestamp, *ev.ReceivedAt)},
		PostExpiryTickSeen: ev.PostExpiryTickSeen,
	}, ev.Policy)
	if replayed.Kind != DecisionPriced || replayed.Tick.Price != *ev.Price {
		t.Fatalf("selection replay %+v", replayed)
	}

	got := h.load(trade.ID)
	if *got.ExpiryPrice != 101.25 || *got.ExpiryPriceSource != models.SettlementRuleTickAtOrBeforeExpiry ||
		*got.ExpiryTickSequence != 42 || *got.ExpiryProviderTimestamp != exp-40 || got.SettlementIntent == nil {
		t.Fatalf("trade fields %+v", got)
	}
	if h.exposureOpenTrades(trade) != 0 || h.queued(config.RedisKeys.ExpiryQueue, trade.ID.Hex()) {
		t.Fatal("exposure or expiry queue not released")
	}
}

func TestSettleEqualityAppliesDrawPolicy(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	refund := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(refund, tickOpts{price: 100, ts: exp - 10})
	h.tick(refund, tickOpts{price: 100.1, ts: exp + 10})
	settle(h, refund)
	row := h.assertBooked(refund, models.TradeStatusDraw, 0)
	if *row.Evidence.RawResult != models.TradeResultDraw || row.PayoutAmount != testStake {
		t.Fatalf("refund row %+v", row)
	}

	h.setRiskConfig(bson.M{"drawPolicy": string(models.DrawPolicyLoss)})
	loss := h.openTrade(tradeOpts{entry: 100, expiry: exp, direction: models.DirectionDown})
	h.tick(loss, tickOpts{price: 100, ts: exp - 10})
	h.tick(loss, tickOpts{price: 100.1, ts: exp + 10})
	out := settle(h, loss)
	if *out.Result != models.TradeResultLoss {
		t.Fatalf("outcome %+v", out)
	}
	row = h.row(loss.ID)
	if *row.Evidence.RawResult != models.TradeResultDraw || row.Evidence.DrawPolicy != models.DrawPolicyLoss || row.Result != models.TradeResultLoss {
		t.Fatalf("loss row %+v", row.Evidence)
	}
	if r := ReplayExpiryResult(loss.Direction, row.EntryPrice, row.Evidence); *r != models.TradeResultLoss {
		t.Fatalf("replay %s", *r)
	}
}

func TestSettleUsesLateAndOutOfOrderTicksStampedBeforeExpiry(t *testing.T) {
	h := newHarness(t)
	exp := now() - 4000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp, direction: models.DirectionDown})
	h.tick(trade, tickOpts{price: 98, ts: exp - 900})
	h.tick(trade, tickOpts{price: 104, ts: exp + 30}) // post-expiry first
	// Arrives after the post-expiry tick, stamped before expiry: in flight.
	h.tick(trade, tickOpts{price: 99.5, ts: exp - 15, receivedAt: exp + 1200})
	out := settle(h, trade)
	if *out.Result != models.TradeResultWin || *out.ExpiryPrice != 99.5 {
		t.Fatalf("outcome %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusWon, testProfit)
	if *row.Evidence.ReceivedAt != exp+1200 || *row.Evidence.TickTimestamp != exp-15 {
		t.Fatalf("evidence %+v", row.Evidence)
	}
}

func TestSettleIgnoresClockSkewedTick(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 99, ts: exp - 500})
	// Stamped 5s ahead of its receipt: the provider clock cannot be trusted.
	h.tick(trade, tickOpts{price: 150, ts: exp - 10, receivedAt: exp - 5010})
	h.tick(trade, tickOpts{price: 99.2, ts: exp + 10})
	out := settle(h, trade)
	if *out.ExpiryPrice != 99 || *out.Result != models.TradeResultLoss {
		t.Fatalf("outcome %+v", out)
	}
	h.assertBooked(trade, models.TradeStatusLost, -testStake)
}

func TestSettleAwaitsPostExpiryTickThenSettles(t *testing.T) {
	h := newHarness(t)
	exp := now() - 300
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 101, ts: exp - 20})
	out := settle(h, trade)
	if out.Status != StatusAwaitingTick || out.RetryAt == nil {
		t.Fatalf("outcome %+v", out)
	}
	if got := h.load(trade.ID); got.Status != models.TradeStatusOpen {
		t.Fatalf("deferred trade is %s", got.Status)
	}
	if score, err := redisx.R().ZScore(h.ctx, config.RedisKeys.ExpiryQueue, trade.ID.Hex()).Result(); err != nil || int64(score) != *out.RetryAt {
		t.Fatalf("not rescheduled: %v %v", score, err)
	}
	h.tick(trade, tickOpts{price: 90, ts: exp + 5})
	if out := settle(h, trade); out.Status != StatusSettled || *out.ExpiryPrice != 101 {
		t.Fatalf("outcome %+v", out)
	}
	h.assertBooked(trade, models.TradeStatusWon, testProfit)
}

func TestSettlePostExpiryOnlyDefersThenVoidsAndRefunds(t *testing.T) {
	h := newHarness(t)
	h.setRiskConfig(bson.M{"settlementDeferWindowMs": int64(5000), "settlementMaxTickAgeMs": int64(1000)})
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 90, ts: exp - 1500}) // too old for the 1s max age
	h.tick(trade, tickOpts{price: 120, ts: exp + 1})   // post-expiry: never used
	h.tick(trade, tickOpts{price: 121, ts: exp + 800})

	out := settle(h, trade)
	if out.Status != StatusSkippedNoPrice || h.load(trade.ID).Status != models.TradeStatusOpen {
		t.Fatalf("outcome %+v", out)
	}
	if n := h.count(models.CollWalletTransactions, bson.M{"idempotencyKey": SettleKey(trade.ID.Hex())}); n != 0 {
		t.Fatal("money moved while deferred")
	}

	// Past the defer window: the configured rule (void) applies, on record.
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(h.ctx, bson.M{"_id": trade.ID},
		bson.M{"$set": bson.M{"expiryTimestamp": exp - 6000}}); err != nil {
		t.Fatal(err)
	}
	out = settle(h, trade)
	if out.Status != StatusSettled || out.Rule != models.SettlementRuleNoPriceVoidRefund || *out.Result != models.TradeResultDraw {
		t.Fatalf("outcome %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusDraw, 0)
	if row.Evidence.Price != nil || row.Evidence.Rule != models.SettlementRuleNoPriceVoidRefund ||
		row.Evidence.Policy.DeferWindowMs != 5000 || row.Evidence.Policy.MaxTickAgeMs != 1000 || row.PayoutAmount != testStake {
		t.Fatalf("void evidence %+v", row.Evidence)
	}
	got := h.load(trade.ID)
	if got.ExpiryPrice != nil || *got.ExpiryPriceSource != models.SettlementRuleNoPriceVoidRefund {
		t.Fatalf("void trade %+v", got)
	}
	if n := h.count(models.CollAuditLogs, bson.M{"action": "trade.voided", "resourceId": trade.ID.Hex()}); n != 1 {
		t.Fatalf("%d void audit rows", n)
	}
	if n := h.count(models.CollNotifications, bson.M{"userId": h.userID, "title": "Trade voided"}); n != 1 {
		t.Fatalf("%d void notifications", n)
	}
}

func TestSettleStaleTickRuleIsExplicit(t *testing.T) {
	h := newHarness(t)
	h.setRiskConfig(bson.M{"settlementNoPriceRule": models.NoPriceRuleStaleTickThenVoid, "settlementDeferWindowMs": int64(2000)})
	exp := now() - 10_000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 99, ts: exp - 30_000})
	h.tick(trade, tickOpts{price: 130, ts: exp + 100})
	out := settle(h, trade)
	if out.Rule != models.SettlementRuleStaleTickBeforeExpiry || *out.ExpiryPrice != 99 {
		t.Fatalf("outcome %+v", out)
	}
	h.assertBooked(trade, models.TradeStatusLost, -testStake)
	if n := h.count(models.CollAuditLogs, bson.M{"action": "trade.settled_by_rule", "resourceId": trade.ID.Hex()}); n != 1 {
		t.Fatalf("%d audit rows", n)
	}
}

// ── F-14 / F-12: never early, one outcome under concurrency ──────────────

func TestSettleNeverBeforeExpiryRequeues(t *testing.T) {
	h := newHarness(t)
	exp := now() + 60_000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 150, ts: now()})
	out := settle(h, trade)
	if out.Status != StatusNotDue || *out.RetryAt != exp || h.load(trade.ID).Status != models.TradeStatusOpen {
		t.Fatalf("outcome %+v", out)
	}
	// Even a direct expiry claim is refused by the database clock.
	intent := expiryIntent(trade, models.TradeResultWin, f64p(150), models.SettlementEvidence{Rule: "TEST"}, "test")
	if claimed, err := claimExpiry(h.ctx, trade.ID, &intent, "test"); err != nil || claimed != nil {
		t.Fatalf("early expiry claim: %v %v", claimed, err)
	}
}

func TestTwoSettlementWorkersOnSameTrade(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 100.01, ts: exp - 5})
	h.tick(trade, tickOpts{price: 100.02, ts: exp + 5})

	const workers = 8
	var wg sync.WaitGroup
	outcomes := make([]Outcome, workers)
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			outcomes[i], errs[i] = SettleTrade(h.ctx, trade.ID.Hex(), "worker-"+string(rune('a'+i)))
		}(i)
	}
	wg.Wait()
	settled := 0
	for i := range outcomes {
		if errs[i] != nil {
			t.Fatalf("worker %d: %v", i, errs[i])
		}
		if outcomes[i].Status == StatusSettled {
			settled++
		}
	}
	if settled != 1 {
		t.Fatalf("%d workers reported SETTLED: %+v", settled, outcomes)
	}
	h.assertBooked(trade, models.TradeStatusWon, testProfit)
	if h.exposureOpenTrades(trade) != 0 {
		t.Fatal("exposure released more or less than once")
	}
	if n := h.count(models.CollNotifications, bson.M{"userId": h.userID}); n != 1 {
		t.Fatalf("%d notifications", n)
	}
}

func TestDuplicateQueueEntriesSettleOnce(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 99.9, ts: exp - 5})
	h.tick(trade, tickOpts{price: 99.8, ts: exp + 5})
	for i := 0; i < 5; i++ {
		if err := redisx.R().LPush(h.ctx, config.RedisKeys.SettlementQueue, trade.ID.Hex()).Err(); err != nil {
			t.Fatal(err)
		}
	}
	ids, err := drain(h.ctx, redisx.R(), 50)
	if err != nil || len(ids) != 5 {
		t.Fatalf("drain %v %v", ids, err)
	}
	for _, id := range ids {
		settleOne(id)
	}
	h.assertBooked(trade, models.TradeStatusLost, -testStake)
	if h.exposureOpenTrades(trade) != 0 {
		t.Fatal("exposure released more than once")
	}
}

// ── F-12 / F-14: crash recovery ───────────────────────────────────────────

func crashAt(stage string) func(string) error {
	once := false
	return func(s string) error {
		if s == stage && !once {
			once = true
			return errors.New("injected crash at " + stage)
		}
		return nil
	}
}

func TestCrashAfterWalletMutationRecoversToOneOutcome(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 101, ts: exp - 5})
	h.tick(trade, tickOpts{price: 102, ts: exp + 5})

	testHook = crashAt("after_wallet")
	if _, err := SettleTrade(h.ctx, trade.ID.Hex(), ""); err == nil {
		t.Fatal("expected the injected crash")
	}
	got := h.load(trade.ID)
	if got.Status != models.TradeStatusSettling || got.SettlementIntent == nil || h.row(trade.ID) != nil {
		t.Fatalf("after crash: %s intent=%v", got.Status, got.SettlementIntent)
	}
	if w := h.wallet(); w.Balance != testBalance+testProfit {
		t.Fatalf("wallet not moved before crash: %+v", w)
	}

	// A later tick with a different price must not change the booked outcome.
	h.tick(trade, tickOpts{price: 50, ts: exp - 1})
	out := settle(h, trade)
	if out.Status != StatusSettled || *out.Result != models.TradeResultWin {
		t.Fatalf("recovery %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusWon, testProfit)
	if *row.Evidence.Price != 101 {
		t.Fatalf("recovery re-priced: %v", *row.Evidence.Price)
	}
	if again := settle(h, trade); again.Status != StatusAlreadySettled {
		t.Fatalf("replay %+v", again)
	}
}

func TestCrashAfterSettlementRowRecovers(t *testing.T) {
	h := newHarness(t)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 99, ts: exp - 5})
	h.tick(trade, tickOpts{price: 98, ts: exp + 5})

	testHook = crashAt("after_row")
	if _, err := SettleTrade(h.ctx, trade.ID.Hex(), ""); err == nil {
		t.Fatal("expected the injected crash")
	}
	if h.load(trade.ID).Status != models.TradeStatusSettling || h.row(trade.ID) == nil {
		t.Fatal("crash state not as expected")
	}
	settle(h, trade)
	h.assertBooked(trade, models.TradeStatusLost, -testStake)
	if h.exposureOpenTrades(trade) != 0 {
		t.Fatal("exposure not released after recovery")
	}
}

func earlyCloseIntentFor(trade *models.BinaryTrade, value int64) models.SettlementIntent {
	ev := models.SettlementEvidence{Rule: models.SettlementRuleEarlyCloseAtMarket, Kind: models.SettlementKindEarlyClose, ExpiryTimestamp: trade.ExpiryTimestamp, DecidedAt: now()}
	return EarlyCloseIntent(trade, value, f64p(101), ev, "customer.early_close")
}

func TestEarlyCloseCrashAfterWalletRecoversAsClosedEarly(t *testing.T) {
	h := newHarness(t)
	trade := h.openTrade(tradeOpts{entry: 100, expiry: now() + 120_000})
	value := int64(120_00)
	intent := earlyCloseIntentFor(trade, value)
	claimed, err := ClaimEarlyClose(h.ctx, trade.ID, h.userID, &intent, EarlyCloseHardFloorMs)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v %v", claimed, err)
	}
	testHook = crashAt("after_wallet")
	if _, err := Execute(h.ctx, claimed); err == nil {
		t.Fatal("expected the injected crash")
	}

	// The sweeper / worker picks it up before expiry: it must finish the
	// close, not settle at the current price and not end as a draw.
	h.tick(trade, tickOpts{price: 200, ts: now()})
	out := settle(h, trade)
	if out.Status != StatusSettled || *out.Result != models.TradeResultEarlyClose {
		t.Fatalf("recovery %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusClosedEarly, value-testStake)
	if row.PayoutAmount != value || row.SettledBy != "customer.early_close" {
		t.Fatalf("row %+v", row)
	}
	if n := h.count(models.CollWalletTransactions, bson.M{"idempotencyKey": SettleKey(trade.ID.Hex()), "type": models.WalletTxTradeEarlyClose}); n != 1 {
		t.Fatal("ledger row is not the early close")
	}
}

func TestEarlyCloseAndExpiryAreMutuallyExclusive(t *testing.T) {
	h := newHarness(t)

	// After expiry: an early-close claim is refused even though the trade is OPEN.
	past := h.openTrade(tradeOpts{entry: 100, expiry: now() - 2500})
	intent := earlyCloseIntentFor(past, 150_00)
	if claimed, err := ClaimEarlyClose(h.ctx, past.ID, h.userID, &intent, EarlyCloseHardFloorMs); err != nil || claimed != nil {
		t.Fatalf("claim after expiry: %v %v", claimed, err)
	}
	// Inside the floor: refused too.
	near := h.openTrade(tradeOpts{entry: 100, expiry: now() + 400})
	intent = earlyCloseIntentFor(near, 150_00)
	if claimed, err := ClaimEarlyClose(h.ctx, near.ID, h.userID, &intent, EarlyCloseHardFloorMs); err != nil || claimed != nil {
		t.Fatalf("claim inside floor: %v %v", claimed, err)
	}

	// Race: closes and settlements hammering one trade before expiry.
	open := h.openTrade(tradeOpts{entry: 100, expiry: now() + 60_000})
	h.tick(open, tickOpts{price: 101, ts: now()})
	var wg sync.WaitGroup
	var mu sync.Mutex
	closes, settles := 0, 0
	for i := 0; i < 6; i++ {
		wg.Add(2)
		go func(v int64) {
			defer wg.Done()
			in := earlyCloseIntentFor(open, v)
			claimed, err := ClaimEarlyClose(h.ctx, open.ID, h.userID, &in, EarlyCloseHardFloorMs)
			if err != nil {
				t.Error(err)
				return
			}
			if claimed != nil {
				if _, err := Execute(h.ctx, claimed); err != nil {
					t.Error(err)
				}
				mu.Lock()
				closes++
				mu.Unlock()
			}
		}(int64(110_00 + i))
		go func() {
			defer wg.Done()
			out, err := SettleTrade(h.ctx, open.ID.Hex(), "")
			if err != nil {
				t.Error(err)
				return
			}
			if out.Status == StatusSettled && out.Rule != models.SettlementRuleEarlyCloseAtMarket {
				mu.Lock()
				settles++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if closes != 1 || settles != 0 {
		t.Fatalf("closes %d expiry settlements %d", closes, settles)
	}
	got := h.load(open.ID)
	if got.Status != models.TradeStatusClosedEarly {
		t.Fatalf("status %s", got.Status)
	}
	if n := h.count(models.CollWalletTransactions, bson.M{"idempotencyKey": SettleKey(open.ID.Hex())}); n != 1 {
		t.Fatalf("%d wallet effects", n)
	}
	if h.exposureOpenTrades(open) != 0 {
		t.Fatal("exposure")
	}
	// The trade that expired settles normally; neither path touched the other.
	h.tick(past, tickOpts{price: 99, ts: past.ExpiryTimestamp - 10})
	h.tick(past, tickOpts{price: 99, ts: past.ExpiryTimestamp + 10})
	if out := settle(h, past); out.Status != StatusSettled || *out.Result != models.TradeResultLoss {
		t.Fatalf("expired trade %+v", out)
	}
}

// ── recovery of states left by an earlier version (no intent) ─────────────

func (h *harness) makeLegacySettling(trade *models.BinaryTrade) {
	h.t.Helper()
	stale := time.UnixMilli(now() - StaleSettlingMs - 5000).UTC()
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(h.ctx, bson.M{"_id": trade.ID},
		bson.M{"$set": bson.M{"status": string(models.TradeStatusSettling), "updatedAt": stale}}); err != nil {
		h.t.Fatal(err)
	}
}

func TestLegacySettlingBeforeExpiryReturnsToOpen(t *testing.T) {
	h := newHarness(t)
	trade := h.openTrade(tradeOpts{entry: 100, expiry: now() + 90_000})
	h.tick(trade, tickOpts{price: 130, ts: now()})
	h.makeLegacySettling(trade)
	_ = redisx.R().ZRem(h.ctx, config.RedisKeys.ExpiryQueue, trade.ID.Hex())

	out := settle(h, trade)
	if out.Status != StatusNotDue {
		t.Fatalf("outcome %+v", out)
	}
	got := h.load(trade.ID)
	if got.Status != models.TradeStatusOpen || got.SettlementClaim != nil {
		t.Fatalf("status %s", got.Status)
	}
	if !h.queued(config.RedisKeys.ExpiryQueue, trade.ID.Hex()) {
		t.Fatal("not back on the expiry queue")
	}
	if w := h.wallet(); w.LockedBalance != testStake || w.Balance != testBalance {
		t.Fatalf("money moved: %+v", w)
	}
	if n := h.count(models.CollAuditLogs, bson.M{"action": "settlement.claim_reverted"}); n != 1 {
		t.Fatalf("%d audit rows", n)
	}
}

func TestLegacySettlingAfterExpirySettlesOnPolicyPrice(t *testing.T) {
	h := newHarness(t)
	exp := now() - 40_000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 100.5, ts: exp - 100})
	h.tick(trade, tickOpts{price: 90, ts: exp + 100}) // the old fallback could have used this
	h.makeLegacySettling(trade)
	out := settle(h, trade)
	if out.Status != StatusSettled || *out.ExpiryPrice != 100.5 {
		t.Fatalf("outcome %+v", out)
	}
	h.assertBooked(trade, models.TradeStatusWon, testProfit)
}

func TestLegacyEarlyCloseLedgerWithoutRowFinishesAsClosedEarly(t *testing.T) {
	h := newHarness(t)
	exp := now() - 5000 // expired meanwhile: still must not become WIN/LOSS
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 150, ts: exp - 10})
	h.tick(trade, tickOpts{price: 150, ts: exp + 10})
	value := int64(70_00)
	// The old close path moved the money, then died before the row.
	if _, err := wallets.SettleTradeEarlyClose(h.ctx, wallets.TradeFundsInput{
		UserID: h.userID.Hex(), Stake: testStake, TradeID: trade.ID.Hex(),
	}, value); err != nil {
		t.Fatal(err)
	}
	h.makeLegacySettling(trade)
	out := settle(h, trade)
	if out.Status != StatusSettled || *out.Result != models.TradeResultEarlyClose {
		t.Fatalf("outcome %+v", out)
	}
	row := h.assertBooked(trade, models.TradeStatusClosedEarly, value-testStake)
	if row.PayoutAmount != value || row.Evidence.Rule != models.SettlementRuleLedgerRecovery {
		t.Fatalf("row %+v", row)
	}
}

func TestLegacyEarlyCloseRowFinishesAsClosedEarlyNotDraw(t *testing.T) {
	h := newHarness(t)
	trade := h.openTrade(tradeOpts{entry: 100, expiry: now() + 60_000})
	value := int64(130_00)
	mut, err := wallets.SettleTradeEarlyClose(h.ctx, wallets.TradeFundsInput{
		UserID: h.userID.Hex(), Stake: testStake, TradeID: trade.ID.Hex(),
	}, value)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := bson.ObjectIDFromHex(mut.TransactionID)
	if _, err := mongox.C(models.CollSettlements).InsertOne(h.ctx, models.Settlement{
		TradeID: trade.ID, UserID: h.userID, AssetID: trade.AssetID, Symbol: trade.Symbol, Result: models.TradeResultEarlyClose,
		AccountType: models.AccountLive, EntryPrice: 100, ExpiryPrice: 101, ExpiryTimestamp: trade.ExpiryTimestamp,
		Stake: testStake, PayoutPercent: testPayout, PayoutAmount: value, PlatformResult: testStake - value,
		Currency: "INR", WalletTransactionID: &txID, SettledBy: "customer.early_close", CreatedAt: mongox.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	h.makeLegacySettling(trade)
	settle(h, trade)
	got := h.load(trade.ID)
	if got.Status != models.TradeStatusClosedEarly || *got.SettlementAmount != value-testStake {
		t.Fatalf("status %s amount %v", got.Status, *got.SettlementAmount)
	}
	h.assertBooked(trade, models.TradeStatusClosedEarly, value-testStake)
}

func TestIntentDisagreeingWithLedgerAdoptsLedger(t *testing.T) {
	h := newHarness(t)
	trade := h.openTrade(tradeOpts{entry: 100, expiry: now() - 5000})
	value := int64(90_00)
	if _, err := wallets.SettleTradeEarlyClose(h.ctx, wallets.TradeFundsInput{
		UserID: h.userID.Hex(), Stake: testStake, TradeID: trade.ID.Hex(),
	}, value); err != nil {
		t.Fatal(err)
	}
	// An intent that disagrees with what was booked.
	intent := expiryIntent(trade, models.TradeResultWin, f64p(150), models.SettlementEvidence{Rule: models.SettlementRuleTickAtOrBeforeExpiry}, "test")
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(h.ctx, bson.M{"_id": trade.ID}, claimSet(&intent, "test")); err != nil {
		t.Fatal(err)
	}
	out := settle(h, trade)
	if *out.Result != models.TradeResultEarlyClose {
		t.Fatalf("outcome %+v", out)
	}
	h.assertBooked(trade, models.TradeStatusClosedEarly, value-testStake)
	if n := h.count(models.CollAuditLogs, bson.M{"action": "settlement.ledger_adopted"}); n != 1 {
		t.Fatalf("%d audit rows", n)
	}
}

func TestDemoTradeSettlesWithoutExposure(t *testing.T) {
	h := newHarness(t)
	h.fund(models.AccountDemo)
	exp := now() - 3000
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp, accountType: models.AccountDemo})
	h.tick(trade, tickOpts{price: 101, ts: exp - 5})
	h.tick(trade, tickOpts{price: 101, ts: exp + 5})
	if out := settle(h, trade); out.Status != StatusSettled {
		t.Fatalf("outcome %+v", out)
	}
	if row := h.row(trade.ID); row.AccountType != models.AccountDemo {
		t.Fatalf("row account %s", row.AccountType)
	}
}

// ── with multi-document transactions (row + trade finalised atomically) ───

func TestTransactionalSettlementConcurrentAndCrash(t *testing.T) {
	h := newHarnessTx(t, true)
	exp := now() - 3000

	// Concurrent executors with transactions: still one outcome.
	trade := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(trade, tickOpts{price: 100.5, ts: exp - 5})
	h.tick(trade, tickOpts{price: 100.6, ts: exp + 5})
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := SettleTrade(h.ctx, trade.ID.Hex(), ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	h.assertBooked(trade, models.TradeStatusWon, testProfit)

	// A crash between the row and the trade update rolls the row back with
	// the transaction; recovery books exactly one row.
	second := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(second, tickOpts{price: 99.5, ts: exp - 5})
	h.tick(second, tickOpts{price: 99.4, ts: exp + 5})
	testHook = crashAt("after_row")
	if _, err := SettleTrade(h.ctx, second.ID.Hex(), ""); err == nil {
		t.Fatal("expected the injected crash")
	}
	if h.row(second.ID) != nil {
		t.Fatal("row survived an aborted transaction")
	}
	if got := h.load(second.ID); got.Status != models.TradeStatusSettling || got.SettlementIntent == nil {
		t.Fatalf("after crash: %s", got.Status)
	}
	settle(h, second)
	if got := h.load(second.ID); got.Status != models.TradeStatusLost {
		t.Fatalf("status %s", got.Status)
	}
	if n := h.count(models.CollSettlements, bson.M{"tradeId": second.ID}); n != 1 {
		t.Fatalf("%d rows", n)
	}
	w := h.wallet()
	if w.Balance != testBalance+testProfit-testStake || w.LockedBalance != 0 {
		t.Fatalf("wallet %+v", w)
	}
}
