package marketdata

import (
	"context"
	"math"
	"slices"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
)

// CandleIntervalSeconds is the only persisted bucket size.
const CandleIntervalSeconds = 60

type candleBucket struct {
	assetID  bson.ObjectID
	symbol   string
	openTime int64
	open     float64
	high     float64
	low      float64
	close    float64
	ticks    int64
}

var (
	candleMu    sync.Mutex
	openCandles = map[string]*candleBucket{}
)

func aggregateCandle(assetID, symbol string, tick *models.NormalisedTick) {
	const bucketMs = CandleIntervalSeconds * 1000
	openTime := floorDiv(tick.Timestamp, bucketMs) * bucketMs

	candleMu.Lock()
	defer candleMu.Unlock()
	current := openCandles[assetID]
	if current == nil || current.openTime != openTime {
		if current != nil {
			done := *current
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				flushCandle(ctx, &done)
			}()
		}
		oid, _ := mongox.ParseID(assetID)
		openCandles[assetID] = &candleBucket{
			assetID: oid, symbol: symbol, openTime: openTime,
			open: tick.Price, high: tick.Price, low: tick.Price, close: tick.Price, ticks: 1,
		}
		return
	}
	current.high = math.Max(current.high, tick.Price)
	current.low = math.Min(current.low, tick.Price)
	current.close = tick.Price
	current.ticks++
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

func flushCandle(ctx context.Context, b *candleBucket) {
	_, err := mongox.C(models.CollCandles).UpdateOne(ctx,
		bson.M{"assetId": b.assetID, "interval": CandleIntervalSeconds, "openTime": b.openTime},
		bson.M{"$set": bson.M{
			"symbol": b.symbol,
			"open":   b.open,
			"high":   b.high,
			"low":    b.low,
			"close":  b.close,
			"ticks":  b.ticks,
		}},
		options.UpdateOne().SetUpsert(true))
	if err != nil {
		logx.Error("failed to persist candle", "err", err.Error(), "symbol", b.symbol)
	}
}

// FlushOpenCandles persists every partially filled bucket - called on shutdown.
func FlushOpenCandles(ctx context.Context) {
	candleMu.Lock()
	buckets := make([]*candleBucket, 0, len(openCandles))
	for _, b := range openCandles {
		c := *b
		buckets = append(buckets, &c)
	}
	openCandles = map[string]*candleBucket{}
	candleMu.Unlock()

	var wg sync.WaitGroup
	for _, b := range buckets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			flushCandle(ctx, b)
		}()
	}
	wg.Wait()
}

// SupportedCandleIntervals are the timeframes the chart may request.
//
// Only 60s buckets are persisted. Anything coarser is folded from those;
// anything finer is rebuilt from the retained tick history, which is why the
// sub-minute frames reach back only as far as TickHistoryRetentionMs.
var SupportedCandleIntervals = []int64{5, 15, 30, 60, 300, 900, 1800, 3600, 10800, 86400}

// IsSupportedCandleInterval reports membership of SupportedCandleIntervals.
func IsSupportedCandleInterval(seconds int64) bool {
	return slices.Contains(SupportedCandleIntervals, seconds)
}

// foldCandles folds an ascending series into wider buckets. Open is the first
// source open in the bucket and close the last source close, so a folded
// candle carries the same open/close the raw series would have produced over
// that span - it is a regrouping, not a re-derivation.
func foldCandles(source []models.CandleView, bucketMs int64) []models.CandleView {
	out := []models.CandleView{}
	for _, c := range source {
		openTime := floorDiv(c.Time, bucketMs) * bucketMs
		if n := len(out); n > 0 && out[n-1].Time == openTime {
			cur := &out[n-1]
			cur.High = math.Max(cur.High, c.High)
			cur.Low = math.Min(cur.Low, c.Low)
			cur.Close = c.Close
		} else {
			out = append(out, models.CandleView{Time: openTime, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close})
		}
	}
	return out
}

func lastN[T any](s []T, n int64) []T {
	if n >= 0 && int64(len(s)) > n {
		return s[int64(len(s))-n:]
	}
	return s
}

// candlesFromTicks rebuilds sub-minute frames from raw ticks - no such
// candles are stored. toMs (optional) drops the ticks after it.
func candlesFromTicks(ctx context.Context, assetID string, intervalSeconds, limit int64, toMs *int64) ([]models.CandleView, error) {
	ticks, err := GetTicksBetween(ctx, assetID, util.NowMs()-config.TickHistoryRetentionMs, toMs)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(ticks, func(i, j int) bool { return ticks[i].Timestamp < ticks[j].Timestamp })
	points := make([]models.CandleView, len(ticks))
	for i, t := range ticks {
		points[i] = models.CandleView{Time: t.Timestamp, Open: t.Price, High: t.Price, Low: t.Price, Close: t.Price}
	}
	return lastN(foldCandles(points, intervalSeconds*1000), limit), nil
}

// GetCandles is the OHLC series for the chart. limit counts candles at the
// requested interval, so a 5m request reads five times as many stored minutes
// as it returns. TS defaults: limit 300, interval 60.
//
// toMs (optional) ends the series in the past: the last limit candles whose
// openTime <= toMs, built only from data up to toMs (stored minutes that open
// by then, or ticks at or before it), so a wider candle holding toMs is the
// one the chart showed at that moment. Without it the series ends now and
// includes the candle still being built.
func GetCandles(ctx context.Context, assetID string, limit, intervalSeconds int64, toMs *int64) ([]models.CandleView, error) {
	asset, err := assets.RequireAssetDoc(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if intervalSeconds < CandleIntervalSeconds {
		return candlesFromTicks(ctx, assetID, intervalSeconds, limit, toMs)
	}

	perBucket := max(1, int64(util.RoundHalfUp(float64(intervalSeconds)/CandleIntervalSeconds)))
	filter := bson.M{"assetId": asset.ID, "interval": CandleIntervalSeconds}
	if toMs != nil {
		filter["openTime"] = bson.M{"$lte": *toMs}
	}
	cur, err := mongox.C(models.CollCandles).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "openTime", Value: -1}}).SetLimit(min(limit*perBucket, 5000)))
	if err != nil {
		return nil, err
	}
	var stored []models.Candle
	if err := cur.All(ctx, &stored); err != nil {
		return nil, err
	}
	minutes := make([]models.CandleView, 0, len(stored)+1)
	for i := len(stored) - 1; i >= 0; i-- {
		minutes = append(minutes, models.ToCandleView(&stored[i]))
	}

	// Include the bucket still being built so the chart is current.
	candleMu.Lock()
	if open := openCandles[assetID]; open != nil && (toMs == nil || open.openTime <= *toMs) &&
		(len(minutes) == 0 || minutes[len(minutes)-1].Time != open.openTime) {
		minutes = append(minutes, models.CandleView{Time: open.openTime, Open: open.open, High: open.high, Low: open.low, Close: open.close})
	}
	candleMu.Unlock()

	if perBucket == 1 {
		return lastN(minutes, limit), nil
	}
	return lastN(foldCandles(minutes, intervalSeconds*1000), limit), nil
}
