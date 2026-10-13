// Package metrics records latency samples and counters (spec section 28).
// Recording is fire-and-forget: instrumentation must never fail a trade.
package metrics

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
)

// QueueLatencies adds samples to a pipeline the caller executes, for a hot path
// that already makes one Redis round trip and must not add one per sample. The
// list ends up as it would after one RecordLatency per sample, in order.
func QueueLatencies(ctx context.Context, pipe redis.Pipeliner, metric string, samples ...float64) {
	values := make([]any, 0, len(samples))
	for _, ms := range samples {
		if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 {
			continue
		}
		values = append(values, strconv.FormatFloat(ms, 'f', 3, 64))
	}
	if len(values) == 0 {
		return
	}
	key := config.RedisKeys.Latency(metric)
	pipe.LPush(ctx, key, values...)
	pipe.LTrim(ctx, key, 0, config.LatencySampleLimit-1)
}

// RecordLatency stores one sample asynchronously.
func RecordLatency(metric string, ms float64) {
	if math.IsNaN(ms) || math.IsInf(ms, 0) || ms < 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		key := config.RedisKeys.Latency(metric)
		pipe := redisx.R().Pipeline()
		pipe.LPush(ctx, key, strconv.FormatFloat(ms, 'f', 3, 64))
		pipe.LTrim(ctx, key, 0, config.LatencySampleLimit-1)
		if _, err := pipe.Exec(ctx); err != nil {
			logx.Debug("failed to record latency sample", "metric", metric, "err", err.Error())
		}
	}()
}

// Since is a convenience for RecordLatency(metric, elapsed ms since start).
func Since(metric string, start time.Time) {
	RecordLatency(metric, float64(time.Since(start).Microseconds())/1000)
}

func GetLatencySummary(ctx context.Context, metric string) util.PercentileSummary {
	raw, err := redisx.R().LRange(ctx, config.RedisKeys.Latency(metric), 0, config.LatencySampleLimit-1).Result()
	if err != nil {
		return util.Summarise(nil)
	}
	samples := make([]float64, 0, len(raw))
	for _, s := range raw {
		if v, err := strconv.ParseFloat(s, 64); err == nil && !math.IsInf(v, 0) && !math.IsNaN(v) {
			samples = append(samples, v)
		}
	}
	return util.Summarise(samples)
}

type LatencyReportEntry struct {
	Metric string `json:"metric"`
	util.PercentileSummary
}

func GetLatencyReport(ctx context.Context) []LatencyReportEntry {
	out := make([]LatencyReportEntry, 0, len(config.LatencyMetrics))
	for _, m := range config.LatencyMetrics {
		out = append(out, LatencyReportEntry{Metric: m, PercentileSummary: GetLatencySummary(ctx, m)})
	}
	return out
}

// IncrementCounter bumps a per-UTC-day counter asynchronously. atMs 0 = now.
func IncrementCounter(name string, by float64, atMs int64) {
	if atMs == 0 {
		atMs = util.NowMs()
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		key := config.RedisKeys.Counters(util.UTCDayKey(atMs))
		pipe := redisx.R().Pipeline()
		pipe.HIncrByFloat(ctx, key, name, by)
		pipe.Expire(ctx, key, 40*24*time.Hour)
		if _, err := pipe.Exec(ctx); err != nil {
			logx.Debug("failed to increment counter", "name", name, "err", err.Error())
		}
	}()
}

// GetCounters returns a day's counters; day "" = today.
func GetCounters(ctx context.Context, day string) map[string]float64 {
	if day == "" {
		day = util.UTCDayKey(util.NowMs())
	}
	out := map[string]float64{}
	raw, err := redisx.R().HGetAll(ctx, config.RedisKeys.Counters(day)).Result()
	if err != nil {
		return out
	}
	for k, v := range raw {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			out[k] = f
		}
	}
	return out
}
