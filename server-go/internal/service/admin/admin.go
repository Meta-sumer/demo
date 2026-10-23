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

	// Hedge book (metric dictionary, docs/audit/operations): the policy
	// target, the marks and reconciliation behind HedgedExposure.
	HedgeTargetNotional  int64      `json:"hedgeTargetNotional"`
	HedgeMarkedAt        *time.Time `json:"-"`
	HedgeTargetAsOf      *time.Time `json:"-"`
	HedgeReconcileStatus string     `json:"hedgeReconcileStatus"`
	HedgeReconciledAt    *time.Time `json:"-"`
	GeneratedAt          time.Time  `json:"-"`
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

// Visibility says which aggregate figures a caller may see (least privilege:
// a SUPPORT agent runs the desk without seeing the book).
type Visibility struct {
	// Exposure: gross/net exposure, potential liability, hedged/unhedged
	// (exposure:read - the same figures as GET /admin/exposure).
	Exposure bool
	// PnL: the platform's result, customer payouts and the customer balance
	// totals, i.e. what the platform owes (reports:read).
	PnL bool
	// Deposits / Withdrawals: the pending queues (deposits:read /
	// withdrawals:read).
	Deposits    bool
	Withdrawals bool
}

// FullVisibility is every figure.
var FullVisibility = Visibility{Exposure: true, PnL: true, Deposits: true, Withdrawals: true}

// VisibilityFor derives what a role with these permissions may see.
func VisibilityFor(role string, permissions []string) Visibility {
	has := func(p string) bool { return config.HasPermission(role, permissions, p) }
	return Visibility{
		Exposure:    has(config.PermExposureRead),
		PnL:         has(config.PermReportsRead),
		Deposits:    has(config.PermDepositsRead),
		Withdrawals: has(config.PermWithdrawalsRead),
	}
}

// DashboardView is GET /admin/dashboard for one caller: a figure the caller
// may not see is absent (not zero, which would be a lie).
type DashboardView struct {
	TotalUsers           int64    `json:"totalUsers"`
	ActiveUsers          int64    `json:"activeUsers"`
	OpenTrades           int64    `json:"openTrades"`
	TodayTrades          float64  `json:"todayTrades"`
	TodayVolume          float64  `json:"todayVolume"`
	TodayPlatformResult  *float64 `json:"todayPlatformResult,omitempty"`
	CurrentExposure      *int64   `json:"currentExposure,omitempty"`
	CurrentNetExposure   *int64   `json:"currentNetExposure,omitempty"`
	PotentialLiability   *int64   `json:"potentialLiability,omitempty"`
	HedgedExposure       *int64   `json:"hedgedExposure,omitempty"`
	UnhedgedExposure     *int64   `json:"unhedgedExposure,omitempty"`
	ActiveAssets         int64    `json:"activeAssets"`
	FeedLatencyP50       float64  `json:"feedLatencyP50"`
	FeedLatencyP95       float64  `json:"feedLatencyP95"`
	FeedLatencyP99       float64  `json:"feedLatencyP99"`
	TradeAcceptanceP95   float64  `json:"tradeAcceptanceP95"`
	TotalCustomerBalance *float64 `json:"totalCustomerBalance,omitempty"`
	TotalLockedBalance   *float64 `json:"totalLockedBalance,omitempty"`
	PendingDeposits      *int64   `json:"pendingDeposits,omitempty"`
	PendingWithdrawals   *int64   `json:"pendingWithdrawals,omitempty"`
	// Hedge book (exposure:read): policy target, oldest mark of an open
	// hedge position, oldest target evaluation, reconciliation state.
	HedgeTargetNotional  *int64    `json:"hedgeTargetNotional,omitempty"`
	HedgeMarkedAt        *iso.Time `json:"hedgeMarkedAt,omitempty"`
	HedgeTargetAsOf      *iso.Time `json:"hedgeTargetAsOf,omitempty"`
	HedgeReconcileStatus *string   `json:"hedgeReconcileStatus,omitempty"`
	HedgeReconciledAt    *iso.Time `json:"hedgeReconciledAt,omitempty"`
	// When the server computed these figures.
	GeneratedAt iso.Time `json:"generatedAt"`
	// Redacted names the figure groups withheld from this caller.
	Redacted []string `json:"redacted"`
}

func ptr[T any](v T) *T { return &v }

