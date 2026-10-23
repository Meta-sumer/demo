package settlement

import (
	"context"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/marketdata"
)

// noPriceReason explains a void on the record (feed data rules, audit
// F-17): the expiry fell inside a recorded market data gap; or the tick
// history no longer reaches back to the expiry window (trimmed by
// feedTickHistoryRetentionMs, or lost with Redis); or there simply was no
// qualifying tick. It never changes the outcome - the void is decided by the
// policy alone - and a lookup failure only makes the reason less specific.
func noPriceReason(ctx context.Context, assetID string, expiry int64, p models.SettlementPolicy) (string, string) {
	gap, err := marketdata.GapAt(ctx, assetID, expiry)
	if err != nil {
		logx.Warn("gap lookup for a void failed", "assetId", assetID, "err", err.Error())
	}
	if gap != nil {
		return models.NoPriceReasonFeedGap, gap.ID.Hex()
	}
	oldest, err := redisx.R().ZRangeWithScores(ctx, config.RedisKeys.TickHistory(assetID), 0, 0).Result()
	if err != nil {
		logx.Warn("history lookup for a void failed", "assetId", assetID, "err", err.Error())
		return models.NoPriceReasonNoTick, ""
	}
	if len(oldest) == 0 || int64(oldest[0].Score) > expiry-p.MaxTickAgeMs {
		return models.NoPriceReasonHistoryUnavailable, ""
	}
	return models.NoPriceReasonNoTick, ""
}
