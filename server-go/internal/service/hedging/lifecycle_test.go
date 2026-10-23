package hedging

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/hedging/binancefake"
	"binarytrading/internal/service/hedging/providers"
)

// End to end: a book, a target, an order, fills, a position, a clean
// reconciliation, and a decision that replays to the same order.
func TestEvaluatePlacesFillsAndReplays(t *testing.T) {
	h := newHarness(t)
	h.fake.Set(func(f *binancefake.Fake) { f.Splits = 2 })
	h.trade(models.DirectionUp, 1_000_00, 300)

	ev := h.evaluate()
	o := ev.Order
	if o == nil || ev.SkippedReason != "" {
		t.Fatalf("no order: %+v", ev)
	}
	if o.Status != models.HedgeOrderStatusFilled || o.Side != models.DirectionUp || o.FilledQtyE8 != o.TargetQtyE8 {
		t.Fatalf("order %+v", o)
	}
	if !strings.HasPrefix(o.ClientOrderID, "bth-") || o.IdempotencyKey != o.ClientOrderID || o.WorkingSlot != nil {
		t.Fatalf("ids %q %q slot %v", o.ClientOrderID, o.IdempotencyKey, o.WorkingSlot)
	}
	if o.FillCount != 2 || o.FillsQtyE8 != o.FilledQtyE8 || o.FeeE8 <= 0 || o.FeeInr <= 0 || o.RealisedCostBps == nil {
		t.Fatalf("fills on order %+v", o)
	}
	// Every decision input is stored (volatility and expiry were 0/empty before).
	d := o.Decision
	if d.Target.Sigma != testSigma || d.Target.SigmaSource != "test" || d.SecondsToNearestExpiry == nil ||
		len(d.Legs) != 1 || d.InrPerUsdt != 88 || d.VenuePrice <= 0 || d.Rules.StepE8 != 100_000 || d.EstimatedCost.RoundTripBps <= 0 {
		t.Fatalf("decision %+v", d)
	}
	r := ReplayDecision(d)
	if !r.Exact || r.BookDelta != d.Target.BookDelta || r.TargetSigned != d.Target.TargetSigned || r.Plan.ExecutableQtyE8 != d.ExecutableQtyE8 {
		t.Fatalf("replay %+v vs stored delta %d target %d qty %d", r, d.Target.BookDelta, d.Target.TargetSigned, d.ExecutableQtyE8)
	}

	p := h.position()
	if p.QtyE8 != o.FilledQtyE8 || p.FillCount != 2 || p.AvgPrice <= 60_000 || h.fake.PositionE8("BTCUSDT") != p.QtyE8 {
		t.Fatalf("position %+v venue %d", p, h.fake.PositionE8("BTCUSDT"))
	}
	res, err := ReconcilePositions(h.ctx)
	if err != nil || len(res) != 1 || res[0].Status != models.HedgeReconOK {
		t.Fatalf("reconcile %+v %v", res, err)
	}
	if p = h.position(); p.ReconcileStatus != models.HedgeReconOK || p.ReconciledAt == nil {
		t.Fatalf("reconciled position %+v", p)
	}

	// At target: no second order (the rest is below one quantity step).
	ev = h.evaluate()
	if ev.Order != nil || ev.Plan == nil || ev.Plan.ExecutableQtyE8 != 0 {
		t.Fatalf("second evaluation placed %+v", ev)
	}
	if h.fake.Placed != 1 {
		t.Fatalf("venue orders %d", h.fake.Placed)
	}

	// The book goes away: the hedge is unwound reduce-only, P&L realised.
	h.closeAllTrades()
	ev = h.evaluate()
	if ev.Order == nil || ev.Order.Side != models.DirectionDown || !ev.Order.ReduceOnly || ev.Order.Status != models.HedgeOrderStatusFilled {
		t.Fatalf("unwind %+v", ev)
	}
	if p = h.position(); p.QtyE8 != 0 || p.RealisedE8 == 0 || p.FeesE8 <= 0 {
		t.Fatalf("flat position %+v", p)
	}
}

