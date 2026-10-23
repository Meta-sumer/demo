package admin

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	adminsvc "binarytrading/internal/service/admin"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/service/riskconfig"
)

// mountCore is admin.routes.ts's secured plugin (the admin login is mounted
// separately, outside the authenticated group).
func mountCore(r chi.Router) {
	perm := func(p string) chi.Router { return r.With(httpx.RequirePermission(p)) }
	write := func(p string) chi.Router {
		return r.With(httpx.RequirePermission(p), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	}

	r.Get("/dashboard", httpx.H(dashboard))
	r.Get("/dashboard/series", httpx.H(dashboardSeries))
	r.Get("/dashboard/assets", httpx.H(dashboardAssets))
	perm(config.PermReportsRead).Get("/reports/pnl", httpx.H(reportPnl))
	perm(config.PermReportsRead).Get("/reports/counters", httpx.H(reportCounters))
	r.Get("/metrics/latency", httpx.H(latency))
	perm(config.PermAuditRead).Get("/audit-logs", httpx.H(auditLogs))
	perm(config.PermRiskRead).Get("/settings/risk", httpx.H(getRiskSettings))
	write(config.PermRiskWrite).Put("/settings/risk", httpx.H(putRiskSettings))
	perm(config.PermReconciliationRead).Get("/reconciliation", httpx.H(listReconciliation))
	write(config.PermReconciliationRead).Post("/reconciliation/run", httpx.H(runReconciliation))
	write(config.PermReconciliationResolve).Post("/reconciliation/{id}/resolve", httpx.H(resolveReconciliation))
}

// visibility is what the caller may see on the dashboard: exposure needs
// exposure:read and P&L needs reports:read, as their own pages do. Any admin
// reaches the dashboard; a SUPPORT agent sees activity, not the book.
func visibility(r *http.Request) adminsvc.Visibility {
	a := httpx.ActorFrom(r)
	if a == nil {
		return adminsvc.Visibility{}
	}
	return adminsvc.VisibilityFor(a.Role, a.Permissions)
}

func dashboard(w http.ResponseWriter, r *http.Request) error {
	s, err := adminsvc.GetDashboardFor(r.Context(), visibility(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, s)
}

// seriesDays is seriesQuerySchema: days 1..90, default 14.
func seriesDays(r *http.Request) (int64, error) {
	q := httpx.Query(r)
	days := q.Int("days", 14, 1, 90)
	return days, q.Err()
}

func dashboardSeries(w http.ResponseWriter, r *http.Request) error {
	days, err := seriesDays(r)
	if err != nil {
		return err
	}
	series, err := adminsvc.GetDailySeriesFor(r.Context(), days, visibility(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, series)
}

func dashboardAssets(w http.ResponseWriter, r *http.Request) error {
	days, err := seriesDays(r)
	if err != nil {
		return err
	}
	rows, err := adminsvc.GetAssetVolumeFor(r.Context(), days, visibility(r))
	if err != nil {
		return err
	}
	return httpx.OK(w, rows)
}

func reportPnl(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	fromP, toP := dateRange(q)
	if err := q.Err(); err != nil {
		return err
	}
	to := time.Now().UTC().Truncate(time.Millisecond)
	if toP != nil {
		to = *toP
	}
	from := to.Add(-30 * 24 * time.Hour)
	if fromP != nil {
		from = *fromP
	}
	pnl, err := adminsvc.GetPlatformPnl(r.Context(), from, to)
	if err != nil {
		return err
	}
	return httpx.OK(w, pnl)
}

func reportCounters(w http.ResponseWriter, r *http.Request) error {
	return httpx.OK(w, metrics.GetCounters(r.Context(), ""))
}

func latency(w http.ResponseWriter, r *http.Request) error {
	return httpx.OK(w, metrics.GetLatencyReport(r.Context()))
}

func auditLogs(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	from, to := dateRange(q)
	actorID := q.OptStr("actorId")
	action := optQueryStr(q, "action", 0, 80)
	resource := optQueryStr(q, "resource", 0, 80)
	resourceID := optQueryStr(q, "resourceId", 0, 80)
	if err := q.Err(); err != nil {
		return err
	}
	query := audit.Query{From: from, To: to, Page: page, Limit: limit}
	// Empty strings are falsy in the TS filter builder and are ignored.
	if actorID != nil && *actorID != "" {
		id, ok := parseHex(*actorID)
		if !ok {
			// Mongoose cannot cast the filter value (CastError).
			return apperr.New(apperr.ValidationError, "Malformed identifier", 400)
		}
		query.ActorID = &id
	}
	if action != nil && *action != "" {
		query.Action = action
	}
	if resource != nil && *resource != "" {
		query.Resource = resource
	}
	if resourceID != nil && *resourceID != "" {
		query.ResourceID = resourceID
	}
	items, total, err := audit.List(r.Context(), query)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func getRiskSettings(w http.ResponseWriter, r *http.Request) error {
	cfg, err := riskconfig.GetRiskConfig(r.Context())
	if err != nil {
		return err
	}
	versions, err := riskconfig.ListRiskConfigVersions(r.Context(), 20)
	if err != nil {
		return err
	}
	views := make([]models.RiskConfigLean, 0, len(versions))
	for i := range versions {
		views = append(views, models.ToRiskConfigLean(&versions[i]))
	}
	return httpx.OK(w, struct {
		Config   models.RiskConfigLean   `json:"config"`
		Versions []models.RiskConfigLean `json:"versions"`
	}{models.ToRiskConfigLean(cfg), views})
}

// parseRiskPatch is riskConfigPatchSchema.
func parseRiskPatch(r *http.Request) (riskconfig.RiskConfigPatch, *string, error) {
	b, err := readBody(r)
	if err != nil {
		return riskconfig.RiskConfigPatch{}, nil, err
	}
	var p riskconfig.RiskConfigPatch
	nonneg := numRule{Min: f(0)}
	p.MaxAssetExposure = b.optInt("maxAssetExposure", nonneg)
	p.MaxUserExposure = b.optInt("maxUserExposure", nonneg)
	p.MaxGrossExposure = b.optInt("maxGrossExposure", nonneg)
	p.MinTradeAmount = b.optInt("minTradeAmount", nonneg)
	p.MaxTradeAmount = b.optInt("maxTradeAmount", nonneg)
	p.MaxOpenTradesPerUser = b.optInt("maxOpenTradesPerUser", numRule{Min: f(1)})
	// Liability ceilings, minor units; 0 = no limit (riskconfig validates
	// that they are not negative).
	p.MaxExpiryLiability = b.optInt("maxExpiryLiability", nonneg)
	p.MaxAssetLiability = b.optInt("maxAssetLiability", nonneg)
	p.MaxPlatformLiability = b.optInt("maxPlatformLiability", nonneg)
	p.MaxUserAssetConcentrationPercent = b.optNum("maxUserAssetConcentrationPercent", numRule{Min: f(1), Max: f(100)})
	p.HedgeThresholdPercent = b.optNum("hedgeThresholdPercent", numRule{Min: f(0), Max: f(100)})
	p.CriticalExposurePercent = b.optNum("criticalExposurePercent", numRule{Min: f(1), Max: f(100)})
	if raw, ok := b.get("hedgeLevels"); ok {
		if items, ok := checkArray(b.v, "hedgeLevels", raw, 1); ok {
			levels := make([]models.HedgeLevel, 0, len(items))
			for i, item := range items {
				path := sub("hedgeLevels", i)
				obj, ok := checkObject(b.v, path, item)
				if !ok {
					continue
				}
				var lv models.HedgeLevel
				lb := &body{f: obj, v: b.v}
				lv.Name, _ = lb.nested(path, "name", func(p string, raw []byte) (string, bool) { return checkStr(b.v, p, raw, 2, 40) })
				lv.FromPercent, _ = lb.nestedNum(path, "fromPercent", numRule{Min: f(0)})
				lv.ToPercent, _ = lb.nestedNum(path, "toPercent", numRule{Min: f(0)})
				lv.HedgeRatio, _ = lb.nestedNum(path, "hedgeRatio", numRule{Min: f(0), Max: f(1)})
				levels = append(levels, lv)
			}
			p.HedgeLevels = levels
		}
	}
	p.MinSecondsToExpiryForHedge = b.optInt("minSecondsToExpiryForHedge", numRule{Min: f(0)})
	p.VolatilityHedgeBoost = b.optNum("volatilityHedgeBoost", numRule{Min: f(0), Max: f(1)})
	p.SlippageTolerancePercent = b.optNum("slippageTolerancePercent", numRule{Min: f(0), Max: f(100)})
	p.EarlyCloseEnabled = b.optBool("earlyCloseEnabled")
	p.EarlyCloseFeePercent = b.optNum("earlyCloseFeePercent", numRule{Min: f(0), Max: f(50)})
	p.MinSecondsRemainingForEarlyClose = b.optInt("minSecondsRemainingForEarlyClose", numRule{Min: f(0), Max: f(3600)})
	if s := b.optEnum("drawPolicy", models.DrawPolicyValues...); s != nil {
		dp := models.DrawPolicy(*s)
		p.DrawPolicy = &dp
	}
	p.FeedHealthyMaxMs = b.optInt("feedHealthyMaxMs", numRule{Min: f(1)})
	p.FeedDegradedMaxMs = b.optInt("feedDegradedMaxMs", numRule{Min: f(1)})
	// feedStaleMaxMs is retired (never read): an old admin build may still
	// send it, and it is ignored.
	p.FeedTickMaxAgeMs = b.optInt("feedTickMaxAgeMs", numRule{Min: f(1)})
	p.HaltTradingOnStaleFeed = b.optBool("haltTradingOnStaleFeed")
	p.HaltTradingOnDegradedFeed = b.optBool("haltTradingOnDegradedFeed")
	p.TradingHalted = b.optBool("tradingHalted")
	// Feed data rules and the settlement policy; the cross-field and hard
	// limits are in riskconfig (policy_validate.go).
	p.FeedTickHistoryRetentionMs = b.optInt("feedTickHistoryRetentionMs", numRule{Min: f(60_000), Max: f(86_400_000)})
	p.FeedGapMinMs = b.optInt("feedGapMinMs", numRule{Min: f(500), Max: f(600_000)})
	p.FeedGapIntervalMultiple = b.optNum("feedGapIntervalMultiple", numRule{Min: f(2), Max: f(1000)})
	p.FeedSaturationHoldMs = b.optInt("feedSaturationHoldMs", numRule{Min: f(0), Max: f(600_000)})
	p.FeedMaxClockDriftMs = b.optInt("feedMaxClockDriftMs", numRule{Min: f(50), Max: f(60_000)})
	p.FeedClockCheckIntervalMs = b.optInt("feedClockCheckIntervalMs", numRule{Min: f(0), Max: f(86_400_000)})
	p.SettlementMaxTickAgeMs = b.optInt("settlementMaxTickAgeMs", numRule{Min: f(100), Max: f(60_000)})
	p.SettlementConfirmationWindowMs = b.optInt("settlementConfirmationWindowMs", numRule{Min: f(0), Max: f(60_000)})
	p.SettlementDeferWindowMs = b.optInt("settlementDeferWindowMs", numRule{Min: f(1000), Max: f(3_600_000)})
	p.SettlementRetryDelayMs = b.optInt("settlementRetryDelayMs", numRule{Min: f(100), Max: f(60_000)})
	p.SettlementMaxClockSkewMs = b.optInt("settlementMaxClockSkewMs", numRule{Min: f(1), Max: f(60_000)})
	p.SettlementNoPriceRule = b.optEnum("settlementNoPriceRule", models.NoPriceRuleValues...)
	p.EarlyCloseFloorMs = b.optInt("earlyCloseFloorMs", numRule{Min: f(models.EarlyCloseHardFloorMs), Max: f(3_600_000)})
	p.EarlyCloseMaxTickAgeMs = b.optInt("earlyCloseMaxTickAgeMs", numRule{Min: f(100), Max: f(10_000)})
	p.EarlyCloseQuoteMaxAgeMs = b.optInt("earlyCloseQuoteMaxAgeMs", numRule{Min: f(1000), Max: f(60_000)})
	p.EarlyCloseQuoteTolerancePercent = b.optNum("earlyCloseQuoteTolerancePercent", numRule{Min: f(0), Max: f(50)})
	// Pricing engine (docs/audit/pricing/pricing-spec.md; the owner's
	// specification section 11). Halts are set with POST /admin/pricing/halt.
	p.TradingHaltReason = b.optStr("tradingHaltReason", 0, 300)
	p.PricingMode = b.optEnum("pricingMode", models.PricingModeValues...)
	p.PricingRuleLabel = b.optStr("pricingRuleLabel", 3, 60)
	p.PricingStaleAfterMs = b.optInt("pricingStaleAfterMs", numRule{Min: f(100), Max: f(60_000)})
	p.PricingMaxSpreadBps = b.optNum("pricingMaxSpreadBps", numRule{Min: f(0), Max: f(1000)})
	p.PricingMaxJumpBps = b.optNum("pricingMaxJumpBps", numRule{Min: f(1), Max: f(10_000)})
	p.PricingJumpConfirmations = b.optInt("pricingJumpConfirmations", numRule{Min: f(1), Max: f(50)})
	p.PricingPendingOutlierMaxAgeMs = b.optInt("pricingPendingOutlierMaxAgeMs", numRule{Min: f(0), Max: f(600_000)})
	p.PricingEwmaLambda = b.optNum("pricingEwmaLambda", numRule{Min: f(0.5), Max: f(0.9999)})
	p.PricingVolSampleIntervalMs = b.optInt("pricingVolSampleIntervalMs", numRule{Min: f(0), Max: f(60_000)})
	p.PricingKVol = b.optNum("pricingKVol", numRule{Min: f(0), Max: f(1)})
	p.PricingMaxVolAdj = b.optNum("pricingMaxVolAdj", numRule{Min: f(0), Max: f(1)})
	p.PricingVolBlockRatio = b.optNum("pricingVolBlockRatio", numRule{Min: f(1), Max: f(100)})
	p.PricingKSpread = b.optNum("pricingKSpread", numRule{Min: f(0), Max: f(1)})
	p.PricingMaxSpreadAdj = b.optNum("pricingMaxSpreadAdj", numRule{Min: f(0), Max: f(1)})
	p.PricingKExposure = b.optNum("pricingKExposure", numRule{Min: f(0), Max: f(1)})
	p.PricingMaxExposureAdj = b.optNum("pricingMaxExposureAdj", numRule{Min: f(0), Max: f(1)})
	if raw, ok := b.get("pricingExpiryFactors"); ok {
		if items, ok := checkArray(b.v, "pricingExpiryFactors", raw, 0); ok {
			factors := make([]models.PricingExpiryFactor, 0, len(items))
			for i, item := range items {
				path := sub("pricingExpiryFactors", i)
				obj, ok := checkObject(b.v, path, item)
				if !ok {
					continue
				}
				fb := &body{f: obj, v: b.v}
				exp, _ := fb.nestedNum(path, "expirySeconds", numRule{Int: true, Positive: true})
				fac, _ := fb.nestedNum(path, "factor", numRule{Min: f(0.01), Max: f(10)})
				factors = append(factors, models.PricingExpiryFactor{ExpirySeconds: int64(exp), Factor: fac})
			}
			p.PricingExpiryFactors = factors
		}
	}
	p.PricingPayoutStep = b.optNum("pricingPayoutStep", numRule{Min: f(0.0001), Max: f(0.1)})
	p.PricingOfferTTLMs = b.optInt("pricingOfferTtlMs", numRule{Min: f(500), Max: f(60_000)})
	p.PricingMaxSlippageBps = b.optNum("pricingMaxSlippageBps", numRule{Min: f(0), Max: f(1000)})
	p.PricingDisplayTolerancePercent = b.optNum("pricingDisplayTolerancePercent", numRule{Min: f(0), Max: f(5)})
	p.PricingGuardEnabled = b.optBool("pricingGuardEnabled")
	p.PricingGuardMinEdgePercent = b.optNum("pricingGuardMinEdgePercent", numRule{Min: f(-10), Max: f(50)})
	p.PricingGuardConservative = b.optBool("pricingGuardConservative")
	p.PricingHedgeUnavailableAction = b.optEnum("pricingHedgeUnavailableAction", models.PricingActionValues...)
	p.PricingHedgeUnavailableAdj = b.optNum("pricingHedgeUnavailableAdj", numRule{Min: f(0), Max: f(1)})
	p.PricingHedgeTakerFeeBps = b.optNum("pricingHedgeTakerFeeBps", numRule{Min: f(0), Max: f(1000)})
	p.PricingHedgeSpreadBps = b.optNum("pricingHedgeSpreadBps", numRule{Min: f(0), Max: f(1000)})
	p.PricingHedgeSlippageBps = b.optNum("pricingHedgeSlippageBps", numRule{Min: f(0), Max: f(1000)})
	if raw, ok := b.get("pricingSymbolRules"); ok {
		if items, ok := checkArray(b.v, "pricingSymbolRules", raw, 0); ok {
			rules := make([]models.PricingSymbolRule, 0, len(items))
			for i, item := range items {
				path := sub("pricingSymbolRules", i)
				obj, ok := checkObject(b.v, path, item)
				if !ok {
					continue
				}
				rb := &body{f: obj, v: b.v}
				var sr models.PricingSymbolRule
				sr.Symbol, _ = rb.nested(path, "symbol", func(p string, raw []byte) (string, bool) { return checkStr(b.v, p, raw, 3, 20) })
				if raw, ok := rb.get("enabled"); ok {
					sr.Enabled, _ = checkBool(b.v, sub(path, "enabled"), raw)
				}
				if raw, ok := rb.get("disabledReason"); ok {
					sr.DisabledReason, _ = checkStr(b.v, sub(path, "disabledReason"), raw, 0, 200)
				}
				sr.BasePayout, _ = rb.nestedNum(path, "basePayout", numRule{Min: f(0.01), Max: f(5)})
				sr.MinPayout, _ = rb.nestedNum(path, "minPayout", numRule{Min: f(0.005), Max: f(5)})
				sr.MaxPayout, _ = rb.nestedNum(path, "maxPayout", numRule{Min: f(0.005), Max: f(5)})
				sr.BaselineVol, _ = rb.nestedNum(path, "baselineVol", numRule{Min: f(1e-9), Max: f(0.1)})
				sr.HedgeCost, _ = rb.nestedNum(path, "hedgeCost", numRule{Min: f(0), Max: f(1)})
				sr.Margin, _ = rb.nestedNum(path, "margin", numRule{Min: f(0), Max: f(1)})
				mx, _ := rb.nestedNum(path, "maxExposure", numRule{Int: true, Positive: true})
				sr.MaxExposure = int64(mx)
				rules = append(rules, sr)
			}
			p.PricingSymbolRules = rules
		}
	}
	// Calibrated Sell-now correction (audit F-12); ranges are checked again
	// by riskconfig.ValidateConfig.
	p.EarlyCloseMinProbabilityPercent = b.optNum("earlyCloseMinProbabilityPercent", numRule{Min: f(0.01), Max: f(10)})
	if raw, ok := b.get("earlyCloseCalibration"); ok {
		if items, ok := checkArray(b.v, "earlyCloseCalibration", raw, 0); ok {
			rows := make([]models.EarlyCloseCalibrationRow, 0, len(items))
			for i, item := range items {
				path := sub("earlyCloseCalibration", i)
				obj, ok := checkObject(b.v, path, item)
				if !ok {
					continue
				}
				rb := &body{f: obj, v: b.v}
				var row models.EarlyCloseCalibrationRow
				if raw, ok := rb.get("symbol"); ok {
					row.Symbol, _ = checkStr(b.v, sub(path, "symbol"), raw, 0, 20)
				}
				sec, _ := rb.nestedNum(path, "maxSecondsRemaining", numRule{Int: true, Min: f(0)})
				row.MaxSecondsRemaining = int64(sec)
				row.MaxVolRatio, _ = rb.nestedNum(path, "maxVolRatio", numRule{Min: f(0), Max: f(1000)})
				row.SigmaScale, _ = rb.nestedNum(path, "sigmaScale", numRule{Min: f(0.01), Max: f(20)})
				row.MinVolRatio, _ = rb.nestedNum(path, "minVolRatio", numRule{Min: f(0), Max: f(10)})
				row.Stickiness, _ = rb.nestedNum(path, "stickiness", numRule{Min: f(0), Max: f(0.95)})
				rows = append(rows, row)
			}
			p.EarlyCloseCalibration = rows
		}
	}
	note := b.optStr("note", 0, 500)
	if err := b.err(); err != nil {
		return riskconfig.RiskConfigPatch{}, nil, err
	}
	return p, note, nil
}

// nested reads a required key of a nested object with the given check.
func (b *body) nested(parent, key string, check func(path string, raw []byte) (string, bool)) (string, bool) {
	raw, ok := b.get(key)
	if !ok {
		b.v.Add(sub(parent, key), "Required")
		return "", false
	}
	return check(sub(parent, key), raw)
}

func (b *body) nestedNum(parent, key string, rule numRule) (float64, bool) {
	raw, ok := b.get(key)
	if !ok {
		b.v.Add(sub(parent, key), "Required")
		return 0, false
	}
	return checkNum(b.v, sub(parent, key), raw, rule)
}

func putRiskSettings(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	patch, note, err := parseRiskPatch(r)
	if err != nil {
		return err
	}
	before, after, err := riskconfig.UpdateRiskConfig(r.Context(), patch, actor.UserID, note)
	if err != nil {
		return err
	}
	// Awaited: a configuration change must not be acknowledged before its
	// audit row exists.
	e := entry(r, actor, audit.RiskConfigUpdated, "RiskConfig", strconv.FormatInt(after.Version, 10))
	e.Before, e.After = before, after
	audit.Write(r.Context(), e)
	return httpx.OK(w, models.ToRiskConfigLean(after))
}

func listReconciliation(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	if err := q.Err(); err != nil {
		return err
	}
	items, total, err := reconciliation.ListReconciliationRuns(r.Context(), page, limit)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func runReconciliation(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	res, err := reconciliation.RunReconciliation(r.Context(), 0)
	if err != nil {
		return err
	}
	// Who ran a reconciliation, and what it found, belongs in the trail next
	// to the resolutions of its alerts.
	e := entry(r, actor, audit.ReconciliationRun, "ReconciliationRun", res.Reference)
	e.After = map[string]any{"reference": res.Reference, "status": string(res.Status), "alerts": res.Alerts}
	audit.Write(r.Context(), e)
	return httpx.OK(w, res)
}

func resolveReconciliation(w http.ResponseWriter, r *http.Request) error {
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
	note, _ := b.reqStr("note", 3, 1000)
	if err := b.err(); err != nil {
		return err
	}
	if err := reconciliation.ResolveReconciliationRun(r.Context(), id.Hex(), actor.UserID, note); err != nil {
		return err
	}
	e := entry(r, actor, audit.ReconciliationResolved, "ReconciliationRun", chi.URLParam(r, "id"))
	e.After = map[string]any{"note": note}
	// Awaited: closing a ledger discrepancy is acknowledged only once audited.
	audit.Write(r.Context(), e)
	return httpx.OK(w, map[string]bool{"resolved": true})
}
