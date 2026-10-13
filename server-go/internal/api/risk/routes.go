// Package risk is the exposure and risk read API (spec section 12), mounted
// at /risk.
//
// Aggregated book data is commercially sensitive, so every route here requires
// an explicit permission - it is never exposed to a trading account.
package risk

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/errgroup"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	risksvc "binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
)

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	exposureRead := r.With(httpx.RequirePermission(config.PermExposureRead), httpx.RateLimit(httpx.RateLimits.Admin))
	riskRead := r.With(httpx.RequirePermission(config.PermRiskRead), httpx.RateLimit(httpx.RateLimits.Admin))

	exposureRead.Get("/exposure", httpx.H(portfolio))
	exposureRead.Get("/exposure/{id}", httpx.H(assetExposure))
	riskRead.Get("/assessments", httpx.H(assessments))
	riskRead.Get("/config", httpx.H(riskConfig))
}

func portfolio(w http.ResponseWriter, r *http.Request) error {
	p, err := exposure.GetPortfolioExposure(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, p)
}

func assetExposure(w http.ResponseWriter, r *http.Request) error {
	oid, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	id := oid.Hex()
	asset, err := assets.RequireAssetDoc(r.Context(), id)
	if err != nil {
		return err
	}
	var (
		snap       models.ExposureSnapshot
		assessment models.RiskAssessment
	)
	g, ctx := errgroup.WithContext(r.Context())
	g.Go(func() (err error) { snap, err = exposure.GetExposure(ctx, id, asset.Symbol); return })
	g.Go(func() (err error) { assessment, err = risksvc.AssessAsset(ctx, id); return })
	if err := g.Wait(); err != nil {
		return err
	}
	return httpx.OK(w, struct {
		Exposure models.ExposureSnapshot `json:"exposure"`
		Risk     models.RiskAssessment   `json:"risk"`
	}{snap, assessment})
}

func assessments(w http.ResponseWriter, r *http.Request) error {
	list, err := risksvc.AssessAllAssets(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, list)
}

func riskConfig(w http.ResponseWriter, r *http.Request) error {
	cfg, err := riskconfig.GetRiskConfig(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, models.ToRiskConfigLean(cfg))
}
