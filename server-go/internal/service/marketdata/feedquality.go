package marketdata

import (
	"context"
	"math"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/alerts"
	"binarytrading/internal/service/assets"
)

// Feed data rules (audit F-17; docs/audit/operations/feed-rules.md).
//
// The feed leader tracks, per asset, every message the provider sends for it
// - price updates and heartbeats alike - and tells a gap from a quiet market:
//
//   - a heartbeat is a message that repeats the provider's last sequence
//     number (Binance bookTicker update id "u": the provider re-emits an
//     unchanged book while the socket is demonstrably alive); an update
//     carries a new one (or no sequence at all);
//   - the expected interval between messages is learnt per asset (an EWMA);
//     the gap threshold is max(feedGapMinMs, feedGapIntervalMultiple x that
//     interval), so a symbol that normally updates every 100 ms and one that
//     updates every few seconds are judged against their own rate;
//   - nothing at all for longer than the threshold is a GAP (reason SILENCE,
//     or DISCONNECTED when the provider connection is down); heartbeats
//     without updates is QUIET - the provider is alive, the price is not
//     moving - and is not a gap;
//   - a gap that nobody was watching (no feed leader: a restart or a leader
//     change) is found when data resumes (reason NOT_INGESTING).
//
// Each gap is recorded with the exact receipt times of the last message
// before it and the first after it (models.MarketDataGap), and raises an
// alert that clears when data resumes. Safety does not depend on the gap
// detector: without new ticks the feed turns STALE within feedDegradedMaxMs
// and new trades and quotes stop; settlement defers, then voids (policy).
//
// Two conditions make the feed STALE although its last tick is fresh
// (FeedBlockFor): ticks dropped by the saturated ingestion buffer within
// feedSaturationHoldMs - the history then has holes nobody can see - and a
// server clock that drifts from the provider's beyond feedMaxClockDriftMs
// (clock.go).

// Feed quality states.
const (
	FeedStateLive         = "LIVE"
	FeedStateQuiet        = "QUIET"
	FeedStateGap          = "GAP"
	FeedStateDisconnected = "DISCONNECTED"
	FeedStateNoData       = "NO_DATA"
)

// Block reasons (FeedHealthSnapshot.BlockReason).
const (
	BlockTicksDropped = "TICKS_DROPPED"
	BlockClockDrift   = "CLOCK_DRIFT"
)

// Alert codes.
const (
	AlertFeedGap            = "feed.gap"
	AlertFeedTicksDropped   = "feed.ticks_dropped"
	AlertFeedClockDrift     = "feed.clock_drift"
	AlertFeedClockCheck     = "feed.clock_check_failed"
	AlertFeedProviderAhead  = "feed.provider_clock_ahead"
	gapCriticalAfterMs      = 60_000
	dropAlertClearAfterMs   = 60_000
	feedQualityEvalInterval = time.Second
	feedGuardCacheTTL       = 250 * time.Millisecond
	intervalAlpha           = 0.05
)

// feedQualityGlobalKey holds the ingestion buffer's counters (per database).
func feedQualityGlobalKey() string { return "md:feedq:global:" + mongox.DB().Name() }

// QualityKey is the Redis hash of one asset's feed quality, written by the
// feed leader and read by every instance.
func QualityKey(assetID string) string { return "md:feedq:" + assetID }

// GapParams are the gap thresholds.
type GapParams struct {
	MinMs    int64
	Multiple float64
}

// GapParamsFrom reads them from the risk configuration.
func GapParamsFrom(cfg *models.RiskConfig) GapParams {
	p := GapParams{MinMs: 3000, Multiple: 10}
	if cfg != nil && cfg.FeedGapMinMs > 0 {
		p.MinMs = cfg.FeedGapMinMs
	}
	if cfg != nil && cfg.FeedGapIntervalMultiple > 0 {
		p.Multiple = cfg.FeedGapIntervalMultiple
	}
	return p
}

