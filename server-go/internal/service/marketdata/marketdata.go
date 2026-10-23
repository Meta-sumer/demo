// Package marketdata is the market data pipeline (spec sections 5, 6, 23).
//
//	provider tick -> normalise -> Redis (last tick + history) -> websocket
//
// The server timestamps every tick on arrival. Client clocks are never used and
// client-supplied prices are never accepted anywhere in the platform.
package marketdata

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/riskconfig"
)

var (
	stateMu            sync.RWMutex
	providerName       = "unknown"
	lastProviderStatus *providers.Status
)

func SetProviderName(name string) {
	stateMu.Lock()
	providerName = name
	stateMu.Unlock()
}

func currentProviderName() string {
	stateMu.RLock()
	defer stateMu.RUnlock()
	return providerName
}

// RecordProviderStatus keeps the latest status in memory, mirrors it to the
// md:provider:status hash and tells the admin market channel.
func RecordProviderStatus(status providers.Status) {
	stateMu.Lock()
	s := status
	lastProviderStatus = &s
	stateMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := redisx.R().HSet(ctx, config.RedisKeys.ProviderStatus,
			"provider", status.Provider,
			"state", string(status.State),
			"message", status.Message,
			"at", strconv.FormatInt(status.At, 10),
		).Err()
		if err != nil {
			logx.Debug("failed to persist provider status", "err", err.Error())
		}
	}()
	ws.Emit([]string{config.ChAdminMarket}, config.EvMarketStatus, map[string]any{
		"provider": status.Provider, "state": status.State, "at": status.At,
	})
}

// GetProviderStatus returns the latest status seen by this process, or nil.
func GetProviderStatus() *providers.Status {
	stateMu.RLock()
	defer stateMu.RUnlock()
	if lastProviderStatus == nil {
		return nil
	}
	s := *lastProviderStatus
	return &s
}

// tickJSON is NormalisedTick with the TS object-literal key order, so the
// history ZSET members and websocket payloads are byte-identical to Node's
// JSON.stringify output.
type tickJSON struct {
	AssetID             string   `json:"assetId"`
	Asset               string   `json:"asset"`
	Symbol              string   `json:"symbol"`
	Price               float64  `json:"price"`
	Bid                 *float64 `json:"bid,omitempty"`
	Ask                 *float64 `json:"ask,omitempty"`
	Timestamp           int64    `json:"timestamp"`
	ProviderTimestamp   int64    `json:"providerTimestamp"`
	ReceivedAt          int64    `json:"receivedAt"`
	Sequence            *int64   `json:"sequence,omitempty"`
	ProcessedAt         int64    `json:"processedAt"`
	FeedLatencyMs       int64    `json:"feedLatencyMs"`
	ProcessingLatencyMs int64    `json:"processingLatencyMs"`
	TotalLatencyMs      int64    `json:"totalLatencyMs"`
	SpreadBps           *float64 `json:"spreadBps,omitempty"`
}

func toTickJSON(t *models.NormalisedTick) tickJSON {
	pt := t.Timestamp
	if t.ProviderTimestamp != nil {
		pt = *t.ProviderTimestamp
	}
	return tickJSON{
		AssetID: t.AssetID, Asset: t.Asset, Symbol: t.Symbol, Price: t.Price, Bid: t.Bid, Ask: t.Ask,
		Timestamp: t.Timestamp, ProviderTimestamp: pt, ReceivedAt: t.ReceivedAt, Sequence: t.Sequence,
		ProcessedAt: t.ProcessedAt, FeedLatencyMs: t.FeedLatencyMs,
		ProcessingLatencyMs: t.ProcessingLatencyMs, TotalLatencyMs: t.TotalLatencyMs, SpreadBps: t.SpreadBps,
	}
}

// IngestTick is the entry point for a single tick. It returns nil (no error)
// when the provider sent a symbol this platform does not trade, or a bad price.
func IngestTick(ctx context.Context, tick providers.Tick) (*models.NormalisedTick, error) {
	return ingestTickAt(ctx, tick, util.NowMs())
}

