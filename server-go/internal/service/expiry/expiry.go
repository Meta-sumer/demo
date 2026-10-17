// Package expiry is the expiry engine (spec section 17).
//
// The expiry queue is a Redis sorted set scored by the time a trade is next
// due for settlement (its expiry, or a later retry time set by settlement), so
// finding what is due is one O(log n) range read rather than a table scan.
//
// A trade is only handed to settlement once a tick stamped after its expiry
// has been observed, or the confirmation window has elapsed
// (settlement.ExpiryConfirmed). Without that ordering a tick still in flight
// would be missed.
//
// Concurrency (F-14). The expiry loop is leader-elected, but leadership can
// briefly overlap (a paused leader whose lock lapsed). Every queue mutation it
// makes therefore runs in a Redis script that first checks the leader token
// (leader.Fence), so a former leader's hand-off is refused atomically. The
// hand-off itself is a move - ZREM from the expiry queue and LPUSH onto the
// settlement queue in one step, and only if the entry's score is still the
// one that was read - so an entry settlement has just rescheduled is never
// dropped. None of this is needed for correctness of outcomes (settlement is
// guarded by the trade's state machine); it keeps the queues tidy and
// prevents duplicate work and duplicate trade.expiring events.
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
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/settlement"
)

// ExpiryTickGraceMs: the default confirmation window - how long to wait for a
// post-expiry tick before handing a trade to settlement anyway. The effective
// value is settlement policy (ConfirmationWindowMs).
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
	// The queue score the entry was read with (when it became due).
	DueAt int64 `json:"dueAt"`
}

var liveStatuses = bson.A{string(models.TradeStatusOpen), string(models.TradeStatusSettling)}

// Scripts. KEYS[last] is the leader lock key; ARGV[1] is "1" when the call is
// fenced and ARGV[2] the token that must still hold it. A refused fence
// returns -1 and changes nothing.
var (
	// Move due entries whose score is unchanged to the settlement queue.
	handOffScript = redis.NewScript(`
if ARGV[1] == '1' and redis.call('get', KEYS[3]) ~= ARGV[2] then return -1 end
local moved = {}
for i = 3, #ARGV, 2 do
  local s = redis.call('zscore', KEYS[1], ARGV[i])
  if s and tonumber(s) == tonumber(ARGV[i + 1]) then
    redis.call('zrem', KEYS[1], ARGV[i])
    redis.call('lpush', KEYS[2], ARGV[i])
    moved[#moved + 1] = ARGV[i]
  end
end
return moved`)

	// Drop entries whose score is unchanged (orphans: no live trade).
	dropScript = redis.NewScript(`
if ARGV[1] == '1' and redis.call('get', KEYS[2]) ~= ARGV[2] then return -1 end
local n = 0
for i = 3, #ARGV, 2 do
  local s = redis.call('zscore', KEYS[1], ARGV[i])
  if s and tonumber(s) == tonumber(ARGV[i + 1]) then
    redis.call('zrem', KEYS[1], ARGV[i])
    n = n + 1
  end
end
return n`)

	// Push ids onto the settlement queue.
	pushScript = redis.NewScript(`
if ARGV[1] == '1' and redis.call('get', KEYS[2]) ~= ARGV[2] then return -1 end
for i = 3, #ARGV do redis.call('lpush', KEYS[1], ARGV[i]) end
return #ARGV - 2`)
)

// fenceArgs returns the lock key and the leading ARGV for a script. Outside a
// leader loop (tests, manual runs) the call is unfenced.
func fenceArgs(ctx context.Context) (string, []any, error) {
	f := leader.FenceFromContext(ctx)
	if f == nil {
		return config.RedisKeys.ExpiryQueue, []any{"0", ""}, nil
	}
	if !f.LeaseValid() {
		return "", nil, leader.ErrLeadershipLost
	}
	return f.Key, []any{"1", f.Token}, nil
}

func fenced(res any) error {
	if n, ok := res.(int64); ok && n == -1 {
		return leader.ErrLeadershipLost
	}
	return nil
}

