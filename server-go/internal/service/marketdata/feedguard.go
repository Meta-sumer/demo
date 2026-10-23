package marketdata

import (
	"context"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/alerts"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/service/riskconfig"
)

// ── feed guard ─────────────────────────────────────────────────────────

// FeedBlock is why an asset's feed is treated as STALE although its last
// tick is fresh.
type FeedBlock struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// String is "CODE: detail".
func (b *FeedBlock) String() string {
	if b == nil {
		return ""
	}
	return b.Code + ": " + b.Detail
}

type guardEntry struct {
	at    time.Time
	block *FeedBlock
}

var (
	guardMu    sync.Mutex
	guardCache = map[string]guardEntry{}
)

func invalidateGuard() {
	guardMu.Lock()
	guardCache = map[string]guardEntry{}
	guardMu.Unlock()
}

// FeedBlockFor reports whether the feed data rules block new trades on an
// asset although its last tick is fresh: ticks were dropped by the saturated
// ingestion buffer within feedSaturationHoldMs, or the server clock drifts
// from the provider's beyond feedMaxClockDriftMs. nil = nothing blocks.
// Cached for 250 ms per asset (it runs on every quote and trade).
func FeedBlockFor(ctx context.Context, assetID string) (*FeedBlock, error) {
	guardMu.Lock()
	if e, ok := guardCache[assetID]; ok && time.Since(e.at) < feedGuardCacheTTL {
		guardMu.Unlock()
		return e.block, nil
	}
	guardMu.Unlock()

	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	pipe := redisx.R().Pipeline()
	dropCmd := pipe.HGet(ctx, QualityKey(assetID), "lastDropAt")
	clockCmd := pipe.HGetAll(ctx, clockKey())
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		return nil, err
	}
	now := util.NowMs()
	var block *FeedBlock
	lastDrop, _ := strconv.ParseInt(dropCmd.Val(), 10, 64)
	clock := clockFrom(clockCmd.Val())
	switch {
	case cfg.FeedSaturationHoldMs > 0 && lastDrop > 0 && now-lastDrop < cfg.FeedSaturationHoldMs:
		block = &FeedBlock{Code: BlockTicksDropped, Detail: "ticks were dropped by the saturated ingestion buffer " +
			strconv.FormatInt(now-lastDrop, 10) + " ms ago (hold " + strconv.FormatInt(cfg.FeedSaturationHoldMs, 10) + " ms)"}
	case clock != nil && clock.State == ClockDrift:
		block = &FeedBlock{Code: BlockClockDrift, Detail: "the server clock differs from the provider's by " +
			strconv.FormatInt(clock.DriftMs, 10) + " ms (limit " + strconv.FormatInt(clock.MaxDriftMs, 10) + " ms)"}
	}
	guardMu.Lock()
	guardCache[assetID] = guardEntry{at: time.Now(), block: block}
	guardMu.Unlock()
	return block, nil
}

// ── clock-sync check ───────────────────────────────────────────────────

// Clock check states.
const (
	ClockOK           = "OK"
	ClockDrift        = "DRIFT"
	ClockError        = "ERROR"
	ClockNotSupported = "NOT_SUPPORTED"
)

// clockKey holds the last clock check (per database, so test databases
// sharing a Redis never see each other's verdict).
func clockKey() string { return "md:clock:" + mongox.DB().Name() }

// ClockState is the last clock-sync check: the provider's clock against the
// server's.
type ClockState struct {
	State  string `json:"state"`
	Source string `json:"source"`
	// Provider time minus the server time at the midpoint of the request.
	OffsetMs int64 `json:"offsetMs"`
	RttMs    int64 `json:"rttMs"`
	// |offset| beyond what the round trip can explain (half of it).
	DriftMs    int64  `json:"driftMs"`
	MaxDriftMs int64  `json:"maxDriftMs"`
	CheckedAt  int64  `json:"checkedAt"`
	Error      string `json:"error,omitempty"`
}

// ClockVerdict is the pure measurement: the request left at t0 and returned
// at t1 (server clock), the provider said server.
func ClockVerdict(t0, server, t1, maxDriftMs int64) ClockState {
	rtt := max(0, t1-t0)
	offset := server - (t0+t1)/2
	abs := offset
	if abs < 0 {
		abs = -abs
	}
	drift := max(0, abs-rtt/2)
	s := ClockState{State: ClockOK, OffsetMs: offset, RttMs: rtt, DriftMs: drift, MaxDriftMs: maxDriftMs, CheckedAt: t1}
	if drift > maxDriftMs {
		s.State = ClockDrift
	}
	return s
}

