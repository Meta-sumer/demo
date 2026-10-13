// Package admin is the admin analytics (spec sections 20, 41).
//
// Platform P&L is never computed as "wins minus losses". The components are
// tracked separately - what customers were paid, what the platform grossed on
// the book, what the hedge legs made or lost - so the figures reconcile against
// the ledgers rather than merely looking plausible.
package admin

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"golang.org/x/sync/errgroup"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/hedging"
	"binarytrading/internal/service/metrics"
)

const dayMs = 86_400_000

type DashboardSummary struct {
	TotalUsers           int64   `json:"totalUsers"`
	ActiveUsers          int64   `json:"activeUsers"`
	OpenTrades           int64   `json:"openTrades"`
	TodayTrades          float64 `json:"todayTrades"`
	TodayVolume          float64 `json:"todayVolume"`
	TodayPlatformResult  float64 `json:"todayPlatformResult"`
	CurrentExposure      int64   `json:"currentExposure"`
	CurrentNetExposure   int64   `json:"currentNetExposure"`
	PotentialLiability   int64   `json:"potentialLiability"`
	HedgedExposure       int64   `json:"hedgedExposure"`
	UnhedgedExposure     int64   `json:"unhedgedExposure"`
	ActiveAssets         int64   `json:"activeAssets"`
	FeedLatencyP50       float64 `json:"feedLatencyP50"`
	FeedLatencyP95       float64 `json:"feedLatencyP95"`
	FeedLatencyP99       float64 `json:"feedLatencyP99"`
	TradeAcceptanceP95   float64 `json:"tradeAcceptanceP95"`
	TotalCustomerBalance float64 `json:"totalCustomerBalance"`
	TotalLockedBalance   float64 `json:"totalLockedBalance"`
	PendingDeposits      int64   `json:"pendingDeposits"`
	PendingWithdrawals   int64   `json:"pendingWithdrawals"`
}

// aggregate runs a pipeline and decodes every row into out (a slice pointer).
func aggregate(ctx context.Context, coll string, pipeline bson.A, out any) error {
	cur, err := mongox.C(coll).Aggregate(ctx, pipeline)
	if err != nil {
		return err
	}
	return cur.All(ctx, out)
}

func sumIfNull(field string) bson.M {
	return bson.M{"$sum": bson.M{"$ifNull": bson.A{field, 0}}}
}

