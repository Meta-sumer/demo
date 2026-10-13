package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/transfers"
	"binarytrading/internal/service/wallets"
)

var transferStatuses = []string{"PENDING", "APPROVED", "REJECTED", "FAILED", "COMPLETED"}

// mountFinance is adminFinanceRoutes (spec section 19): /admin/wallets,
// /admin/deposits, /admin/withdrawals.
func mountFinance(r chi.Router) {
	perm := func(p ...string) chi.Router { return r.With(httpx.RequirePermission(p...)) }
	write := func(p ...string) chi.Router {
		return r.With(httpx.RequirePermission(p...), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	}

	perm(config.PermWalletsRead).Get("/wallets", httpx.H(listWallets))
	perm(config.PermDepositsRead).Get("/deposits", httpx.H(listTransfersOf(models.TransferKind("DEPOSIT"))))
	perm(config.PermWithdrawalsRead).Get("/withdrawals", httpx.H(listTransfersOf(models.TransferKind("WITHDRAWAL"))))
	write(config.PermDepositsApprove).Post("/deposits/{id}/approve", httpx.H(approveDeposit))
	write(config.PermWithdrawalsApprove).Post("/withdrawals/{id}/approve", httpx.H(approveWithdrawal))
	write(config.PermDepositsApprove, config.PermWithdrawalsApprove).Post("/transfers/{id}/reject", httpx.H(rejectTransfer))
}

func listWallets(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	page, limit := q.Page()
	account := ""
	if s := q.OptEnum("accountType", models.AccountTypeValues...); s != nil {
		account = *s
	}
	if err := q.Err(); err != nil {
		return err
	}
	res, err := wallets.ListWalletsForAdmin(r.Context(), account, page, limit)
	if err != nil {
		return err
	}
	return httpx.Send(w, http.StatusOK, httpx.Paginate(res.Items, page, limit, res.Total), map[string]any{
		"totalBalance": res.TotalBalance,
		"totalLocked":  res.TotalLocked,
	})
}

func listTransfersOf(kind models.TransferKind) httpx.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		q := httpx.Query(r)
		page, limit := q.Page()
		status := q.OptEnum("status", transferStatuses...)
		userID := q.OptStr("userId")
		utr := optQueryStr(q, "utr", 4, 40)
		if err := q.Err(); err != nil {
			return err
		}
		query := transfers.TransferQuery{Kind: &kind, Utr: utr, Page: page, Limit: limit}
		if status != nil {
			s := models.TransferStatus(*status)
			query.Status = &s
		}
		if userID != nil {
			query.UserID = *userID
		}
		items, total, err := transfers.ListTransfers(r.Context(), query)
		if err != nil {
			return err
		}
		return httpx.OK(w, httpx.Paginate(items, page, limit, total))
	}
}

func approveDeposit(w http.ResponseWriter, r *http.Request) error {
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
	note := b.optStr("note", 0, 500)
	if err := b.err(); err != nil {
		return err
	}
	transfer, err := transfers.ApproveDeposit(r.Context(), id.Hex(), actor.UserID, note)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.DepositApproved, "Transfer", chi.URLParam(r, "id"))
	e.After = auditDoc(transfer)
	audit.Write(r.Context(), e)
	return httpx.OK(w, transfer)
}

// Approving a withdrawal requires the reference of the payment the operator
// actually made, so "did we pay this?" is answerable months later.
func approveWithdrawal(w http.ResponseWriter, r *http.Request) error {
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
	utr, _ := b.reqStr("utr", 6, 40)
	note := b.optStr("note", 0, 500)
	if err := b.err(); err != nil {
		return err
	}
	transfer, err := transfers.ApproveWithdrawal(r.Context(), id.Hex(), actor.UserID, transfers.WithdrawalApproval{Utr: utr, Note: note})
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.WithdrawalApproved, "Transfer", chi.URLParam(r, "id"))
	e.After = auditDoc(transfer)
	audit.Write(r.Context(), e)
	return httpx.OK(w, transfer)
}

func rejectTransfer(w http.ResponseWriter, r *http.Request) error {
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
	note, _ := b.reqStr("note", 3, 500)
	if err := b.err(); err != nil {
		return err
	}
	transfer, err := transfers.RejectTransfer(r.Context(), id.Hex(), actor.UserID, note)
	if err != nil {
		return err
	}
	action := audit.WithdrawalRejected
	if transfer.Kind == "DEPOSIT" {
		action = audit.DepositRejected
	}
	e := entry(r, actor, action, "Transfer", chi.URLParam(r, "id"))
	e.After, e.Reason = auditDoc(transfer), note
	audit.Write(r.Context(), e)
	return httpx.OK(w, transfer)
}

// ── payment methods ─────────────────────────────────────────────────────

// mountPaymentMethods is adminPaymentMethodRoutes. These are the account
// details a customer will actually send money to, so changing one is audited
// before the response returns.
func mountPaymentMethods(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
	r.With(httpx.RequirePermission(config.PermDepositsRead)).Get("/", httpx.H(listPaymentMethods))
	write := r.With(httpx.RequirePermission(config.PermSettingsWrite), httpx.RateLimit(httpx.RateLimits.AdminWrite))
	write.Put("/", httpx.H(putPaymentMethod))
	write.Post("/{key}/active", httpx.H(setPaymentMethodActive))
}

func listPaymentMethods(w http.ResponseWriter, r *http.Request) error {
	list, err := transfers.ListPaymentMethods(r.Context(), false)
	if err != nil {
		return err
	}
	return httpx.OK(w, nonNil(list))
}

func putPaymentMethod(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	var in transfers.PaymentMethodInput
	in.Key, _ = b.reqStr("key", 2, 40)
	in.Label, _ = b.reqStr("label", 2, 60)
	typ, _ := b.reqEnum("type", "UPI", "BANK")
	in.Type = models.PaymentMethodType(typ)
	in.UpiID = b.optStr("upiId", 0, 80)
	in.AccountName = b.optStr("accountName", 0, 120)
	in.AccountNumber = b.optStr("accountNumber", 0, 20)
	in.Ifsc = b.optStr("ifsc", 0, 11)
	in.BankName = b.optStr("bankName", 0, 120)
	in.Instructions = b.optStr("instructions", 0, 1000)
	in.MinAmount = b.optInt("minAmount", numRule{Positive: true})
	in.MaxAmount = b.optInt("maxAmount", numRule{Positive: true})
	in.Active = b.optBool("active")
	in.DisplayOrder = b.optInt("displayOrder", numRule{})
	if err := b.err(); err != nil {
		return err
	}
	res, err := transfers.UpsertPaymentMethod(r.Context(), in, actor.UserID)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.PaymentMethodUpdated, "PaymentMethod", res.After.Key)
	if res.Before != nil {
		e.Before = auditDoc(*res.Before)
	}
	e.After = auditDoc(res.After)
	audit.Write(r.Context(), e)
	return httpx.OK(w, res.After)
}

func setPaymentMethodActive(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	key := chi.URLParam(r, "key")
	pv := httpx.NewV()
	pv.Str("key", key, 2, 40)
	if err := pv.Err("path parameters"); err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	active, _ := b.reqBool("active")
	if err := b.err(); err != nil {
		return err
	}
	after, err := transfers.SetPaymentMethodActive(r.Context(), key, active)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.PaymentMethodUpdated, "PaymentMethod", key)
	e.After = map[string]any{"active": active}
	audit.Write(r.Context(), e)
	return httpx.OK(w, after)
}
