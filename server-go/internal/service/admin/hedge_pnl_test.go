package admin

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/testsupport"
)

// The platform P&L takes the hedge's realised result from the execution
// ledger (fills, funding, corrections), so it is no longer always 0, and
// unrealised stays out of the net result.
func TestPlatformPnlUsesHedgeLedger(t *testing.T) {
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)

	asset := bson.NewObjectID()
	now := time.Now()
	from, to := now.Add(-time.Hour), now.Add(time.Hour)
	fills := []any{
		models.HedgeFill{Venue: "binance-testnet", VenueSymbol: "BTCUSDT", TradeID: 1, AssetID: asset, Side: "BUY",
			QtyE8: 5_000_000, Price: 60_000, TradeTime: now.UnixMilli(), InrPerUsdt: 88, FeeInr: 13_200, CreatedAt: now},
		models.HedgeFill{Venue: "binance-testnet", VenueSymbol: "BTCUSDT", TradeID: 2, AssetID: asset, Side: "SELL",
			QtyE8: 5_000_000, Price: 60_300, TradeTime: now.UnixMilli(), InrPerUsdt: 88, RealisedE8: 1_500_000_000,
			RealisedInr: 132_000, FeeInr: 13_266, CreatedAt: now},
		// Outside the window: not counted.
		models.HedgeFill{Venue: "binance-testnet", VenueSymbol: "BTCUSDT", TradeID: 3, AssetID: asset, Side: "SELL",
			QtyE8: 100_000, Price: 60_000, TradeTime: now.Add(-48 * time.Hour).UnixMilli(), RealisedInr: 999_999, CreatedAt: now},
	}
	if _, err := mongox.C(models.CollHedgeFills).InsertMany(ctx, fills); err != nil {
		t.Fatal(err)
	}
	if _, err := mongox.C(models.CollHedgeFunding).InsertOne(ctx, models.HedgeFunding{Venue: "binance-testnet",
		VenueSymbol: "BTCUSDT", TranID: 7, AssetID: asset, AmountE8: -75_000_000, Time: now.UnixMilli(), AmountInr: -6_600, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := mongox.C(models.CollHedgePositions).InsertOne(ctx, models.HedgePosition{AssetID: asset, VenueSymbol: "BTCUSDT",
		UnrealisedInr: 5_000, ReconcileStatus: models.HedgeReconOK, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	p, err := GetPlatformPnl(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// realised = trading 132,000 - fees 26,466 + funding -6,600 = 98,934.
	if p.HedgeTradingPnl != 132_000 || p.HedgeFees != 26_466 || p.HedgeFunding != -6_600 || p.HedgeRealisedPnl != 98_934 {
		t.Fatalf("hedge pnl %+v", p)
	}
	if p.HedgeUnrealisedPnl != 5_000 || p.NetResult != p.PlatformGrossResult+p.HedgeRealisedPnl+p.Fees+p.Adjustments {
		t.Fatalf("net %+v", p)
	}
	if p.HedgeReconcileStatus != models.HedgeReconOK || p.HedgeFills != 2 {
		t.Fatalf("reconciliation %+v", p)
	}
}
