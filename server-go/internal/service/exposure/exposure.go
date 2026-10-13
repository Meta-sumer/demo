// Package exposure is the exposure engine (spec section 11).
//
// Counters live in Redis so a trade updates them in a single round trip; the
// `exposures` collection is the durable snapshot, refreshed by the risk worker
// and rebuilt from open trades at boot. Redis is a cache of a fact that MongoDB
// owns, never the fact itself (rule 6).
//
// Definitions, all in minor units:
//
//	upStake / downStake  - staked volume on each side
//	netExposure          - upStake - downStake (which way the book leans)
//	grossExposure        - upStake + downStake (total at risk in the book)
//	upPotentialPayout    - stake + profit credited if every UP trade wins
//	potentialLiability   - the platform's worst-case net loss across both
//	                       outcomes: UP profits are only a loss to the extent
//	                       they exceed the losing side's forfeited stakes.
package exposure

import (
	"context"
	"math"
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
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
)

// RawCounters are the Redis hash counters for one asset.
type RawCounters struct {
	UpStake    int64
	DownStake  int64
	UpProfit   int64
	DownProfit int64
	OpenTrades int64
}

type ExposureDelta struct {
	AssetID   string
	Symbol    string
	UserID    string
	Direction models.Direction
	Stake     int64
	Profit    int64
	// +1 when a trade opens, -1 when it settles or is cancelled.
	Sign int64
}

// ApplyExposureDelta updates the asset and user counters in one pipeline and
// returns the resulting snapshot.
func ApplyExposureDelta(ctx context.Context, delta ExposureDelta) (models.ExposureSnapshot, error) {
	key := config.RedisKeys.Exposure(delta.AssetID)
	stakeField, profitField := "downStake", "downProfit"
	if delta.Direction == models.DirectionUp {
		stakeField, profitField = "upStake", "upProfit"
	}
	userKey := config.RedisKeys.UserExposure(delta.UserID)

	pipe := redisx.R().Pipeline()
	pipe.SAdd(ctx, config.RedisKeys.ExposureAssets, delta.AssetID)
	pipe.HIncrBy(ctx, key, stakeField, delta.Sign*delta.Stake)
	pipe.HIncrBy(ctx, key, profitField, delta.Sign*delta.Profit)
	pipe.HIncrBy(ctx, key, "openTrades", delta.Sign)
	pipe.HSet(ctx, key, "symbol", delta.Symbol, "updatedAt", strconv.FormatInt(util.NowMs(), 10))
	pipe.HIncrBy(ctx, userKey, delta.AssetID, delta.Sign*delta.Stake)
	pipe.HIncrBy(ctx, userKey, "total", delta.Sign*delta.Stake)
	if _, err := pipe.Exec(ctx); err != nil {
		return models.ExposureSnapshot{}, err
	}

	snapshot, err := GetExposure(ctx, delta.AssetID, delta.Symbol)
	if err != nil {
		return models.ExposureSnapshot{}, err
	}
	ws.Emit([]string{config.ChAdminRisk}, config.EvExposureUpdated, snapshot)
	// Signal the hedge worker that this asset needs re-evaluating.
	assetID := delta.AssetID
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := redisx.R().LPush(c, config.RedisKeys.HedgeQueue, assetID).Err(); err != nil {
			logx.Warn("failed to queue hedge evaluation", "assetId", assetID, "err", err.Error())
		}
	}()
	return snapshot, nil
}

// num mirrors JS Number() on a Redis string: missing -> 0.
func num(raw map[string]string, field string) int64 {
	s, ok := raw[field]
	if !ok {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) {
		return 0
	}
	return int64(f)
}

func countersFrom(raw map[string]string) RawCounters {
	return RawCounters{
		UpStake:    num(raw, "upStake"),
		DownStake:  num(raw, "downStake"),
		UpProfit:   num(raw, "upProfit"),
		DownProfit: num(raw, "downProfit"),
		OpenTrades: num(raw, "openTrades"),
	}
}