// feedTracker is the pure gap-detection state of one asset.
type feedTracker struct {
	LastSeenAt, LastUpdateAt         int64
	LastSeq                          int64
	HasSeq                           bool
	SeenIntervalMs, UpdateIntervalMs float64
	Seen, Updates, Heartbeats        int64
	SeqRegressions                   int64
	// Provider timestamp ahead of receipt, the largest since the last
	// evaluation (a provider clock running ahead of ours).
	MaxAheadMs int64
	GapOpen    bool
	GapStarted int64
	GapReason  string
	GapFound   int64
	// The last evaluation by this process, and whether the provider was
	// connected then.
	LastEvalAt    int64
	LastConnected bool
}

// gapEvent is a gap opened (EndedAt nil) or closed.
type gapEvent struct {
	Reason             string
	StartedAt          int64
	EndedAt            *int64
	DetectedAt         int64
	ThresholdMs        int64
	ExpectedIntervalMs float64
	// Found only when data resumed (never seen open).
	AfterTheFact bool
}

func ewma(prev, x float64) float64 {
	if prev <= 0 {
		return x
	}
	return prev + intervalAlpha*(x-prev)
}

// threshold is the silence after which the asset is in a gap.
func (t *feedTracker) threshold(p GapParams) int64 {
	thr := p.MinMs
	if x := int64(math.Ceil(p.Multiple * t.SeenIntervalMs)); x > thr {
		thr = x
	}
	return thr
}

// observe records one message received at receivedAt (provider time
// providerTs, sequence seq) and returns the gap it ends, if any: an open
// one, or one nobody saw (the silence exceeded the threshold before any
// evaluation noticed it).
func (t *feedTracker) observe(receivedAt int64, seq *int64, providerTs int64, p GapParams) *gapEvent {
	var closed *gapEvent
	if t.LastSeenAt > 0 {
		interval := receivedAt - t.LastSeenAt
		thr := t.threshold(p)
		end := receivedAt
		switch {
		case t.GapOpen:
			closed = &gapEvent{Reason: t.GapReason, StartedAt: t.GapStarted, EndedAt: &end, DetectedAt: t.GapFound,
				ThresholdMs: thr, ExpectedIntervalMs: t.SeenIntervalMs}
			t.GapOpen, t.GapStarted, t.GapReason, t.GapFound = false, 0, "", 0
		case interval > thr:
			reason := models.GapReasonNotIngesting
			if t.LastEvalAt >= t.LastSeenAt {
				// This process was watching but evaluated just before the
				// threshold was crossed.
				reason = models.GapReasonSilence
				if !t.LastConnected {
					reason = models.GapReasonDisconnected
				}
			}
			closed = &gapEvent{Reason: reason, StartedAt: t.LastSeenAt, EndedAt: &end, DetectedAt: receivedAt,
				ThresholdMs: thr, ExpectedIntervalMs: t.SeenIntervalMs, AfterTheFact: true}
		case interval >= 0:
			// A gap is not part of the normal rhythm: it never feeds the
			// expected interval.
			t.SeenIntervalMs = ewma(t.SeenIntervalMs, float64(interval))
		}
	}
	if ahead := providerTs - receivedAt; ahead > t.MaxAheadMs {
		t.MaxAheadMs = ahead
	}
	switch {
	case seq != nil && t.HasSeq && *seq == t.LastSeq:
		t.Heartbeats++
	case seq != nil && t.HasSeq && *seq < t.LastSeq:
		// Out of order, or the provider reset its numbering.
		t.SeqRegressions++
	default:
		if t.LastUpdateAt > 0 && closed == nil && receivedAt >= t.LastUpdateAt {
			t.UpdateIntervalMs = ewma(t.UpdateIntervalMs, float64(receivedAt-t.LastUpdateAt))
		}
		t.Updates++
		t.LastUpdateAt = max(t.LastUpdateAt, receivedAt)
		if seq != nil {
			t.LastSeq, t.HasSeq = *seq, true
		}
	}
	t.LastSeenAt = max(t.LastSeenAt, receivedAt)
	t.Seen++
	return closed
}

