// Package transfers is the customer side of deposits and withdrawals, mounted
// at /transfers.
//
// A deposit is a claim: the customer has already paid outside the platform and
// submits the UTR and/or a screenshot as evidence. Nothing is credited until
// an operator with deposits:approve verifies it.
package transfers

import (
	"io"
	"mime"
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	svc "binarytrading/internal/service/transfers"
)

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	read := r.With(httpx.RateLimit(httpx.RateLimits.Read))
	write := r.With(httpx.RequireActiveUser, httpx.RateLimit(httpx.RateLimits.Transfer))

	// Where to pay. Without this the deposit screen cannot tell the customer anything.
	read.Get("/payment-methods", httpx.H(paymentMethods))
	write.Post("/deposits", httpx.H(createDeposit))
	write.Post("/withdrawals", httpx.H(createWithdrawal))
	read.Get("/", httpx.H(list))
	read.Get("/{id}/proof", httpx.H(proof))
}

func paymentMethods(w http.ResponseWriter, r *http.Request) error {
	methods, err := svc.ListPaymentMethods(r.Context(), true)
	if err != nil {
		return err
	}
	return httpx.OK(w, methods)
}

// ── deposits ────────────────────────────────────────────────────────────

type depositBody struct {
	Amount           *float64 `json:"amount"`
	PaymentMethodKey *string  `json:"paymentMethodKey"`
	Utr              *string  `json:"utr"`
	CustomerNote     *string  `json:"customerNote"`
}

// amountNaN marks a multipart amount that Number() could not parse.
type depositFields struct {
	depositBody
	amountNaN bool
}

func validateDeposit(v *httpx.V, d depositFields) {
	switch {
	case d.amountNaN:
		v.Add("amount", "Expected number, received nan")
	case d.Amount == nil:
		v.Required("amount", false)
	default:
		minorAmount(v, "amount", *d.Amount)
	}
	optStr(v, "paymentMethodKey", d.PaymentMethodKey, 2, 40)
	optStr(v, "utr", d.Utr, 6, 40)
	optStr(v, "customerNote", d.CustomerNote, 0, 500)
}

func createDeposit(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}

	var fields depositFields
	var proofFile *svc.ProofFile
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt == "multipart/form-data" {
		fields, proofFile, err = readMultipartDeposit(w, r)
		if err != nil {
			return err
		}
		v := httpx.NewV()
		validateDeposit(v, fields)
		if err := v.Err("deposit request"); err != nil {
			return err
		}
	} else {
		if err := httpx.DecodeBody(r, &fields.depositBody); err != nil {
			return err
		}
		v := httpx.NewV()
		validateDeposit(v, fields)
		if err := v.Err("request body"); err != nil {
			return err
		}
	}

	transfer, err := svc.RequestDeposit(r.Context(), svc.DepositRequestInput{
		UserID:           actor.UserID,
		Amount:           int64(*fields.Amount),
		PaymentMethodKey: fields.PaymentMethodKey,
		Utr:              fields.Utr,
		CustomerNote:     fields.CustomerNote,
		ProofFile:        proofFile,
	})
	if err != nil {
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.DepositCreated
	e.Resource = "Transfer"
	e.ResourceID = transfer.ID
	e.After = map[string]any{
		"reference": transfer.Reference,
		"amount":    transfer.Amount,
		"method":    transfer.Method,
		"utr":       transfer.Utr,
		"hasProof":  transfer.Proof != nil,
	}
	audit.Log(e)

	return httpx.Created(w, transfer)
}

// ── withdrawals ─────────────────────────────────────────────────────────

type destinationBody struct {
	Type          *string `json:"type"`
	UpiID         *string `json:"upiId"`
	AccountName   *string `json:"accountName"`
	AccountNumber *string `json:"accountNumber"`
	Ifsc          *string `json:"ifsc"`
	BankName      *string `json:"bankName"`
}

type withdrawalBody struct {
	Amount       *float64         `json:"amount"`
	CustomerNote *string          `json:"customerNote"`
	Destination  *destinationBody `json:"destination"`
}