func TestPartialFillThenRemainder(t *testing.T) {
	h := newHarness(t)
	h.fake.Set(func(f *binancefake.Fake) { f.FillRatio = 0.5 })
	h.trade(models.DirectionUp, 1_000_00, 300)

	first := h.evaluate().Order
	if first.Status != models.HedgeOrderStatusCancelled || first.VenueStatus != providers.VenueExpired ||
		first.FilledQtyE8 == 0 || first.FilledQtyE8 >= first.TargetQtyE8 || first.WorkingSlot != nil {
		t.Fatalf("partial %+v", first)
	}
	view := models.ToHedgeView(first)
	if view.RemainingQty <= 0 || view.FilledQty <= 0 || view.Working {
		t.Fatalf("view %+v", view)
	}
	// Only the fill counts as protection; the next pass orders the rest.
	if h.position().QtyE8 != first.FilledQtyE8 {
		t.Fatal("position must equal the fill")
	}
	h.fake.Set(func(f *binancefake.Fake) { f.FillRatio = 1 })
	second := h.evaluate().Order
	if second == nil || second.Status != models.HedgeOrderStatusFilled || second.ClientOrderID == first.ClientOrderID {
		t.Fatalf("remainder %+v", second)
	}
	if got := h.position().QtyE8; got != first.FilledQtyE8+second.FilledQtyE8 || got != h.fake.PositionE8("BTCUSDT") {
		t.Fatalf("position %d", got)
	}
}

// The venue accepted and filled the order but the answer never arrived: the
// order stays PENDING, is looked up by clientOrderId, and is never re-sent.
func TestTimeoutAfterAcceptanceIsQueriedNotResent(t *testing.T) {
	h := newHarness(t, func(e *config.Env) { e.HedgeRequestTimeoutMs = 300 })
	h.fake.Set(func(f *binancefake.Fake) { f.DelayAfterAccept = 2 * time.Second })
	h.trade(models.DirectionUp, 1_000_00, 300)

	o := h.evaluate().Order
	if o.Status != models.HedgeOrderStatusPending || o.Error == nil || !strings.Contains(*o.Error, "outcome unknown") || o.SendAttempts != 1 {
		t.Fatalf("after timeout %+v", o)
	}
	h.fake.Set(func(f *binancefake.Fake) { f.DelayAfterAccept = 0 })
	if _, err := RefreshWorkingOrders(h.ctx); err != nil {
		t.Fatal(err)
	}
	o = h.order(o.ID)
	if o.Status != models.HedgeOrderStatusFilled || o.FillsQtyE8 != o.FilledQtyE8 || o.ReconciledAt == nil {
		t.Fatalf("recovered %+v", o)
	}
	// The next evaluation sees the filled position: nothing more to send.
	if ev := h.evaluate(); ev.Order != nil {
		t.Fatalf("second order after recovery: %+v", ev.Order)
	}
	if h.fake.Placed != 1 || h.fake.OrderCount(o.ClientOrderID) != 1 || h.fake.Requests["POST /fapi/v1/order"] != 1 {
		t.Fatalf("re-sent: placed %d", h.fake.Placed)
	}
}

// The request was lost before the venue accepted it: "not found" is only
// believed after the recvWindow, then the order FAILS and a fresh intent
// (new clientOrderId) is placed.
func TestLostRequestFailsAfterRecvWindow(t *testing.T) {
	h := newHarness(t, func(e *config.Env) { e.HedgeRequestTimeoutMs = 300 })
	h.fake.Set(func(f *binancefake.Fake) { f.DropBeforeAccept = time.Second })
	h.trade(models.DirectionUp, 1_000_00, 300)

	o := h.evaluate().Order
	if o.Status != models.HedgeOrderStatusPending {
		t.Fatalf("after loss %+v", o)
	}
	h.fake.Set(func(f *binancefake.Fake) { f.DropBeforeAccept = 0 })
	if _, err := RefreshWorkingOrders(h.ctx); err != nil {
		t.Fatal(err)
	}
	if o = h.order(o.ID); o.Status != models.HedgeOrderStatusPending {
		t.Fatalf("believed not-found inside the recvWindow: %+v", o)
	}
	// Let the recvWindow pass.
	old := time.Now().Add(-time.Minute)
	if _, err := mongox.C(models.CollHedgeOrders).UpdateOne(h.ctx, bson.M{"_id": o.ID}, bson.M{"$set": bson.M{"sentAt": old}}); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshWorkingOrders(h.ctx); err != nil {
		t.Fatal(err)
	}
	if o = h.order(o.ID); o.Status != models.HedgeOrderStatusFailed || o.WorkingSlot != nil {
		t.Fatalf("not failed %+v", o)
	}
	next := h.evaluate().Order
	if next == nil || next.ClientOrderID == o.ClientOrderID || next.Status != models.HedgeOrderStatusFilled || h.fake.Placed != 1 {
		t.Fatalf("fresh intent %+v placed %d", next, h.fake.Placed)
	}
}

