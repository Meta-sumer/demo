package marketdata

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/testsupport"
)

// Feed data rules (audit F-17, docs/audit/operations/feed-rules.md).

func seqp(v int64) *int64 { return &v }

// ── pure: the gap tracker ─────────────────────────────────────────────

func TestGapTrackerTellsAGapFromAQuietMarket(t *testing.T) {
	p := GapParams{MinMs: 3000, Multiple: 10}
	var tr feedTracker
	at := int64(1_000_000)
	// A live book: an update (new sequence) every 100 ms.
	for i := int64(0); i < 50; i++ {
		if ev := tr.observe(at, seqp(100+i), at, p); ev != nil {
			t.Fatalf("gap while live: %+v", ev)
		}
		at += 100
	}
	if s, _ := tr.evaluate(at, true, p); s != FeedStateLive || tr.threshold(p) != 3000 {
		t.Fatalf("state %s threshold %d", s, tr.threshold(p))
	}
	lastUpdate := tr.LastUpdateAt

	// The market goes quiet: the provider keeps re-sending the unchanged
	// book (same sequence) every 250 ms for 10 s. Not a gap.
	for i := 0; i < 40; i++ {
		at += 250
		if ev := tr.observe(at, seqp(149), at, p); ev != nil {
			t.Fatalf("quiet market reported as a gap: %+v", ev)
		}
		if s, opened := tr.evaluate(at, true, p); opened != nil || s == FeedStateGap {
			t.Fatalf("quiet market: %s %+v", s, opened)
		}
	}
	if s, _ := tr.evaluate(at, true, p); s != FeedStateQuiet || tr.Heartbeats != 40 || tr.LastUpdateAt != lastUpdate {
		t.Fatalf("state %s heartbeats %d", s, tr.Heartbeats)
	}

	// Nothing at all for longer than the threshold: a gap, from the last
	// message received.
	lastSeen := at
	if s, opened := tr.evaluate(at+2500, true, p); s != FeedStateQuiet || opened != nil {
		t.Fatalf("below the threshold: %s %+v", s, opened)
	}
	s, opened := tr.evaluate(at+3500, true, p)
	if s != FeedStateGap || opened == nil || opened.StartedAt != lastSeen || opened.Reason != models.GapReasonSilence ||
		opened.EndedAt != nil || opened.DetectedAt != at+3500 {
		t.Fatalf("gap %s %+v", s, opened)
	}
	// Opened once.
	if _, again := tr.evaluate(at+4500, false, p); again != nil {
		t.Fatalf("gap opened twice: %+v", again)
	}
	// Data resumes: the gap closes at the first message after it.
	resume := at + 9000
	closed := tr.observe(resume, seqp(150), resume, p)
	if closed == nil || closed.StartedAt != lastSeen || closed.EndedAt == nil || *closed.EndedAt != resume ||
		closed.AfterTheFact || closed.Reason != models.GapReasonSilence {
		t.Fatalf("closed %+v", closed)
	}
	if s, _ := tr.evaluate(resume+10, true, p); s != FeedStateLive {
		t.Fatalf("after resume %s", s)
	}
	// The gap is not part of the rhythm: the threshold is unchanged.
	if tr.threshold(p) != 3000 {
		t.Fatalf("threshold inflated by the gap: %d", tr.threshold(p))
	}
}

func TestGapReasonsDisconnectAndRestart(t *testing.T) {
	p := GapParams{MinMs: 1000, Multiple: 10}
	var tr feedTracker
	at := int64(5_000_000)
	for i := int64(0); i < 10; i++ {
		tr.observe(at, nil, at, p) // no sequence: every message is an update
		at += 50
	}
	if tr.Updates != 10 || tr.Heartbeats != 0 {
		t.Fatalf("updates %d heartbeats %d", tr.Updates, tr.Heartbeats)
	}
	// Connection lost: DISCONNECTED until the silence crosses the
	// threshold, then a gap with that reason.
	if s, opened := tr.evaluate(at+200, false, p); s != FeedStateDisconnected || opened != nil {
		t.Fatalf("%s %+v", s, opened)
	}
	_, opened := tr.evaluate(at+1500, false, p)
	if opened == nil || opened.Reason != models.GapReasonDisconnected {
		t.Fatalf("disconnect gap %+v", opened)
	}
	tr.observe(at+3000, nil, at+3000, p)

	// A new feed leader restored this state from Redis (LastEvalAt 0) and
	// the first message comes 20 s after the last one: a gap nobody saw.
	restored := tr
	restored.LastEvalAt = 0
	ev := restored.observe(at+23_000, nil, at+23_000, p)
	if ev == nil || !ev.AfterTheFact || ev.Reason != models.GapReasonNotIngesting || ev.StartedAt != at+3000 || *ev.EndedAt != at+23_000 {
		t.Fatalf("restart gap %+v", ev)
	}
	// Sequence regressions are counted (out of order, or a reset).
	var sq feedTracker
	sq.observe(1, seqp(10), 1, p)
	sq.observe(2, seqp(9), 2, p)
	if sq.SeqRegressions != 1 || sq.LastSeq != 10 {
		t.Fatalf("regressions %d last %d", sq.SeqRegressions, sq.LastSeq)
	}
}

