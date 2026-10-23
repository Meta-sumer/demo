package reconciliation

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// Stuck settlements (audit: "a settlement that fails every time is retried
// forever with no alert"). Settlement retries a failing trade every 5 s and
// the sweep re-queues it every ~30 s, but nothing reported a trade that never
// finishes. A trade is stuck when it has been SETTLING for longer than the
// threshold, or is still OPEN that long after its expiry (the price defer
// window is 60 s, after which it is settled or voided). Both mean a customer
// is waiting for an outcome and, for LIVE, its stake is still locked.

// StuckTrade is one trade that should have finished settling by now.
type StuckTrade struct {
	TradeID         string `json:"tradeId"`
	Reference       string `json:"reference"`
	Symbol          string `json:"symbol"`
	Status          string `json:"status"`
	AccountType     string `json:"accountType"`
	ExpiryTimestamp int64  `json:"expiryTimestamp"`
	UpdatedAt       string `json:"updatedAt"`
}

// StuckSettlementAfter is the threshold (ALERT_SETTLEMENT_STUCK_MINUTES).
func StuckSettlementAfter() time.Duration {
	return time.Duration(config.Alerts().SettlementStuckMinutes) * time.Minute
}

// FindStuckSettlements counts the stuck trades at now and returns up to
// limit of them, oldest expiry first.
func FindStuckSettlements(ctx context.Context, now time.Time, olderThan time.Duration, limit int64) (int64, []StuckTrade, error) {
	cutoff := now.Add(-olderThan)
	filter := bson.M{"$or": bson.A{
		bson.M{"status": models.TradeStatusSettling, "updatedAt": bson.M{"$lt": cutoff}},
		bson.M{"status": models.TradeStatusOpen, "expiryTimestamp": bson.M{"$lt": cutoff.UnixMilli()}},
	}}
	coll := mongox.C(models.CollBinaryTrades)
	n, err := coll.CountDocuments(ctx, filter)
	if err != nil || n == 0 {
		return n, []StuckTrade{}, err
	}
	cur, err := coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "expiryTimestamp", Value: 1}}).SetLimit(max(limit, 1)).
		SetProjection(bson.M{"reference": 1, "symbol": 1, "status": 1, "accountType": 1, "expiryTimestamp": 1, "updatedAt": 1}))
	if err != nil {
		return n, nil, err
	}
	var rows []models.BinaryTrade
	if err := cur.All(ctx, &rows); err != nil {
		return n, nil, err
	}
	out := make([]StuckTrade, 0, len(rows))
	for _, t := range rows {
		out = append(out, StuckTrade{
			TradeID: t.ID.Hex(), Reference: t.Reference, Symbol: t.Symbol, Status: string(t.Status),
			AccountType: models.NormAccountType(t.AccountType), ExpiryTimestamp: t.ExpiryTimestamp,
			UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}
	return n, out, nil
}

func checkStuckSettlements(ctx context.Context) (models.ReconciliationCheck, error) {
	return stuckSettlements(ctx, time.Now(), StuckSettlementAfter())
}

func stuckSettlements(ctx context.Context, now time.Time, after time.Duration) (models.ReconciliationCheck, error) {
	n, sample, err := FindStuckSettlements(ctx, now, after, 20)
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	items := make([]map[string]any, 0, len(sample))
	for _, t := range sample {
		items = append(items, map[string]any{
			"tradeId": t.TradeID, "reference": t.Reference, "symbol": t.Symbol, "status": t.Status,
			"accountType": t.AccountType, "expiryTimestamp": t.ExpiryTimestamp, "updatedAt": t.UpdatedAt,
		})
	}
	return models.ReconciliationCheck{
		Name:       "trades_stuck_settling",
		Passed:     n == 0,
		Expected:   0,
		Actual:     n,
		Difference: n,
		Details: map[string]any{
			"olderThanMinutes": int64(after / time.Minute),
			"trades":           items,
			"count":            n,
		},
	}, nil
}
