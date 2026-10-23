package settlement

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
)

// A void records why no price existed (feed data rules, audit F-17); the
// outcome itself is the policy's: deferred, then voided and refunded.
func TestVoidRecordsWhyNoPriceExisted(t *testing.T) {
	h := newHarness(t)
	h.setRiskConfig(bson.M{"settlementDeferWindowMs": int64(2000)})
	exp := now() - 10_000

	// History loss: Redis lost md:hist (or it was trimmed).
	lost := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(lost, tickOpts{price: 100, ts: exp - 500})
	if err := redisx.R().Del(h.ctx, config.RedisKeys.TickHistory(lost.AssetID.Hex())).Err(); err != nil {
		t.Fatal(err)
	}
	if out := settle(h, lost); out.Rule != models.SettlementRuleNoPriceVoidRefund {
		t.Fatalf("outcome %+v", out)
	}
	row := h.assertBooked(lost, models.TradeStatusDraw, 0)
	if row.Evidence.NoPriceReason != models.NoPriceReasonHistoryUnavailable {
		t.Fatalf("history loss evidence %+v", row.Evidence)
	}

	// The expiry fell inside a recorded market data gap.
	inGap := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(inGap, tickOpts{price: 100, ts: exp - 30_000})
	h.tick(inGap, tickOpts{price: 101, ts: exp + 5_000})
	end := exp + 5_000
	dur := end - (exp - 30_000)
	gap := models.MarketDataGap{ID: bson.NewObjectID(), AssetID: inGap.AssetID, Symbol: inGap.Symbol, Provider: "test",
		Reason: models.GapReasonDisconnected, StartedAt: exp - 30_000, EndedAt: &end, DurationMs: &dur, DetectedAt: exp - 27_000,
		ThresholdMs: 3000, CreatedAt: mongox.Now(), UpdatedAt: mongox.Now()}
	if _, err := mongox.C(models.CollMarketDataGaps).InsertOne(h.ctx, gap); err != nil {
		t.Fatal(err)
	}
	if out := settle(h, inGap); out.Rule != models.SettlementRuleNoPriceVoidRefund {
		t.Fatalf("outcome %+v", out)
	}
	row = h.row(inGap.ID)
	if row.Evidence.NoPriceReason != models.NoPriceReasonFeedGap || row.Evidence.FeedGapID != gap.ID.Hex() {
		t.Fatalf("gap evidence %+v", row.Evidence)
	}

	// History intact, no gap recorded, simply no tick in the window.
	quiet := h.openTrade(tradeOpts{entry: 100, expiry: exp})
	h.tick(quiet, tickOpts{price: 100, ts: exp - 30_000})
	h.tick(quiet, tickOpts{price: 101, ts: exp + 5_000})
	if out := settle(h, quiet); out.Rule != models.SettlementRuleNoPriceVoidRefund {
		t.Fatalf("outcome %+v", out)
	}
	if row = h.row(quiet.ID); row.Evidence.NoPriceReason != models.NoPriceReasonNoTick || row.Evidence.FeedGapID != "" {
		t.Fatalf("no-tick evidence %+v", row.Evidence)
	}
}
