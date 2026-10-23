package binary

import (
	"encoding/json"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/settlement"
)

// The tie rule is part of the trade's terms: frozen at acceptance and used at
// settlement, whatever the configuration says by then.
func TestAcceptanceFreezesTheDrawPolicy(t *testing.T) {
	e := newPricingEnv(t)

	res, err := e.create(e.input(models.DirectionUp))
	if err != nil {
		t.Fatal(err)
	}
	refund := e.stored(res.Trade.ID)
	if refund.DrawPolicy == nil || *refund.DrawPolicy != models.DrawPolicyRefund ||
		res.Trade.DrawPolicy == nil || *res.Trade.DrawPolicy != models.DrawPolicyRefund {
		t.Fatalf("stored %v view %v", refund.DrawPolicy, res.Trade.DrawPolicy)
	}

	// The platform changes the rule: new trades take the new one, the open
	// trade keeps its own.
	e.setRiskConfig(bson.M{"drawPolicy": string(models.DrawPolicyLoss)})
	e.moveTo(100)
	res2, err := e.create(e.input(models.DirectionDown))
	if err != nil {
		t.Fatal(err)
	}
	if loss := e.stored(res2.Trade.ID); loss.DrawPolicy == nil || *loss.DrawPolicy != models.DrawPolicyLoss {
		t.Fatalf("new trade %v", loss.DrawPolicy)
	}

	// Settle the first trade on a tie, under the LOSS configuration.
	exp := util.NowMs() - 5000
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(e.ctx, bson.M{"_id": refund.ID},
		bson.M{"$set": bson.M{"expiryTimestamp": exp}}); err != nil {
		t.Fatal(err)
	}
	for _, tk := range []struct {
		price float64
		ts    int64
	}{{refund.EntryPrice, exp - 10}, {refund.EntryPrice + 1, exp + 10}} {
		pt := tk.ts
		member, _ := json.Marshal(models.NormalisedTick{
			MarketTick: models.MarketTick{Asset: refund.Symbol, Price: tk.price, Timestamp: tk.ts, ProviderTimestamp: &pt, ReceivedAt: tk.ts},
			AssetID:    refund.AssetID.Hex(), Symbol: refund.Symbol, ProcessedAt: tk.ts,
		})
		if err := redisx.R().ZAdd(e.ctx, config.RedisKeys.TickHistory(refund.AssetID.Hex()),
			redis.Z{Score: float64(tk.ts), Member: string(member)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	out, err := settlement.SettleTrade(e.ctx, refund.ID.Hex(), "test")
	if err != nil || out.Status != settlement.StatusSettled || out.Result == nil || *out.Result != models.TradeResultDraw {
		t.Fatalf("settled %+v %v (want a refunded tie under the frozen REFUND rule)", out, err)
	}
	got := e.stored(refund.ID.Hex())
	if got.Status != models.TradeStatusDraw || got.SettlementIntent == nil ||
		got.SettlementIntent.Evidence.DrawPolicy != models.DrawPolicyRefund {
		t.Fatalf("trade %s evidence %+v", got.Status, got.SettlementIntent)
	}
}

func TestDrawPolicyForFallsBackForOldTrades(t *testing.T) {
	cfg := models.DefaultRiskConfig()
	cfg.DrawPolicy = models.DrawPolicyWin
	old := &models.BinaryTrade{}
	if p := settlement.DrawPolicyFor(old, &cfg); p != models.DrawPolicyWin {
		t.Fatalf("old trade %s", p)
	}
	loss := models.DrawPolicyLoss
	if p := settlement.DrawPolicyFor(&models.BinaryTrade{DrawPolicy: &loss}, &cfg); p != models.DrawPolicyLoss {
		t.Fatalf("frozen %s", p)
	}
	if p := settlement.DrawPolicyFor(old, nil); p != models.DrawPolicyRefund {
		t.Fatalf("no config %s", p)
	}
}