func TestDuplicateFillReportsCountOnce(t *testing.T) {
	h := newHarness(t)
	h.fake.Set(func(f *binancefake.Fake) { f.Splits = 3; f.DuplicateTrades = true })
	h.trade(models.DirectionUp, 1_000_00, 300)
	o := h.evaluate().Order
	if o.FillCount != 3 || o.FillsQtyE8 != o.FilledQtyE8 {
		t.Fatalf("fills %+v", o)
	}
	// The same fills reported again (another sync) change nothing.
	if _, err := syncFills(h.ctx, h.venue, o); err != nil {
		t.Fatal(err)
	}
	if n := h.count(models.CollHedgeFills, bson.M{"hedgeOrderId": o.ID}); n != 3 {
		t.Fatalf("ledger rows %d", n)
	}
	before := h.position()
	h.closeAllTrades()
	if ev := h.evaluate(); ev.Order == nil {
		t.Fatalf("no unwind: %s", ev.SkippedReason)
	}
	after := h.position()
	if after.QtyE8 != 0 || after.QtyE8 != h.fake.PositionE8("BTCUSDT") {
		t.Fatalf("after close %+v venue %d", after, h.fake.PositionE8("BTCUSDT"))
	}
	// Our realised P&L equals the venue's own realizedPnl (average cost).
	cur, err := mongox.C(models.CollHedgeFills).Find(h.ctx, bson.M{"assetId": h.asset.ID})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.HedgeFill
	if err := cur.All(h.ctx, &rows); err != nil {
		t.Fatal(err)
	}
	var venue int64
	for _, r := range rows {
		venue += r.VenueRealizedPnlE8
	}
	if after.RealisedE8 != venue || before.FillCount != 3 || after.FillCount != int64(len(rows)) {
		t.Fatalf("realised %d venue %d rows %d", after.RealisedE8, venue, len(rows))
	}
}

// A cancel that reaches the venue after the order filled must leave it
// FILLED, never CANCELLED.
func TestCancelRacingAFill(t *testing.T) {
	h := newHarness(t)
	h.fake.Set(func(f *binancefake.Fake) { f.RestNew = true })
	h.trade(models.DirectionUp, 1_000_00, 300)
	o := h.evaluate().Order
	if o.Status != models.HedgeOrderStatusAccepted || o.WorkingSlot == nil {
		t.Fatalf("resting %+v", o)
	}
	// While it works, no second order is stacked for the asset.
	if ev := h.evaluate(); ev.SkippedReason != "an order is still working" || ev.Order.ID != o.ID || h.fake.Placed != 1 {
		t.Fatalf("stacked: %+v placed %d", ev, h.fake.Placed)
	}
	h.fake.Set(func(f *binancefake.Fake) { f.FillOnCancel = true })
	view, err := CancelHedgeOrder(h.ctx, o.ID.Hex())
	if err != nil {
		t.Fatal(err)
	}
	if view.Status != models.HedgeOrderStatusFilled || view.FilledQty == 0 {
		t.Fatalf("cancel lost the race but %+v", view)
	}
	if h.position().QtyE8 != h.fake.PositionE8("BTCUSDT") {
		t.Fatal("fills of the raced order not in the ledger")
	}

	// A cancel that wins: CANCELLED with nothing filled, the slot freed.
	h.fake.Set(func(f *binancefake.Fake) { f.FillOnCancel = false })
	h.closeAllTrades()
	o = h.evaluate().Order
	view, err = CancelHedgeOrder(h.ctx, o.ID.Hex())
	if err != nil || view.Status != models.HedgeOrderStatusCancelled || view.FilledQty != 0 || h.order(o.ID).WorkingSlot != nil {
		t.Fatalf("clean cancel %+v %v", view, err)
	}
	if _, err := CancelHedgeOrder(h.ctx, o.ID.Hex()); err == nil {
		t.Fatal("cancelling a final order must conflict")
	}
}