func ingestTickAt(ctx context.Context, tick providers.Tick, receivedAt int64) (*models.NormalisedTick, error) {
	out := ingestTicks(ctx, []queuedTick{{tick: tick, receivedAt: receivedAt}})
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}

// ingestTicks processes a batch of queued ticks in arrival order. A tick that
// fails to normalise is logged and skipped; a quote the normalisation checks
// reject (normalize.go) is counted and dropped without touching market
// state; the rest of the batch goes on.
func ingestTicks(ctx context.Context, batch []queuedTick) []*models.NormalisedTick {
	ticks := make([]*models.NormalisedTick, 0, len(batch))
	for _, q := range batch {
		n, err := normaliseTick(ctx, q.tick, q.receivedAt)
		if err != nil {
			logx.Error("tick ingestion failed", "err", err.Error(), "symbol", q.tick.Symbol)
			continue
		}
		if n != nil {
			ticks = append(ticks, n)
		}
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		d := models.DefaultRiskConfig()
		cfg = &d
	}
	// Every message counts for gap detection, accepted or not: a rejected
	// quote still proves the provider alive (feedquality.go).
	observeTicks(ctx, ticks, GapParamsFrom(cfg))
	accepted, vols, rejects := normalizeBatch(ctx, ticks)
	writeTicks(ctx, accepted, cfg.FeedTickHistoryRetentionMs)
	persistNormalization(ctx, vols, rejects)
	return accepted
}

// writeTicks makes ONE Redis round trip for a batch of ticks: the last-tick
// and history writes, the websocket broadcasts and the latency samples go in
// one pipeline, which Redis runs in order.
//
// The feed worker hands over whatever queued up since the previous batch. One
// round trip per tick capped the feed at 1/RTT ticks a second; with Redis at
// 20 ms (Docker Desktop on Windows over 127.0.0.1, or any loaded host) the
// simulated feed's 100 ticks a second fell further behind every second and
// the markets read as stale. Per batch, a slower Redis makes the batches grow
// instead of the queue. Broadcasting from the same pipeline also keeps ticks
// in order on the wire, which separate fire-and-forget publishes did not.
//
// retentionMs is how long md:hist keeps ticks (risk configuration
// feedTickHistoryRetentionMs; <= 0 = config.TickHistoryRetentionMs).
func writeTicks(ctx context.Context, ticks []*models.NormalisedTick, retentionMs int64) {
	if len(ticks) == 0 {
		return
	}
	if retentionMs <= 0 {
		retentionMs = config.TickHistoryRetentionMs
	}
	feed := make([]float64, 0, len(ticks))
	processing := make([]float64, 0, len(ticks))
	total := make([]float64, 0, len(ticks))
	pipe := redisx.R().Pipeline()

	for _, n := range ticks {
		member, _ := json.Marshal(toTickJSON(n))
		queuePersistTick(ctx, pipe, n, string(member), retentionMs)
		aggregateCandle(n.AssetID, n.Symbol, n)
		if raw, err := ws.EnvelopeFor([]string{config.ChMarket(n.AssetID), config.ChAdminMarket}, config.EvMarketTick, member); err == nil {
			pipe.Publish(ctx, config.PubSubBroadcast, raw)
		}
		feed = append(feed, float64(n.FeedLatencyMs))
		processing = append(processing, float64(n.ProcessingLatencyMs))
		total = append(total, float64(n.TotalLatencyMs))
	}

	metrics.QueueLatencies(ctx, pipe, config.LatFeedProviderToServer, feed...)
	metrics.QueueLatencies(ctx, pipe, config.LatFeedProcessing, processing...)
	metrics.QueueLatencies(ctx, pipe, config.LatFeedTotal, total...)
	if _, err := pipe.Exec(ctx); err != nil {
		logx.Error("failed to persist ticks", "err", err.Error(), "ticks", len(ticks))
	}
}

