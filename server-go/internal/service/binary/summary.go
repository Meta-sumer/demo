package binary

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// TradeSummary totals every trade a list query matches, across all pages.
// Money is in minor units.
type TradeSummary struct {
	Count int64 `json:"count"`
	// OPEN and SETTLING.
	Open      int64 `json:"open"`
	Won       int64 `json:"won"`
	Lost      int64 `json:"lost"`
	Draw      int64 `json:"draw"`
	SoldEarly int64 `json:"soldEarly"`
	Cancelled int64 `json:"cancelled"`
	// Sum of stakes over every matched trade.
	Invested int64 `json:"invested"`
	// Signed result of the settled trades (WON, LOST, DRAW, CLOSED_EARLY).
	NetResult int64 `json:"netResult"`
}

// StatusTotals is one row of the summary pipeline: the matched trades in one
// status.
type StatusTotals struct {
	Status models.TradeStatus `bson:"_id"`
	Count  int64              `bson:"count"`
	Stake  int64              `bson:"stake"`
	// Sum of settlementAmount over the trades that have one.
	Settled int64 `bson:"settled"`
	// Profit and stake of the trades without a settlementAmount, for the
	// fallbacks in SummariseStatusTotals.
	UnsettledProfit int64 `bson:"unsettledProfit"`
	UnsettledStake  int64 `bson:"unsettledStake"`
}

// tradeSummaryPipeline groups the matched trades by status in one pass.
func tradeSummaryPipeline(filter bson.M) mongo.Pipeline {
	unsettled := bson.M{"$in": bson.A{bson.M{"$type": "$settlementAmount"}, bson.A{"missing", "null"}}}
	return mongo.Pipeline{
		{{Key: "$match", Value: filter}},
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$status"},
			{Key: "count", Value: bson.M{"$sum": 1}},
			{Key: "stake", Value: bson.M{"$sum": "$stake"}},
			// $sum skips null and missing values.
			{Key: "settled", Value: bson.M{"$sum": "$settlementAmount"}},
			{Key: "unsettledProfit", Value: bson.M{"$sum": bson.M{"$cond": bson.A{unsettled, "$profit", 0}}}},
			{Key: "unsettledStake", Value: bson.M{"$sum": bson.M{"$cond": bson.A{unsettled, "$stake", 0}}}},
		}}},
	}
}

// SummariseTrades totals the trades matching q (TradeFilter; paging ignored)
// with a single aggregation.
func SummariseTrades(ctx context.Context, q TradeListQuery) (TradeSummary, error) {
	filter, err := TradeFilter(q)
	if err != nil {
		return TradeSummary{}, err
	}
	cur, err := mongox.C(models.CollBinaryTrades).Aggregate(ctx, tradeSummaryPipeline(filter))
	if err != nil {
		return TradeSummary{}, err
	}
	var rows []StatusTotals
	if err := cur.All(ctx, &rows); err != nil {
		return TradeSummary{}, err
	}
	return SummariseStatusTotals(rows), nil
}

// SummariseStatusTotals folds per-status totals into a TradeSummary.
//
// netResult follows the client's settlementOf (features/trade/settlement.ts):
// a stored settlementAmount is already net and signed and is taken as is; a
// trade that reached a terminal status without one counts as its profit when
// WON, minus its stake when LOST, and zero otherwise. CANCELLED trades are not
// settled trades and never count (their stake was returned).
func SummariseStatusTotals(rows []StatusTotals) TradeSummary {
	var s TradeSummary
	for _, r := range rows {
		s.Count += r.Count
		s.Invested += r.Stake
		switch r.Status {
		case models.TradeStatusOpen, models.TradeStatusSettling:
			s.Open += r.Count
		case models.TradeStatusWon:
			s.Won += r.Count
			s.NetResult += r.Settled + r.UnsettledProfit
		case models.TradeStatusLost:
			s.Lost += r.Count
			s.NetResult += r.Settled - r.UnsettledStake
		case models.TradeStatusDraw:
			s.Draw += r.Count
			s.NetResult += r.Settled
		case models.TradeStatusClosedEarly:
			s.SoldEarly += r.Count
			s.NetResult += r.Settled
		case models.TradeStatusCancelled:
			s.Cancelled += r.Count
		}
	}
	return s
}