// GetDashboardFor is the dashboard limited to what v allows; the withheld
// figures are not even computed.
func GetDashboardFor(ctx context.Context, v Visibility) (DashboardView, error) {
	s, err := getDashboard(ctx, v)
	if err != nil {
		return DashboardView{}, err
	}
	out := DashboardView{
		TotalUsers: s.TotalUsers, ActiveUsers: s.ActiveUsers, OpenTrades: s.OpenTrades,
		TodayTrades: s.TodayTrades, TodayVolume: s.TodayVolume, ActiveAssets: s.ActiveAssets,
		FeedLatencyP50: s.FeedLatencyP50, FeedLatencyP95: s.FeedLatencyP95, FeedLatencyP99: s.FeedLatencyP99,
		TradeAcceptanceP95: s.TradeAcceptanceP95, Redacted: []string{}, GeneratedAt: iso.From(s.GeneratedAt),
	}
	if v.Exposure {
		out.CurrentExposure, out.CurrentNetExposure = ptr(s.CurrentExposure), ptr(s.CurrentNetExposure)
		out.PotentialLiability = ptr(s.PotentialLiability)
		out.HedgedExposure, out.UnhedgedExposure = ptr(s.HedgedExposure), ptr(s.UnhedgedExposure)
		out.HedgeTargetNotional, out.HedgeReconcileStatus = ptr(s.HedgeTargetNotional), ptr(s.HedgeReconcileStatus)
		out.HedgeMarkedAt, out.HedgeTargetAsOf = iso.Ptr(s.HedgeMarkedAt), iso.Ptr(s.HedgeTargetAsOf)
		out.HedgeReconciledAt = iso.Ptr(s.HedgeReconciledAt)
	} else {
		out.Redacted = append(out.Redacted, "exposure")
	}
	if v.PnL {
		out.TodayPlatformResult = ptr(s.TodayPlatformResult)
		out.TotalCustomerBalance, out.TotalLockedBalance = ptr(s.TotalCustomerBalance), ptr(s.TotalLockedBalance)
	} else {
		out.Redacted = append(out.Redacted, "pnl")
	}
	if v.Deposits {
		out.PendingDeposits = ptr(s.PendingDeposits)
	} else {
		out.Redacted = append(out.Redacted, "deposits")
	}
	if v.Withdrawals {
		out.PendingWithdrawals = ptr(s.PendingWithdrawals)
	} else {
		out.Redacted = append(out.Redacted, "withdrawals")
	}
	return out, nil
}

func GetDashboardSummary(ctx context.Context) (DashboardSummary, error) {
	return getDashboard(ctx, FullVisibility)
}

func getDashboard(ctx context.Context, vis Visibility) (DashboardSummary, error) {
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
		portfolio    exposure.PortfolioExposure
		hedgeBook    hedging.HedgeBookSummary
		activeAssets []*models.Asset
	)
	s.GeneratedAt = mongox.Now()

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
	if vis.Exposure {
		g.Go(func() (err error) {
			portfolio, err = exposure.GetPortfolioExposure(gctx)
			return
		})
		g.Go(func() error {
			docs, err := assets.ListAssetDocs(gctx, false)
			if err != nil {
				return err
			}
			ids := make([]bson.ObjectID, 0, len(docs))
			for _, a := range docs {
				ids = append(ids, a.ID)
			}
			hedgeBook, err = hedging.GetHedgeBookSummary(gctx, ids)
			return err
		})
	}
	g.Go(func() (err error) {
		activeAssets, err = assets.ListAssetDocs(gctx, true)
		return
	})
	if vis.PnL {
		g.Go(func() error {
			// Customer money only: DEMO balances are virtual, and the USDT wallets
			// (another currency, not trading money) are not part of this total.
			return aggregate(gctx, models.CollWallets, bson.A{
				bson.M{"$match": bson.M{"accountType": models.NotDemo(), "currency": models.INRCurrency()}},
				bson.M{"$group": bson.M{"_id": nil, "balance": bson.M{"$sum": "$balance"}, "locked": bson.M{"$sum": "$lockedBalance"}}},
			}, &walletTotals)
		})
	}
	if vis.Deposits {
		g.Go(func() (err error) {
			s.PendingDeposits, err = mongox.C(models.CollTransfers).CountDocuments(gctx, bson.M{"kind": "DEPOSIT", "status": "PENDING"})
			return
		})
	}
	if vis.Withdrawals {
		g.Go(func() (err error) {
			s.PendingWithdrawals, err = mongox.C(models.CollTransfers).CountDocuments(gctx, bson.M{"kind": "WITHDRAWAL", "status": "PENDING"})
			return
		})
	}
	if err := g.Wait(); err != nil {
		return DashboardSummary{}, err
	}
	feed := metrics.GetLatencySummary(ctx, config.LatFeedTotal)
	acceptance := metrics.GetLatencySummary(ctx, config.LatTradeAcceptance)

	if len(activeUsers) > 0 {
		s.ActiveUsers = activeUsers[0].N
	}
	if len(today) > 0 {
		s.TodayTrades, s.TodayVolume, s.TodayPlatformResult = today[0].Trades, today[0].Volume, today[0].PlatformResult
	}
	s.CurrentExposure = portfolio.TotalGrossExposure
	s.CurrentNetExposure = portfolio.TotalNetExposure
	s.PotentialLiability = portfolio.TotalPotentialLiability
	// Hedge figures are delta-equivalent INR notionals: held = filled
	// positions at mark; unhedged = what the target still lacks (target
	// minus held, summed over assets). Working orders count in neither.
	s.HedgedExposure = hedgeBook.Held
	s.UnhedgedExposure = hedgeBook.Residual
	s.HedgeTargetNotional = hedgeBook.Target
	s.HedgeMarkedAt, s.HedgeTargetAsOf = hedgeBook.MarkedAt, hedgeBook.TargetAsOf
	s.HedgeReconcileStatus, s.HedgeReconciledAt = hedgeBook.ReconcileStatus, hedgeBook.ReconciledAt
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