func TestGapThresholdFollowsTheExpectedRate(t *testing.T) {
	p := GapParams{MinMs: 3000, Multiple: 10}
	var slow feedTracker
	at := int64(0)
	for i := 0; i < 60; i++ {
		at += 2000 // a symbol that updates every 2 s
		slow.observe(at, nil, at, p)
	}
	thr := slow.threshold(p)
	if thr < 19_000 || thr > 21_000 {
		t.Fatalf("threshold %d, want about 20 s", thr)
	}
	// 5 s without data is normal for it ...
	if s, opened := slow.evaluate(at+5000, true, p); s == FeedStateGap || opened != nil {
		t.Fatalf("%s %+v", s, opened)
	}
	// ... 25 s is not.
	if s, _ := slow.evaluate(at+25_000, true, p); s != FeedStateGap {
		t.Fatalf("%s", s)
	}
}

func TestClockVerdict(t *testing.T) {
	// The provider is 40 ms ahead, the round trip 100 ms: within what the
	// round trip explains.
	if v := ClockVerdict(1000, 1090, 1100, 1000); v.State != ClockOK || v.OffsetMs != 40 || v.RttMs != 100 || v.DriftMs != 0 {
		t.Fatalf("%+v", v)
	}
	// 3 s behind, 100 ms round trip: drift 2,950 ms > 1,000 ms.
	if v := ClockVerdict(1000, -1950, 1100, 1000); v.State != ClockDrift || v.DriftMs != 2950 {
		t.Fatalf("%+v", v)
	}
}

// ── integration (MongoDB + Redis) ─────────────────────────────────────

type feedEnv struct {
	t     *testing.T
	ctx   context.Context
	asset models.Asset
	id    string
}