func updatedAtFrom(raw map[string]string, fallback int64) int64 {
	if _, ok := raw["updatedAt"]; !ok {
		return fallback
	}
	return num(raw, "updatedAt")
}

func readCounters(ctx context.Context, assetID string) (RawCounters, string, int64, error) {
	raw, err := redisx.R().HGetAll(ctx, config.RedisKeys.Exposure(assetID)).Result()
	if err != nil {
		return RawCounters{}, "", 0, err
	}
	now := util.NowMs()
	if len(raw) == 0 {
		return RawCounters{}, "", now, nil
	}
	return countersFrom(raw), raw["symbol"], updatedAtFrom(raw, now), nil
}

// DeriveExposure is the pure derivation, unit tested without Redis. TS
// defaults updatedAt to Date.now(); pass util.NowMs().
func DeriveExposure(assetID, symbol string, c RawCounters, updatedAt int64) models.ExposureSnapshot {
	upStake := max(0, c.UpStake)
	downStake := max(0, c.DownStake)
	upProfit := max(0, c.UpProfit)
	downProfit := max(0, c.DownProfit)

	return models.ExposureSnapshot{
		AssetID:             assetID,
		Symbol:              symbol,
		UpStake:             upStake,
		DownStake:           downStake,
		NetExposure:         upStake - downStake,
		GrossExposure:       upStake + downStake,
		UpPotentialPayout:   upStake + upProfit,
		DownPotentialPayout: downStake + downProfit,
		PotentialLiability:  max(0, upProfit-downStake, downProfit-upStake),
		OpenTrades:          max(0, c.OpenTrades),
		UpdatedAt:           updatedAt,
	}
}

// GetExposure reads one asset's snapshot. symbolHint, when given, overrides
// the symbol stored in the hash.
func GetExposure(ctx context.Context, assetID string, symbolHint ...string) (models.ExposureSnapshot, error) {
	counters, symbol, updatedAt, err := readCounters(ctx, assetID)
	if err != nil {
		return models.ExposureSnapshot{}, err
	}
	if len(symbolHint) > 0 {
		symbol = symbolHint[0]
	}
	return DeriveExposure(assetID, symbol, counters, updatedAt), nil
}

// GetAllExposures returns every asset in one round trip.
//
// This sits on the trade path - the pre-trade risk check needs the platform
// total - so it must not cost one Redis call per asset. A pipeline keeps it at
// a single round trip however many assets are listed.
func GetAllExposures(ctx context.Context) ([]models.ExposureSnapshot, error) {
	list, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]models.ExposureSnapshot, 0, len(list))
	if len(list) == 0 {
		return out, nil
	}

	pipe := redisx.R().Pipeline()
	cmds := make([]*redis.MapStringStringCmd, len(list))
	for i, a := range list {
		cmds[i] = pipe.HGetAll(ctx, config.RedisKeys.Exposure(a.ID.Hex()))
	}
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		return nil, err
	}

	at := util.NowMs()
	for i, a := range list {
		raw := cmds[i].Val()
		if raw == nil {
			raw = map[string]string{}
		}
		out = append(out, DeriveExposure(a.ID.Hex(), a.Symbol, countersFrom(raw), updatedAtFrom(raw, at)))
	}
	return out, nil
}

type PortfolioExposure struct {
	TotalGrossExposure      int64                     `json:"totalGrossExposure"`
	TotalNetExposure        int64                     `json:"totalNetExposure"`
	TotalPotentialLiability int64                     `json:"totalPotentialLiability"`
	TotalOpenTrades         int64                     `json:"totalOpenTrades"`
	Assets                  []models.ExposureSnapshot `json:"assets"`
}

func GetPortfolioExposure(ctx context.Context) (PortfolioExposure, error) {
	list, err := GetAllExposures(ctx)
	if err != nil {
		return PortfolioExposure{}, err
	}
	p := PortfolioExposure{Assets: list}
	for _, a := range list {
		p.TotalGrossExposure += a.GrossExposure
		p.TotalNetExposure += a.NetExposure
		p.TotalPotentialLiability += a.PotentialLiability
		p.TotalOpenTrades += a.OpenTrades
	}
	return p, nil
}

