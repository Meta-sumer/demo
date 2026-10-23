package pricing

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
)

// Live feature windows. The engine builds, from the platform's own data, the
// grid cmd/calibrate built from Binance klines:
//
//   - 1-second resolution: the last trade price in each of the WindowSteps
//     whole seconds before the current one, from the Redis tick history
//     (md:hist, scored by provider time, kept 15 minutes);
//   - 1-minute resolution: the close of each of the WindowSteps completed
//     minutes before the current one, from the persisted 1-minute candles.
//
// The Binance provider's ticks are trades (stream "@trade", price "p",
// timestamp "T"), so a second's last tick is that second's kline close and a
// candle close is the minute's kline close.

// tickGrid caches one asset's per-second closes. It is refreshed
// incrementally, so a quote costs one small Redis read rather than five
// minutes of ticks.
type tickGrid struct {
	mu        sync.Mutex
	closes    map[int64]float64
	stamp     map[int64]int64
	fetchedTo int64
	at        time.Time
}

var (
	gridsMu sync.Mutex
	grids   = map[string]*tickGrid{}
)

// gridRefresh is how often a grid re-reads Redis; overlap re-reads ticks a
// little before the last fetch, so a late tick still lands in its second.
const (
	gridRefresh = 250 * time.Millisecond
	gridOverlap = 3000
)

// ResetLiveCaches drops the cached grids (tests).
func ResetLiveCaches() {
	gridsMu.Lock()
	grids = map[string]*tickGrid{}
	gridsMu.Unlock()
	candleMu.Lock()
	candleCache = map[string]*candleWindow{}
	candleMu.Unlock()
}

func gridFor(assetID string) *tickGrid {
	gridsMu.Lock()
	defer gridsMu.Unlock()
	g := grids[assetID]
	if g == nil {
		g = &tickGrid{closes: map[int64]float64{}, stamp: map[int64]int64{}}
		grids[assetID] = g
	}
	return g
}

type histTick struct {
	Price     float64 `json:"price"`
	Timestamp int64   `json:"timestamp"`
}

// secondWindow returns the closes of the window seconds before nowMs's
// second.
func secondWindow(ctx context.Context, assetID string, window int, nowMs int64) ([]float64, []bool, error) {
	nowSec := nowMs / 1000
	from := (nowSec - int64(window) - 1) * 1000
	g := gridFor(assetID)
	g.mu.Lock()
	defer g.mu.Unlock()
	if time.Since(g.at) >= gridRefresh || g.fetchedTo < from {
		start := from
		if g.fetchedTo > from {
			start = g.fetchedTo - gridOverlap
		}
		raw, err := redisx.R().ZRangeByScore(ctx, config.RedisKeys.TickHistory(assetID), &redis.ZRangeBy{
			Min: strconv.FormatInt(start, 10), Max: "+inf",
		}).Result()
		if err != nil {
			return nil, nil, err
		}
		for _, m := range raw {
			var t histTick
			if json.Unmarshal([]byte(m), &t) != nil || !(t.Price > 0) || math.IsInf(t.Price, 0) {
				continue
			}
			sec := t.Timestamp / 1000
			if prev, ok := g.stamp[sec]; !ok || t.Timestamp >= prev {
				g.closes[sec], g.stamp[sec] = t.Price, t.Timestamp
			}
			if t.Timestamp > g.fetchedTo {
				g.fetchedTo = t.Timestamp
			}
		}
		if g.fetchedTo < from {
			g.fetchedTo = from
		}
		for sec := range g.closes {
			if sec < nowSec-int64(max(window, 600))-5 {
				delete(g.closes, sec)
				delete(g.stamp, sec)
			}
		}
		g.at = time.Now()
	}
	closes := make([]float64, window)
	observed := make([]bool, window)
	for i := 0; i < window; i++ {
		sec := nowSec - int64(window) + int64(i)
		if p, ok := g.closes[sec]; ok {
			closes[i], observed[i] = p, true
		} else {
			closes[i] = math.NaN()
		}
	}
	return closes, observed, nil
}

type candleWindow struct {
	minute int64
	at     time.Time
	closes map[int64]float64
}

var (
	candleMu    sync.Mutex
	candleCache = map[string]*candleWindow{}
)

// minuteWindow returns the closes of the window completed minutes before
// nowMs's minute, from the candles collection (re-read every 5 s, as the
// previous minute is persisted when the next one opens).
func minuteWindow(ctx context.Context, assetID string, window int, nowMs int64) ([]float64, []bool, error) {
	nowMin := nowMs / 60_000
	key := assetID + ":" + strconv.Itoa(window)
	candleMu.Lock()
	cw := candleCache[key]
	fresh := cw != nil && cw.minute == nowMin && time.Since(cw.at) < 5*time.Second
	candleMu.Unlock()
	if !fresh {
		oid, ok := mongox.ParseID(assetID)
		if !ok {
			return nil, nil, nil
		}
		cur, err := mongox.C(models.CollCandles).Find(ctx, bson.M{
			"assetId": oid, "interval": 60,
			"openTime": bson.M{"$gte": (nowMin - int64(window)) * 60_000, "$lt": nowMin * 60_000},
		}, options.Find().SetProjection(bson.M{"openTime": 1, "close": 1, "ticks": 1}))
		if err != nil {
			return nil, nil, err
		}
		var rows []models.Candle
		if err := cur.All(ctx, &rows); err != nil {
			return nil, nil, err
		}
		cw = &candleWindow{minute: nowMin, at: time.Now(), closes: map[int64]float64{}}
		for _, c := range rows {
			if c.Close > 0 && c.Ticks > 0 {
				cw.closes[c.OpenTime/60_000] = c.Close
			}
		}
		candleMu.Lock()
		candleCache[key] = cw
		candleMu.Unlock()
	}
	closes := make([]float64, window)
	observed := make([]bool, window)
	for i := 0; i < window; i++ {
		m := nowMin - int64(window) + int64(i)
		if p, ok := cw.closes[m]; ok {
			closes[i], observed[i] = p, true
		} else {
			closes[i] = math.NaN()
		}
	}
	return closes, observed, nil
}

// LiveFeatures builds an entry's features at nowMs from the platform's data,
// ending at price (the tick that prices the trade).
func LiveFeatures(ctx context.Context, assetID string, spec models.PricingFeatureSpec, price float64, nowMs int64) (Features, bool, error) {
	var closes []float64
	var observed []bool
	var err error
	switch spec.ResolutionSeconds {
	case 1:
		closes, observed, err = secondWindow(ctx, assetID, spec.WindowSteps, nowMs)
	case 60:
		closes, observed, err = minuteWindow(ctx, assetID, spec.WindowSteps, nowMs)
	default:
		return Features{}, false, nil
	}
	if err != nil {
		return Features{}, false, err
	}
	f, ok := ComputeFeatures(spec, closes, observed, price)
	return f, ok, nil
}