func newFeedEnv(t *testing.T, cfgMods func(*models.RiskConfig)) *feedEnv {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	ResetNormalizationState()
	ResetFeedQualityState()
	t.Cleanup(ResetNormalizationState)
	t.Cleanup(ResetFeedQualityState)

	cfg := models.DefaultRiskConfig()
	// Generous bands: the machine's speed is not under test.
	cfg.FeedHealthyMaxMs, cfg.FeedDegradedMaxMs, cfg.FeedTickMaxAgeMs = 5000, 10_000, 20_000
	if cfgMods != nil {
		cfgMods(&cfg)
	}
	cfg.CreatedAt, cfg.UpdatedAt = mongox.Now(), mongox.Now()
	if _, err := mongox.C(models.CollRiskConfigs).InsertOne(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	riskconfig.InvalidateRiskConfigCache()
	t.Cleanup(riskconfig.InvalidateRiskConfigCache)

	asset := models.Asset{ID: bson.NewObjectID(), Symbol: "TEST/FEED", Name: "Test", Category: models.AssetCategoryCrypto,
		Active: true, ProviderSymbol: "feed" + bson.NewObjectID().Hex(), Provider: "binance", PricePrecision: 2}
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, asset); err != nil {
		t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	t.Cleanup(assets.InvalidateAssetCache)
	id := asset.ID.Hex()
	t.Cleanup(func() {
		redisx.R().Del(context.Background(), config.RedisKeys.LastTick(id), config.RedisKeys.TickHistory(id), volKey(id),
			QuoteRejectKey(id), QualityKey(id), clockKey(), feedQualityGlobalKey())
	})
	return &feedEnv{t: t, ctx: ctx, asset: asset, id: id}
}

// ingest feeds one quote received at at (provider timestamp at, sequence
// seq) through the real ingestion path.
func (e *feedEnv) ingest(price float64, at int64, seq *int64) []*models.NormalisedTick {
	e.t.Helper()
	bid, ask := price-0.005, price+0.005
	ts := at
	return ingestTicks(e.ctx, []queuedTick{{tick: providers.Tick{Symbol: e.asset.ProviderSymbol, Price: price, Bid: &bid, Ask: &ask,
		ProviderTimestamp: &ts, Sequence: seq}, receivedAt: at}})
}

func (e *feedEnv) tradable() error {
	_, err := AssertFeedTradable(e.ctx, e.id, e.asset.Symbol)
	return err
}

func (e *feedEnv) count(coll string, filter bson.M) int64 {
	e.t.Helper()
	n, err := mongox.C(coll).CountDocuments(e.ctx, filter)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *feedEnv) openAlert(key string) bool {
	return e.count(models.CollAlerts, bson.M{"dedupKey": key, "status": models.AlertOpen}) == 1
}

// Buffer saturation: a dropped tick is counted per asset, raises an alert,
// and makes the feed STALE (new trades refused) for feedSaturationHoldMs
// although the last tick is fresh.
func TestBufferSaturationIsCountedAndRefusesTrading(t *testing.T) {
	e := newFeedEnv(t, func(c *models.RiskConfig) { c.FeedSaturationHoldMs = 2000 })
	now := util.NowMs()
	e.ingest(100, now, seqp(1))
	if err := e.tradable(); err != nil {
		t.Fatalf("fresh feed refused: %v", err)
	}

	// A full buffer: the provider's reader never blocks, the tick is dropped.
	q := make(chan queuedTick, 1)
	enqueueInto(q, providers.Tick{Symbol: e.asset.ProviderSymbol, Price: 100})
	enqueueInto(q, providers.Tick{Symbol: e.asset.ProviderSymbol, Price: 100.01})
	enqueueInto(q, providers.Tick{Symbol: e.asset.ProviderSymbol, Price: 100.02})
	if len(q) != 1 {
		t.Fatalf("queue %d", len(q))
	}
	if err := EvaluateFeeds(e.ctx, util.NowMs(), true); err != nil {
		t.Fatal(err)
	}
	m := redisx.R().HGetAll(e.ctx, QualityKey(e.id)).Val()
	if m["droppedQueueFull"] != "2" || m["lastDropAt"] == "" {
		t.Fatalf("drop counters %v", m)
	}
	if !e.openAlert(AlertFeedTicksDropped + ":" + e.id) {
		t.Fatal("no drop alert")
	}
	h, err := GetFeedHealth(e.ctx, e.id, e.asset.Symbol)
	if err != nil || h.Status != models.FeedStatusStale || h.BlockReason == "" {
		t.Fatalf("health %+v %v", h, err)
	}
	err = e.tradable()
	if !apperr.HasCode(err, apperr.FeedUnhealthy) {
		t.Fatalf("trade during saturation: %v", err)
	}
	// Even with the stale-feed halt switched off, the block holds.
	riskconfig.InvalidateRiskConfigCache()
	if _, err := mongox.C(models.CollRiskConfigs).UpdateOne(e.ctx, bson.M{"active": true},
		bson.M{"$set": bson.M{"haltTradingOnStaleFeed": false}}); err != nil {
		t.Fatal(err)
	}
	riskconfig.InvalidateRiskConfigCache()
	invalidateGuard()
	if err := e.tradable(); !apperr.HasCode(err, apperr.FeedUnhealthy) {
		t.Fatalf("block ignored with the halt switch off: %v", err)
	}

	// After the hold, a fresh tick trades again.
	time.Sleep(2100 * time.Millisecond)
	invalidateGuard()
	e.ingest(100.03, util.NowMs(), seqp(2))
	if err := e.tradable(); err != nil {
		t.Fatalf("after the hold: %v", err)
	}
}

// A disconnect: the gap is recorded with the receipt times of the last
// message before it and the first after it, an alert is open while it
// lasts, and trading is refused because the feed goes stale.
func TestDisconnectGapIsRecordedExactlyAndTradingStops(t *testing.T) {
	e := newFeedEnv(t, func(c *models.RiskConfig) {
		c.FeedGapMinMs = 1000
		c.FeedHealthyMaxMs, c.FeedDegradedMaxMs, c.FeedTickMaxAgeMs = 200, 400, 5000
	})
	base := util.NowMs() - 10_000
	for i := int64(0); i < 20; i++ {
		e.ingest(100+float64(i)*0.01, base+i*100, seqp(1000+i))
	}
	last := base + 19*100
	if err := EvaluateFeeds(e.ctx, last+50, true); err != nil {
		t.Fatal(err)
	}
	if e.count(models.CollMarketDataGaps, bson.M{"assetId": e.asset.ID}) != 0 {
		t.Fatal("gap while live")
	}

	// The connection drops; 2 s later the evaluator sees the gap.
	if err := EvaluateFeeds(e.ctx, last+2000, false); err != nil {
		t.Fatal(err)
	}
	var g models.MarketDataGap
	if err := mongox.C(models.CollMarketDataGaps).FindOne(e.ctx, bson.M{"assetId": e.asset.ID}).Decode(&g); err != nil {
		t.Fatal(err)
	}
	if !g.Open || g.StartedAt != last || g.Reason != models.GapReasonDisconnected || g.EndedAt != nil || g.DetectedAt != last+2000 {
		t.Fatalf("open gap %+v", g)
	}
	if !e.openAlert(gapAlertKey(e.id)) {
		t.Fatal("no gap alert")
	}
	if q := redisx.R().HGet(e.ctx, QualityKey(e.id), "state").Val(); q != FeedStateGap {
		t.Fatalf("state %q", q)
	}
	// No new data: the feed is stale (the last tick is 10 s old), trading
	// is refused; a settlement lookup inside the gap finds it.
	if err := e.tradable(); !apperr.HasCode(err, apperr.FeedUnhealthy) {
		t.Fatalf("trade during the gap: %v", err)
	}
	if found, err := GapAt(e.ctx, e.id, last+500); err != nil || found == nil || found.ID != g.ID {
		t.Fatalf("GapAt %v %v", found, err)
	}
	if found, _ := GapAt(e.ctx, e.id, last-500); found != nil {
		t.Fatal("GapAt before the gap")
	}

	// Data resumes.
	resume := util.NowMs()
	e.ingest(100.5, resume, seqp(2000))
	if err := EvaluateFeeds(e.ctx, resume+10, true); err != nil {
		t.Fatal(err)
	}
	if err := mongox.C(models.CollMarketDataGaps).FindOne(e.ctx, bson.M{"_id": g.ID}).Decode(&g); err != nil {
		t.Fatal(err)
	}
	if g.Open || g.EndedAt == nil || *g.EndedAt != resume || g.DurationMs == nil || *g.DurationMs != resume-last {
		t.Fatalf("closed gap %+v", g)
	}
	if e.openAlert(gapAlertKey(e.id)) {
		t.Fatal("gap alert still open")
	}
	if found, _ := GapAt(e.ctx, e.id, resume+1); found != nil {
		t.Fatal("GapAt after the gap")
	}
	if err := e.tradable(); err != nil {
		t.Fatalf("after resume: %v", err)
	}
	if n := e.count(models.CollMarketDataGaps, bson.M{"assetId": e.asset.ID}); n != 1 {
		t.Fatalf("%d gap rows", n)
	}
}

// A feed leader change: the new leader restores the state from Redis and
// records the stretch nobody ingested when data resumes.
func TestRestartGapFoundWhenDataResumes(t *testing.T) {
	e := newFeedEnv(t, func(c *models.RiskConfig) { c.FeedGapMinMs = 1000 })
	base := util.NowMs() - 30_000
	for i := int64(0); i < 5; i++ {
		e.ingest(100, base+i*100, seqp(i+1))
	}
	if err := EvaluateFeeds(e.ctx, base+450, true); err != nil {
		t.Fatal(err)
	}
	ResetFeedQualityState() // the process dies
	resume := util.NowMs()
	e.ingest(100.1, resume, seqp(10))
	if err := EvaluateFeeds(e.ctx, resume+5, true); err != nil {
		t.Fatal(err)
	}
	var g models.MarketDataGap
	if err := mongox.C(models.CollMarketDataGaps).FindOne(e.ctx, bson.M{"assetId": e.asset.ID}).Decode(&g); err != nil {
		t.Fatal(err)
	}
	if g.Open || g.Reason != models.GapReasonNotIngesting || g.StartedAt != base+400 || *g.EndedAt != resume {
		t.Fatalf("gap %+v", g)
	}
	if e.openAlert(gapAlertKey(e.id)) {
		t.Fatal("an after-the-fact gap leaves no open alert")
	}
	if n := e.count(models.CollAlerts, bson.M{"dedupKey": gapAlertKey(e.id)}); n != 1 {
		t.Fatalf("%d gap alerts recorded", n)
	}
}

type fakeClock struct {
	offset int64
	err    error
}

func (f *fakeClock) ServerTime(context.Context) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	return util.NowMs() + f.offset, nil
}

