package usdt

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/beex"
	usdtsvc "binarytrading/internal/service/usdt"
)

// MountAdmin registers the operators' USDT routes. Call it as
// r.Route("/usdt", func(r chi.Router) { r.Group(usdt.MountAdmin) }) inside
// /admin. Permissions are the existing money ones: deposits:read/approve,
// withdrawals:read/approve, settings:write for network switches and
// reconciliation:read/resolve for provider reconciliation. Every action that
// moves money or changes a setting is audited before the response returns.
func MountAdmin(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
	perm := func(p ...string) chi.Router { return r.With(httpx.RequirePermission(p...)) }
	write := func(p ...string) chi.Router {
		return r.With(httpx.RequirePermission(p...), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	}
	anyRead := r.With(requireAnyPermission(config.PermDepositsRead, config.PermWithdrawalsRead))

	perm(config.PermDepositsRead).Get("/summary", httpx.H(adminSummary))
	anyRead.Get("/payout-limits", httpx.H(adminPayoutLimits))

	perm(config.PermDepositsRead).Get("/deposits", httpx.H(adminListDeposits))
	perm(config.PermDepositsRead).Get("/deposits/{id}", httpx.H(adminGetDeposit))
	write(config.PermDepositsRead).Post("/deposits/{id}/refresh", httpx.H(adminRefreshDeposit))
	write(config.PermDepositsApprove).Post("/deposits/{id}/credit", httpx.H(adminCreditDeposit))

	perm(config.PermWithdrawalsRead).Get("/withdrawals", httpx.H(adminListWithdrawals))
	perm(config.PermWithdrawalsRead).Get("/withdrawals/{id}", httpx.H(adminGetWithdrawal))
	write(config.PermWithdrawalsApprove).Post("/withdrawals/{id}/approve", httpx.H(adminApproveWithdrawal))
	write(config.PermWithdrawalsApprove).Post("/withdrawals/{id}/reject", httpx.H(adminRejectWithdrawal))
	write(config.PermWithdrawalsRead).Post("/withdrawals/{id}/refresh", httpx.H(adminRefreshWithdrawal))
	// A withdrawal whose BeeX answer did not match, settled or released by
	// hand after the operator checked it with BeeX.
	write(config.PermWithdrawalsApprove).Post("/withdrawals/{id}/mark-completed", httpx.H(adminResolveWithdrawal(models.WithdrawalResolutionCompleted)))
	write(config.PermWithdrawalsApprove).Post("/withdrawals/{id}/mark-failed", httpx.H(adminResolveWithdrawal(models.WithdrawalResolutionFailed)))

	perm(config.PermDepositsRead).Get("/webhook-events", httpx.H(adminListEvents))
	// The permission depends on what the event moves (checked inside).
	r.With(httpx.RateLimit(httpx.RateLimits.AdminWrite)).Post("/webhook-events/{id}/reprocess", httpx.H(adminReprocessEvent))

	perm(config.PermDepositsRead).Get("/networks", httpx.H(adminListNetworks))
	write(config.PermSettingsWrite).Patch("/networks/{code}", httpx.H(adminPatchNetwork))

	anyRead.Get("/alerts", httpx.H(adminListAlerts))
	write(config.PermReconciliationResolve).Post("/alerts/{id}/resolve", httpx.H(adminResolveAlert))
	write(config.PermReconciliationRead).Post("/reconcile", httpx.H(adminReconcile))
}

// requireAnyPermission passes when the actor holds at least one of them.
func requireAnyPermission(perms ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range perms {
				if httpx.HasPermission(r, p) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.WriteError(w, r, apperr.Forbid("Missing permission: "+strings.Join(perms, " or ")))
		})
	}
}

func entry(r *http.Request, actor *httpx.Actor, action, resource, id string) audit.Entry {
	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action, e.Resource, e.ResourceID = action, resource, id
	return e
}

// ── overview ────────────────────────────────────────────────────────────

