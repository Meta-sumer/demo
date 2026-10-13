package marketdata

import (
	"context"
	"math"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
)

// 24h movement shown on the asset list. It is decoration, not a trading
// input: it is computed for every asset at once, cached briefly, and any
// failure degrades to null rather than slowing or failing the asset list.

const (
	change24hTTL     = 60 * time.Second
	change24hTimeout = 3 * time.Second
	dayMs            = int64(24 * 60 * 60 * 1000)
)

// Change24hPercent is (last - reference) / reference x 100, rounded to two
// decimals; nil when either price is missing or the reference is not positive.
func Change24hPercent(last, reference *float64) *float64 {
	if last == nil || reference == nil || *reference <= 0 || math.IsNaN(*last) || math.IsInf(*last, 0) {
		return nil
	}
	pct := (*last - *reference) / *reference * 100
	rounded := util.RoundHalfUp(pct*100) / 100
	if rounded == 0 {
		rounded = 0 // no negative zero in JSON
	}
	return &rounded
}

var change24hCache struct {
	mu        sync.Mutex
	values    map[string]*float64
	fetchedAt time.Time
}

// GetChange24h returns the 24h change per asset id (missing = null). Never
// fails: errors are logged and yield an empty map.
func GetChange24h(ctx context.Context) map[string]*float64 {
	c := &change24hCache
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.values != nil && time.Since(c.fetchedAt) < change24hTTL {
		return c.values
	}
	values, err := computeChange24h(ctx)
	if err != nil {
		logx.Warn("24h change computation failed", "err", err.Error())
		if c.values != nil {
			return c.values // serve the stale values rather than nothing
		}
		values = map[string]*float64{}
	}
	c.values, c.fetchedAt = values, time.Now()
	return values
}

func computeChange24h(parent context.Context) (map[string]*float64, error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), change24hTimeout)
	defer cancel()

	// The reference is the open of the earliest candle inside the window.
	since := util.NowMs() - dayMs
	cur, err := mongox.C(models.CollCandles).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"interval": CandleIntervalSeconds, "openTime": bson.M{"$gte": since}}},
		bson.M{"$sort": bson.D{{Key: "assetId", Value: 1}, {Key: "openTime", Value: 1}}},
		bson.M{"$group": bson.M{"_id": "$assetId", "open": bson.M{"$first": "$open"}}},
	})
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID   bson.ObjectID `bson:"_id"`
		Open float64       `bson:"open"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}

	out := make(map[string]*float64, len(rows))
	for _, row := range rows {
		assetID := row.ID.Hex()
		tick, err := GetLastTick(ctx, assetID)
		if err != nil {
			return nil, err
		}
		if tick == nil {
			continue
		}
		last, ref := tick.Price, row.Open
		if pct := Change24hPercent(&last, &ref); pct != nil {
			out[assetID] = pct
		}
	}
	return out, nil
}