// normaliseTick stamps a provider tick with the platform's asset and latency
// fields. Nil (no error) for a symbol this platform does not trade or a bad price.
func normaliseTick(ctx context.Context, tick providers.Tick, receivedAt int64) (*models.NormalisedTick, error) {
	asset, err := assets.FindAssetByProviderSymbol(ctx, tick.Symbol)
	if err != nil {
		return nil, err
	}
	if asset == nil {
		return nil, nil
	}
	if math.IsNaN(tick.Price) || math.IsInf(tick.Price, 0) || tick.Price <= 0 {
		logx.Warn("discarding tick with an invalid price", "symbol", tick.Symbol, "price", tick.Price)
		recordInvalid(asset.ID.Hex(), asset.Symbol)
		return nil, nil
	}

	providerTimestamp := receivedAt
	if tick.ProviderTimestamp != nil {
		providerTimestamp = *tick.ProviderTimestamp
	}
	// Provider clocks can run ahead of ours; a negative latency is meaningless.
	feedLatencyMs := max(0, receivedAt-providerTimestamp)
	processedAt := util.NowMs()

	pt := providerTimestamp
	n := &models.NormalisedTick{
		MarketTick: models.MarketTick{
			Asset:             asset.Symbol,
			Bid:               tick.Bid,
			Ask:               tick.Ask,
			Price:             tick.Price,
			Timestamp:         providerTimestamp,
			ProviderTimestamp: &pt,
			ReceivedAt:        receivedAt,
			Sequence:          tick.Sequence,
		},
		AssetID:             asset.ID.Hex(),
		Symbol:              asset.Symbol,
		ProcessedAt:         processedAt,
		FeedLatencyMs:       feedLatencyMs,
		ProcessingLatencyMs: processedAt - receivedAt,
		TotalLatencyMs:      max(0, processedAt-providerTimestamp),
	}
	return n, nil
}

// queuePersistTick adds one tick's last-tick and history writes to pipe.
// The history keeps retentionMs of ticks: settlement reads the expiry price
// from it, so the risk configuration requires it to cover the settlement
// defer window with a margin.
func queuePersistTick(ctx context.Context, pipe redis.Pipeliner, t *models.NormalisedTick, member string, retentionMs int64) {
	lastKey := config.RedisKeys.LastTick(t.AssetID)
	historyKey := config.RedisKeys.TickHistory(t.AssetID)
	cutoff := t.ReceivedAt - retentionMs
	retention := time.Duration(retentionMs) * time.Millisecond

	pipe.HSet(ctx, lastKey, serialiseTick(t))
	pipe.PExpire(ctx, lastKey, retention)
	// Scored by provider event time: the expiry rule selects on market time,
	// not on when this server happened to receive the packet.
	pipe.ZAdd(ctx, historyKey, redis.Z{Score: float64(t.Timestamp), Member: member})
	pipe.ZRemRangeByScore(ctx, historyKey, "0", strconv.FormatInt(cutoff, 10))
	pipe.PExpire(ctx, historyKey, retention)
}

// jsString is JavaScript String(number). encoding/json formats float64 with
// the same ES6 rules.
func jsString(f float64) string {
	b, err := json.Marshal(f)
	if err != nil {
		return strconv.FormatFloat(f, 'g', -1, 64)
	}
	return string(b)
}

func optString(f *float64) string {
	if f == nil {
		return ""
	}
	return jsString(*f)
}

func serialiseTick(t *models.NormalisedTick) map[string]any {
	pt := t.Timestamp
	if t.ProviderTimestamp != nil {
		pt = *t.ProviderTimestamp
	}
	seq := ""
	if t.Sequence != nil {
		seq = strconv.FormatInt(*t.Sequence, 10)
	}
	i := func(n int64) string { return strconv.FormatInt(n, 10) }
	return map[string]any{
		"assetId":             t.AssetID,
		"symbol":              t.Symbol,
		"price":               jsString(t.Price),
		"bid":                 optString(t.Bid),
		"ask":                 optString(t.Ask),
		"timestamp":           i(t.Timestamp),
		"providerTimestamp":   i(pt),
		"receivedAt":          i(t.ReceivedAt),
		"processedAt":         i(t.ProcessedAt),
		"sequence":            seq,
		"feedLatencyMs":       i(t.FeedLatencyMs),
		"processingLatencyMs": i(t.ProcessingLatencyMs),
		"totalLatencyMs":      i(t.TotalLatencyMs),
		"spreadBps":           optString(t.SpreadBps),
		"provider":            currentProviderName(),
	}
}