// evaluate classifies the asset at now and opens a gap when the silence
// exceeds the threshold.
func (t *feedTracker) evaluate(now int64, connected bool, p GapParams) (string, *gapEvent) {
	t.LastEvalAt, t.LastConnected = now, connected
	if t.LastSeenAt == 0 {
		if !connected {
			return FeedStateDisconnected, nil
		}
		return FeedStateNoData, nil
	}
	thr := t.threshold(p)
	if now-t.LastSeenAt > thr {
		var opened *gapEvent
		if !t.GapOpen {
			reason := models.GapReasonSilence
			if !connected {
				reason = models.GapReasonDisconnected
			}
			t.GapOpen, t.GapStarted, t.GapReason, t.GapFound = true, t.LastSeenAt, reason, now
			opened = &gapEvent{Reason: reason, StartedAt: t.LastSeenAt, DetectedAt: now, ThresholdMs: thr, ExpectedIntervalMs: t.SeenIntervalMs}
		}
		return FeedStateGap, opened
	}
	if !connected {
		return FeedStateDisconnected, nil
	}
	if now-t.LastUpdateAt > thr {
		return FeedStateQuiet, nil
	}
	return FeedStateLive, nil
}

// ── process state (feed leader) ─────────────────────────────────────────

type trackedAsset struct {
	symbol string
	loaded bool
	t      feedTracker
	// Ticks discarded for an invalid price since the last flush.
	invalid int64
	// Alerts this process has open (raised on the transition only, so a
	// lasting condition writes one alert, not one a second).
	dropAlert, aheadAlert, gapEscalated bool
	lastDropAt                          int64
}

type queuedGap struct {
	assetID, symbol string
	ev              gapEvent
}

var (
	trackMu     sync.Mutex
	trackers    = map[string]*trackedAsset{}
	pendingGaps []queuedGap

	// Ticks the ingestion buffer refused, by provider symbol.
	dropMu         sync.Mutex
	dropPending    = map[string]int64{}
	dropLastAt     = map[string]int64{}
	queueHighWater int
)

// ResetFeedQualityState drops the in-process trackers (tests, lost
// leadership: the next leader reloads them from Redis).
func ResetFeedQualityState() {
	trackMu.Lock()
	trackers, pendingGaps = map[string]*trackedAsset{}, nil
	trackMu.Unlock()
	dropMu.Lock()
	dropPending, dropLastAt, queueHighWater = map[string]int64{}, map[string]int64{}, 0
	dropMu.Unlock()
	guardMu.Lock()
	guardCache = map[string]guardEntry{}
	guardMu.Unlock()
}

// tracked returns the asset's tracker, restored from Redis on first use
// (caller holds trackMu).
func tracked(ctx context.Context, assetID, symbol string) *trackedAsset {
	ta := trackers[assetID]
	if ta == nil {
		ta = &trackedAsset{symbol: symbol}
		trackers[assetID] = ta
	}
	if !ta.loaded {
		ta.loaded = true
		ta.t = loadTracker(ctx, assetID)
	}
	if symbol != "" {
		ta.symbol = symbol
	}
	return ta
}

// observeTicks feeds a batch of normalised ticks (accepted or not: any
// message proves the provider alive) to the trackers.
func observeTicks(ctx context.Context, ticks []*models.NormalisedTick, p GapParams) {
	trackMu.Lock()
	defer trackMu.Unlock()
	for _, n := range ticks {
		ta := tracked(ctx, n.AssetID, n.Symbol)
		pt := n.Timestamp
		if n.ProviderTimestamp != nil {
			pt = *n.ProviderTimestamp
		}
		if ev := ta.t.observe(n.ReceivedAt, n.Sequence, pt, p); ev != nil {
			pendingGaps = append(pendingGaps, queuedGap{assetID: n.AssetID, symbol: n.Symbol, ev: *ev})
		}
	}
}

// recordInvalid counts a tick discarded for an invalid price.
func recordInvalid(assetID, symbol string) {
	trackMu.Lock()
	ta := trackers[assetID]
	if ta == nil {
		ta = &trackedAsset{symbol: symbol}
		trackers[assetID] = ta
	}
	ta.invalid++
	trackMu.Unlock()
}