func clockFrom(m map[string]string) *ClockState {
	if len(m) == 0 {
		return nil
	}
	pi := func(k string) int64 { v, _ := strconv.ParseInt(m[k], 10, 64); return v }
	return &ClockState{State: m["state"], Source: m["source"], OffsetMs: pi("offsetMs"), RttMs: pi("rttMs"), DriftMs: pi("driftMs"),
		MaxDriftMs: pi("maxDriftMs"), CheckedAt: pi("checkedAt"), Error: m["error"]}
}

// GetClockState is the last clock-sync check (nil when none is recorded).
func GetClockState(ctx context.Context) (*ClockState, error) {
	m, err := redisx.R().HGetAll(ctx, clockKey()).Result()
	if err != nil {
		return nil, err
	}
	return clockFrom(m), nil
}

// CheckClock runs one clock-sync check against src and records it. The
// record expires after three intervals, so a verdict nobody refreshes (no
// feed leader) stops blocking. A failed check keeps a DRIFT verdict (the
// clock is not known to be right again) and never blocks by itself.
func CheckClock(ctx context.Context, src providers.ClockSource, source string, cfg *models.RiskConfig) ClockState {
	maxDrift := cfg.FeedMaxClockDriftMs
	if maxDrift <= 0 {
		maxDrift = 1000
	}
	var s ClockState
	if src == nil {
		s = ClockState{State: ClockNotSupported, MaxDriftMs: maxDrift, CheckedAt: util.NowMs()}
	} else {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		t0 := util.NowMs()
		server, err := src.ServerTime(cctx)
		t1 := util.NowMs()
		cancel()
		if err != nil {
			s = ClockState{State: ClockError, MaxDriftMs: maxDrift, CheckedAt: t1, Error: err.Error()}
			if prev, _ := GetClockState(ctx); prev != nil && prev.State == ClockDrift {
				s.State, s.OffsetMs, s.RttMs, s.DriftMs = ClockDrift, prev.OffsetMs, prev.RttMs, prev.DriftMs
			}
		} else {
			s = ClockVerdict(t0, server, t1, maxDrift)
		}
	}
	s.Source = source
	ttl := 3 * time.Duration(max(cfg.FeedClockCheckIntervalMs, 60_000)) * time.Millisecond
	pipe := redisx.R().Pipeline()
	pipe.Del(ctx, clockKey())
	pipe.HSet(ctx, clockKey(), "state", s.State, "source", s.Source, "offsetMs", i64s(s.OffsetMs), "rttMs", i64s(s.RttMs),
		"driftMs", i64s(s.DriftMs), "maxDriftMs", i64s(s.MaxDriftMs), "checkedAt", i64s(s.CheckedAt), "error", s.Error)
	pipe.PExpire(ctx, clockKey(), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		logx.Warn("failed to record the clock check", "err", err.Error())
	}
	invalidateGuard()

	switch s.State {
	case ClockDrift:
		alerts.Raise(ctx, alerts.Alert{Code: AlertFeedClockDrift, Severity: models.AlertCritical, Source: models.AlertSourceFeed,
			Title: "Server clock drifts from the market data provider", Detail: "drift " + i64s(s.DriftMs) + " ms (offset " +
				i64s(s.OffsetMs) + " ms, round trip " + i64s(s.RttMs) + " ms, limit " + i64s(s.MaxDriftMs) + " ms, source " + source +
				"); new trades are refused until the clocks agree. Check NTP on the host."})
	case ClockOK:
		alerts.ResolveByKey(ctx, AlertFeedClockDrift, "clocks agree again (offset "+i64s(s.OffsetMs)+" ms)")
		alerts.ResolveByKey(ctx, AlertFeedClockCheck, "clock check succeeded")
	}
	if s.State == ClockError || (s.State == ClockDrift && s.Error != "") {
		alerts.Raise(ctx, alerts.Alert{Code: AlertFeedClockCheck, Severity: models.AlertWarning, Source: models.AlertSourceFeed,
			Title: "Clock-sync check failed", Detail: source + ": " + s.Error})
	}
	return s
}

// ── evaluator (feed leader) ────────────────────────────────────────────