type UserExposure struct {
	Total    int64            `json:"total"`
	PerAsset map[string]int64 `json:"perAsset"`
}

func GetUserExposure(ctx context.Context, userID string) (UserExposure, error) {
	raw, err := redisx.R().HGetAll(ctx, config.RedisKeys.UserExposure(userID)).Result()
	if err != nil {
		return UserExposure{}, err
	}
	out := UserExposure{PerAsset: map[string]int64{}}
	var total int64
	for field := range raw {
		amount := num(raw, field)
		if field == "total" {
			total = amount
			continue
		}
		if amount != 0 {
			out.PerAsset[field] = amount
		}
	}
	out.Total = max(0, total)
	return out, nil
}

// PersistExposure writes the Redis snapshot through to the durable
// collection. Failures are logged, never returned (as in TS).
func PersistExposure(ctx context.Context, snapshot models.ExposureSnapshot, hedgedNotional int64) {
	assetID, ok := mongox.ParseID(snapshot.AssetID)
	if !ok {
		logx.Error("failed to persist exposure snapshot", "assetId", snapshot.AssetID, "err", "invalid asset id")
		return
	}
	now := mongox.Now()
	_, err := mongox.C(models.CollExposures).UpdateOne(ctx,
		bson.M{"assetId": assetID},
		bson.M{
			"$set": bson.M{
				"symbol":              snapshot.Symbol,
				"upStake":             snapshot.UpStake,
				"downStake":           snapshot.DownStake,
				"netExposure":         snapshot.NetExposure,
				"grossExposure":       snapshot.GrossExposure,
				"upPotentialPayout":   snapshot.UpPotentialPayout,
				"downPotentialPayout": snapshot.DownPotentialPayout,
				"potentialLiability":  snapshot.PotentialLiability,
				"openTrades":          snapshot.OpenTrades,
				"hedgedNotional":      hedgedNotional,
				"updatedAt":           now,
			},
			"$setOnInsert": bson.M{"createdAt": now},
		},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		logx.Error("failed to persist exposure snapshot", "assetId", snapshot.AssetID, "err", err.Error())
	}
}

// PersistAllExposures flushes every asset's live counters into the durable
// snapshot.
func PersistAllExposures(ctx context.Context, hedgedByAsset map[string]int64) error {
	snapshots, err := GetAllExposures(ctx)
	if err != nil {
		return err
	}
	for _, s := range snapshots {
		PersistExposure(ctx, s, hedgedByAsset[s.AssetID])
	}
	return nil
}

var openStatuses = bson.M{"$in": bson.A{string(models.TradeStatusOpen), string(models.TradeStatusSettling)}}

