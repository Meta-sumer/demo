// Package exposure is the exposure engine (spec section 11).
//
// Counters live in Redis so a trade updates them in a single round trip; the
// `exposures` collection is the durable snapshot, refreshed by the risk worker.
// Redis is a cache of a fact that MongoDB owns, never the fact itself (rule 6):
// the open LIVE trades in binary_trades are the source of truth, and the book
// (book.go) is rebuilt from them (rebuild.go) and reconciled against them
// (reconcile.go). Every counter mutation is an atomic, per-trade idempotent
// Lua script (lua.go).
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
//	expiryLiability      - the same worst case taken per expiry bucket and
//	                       summed (liability.go).
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
	// Sum of the asset's expiry-bucket liabilities, and how many buckets.
	ExpiryLiability int64
	ExpiryBuckets   int64
	// Live reservations on the asset.
	ReservedStake  int64
	ReservedTrades int64
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
	// Optional. With the trade id the delta is applied exactly once per trade
	// (a repeated release is a no-op, a release before the open is
	// remembered). Without it a release is matched to an open trade of the
	// user with the same asset, side, stake and profit, which moves the same
	// counters. ExpiryTimestamp places an opening trade in its expiry bucket.
	TradeID         string
	ExpiryTimestamp int64
}

// ApplyExposureDelta applies a trade opening (+1) or a release (-1) to the
// book through the per-trade idempotent scripts and returns the resulting
// snapshot. New LIVE trades go through Reserve/Commit instead; this remains
// the release path for settlement, early close and cancellation.
func ApplyExposureDelta(ctx context.Context, delta ExposureDelta) (models.ExposureSnapshot, error) {
	var err error
	switch {
	case delta.Sign > 0:
		id := delta.TradeID
		if id == "" {
			id = anonTradeID()
		}
		_, err = CommitTrade(ctx, OpenTrade{
			TradeID: id, AssetID: delta.AssetID, Symbol: delta.Symbol, UserID: delta.UserID,
			Direction: delta.Direction, Stake: delta.Stake, Profit: delta.Profit, ExpiryTimestamp: delta.ExpiryTimestamp,
		})
	case delta.Sign < 0:
		if delta.TradeID != "" {
			_, err = CloseTrade(ctx, delta.TradeID, delta.UserID)
		} else {
			_, err = closeByMatch(ctx, delta)
		}
	}
	if err != nil {
		return models.ExposureSnapshot{}, err
	}

	snapshot, err := GetExposure(ctx, delta.AssetID, delta.Symbol)
	if err != nil {
		return models.ExposureSnapshot{}, err
	}
	announce(snapshot)
	return snapshot, nil
}

// announce pushes the new snapshot to the admin risk channel and signals the
// hedge worker that the asset needs re-evaluating.
func announce(snapshot models.ExposureSnapshot) {
	ws.Emit([]string{config.ChAdminRisk}, config.EvExposureUpdated, snapshot)
	assetID := snapshot.AssetID
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := redisx.R().LPush(c, config.RedisKeys.HedgeQueue, assetID).Err(); err != nil {
			logx.Warn("failed to queue hedge evaluation", "assetId", assetID, "err", err.Error())
		}
	}()
}

// Notify announces an asset whose exposure just changed (after a commit),
// off the caller's path: exposure.updated to admin:risk and a hedge
// re-evaluation, as ApplyExposureDelta does.
func Notify(assetID, symbol string) {
	go func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		snapshot, err := GetExposure(c, assetID, symbol)
		if err != nil {
			logx.Warn("failed to read exposure for notification", "assetId", assetID, "err", err.Error())
			return
		}
		announce(snapshot)
	}()
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
		UpStake:         num(raw, "upStake"),
		DownStake:       num(raw, "downStake"),
		UpProfit:        num(raw, "upProfit"),
		DownProfit:      num(raw, "downProfit"),
		OpenTrades:      num(raw, "openTrades"),
		ExpiryLiability: num(raw, "bLiab"),
		ExpiryBuckets:   num(raw, "nb"),
	}
}