var (
	qualityOnce    sync.Once
	lastClockCheck time.Time
	wasLeader      bool
	// feedLeader mirrors marketLoop's leadership (set in onAcquire and
	// onRelease; reading marketLoop here would be an initialisation cycle).
	feedLeader atomic.Bool
)

// startFeedQuality runs the evaluator once a second on this process; it
// acts only while this process is the feed leader.
func startFeedQuality() {
	qualityOnce.Do(func() {
		go func() {
			t := time.NewTicker(feedQualityEvalInterval)
			defer t.Stop()
			for range t.C {
				if !feedLeader.Load() {
					if wasLeader {
						wasLeader = false
						ResetFeedQualityState()
					}
					continue
				}
				wasLeader = true
				connected := false
				var src providers.ClockSource
				name := ""
				if p, err := providers.GetMarketDataProvider(); err == nil {
					connected, name = p.IsConnected(), p.Name()
					src, _ = p.(providers.ClockSource)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if err := EvaluateFeeds(ctx, util.NowMs(), connected); err != nil {
					logx.Warn("feed quality evaluation failed", "err", err.Error())
				}
				maybeCheckClock(ctx, src, name)
				cancel()
			}
		}()
	})
}

func maybeCheckClock(ctx context.Context, src providers.ClockSource, name string) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil || cfg.FeedClockCheckIntervalMs <= 0 {
		return
	}
	if time.Since(lastClockCheck) < time.Duration(cfg.FeedClockCheckIntervalMs)*time.Millisecond {
		return
	}
	lastClockCheck = time.Now()
	CheckClock(ctx, src, name, cfg)
}

type qualitySnap struct {
	assetID, symbol, state string
	thr                    int64
	t                      feedTracker
	invalid                int64
	resolveDrop            bool
	raiseAhead             bool
	resolveAhead           bool
	escalateGap            bool
}

