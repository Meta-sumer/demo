package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	assetsvc "binarytrading/internal/service/assets"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/payouts"
)

var assetCategories = []string{"CRYPTO", "FOREX", "STOCK", "COMMODITY", "INDEX"}

// mountAssets is admin.assets.routes.ts (spec sections 24 and 25).
func mountAssets(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
	write := func(p string) chi.Router {
		return r.With(httpx.RequirePermission(p), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	}

	r.With(httpx.RequirePermission(config.PermAssetsRead)).Get("/", httpx.H(listAssets))
	r.With(httpx.RequirePermission(config.PermAssetsRead)).Get("/{id}", httpx.H(getAsset))
	write(config.PermAssetsWrite).Post("/", httpx.H(createAsset))
	write(config.PermAssetsWrite).Patch("/{id}", httpx.H(patchAsset))
	r.With(httpx.RequirePermission(config.PermPayoutsRead)).Get("/{id}/payouts", httpx.H(listPayouts))
	// POST, not PUT: publishing appends a new effective-dated version rather
	// than replacing one, and POST is what the admin console has always sent.
	write(config.PermPayoutsWrite).Post("/{id}/payouts", httpx.H(upsertPayout))
	write(config.PermPayoutsWrite).Delete("/payouts/{id}", httpx.H(deletePayout))
	// Resolve payout replacements a failure left half done.
	write(config.PermPayoutsWrite).Post("/payouts/repair", httpx.H(repairPayouts))
}

type assetWithPayouts struct {
	models.AssetView
	Payouts []models.PayoutView `json:"payouts"`
}

type assetDetail struct {
	models.AssetView
	Payouts       []models.PayoutView `json:"payouts"`
	PayoutHistory []models.PayoutView `json:"payoutHistory"`
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func listAssets(w http.ResponseWriter, r *http.Request) error {
	list, err := assetsvc.ListAssets(r.Context(), false)
	if err != nil {
		return err
	}
	out := make([]assetWithPayouts, 0, len(list))
	for _, a := range list {
		p, err := payouts.ListActivePayouts(r.Context(), a.ID)
		if err != nil {
			return err
		}
		out = append(out, assetWithPayouts{AssetView: a, Payouts: nonNil(p)})
	}
	return httpx.OK(w, out)
}

func getAsset(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	asset, err := assetsvc.GetAsset(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	active, err := payouts.ListActivePayouts(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	history, err := payouts.ListPayoutVersions(r.Context(), id.Hex(), nil)
	if err != nil {
		return err
	}
	return httpx.OK(w, assetDetail{AssetView: asset, Payouts: nonNil(active), PayoutHistory: nonNil(history)})
}

// parseTradingWindows is z.array(tradingWindowSchema).
func parseTradingWindows(b *body) []models.TradingWindow {
	raw, ok := b.get("tradingWindows")
	if !ok {
		return nil
	}
	items, ok := checkArray(b.v, "tradingWindows", raw, 0)
	if !ok {
		return nil
	}
	out := make([]models.TradingWindow, 0, len(items))
	for i, item := range items {
		path := sub("tradingWindows", i)
		obj, ok := checkObject(b.v, path, item)
		if !ok {
			continue
		}
		wb := &body{f: obj, v: b.v}
		var tw models.TradingWindow
		if rawDays, ok := wb.get("days"); !ok {
			b.v.Add(sub(path, "days"), "Required")
		} else if days, ok := checkArray(b.v, sub(path, "days"), rawDays, 1); ok {
			tw.Days = make([]int, 0, len(days))
			for j, d := range days {
				n, _ := checkNum(b.v, sub(sub(path, "days"), j), d, numRule{Int: true, Min: f(1), Max: f(7)})
				tw.Days = append(tw.Days, int(n))
			}
		}
		start, _ := wb.nestedNum(path, "startMinuteUtc", numRule{Int: true, Min: f(0), Max: f(1440)})
		end, _ := wb.nestedNum(path, "endMinuteUtc", numRule{Int: true, Min: f(0), Max: f(1440)})
		tw.StartMinuteUtc, tw.EndMinuteUtc = int(start), int(end)
		out = append(out, tw)
	}
	return out
}

func parseExpiries(b *body) []int64 {
	raw, ok := b.get("supportedExpiries")
	if !ok {
		return nil
	}
	items, ok := checkArray(b.v, "supportedExpiries", raw, 1)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(items))
	for i, item := range items {
		n, _ := checkNum(b.v, sub("supportedExpiries", i), item, numRule{Int: true, Positive: true})
		out = append(out, int64(n))
	}
	return out
}

// parseAssetBody is assetBodySchema; partial makes every key optional
// (assetBodySchema.partial()).
func parseAssetBody(r *http.Request, partial bool) (assetsvc.UpdateAssetInput, error) {
	b, err := readBody(r)
	if err != nil {
		return assetsvc.UpdateAssetInput{}, err
	}
	var in assetsvc.UpdateAssetInput
	str := func(key string, min, max int) *string {
		if !partial {
			if s, ok := b.reqStr(key, min, max); ok {
				return &s
			}
			return nil
		}
		return b.optStr(key, min, max)
	}
	in.Symbol = str("symbol", 3, 20)
	in.Name = str("name", 2, 80)
	var cat *string
	if partial {
		cat = b.optEnum("category", assetCategories...)
	} else if s, ok := b.reqEnum("category", assetCategories...); ok {
		cat = &s
	}
	if cat != nil {
		c := models.AssetCategory(*cat)
		in.Category = &c
	}
	in.ProviderSymbol = str("providerSymbol", 2, 40)
	in.Provider = b.optStr("provider", 2, 40)
	in.PricePrecision = b.optInt("pricePrecision", numRule{Min: f(0), Max: f(8)})
	in.MinStake = b.optInt("minStake", numRule{Positive: true})
	in.MaxStake = b.optInt("maxStake", numRule{Positive: true})
	in.SupportedExpiries = parseExpiries(b)
	in.ExposureLimit = b.optInt("exposureLimit", numRule{Positive: true})
	in.NetExposureLimit = b.optInt("netExposureLimit", numRule{Positive: true})
	in.HedgeEnabled = b.optBool("hedgeEnabled")
	in.TargetHedgeRatio = b.optNum("targetHedgeRatio", numRule{Min: f(0), Max: f(1)})
	in.HedgeThresholdPercentSet, in.HedgeThresholdPercent = b.nullableNum("hedgeThresholdPercent", numRule{Min: f(0), Max: f(100)})
	in.MinHedgeNotional = b.optInt("minHedgeNotional", numRule{Min: f(0)})
	in.MaxHedgeNotional = b.optInt("maxHedgeNotional", numRule{Positive: true})
	in.TradingWindows = parseTradingWindows(b)
	in.DisplayOrder = b.optInt("displayOrder", numRule{})
	in.Active = b.optBool("active")
	return in, b.err()
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func createAsset(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	in, err := parseAssetBody(r, false)
	if err != nil {
		return err
	}
	create := assetsvc.CreateAssetInput{
		Symbol:            deref(in.Symbol),
		Name:              deref(in.Name),
		Category:          deref(in.Category),
		ProviderSymbol:    deref(in.ProviderSymbol),
		Provider:          in.Provider,
		PricePrecision:    in.PricePrecision,
		MinStake:          in.MinStake,
		MaxStake:          in.MaxStake,
		SupportedExpiries: in.SupportedExpiries,
		ExposureLimit:     in.ExposureLimit,
		NetExposureLimit:  in.NetExposureLimit,
		HedgeEnabled:      in.HedgeEnabled,
		TargetHedgeRatio:  in.TargetHedgeRatio,
		// null and absent both leave the schema default (null).
		HedgeThresholdPercent: in.HedgeThresholdPercent,
		MinHedgeNotional:      in.MinHedgeNotional,
		MaxHedgeNotional:      in.MaxHedgeNotional,
		TradingWindows:        in.TradingWindows,
		DisplayOrder:          in.DisplayOrder,
		Active:                in.Active,
	}
	asset, err := assetsvc.CreateAsset(r.Context(), create)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.AssetCreated, "Asset", asset.ID)
	e.After = auditDoc(asset)
	audit.Write(r.Context(), e)
	return httpx.Created(w, asset)
}

func patchAsset(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	in, err := parseAssetBody(r, true)
	if err != nil {
		return err
	}
	before, after, err := assetsvc.UpdateAsset(r.Context(), id.Hex(), in)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.AssetUpdated, "Asset", chi.URLParam(r, "id"))
	e.Before, e.After = auditDoc(before), auditDoc(after)
	audit.Write(r.Context(), e)
	return httpx.OK(w, after)
}

func listPayouts(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	// z.coerce.boolean(): any non-empty string (even "false") is true.
	history := r.URL.Query().Get("history") != ""
	var rows []models.PayoutView
	if history {
		rows, err = payouts.ListPayoutVersions(r.Context(), id.Hex(), nil)
	} else {
		rows, err = payouts.ListActivePayouts(r.Context(), id.Hex())
	}
	if err != nil {
		return err
	}
	return httpx.OK(w, nonNil(rows))
}

func upsertPayout(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	expiry, _ := b.reqNum("expirySeconds", numRule{Int: true, Positive: true})
	percent, _ := b.reqNum("payoutPercent", numRule{Min: f(0), Max: f(500)})
	_, minStake := b.nullableInt("minStake", numRule{Positive: true})
	_, maxStake := b.nullableInt("maxStake", numRule{Positive: true})
	note := b.optStr("note", 0, 300)
	if err := b.err(); err != nil {
		return err
	}
	before, after, err := payouts.UpsertPayout(r.Context(), payouts.UpsertPayoutInput{
		AssetID: id.Hex(), ExpirySeconds: int64(expiry), PayoutPercent: percent,
		MinStake: minStake, MaxStake: maxStake, Note: note,
	}, actor.UserID)
	if err != nil {
		return err
	}

	// Payout terms are contractual: the audit row lands before the response.
	e := entry(r, actor, audit.PayoutUpdated, "PayoutConfig", after.ID)
	if before != nil {
		e.Before = auditDoc(*before)
	}
	e.After = auditDoc(after)
	audit.Write(r.Context(), e)
	return httpx.OK(w, after)
}

func deletePayout(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	payout, err := payouts.DeactivatePayout(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.PayoutUpdated, "PayoutConfig", chi.URLParam(r, "id"))
	e.After = map[string]any{"active": false}
	audit.Write(r.Context(), e)
	return httpx.OK(w, payout)
}

func repairPayouts(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	res, err := payouts.RepairPayoutReplacements(r.Context())
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.PayoutUpdated, "PayoutConfig", "")
	e.After, e.Reason = auditDoc(res), "repaired unfinished payout replacements"
	audit.Write(r.Context(), e)
	return httpx.OK(w, res)
}
