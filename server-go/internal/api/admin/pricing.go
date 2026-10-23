package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/riskconfig"
)

// mountPricing is the pricing engine's console (docs/audit/pricing).
//
//	GET  /admin/pricing                  live offer terms (components, guard) for every payout row
//	GET  /admin/pricing?assetId=         one asset
//	GET  /admin/pricing/market           per asset: EWMA volatility state and quote-rejection counters
//	GET  /admin/pricing/models           published guard-model versions (newest first)
//	GET  /admin/pricing/models/active    the active guard model with its entries
//	POST /admin/pricing/halt             kill switch: {symbol?, reason}   (risk:write)
//	POST /admin/pricing/resume           lift it:     {symbol?}           (risk:write)
//
// The rules themselves (spec section 11) are edited with PUT
// /admin/settings/risk and the per-(asset, expiry) terms on the payout rows:
// both versioned and audited.
func mountPricing(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
	read := r.With(httpx.RequirePermission(config.PermPayoutsRead))
	read.Get("/", httpx.H(getPricingBoard))
	read.Get("/market", httpx.H(getPricingMarket))
	read.Get("/models", httpx.H(listPricingModels))
	read.Get("/models/active", httpx.H(getActivePricingModel))
	write := r.With(httpx.RequirePermission(config.PermRiskWrite), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	write.Post("/halt", httpx.H(haltPricing))
	write.Post("/resume", httpx.H(resumePricing))
}

func getPricingBoard(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	assetID := ""
	if id := q.OptObjectID("assetId"); id != nil {
		assetID = id.Hex()
	}
	if err := q.Err(); err != nil {
		return err
	}
	cfg, err := riskconfig.GetRiskConfig(r.Context())
	if err != nil {
		return err
	}
	rows, err := pricing.AdminBoard(r.Context(), assetID)
	if err != nil {
		return err
	}
	var model *pricing.ModelView
	if m, err := pricing.ActiveModel(r.Context()); err == nil {
		v := pricing.ToModelView(m, false)
		model = &v
	}
	return httpx.OK(w, struct {
		Mode              string                `json:"mode"`
		RuleLabel         string                `json:"ruleLabel"`
		RiskConfigVersion int64                 `json:"riskConfigVersion"`
		TradingHalted     bool                  `json:"tradingHalted"`
		TradingHaltReason string                `json:"tradingHaltReason"`
		Halts             []models.PricingHalt  `json:"halts"`
		Model             *pricing.ModelView    `json:"model"`
		HedgeEstimator    string                `json:"hedgeEstimator"`
		Rows              []pricing.AdminRow    `json:"rows"`
		Config            models.RiskConfigLean `json:"config"`
	}{cfg.PricingMode, cfg.PricingRuleLabel, cfg.Version, cfg.TradingHalted, cfg.TradingHaltReason,
		nonNil(cfg.PricingHalts), model, pricing.EstimatorName(), rows, models.ToRiskConfigLean(cfg)})
}

type marketRow struct {
	AssetID    string               `json:"assetId"`
	Symbol     string               `json:"symbol"`
	Baseline   float64              `json:"baselineVol"`
	Sigma      float64              `json:"sigma"`
	VolRatio   float64              `json:"volRatio"`
	Vol        *marketdata.VolState `json:"vol"`
	Rejections map[string]int64     `json:"rejections"`
}

func getPricingMarket(w http.ResponseWriter, r *http.Request) error {
	cfg, err := riskconfig.GetRiskConfig(r.Context())
	if err != nil {
		return err
	}
	docs, err := assets.ListAssetDocs(r.Context(), false)
	if err != nil {
		return err
	}
	rows := make([]marketRow, 0, len(docs))
	for _, a := range docs {
		row := marketRow{AssetID: a.ID.Hex(), Symbol: a.Symbol, Baseline: marketdata.BaselineVolFor(cfg, a.Symbol),
			Rejections: map[string]int64{}}
		if v, ok, err := marketdata.GetVolatility(r.Context(), row.AssetID); err == nil && ok {
			row.Vol, row.Sigma = &v, v.Sigma()
			if row.Baseline > 0 {
				row.VolRatio = row.Sigma / row.Baseline
			}
		}
		if stats, err := marketdata.QuoteRejectionStats(r.Context(), row.AssetID); err == nil {
			for k, v := range stats {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					row.Rejections[k] = n
				}
			}
		}
		rows = append(rows, row)
	}
	recent, err := marketdata.RecentQuoteRejections(r.Context(), 50)
	if err != nil {
		return err
	}
	return httpx.OK(w, struct {
		Assets           []marketRow       `json:"assets"`
		RecentRejections []json.RawMessage `json:"recentRejections"`
	}{rows, recent})
}

func listPricingModels(w http.ResponseWriter, r *http.Request) error {
	list, err := pricing.ListModelVersions(r.Context(), 50)
	if err != nil {
		return err
	}
	out := make([]pricing.ModelView, 0, len(list))
	for i := range list {
		out = append(out, pricing.ToModelView(&list[i], false))
	}
	return httpx.OK(w, out)
}

func getActivePricingModel(w http.ResponseWriter, r *http.Request) error {
	m, err := pricing.ActiveModel(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, pricing.ToModelView(m, true))
}

func haltPricing(w http.ResponseWriter, r *http.Request) error {
	return switchPricing(w, r, true)
}

func resumePricing(w http.ResponseWriter, r *http.Request) error {
	return switchPricing(w, r, false)
}

// switchPricing is the kill switch (spec 7.4): a versioned risk-config
// change, audited before the response like every risk setting.
func switchPricing(w http.ResponseWriter, r *http.Request, halt bool) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	symbol := ""
	if s := b.optStr("symbol", 1, 40); s != nil {
		symbol = *s
	}
	reason := ""
	if halt {
		if s, ok := b.reqStr("reason", 3, 300); ok {
			reason = s
		}
	}
	if err := b.err(); err != nil {
		return err
	}
	var before, after *models.RiskConfig
	if halt {
		before, after, err = pricing.Halt(r.Context(), symbol, reason, actor.UserID)
	} else {
		before, after, err = pricing.Resume(r.Context(), symbol, actor.UserID)
	}
	if err != nil {
		return err
	}
	action := "pricing.resumed"
	if halt {
		action = "pricing.halted"
	}
	e := entry(r, actor, action, "RiskConfig", strconv.FormatInt(after.Version, 10))
	e.Before, e.After, e.Reason = haltState(before), haltState(after), reason
	audit.Write(r.Context(), e)
	return httpx.OK(w, models.ToRiskConfigLean(after))
}

func haltState(c *models.RiskConfig) map[string]any {
	return map[string]any{"version": c.Version, "tradingHalted": c.TradingHalted, "tradingHaltReason": c.TradingHaltReason,
		"pricingHalts": nonNil(c.PricingHalts)}
}