func GetDashboardSummary(ctx context.Context) (DashboardSummary, error) {
	dayStart := time.UnixMilli(util.StartOfUTCDay(util.NowMs())).UTC()
	var s DashboardSummary

	var (
		today []struct {
			Trades         float64 `bson:"trades"`
			Volume         float64 `bson:"volume"`
			PlatformResult float64 `bson:"platformResult"`
		}
		activeUsers []struct {
			N int64 `bson:"n"`
		}
		walletTotals []struct {
			Balance float64 `bson:"balance"`
			Locked  float64 `bson:"locked"`
		}
		portfolio     exposure.PortfolioExposure
		hedgedByAsset map[string]int64
		activeAssets  []*models.Asset
	)

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) {
		s.TotalUsers, err = mongox.C(models.CollUsers).CountDocuments(gctx, bson.M{"role": config.RoleUser})
		return
	})
	g.Go(func() error {
		// distinct('userId', ...).length
		return aggregate(gctx, models.CollBinaryTrades, bson.A{
			bson.M{"$match": bson.M{"createdAt": bson.M{"$gte": dayStart}, "accountType": models.NotDemo()}},
			bson.M{"$group": bson.M{"_id": "$userId"}},
			bson.M{"$count": "n"},
		}, &activeUsers)
	})
	g.Go(func() (err error) {
		s.OpenTrades, err = mongox.C(models.CollBinaryTrades).CountDocuments(gctx,
			bson.M{"status": bson.M{"$in": bson.A{"OPEN", "SETTLING"}}, "accountType": models.NotDemo()})
		return
	})
	g.Go(func() error {
		return aggregate(gctx, models.CollBinaryTrades, bson.A{
			bson.M{"$match": bson.M{"createdAt": bson.M{"$gte": dayStart}, "accountType": models.NotDemo()}},
			bson.M{"$group": bson.M{
				"_id":            nil,
				"trades":         bson.M{"$sum": 1},
				"volume":         bson.M{"$sum": "$stake"},
				"platformResult": sumIfNull("$platformResult"),
			}},
		}, &today)
	})
	g.Go(func() (err error) {
		portfolio, err = exposure.GetPortfolioExposure(gctx)
		return
	})
	g.Go(func() (err error) {
		hedgedByAsset, err = hedging.GetHedgedNotionalByAsset(gctx)
		return
	})
	g.Go(func() (err error) {
		activeAssets, err = assets.ListAssetDocs(gctx, true)
		return
	})
	g.Go(func() error {
		// Customer money only: DEMO balances are virtual, and the USDT wallets
		// (another currency, not trading money) are not part of this total.
		return aggregate(gctx, models.CollWallets, bson.A{
			bson.M{"$match": bson.M{"accountType": models.NotDemo(), "currency": models.INRCurrency()}},
			bson.M{"$group": bson.M{"_id": nil, "balance": bson.M{"$sum": "$balance"}, "locked": bson.M{"$sum": "$lockedBalance"}}},
		}, &walletTotals)
	})
	g.Go(func() (err error) {
		s.PendingDeposits, err = mongox.C(models.CollTransfers).CountDocuments(gctx, bson.M{"kind": "DEPOSIT", "status": "PENDING"})
		return
	})
	g.Go(func() (err error) {
		s.PendingWithdrawals, err = mongox.C(models.CollTransfers).CountDocuments(gctx, bson.M{"kind": "WITHDRAWAL", "status": "PENDING"})
		return
	})
	if err := g.Wait(); err != nil {
		return DashboardSummary{}, err
	}
	feed := metrics.GetLatencySummary(ctx, config.LatFeedTotal)
	acceptance := metrics.GetLatencySummary(ctx, config.LatTradeAcceptance)

	var hedged int64
	for _, v := range hedgedByAsset {
		hedged += v
	}
	if len(activeUsers) > 0 {
		s.ActiveUsers = activeUsers[0].N
	}
	if len(today) > 0 {
		s.TodayTrades, s.TodayVolume, s.TodayPlatformResult = today[0].Trades, today[0].Volume, today[0].PlatformResult
	}
	s.CurrentExposure = portfolio.TotalGrossExposure
	s.CurrentNetExposure = portfolio.TotalNetExposure
	s.PotentialLiability = portfolio.TotalPotentialLiability
	s.HedgedExposure = hedged
	s.UnhedgedExposure = max(0, absInt(portfolio.TotalNetExposure)-hedged)
	s.ActiveAssets = int64(len(activeAssets))
	s.FeedLatencyP50, s.FeedLatencyP95, s.FeedLatencyP99 = feed.P50, feed.P95, feed.P99
	s.TradeAcceptanceP95 = acceptance.P95
	if len(walletTotals) > 0 {
		s.TotalCustomerBalance, s.TotalLockedBalance = walletTotals[0].Balance, walletTotals[0].Locked
	}
	return s, nil
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

type DailySeriesPoint struct {
	Date           string  `json:"date"`
	Trades         float64 `json:"trades"`
	Volume         float64 `json:"volume"`
	PlatformResult float64 `json:"platformResult"`
	CustomerPayout float64 `json:"customerPayout"`
	Wins           float64 `json:"wins"`
	Losses         float64 `json:"losses"`
	Draws          float64 `json:"draws"`
}

func countStatus(status string) bson.M {
	return bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$status", status}}, 1, 0}}}
}

var byDay = bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$createdAt"}}

