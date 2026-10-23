// Package assets is the tradable asset catalogue for the client, mounted at
// /assets.
//
// Each row carries its live payout table and feed status, which is what the
// trading screen needs to decide whether the UP/DOWN buttons are enabled.
// payouts[].payoutPercent is the pricing engine's current indicative payout
// (the lower of UP and DOWN at the smallest stake, refreshed every second);
// an expiry the engine cannot price is left out and named in
// pricing.unavailable. GET /binary/quotes gives the exact per-direction
// price for a stake.
package assets

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	assetsvc "binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/pricing"
	"binarytrading/internal/service/riskconfig"
)

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	read := r.With(httpx.RateLimit(httpx.RateLimits.Read))

	read.Get("/", httpx.H(list))
	read.Get("/{id}", httpx.H(get))
}

type assetDetail struct {
	models.AssetView
	Payouts []pricing.CustomerPayout  `json:"payouts"`
	Feed    models.FeedHealthSnapshot `json:"feed"`
	// Pricing mode, model version and the expiries that cannot be priced
	// right now, with the reason.
	Pricing pricing.AssetPricing `json:"pricing"`
	// Percent move of the last price against the first candle open of the
	// last 24h; null when there is not enough data.
	Change24hPercent *float64 `json:"change24hPercent"`
	// What an exact match of entry and expiry price settles as: REFUND, LOSS
	// or WIN. It is platform-wide, and sent with each asset so the trade panel
	// states the rule the trade will be settled by rather than assuming one.
	DrawPolicy models.DrawPolicy `json:"drawPolicy"`
}

type assetListRow struct {
	assetDetail
	Sentiment *exposure.AssetSentiment `json:"sentiment"`
}

func detail(r *http.Request, asset models.AssetView, change map[string]*float64, drawPolicy models.DrawPolicy) (assetDetail, error) {
	payoutRows, info, err := pricing.CustomerPayouts(r.Context(), asset.ID)
	if err != nil {
		return assetDetail{}, err
	}
	if payoutRows == nil {
		payoutRows = []pricing.CustomerPayout{}
	}
	feed, err := marketdata.GetFeedHealth(r.Context(), asset.ID, asset.Symbol)
	if err != nil {
		return assetDetail{}, err
	}
	return assetDetail{AssetView: asset, Payouts: payoutRows, Feed: feed, Pricing: info, Change24hPercent: change[asset.ID], DrawPolicy: drawPolicy}, nil
}

func list(w http.ResponseWriter, r *http.Request) error {
	views, err := assetsvc.ListAssets(r.Context(), true)
	if err != nil {
		return err
	}
	sentiment, err := exposure.GetSentiment(r.Context())
	if err != nil {
		return err
	}
	byAsset := make(map[string]*exposure.AssetSentiment, len(sentiment))
	for i := range sentiment {
		byAsset[sentiment[i].AssetID] = &sentiment[i]
	}

	cfg, err := riskconfig.GetRiskConfig(r.Context())
	if err != nil {
		return err
	}

	change := marketdata.GetChange24h(r.Context())
	out := make([]assetListRow, 0, len(views))
	for _, v := range views {
		d, err := detail(r, v, change, cfg.DrawPolicy)
		if err != nil {
			return err
		}
		out = append(out, assetListRow{assetDetail: d, Sentiment: byAsset[v.ID]})
	}
	return httpx.OK(w, out)
}

func get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	asset, err := assetsvc.GetAsset(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	cfg, err := riskconfig.GetRiskConfig(r.Context())
	if err != nil {
		return err
	}
	d, err := detail(r, asset, marketdata.GetChange24h(r.Context()), cfg.DrawPolicy)
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}