// Clock drift beyond feedMaxClockDriftMs refuses trading and raises a
// critical alert; agreement clears both; a failed check keeps the verdict.
func TestClockDriftRefusesTradingAndClears(t *testing.T) {
	e := newFeedEnv(t, nil)
	e.ingest(100, util.NowMs(), seqp(1))
	cfg, err := riskconfig.GetRiskConfig(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	clock := &fakeClock{offset: 5000}
	if s := CheckClock(e.ctx, clock, "fake", cfg); s.State != ClockDrift || s.DriftMs < 4000 {
		t.Fatalf("drift %+v", s)
	}
	if !e.openAlert(AlertFeedClockDrift) {
		t.Fatal("no drift alert")
	}
	if err := e.tradable(); !apperr.HasCode(err, apperr.FeedUnhealthy) {
		t.Fatalf("trade with a drifting clock: %v", err)
	}
	// The time source fails: the drift verdict is kept (the clock is not
	// known to be right).
	clock.err = errors.New("timeout")
	if s := CheckClock(e.ctx, clock, "fake", cfg); s.State != ClockDrift || s.Error == "" {
		t.Fatalf("failed check %+v", s)
	}
	if err := e.tradable(); !apperr.HasCode(err, apperr.FeedUnhealthy) {
		t.Fatalf("failed check released the block: %v", err)
	}
	clock.err, clock.offset = nil, 3
	if s := CheckClock(e.ctx, clock, "fake", cfg); s.State != ClockOK {
		t.Fatalf("agreement %+v", s)
	}
	if e.openAlert(AlertFeedClockDrift) || e.openAlert(AlertFeedClockCheck) {
		t.Fatal("alerts not cleared")
	}
	e.ingest(100.01, util.NowMs(), seqp(2))
	if err := e.tradable(); err != nil {
		t.Fatalf("after agreement: %v", err)
	}
	// A provider without a clock: reported, never blocking.
	if s := CheckClock(e.ctx, nil, "simulated", cfg); s.State != ClockNotSupported {
		t.Fatalf("no source %+v", s)
	}
	if err := e.tradable(); err != nil {
		t.Fatalf("not supported blocked: %v", err)
	}
}

// Out-of-order ticks never reach market state, are counted, and a
// sequence going backwards is reported.
func TestOutOfOrderTicksAreRejectedAndCounted(t *testing.T) {
	e := newFeedEnv(t, nil)
	now := util.NowMs()
	e.ingest(100, now-2000, seqp(10))
	e.ingest(100.01, now-1000, seqp(11))
	got := e.ingest(99, now-1500, seqp(9)) // older timestamp, older sequence
	if len(got) != 0 {
		t.Fatalf("out-of-order tick accepted: %+v", got)
	}
	if p := redisx.R().HGet(e.ctx, config.RedisKeys.LastTick(e.id), "price").Val(); p != "100.01" {
		t.Fatalf("last tick %s", p)
	}
	if n := redisx.R().ZCard(e.ctx, config.RedisKeys.TickHistory(e.id)).Val(); n != 2 {
		t.Fatalf("history %d", n)
	}
	stats, _ := QuoteRejectionStats(e.ctx, e.id)
	if stats[RejectOutOfOrder] != "1" {
		t.Fatalf("rejections %v", stats)
	}
	if err := EvaluateFeeds(e.ctx, now, true); err != nil {
		t.Fatal(err)
	}
	report, err := GetFeedQuality(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range report.Assets {
		if a.AssetID == e.id {
			if a.SeqRegressions != 1 || a.Seen != 3 || a.Rejected[RejectOutOfOrder] != 1 || a.State == "" {
				t.Fatalf("quality %+v", a)
			}
			return
		}
	}
	t.Fatal("asset missing from the quality report")
}

// The tick history keeps feedTickHistoryRetentionMs of ticks.
func TestTickHistoryRetentionIsConfigurable(t *testing.T) {
	const retention = 400_000 // above the 362 s minimum of the default policy
	e := newFeedEnv(t, func(c *models.RiskConfig) { c.FeedTickHistoryRetentionMs = retention })
	now := util.NowMs()
	e.ingest(100, now-retention-60_000, seqp(1)) // older than the retention
	e.ingest(100.01, now-retention+60_000, seqp(2))
	e.ingest(100.02, now, seqp(3))
	if n := redisx.R().ZCard(e.ctx, config.RedisKeys.TickHistory(e.id)).Val(); n != 2 {
		t.Fatalf("history %d, want 2 (the tick beyond the retention trimmed)", n)
	}
}

// The default retention (15 min) keeps the same old tick.
func TestTickHistoryDefaultRetention(t *testing.T) {
	e := newFeedEnv(t, nil)
	now := util.NowMs()
	e.ingest(100, now-460_000, seqp(1))
	e.ingest(100.01, now, seqp(2))
	if n := redisx.R().ZCard(e.ctx, config.RedisKeys.TickHistory(e.id)).Val(); n != 2 {
		t.Fatalf("default history %d", n)
	}
}