func TestOutageAndStaleRateMakeHedgingUnavailable(t *testing.T) {
	h := newHarness(t)
	h.trade(models.DirectionUp, 1_000_00, 300)
	RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")
	c, err := CostEstimate(h.ctx, h.asset.ID.Hex(), 10_000_000)
	if err != nil || !c.Available || c.Expensive || c.FeeSource != "venue" || c.TakerFeeBps != 5 || c.CostInr <= 0 || c.InrPerUsdt != 88 {
		t.Fatalf("estimate %+v %v", c, err)
	}

	h.fake.Set(func(f *binancefake.Fake) { f.Down = true })
	RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")
	if c, _ = CostEstimate(h.ctx, h.asset.ID.Hex(), 10_000_000); c.Available || c.Reason != ReasonVenueUnavail {
		t.Fatalf("outage %+v", c)
	}
	if ev := h.evaluate(); ev.Order != nil || !strings.HasPrefix(ev.SkippedReason, "venue unavailable") {
		t.Fatalf("placed during an outage: %+v", ev)
	}
	if n := h.count(models.CollHedgeOrders, bson.M{}); n != 0 {
		t.Fatalf("orders %d", n)
	}

	h.fake.Set(func(f *binancefake.Fake) { f.Down = false })
	RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")
	config.E.HedgeINRPerUSDTAsOf = time.Now().Add(-48 * time.Hour).UnixMilli()
	invalidateFxCache()
	if c, _ = CostEstimate(h.ctx, h.asset.ID.Hex(), 10_000_000); c.Available || c.Reason != "FX_STALE" {
		t.Fatalf("stale rate %+v", c)
	}
	if ev := h.evaluate(); ev.Order != nil || ev.SkippedReason != "INR rate unusable: FX_STALE" {
		t.Fatalf("placed on a stale rate: %+v", ev)
	}
	if n := h.count(models.CollHedgeAlerts, bson.M{"code": models.HedgeAlertFxStale, "status": models.AlertOpen}); n != 1 {
		t.Fatalf("fx alert %d", n)
	}
	// An operator enters a fresh rate: hedging resumes, the alert clears.
	if _, err := SetFxRate(h.ctx, SetFxRateInput{Rate: 88.2, Source: "test"}); err != nil {
		t.Fatal(err)
	}
	if ev := h.evaluate(); ev.Order == nil || ev.Order.Decision.InrPerUsdt != 88.2 {
		t.Fatalf("after a fresh rate %+v", ev)
	}
	if n := h.count(models.CollHedgeAlerts, bson.M{"code": models.HedgeAlertFxStale, "status": models.AlertOpen}); n != 0 {
		t.Fatalf("fx alert still open %d", n)
	}

	// Costs above the limit: available but expensive.
	config.E.HedgeMaxCostBps = 10
	if c, _ = CostEstimate(h.ctx, h.asset.ID.Hex(), 10_000_000); !c.Available || !c.Expensive || c.Reason != ReasonCostLimit {
		t.Fatalf("expensive %+v", c)
	}
}