// DailySeriesView is a series point for one caller: the platform result and
// the customer payouts are P&L figures (reports:read) and absent otherwise.
type DailySeriesView struct {
	Date           string   `json:"date"`
	Trades         float64  `json:"trades"`
	Volume         float64  `json:"volume"`
	PlatformResult *float64 `json:"platformResult,omitempty"`
	CustomerPayout *float64 `json:"customerPayout,omitempty"`
	Wins           float64  `json:"wins"`
	Losses         float64  `json:"losses"`
	Draws          float64  `json:"draws"`
}

func GetDailySeriesFor(ctx context.Context, days int64, v Visibility) ([]DailySeriesView, error) {
	points, err := GetDailySeries(ctx, days)
	if err != nil {
		return nil, err
	}
	out := make([]DailySeriesView, 0, len(points))
	for _, p := range points {
		row := DailySeriesView{Date: p.Date, Trades: p.Trades, Volume: p.Volume, Wins: p.Wins, Losses: p.Losses, Draws: p.Draws}
		if v.PnL {
			row.PlatformResult, row.CustomerPayout = ptr(p.PlatformResult), ptr(p.CustomerPayout)
		}
		out = append(out, row)
	}
	return out, nil
}

// AssetVolumeView is an asset-volume row for one caller (platformResult
// needs reports:read).
type AssetVolumeView struct {
	AssetID        string   `json:"assetId"`
	Symbol         string   `json:"symbol"`
	Trades         float64  `json:"trades"`
	Volume         float64  `json:"volume"`
	PlatformResult *float64 `json:"platformResult,omitempty"`
}

func GetAssetVolumeFor(ctx context.Context, days int64, v Visibility) ([]AssetVolumeView, error) {
	rows, err := GetAssetVolume(ctx, days)
	if err != nil {
		return nil, err
	}
	out := make([]AssetVolumeView, 0, len(rows))
	for _, r := range rows {
		row := AssetVolumeView{AssetID: r.AssetID, Symbol: r.Symbol, Trades: r.Trades, Volume: r.Volume}
		if v.PnL {
			row.PlatformResult = ptr(r.PlatformResult)
		}
		out = append(out, row)
	}
	return out, nil
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

	// Hedge realised P&L decomposed (hedgeRealisedPnl = trading - fees +
	// funding + adjustments), from the fills ledger by execution time, and
	// the reconciliation state it rests on. Unrealised is at the oldest mark.
	HedgeTradingPnl      float64   `json:"hedgeTradingPnl"`
	HedgeFees            float64   `json:"hedgeFees"`
	HedgeFunding         float64   `json:"hedgeFunding"`
	HedgeAdjustments     float64   `json:"hedgeAdjustments"`
	HedgeFills           int64     `json:"hedgeFills"`
	HedgeReconcileStatus string    `json:"hedgeReconcileStatus"`
	HedgeReconciledAt    *iso.Time `json:"hedgeReconciledAt"`
	HedgeMarkedAt        *iso.Time `json:"hedgeMarkedAt"`
	GeneratedAt          iso.Time  `json:"generatedAt"`
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
		hedge, err = hedging.GetHedgePnl(gctx, from, to)
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

		HedgeTradingPnl:      float64(hedge.TradingPnl),
		HedgeFees:            float64(hedge.Fees),
		HedgeFunding:         float64(hedge.Funding),
		HedgeAdjustments:     float64(hedge.Adjustments),
		HedgeFills:           hedge.Fills,
		HedgeReconcileStatus: hedge.ReconcileStatus,
		HedgeReconciledAt:    iso.Ptr(hedge.ReconciledAt),
		HedgeMarkedAt:        iso.Ptr(hedge.MarkedAt),
		GeneratedAt:          iso.From(mongox.Now()),
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