// RebuildExposureFromDatabase rebuilds the Redis counters from the open
// trades in MongoDB. Run at boot and after any Redis incident: the durable
// trade ledger is the source of truth, the cache is disposable.
func RebuildExposureFromDatabase(ctx context.Context) (int, error) {
	// DEMO trades are not part of the house book.
	cur, err := mongox.C(models.CollBinaryTrades).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"status": openStatuses, "accountType": models.NotDemo()}},
		bson.M{"$group": bson.M{
			"_id":    bson.M{"assetId": "$assetId", "direction": "$direction"},
			"stake":  bson.M{"$sum": "$stake"},
			"profit": bson.M{"$sum": "$profit"},
			"count":  bson.M{"$sum": 1},
			"symbol": bson.M{"$first": "$symbol"},
		}},
	})
	if err != nil {
		return 0, err
	}
	var grouped []struct {
		ID struct {
			AssetID   bson.ObjectID    `bson:"assetId"`
			Direction models.Direction `bson:"direction"`
		} `bson:"_id"`
		Stake  int64  `bson:"stake"`
		Profit int64  `bson:"profit"`
		Count  int64  `bson:"count"`
		Symbol string `bson:"symbol"`
	}
	if err := cur.All(ctx, &grouped); err != nil {
		return 0, err
	}

	type entry struct {
		symbol   string
		counters RawCounters
	}
	perAsset := map[string]*entry{}
	for _, row := range grouped {
		id := row.ID.AssetID.Hex()
		e := perAsset[id]
		if e == nil {
			e = &entry{symbol: row.Symbol}
			perAsset[id] = e
		}
		if row.ID.Direction == models.DirectionUp {
			e.counters.UpStake += row.Stake
			e.counters.UpProfit += row.Profit
		} else {
			e.counters.DownStake += row.Stake
			e.counters.DownProfit += row.Profit
		}
		e.counters.OpenTrades += row.Count
	}

	list, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return 0, err
	}
	pipe := redisx.R().Pipeline()
	for _, a := range list {
		id := a.ID.Hex()
		e := perAsset[id]
		if e == nil {
			e = &entry{symbol: a.Symbol}
		}
		key := config.RedisKeys.Exposure(id)
		pipe.Del(ctx, key)
		pipe.HSet(ctx, key, map[string]any{
			"upStake":    strconv.FormatInt(e.counters.UpStake, 10),
			"downStake":  strconv.FormatInt(e.counters.DownStake, 10),
			"upProfit":   strconv.FormatInt(e.counters.UpProfit, 10),
			"downProfit": strconv.FormatInt(e.counters.DownProfit, 10),
			"openTrades": strconv.FormatInt(e.counters.OpenTrades, 10),
			"symbol":     e.symbol,
			"updatedAt":  strconv.FormatInt(util.NowMs(), 10),
		})
		pipe.SAdd(ctx, config.RedisKeys.ExposureAssets, id)
	}
	if len(list) > 0 {
		if _, err := pipe.Exec(ctx); err != nil {
			return 0, err
		}
	}

	if err := rebuildUserExposure(ctx); err != nil {
		return 0, err
	}
	logx.Info("exposure counters rebuilt from the trade ledger", "assets", len(list))
	return len(list), nil
}

func rebuildUserExposure(ctx context.Context) error {
	cur, err := mongox.C(models.CollBinaryTrades).Aggregate(ctx, bson.A{
		bson.M{"$match": bson.M{"status": openStatuses, "accountType": models.NotDemo()}},
		bson.M{"$group": bson.M{
			"_id":   bson.M{"userId": "$userId", "assetId": "$assetId"},
			"stake": bson.M{"$sum": "$stake"},
		}},
	})
	if err != nil {
		return err
	}
	var grouped []struct {
		ID struct {
			UserID  bson.ObjectID `bson:"userId"`
			AssetID bson.ObjectID `bson:"assetId"`
		} `bson:"_id"`
		Stake int64 `bson:"stake"`
	}
	if err := cur.All(ctx, &grouped); err != nil {
		return err
	}

	perUser := map[string]map[string]int64{}
	for _, row := range grouped {
		u := row.ID.UserID.Hex()
		if perUser[u] == nil {
			perUser[u] = map[string]int64{}
		}
		perUser[u][row.ID.AssetID.Hex()] = row.Stake
	}

	rdb := redisx.R()
	existing, err := rdb.Keys(ctx, config.RedisKeys.UserExposure("*")).Result()
	if err != nil {
		return err
	}
	pipe := rdb.Pipeline()
	for _, k := range existing {
		pipe.Del(ctx, k)
	}
	for userID, stakes := range perUser {
		fields := make(map[string]any, len(stakes)+1)
		var total int64
		for assetID, stake := range stakes {
			fields[assetID] = strconv.FormatInt(stake, 10)
			total += stake
		}
		fields["total"] = strconv.FormatInt(total, 10)
		pipe.HSet(ctx, config.RedisKeys.UserExposure(userID), fields)
	}
	if pipe.Len() == 0 {
		return nil
	}
	_, err = pipe.Exec(ctx)
	return err
}