// EvaluateFeeds classifies every tracked and subscribed asset at now,
// records the state in Redis, the gaps in MongoDB and raises or clears the
// feed alerts. Run by the feed leader every second; exported for tests.
func EvaluateFeeds(ctx context.Context, now int64, connected bool) error {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return err
	}
	p := GapParamsFrom(cfg)
	drops := flushDrops(ctx)

	workerMu.Lock()
	subs := append([]string(nil), subscribed...)
	workerMu.Unlock()
	subAssets := map[string]string{}
	for _, s := range subs {
		if a, err := assets.FindAssetByProviderSymbol(ctx, s); err == nil && a != nil {
			subAssets[a.ID.Hex()] = a.Symbol
		}
	}

	dropClear := max(cfg.FeedSaturationHoldMs, dropAlertClearAfterMs)
	var snaps []qualitySnap
	trackMu.Lock()
	for id, sym := range subAssets {
		tracked(ctx, id, sym)
	}
	raiseDrop := map[string]string{}
	for id, at := range drops {
		ta := tracked(ctx, id, subAssets[id])
		if !ta.dropAlert {
			raiseDrop[id] = ta.symbol
		}
		ta.lastDropAt, ta.dropAlert = at, true
	}
	for id, ta := range trackers {
		if !ta.loaded {
			// Known only from an invalid tick; load before judging it.
			tracked(ctx, id, ta.symbol)
		}
		state, opened := ta.t.evaluate(now, connected, p)
		if opened != nil {
			pendingGaps = append(pendingGaps, queuedGap{assetID: id, symbol: ta.symbol, ev: *opened})
		}
		s := qualitySnap{assetID: id, symbol: ta.symbol, state: state, thr: ta.t.threshold(p), t: ta.t, invalid: ta.invalid}
		ta.invalid = 0
		ta.t.MaxAheadMs = 0
		if ta.dropAlert && now-ta.lastDropAt > dropClear {
			ta.dropAlert, s.resolveDrop = false, true
		}
		switch ahead := s.t.MaxAheadMs > cfg.FeedMaxClockDriftMs; {
		case ahead && !ta.aheadAlert:
			ta.aheadAlert, s.raiseAhead = true, true
		case !ahead && ta.aheadAlert:
			ta.aheadAlert, s.resolveAhead = false, true
		}
		switch {
		case ta.t.GapOpen && now-ta.t.GapStarted > gapCriticalAfterMs && !ta.gapEscalated:
			ta.gapEscalated, s.escalateGap = true, true
		case !ta.t.GapOpen:
			ta.gapEscalated = false
		}
		snaps = append(snaps, s)
	}
	gaps := pendingGaps
	pendingGaps = nil
	trackMu.Unlock()

	pipe := redisx.R().Pipeline()
	for _, s := range snaps {
		t := s.t
		key := QualityKey(s.assetID)
		pipe.HSet(ctx, key,
			"assetId", s.assetID, "symbol", s.symbol, "state", s.state, "evaluatedAt", i64s(now), "connected", boolS(connected),
			"lastSeenAt", i64s(t.LastSeenAt), "lastUpdateAt", i64s(t.LastUpdateAt), "lastSeq", i64s(t.LastSeq), "hasSeq", boolS(t.HasSeq),
			"seenIntervalMs", f64s(t.SeenIntervalMs), "updateIntervalMs", f64s(t.UpdateIntervalMs), "gapThresholdMs", i64s(s.thr),
			"seen", i64s(t.Seen), "updates", i64s(t.Updates), "heartbeats", i64s(t.Heartbeats), "seqRegressions", i64s(t.SeqRegressions),
			"maxAheadMs", i64s(t.MaxAheadMs), "gapOpen", boolS(t.GapOpen), "gapStartedAt", i64s(t.GapStarted),
			"gapReason", t.GapReason, "gapDetectedAt", i64s(t.GapFound))
		if s.invalid > 0 {
			pipe.HIncrBy(ctx, key, "droppedInvalid", s.invalid)
		}
		pipe.PExpire(ctx, key, 7*24*time.Hour)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		logx.Warn("failed to persist feed quality", "err", err.Error())
	}

	for _, g := range gaps {
		reportGap(ctx, g)
	}
	for id, sym := range raiseDrop {
		at := drops[id]
		alerts.Raise(ctx, alerts.Alert{Code: AlertFeedTicksDropped, Severity: models.AlertWarning, Source: models.AlertSourceFeed,
			DedupKey: AlertFeedTicksDropped + ":" + id, Title: "Market data ticks dropped: " + sym,
			Detail: "the ingestion buffer was full and refused ticks (last at " + time.UnixMilli(at).UTC().Format(time.RFC3339Nano) +
				"); new trades on the asset are refused for feedSaturationHoldMs after each drop"})
	}
	for _, s := range snaps {
		if s.resolveDrop {
			alerts.ResolveByKey(ctx, AlertFeedTicksDropped+":"+s.assetID, "no ticks dropped since "+time.UnixMilli(now-dropClear).UTC().Format(time.RFC3339))
		}
		if s.raiseAhead {
			alerts.Raise(ctx, alerts.Alert{Code: AlertFeedProviderAhead, Severity: models.AlertWarning, Source: models.AlertSourceFeed,
				DedupKey: AlertFeedProviderAhead + ":" + s.assetID, Title: "Provider timestamps ahead of the server clock: " + s.symbol,
				Detail: "provider timestamps were up to " + i64s(s.t.MaxAheadMs) + " ms ahead of receipt (limit " +
					i64s(cfg.FeedMaxClockDriftMs) + " ms); such quotes are rejected (future_timestamp) and settlement ignores ticks stamped more than settlementMaxClockSkewMs ahead"})
		}
		if s.resolveAhead {
			alerts.ResolveByKey(ctx, AlertFeedProviderAhead+":"+s.assetID, "provider timestamps back within the limit")
		}
		if s.escalateGap {
			alerts.Raise(ctx, alerts.Alert{Code: AlertFeedGap, Severity: models.AlertCritical, Source: models.AlertSourceFeed,
				DedupKey: gapAlertKey(s.assetID), Title: "Market data gap: " + s.symbol,
				Detail: s.symbol + ": no data for " + i64s(now-s.t.GapStarted) + " ms (reason " + s.t.GapReason + ")"})
		}
	}
	return nil
}

