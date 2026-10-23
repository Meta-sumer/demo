package marketdata

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/testsupport"
)

// Normalisation in the ingestion path: a rejected quote never reaches market
// state, rejections are counted (not logged per tick), the EWMA lives in
// Redis, and a restart resumes from it.
func TestIngestionNormalisesAndPersists(t *testing.T) {
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()
	ResetNormalizationState()
	t.Cleanup(ResetNormalizationState)

	asset := models.Asset{ID: bson.NewObjectID(), Symbol: "TEST/NORM", Name: "Test", Category: models.AssetCategoryCrypto,
		Active: true, ProviderSymbol: "norm" + bson.NewObjectID().Hex(), Provider: "simulated", PricePrecision: 2}
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, asset); err != nil {
		t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	t.Cleanup(assets.InvalidateAssetCache)
	id := asset.ID.Hex()
	t.Cleanup(func() {
		redisx.R().Del(context.Background(), config.RedisKeys.LastTick(id), config.RedisKeys.TickHistory(id), volKey(id), QuoteRejectKey(id))
	})

	base := time.Now().UnixMilli()
	tick := func(bid, ask float64, at int64) queuedTick {
		ts := at
		return queuedTick{tick: providers.Tick{Symbol: asset.ProviderSymbol, Price: (bid + ask) / 2, Bid: &bid, Ask: &ask, ProviderTimestamp: &ts}, receivedAt: at}
	}
	got := ingestTicks(ctx, []queuedTick{
		tick(99.99, 100.01, base),      // accepted
		tick(100.02, 100.00, base+100), // crossed book
		tick(102.99, 103.01, base+200), // +3%: held outlier
		tick(99.99, 100.01, base-50),   // out of order
		tick(100.0, 100.02, base+1300), // accepted, an EWMA observation 1.3 s later
		tick(99.0, 101.0, base+1400),   // 200 bps spread
	})
	if len(got) != 2 || got[1].Price != 100.01 || got[1].SpreadBps == nil || *got[1].Bid != 100.0 {
		t.Fatalf("accepted %d: %+v", len(got), got)
	}
	if price := redisx.R().HGet(ctx, config.RedisKeys.LastTick(id), "price").Val(); price != "100.01" {
		t.Fatalf("last tick %q", price)
	}
	if n := redisx.R().ZCard(ctx, config.RedisKeys.TickHistory(id)).Val(); n != 2 {
		t.Fatalf("history %d, want 2 (rejected quotes are not stored)", n)
	}
	stats, _ := QuoteRejectionStats(ctx, id)
	if stats[RejectInvalidBook] != "1" || stats[RejectOutlierUnconfirmed] != "1" || stats[RejectOutOfOrder] != "1" ||
		stats[RejectSpread] != "1" || stats["total"] != "4" {
		t.Fatalf("rejection counters %v", stats)
	}
	v, ok, err := GetVolatility(ctx, id)
	if err != nil || !ok || v.N != 1 || !(v.Var > 0) || v.ObsMid != 100.01 {
		t.Fatalf("vol state %+v %v %v", v, ok, err)
	}

	// A restart (or a new feed leader): the state is restored from Redis -
	// an old quote is still out of order and the EWMA continues.
	ResetNormalizationState()
	got = ingestTicks(ctx, []queuedTick{tick(99.99, 100.01, base+1200), tick(100.01, 100.03, base+2400)})
	if len(got) != 1 || got[0].Price != 100.02 {
		t.Fatalf("after restart %+v", got)
	}
	v2, _, _ := GetVolatility(ctx, id)
	if v2.N != 2 {
		t.Fatalf("EWMA did not resume: %+v", v2)
	}
}
