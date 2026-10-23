package marketdata

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/riskconfig"
)

// Normalisation and volatility state per instrument.
//
// Ingestion runs on the market-feed leader only, so the checks themselves
// run in one process; the state they produce is in Redis so that every
// instance prices from the same figures and a restart or a change of leader
// resumes rather than starts over:
//
//	md:tick:<assetId>  the last accepted quote (bid, ask, mid, spreadBps)
//	md:hist:<assetId>  accepted quotes, scored by timestamp (time-indexed)
//	md:vol:<assetId>   the EWMA state (var, observation anchor, n)
//
// On first use in a process the state is restored: the last accepted quote
// from md:tick (when under a minute old) and the EWMA from md:vol (when under
// 15 minutes old), otherwise rebuilt by replaying md:hist from the baseline.
// Each instrument has its own lock; nothing is serialised globally.

func volKey(assetID string) string { return "md:vol:" + assetID }

// QuoteRejectKey counts rejected quotes per reason for an asset; the capped
// QuoteRejectLogKey holds sampled examples.
func QuoteRejectKey(assetID string) string { return "md:qrej:" + assetID }

const (
	QuoteRejectLogKey = "md:qrej:log"
	quoteRejectLogCap = 200
	// One example per (asset, reason) per window goes to the capped log.
	quoteRejectSampleWindow = 10 * time.Second
	// Rejection counts are written to the audit log once a minute.
	quoteRejectAuditEvery = time.Minute
	volStateMaxAge        = 15 * time.Minute
	lastQuoteMaxAge       = time.Minute
)

type instrumentState struct {
	mu       sync.Mutex
	loaded   bool
	norm     Normalizer
	vol      VolState
	baseline float64
}

var (
	instrumentsMu sync.Mutex
	instruments   = map[string]*instrumentState{}
)

// ResetNormalizationState drops the in-process state (tests, leader change).
func ResetNormalizationState() {
	instrumentsMu.Lock()
	instruments = map[string]*instrumentState{}
	instrumentsMu.Unlock()
	rejMu.Lock()
	rejSampled = map[string]time.Time{}
	rejCounts = map[string]map[string]int64{}
	rejMu.Unlock()
}

func instrumentFor(assetID string) *instrumentState {
	instrumentsMu.Lock()
	defer instrumentsMu.Unlock()
	st := instruments[assetID]
	if st == nil {
		st = &instrumentState{}
		instruments[assetID] = st
	}
	return st
}

// NormParamsFrom reads the thresholds from the risk configuration.
func NormParamsFrom(cfg *models.RiskConfig) NormParams {
	p := DefaultNormParams()
	if cfg == nil {
		return p
	}
	if cfg.PricingStaleAfterMs > 0 {
		p.StaleAfterMs = cfg.PricingStaleAfterMs
	}
	if cfg.PricingMaxSpreadBps > 0 {
		p.MaxSpreadBps = cfg.PricingMaxSpreadBps
	}
	if cfg.PricingMaxJumpBps > 0 {
		p.MaxJumpBps = cfg.PricingMaxJumpBps
	}
	if cfg.PricingJumpConfirmations > 0 {
		p.JumpConfirmations = int(cfg.PricingJumpConfirmations)
	}
	if cfg.PricingPendingOutlierMaxAgeMs > 0 {
		p.PendingMaxAgeMs = cfg.PricingPendingOutlierMaxAgeMs
	}
	return p
}

// BaselineVolFor is the instrument's baseline volatility from the pricing
// rules (0 when the symbol has no rule).
func BaselineVolFor(cfg *models.RiskConfig, symbol string) float64 {
	if cfg == nil {
		return 0
	}
	for _, r := range cfg.PricingSymbolRules {
		if r.Symbol == symbol {
			return r.BaselineVol
		}
	}
	return 0
}