// recordDrop counts a tick the ingestion buffer refused.
func recordDrop(symbol string, at int64, depth int) {
	dropMu.Lock()
	dropPending[symbol]++
	dropLastAt[symbol] = at
	queueHighWater = max(queueHighWater, depth)
	dropMu.Unlock()
}

// noteQueueDepth keeps the buffer's high-water mark.
func noteQueueDepth(depth int) {
	dropMu.Lock()
	queueHighWater = max(queueHighWater, depth)
	dropMu.Unlock()
}

// flushDrops writes the pending drop counts and returns the assets that
// dropped ticks, with the time of the last drop.
func flushDrops(ctx context.Context) map[string]int64 {
	dropMu.Lock()
	pending, lastAt, hw := dropPending, dropLastAt, queueHighWater
	dropPending, dropLastAt, queueHighWater = map[string]int64{}, map[string]int64{}, 0
	dropMu.Unlock()
	out := map[string]int64{}
	pipe := redisx.R().Pipeline()
	pipe.HSet(ctx, feedQualityGlobalKey(), "queueHighWater", strconv.Itoa(hw), "queueCap", strconv.Itoa(cap(tickQueue)),
		"at", strconv.FormatInt(util.NowMs(), 10))
	var total int64
	for symbol, n := range pending {
		total += n
		asset, err := assets.FindAssetByProviderSymbol(ctx, symbol)
		if err != nil || asset == nil {
			continue
		}
		id := asset.ID.Hex()
		pipe.HIncrBy(ctx, QualityKey(id), "droppedQueueFull", n)
		pipe.HSet(ctx, QualityKey(id), "lastDropAt", strconv.FormatInt(lastAt[symbol], 10))
		out[id] = lastAt[symbol]
	}
	if total > 0 {
		pipe.HIncrBy(ctx, feedQualityGlobalKey(), "droppedTotal", total)
		pipe.HSet(ctx, feedQualityGlobalKey(), "lastDropAt", strconv.FormatInt(util.NowMs(), 10))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		logx.Warn("failed to persist feed drop counters", "err", err.Error())
	}
	if len(out) > 0 {
		invalidateGuard()
	}
	return out
}

// ── persistence ────────────────────────────────────────────────────────

func i64s(v int64) string { return strconv.FormatInt(v, 10) }