// GetLastTick returns the newest tick for an asset, or nil.
func GetLastTick(ctx context.Context, assetID string) (*models.NormalisedTick, error) {
	raw, err := redisx.R().HGetAll(ctx, config.RedisKeys.LastTick(assetID)).Result()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return deserialiseTick(raw), nil
}

// jsNum is Number(s) for the numeric strings this package writes.
func jsNum(s string) float64 {
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return math.NaN()
	}
	return f
}

func jsInt(s string) int64 {
	f := jsNum(s)
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return int64(f)
}

func deserialiseTick(raw map[string]string) *models.NormalisedTick {
	optional := func(v string) *float64 {
		if v == "" {
			return nil
		}
		f := jsNum(v)
		return &f
	}
	pt := jsInt(raw["providerTimestamp"])
	t := &models.NormalisedTick{
		MarketTick: models.MarketTick{
			Asset:             raw["symbol"],
			Price:             jsNum(raw["price"]),
			Bid:               optional(raw["bid"]),
			Ask:               optional(raw["ask"]),
			Timestamp:         jsInt(raw["timestamp"]),
			ProviderTimestamp: &pt,
			ReceivedAt:        jsInt(raw["receivedAt"]),
		},
		AssetID:             raw["assetId"],
		Symbol:              raw["symbol"],
		ProcessedAt:         jsInt(raw["processedAt"]),
		FeedLatencyMs:       jsInt(raw["feedLatencyMs"]),
		ProcessingLatencyMs: jsInt(raw["processingLatencyMs"]),
		TotalLatencyMs:      jsInt(raw["totalLatencyMs"]),
	}
	if s := raw["sequence"]; s != "" {
		n := jsInt(s)
		t.Sequence = &n
	}
	t.SpreadBps = optional(raw["spreadBps"])
	return t
}

// GetFeedHealth reports feed health for one asset (spec section 6). symbol ""
// means "use the tick's symbol".
//
// Two dimensions are combined and the worse one wins: transport quality (how
// long the last tick took to reach us) and freshness (how long ago that was).
// A feed that delivered a 40ms tick two seconds ago is not healthy, and a
// fresh tick that took a second to arrive is not healthy either.
func GetFeedHealth(ctx context.Context, assetID, symbol string) (models.FeedHealthSnapshot, error) {
	tick, err := GetLastTick(ctx, assetID)
	if err != nil {
		return models.FeedHealthSnapshot{}, err
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return models.FeedHealthSnapshot{}, err
	}
	at := util.NowMs()

	if tick == nil {
		return models.FeedHealthSnapshot{
			AssetID:   assetID,
			Symbol:    symbol,
			Status:    models.FeedStatusOffline,
			Provider:  currentProviderName(),
			UpdatedAt: at,
		}, nil
	}

	ageMs := at - tick.ReceivedAt
	status := ClassifyFeed(FeedClassificationInput{
		AgeMs:         ageMs,
		LatencyMs:     tick.TotalLatencyMs,
		HealthyMaxMs:  cfg.FeedHealthyMaxMs,
		DegradedMaxMs: cfg.FeedDegradedMaxMs,
		TickMaxAgeMs:  cfg.FeedTickMaxAgeMs,
	})
	// Feed data rules: ticks dropped by a saturated buffer, or a drifting
	// clock, make even a fresh feed STALE (feedguard.go).
	block, err := FeedBlockFor(ctx, assetID)
	if err != nil {
		return models.FeedHealthSnapshot{}, err
	}
	blockReason := ""
	if block != nil {
		blockReason = block.String()
		if status == models.FeedStatusHealthy || status == models.FeedStatusDegraded {
			status = models.FeedStatusStale
		}
	}
	if symbol == "" {
		symbol = tick.Symbol
	}
	pt := tick.Timestamp
	if tick.ProviderTimestamp != nil {
		pt = *tick.ProviderTimestamp
	}
	price := tick.Price
	return models.FeedHealthSnapshot{
		AssetID:             assetID,
		Symbol:              symbol,
		Status:              status,
		LastPrice:           &price,
		ProviderTimestamp:   &pt,
		ServerReceivedAt:    &tick.ReceivedAt,
		ProcessedAt:         &tick.ProcessedAt,
		FeedLatencyMs:       &tick.FeedLatencyMs,
		ProcessingLatencyMs: &tick.ProcessingLatencyMs,
		TotalLatencyMs:      &tick.TotalLatencyMs,
		AgeMs:               &ageMs,
		Provider:            currentProviderName(),
		UpdatedAt:           at,
		BlockReason:         blockReason,
	}, nil
}