// load restores the state of one instrument (caller holds st.mu).
func (st *instrumentState) load(ctx context.Context, assetID string, baseline float64, now int64, lambda float64, sampleMs int64) {
	st.loaded = true
	st.baseline = baseline
	st.vol = NewVolState(baseline)
	if last, err := GetLastTick(ctx, assetID); err == nil && last != nil && now-last.ReceivedAt <= lastQuoteMaxAge.Milliseconds() {
		bid, ask := last.Price, last.Price
		if last.Bid != nil && last.Ask != nil {
			bid, ask = *last.Bid, *last.Ask
		}
		st.norm.Seed(NormQuote{Bid: bid, Ask: ask, Ts: last.Timestamp})
	}
	if raw, err := redisx.R().Get(ctx, volKey(assetID)).Result(); err == nil {
		var v VolState
		if json.Unmarshal([]byte(raw), &v) == nil && now-v.UpdatedAt <= volStateMaxAge.Milliseconds() && v.Var > 0 {
			st.vol = v
			return
		}
	}
	// Rebuild from the accepted-quote history.
	ticks, err := GetTicksBetween(ctx, assetID, now-volStateMaxAge.Milliseconds(), nil)
	if err != nil || len(ticks) == 0 {
		return
	}
	sort.SliceStable(ticks, func(i, j int) bool { return ticks[i].Timestamp < ticks[j].Timestamp })
	for _, t := range ticks {
		st.vol.Observe(t.Timestamp, t.Price, lambda, sampleMs)
	}
	logx.Info("volatility state rebuilt from tick history", "assetId", assetID, "ticks", len(ticks), "sigma", st.vol.Sigma())
}

// rejection is one refused quote.
type rejection struct {
	AssetID string  `json:"assetId"`
	Symbol  string  `json:"symbol"`
	Reason  string  `json:"reason"`
	Bid     float64 `json:"bid"`
	Ask     float64 `json:"ask"`
	Ts      int64   `json:"ts"`
	At      int64   `json:"at"`
	JumpBps float64 `json:"jumpBps,omitempty"`
}

var (
	rejMu        sync.Mutex
	rejSampled   = map[string]time.Time{}
	rejCounts    = map[string]map[string]int64{}
	rejLastAudit = time.Now()
)

// normalizeBatch applies the checks to a batch of normalised ticks (arrival
// order) and returns the accepted ones with their volatility states.
func normalizeBatch(ctx context.Context, ticks []*models.NormalisedTick) ([]*models.NormalisedTick, map[string]VolState, []rejection) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		d := models.DefaultRiskConfig()
		cfg = &d
	}
	params := NormParamsFrom(cfg)
	accepted := make([]*models.NormalisedTick, 0, len(ticks))
	vols := map[string]VolState{}
	var rejects []rejection
	for _, n := range ticks {
		bid, ask := n.Price, n.Price
		if n.Bid != nil && n.Ask != nil {
			bid, ask = *n.Bid, *n.Ask
		}
		q := NormQuote{Bid: bid, Ask: ask, Ts: n.Timestamp}
		st := instrumentFor(n.AssetID)
		st.mu.Lock()
		if !st.loaded {
			st.load(ctx, n.AssetID, BaselineVolFor(cfg, n.Symbol), n.ReceivedAt, cfg.PricingEwmaLambda, cfg.PricingVolSampleIntervalMs)
		}
		res := st.norm.Check(q, n.ReceivedAt, params)
		if !res.Accepted {
			st.mu.Unlock()
			rejects = append(rejects, rejection{AssetID: n.AssetID, Symbol: n.Symbol, Reason: res.Reason,
				Bid: bid, Ask: ask, Ts: n.Timestamp, At: n.ReceivedAt, JumpBps: res.JumpBps})
			continue
		}
		// Rounded to 8 decimals (far below any instrument's tick) so the
		// stored mid carries no float noise: 100.00999999999999 is 100.01.
		mid := math.Round(q.Mid()*1e8) / 1e8
		if res.Rebaselined {
			st.vol.Rebaseline(n.Timestamp, mid)
		} else {
			st.vol.Observe(n.Timestamp, mid, cfg.PricingEwmaLambda, cfg.PricingVolSampleIntervalMs)
		}
		st.vol.UpdatedAt = n.Timestamp
		vols[n.AssetID] = st.vol
		st.mu.Unlock()

		// The accepted quote is stored as its mid, with the book beside it.
		b, a, spread := bid, ask, q.SpreadBps()
		n.Price, n.Bid, n.Ask, n.SpreadBps = mid, &b, &a, &spread
		accepted = append(accepted, n)
	}
	return accepted, vols, rejects
}