// After a restart the working orders are resolved from the venue: one that
// reached the venue becomes FILLED, one that never left the process FAILS.
func TestRestartRecovery(t *testing.T) {
	h := newHarness(t)
	h.trade(models.DirectionUp, 1_000_00, 300)
	// The previous process persisted the intent, sent it, and died before
	// storing the answer.
	sent := &models.HedgeOrder{ID: bson.NewObjectID(), Reference: "HDG-SENT", AssetID: h.asset.ID, Symbol: h.asset.Symbol,
		Provider: "binance", Venue: h.venue.ID(), VenueSymbol: "BTCUSDT", Side: models.DirectionUp, TargetQtyE8: 1_000_000,
		Status: models.HedgeOrderStatusPending, SendAttempts: 1}
	sent.ClientOrderID, sent.IdempotencyKey = newClientOrderID(sent.ID), newClientOrderID(sent.ID)
	sentAt := mongox.Now()
	sent.SentAt = &sentAt
	sent.Decision.InrPerUsdt, sent.Decision.VenuePrice = 88, 60_000
	slot := h.asset.ID.Hex()
	sent.WorkingSlot = &slot
	if _, err := CreateHedgeOrder(h.ctx, sent); err != nil {
		t.Fatal(err)
	}
	if _, err := h.venue.PlaceOrder(h.ctx, providers.OrderRequest{Symbol: "BTCUSDT", Side: providers.SideBuy, QtyE8: 1_000_000,
		ClientOrderID: sent.ClientOrderID}); err != nil {
		t.Fatal(err)
	}
	// And an intent it never sent, a minute old.
	unsent := &models.HedgeOrder{ID: bson.NewObjectID(), Reference: "HDG-UNSENT", AssetID: bson.NewObjectID(), Symbol: "BTC/USD",
		Provider: "binance", Venue: h.venue.ID(), VenueSymbol: "BTCUSDT", Side: models.DirectionUp, TargetQtyE8: 1_000_000,
		Status: models.HedgeOrderStatusPending}
	unsent.ClientOrderID, unsent.IdempotencyKey = newClientOrderID(unsent.ID), newClientOrderID(unsent.ID)
	if _, err := CreateHedgeOrder(h.ctx, unsent); err != nil {
		t.Fatal(err)
	}
	if _, err := mongox.C(models.CollHedgeOrders).UpdateOne(h.ctx, bson.M{"_id": unsent.ID},
		bson.M{"$set": bson.M{"createdAt": time.Now().Add(-time.Minute)}}); err != nil {
		t.Fatal(err)
	}

	if err := RecoverOnBoot(h.ctx); err != nil {
		t.Fatal(err)
	}
	if o := h.order(sent.ID); o.Status != models.HedgeOrderStatusFilled || o.FillsQtyE8 != 1_000_000 || o.WorkingSlot != nil {
		t.Fatalf("sent order after recovery %+v", o)
	}
	if o := h.order(unsent.ID); o.Status != models.HedgeOrderStatusFailed || !strings.Contains(*o.Error, "never sent") {
		t.Fatalf("unsent order after recovery %+v", o)
	}
	if h.fake.Placed != 1 || h.position().QtyE8 != 1_000_000 {
		t.Fatalf("placed %d position %d", h.fake.Placed, h.position().QtyE8)
	}
}

func TestReconcileDriftRaisesAlert(t *testing.T) {
	h := newHarness(t)
	h.trade(models.DirectionUp, 1_000_00, 300)
	h.evaluate()
	RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")

	h.fake.ShiftPosition("BTCUSDT", 1_000_000) // a manual trade on the account
	for i := 0; i < 2; i++ {
		res, err := ReconcilePositions(h.ctx)
		if err != nil || res[0].Status != models.HedgeReconDrift || res[0].Drift != "0.01" {
			t.Fatalf("drift %+v %v", res, err)
		}
	}
	var a models.HedgeAlert
	if err := mongox.C(models.CollHedgeAlerts).FindOne(h.ctx, bson.M{"code": models.HedgeAlertPositionDrift, "status": models.AlertOpen}).Decode(&a); err != nil {
		t.Fatal(err)
	}
	if a.Occurrences != 2 || a.Severity != models.AlertCritical {
		t.Fatalf("alert %+v", a)
	}
	p := h.position()
	if p.ReconcileStatus != models.HedgeReconDrift || p.DriftE8 != 1_000_000 || p.VenueQtyE8 == nil {
		t.Fatalf("position %+v", p)
	}
	// Nothing is corrected automatically: the ledger keeps its quantity.
	if p.QtyE8 == h.fake.PositionE8("BTCUSDT") {
		t.Fatal("ledger silently followed the venue")
	}
	h.fake.ShiftPosition("BTCUSDT", -1_000_000)
	if res, _ := ReconcilePositions(h.ctx); res[0].Status != models.HedgeReconOK {
		t.Fatalf("after fix %+v", res)
	}
	if n := h.count(models.CollHedgeAlerts, bson.M{"code": models.HedgeAlertPositionDrift, "status": models.AlertOpen}); n != 0 {
		t.Fatal("drift alert not cleared")
	}
}