// FindDueTrades reads due ids from the queue and drops those that no longer
// correspond to an open trade.
func FindDueTrades(ctx context.Context, at int64, limit int64) ([]DueTrade, error) {
	entries, err := redisx.R().ZRangeByScoreWithScores(ctx, config.RedisKeys.ExpiryQueue, &redis.ZRangeBy{
		Min: "-inf", Max: strconv.FormatInt(at, 10), Offset: 0, Count: limit,
	}).Result()
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return []DueTrade{}, nil
	}

	oids := make(bson.A, 0, len(entries))
	scores := make(map[string]int64, len(entries))
	malformed := []redis.Z{}
	for _, e := range entries {
		id, _ := e.Member.(string)
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			malformed = append(malformed, e)
			continue
		}
		oids = append(oids, oid)
		scores[id] = int64(e.Score)
	}

	var trades []struct {
		ID              bson.ObjectID `bson:"_id"`
		AssetID         bson.ObjectID `bson:"assetId"`
		Symbol          string        `bson:"symbol"`
		ExpiryTimestamp int64         `bson:"expiryTimestamp"`
		UserID          bson.ObjectID `bson:"userId"`
	}
	if len(oids) > 0 {
		cur, err := mongox.C(models.CollBinaryTrades).Find(ctx,
			bson.M{"_id": bson.M{"$in": oids}, "status": bson.M{"$in": liveStatuses}},
			options.Find().SetProjection(bson.M{"assetId": 1, "symbol": 1, "expiryTimestamp": 1, "userId": 1}))
		if err != nil {
			return nil, err
		}
		if err := cur.All(ctx, &trades); err != nil {
			return nil, err
		}
	}

	// Ids in the queue that no longer correspond to a live trade are stale.
	found := make(map[string]bool, len(trades))
	for _, t := range trades {
		found[t.ID.Hex()] = true
	}
	orphans := malformed
	for _, e := range entries {
		id, _ := e.Member.(string)
		if _, valid := scores[id]; valid && !found[id] {
			orphans = append(orphans, e)
		}
	}
	if len(orphans) > 0 {
		lockKey, args, err := fenceArgs(ctx)
		if err != nil {
			return nil, err
		}
		for _, o := range orphans {
			args = append(args, o.Member, strconv.FormatInt(int64(o.Score), 10))
		}
		res, err := dropScript.Run(ctx, redisx.R(), []string{config.RedisKeys.ExpiryQueue, lockKey}, args...).Result()
		if err != nil {
			return nil, err
		}
		if err := fenced(res); err != nil {
			return nil, err
		}
	}

	out := make([]DueTrade, 0, len(trades))
	for _, t := range trades {
		id := t.ID.Hex()
		out = append(out, DueTrade{
			TradeID:         id,
			AssetID:         t.AssetID.Hex(),
			Symbol:          t.Symbol,
			ExpiryTimestamp: t.ExpiryTimestamp,
			UserID:          t.UserID.Hex(),
			DueAt:           scores[id],
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
	if _, _, err := fenceArgs(ctx); err != nil {
		return TickResult{}, err
	}
	due, err := FindDueTrades(ctx, at, limit)
	if err != nil {
		return TickResult{}, err
	}
	if len(due) == 0 {
		return TickResult{}, nil
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return TickResult{}, err
	}
	policy, err := settlement.PolicyFor(ctx, cfg)
	if err != nil {
		return TickResult{}, err
	}

	ready := make([]DueTrade, 0, len(due))
	waiting := 0
	for _, trade := range due {
		confirmed, err := settlement.ExpiryConfirmed(ctx, trade.AssetID, trade.ExpiryTimestamp, at, policy)
		if err != nil {
			return TickResult{}, err
		}
		if !confirmed {
			waiting++
			continue
		}
		ready = append(ready, trade)
	}
	if len(ready) == 0 {
		return TickResult{Due: len(due), Waiting: waiting}, nil
	}

	lockKey, args, err := fenceArgs(ctx)
	if err != nil {
		return TickResult{}, err
	}
	byID := make(map[string]DueTrade, len(ready))
	for _, t := range ready {
		byID[t.TradeID] = t
		args = append(args, t.TradeID, strconv.FormatInt(t.DueAt, 10))
	}
	res, err := handOffScript.Run(ctx, redisx.R(),
		[]string{config.RedisKeys.ExpiryQueue, config.RedisKeys.SettlementQueue, lockKey}, args...).Result()
	if err != nil {
		return TickResult{}, err
	}
	if err := fenced(res); err != nil {
		return TickResult{}, err
	}
	moved, _ := res.([]any)

	for _, m := range moved {
		id, _ := m.(string)
		trade, ok := byID[id]
		if !ok {
			continue
		}
		ws.Emit([]string{config.ChUser(trade.UserID)}, config.EvTradeExpiring, map[string]any{
			"tradeId":         trade.TradeID,
			"symbol":          trade.Symbol,
			"expiryTimestamp": trade.ExpiryTimestamp,
		})
		metrics.RecordLatency(config.LatExpiryDetection, float64(at-trade.ExpiryTimestamp))
	}
	return TickResult{Due: len(due), Queued: len(moved), Waiting: waiting}, nil
}

// SweepMissedExpiries is the safety net that rebuilds the queues from the
// trades themselves: anything past expiry that is still OPEN, plus anything
// left in SETTLING (an interrupted settlement or early close). Duplicates are
// harmless - settlement is idempotent and refuses to act early.
func SweepMissedExpiries(ctx context.Context, at int64, limit int64) (int, error) {
	if _, _, err := fenceArgs(ctx); err != nil {
		return 0, err
	}
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

	lockKey, args, err := fenceArgs(ctx)
	if err != nil {
		return 0, err
	}
	for _, t := range missed {
		args = append(args, t.ID.Hex())
	}
	res, err := pushScript.Run(ctx, redisx.R(), []string{config.RedisKeys.SettlementQueue, lockKey}, args...).Result()
	if err != nil {
		return 0, err
	}
	if err := fenced(res); err != nil {
		return 0, err
	}

	logx.Warn("requeued trades missed by the expiry queue", "count", len(missed))
	return len(missed), nil
}