// persistNormalization writes the volatility states and the rejection
// counters (one pipeline), and once a minute an aggregated quote_rejected
// audit row per asset - never one row per tick.
func persistNormalization(ctx context.Context, vols map[string]VolState, rejects []rejection) {
	if len(vols) == 0 && len(rejects) == 0 {
		maybeAuditRejections()
		return
	}
	pipe := redisx.R().Pipeline()
	for id, v := range vols {
		raw, _ := json.Marshal(v)
		pipe.Set(ctx, volKey(id), raw, 24*time.Hour)
	}
	now := time.Now()
	rejMu.Lock()
	for _, r := range rejects {
		pipe.HIncrBy(ctx, QuoteRejectKey(r.AssetID), r.Reason, 1)
		pipe.HIncrBy(ctx, QuoteRejectKey(r.AssetID), "total", 1)
		pipe.HSet(ctx, QuoteRejectKey(r.AssetID), "lastAt", strconv.FormatInt(r.At, 10), "lastReason", r.Reason)
		key := r.AssetID + "|" + r.Reason
		if now.Sub(rejSampled[key]) >= quoteRejectSampleWindow {
			rejSampled[key] = now
			raw, _ := json.Marshal(r)
			pipe.LPush(ctx, QuoteRejectLogKey, raw)
			pipe.LTrim(ctx, QuoteRejectLogKey, 0, quoteRejectLogCap-1)
		}
		m := rejCounts[r.AssetID]
		if m == nil {
			m = map[string]int64{}
			rejCounts[r.AssetID] = m
		}
		m[r.Reason]++
	}
	rejMu.Unlock()
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		logx.Warn("failed to persist normalisation state", "err", err.Error())
	}
	maybeAuditRejections()
}

func maybeAuditRejections() {
	rejMu.Lock()
	if time.Since(rejLastAudit) < quoteRejectAuditEvery || len(rejCounts) == 0 {
		rejMu.Unlock()
		return
	}
	counts := rejCounts
	rejCounts = map[string]map[string]int64{}
	since := rejLastAudit
	rejLastAudit = time.Now()
	rejMu.Unlock()
	for assetID, m := range counts {
		audit.Log(audit.Entry{Action: "quote_rejected", Resource: "MarketData", ResourceID: assetID,
			After: map[string]any{"counts": m, "from": since.UTC().Format(time.RFC3339), "to": time.Now().UTC().Format(time.RFC3339)}})
	}
}

// GetVolatility returns the shared EWMA state of an asset. ok is false when
// no state is stored (the engine then starts from the baseline).
func GetVolatility(ctx context.Context, assetID string) (VolState, bool, error) {
	raw, err := redisx.R().Get(ctx, volKey(assetID)).Result()
	if err != nil {
		if redisx.IsNil(err) {
			return VolState{}, false, nil
		}
		return VolState{}, false, err
	}
	var v VolState
	if json.Unmarshal([]byte(raw), &v) != nil {
		return VolState{}, false, nil
	}
	return v, true, nil
}

// QuoteRejectionStats is the rejection counters of one asset.
func QuoteRejectionStats(ctx context.Context, assetID string) (map[string]string, error) {
	return redisx.R().HGetAll(ctx, QuoteRejectKey(assetID)).Result()
}

// RecentQuoteRejections is the sampled rejection log, newest first.
func RecentQuoteRejections(ctx context.Context, limit int64) ([]json.RawMessage, error) {
	if limit <= 0 || limit > quoteRejectLogCap {
		limit = quoteRejectLogCap
	}
	raw, err := redisx.R().LRange(ctx, QuoteRejectLogKey, 0, limit-1).Result()
	if err != nil && !redisx.IsNil(err) {
		return nil, err
	}
	out := make([]json.RawMessage, 0, len(raw))
	for _, r := range raw {
		out = append(out, json.RawMessage(r))
	}
	return out, nil
}

// PriceAtOrBefore is the last accepted mid at or before ts, within
// toleranceMs of it (spec 9: the settlement reference rule), from the
// time-indexed history. nil when there is none.
func PriceAtOrBefore(ctx context.Context, assetID string, ts, toleranceMs int64) (*models.NormalisedTick, error) {
	raw, err := redisx.R().ZRevRangeByScore(ctx, config.RedisKeys.TickHistory(assetID), &redis.ZRangeBy{
		Max: strconv.FormatInt(ts, 10), Min: strconv.FormatInt(ts-toleranceMs, 10), Offset: 0, Count: 1,
	}).Result()
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var t models.NormalisedTick
	if json.Unmarshal([]byte(raw[0]), &t) != nil {
		return nil, nil
	}
	return &t, nil
}