// FeedQualityView is one asset's feed quality as the admin API returns it.
type FeedQualityView struct {
	AssetID string `json:"assetId"`
	Symbol  string `json:"symbol"`
	// LIVE, QUIET, GAP, DISCONNECTED, NO_DATA; NOT_EVALUATED when no feed
	// leader has evaluated it for 5 s.
	State              string           `json:"state"`
	EvaluatedAt        int64            `json:"evaluatedAt"`
	LastSeenAt         int64            `json:"lastSeenAt"`
	LastUpdateAt       int64            `json:"lastUpdateAt"`
	ExpectedIntervalMs float64          `json:"expectedIntervalMs"`
	UpdateIntervalMs   float64          `json:"updateIntervalMs"`
	GapThresholdMs     int64            `json:"gapThresholdMs"`
	Seen               int64            `json:"seen"`
	Updates            int64            `json:"updates"`
	Heartbeats         int64            `json:"heartbeats"`
	SeqRegressions     int64            `json:"seqRegressions"`
	DroppedQueueFull   int64            `json:"droppedQueueFull"`
	DroppedInvalid     int64            `json:"droppedInvalid"`
	LastDropAt         int64            `json:"lastDropAt"`
	MaxAheadMs         int64            `json:"maxAheadMs"`
	GapOpen            bool             `json:"gapOpen"`
	GapStartedAt       int64            `json:"gapStartedAt"`
	GapReason          string           `json:"gapReason,omitempty"`
	Rejected           map[string]int64 `json:"rejected"`
	Block              *FeedBlock       `json:"block"`
}

// FeedQualityReport is the admin market-data quality block.
type FeedQualityReport struct {
	Assets         []FeedQualityView          `json:"assets"`
	Clock          *ClockState                `json:"clock"`
	QueueHighWater int64                      `json:"queueHighWater"`
	QueueCap       int64                      `json:"queueCap"`
	DroppedTotal   int64                      `json:"droppedTotal"`
	Gaps           []models.MarketDataGapView `json:"gaps"`
}

// GetFeedQuality reads every asset's feed quality (any instance).
func GetFeedQuality(ctx context.Context) (FeedQualityReport, error) {
	out := FeedQualityReport{Assets: []FeedQualityView{}, Gaps: []models.MarketDataGapView{}}
	docs, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return out, err
	}
	now := util.NowMs()
	for _, a := range docs {
		id := a.ID.Hex()
		m, err := redisx.R().HGetAll(ctx, QualityKey(id)).Result()
		if err != nil {
			return out, err
		}
		pi := func(k string) int64 { v, _ := strconv.ParseInt(m[k], 10, 64); return v }
		pf := func(k string) float64 { v, _ := strconv.ParseFloat(m[k], 64); return v }
		v := FeedQualityView{AssetID: id, Symbol: a.Symbol, State: m["state"], EvaluatedAt: pi("evaluatedAt"),
			LastSeenAt: pi("lastSeenAt"), LastUpdateAt: pi("lastUpdateAt"), ExpectedIntervalMs: pf("seenIntervalMs"),
			UpdateIntervalMs: pf("updateIntervalMs"), GapThresholdMs: pi("gapThresholdMs"), Seen: pi("seen"), Updates: pi("updates"),
			Heartbeats: pi("heartbeats"), SeqRegressions: pi("seqRegressions"), DroppedQueueFull: pi("droppedQueueFull"),
			DroppedInvalid: pi("droppedInvalid"), LastDropAt: pi("lastDropAt"), MaxAheadMs: pi("maxAheadMs"),
			GapOpen: m["gapOpen"] == "1", GapStartedAt: pi("gapStartedAt"), GapReason: m["gapReason"], Rejected: map[string]int64{}}
		if v.State == "" || now-v.EvaluatedAt > 5000 {
			v.State = "NOT_EVALUATED"
		}
		if rej, err := QuoteRejectionStats(ctx, id); err == nil {
			for k, s := range rej {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil && k != "lastAt" {
					v.Rejected[k] = n
				}
			}
		}
		if b, err := FeedBlockFor(ctx, id); err == nil {
			v.Block = b
		}
		out.Assets = append(out.Assets, v)
	}
	if c, err := GetClockState(ctx); err == nil {
		out.Clock = c
	}
	if g, err := redisx.R().HGetAll(ctx, feedQualityGlobalKey()).Result(); err == nil {
		out.QueueHighWater, _ = strconv.ParseInt(g["queueHighWater"], 10, 64)
		out.QueueCap, _ = strconv.ParseInt(g["queueCap"], 10, 64)
		out.DroppedTotal, _ = strconv.ParseInt(g["droppedTotal"], 10, 64)
	}
	gaps, err := RecentGaps(ctx, "", 50)
	if err != nil {
		return out, err
	}
	out.Gaps = gaps
	return out, nil
}