func GetDailySeries(ctx context.Context, days int64) ([]DailySeriesPoint, error) {
	if days <= 0 {
		days = 14
	}
	startOfDay := util.StartOfUTCDay(util.NowMs())
	from := time.UnixMilli(startOfDay - (days-1)*dayMs).UTC()

	var rows []struct {
		ID             string  `bson:"_id"`
		Trades         float64 `bson:"trades"`
		Volume         float64 `bson:"volume"`
		PlatformResult float64 `bson:"platformResult"`
		Wins           float64 `bson:"wins"`
		Losses         float64 `bson:"losses"`
		Draws          float64 `bson:"draws"`
	}
	if err := aggregate(ctx, models.CollBinaryTrades, bson.A{
		bson.M{"$match": bson.M{"createdAt": bson.M{"$gte": from}, "accountType": models.NotDemo()}},
		bson.M{"$group": bson.M{
			"_id":            byDay,
			"trades":         bson.M{"$sum": 1},
			"volume":         bson.M{"$sum": "$stake"},
			"platformResult": sumIfNull("$platformResult"),
			"wins":           countStatus("WON"),
			"losses":         countStatus("LOST"),
			"draws":          countStatus("DRAW"),
		}},
	}, &rows); err != nil {
		return nil, err
	}

	var payouts []struct {
		ID             string  `bson:"_id"`
		CustomerPayout float64 `bson:"customerPayout"`
	}
	if err := aggregate(ctx, models.CollSettlements, bson.A{
		bson.M{"$match": bson.M{"createdAt": bson.M{"$gte": from}, "accountType": models.NotDemo()}},
		bson.M{"$group": bson.M{"_id": byDay, "customerPayout": bson.M{"$sum": "$payoutAmount"}}},
	}, &payouts); err != nil {
		return nil, err
	}

	payoutByDate := map[string]float64{}
	for _, p := range payouts {
		payoutByDate[p.ID] = p.CustomerPayout
	}
	type row = DailySeriesPoint
	byDate := map[string]row{}
	for _, r := range rows {
		byDate[r.ID] = row{Trades: r.Trades, Volume: r.Volume, PlatformResult: r.PlatformResult, Wins: r.Wins, Losses: r.Losses, Draws: r.Draws}
	}

	// Emit a point per day, including days with no activity, so charts do not
	// lie by omission.
	series := make([]DailySeriesPoint, 0, days)
	for i := int64(0); i < days; i++ {
		date := time.UnixMilli(startOfDay - (days-1-i)*dayMs).UTC().Format("2006-01-02")
		p := byDate[date]
		p.Date = date
		p.CustomerPayout = payoutByDate[date]
		series = append(series, p)
	}
	return series, nil
}

type AssetVolumeRow struct {
	AssetID        string  `json:"assetId"`
	Symbol         string  `json:"symbol"`
	Trades         float64 `json:"trades"`
	Volume         float64 `json:"volume"`
	PlatformResult float64 `json:"platformResult"`
}

func GetAssetVolume(ctx context.Context, days int64) ([]AssetVolumeRow, error) {
	if days <= 0 {
		days = 7
	}
	from := time.UnixMilli(util.StartOfUTCDay(util.NowMs()) - (days-1)*dayMs).UTC()
	var rows []struct {
		ID             bson.ObjectID `bson:"_id"`
		Symbol         string        `bson:"symbol"`
		Trades         float64       `bson:"trades"`
		Volume         float64       `bson:"volume"`
		PlatformResult float64       `bson:"platformResult"`
	}
	if err := aggregate(ctx, models.CollBinaryTrades, bson.A{
		bson.M{"$match": bson.M{"createdAt": bson.M{"$gte": from}, "accountType": models.NotDemo()}},
		bson.M{"$group": bson.M{
			"_id":            "$assetId",
			"symbol":         bson.M{"$first": "$symbol"},
			"trades":         bson.M{"$sum": 1},
			"volume":         bson.M{"$sum": "$stake"},
			"platformResult": sumIfNull("$platformResult"),
		}},
		bson.M{"$sort": bson.M{"volume": -1}},
	}, &rows); err != nil {
		return nil, err
	}
	out := make([]AssetVolumeRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, AssetVolumeRow{
			AssetID: r.ID.Hex(), Symbol: r.Symbol, Trades: r.Trades, Volume: r.Volume, PlatformResult: r.PlatformResult,
		})
	}
	return out, nil
}

// PlatformPnl is the full P&L decomposition (spec section 41). Each component
// comes from a different ledger so they can be cross-checked.
type PlatformPnl struct {
	From                    iso.Time `json:"from"`
	To                      iso.Time `json:"to"`
	CustomerLiability       float64  `json:"customerLiability"`
	CustomerPayout          float64  `json:"customerPayout"`
	CustomerStakesCollected float64  `json:"customerStakesCollected"`
	PlatformGrossResult     float64  `json:"platformGrossResult"`
	HedgeRealisedPnl        float64  `json:"hedgeRealisedPnl"`
	HedgeUnrealisedPnl      float64  `json:"hedgeUnrealisedPnl"`
	Fees                    float64  `json:"fees"`
	Adjustments             float64  `json:"adjustments"`
	NetResult               float64  `json:"netResult"`
}