func adminSummary(w http.ResponseWriter, r *http.Request) error {
	s, err := usdtsvc.GetSummary(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, s)
}

func adminPayoutLimits(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	refresh := q.OptBool("refresh")
	if err := q.Err(); err != nil {
		return err
	}
	var l usdtsvc.Limits
	var err error
	if refresh != nil && *refresh {
		l, err = usdtsvc.RefreshPayoutLimits(r.Context())
	} else {
		l, err = usdtsvc.GetPayoutLimits(r.Context())
	}
	if err != nil {
		return err
	}
	return httpx.OK(w, l.View())
}

// ── deposits ────────────────────────────────────────────────────────────

// search resolves ?q= (email or username) to customer ids.
func search(ctx context.Context, q *httpx.Q) (bool, []bson.ObjectID, error) {
	text := q.OptStr("q")
	if text == nil || strings.TrimSpace(*text) == "" {
		return false, nil, nil
	}
	s := strings.TrimSpace(*text)
	if len(s) > 100 {
		s = s[:100]
	}
	ids, err := usdtsvc.SearchUserIDs(ctx, s)
	return true, ids, err
}

func adminListDeposits(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	query := usdtsvc.DepositQuery{
		UserID: q.OptObjectID("userId"), Status: q.OptEnum("status", models.InvoiceStatusValues...),
		Network: q.OptStr("network"), Reference: q.OptStr("reference"), OrderID: q.OptStr("orderId"),
		From: q.OptDate("from"), To: q.OptDate("to"), Page: page, Limit: limit,
	}
	if err := q.Err(); err != nil {
		return err
	}
	var err error
	if query.Search, query.UserIDs, err = search(r.Context(), q); err != nil {
		return err
	}
	rows, total, err := usdtsvc.ListDeposits(r.Context(), query)
	if err != nil {
		return err
	}
	items, err := usdtsvc.DepositAdminViews(r.Context(), rows)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func adminGetDeposit(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	d, err := usdtsvc.GetDepositDetail(r.Context(), id)
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}

type depositRefresh struct {
	Changed  bool                         `json:"changed"`
	Credited bool                         `json:"credited"`
	Note     string                       `json:"note"`
	Deposit  *models.UsdtDepositAdminView `json:"deposit"`
}

func adminRefreshDeposit(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	if err := enabledForOperators(); err != nil {
		return err
	}
	res, d, err := usdtsvc.RefreshDeposit(r.Context(), id, "admin")
	if err != nil {
		return err
	}
	views, err := usdtsvc.DepositAdminViews(r.Context(), []models.UsdtDeposit{*d})
	if err != nil {
		return err
	}
	out := depositRefresh{Changed: res.Changed, Credited: res.Credited, Note: res.Note, Deposit: &views[0]}
	if res.Changed {
		// Applying BeeX's answer may credit: who asked is on record.
		e := entry(r, actor, usdtsvc.AuditDepositRefreshed, "UsdtDeposit", id.Hex())
		e.After = auditDoc(out)
		audit.Write(r.Context(), e)
	}
	return httpx.OK(w, out)
}

func adminCreditDeposit(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	note := f.str("note", 3, 500, true)
	if err := f.err(); err != nil {
		return err
	}
	if err := enabledForOperators(); err != nil {
		return err
	}
	before, err := usdtsvc.GetDeposit(r.Context(), id)
	if err != nil {
		return err
	}
	d, amount, err := usdtsvc.AdminCreditDeposit(r.Context(), id, actor.UserID, *note)
	if err != nil {
		return err
	}
	views, err := usdtsvc.DepositAdminViews(r.Context(), []models.UsdtDeposit{*d})
	if err != nil {
		return err
	}
	e := entry(r, actor, usdtsvc.AuditDepositManualCredit, "UsdtDeposit", id.Hex())
	e.Before = auditDoc(models.ToUsdtDepositView(before))
	e.After, e.Reason = auditDoc(map[string]any{"deposit": views[0], "amount": amount}), *note
	audit.Write(r.Context(), e)
	return httpx.OK(w, map[string]any{"deposit": views[0], "amount": amount})
}

// ── withdrawals ─────────────────────────────────────────────────────────

func adminListWithdrawals(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	query := usdtsvc.WithdrawalQuery{
		UserID: q.OptObjectID("userId"), Status: q.OptEnum("status", models.WithdrawalStatusValues...),
		Network: q.OptStr("network"), Reference: q.OptStr("reference"), Address: q.OptStr("address"),
		From: q.OptDate("from"), To: q.OptDate("to"), Page: page, Limit: limit,
	}
	if err := q.Err(); err != nil {
		return err
	}
	var err error
	if query.Search, query.UserIDs, err = search(r.Context(), q); err != nil {
		return err
	}
	rows, total, err := usdtsvc.ListWithdrawals(r.Context(), query)
	if err != nil {
		return err
	}
	items, err := usdtsvc.WithdrawalAdminViews(r.Context(), rows)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func adminGetWithdrawal(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	d, err := usdtsvc.GetWithdrawalDetail(r.Context(), id)
	if err != nil {
		return err
	}
	return httpx.OK(w, d)
}

func withdrawalAdminView(ctx context.Context, wd *models.UsdtWithdrawal) (models.UsdtWithdrawalAdminView, error) {
	views, err := usdtsvc.WithdrawalAdminViews(ctx, []models.UsdtWithdrawal{*wd})
	if err != nil {
		return models.UsdtWithdrawalAdminView{}, err
	}
	return views[0], nil
}

func adminApproveWithdrawal(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	note := f.str("note", 0, 500, false)
	if err := f.err(); err != nil {
		return err
	}
	if note != nil && *note == "" {
		note = nil
	}
	before, err := usdtsvc.GetWithdrawal(r.Context(), id)
	if err != nil {
		return err
	}
	wd, err := usdtsvc.ApproveWithdrawal(r.Context(), id, actor.UserID, note)
	if err != nil {
		return err
	}
	view, err := withdrawalAdminView(r.Context(), wd)
	if err != nil {
		return err
	}
	e := entry(r, actor, usdtsvc.AuditWithdrawalApproved, "UsdtWithdrawal", id.Hex())
	e.Before, e.After = auditDoc(models.ToUsdtWithdrawalView(before)), auditDoc(view)
	if note != nil {
		e.Reason = *note
	}
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

func adminRejectWithdrawal(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	note := f.str("note", 3, 500, true)
	if err := f.err(); err != nil {
		return err
	}
	before, err := usdtsvc.GetWithdrawal(r.Context(), id)
	if err != nil {
		return err
	}
	wd, err := usdtsvc.RejectWithdrawal(r.Context(), id, actor.UserID, *note)
	if err != nil {
		return err
	}
	view, err := withdrawalAdminView(r.Context(), wd)
	if err != nil {
		return err
	}
	e := entry(r, actor, usdtsvc.AuditWithdrawalRejected, "UsdtWithdrawal", id.Hex())
	e.Before, e.After, e.Reason = auditDoc(models.ToUsdtWithdrawalView(before)), auditDoc(view), *note
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

// adminResolveWithdrawal settles (completed) or releases (failed) a
// withdrawal whose BeeX answer did not match. Body {note (3-500, required),
// txHash? (completed only)}. Audited before the response; a repeat of an
// applied resolution returns the withdrawal with Idempotent-Replayed: true and
// writes no second audit row.
func adminResolveWithdrawal(outcome string) func(http.ResponseWriter, *http.Request) error {
	action := usdtsvc.AuditWithdrawalResolvedCompleted
	if outcome == models.WithdrawalResolutionFailed {
		action = usdtsvc.AuditWithdrawalResolvedFailed
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		actor, err := httpx.RequireActor(r)
		if err != nil {
			return err
		}
		id, err := httpx.IDParam(r, "id")
		if err != nil {
			return err
		}
		f, err := readFields(r)
		if err != nil {
			return err
		}
		note := f.str("note", 3, 500, true)
		var txHash *string
		if outcome == models.WithdrawalResolutionCompleted {
			txHash = f.str("txHash", 0, 200, false)
		}
		if err := f.err(); err != nil {
			return err
		}
		before, err := usdtsvc.GetWithdrawal(r.Context(), id)
		if err != nil {
			return err
		}
		in := usdtsvc.ResolveInput{ID: id, AdminID: actor.UserID, Outcome: outcome, Note: *note}
		if txHash != nil {
			in.TxHash = *txHash
		}
		wd, changed, err := usdtsvc.ResolveWithdrawal(r.Context(), in)
		if err != nil {
			return err
		}
		view, err := withdrawalAdminView(r.Context(), wd)
		if err != nil {
			return err
		}
		if !changed {
			w.Header().Set("Idempotent-Replayed", "true")
			return httpx.OK(w, view)
		}
		e := entry(r, actor, action, "UsdtWithdrawal", id.Hex())
		e.Before, e.After, e.Reason = auditDoc(models.ToUsdtWithdrawalAdminView(before, nil)), auditDoc(view), *note
		audit.Write(r.Context(), e)
		return httpx.OK(w, view)
	}
}

type withdrawalRefresh struct {
	Changed    bool                            `json:"changed"`
	Note       string                          `json:"note"`
	Withdrawal *models.UsdtWithdrawalAdminView `json:"withdrawal"`
}

func adminRefreshWithdrawal(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	if err := enabledForOperators(); err != nil {
		return err
	}
	res, wd, err := usdtsvc.RefreshWithdrawal(r.Context(), id, "admin")
	if err != nil {
		return err
	}
	view, err := withdrawalAdminView(r.Context(), wd)
	if err != nil {
		return err
	}
	out := withdrawalRefresh{Changed: res.Changed, Note: res.Note, Withdrawal: &view}
	if res.Changed {
		// Applying BeeX's answer may settle or release the funds.
		e := entry(r, actor, usdtsvc.AuditWithdrawalRefreshed, "UsdtWithdrawal", id.Hex())
		e.After = auditDoc(out)
		audit.Write(r.Context(), e)
	}
	return httpx.OK(w, out)
}

// ── webhook events ──────────────────────────────────────────────────────

func adminListEvents(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	query := usdtsvc.EventQuery{
		Event: q.OptStr("event"), Reference: q.OptStr("reference"),
		ProcessingStatus: q.OptEnum("processingStatus", models.WebhookProcessingStatusValues...),
		DepositID:        q.OptObjectID("depositId"), WithdrawalID: q.OptObjectID("withdrawalId"),
		Page: page, Limit: limit,
	}
	if err := q.Err(); err != nil {
		return err
	}
	rows, total, err := usdtsvc.ListEvents(r.Context(), query)
	if err != nil {
		return err
	}
	items := make([]models.WebhookEventView, 0, len(rows))
	for i := range rows {
		items = append(items, models.ToWebhookEventView(&rows[i]))
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func adminReprocessEvent(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	ev, err := usdtsvc.GetEvent(r.Context(), id)
	if err != nil {
		return err
	}
	// Reprocessing an invoice event may credit; a payout event may settle
	// or release. Anything else needs both.
	need := []string{config.PermDepositsApprove, config.PermWithdrawalsApprove}
	switch beex.EventKind(ev.Event) {
	case "invoice":
		need = need[:1]
	case "payout":
		need = need[1:]
	}
	for _, p := range need {
		if !httpx.HasPermission(r, p) {
			return apperr.Forbid("Missing permission: " + p)
		}
	}
	after, err := usdtsvc.ReprocessEvent(r.Context(), id)
	if err != nil {
		return err
	}
	view := models.ToWebhookEventView(after)
	e := entry(r, actor, usdtsvc.AuditWebhookReprocessed, "BeexWebhookEvent", id.Hex())
	e.Before = map[string]any{"processingStatus": ev.ProcessingStatus, "attempts": ev.Attempts}
	e.After = map[string]any{"processingStatus": after.ProcessingStatus, "attempts": after.Attempts, "error": after.Error}
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

// ── networks ────────────────────────────────────────────────────────────

func adminListNetworks(w http.ResponseWriter, r *http.Request) error {
	views, err := usdtsvc.NetworkViews(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, views)
}

func adminPatchNetwork(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	code := usdtsvc.NormNetwork(chi.URLParam(r, "code"))
	var body struct {
		DepositEnabled  *bool            `json:"depositEnabled"`
		WithdrawEnabled *bool            `json:"withdrawEnabled"`
		MinDeposit      *json.RawMessage `json:"minDeposit"`
	}
	if err := httpx.DecodeBody(r, &body); err != nil {
		return err
	}
	patch := usdtsvc.NetworkPatch{DepositEnabled: body.DepositEnabled, WithdrawEnabled: body.WithdrawEnabled}
	v := httpx.NewV()
	if body.MinDeposit != nil {
		f := &fields{raw: map[string]json.RawMessage{"minDeposit": *body.MinDeposit}, v: v}
		n := f.micro("minDeposit")
		v.Int("minDeposit", n, 0, maxMicro)
		patch.MinDeposit = &n
	}
	if patch.DepositEnabled == nil && patch.WithdrawEnabled == nil && patch.MinDeposit == nil {
		v.Add("", "Nothing to change: send depositEnabled, withdrawEnabled or minDeposit")
	}
	if err := v.Err("request body"); err != nil {
		return err
	}
	before, after, err := usdtsvc.UpdateNetwork(r.Context(), code, patch)
	if err != nil {
		return err
	}
	view := models.ToUsdtNetworkView(&after)
	e := entry(r, actor, usdtsvc.AuditNetworkUpdated, "UsdtNetwork", code)
	e.Before, e.After = auditDoc(models.ToUsdtNetworkView(&before)), auditDoc(view)
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

// ── alerts and reconciliation ───────────────────────────────────────────

func adminListAlerts(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	query := usdtsvc.AlertQuery{
		Status:   q.OptEnum("status", models.AlertOpen, models.AlertResolved),
		Severity: q.OptEnum("severity", models.AlertWarning, models.AlertCritical),
		Kind: q.OptEnum("kind", models.AlertKindDeposit, models.AlertKindWithdrawal, models.AlertKindWebhook,
			models.AlertKindProvider),
		Code: q.OptStr("code"), DepositID: q.OptObjectID("depositId"), WithdrawalID: q.OptObjectID("withdrawalId"),
		Page: page, Limit: limit,
	}
	if err := q.Err(); err != nil {
		return err
	}
	items, total, err := usdtsvc.ListAlerts(r.Context(), query)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

func adminResolveAlert(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	f, err := readFields(r)
	if err != nil {
		return err
	}
	note := f.str("note", 3, 500, true)
	if err := f.err(); err != nil {
		return err
	}
	a, err := usdtsvc.ResolveAlert(r.Context(), id, actor.UserID, *note)
	if err != nil {
		return err
	}
	view := models.ToUsdtAlertView(a)
	e := entry(r, actor, usdtsvc.AuditAlertResolved, "UsdtAlert", id.Hex())
	e.After, e.Reason = auditDoc(view), *note
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

func adminReconcile(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	if err := enabledForOperators(); err != nil {
		return err
	}
	res, err := usdtsvc.ReconcileProvider(r.Context(), time.Now())
	if err != nil {
		return err
	}
	e := entry(r, actor, usdtsvc.AuditReconciliationRun, "UsdtReconciliation", "")
	e.After = auditDoc(res)
	audit.Log(e)
	return httpx.OK(w, res)
}

// enabledForOperators: provider-facing operator actions need USDT on.
func enabledForOperators() error {
	if !usdtsvc.Enabled() {
		return apperr.ConflictErr(apperr.UsdtDisabled, "USDT payments are disabled (USDT_ENABLED=false)")
	}
	return nil
}
