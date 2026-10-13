package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	binaryapi "binarytrading/internal/api/binary"
	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/binary"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/hedging"
	"binarytrading/internal/service/marketdata"
	mdproviders "binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/risk"
)

// mountTrading is admin.trading.routes.ts (spec sections 19, 21, 22, 23):
// trades, exposure, hedging and market data.
func mountTrading(r chi.Router) {
	perm := func(p string) chi.Router { return r.With(httpx.RequirePermission(p)) }
	write := func(p string) chi.Router {
		return r.With(httpx.RequirePermission(p), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	}

	perm(config.PermTradesRead).Get("/binary/trades", httpx.H(listTrades))
	perm(config.PermTradesRead).Get("/binary/trades/{id}", httpx.H(getTrade))
	write(config.PermTradesCancel).Post("/binary/trades/{id}/cancel", httpx.H(cancelTrade))
	perm(config.PermExposureRead).Get("/exposure", httpx.H(getExposure))
	perm(config.PermRiskRead).Get("/risk", httpx.H(getRisk))
	perm(config.PermHedgeRead).Get("/hedging", httpx.H(getHedging))
	perm(config.PermHedgeRead).Get("/hedging/orders", httpx.H(listHedgeOrders))
	write(config.PermHedgeWrite).Post("/hedging/orders/{id}/cancel", httpx.H(cancelHedgeOrder))
	perm(config.PermMarketDataRead).Get("/market-data", httpx.H(getMarketData))
}

func listTrades(w http.ResponseWriter, r *http.Request) error {
	q, err := binaryapi.ParseListTradesQuery(r, true)
	if err != nil {
		return err
	}
	items, total, err := binary.ListTrades(r.Context(), q)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, q.Page, q.Limit, total))
}

func getTrade(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	trade, err := binary.GetTrade(r.Context(), id.Hex(), "")
	if err != nil {
		return err
	}
	return httpx.OK(w, trade)
}

func cancelTrade(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	reason, err := binaryapi.ParseCancelTradeBody(r)
	if err != nil {
		return err
	}
	before, err := binary.GetTrade(r.Context(), id.Hex(), "")
	if err != nil {
		return err
	}
	after, err := binary.CancelTrade(r.Context(), id.Hex(), reason)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.TradeCancelled, "BinaryTrade", chi.URLParam(r, "id"))
	e.Before, e.After, e.Reason = auditDoc(before), auditDoc(after), reason
	audit.Write(r.Context(), e)
	return httpx.OK(w, after)
}

type exposureRow struct {
	models.ExposureSnapshot
	Risk *models.RiskAssessment `json:"risk"`
}

func getExposure(w http.ResponseWriter, r *http.Request) error {
	portfolio, err := exposure.GetPortfolioExposure(r.Context())
	if err != nil {
		return err
	}
	assessments, err := risk.AssessAllAssets(r.Context())
	if err != nil {
		return err
	}
	byAsset := map[string]*models.RiskAssessment{}
	for i := range assessments {
		byAsset[assessments[i].AssetID] = &assessments[i]
	}
	rows := make([]exposureRow, 0, len(portfolio.Assets))
	for _, e := range portfolio.Assets {
		rows = append(rows, exposureRow{ExposureSnapshot: e, Risk: byAsset[e.AssetID]})
	}
	type totals struct {
		GrossExposure      int64 `json:"grossExposure"`
		NetExposure        int64 `json:"netExposure"`
		PotentialLiability int64 `json:"potentialLiability"`
		OpenTrades         int64 `json:"openTrades"`
	}
	return httpx.OK(w, struct {
		Totals totals        `json:"totals"`
		Assets []exposureRow `json:"assets"`
	}{totals{portfolio.TotalGrossExposure, portfolio.TotalNetExposure, portfolio.TotalPotentialLiability, portfolio.TotalOpenTrades}, rows})
}

func getRisk(w http.ResponseWriter, r *http.Request) error {
	list, err := risk.AssessAllAssets(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, nonNil(list))
}

func getHedging(w http.ResponseWriter, r *http.Request) error {
	rows, err := hedging.GetHedgeMonitor(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, nonNil(rows))
}

func listHedgeOrders(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	assetID := q.OptStr("assetId")
	status := q.OptEnum("status", models.HedgeOrderStatusValues...)
	page, limit := q.Page()
	if err := q.Err(); err != nil {
		return err
	}
	query := hedging.HedgeOrderQuery{Page: page, Limit: limit}
	if assetID != nil {
		query.AssetID = *assetID
	}
	if status != nil {
		query.Status = *status
	}
	items, total, err := hedging.ListHedgeOrders(r.Context(), query)
	if err != nil {
		return err
	}
	views := make([]models.HedgeOrderView, 0, len(items))
	for i := range items {
		views = append(views, hedging.ToHedgeView(&items[i]))
	}
	return httpx.OK(w, httpx.Paginate(views, page, limit, total))
}

func cancelHedgeOrder(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	order, err := hedging.CancelHedgeOrder(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.HedgeOrderCancelled, "HedgeOrder", chi.URLParam(r, "id"))
	e.After = auditDoc(order)
	audit.Write(r.Context(), e)
	return httpx.OK(w, order)
}

func getMarketData(w http.ResponseWriter, r *http.Request) error {
	feeds, err := marketdata.GetAllFeedHealth(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, struct {
		Provider *mdproviders.Status          `json:"provider"`
		Feeds    []models.FeedHealthSnapshot  `json:"feeds"`
		Latency  []metrics.LatencyReportEntry `json:"latency"`
	}{marketdata.GetProviderStatus(), nonNil(feeds), metrics.GetLatencyReport(r.Context())})
}
