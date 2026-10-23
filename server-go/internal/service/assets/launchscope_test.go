package assets

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/testsupport"
)

// The launch-scope migration switches EUR/USD, GBP/USD and XAU/USD off,
// once, with one audit row per asset it changes; BTC/USD and ETH/USD and
// open trades are untouched; a second run changes nothing.
func TestApplyLaunchScopeIsIdempotentAndAudited(t *testing.T) {
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	t.Cleanup(InvalidateAssetCache)

	ids := map[string]bson.ObjectID{}
	for sym, active := range map[string]bool{"BTC/USD": true, "ETH/USD": true, "EUR/USD": true, "GBP/USD": false, "XAU/USD": true} {
		id := bson.NewObjectID()
		ids[sym] = id
		if _, err := mongox.C(models.CollAssets).InsertOne(ctx, models.Asset{ID: id, Symbol: sym, Name: sym,
			Category: models.AssetCategoryCrypto, Active: active, ProviderSymbol: "p" + id.Hex(), Provider: "binance",
			SupportedExpiries: []int64{60}, TradingWindows: []models.TradingWindow{}, CreatedAt: mongox.Now(), UpdatedAt: mongox.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	open := bson.NewObjectID()
	if _, err := mongox.C(models.CollBinaryTrades).InsertOne(ctx, bson.M{"_id": open, "assetId": ids["XAU/USD"],
		"status": string(models.TradeStatusOpen), "reference": "T-" + open.Hex()}); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyLaunchScope(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Disabled) != 2 || len(res.Unchanged) != 1 || res.Unchanged[0] != "GBP/USD" {
		t.Fatalf("result %+v", res)
	}
	for _, d := range res.Disabled {
		if d.Symbol == "XAU/USD" && d.OpenTrades != 1 {
			t.Fatalf("open trades %+v", d)
		}
	}
	active := func(sym string) bool {
		var a models.Asset
		if err := mongox.C(models.CollAssets).FindOne(ctx, bson.M{"_id": ids[sym]}).Decode(&a); err != nil {
			t.Fatal(err)
		}
		return a.Active
	}
	if !active("BTC/USD") || !active("ETH/USD") || active("EUR/USD") || active("GBP/USD") || active("XAU/USD") {
		t.Fatal("wrong assets switched")
	}
	audits := func() int64 {
		n, err := mongox.C(models.CollAuditLogs).CountDocuments(ctx, bson.M{"action": AuditLaunchScopeDisabled})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if audits() != 2 {
		t.Fatalf("%d audit rows", audits())
	}
	var tr bson.M
	if err := mongox.C(models.CollBinaryTrades).FindOne(ctx, bson.M{"_id": open}).Decode(&tr); err != nil || tr["status"] != string(models.TradeStatusOpen) {
		t.Fatalf("open trade touched: %v %v", tr, err)
	}

	again, err := ApplyLaunchScope(ctx)
	if err != nil || len(again.Disabled) != 0 || len(again.Unchanged) != 3 || audits() != 2 {
		t.Fatalf("second run %+v %v (audits %d)", again, err, audits())
	}
	if err := ApplyLaunchScopeAtBoot(ctx, false); err != nil {
		t.Fatal(err)
	}
}
