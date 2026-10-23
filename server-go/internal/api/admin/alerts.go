package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/alerts"
	"binarytrading/internal/service/alerts/monitor"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/exposure"
)

// Operational alerts (service/alerts): the list, an operator's resolution
// (audited before the response) and the monitoring status (exposure book,
// rate limiter, email outbox). Reading needs reconciliation:read and
// resolving reconciliation:resolve, like the reconciliation and USDT alerts.

const auditAlertResolved = "alert.resolved"

func mountAlerts(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
	read := r.With(httpx.RequirePermission(config.PermReconciliationRead))
	read.Get("/", httpx.H(listAlerts))
	read.Get("/status", httpx.H(alertsStatus))
	r.With(httpx.RequirePermission(config.PermReconciliationResolve), httpx.RateLimit(httpx.RateLimits.AdminWrite)).
		Post("/{id}/resolve", httpx.H(resolveAlert))
}

func listAlerts(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	query := alerts.Query{
		Status:   q.OptEnum("status", models.AlertOpen, models.AlertResolved),
		Severity: q.OptEnum("severity", config.AlertSeverities...),
		Source: q.OptEnum("source", models.AlertSourceUsdt, models.AlertSourceHedging, models.AlertSourceReconciliation,
			models.AlertSourceExposure, models.AlertSourceFeed, models.AlertSourceSettlement, models.AlertSourceRateLimiter,
			models.AlertSourceSystem),
		Page: page, Limit: limit,
	}
	if err := q.Err(); err != nil {
		return err
	}
	items, total, err := alerts.List(r.Context(), query)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func resolveAlert(w http.ResponseWriter, r *http.Request) error {
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
	a, err := alerts.Resolve(r.Context(), id, actor.UserID, note)
	if err != nil {
		return err
	}
	view := models.ToAlertView(a)
	e := entry(r, actor, auditAlertResolved, "Alert", id.Hex())
	e.After, e.Reason = auditDoc(view), note
	// Awaited: closing an alert is acknowledged only once audited.
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

type limiterView struct {
	// Mode is "redis" (shared limits) or "memory" (per-instance fallback).
	Mode string `json:"mode"`
	httpx.RateLimiterState
}

// LimiterView is the rate limiter as monitoring shows it.
func limiterStatus() limiterView {
	s := httpx.RateLimiterStatus()
	mode := "redis"
	if s.Degraded {
		mode = "memory"
	}
	return limiterView{Mode: mode, RateLimiterState: s}
}

func alertsStatus(w http.ResponseWriter, r *http.Request) error {
	st, err := alerts.GetStatus(r.Context())
	if err != nil {
		return err
	}
	type bookView struct {
		exposure.BookStatus
		Error *string `json:"error"`
	}
	book := bookView{}
	if b, berr := exposure.GetBookStatus(r.Context()); berr != nil {
		msg := berr.Error()
		book.Status, book.Error = "UNKNOWN", &msg
	} else {
		book.BookStatus = b
	}
	return httpx.OK(w, struct {
		Alerting     alerts.Status `json:"alerting"`
		ExposureBook bookView      `json:"exposureBook"`
		RateLimiter  limiterView   `json:"rateLimiter"`
		// Conditions the monitor on this instance currently sees as bad
		// (whether or not they have lasted long enough to alert).
		Conditions []string `json:"conditions"`
	}{st, book, limiterStatus(), monitor.OpenConditions()})
}