// sumReserved totals live reservations per asset: [stake, count].
func sumReserved(raw map[string]string) map[string][2]int64 {
	out := map[string][2]int64{}
	for _, v := range raw {
		f := splitPipe(v)
		if len(f) < 4 {
			continue
		}
		stake, _ := strconv.ParseInt(f[3], 10, 64)
		cur := out[f[0]]
		out[f[0]] = [2]int64{cur[0] + stake, cur[1] + 1}
	}
	return out
}

func updatedAtFrom(raw map[string]string, fallback int64) int64 {
	if _, ok := raw["updatedAt"]; !ok {
		return fallback
	}
	return num(raw, "updatedAt")
}

func readCounters(ctx context.Context, assetID string) (RawCounters, string, int64, error) {
	pipe := redisx.R().Pipeline()
	assetCmd := pipe.HGetAll(ctx, config.RedisKeys.Exposure(assetID))
	rsvCmd := pipe.HGetAll(ctx, BookNamespace()+":rsv")
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		return RawCounters{}, "", 0, err
	}
	raw := assetCmd.Val()
	now := util.NowMs()
	c := countersFrom(raw)
	r := sumReserved(rsvCmd.Val())[assetID]
	c.ReservedStake, c.ReservedTrades = r[0], r[1]
	if len(raw) == 0 {
		return c, "", now, nil
	}
	return c, raw["symbol"], updatedAtFrom(raw, now), nil
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
		UpWinNet:            upProfit - downStake,
		DownWinNet:          downProfit - upStake,
		ExpiryLiability:     max(0, c.ExpiryLiability),
		MaxPayoutLiability:  upProfit + downProfit,
		ExpiryBuckets:       max(0, c.ExpiryBuckets),
		ReservedStake:       max(0, c.ReservedStake),
		ReservedTrades:      max(0, c.ReservedTrades),
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
	rsvCmd := pipe.HGetAll(ctx, BookNamespace()+":rsv")
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		return nil, err
	}
	reserved := sumReserved(rsvCmd.Val())

	at := util.NowMs()
	for i, a := range list {
		raw := cmds[i].Val()
		if raw == nil {
			raw = map[string]string{}
		}
		c := countersFrom(raw)
		r := reserved[a.ID.Hex()]
		c.ReservedStake, c.ReservedTrades = r[0], r[1]
		out = append(out, DeriveExposure(a.ID.Hex(), a.Symbol, c, updatedAtFrom(raw, at)))
	}
	return out, nil
}

type PortfolioExposure struct {
	TotalGrossExposure      int64                     `json:"totalGrossExposure"`
	TotalNetExposure        int64                     `json:"totalNetExposure"`
	TotalPotentialLiability int64                     `json:"totalPotentialLiability"`
	TotalOpenTrades         int64                     `json:"totalOpenTrades"`
	Assets                  []models.ExposureSnapshot `json:"assets"`
	// Added fields: liability totals (see ExposureSnapshot), live
	// reservations, and the health of the book (READY means exact).
	TotalExpiryLiability    int64       `json:"totalExpiryLiability"`
	TotalMaxPayoutLiability int64       `json:"totalMaxPayoutLiability"`
	TotalReservedStake      int64       `json:"totalReservedStake"`
	Book                    *BookStatus `json:"book"`
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
		p.TotalExpiryLiability += a.ExpiryLiability
		p.TotalMaxPayoutLiability += a.MaxPayoutLiability
		p.TotalReservedStake += a.ReservedStake
	}
	book, err := GetBookStatus(ctx)
	if err != nil {
		return PortfolioExposure{}, err
	}
	p.Book = &book
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
				"expiryLiability":     snapshot.ExpiryLiability,
				"maxPayoutLiability":  snapshot.MaxPayoutLiability,
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
// trades in MongoDB: the durable trade ledger is the source of truth, the
// cache is disposable. It is the coordinated rebuild (RebuildBook, forced):
// lock-gated, built into temporary keys and swapped in atomically, with every
// trade accepted or released meanwhile replayed. Returns the number of
// registered assets.
func RebuildExposureFromDatabase(ctx context.Context) (int, error) {
	res, err := RebuildBook(ctx, RebuildOptions{Reason: "requested", Wait: 30 * time.Second, Force: true})
	if err != nil {
		return 0, err
	}
	return res.Assets, nil
}
