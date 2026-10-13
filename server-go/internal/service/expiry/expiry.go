// Package expiry is the expiry engine (spec section 17).
//
// The expiry queue is a Redis sorted set scored by expiry time, so finding
// what is due is one O(log n) range read rather than a table scan.
//
// A trade is only handed to settlement once a tick timestamped at or after
// its expiry has been observed, or the grace window has elapsed. Without that
// ordering a tick still in flight would be missed and the trade would settle
// on a price older than the expiry instant.
package expiry

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/settlement"
)

// ExpiryTickGraceMs: how long to wait for a post-expiry tick before settling
// on what we have.
const ExpiryTickGraceMs = 2000

// Defaults of the TS functions' optional parameters.
const (
	DefaultDueLimit   = 200
	DefaultSweepLimit = 500
)

type DueTrade struct {
	TradeID         string `json:"tradeId"`
	AssetID         string `json:"assetId"`
	Symbol          string `json:"symbol"`
	ExpiryTimestamp int64  `json:"expiryTimestamp"`
	UserID          string `json:"userId"`
}

var liveStatuses = bson.A{string(models.TradeStatusOpen), string(models.TradeStatusSettling)}

// FindDueTrades reads due ids from the queue and drops those that no longer
// correspond to an open trade.
func FindDueTrades(ctx context.Context, at int64, limit int64) ([]DueTrade, error) {
	ids, err := redisx.R().ZRangeByScore(ctx, config.RedisKeys.ExpiryQueue, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(at, 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return []DueTrade{}, nil
	}

	oids := make(bson.A, 0, len(ids))
	for _, id := range ids {
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			return nil, err
		}
		oids = append(oids, oid)
	}

	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
		bson.M{"_id": bson.M{"$in": oids}, "status": bson.M{"$in": liveStatuses}},
		options.Find().SetProjection(bson.M{"assetId": 1, "symbol": 1, "expiryTimestamp": 1, "userId": 1}))
	if err != nil {
		return nil, err
	}
	var trades []struct {
		ID              bson.ObjectID `bson:"_id"`
		AssetID         bson.ObjectID `bson:"assetId"`
		Symbol          string        `bson:"symbol"`
		ExpiryTimestamp int64         `bson:"expiryTimestamp"`
		UserID          bson.ObjectID `bson:"userId"`
	}
	if err := cur.All(ctx, &trades); err != nil {
		return nil, err
	}

	// Ids in the queue that no longer correspond to an open trade are stale.
	found := make(map[string]bool, len(trades))
	for _, t := range trades {
		found[t.ID.Hex()] = true
	}
	orphans := []any{}
	for _, id := range ids {
		if !found[id] {
			orphans = append(orphans, id)
		}
	}
	if len(orphans) > 0 {
		if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, orphans...).Err(); err != nil {
			return nil, err
		}
	}

	out := make([]DueTrade, 0, len(trades))
	for _, t := range trades {
		out = append(out, DueTrade{
			TradeID:         t.ID.Hex(),
			AssetID:         t.AssetID.Hex(),
			Symbol:          t.Symbol,
			ExpiryTimestamp: t.ExpiryTimestamp,
			UserID:          t.UserID.Hex(),
		})
	}
	return out, nil
}

type TickResult struct {
	Due     int `json:"due"`
	Queued  int `json:"queued"`
	Waiting int `json:"waiting"`
}

// ProcessExpiries is one pass of the expiry worker: due trades are handed to
// the settlement queue; nothing is settled here.
func ProcessExpiries(ctx context.Context, at int64, limit int64) (TickResult, error) {
	due, err := FindDueTrades(ctx, at, limit)
	if err != nil {
		return TickResult{}, err
	}
	queued, waiting := 0, 0

	for _, trade := range due {
		elapsed := at - trade.ExpiryTimestamp
		if elapsed < ExpiryTickGraceMs {
			confirmed, err := marketdata.HasTickAtOrAfter(ctx, trade.AssetID, trade.ExpiryTimestamp)
			if err != nil {
				return TickResult{}, err
			}
			if !confirmed {
				waiting++
				continue
			}
		}

		ws.Emit([]string{config.ChUser(trade.UserID)}, config.EvTradeExpiring, map[string]any{
			"tradeId":         trade.TradeID,
			"symbol":          trade.Symbol,
			"expiryTimestamp": trade.ExpiryTimestamp,
		})

		if err := redisx.R().LPush(ctx, config.RedisKeys.SettlementQueue, trade.TradeID).Err(); err != nil {
			return TickResult{}, err
		}
		// Remove it now so a second pass cannot enqueue the same trade;
		// settlement is idempotent anyway, and the sweeper re-queues anything
		// left behind.
		if err := redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, trade.TradeID).Err(); err != nil {
			return TickResult{}, err
		}

		metrics.RecordLatency(config.LatExpiryDetection, float64(elapsed))
		queued++
	}

	return TickResult{Due: len(due), Queued: queued, Waiting: waiting}, nil
}

// SweepMissedExpiries is the safety net for trades the queue lost: anything
// past expiry that is still OPEN, plus anything stuck in SETTLING after a
// worker crash.
func SweepMissedExpiries(ctx context.Context, at int64, limit int64) (int, error) {
	staleBefore := time.UnixMilli(at - settlement.StaleSettlingMs).UTC()
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
		bson.M{"$or": bson.A{
			bson.M{"status": string(models.TradeStatusOpen), "expiryTimestamp": bson.M{"$lte": at - ExpiryTickGraceMs}},
			bson.M{"status": string(models.TradeStatusSettling), "updatedAt": bson.M{"$lt": staleBefore}},
		}},
		options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(limit))
	if err != nil {
		return 0, err
	}
	var missed []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &missed); err != nil {
		return 0, err
	}
	if len(missed) == 0 {
		return 0, nil
	}

	pipe := redisx.R().Pipeline()
	for _, t := range missed {
		pipe.LPush(ctx, config.RedisKeys.SettlementQueue, t.ID.Hex())
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}

	logx.Warn("requeued trades missed by the expiry queue", "count", len(missed))
	return len(missed), nil
}