func createWithdrawal(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	var body withdrawalBody
	if err := httpx.DecodeBody(r, &body); err != nil {
		return err
	}

	v := httpx.NewV()
	if v.Required("amount", body.Amount != nil) {
		minorAmount(v, "amount", *body.Amount)
	}
	optStr(v, "customerNote", body.CustomerNote, 0, 500)
	var dest models.PayoutDestination
	if v.Required("destination", body.Destination != nil) {
		d := body.Destination
		switch {
		case d.Type != nil && *d.Type == "UPI":
			dest.Type = models.PaymentMethodUPI
			if v.Required("destination.upiId", d.UpiID != nil) {
				str(v, "destination.upiId", *d.UpiID, 3, 80)
			}
			dest.UpiID = d.UpiID
		case d.Type != nil && *d.Type == "BANK":
			dest.Type = models.PaymentMethodBank
			if v.Required("destination.accountName", d.AccountName != nil) {
				str(v, "destination.accountName", *d.AccountName, 2, 120)
			}
			if v.Required("destination.accountNumber", d.AccountNumber != nil) {
				str(v, "destination.accountNumber", *d.AccountNumber, 6, 20)
			}
			if v.Required("destination.ifsc", d.Ifsc != nil) {
				str(v, "destination.ifsc", *d.Ifsc, 11, 11)
			}
			optStr(v, "destination.bankName", d.BankName, 0, 120)
			dest.AccountName, dest.AccountNumber, dest.Ifsc, dest.BankName = d.AccountName, d.AccountNumber, d.Ifsc, d.BankName
		default:
			v.Add("destination.type", "Invalid discriminator value. Expected 'UPI' | 'BANK'")
		}
	}
	if err := v.Err("request body"); err != nil {
		return err
	}

	transfer, err := svc.RequestWithdrawal(r.Context(), svc.WithdrawalRequestInput{
		UserID:       actor.UserID,
		Amount:       int64(*body.Amount),
		Destination:  dest,
		CustomerNote: body.CustomerNote,
	})
	if err != nil {
		return err
	}

	var destinationType any
	if transfer.Destination != nil {
		destinationType = transfer.Destination.Type
	}
	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.WithdrawalCreated
	e.Resource = "Transfer"
	e.ResourceID = transfer.ID
	e.After = map[string]any{
		"reference":       transfer.Reference,
		"amount":          transfer.Amount,
		"destinationType": destinationType,
	}
	audit.Log(e)

	return httpx.Created(w, transfer)
}

// ── listing & proof ─────────────────────────────────────────────────────

func list(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	kind := q.OptEnum("kind", models.TransferKindValues...)
	status := q.OptEnum("status", models.TransferStatusValues...)
	if err := q.Err(); err != nil {
		return err
	}
	query := svc.TransferQuery{UserID: actor.UserID, Page: page, Limit: limit}
	if kind != nil {
		k := models.TransferKind(*kind)
		query.Kind = &k
	}
	if status != nil {
		s := models.TransferStatus(*status)
		query.Status = &s
	}
	items, total, err := svc.ListTransfers(r.Context(), query)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, page, limit, total))
}

// proof streams a payment screenshot. A screenshot shows a customer's bank
// details, so it is readable only by its owner or by an operator entitled to
// review deposits.
func proof(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	transfer, err := svc.GetTransfer(r.Context(), id.Hex())
	if err != nil {
		return err
	}

	isOwner := transfer.UserID.Hex() == actor.UserID
	isReviewer := config.HasPermission(actor.Role, actor.Permissions, config.PermDepositsRead) ||
		config.HasPermission(actor.Role, actor.Permissions, config.PermWithdrawalsRead)
	// Not 403: confirming the transfer exists would itself leak information.
	if !isOwner && !isReviewer {
		return apperr.NotFoundErr(apperr.NotFound, "Transfer not found")
	}
	if transfer.Proof == nil {
		return apperr.NotFoundErr(apperr.NotFound, "No attachment on this transfer")
	}

	f, err := svc.OpenAttachment(transfer.Proof.FileID)
	if err != nil {
		return err
	}
	defer f.Close()
	h := w.Header()
	h.Set("Content-Type", transfer.Proof.MimeType)
	h.Set("Content-Disposition", `inline; filename="`+transfer.Proof.OriginalName+`"`)
	h.Set("Cache-Control", "private, max-age=60")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
	return nil
}
