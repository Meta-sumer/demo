package marketdata

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/testsupport"
)

// A batch is written in one pipeline: every tick lands in the history, the
// last tick is the batch's last, a symbol the platform does not trade is
// skipped without failing the rest, and the broadcasts go out in order.
func TestIngestTicksWritesABatchInOrder(t *testing.T) {
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx := context.Background()

	asset := models.Asset{ID: bson.NewObjectID(), Symbol: "TEST/BATCH", Name: "Test", Category: models.AssetCategoryCrypto,
		Active: true, ProviderSymbol: "batch" + bson.NewObjectID().Hex(), Provider: "simulated", PricePrecision: 2}
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, asset); err != nil {
		t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	t.Cleanup(assets.InvalidateAssetCache)
	id := asset.ID.Hex()
	// Redis is shared with whatever else runs on it: only this asset's keys.
	t.Cleanup(func() {
		redisx.R().Del(context.Background(), config.RedisKeys.LastTick(id), config.RedisKeys.TickHistory(id))
	})

	sub := redisx.R().Subscribe(ctx, config.PubSubBroadcast)
	defer sub.Close()
	if _, err := sub.Receive(ctx); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UnixMilli()
	batch := []queuedTick{}
	for i := 0; i < 5; i++ {
		at := base + int64(i)
		batch = append(batch, queuedTick{tick: providers.Tick{Symbol: asset.ProviderSymbol, Price: 100 + float64(i), ProviderTimestamp: &at}, receivedAt: at})
		if i == 2 {
			batch = append(batch, queuedTick{tick: providers.Tick{Symbol: "not-traded-here", Price: 1}, receivedAt: at})
		}
	}

	got := ingestTicks(ctx, batch)
	if len(got) != 5 {
		t.Fatalf("ingested %d ticks, want 5", len(got))
	}
	if price := redisx.R().HGet(ctx, config.RedisKeys.LastTick(id), "price").Val(); price != "104" {
		t.Fatalf("last tick price %q, want 104", price)
	}
	if n := redisx.R().ZCard(ctx, config.RedisKeys.TickHistory(id)).Val(); n != 5 {
		t.Fatalf("history holds %d ticks, want 5", n)
	}

	// Other publishers may share the channel: keep this asset's ticks only.
	var prices []float64
	deadline := time.After(5 * time.Second)
	for len(prices) < 5 {
		select {
		case msg := <-sub.Channel():
			var env ws.Envelope
			if json.Unmarshal([]byte(msg.Payload), &env) != nil || !slices.Contains(env.Channels, config.ChMarket(id)) {
				continue
			}
			var tick struct {
				Price float64 `json:"price"`
			}
			if err := json.Unmarshal(env.Payload, &tick); err != nil {
				t.Fatal(err)
			}
			prices = append(prices, tick.Price)
		case <-deadline:
			t.Fatalf("broadcasts received: %v, want 5", prices)
		}
	}
	if !slices.Equal(prices, []float64{100, 101, 102, 103, 104}) {
		t.Fatalf("broadcast order %v", prices)
	}
}