type FeedClassificationInput struct {
	AgeMs         int64
	LatencyMs     int64
	HealthyMaxMs  int64
	DegradedMaxMs int64
	TickMaxAgeMs  int64
}

// ClassifyFeed is pure, so the thresholds can be unit tested without Redis.
// The bands: HEALTHY up to healthyMaxMs, DEGRADED up to degradedMaxMs,
// STALE above it, OFFLINE when the last tick is older than tickMaxAgeMs.
// (feedStaleMaxMs, once listed among them, was never read and is retired.)
func ClassifyFeed(in FeedClassificationInput) models.FeedStatus {
	if in.AgeMs > in.TickMaxAgeMs {
		return models.FeedStatusOffline
	}
	effective := max(in.LatencyMs, in.AgeMs)
	if effective <= in.HealthyMaxMs {
		return models.FeedStatusHealthy
	}
	if effective <= in.DegradedMaxMs {
		return models.FeedStatusDegraded
	}
	return models.FeedStatusStale
}

// GetAllFeedHealth reports every asset (active or not), in registry order.
func GetAllFeedHealth(ctx context.Context) ([]models.FeedHealthSnapshot, error) {
	docs, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]models.FeedHealthSnapshot, len(docs))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(16)
	for i, a := range docs {
		g.Go(func() error {
			h, err := GetFeedHealth(gctx, a.ID.Hex(), a.Symbol)
			out[i] = h
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}

// AssertFeedTradable is the trading gate (spec section 6 and rule 12): when
// the authoritative feed for an asset is not usable, new trades on that asset
// stop. Open trades continue to settle under the terms they were accepted on.
// It applies the freshness rule (freshness.go) - the very rule quotes are
// issued under - to the last accepted tick, and returns that tick: it is the
// price the trade will be accepted at.
func AssertFeedTradable(ctx context.Context, assetID, symbol string) (*models.NormalisedTick, error) {
	tick, err := GetLastTick(ctx, assetID)
	if err != nil {
		return nil, err
	}
	f, err := CheckFreshness(ctx, assetID, tick, util.NowMs())
	if err != nil {
		return nil, err
	}
	if !f.OK {
		return nil, FreshnessError(symbol, f)
	}
	return tick, nil
}

// Expiry price sources.
const (
	SourceTickAtOrBeforeExpiry = "TICK_AT_OR_BEFORE_EXPIRY"
	SourceLastKnownTick        = "LAST_KNOWN_TICK"
)

type ExpiryPriceResult struct {
	Price  float64
	Tick   *models.NormalisedTick
	Source string // SourceTickAtOrBeforeExpiry | SourceLastKnownTick
}

// GetPriceAtOrBefore is the authoritative expiry price (spec section 17): the
// last tick whose market timestamp is at or before the expiry instant. The
// caller waits for a tick published after expiry first, so that a tick still
// in flight is not missed; the rule itself never looks forward. nil when no
// price is known at all.
func GetPriceAtOrBefore(ctx context.Context, assetID string, timestamp int64) (*ExpiryPriceResult, error) {
	raw, err := redisx.R().ZRevRangeByScore(ctx, config.RedisKeys.TickHistory(assetID), &redis.ZRangeBy{
		Max: strconv.FormatInt(timestamp, 10), Min: "-inf", Offset: 0, Count: 1,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(raw) > 0 {
		var t models.NormalisedTick
		if json.Unmarshal([]byte(raw[0]), &t) == nil {
			return &ExpiryPriceResult{Price: t.Price, Tick: &t, Source: SourceTickAtOrBeforeExpiry}, nil
		}
		// fall through to the last known tick
	}
	last, err := GetLastTick(ctx, assetID)
	if err != nil || last == nil {
		return nil, err
	}
	return &ExpiryPriceResult{Price: last.Price, Tick: last, Source: SourceLastKnownTick}, nil
}

// HasTickAtOrAfter is true once a tick timestamped at or after timestamp has
// been observed.
func HasTickAtOrAfter(ctx context.Context, assetID string, timestamp int64) (bool, error) {
	n, err := redisx.R().ZCount(ctx, config.RedisKeys.TickHistory(assetID), strconv.FormatInt(timestamp, 10), "+inf").Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// DefaultVolatilityWindowMs is the TS default for getRealisedVolatility.
const DefaultVolatilityWindowMs = 60_000

// GetRealisedVolatility is annualisation-free realised volatility: the
// standard deviation of per-tick log returns in the window (windowMs <= 0
// means DefaultVolatilityWindowMs).
func GetRealisedVolatility(ctx context.Context, assetID string, windowMs int64) (float64, error) {
	if windowMs <= 0 {
		windowMs = DefaultVolatilityWindowMs
	}
	from := util.NowMs() - windowMs
	raw, err := redisx.R().ZRangeByScore(ctx, config.RedisKeys.TickHistory(assetID), &redis.ZRangeBy{
		Min: strconv.FormatInt(from, 10), Max: "+inf",
	}).Result()
	if err != nil {
		return 0, err
	}
	if len(raw) < 3 {
		return 0, nil
	}
	prices := make([]float64, 0, len(raw))
	for _, entry := range raw {
		var t struct {
			Price float64 `json:"price"`
		}
		if json.Unmarshal([]byte(entry), &t) == nil {
			prices = append(prices, t.Price)
		}
	}
	return realisedVolatility(prices), nil
}

func realisedVolatility(prices []float64) float64 {
	if len(prices) < 3 {
		return 0
	}
	var returns []float64
	for i := 1; i < len(prices); i++ {
		if prices[i-1] > 0 {
			returns = append(returns, math.Log(prices[i]/prices[i-1]))
		}
	}
	if len(returns) < 2 {
		return 0
	}
	var sum float64
	for _, r := range returns {
		sum += r
	}
	mean := sum / float64(len(returns))
	var acc float64
	for _, r := range returns {
		acc += (r - mean) * (r - mean)
	}
	return math.Sqrt(acc / float64(len(returns)-1))
}

// DefaultRecentTicksWindowMs is the TS default for getRecentTicks.
const DefaultRecentTicksWindowMs = 5 * 60 * 1000

// GetRecentTicks returns recent raw ticks (ascending by market time), used by
// the client chart to draw between candles.
func GetRecentTicks(ctx context.Context, assetID string, windowMs int64) ([]models.NormalisedTick, error) {
	if windowMs <= 0 {
		windowMs = DefaultRecentTicksWindowMs
	}
	return GetTicksBetween(ctx, assetID, util.NowMs()-windowMs, nil)
}

// GetTicksBetween returns the retained raw ticks whose market timestamp is in
// [fromMs, toMs] (toMs nil = no upper bound), ascending. Only the last
// TickHistoryRetentionMs are kept, so an older window comes back empty.
func GetTicksBetween(ctx context.Context, assetID string, fromMs int64, toMs *int64) ([]models.NormalisedTick, error) {
	maxScore := "+inf"
	if toMs != nil {
		maxScore = strconv.FormatInt(*toMs, 10)
	}
	raw, err := redisx.R().ZRangeByScore(ctx, config.RedisKeys.TickHistory(assetID), &redis.ZRangeBy{
		Min: strconv.FormatInt(fromMs, 10), Max: maxScore,
	}).Result()
	if err != nil {
		return nil, err
	}
	ticks := make([]models.NormalisedTick, 0, len(raw))
	for _, entry := range raw {
		var t models.NormalisedTick
		if json.Unmarshal([]byte(entry), &t) == nil {
			ticks = append(ticks, t)
		}
	}
	return ticks, nil
}