func TestFundingIsRecordedOnceAndInPnl(t *testing.T) {
	h := newHarness(t)
	h.trade(models.DirectionUp, 1_000_00, 300)
	h.evaluate()
	start := time.Now().Add(-time.Second)
	h.fake.AddFunding("BTCUSDT", "-0.75", time.Now().UnixMilli())
	h.fake.AddFunding("BTCUSDT", "0.25", time.Now().UnixMilli())
	h.fake.Set(func(f *binancefake.Fake) { f.DuplicateTrades = true })
	for i := 0; i < 2; i++ {
		if _, err := ReconcilePositions(h.ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.count(models.CollHedgeFunding, bson.M{"assetId": h.asset.ID}); n != 2 {
		t.Fatalf("funding rows %d", n)
	}
	p := h.position()
	if p.FundingE8 != -50_000_000 || p.FundingInr != -4_400 {
		t.Fatalf("funding on position %+v", p)
	}
	s, err := GetHedgePnl(h.ctx, start, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.Funding != -4_400 || s.Fees != p.FeesInr || s.Fees <= 0 || s.Realised != s.TradingPnl-s.Fees+s.Funding+s.Adjustments || s.Orders != 1 {
		t.Fatalf("pnl %+v", s)
	}
	// A correction is booked separately, never as an edit of a fill.
	if _, err := AddAdjustment(h.ctx, AddAdjustmentInput{AssetID: h.asset.ID, AmountInr: 1_000, Reason: "fee rebate"}); err != nil {
		t.Fatal(err)
	}
	s2, _ := GetHedgePnl(h.ctx, start, time.Now().Add(time.Second))
	if s2.Adjustments != 1_000 || s2.Realised != s.Realised+1_000 {
		t.Fatalf("adjustment %+v", s2)
	}
}

func TestTradeHedgeCostForPricing(t *testing.T) {
	h := newHarness(t)
	h.trade(models.DirectionUp, 1_000_00, 300)
	h.evaluate() // caches the risk state (sigma) and the venue snapshot
	RefreshVenueSnapshot(h.ctx, h.venue, "BTCUSDT")
	est, err := EstimateTradeHedgeCost(h.ctx, TradeCostInput{AssetID: h.asset.ID.Hex(), Direction: models.DirectionUp,
		Stake: 1_000_00, PayoutPercent: 85, NetIncrease: 1_000_00, UtilisationPercent: 50, ExpirySeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if !est.Required || !est.Available || est.HedgeRatio != 1 || est.DeltaNotional <= 0 || est.HedgeNotional != est.DeltaNotional {
		t.Fatalf("estimate %+v", est)
	}
	want := float64(est.HedgeNotional) * est.Cost.RoundTripBps / 10_000 / 1_000_00
	if est.CostPerStake != want || est.PricingHedgeRatio*est.PricingCostFraction != est.CostPerStake {
		t.Fatalf("cost per stake %v want %v", est.CostPerStake, want)
	}
	// A trade that shrinks the book needs no hedge.
	est, _ = EstimateTradeHedgeCost(h.ctx, TradeCostInput{AssetID: h.asset.ID.Hex(), Direction: models.DirectionDown,
		Stake: 1_000_00, NetIncrease: 0, UtilisationPercent: 50, ExpirySeconds: 60})
	if est.Required || est.CostPerStake != 0 {
		t.Fatalf("reducing trade %+v", est)
	}
}
