package binary

import (
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
)

// settlementOf is client/src/features/trade/settlement.ts, per trade.
func settlementOf(t models.BinaryTrade) (int64, bool) {
	switch {
	case t.SettlementAmount != nil:
		return *t.SettlementAmount, true
	case t.Status == models.TradeStatusWon:
		return t.Profit, true
	case t.Status == models.TradeStatusLost:
		return -t.Stake, true
	case t.Status == models.TradeStatusDraw, t.Status == models.TradeStatusCancelled:
		return 0, true
	}
	return 0, false
}

// groupByStatus does what the $group stage does.
func groupByStatus(trades []models.BinaryTrade) []StatusTotals {
	byStatus := map[models.TradeStatus]*StatusTotals{}
	var order []models.TradeStatus
	for _, t := range trades {
		row := byStatus[t.Status]
		if row == nil {
			row = &StatusTotals{Status: t.Status}
			byStatus[t.Status] = row
			order = append(order, t.Status)
		}
		row.Count++
		row.Stake += t.Stake
		if t.SettlementAmount != nil {
			row.Settled += *t.SettlementAmount
		} else {
			row.UnsettledProfit += t.Profit
			row.UnsettledStake += t.Stake
		}
	}
	out := make([]StatusTotals, 0, len(order))
	for _, s := range order {
		out = append(out, *byStatus[s])
	}
	return out
}

func amount(v int64) *int64 { return &v }

func TestSummariseStatusTotals(t *testing.T) {
	if got := SummariseStatusTotals(nil); got != (TradeSummary{}) {
		t.Fatalf("no trades: %+v", got)
	}

	trade := func(status models.TradeStatus, stake, profit int64, settlement *int64) models.BinaryTrade {
		return models.BinaryTrade{Status: status, Stake: stake, Profit: profit, SettlementAmount: settlement}
	}
	trades := []models.BinaryTrade{
		trade(models.TradeStatusOpen, 100_00, 82_00, nil),
		trade(models.TradeStatusSettling, 50_00, 41_00, nil),
		trade(models.TradeStatusWon, 200_00, 168_00, amount(168_00)),
		trade(models.TradeStatusWon, 100_00, 84_00, nil), // no settlementAmount: counts its profit
		trade(models.TradeStatusLost, 300_00, 250_00, amount(-300_00)),
		trade(models.TradeStatusLost, 40_00, 33_00, nil), // no settlementAmount: counts minus its stake
		trade(models.TradeStatusDraw, 70_00, 58_00, amount(0)),
		trade(models.TradeStatusDraw, 10_00, 8_00, nil),
		trade(models.TradeStatusClosedEarly, 150_00, 120_00, amount(-37_45)),
		trade(models.TradeStatusClosedEarly, 20_00, 16_00, nil), // settlementOf is null: nothing
		trade(models.TradeStatusCancelled, 500_00, 400_00, amount(0)),
		trade(models.TradeStatusCancelled, 60_00, 50_00, nil),
	}

	var wantNet, wantInvested int64
	for _, tr := range trades {
		wantInvested += tr.Stake
		if tr.Status == models.TradeStatusOpen || tr.Status == models.TradeStatusSettling ||
			tr.Status == models.TradeStatusCancelled {
			continue
		}
		if v, ok := settlementOf(tr); ok {
			wantNet += v
		}
	}

	got := SummariseStatusTotals(groupByStatus(trades))
	want := TradeSummary{
		Count: 12, Open: 2, Won: 2, Lost: 2, Draw: 2, SoldEarly: 2, Cancelled: 2,
		Invested: wantInvested, NetResult: wantNet,
	}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	// 168 + 84 - 300 - 40 + 0 + 0 - 37.45 + 0, in paise.
	if got.NetResult != -125_45 || got.Invested != 1600_00 {
		t.Fatalf("netResult %d invested %d", got.NetResult, got.Invested)
	}
}

func TestTradeFilter(t *testing.T) {
	user, asset := bson.NewObjectID(), bson.NewObjectID()
	from := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)

	got, err := TradeFilter(TradeListQuery{
		UserID: user.Hex(), AssetID: asset.Hex(), Phase: PhaseSettled, Direction: "UP",
		AccountType: models.AccountLive, From: &from, To: &to, Page: 3, Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := bson.M{
		"userId":      user,
		"assetId":     asset,
		"status":      bson.M{"$in": FinishedStatuses},
		"direction":   "UP",
		"accountType": models.NotDemo(),
		"createdAt":   bson.M{"$gte": from, "$lte": to},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %v\nwant %v", got, want)
	}

	// An explicit status wins over the phase.
	got, _ = TradeFilter(TradeListQuery{Status: "WON", Phase: PhaseOpen, AccountType: models.AccountDemo})
	if !reflect.DeepEqual(got, bson.M{"status": "WON", "accountType": models.AccountDemo}) {
		t.Fatalf("status over phase: %v", got)
	}
	got, _ = TradeFilter(TradeListQuery{Phase: PhaseOpen})
	if !reflect.DeepEqual(got, bson.M{"status": bson.M{"$in": LiveStatuses}}) {
		t.Fatalf("open phase: %v", got)
	}
	if got, _ = TradeFilter(TradeListQuery{}); len(got) != 0 {
		t.Fatalf("no filters: %v", got)
	}
	if _, err := TradeFilter(TradeListQuery{UserID: "nope"}); err == nil {
		t.Fatal("malformed user id must fail")
	}
}