func GetPlatformPnl(ctx context.Context, from, to time.Time) (PlatformPnl, error) {
	window := bson.M{"$gte": from, "$lte": to}

	var settlementSide []struct {
		Payout         float64 `bson:"payout"`
		PlatformResult float64 `bson:"platformResult"`
		Stakes         float64 `bson:"stakes"`
	}
	var openLiability []struct {
		Liability float64 `bson:"liability"`
	}
	var feeRows []struct {
		Total float64 `bson:"total"`
	}
	var adjustmentRows []struct {
		ID    string  `bson:"_id"`
		Total float64 `bson:"total"`
	}
	var hedge hedging.HedgePnlSummary

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return aggregate(gctx, models.CollSettlements, bson.A{
			bson.M{"$match": bson.M{"createdAt": window, "accountType": models.NotDemo()}},
			bson.M{"$group": bson.M{
				"_id":            nil,
				"payout":         bson.M{"$sum": "$payoutAmount"},
				"platformResult": bson.M{"$sum": "$platformResult"},
				"stakes":         bson.M{"$sum": "$stake"},
			}},
		}, &settlementSide)
	})
	g.Go(func() error {
		return aggregate(gctx, models.CollBinaryTrades, bson.A{
			bson.M{"$match": bson.M{"status": bson.M{"$in": bson.A{"OPEN", "SETTLING"}}, "accountType": models.NotDemo()}},
			bson.M{"$group": bson.M{"_id": nil, "liability": bson.M{"$sum": bson.M{"$add": bson.A{"$stake", "$profit"}}}}},
		}, &openLiability)
	})
	g.Go(func() (err error) {
		hedge, err = hedging.GetHedgePnlSummary(gctx, &from)
		return
	})
	g.Go(func() error {
		return aggregate(gctx, models.CollWalletTransactions, bson.A{
			bson.M{"$match": bson.M{"type": "FEE", "createdAt": window, "accountType": models.NotDemo(), "currency": models.INRCurrency()}},
			bson.M{"$group": bson.M{"_id": nil, "total": bson.M{"$sum": "$amount"}}},
		}, &feeRows)
	})
	g.Go(func() error {
		return aggregate(gctx, models.CollWalletTransactions, bson.A{
			bson.M{"$match": bson.M{
				"type":      bson.M{"$in": bson.A{"ADJUSTMENT_CREDIT", "ADJUSTMENT_DEBIT"}},
				"createdAt": window,
				// DEMO opening balances and resets are not platform money.
				"accountType": models.NotDemo(),
				"currency":    models.INRCurrency(),
			}},
			bson.M{"$group": bson.M{"_id": "$type", "total": bson.M{"$sum": "$amount"}}},
		}, &adjustmentRows)
	})
	if err := g.Wait(); err != nil {
		return PlatformPnl{}, err
	}

	var credits, debits float64
	for _, r := range adjustmentRows {
		switch r.ID {
		case "ADJUSTMENT_CREDIT":
			credits = r.Total
		case "ADJUSTMENT_DEBIT":
			debits = r.Total
		}
	}
	p := PlatformPnl{
		From:               iso.From(from),
		To:                 iso.From(to),
		HedgeRealisedPnl:   float64(hedge.Realised),
		HedgeUnrealisedPnl: float64(hedge.Unrealised),
		Adjustments:        debits - credits,
	}
	if len(settlementSide) > 0 {
		p.CustomerPayout = settlementSide[0].Payout
		p.CustomerStakesCollected = settlementSide[0].Stakes
		p.PlatformGrossResult = settlementSide[0].PlatformResult
	}
	if len(openLiability) > 0 {
		p.CustomerLiability = openLiability[0].Liability
	}
	if len(feeRows) > 0 {
		p.Fees = feeRows[0].Total
	}
	p.NetResult = p.PlatformGrossResult + p.HedgeRealisedPnl + p.Fees + p.Adjustments
	return p, nil
}