func f64s(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

func boolS(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func loadTracker(ctx context.Context, assetID string) feedTracker {
	m, err := redisx.R().HGetAll(ctx, QualityKey(assetID)).Result()
	if err != nil || len(m) == 0 {
		return feedTracker{}
	}
	pi := func(k string) int64 { v, _ := strconv.ParseInt(m[k], 10, 64); return v }
	pf := func(k string) float64 { v, _ := strconv.ParseFloat(m[k], 64); return v }
	return feedTracker{
		LastSeenAt: pi("lastSeenAt"), LastUpdateAt: pi("lastUpdateAt"), LastSeq: pi("lastSeq"), HasSeq: m["hasSeq"] == "1",
		SeenIntervalMs: pf("seenIntervalMs"), UpdateIntervalMs: pf("updateIntervalMs"),
		Seen: pi("seen"), Updates: pi("updates"), Heartbeats: pi("heartbeats"), SeqRegressions: pi("seqRegressions"),
		GapOpen: m["gapOpen"] == "1", GapStarted: pi("gapStartedAt"), GapReason: m["gapReason"], GapFound: pi("gapDetectedAt"),
		// LastEvalAt stays 0: this process has not watched yet.
	}
}

// ── gaps in MongoDB ────────────────────────────────────────────────────

func persistGap(ctx context.Context, assetID, symbol string, ev gapEvent) {
	oid, ok := mongox.ParseID(assetID)
	if !ok {
		return
	}
	now := mongox.Now()
	set := bson.M{"symbol": symbol, "provider": currentProviderName(), "reason": ev.Reason, "thresholdMs": ev.ThresholdMs,
		"expectedIntervalMs": math.Round(ev.ExpectedIntervalMs*1000) / 1000, "open": ev.EndedAt == nil, "updatedAt": now}
	if ev.EndedAt != nil {
		d := *ev.EndedAt - ev.StartedAt
		set["endedAt"], set["durationMs"] = *ev.EndedAt, d
	}
	_, err := mongox.C(models.CollMarketDataGaps).UpdateOne(ctx, bson.M{"assetId": oid, "startedAt": ev.StartedAt},
		bson.M{"$set": set, "$setOnInsert": bson.M{"detectedAt": ev.DetectedAt, "createdAt": now}},
		options.UpdateOne().SetUpsert(true))
	if err != nil && !mongox.IsDuplicateKey(err) {
		logx.Error("failed to record a market data gap", "assetId", assetID, "err", err.Error())
	}
}

func gapAlertKey(assetID string) string { return AlertFeedGap + ":" + assetID }

func describeGap(symbol string, ev gapEvent) string {
	d := "last data at " + time.UnixMilli(ev.StartedAt).UTC().Format(time.RFC3339Nano) + ", threshold " + i64s(ev.ThresholdMs) +
		" ms (expected interval " + f64s(ev.ExpectedIntervalMs) + " ms), reason " + ev.Reason
	if ev.EndedAt != nil {
		d += "; data resumed at " + time.UnixMilli(*ev.EndedAt).UTC().Format(time.RFC3339Nano) + " after " + i64s(*ev.EndedAt-ev.StartedAt) + " ms"
	}
	return symbol + ": " + d
}

func reportGap(ctx context.Context, q queuedGap) {
	persistGap(ctx, q.assetID, q.symbol, q.ev)
	key := gapAlertKey(q.assetID)
	switch {
	case q.ev.EndedAt == nil:
		logx.Warn("market data gap", "symbol", q.symbol, "reason", q.ev.Reason, "since", q.ev.StartedAt)
		alerts.Raise(ctx, alerts.Alert{Code: AlertFeedGap, Severity: models.AlertWarning, Source: models.AlertSourceFeed,
			DedupKey: key, Title: "Market data gap: " + q.symbol, Detail: describeGap(q.symbol, q.ev)})
	case q.ev.AfterTheFact:
		logx.Warn("market data gap found when data resumed", "symbol", q.symbol, "reason", q.ev.Reason,
			"startedAt", q.ev.StartedAt, "endedAt", *q.ev.EndedAt)
		alerts.Raise(ctx, alerts.Alert{Code: AlertFeedGap, Severity: models.AlertSeverityInfo, Source: models.AlertSourceFeed,
			DedupKey: key, Title: "Market data gap (ended): " + q.symbol, Detail: describeGap(q.symbol, q.ev)})
		alerts.ResolveByKey(ctx, key, "data resumed")
	default:
		logx.Info("market data gap ended", "symbol", q.symbol, "durationMs", *q.ev.EndedAt-q.ev.StartedAt)
		alerts.ResolveByKey(ctx, key, "data resumed after "+i64s(*q.ev.EndedAt-q.ev.StartedAt)+" ms")
	}
}

// GapAt is the recorded gap that covers ts on an asset (started before it
// and not ended by it), or nil.
func GapAt(ctx context.Context, assetID string, ts int64) (*models.MarketDataGap, error) {
	oid, ok := mongox.ParseID(assetID)
	if !ok {
		return nil, nil
	}
	var g models.MarketDataGap
	err := mongox.C(models.CollMarketDataGaps).FindOne(ctx, bson.M{
		"assetId": oid, "startedAt": bson.M{"$lte": ts},
		"$or": bson.A{bson.M{"open": true}, bson.M{"endedAt": bson.M{"$gt": ts}}},
	}, options.FindOne().SetSort(bson.D{{Key: "startedAt", Value: -1}})).Decode(&g)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &g, nil
}

// RecentGaps lists the newest gaps (all assets when assetID is "").
func RecentGaps(ctx context.Context, assetID string, limit int64) ([]models.MarketDataGapView, error) {
	filter := bson.M{}
	if oid, ok := mongox.ParseID(assetID); ok {
		filter["assetId"] = oid
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	cur, err := mongox.C(models.CollMarketDataGaps).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "startedAt", Value: -1}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	var rows []models.MarketDataGap
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]models.MarketDataGapView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToMarketDataGapView(&rows[i]))
	}
	return out, nil
}
