package reconciliation

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// cancelledCheckBatch is how many cancelled trades one ledger query covers.
const cancelledCheckBatch = 500

// checkCancelledTradesReleased: a CANCELLED trade whose stake was locked
// (ledger key trade:lock:<id>) must have had it released (trade:settle:<id>).
// A cancel interrupted between its claim and the wallet release leaves the
// customer's stake locked against a trade that will never settle; this check
// names every such trade until the cancel is repeated or
// binary.RepairCancelledTrades finishes it (POST
// /admin/binary/trades/cancellations/repair).
func checkCancelledTradesReleased(ctx context.Context) (models.ReconciliationCheck, error) {
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{"status": models.TradeStatusCancelled},
		options.Find().SetProjection(bson.M{"_id": 1, "userId": 1, "stake": 1, "accountType": 1, "cancellation": 1}))
	if err != nil {
		return models.ReconciliationCheck{}, err
	}
	var trades []struct {
		ID           bson.ObjectID             `bson:"_id"`
		UserID       bson.ObjectID             `bson:"userId"`
		Stake        int64                     `bson:"stake"`
		AccountType  string                    `bson:"accountType"`
		Cancellation *models.TradeCancellation `bson:"cancellation"`
	}
	if err := cur.All(ctx, &trades); err != nil {
		return models.ReconciliationCheck{}, err
	}

	var stillLocked []map[string]any
	var lockedMinor int64
	for start := 0; start < len(trades); start += cancelledCheckBatch {
		batch := trades[start:min(start+cancelledCheckBatch, len(trades))]
		keys := bson.A{}
		for _, t := range batch {
			keys = append(keys, "trade:lock:"+t.ID.Hex(), "trade:settle:"+t.ID.Hex())
		}
		lcur, err := mongox.C(models.CollWalletTransactions).Find(ctx, bson.M{"idempotencyKey": bson.M{"$in": keys}},
			options.Find().SetProjection(bson.M{"idempotencyKey": 1}))
		if err != nil {
			return models.ReconciliationCheck{}, err
		}
		var rows []struct {
			Key *string `bson:"idempotencyKey"`
		}
		if err := lcur.All(ctx, &rows); err != nil {
			return models.ReconciliationCheck{}, err
		}
		seen := map[string]bool{}
		for _, r := range rows {
			if r.Key != nil {
				seen[*r.Key] = true
			}
		}
		for _, t := range batch {
			id := t.ID.Hex()
			if !seen["trade:lock:"+id] || seen["trade:settle:"+id] {
				continue
			}
			lockedMinor += t.Stake
			d := map[string]any{"tradeId": id, "userId": t.UserID.Hex(), "accountType": models.NormAccountType(t.AccountType), "stake": t.Stake}
			if t.Cancellation != nil {
				d["attempts"], d["lastError"] = t.Cancellation.Attempts, t.Cancellation.LastError
			}
			stillLocked = append(stillLocked, d)
		}
	}
	return models.ReconciliationCheck{
		Name:       "cancelled_trades_released",
		Passed:     len(stillLocked) == 0,
		Expected:   0,
		Actual:     int64(len(stillLocked)),
		Difference: int64(len(stillLocked)),
		Details: map[string]any{
			"note":          "a cancelled trade's stake must be released (trade:settle:<id>); repair with POST /admin/binary/trades/cancellations/repair",
			"cancelled":     len(trades),
			"lockedMinor":   lockedMinor,
			"stillLocked":   capped(stillLocked),
			"stillLockedOf": len(stillLocked),
		},
	}, nil
}
