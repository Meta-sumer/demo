package usdt

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
	"binarytrading/internal/service/wallets"
)

// Deposits. A deposit is one BeeX invoice: the customer pays the invoice's
// address, BeeX reports the payment (webhook first, polling as a fallback),
// and the FUNDING wallet is credited exactly once when - and only when -
// BeeX reports paid or overpaid.

func depositsColl() *mongo.Collection { return mongox.C(models.CollUsdtDeposits) }

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func depositNotFound() error {
	return apperr.NotFoundErr(apperr.TransactionNotFound, "Deposit not found")
}

func loadDeposit(ctx context.Context, id bson.ObjectID) (*models.UsdtDeposit, error) {
	var d models.UsdtDeposit
	if err := depositsColl().FindOne(ctx, bson.M{"_id": id}).Decode(&d); err != nil {
		if mongox.IsNotFound(err) {
			return nil, depositNotFound()
		}
		return nil, err
	}
	return &d, nil
}

// GetDeposit returns a deposit (admin; any user).
func GetDeposit(ctx context.Context, id bson.ObjectID) (*models.UsdtDeposit, error) {
	return loadDeposit(ctx, id)
}

// GetUserDeposit returns the customer's own deposit; another customer's is
// "not found", never "forbidden".
func GetUserDeposit(ctx context.Context, userID string, id bson.ObjectID) (*models.UsdtDeposit, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return nil, depositNotFound()
	}
	var d models.UsdtDeposit
	if err := depositsColl().FindOne(ctx, bson.M{"_id": id, "userId": uid}).Decode(&d); err != nil {
		if mongox.IsNotFound(err) {
			return nil, depositNotFound()
		}
		return nil, err
	}
	return &d, nil
}

// DepositRequest is POST /wallet/usdt/deposit.
type DepositRequest struct {
	UserID         string
	Network        string
	Amount         int64
	IdempotencyKey string
}

// CreateDeposit opens a BeeX invoice for the amount. The same idempotency
// key returns the same deposit (and so the same invoice and address).
func CreateDeposit(ctx context.Context, in DepositRequest) (*models.UsdtDeposit, error) {
	if err := requireEnabled(); err != nil {
		return nil, err
	}
	uid, ok := mongox.ParseID(in.UserID)
	if !ok {
		return nil, apperr.Unauth()
	}
	ctx = context.WithoutCancel(ctx)
	network, err := GetNetwork(ctx, in.Network)
	if err != nil {
		return nil, err
	}
	if !network.DepositEnabled {
		return nil, apperr.New(apperr.InvalidNetwork, "Deposits on "+network.Label+" are paused. Choose another network.", 400)
	}
	if in.Amount <= 0 {
		return nil, apperr.New(apperr.InvalidAmount, "Deposit amount must be positive", 400)
	}
	if in.Amount < network.MinDeposit {
		return nil, apperr.New(apperr.InvalidAmount,
			"The minimum deposit on "+network.Label+" is "+micro.Format(network.MinDeposit)+" USDT.", 400)
	}
	hash := requestHash("deposit", network.Code, itoa(in.Amount))

	existing, err := depositByKey(ctx, uid, in.IdempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if existing.RequestHash != hash {
			return nil, payloadMismatch()
		}
		return resumeDeposit(ctx, existing)
	}

	if _, err := fundingWallet(ctx, in.UserID); err != nil {
		return nil, err
	}
	id := bson.NewObjectID()
	now := mongox.Now()
	d := models.UsdtDeposit{
		ID: id, UserID: uid, OrderID: "dep-" + id.Hex(), IdempotencyKey: in.IdempotencyKey, RequestHash: hash,
		Network: network.Code, Status: models.InvoiceCreated, AmountExpected: in.Amount,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := depositsColl().InsertOne(ctx, d); err != nil {
		if !mongox.IsDuplicateKey(err) {
			return nil, err
		}
		// A concurrent request with the same key won the insert.
		existing, ferr := depositByKey(ctx, uid, in.IdempotencyKey)
		if ferr != nil || existing == nil {
			return nil, err
		}
		if existing.RequestHash != hash {
			return nil, payloadMismatch()
		}
		return resumeDeposit(ctx, existing)
	}
	return ensureInvoice(ctx, &d)
}

func depositByKey(ctx context.Context, uid bson.ObjectID, key string) (*models.UsdtDeposit, error) {
	var d models.UsdtDeposit
	err := depositsColl().FindOne(ctx, bson.M{"userId": uid, "idempotencyKey": key}).Decode(&d)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

// resumeDeposit: a replay of a request whose invoice was never confirmed
// tries again with the same orderId - BeeX returns the existing invoice, so
// this can never open a second one.
func resumeDeposit(ctx context.Context, d *models.UsdtDeposit) (*models.UsdtDeposit, error) {
	if d.InvoiceID == nil && d.Status == models.InvoiceCreated {
		return ensureInvoice(ctx, d)
	}
	return d, nil
}

func ensureInvoice(ctx context.Context, d *models.UsdtDeposit) (*models.UsdtDeposit, error) {
	p, err := provider()
	if err != nil {
		return nil, err
	}
	inv, err := p.CreateInvoice(ctx, beex.CreateInvoiceRequest{
		OrderID: d.OrderID,
		// An exact decimal written as a JSON number - never via float64.
		Amount:      json.Number(micro.Format(d.AmountExpected)),
		PayCurrency: d.Network,
		Description: "USDT deposit " + d.OrderID,
		SuccessURL:  successURL(d.ID.Hex()),
		CancelURL:   cancelURL(),
	})
	if err != nil {
		set := bson.M{"providerError": providerRaw(err), "updatedAt": mongox.Now()}
		if beex.IsDefinite(err) && !beex.IsConfigError(err) {
			// BeeX refused the order: no invoice exists for it.
			set["status"] = models.InvoiceFailed
			set["failureReason"] = beex.SafeMessage(err)
			logx.Warn("BEEX_PAYMENT_FAILED", "depositId", d.ID.Hex(), "orderId", d.OrderID, "stage", "create_invoice",
				"err", err.Error())
		} else {
			// Unknown outcome: the invoice may exist. A replay of the request or
			// the reconciliation sweep asks BeeX by orderId.
			logx.Warn("BeeX invoice creation did not complete", "depositId", d.ID.Hex(), "orderId", d.OrderID,
				"err", err.Error())
		}
		if _, uerr := depositsColl().UpdateOne(ctx, bson.M{"_id": d.ID, "invoiceId": nil, "status": models.InvoiceCreated},
			bson.M{"$set": set}); uerr != nil {
			logx.Error("could not record the invoice failure", "depositId", d.ID.Hex(), "err", uerr.Error())
		}
		if set["status"] == models.InvoiceFailed {
			emitUpdated(d.UserID.Hex(), KindDeposit, d.ID.Hex(), models.InvoiceFailed)
		}
		return nil, beex.ToAppError(err)
	}
	if err := adoptInvoice(ctx, d, inv); err != nil {
		return nil, err
	}
	if _, err := applyInvoice(ctx, d.ID, inv, "create"); err != nil {
		if _, ok := AsIgnored(err); !ok {
			return nil, err
		}
	}
	return loadDeposit(ctx, d.ID)
}

// adoptInvoice stores the invoice's identity (id, reference, address,
// expiry) on a deposit that does not have it yet.
func adoptInvoice(ctx context.Context, d *models.UsdtDeposit, inv *beex.Invoice) error {
	if inv.ID == "" && inv.Reference == "" {
		return apperr.New(apperr.ProviderError, "The payment provider returned an incomplete invoice. Please try again later.", 502)
	}
	set := bson.M{"updatedAt": mongox.Now(), "providerError": nil}
	if inv.ID != "" {
		set["invoiceId"] = inv.ID
		set["paymentUrl"] = paymentURL(inv.ID)
	} else {
		set["invoiceId"] = inv.Reference
		set["paymentUrl"] = paymentURL(inv.Reference)
	}
	if inv.Reference != "" {
		set["reference"] = inv.Reference
	}
	if inv.Mode != "" {
		set["mode"] = inv.Mode
		if err := ModeAccepted(inv.Mode); err != nil {
			logx.Warn("BeeX invoice will not be credited in this mode", "depositId", d.ID.Hex(), "mode", inv.Mode, "reason", err.Error())
		}
	}
	if inv.PayAddress != "" {
		set["payAddress"] = inv.PayAddress
	}
	if inv.ExpiresAt.Set {
		set["expiresAt"] = inv.ExpiresAt.Ptr()
	}
	if v, _, err := inv.PayAmount.Micro(); err == nil && inv.PayAmount.Set && v > 0 && v != d.AmountExpected {
		// The customer must pay what the invoice says.
		logx.Warn("BeeX invoice amount differs from the request", "depositId", d.ID.Hex(),
			"requested", micro.Format(d.AmountExpected), "invoice", inv.PayAmount.Raw)
		set["amountExpected"] = v
	}
	res, err := depositsColl().UpdateOne(ctx, bson.M{"_id": d.ID, "invoiceId": nil}, bson.M{"$set": set})
	if err != nil {
		return err
	}
	if res.ModifiedCount > 0 {
		logx.Info("BEEX_INVOICE_CREATED", "depositId", d.ID.Hex(), "orderId", d.OrderID, "invoiceId", set["invoiceId"],
			"reference", inv.Reference, "network", d.Network, "amount", micro.Format(d.AmountExpected), "mode", inv.Mode)
	}
	return nil
}

// ── applying BeeX's view of an invoice ─────────────────────────────────

// invoiceRank orders statuses so an out-of-order update cannot move a
// deposit backwards. Closed-unpaid statuses share a rank: a late payment
// (more money) moves them on to paid.
var invoiceRank = map[string]int{
	models.InvoiceCreated: 0, models.InvoiceAwaitingPayment: 1, models.InvoicePartiallyPaid: 2,
	models.InvoiceConfirming: 3, models.InvoiceUnderpaid: 4, models.InvoiceExpired: 4,
	models.InvoiceCancelled: 4, models.InvoiceFailed: 4, models.InvoicePaid: 5, models.InvoiceOverpaid: 6,
}

type invoiceUpdate struct {
	// webhook | refresh | sweeper | admin | user | create
	Source     string
	Status     string
	Mode       string
	InvoiceID  string
	Reference  string
	PayAddress string
	ExpiresAt  *time.Time
	Received   *providerAmount
	Fee        *int64
	Net        *int64
}

func updateFromInvoice(inv *beex.Invoice, source string) (invoiceUpdate, error) {
	u := invoiceUpdate{
		Source: source, Status: inv.Status, Mode: inv.Mode, InvoiceID: inv.ID, Reference: inv.Reference,
		PayAddress: inv.PayAddress, ExpiresAt: inv.ExpiresAt.Ptr(),
	}
	var err error
	if u.Received, err = convertAmount(inv.AmountReceived); err != nil {
		return u, err
	}
	fee, err := convertAmount(inv.FeeAmount)
	if err != nil {
		return u, err
	}
	net, err := convertAmount(inv.NetAmount)
	if err != nil {
		return u, err
	}
	u.Fee, u.Net = amountValue(fee), amountValue(net)
	return u, nil
}

func updateFromWebhook(p beex.WebhookPayload) (invoiceUpdate, error) {
	status := p.Status
	if status == "" {
		// "invoice.paid" -> "paid"
		status = p.Event[len("invoice."):]
	}
	u := invoiceUpdate{Source: "webhook", Status: status, Mode: p.Mode, Reference: p.Reference}
	var err error
	if u.Received, err = convertAmount(p.AmountReceived); err != nil {
		return u, err
	}
	fee, err := convertAmount(p.FeeAmount)
	if err != nil {
		return u, err
	}
	net, err := convertAmount(p.NetAmount)
	if err != nil {
		return u, err
	}
	u.Fee, u.Net = amountValue(fee), amountValue(net)
	return u, nil
}

// applyInvoice applies a BeeX invoice object (create, refresh, sweep).
func applyInvoice(ctx context.Context, id bson.ObjectID, inv *beex.Invoice, source string) (ApplyResult, error) {
	u, err := updateFromInvoice(inv, source)
	if err != nil {
		return ApplyResult{}, err
	}
	return applyInvoiceUpdate(ctx, id, u)
}

func checkInvoiceMode(eventMode string, d *models.UsdtDeposit) (string, error) {
	stored := deref(d.Mode)
	if eventMode != "" && stored != "" && eventMode != stored {
		return "", ignored("mode mismatch: update says " + eventMode + ", invoice is " + stored)
	}
	if eventMode == "" {
		eventMode = stored
	}
	return eventMode, nil
}

func touchSource(set bson.M, source string, now time.Time) {
	switch source {
	case "webhook":
		set["lastWebhookAt"] = now
	case "refresh", "sweeper", "admin", "user", "expiry", "poll", "reconcile":
		set["lastRefreshedAt"] = now
	}
}

// applyInvoiceUpdate is the single path by which BeeX's report of an invoice
// changes a deposit - webhook, refresh and sweep alike. It never moves a
// deposit backwards, never touches a credited deposit's money, and credits
// at most once.
func applyInvoiceUpdate(ctx context.Context, id bson.ObjectID, u invoiceUpdate) (ApplyResult, error) {
	ctx = context.WithoutCancel(ctx)
	if _, ok := invoiceRank[u.Status]; !ok {
		return ApplyResult{}, ignored("unknown invoice status " + strconv.Quote(u.Status))
	}
	d, err := loadDeposit(ctx, id)
	if err != nil {
		return ApplyResult{}, err
	}
	if u.Reference != "" && d.Reference != nil && *d.Reference != u.Reference {
		return ApplyResult{}, ignored("reference mismatch: update says " + u.Reference + ", deposit has " + *d.Reference)
	}
	mode, err := checkInvoiceMode(u.Mode, d)
	if err != nil {
		return ApplyResult{}, err
	}

	now := mongox.Now()
	set := bson.M{"updatedAt": now}
	touchSource(set, u.Source, now)
	if d.InvoiceID == nil && u.InvoiceID != "" {
		set["invoiceId"] = u.InvoiceID
		set["paymentUrl"] = paymentURL(u.InvoiceID)
	}
	if d.Reference == nil && u.Reference != "" {
		set["reference"] = u.Reference
	}
	if d.Mode == nil && u.Mode != "" {
		set["mode"] = u.Mode
	}
	if d.PayAddress == nil && u.PayAddress != "" {
		set["payAddress"] = u.PayAddress
	}
	if d.ExpiresAt == nil && u.ExpiresAt != nil {
		set["expiresAt"] = *u.ExpiresAt
	}

	// A credited deposit's money never changes again: a late expired after
	// paid, or a repeat of paid, only records what BeeX said.
	if d.Credited {
		set["providerStatus"] = u.Status
		changed := false
		note := "already credited"
		if u.Received != nil && u.Received.V > d.AmountReceived {
			set["amountReceived"] = u.Received.V
			set["amountReceivedRaw"] = rawIfInexact(u.Received)
			changed = true
		}
		if models.InvoiceCreditable(u.Status) && invoiceRank[u.Status] > invoiceRank[d.Status] {
			set["status"] = u.Status
			changed = true
		}
		if u.Received != nil && u.Received.V > d.AmountCredited && creditBasis() == CreditBasisRecv {
			note += "; " + micro.Format(u.Received.V-d.AmountCredited) + " USDT received beyond the credited amount needs manual review"
			logx.Warn("USDT received beyond a credited deposit", "depositId", d.ID.Hex(),
				"credited", micro.Format(d.AmountCredited), "received", u.Received.Raw)
			RaiseAlert(ctx, Alert{
				Kind: models.AlertKindDeposit, Code: AlertDepositReceivedBeyond, Severity: models.AlertWarning, Subject: d.ID.Hex(),
				Message: micro.Format(u.Received.V-d.AmountCredited) + " USDT arrived on deposit " + d.OrderID +
					" after it was credited; nothing more was credited automatically.",
				Details:   map[string]any{"amountCredited": d.AmountCredited, "amountReceived": u.Received.V, "source": u.Source},
				DepositID: &d.ID,
			})
		}
		if _, err := depositsColl().UpdateOne(ctx, bson.M{"_id": d.ID, "credited": true}, bson.M{"$set": set}); err != nil {
			return ApplyResult{}, err
		}
		if changed {
			emitUpdated(d.UserID.Hex(), KindDeposit, d.ID.Hex(), d.Status)
		}
		return ApplyResult{Changed: changed, Note: note}, nil
	}

	received := d.AmountReceived
	newMoney := false
	if u.Received != nil {
		if u.Received.V < d.AmountReceived {
			return ApplyResult{Note: "stale update ignored: less received than already recorded"}, nil
		}
		newMoney = u.Received.V > d.AmountReceived
		received = u.Received.V
	}
	if invoiceRank[u.Status] < invoiceRank[d.Status] && !newMoney {
		return ApplyResult{Note: "stale update ignored: " + u.Status + " after " + d.Status}, nil
	}

	set["status"] = u.Status
	set["providerStatus"] = u.Status
	if u.Received != nil {
		set["amountReceived"] = received
		set["amountReceivedRaw"] = rawIfInexact(u.Received)
	}
	if u.Fee != nil {
		set["feeAmount"] = *u.Fee
	}
	if u.Net != nil {
		set["netAmount"] = *u.Net
	}

	if models.InvoiceCreditable(u.Status) {
		if u.Received == nil {
			// Never credit from an older partial figure.
			return ApplyResult{}, errNeedAmounts
		}
		if err := ModeAccepted(mode); err != nil {
			// Recorded for operators, never credited.
			_, _ = depositsColl().UpdateOne(ctx, bson.M{"_id": d.ID, "credited": false},
				bson.M{"$set": bson.M{"providerStatus": u.Status, "updatedAt": now}})
			logx.Warn("BEEX_PAYMENT_FAILED", "depositId", d.ID.Hex(), "reason", err.Error())
			return ApplyResult{}, ignored(err.Error())
		}
		return creditDeposit(ctx, d, u, set, received)
	}

	res, err := depositsColl().UpdateOne(ctx,
		bson.M{"_id": d.ID, "credited": false, "amountReceived": bson.M{"$lte": received}},
		bson.M{"$set": set})
	if err != nil {
		return ApplyResult{}, err
	}
	if res.MatchedCount == 0 {
		return ApplyResult{Note: "deposit changed concurrently; update not applied"}, nil
	}
	changed := u.Status != d.Status || newMoney
	if changed {
		switch u.Status {
		case models.InvoiceUnderpaid, models.InvoiceExpired, models.InvoiceCancelled, models.InvoiceFailed:
			logx.Info("BEEX_PAYMENT_FAILED", "depositId", d.ID.Hex(), "orderId", d.OrderID, "status", u.Status,
				"amountReceived", micro.Format(received), "source", u.Source)
		}
		emitUpdated(d.UserID.Hex(), KindDeposit, d.ID.Hex(), u.Status)
	}
	note := "recorded " + u.Status
	if !changed {
		note = "no change"
	}
	return ApplyResult{Changed: changed, Note: note}, nil
}

func rawIfInexact(a *providerAmount) *string {
	if a == nil || a.Exact {
		return nil
	}
	return &a.Raw
}

// creditDeposit credits a paid deposit: the wallet credit, its ledger row and
// the credited:false -> true flip commit together or not at all.
func creditDeposit(ctx context.Context, d *models.UsdtDeposit, u invoiceUpdate, set bson.M, received int64) (ApplyResult, error) {
	basis := creditBasis()
	amount := received
	if basis == CreditBasisNet {
		switch {
		case u.Net != nil:
			amount = *u.Net
		case u.Fee != nil:
			amount = received - *u.Fee
		default:
			return ApplyResult{}, errNeedAmounts
		}
	}
	if amount <= 0 {
		return ApplyResult{}, errors.New("paid invoice without a received amount; waiting for BeeX to report it")
	}
	fee := d.FeeAmount
	if u.Fee != nil {
		fee = *u.Fee
	}
	if _, err := fundingWallet(ctx, d.UserID.Hex()); err != nil {
		return ApplyResult{}, err
	}
	now := mongox.Now()
	set["credited"] = true
	set["amountCredited"] = amount
	set["creditedAt"] = now
	set["creditBasis"] = basis
	reference := deref(d.Reference)
	if reference == "" {
		reference = d.OrderID
	}
	res, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{
		UserID:        d.UserID.Hex(),
		AccountType:   models.AccountFunding,
		Currency:      models.CurrencyUSDT,
		Amount:        amount,
		Type:          models.WalletTxDeposit,
		ReferenceType: RefDeposit,
		ReferenceID:   d.ID.Hex(),
		// One credit per invoice, whatever path gets here first.
		IdempotencyKey: "usdt:deposit:" + d.ID.Hex(),
		Description:    "USDT deposit " + reference,
		Network:        d.Network,
		Fee:            &fee,
		Metadata: map[string]any{
			"orderId": d.OrderID, "invoiceReference": reference, "status": u.Status, "basis": basis,
			"amountReceived": received, "source": u.Source,
		},
		InTx: func(tctx context.Context, txID string) error {
			if oid, ok := mongox.ParseID(txID); ok {
				set["walletTransactionId"] = oid
			}
			r, err := depositsColl().UpdateOne(tctx, bson.M{"_id": d.ID, "credited": false}, bson.M{"$set": set})
			if err != nil {
				return err
			}
			if r.MatchedCount == 0 {
				return errAlreadyCredited
			}
			return nil
		},
	})
	if errors.Is(err, errAlreadyCredited) {
		return ApplyResult{Note: "already credited"}, nil
	}
	if err != nil {
		return ApplyResult{}, err
	}
	if res.Replayed {
		// The ledger row already exists; make sure the deposit says so (it
		// always does when the credit ran in a transaction).
		if oid, ok := mongox.ParseID(res.TransactionID); ok {
			set["walletTransactionId"] = oid
		}
		r, err := depositsColl().UpdateOne(ctx, bson.M{"_id": d.ID, "credited": false}, bson.M{"$set": set})
		if err != nil {
			return ApplyResult{}, err
		}
		if r.ModifiedCount == 0 {
			return ApplyResult{Note: "already credited"}, nil
		}
	}
	logx.Info("BEEX_PAYMENT_CONFIRMED", "depositId", d.ID.Hex(), "orderId", d.OrderID, "reference", reference,
		"network", d.Network, "status", u.Status, "amountCredited", micro.Format(amount), "basis", basis,
		"amountReceived", micro.Format(received), "fee", micro.Format(fee), "source", u.Source)
	notifyDepositCredited(d, amount)
	emitUpdated(d.UserID.Hex(), KindDeposit, d.ID.Hex(), u.Status)
	return ApplyResult{Changed: true, Credited: true, Note: "credited " + micro.Format(amount) + " USDT"}, nil
}

// ── refresh ─────────────────────────────────────────────────────────────

const userRefreshEvery = 30 * time.Second

// RefreshDeposit asks BeeX for the invoice (by our orderId) and applies the
// answer. It never overwrites a credited deposit's money.
func RefreshDeposit(ctx context.Context, id bson.ObjectID, source string) (ApplyResult, *models.UsdtDeposit, error) {
	ctx = context.WithoutCancel(ctx)
	d, err := loadDeposit(ctx, id)
	if err != nil {
		return ApplyResult{}, nil, err
	}
	p, err := provider()
	if err != nil {
		return ApplyResult{}, d, err
	}
	now := mongox.Now()
	touch := bson.M{"updatedAt": now}
	touchSource(touch, source, now)
	inv, err := p.GetInvoice(ctx, d.OrderID)
	if err != nil {
		if beex.IsNotFound(err) && d.InvoiceID == nil && d.Status == models.InvoiceCreated && now.Sub(d.CreatedAt) > time.Hour {
			touch["status"] = models.InvoiceFailed
			touch["failureReason"] = "The payment request could not be created. Please start a new deposit."
			r, uerr := depositsColl().UpdateOne(ctx, bson.M{"_id": id, "invoiceId": nil, "status": models.InvoiceCreated}, bson.M{"$set": touch})
			if uerr != nil {
				return ApplyResult{}, d, uerr
			}
			d, _ = loadDeposit(ctx, id)
			if r.ModifiedCount > 0 {
				emitUpdated(d.UserID.Hex(), KindDeposit, id.Hex(), models.InvoiceFailed)
				return ApplyResult{Changed: true, Note: "BeeX has no invoice for this order; marked failed"}, d, nil
			}
			return ApplyResult{Note: "no change"}, d, nil
		}
		_, _ = depositsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": touch})
		if beex.IsNotFound(err) {
			return ApplyResult{Note: "BeeX has no invoice for this order"}, d, nil
		}
		return ApplyResult{}, d, beex.ToAppError(err)
	}
	if d.InvoiceID == nil {
		if err := adoptInvoice(ctx, d, inv); err != nil {
			return ApplyResult{}, d, err
		}
	}
	res, err := applyInvoice(ctx, id, inv, source)
	if ig, ok := AsIgnored(err); ok {
		res, err = ApplyResult{Note: "not applied: " + ig.Reason}, nil
		_, _ = depositsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": touch})
	}
	if err != nil {
		return ApplyResult{}, d, err
	}
	d, err = loadDeposit(ctx, id)
	if err != nil {
		return res, nil, err
	}
	logx.Info("BEEX_RECONCILIATION", "kind", KindDeposit, "depositId", id.Hex(), "source", source,
		"status", d.Status, "changed", res.Changed, "credited", res.Credited, "note", res.Note)
	return res, d, nil
}

// UserRefreshDeposit: a customer may ask BeeX about their deposit at most
// once per 30 seconds; otherwise (or if BeeX is unreachable) the stored
// record is returned.
func UserRefreshDeposit(ctx context.Context, userID string, id bson.ObjectID) (*models.UsdtDeposit, error) {
	if err := requireEnabled(); err != nil {
		return nil, err
	}
	d, err := GetUserDeposit(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	if d.Credited {
		return d, nil
	}
	now := mongox.Now()
	claim, err := depositsColl().UpdateOne(ctx, bson.M{"_id": id, "userId": d.UserID, "$or": bson.A{
		bson.M{"lastUserRefreshAt": nil},
		bson.M{"lastUserRefreshAt": bson.M{"$lt": now.Add(-userRefreshEvery)}},
	}}, bson.M{"$set": bson.M{"lastUserRefreshAt": now}})
	if err != nil {
		return nil, err
	}
	if claim.ModifiedCount == 0 {
		return d, nil
	}
	_, fresh, err := RefreshDeposit(ctx, id, "user")
	if err != nil || fresh == nil {
		if err != nil {
			logx.Warn("customer deposit refresh failed", "depositId", id.Hex(), "err", err.Error())
		}
		return loadDeposit(ctx, id)
	}
	return fresh, nil
}

// ── operator credit of a closed short deposit ──────────────────────────

// AdminCreditDeposit credits what actually arrived on a deposit that closed
// short (underpaid, or partially paid after its window). Only an operator
// can do this, with a note; the API audits it.
func AdminCreditDeposit(ctx context.Context, id bson.ObjectID, adminID, note string) (*models.UsdtDeposit, int64, error) {
	ctx = context.WithoutCancel(ctx)
	admin, ok := mongox.ParseID(adminID)
	if !ok {
		return nil, 0, apperr.Unauth()
	}
	d, err := loadDeposit(ctx, id)
	if err != nil {
		return nil, 0, err
	}
	if d.Credited {
		return nil, 0, apperr.ConflictErr(apperr.TransactionAlreadyProcessed, "This deposit has already been credited")
	}
	switch d.Status {
	case models.InvoiceUnderpaid:
	case models.InvoicePartiallyPaid:
		if d.ExpiresAt == nil || d.ExpiresAt.After(time.Now()) {
			return nil, 0, apperr.ConflictErr(apperr.Conflict, "The invoice is still open; wait until its payment window closes")
		}
	default:
		return nil, 0, apperr.ConflictErr(apperr.Conflict, "Only underpaid or partially paid deposits can be credited manually")
	}
	amount := d.AmountReceived
	if creditBasis() == CreditBasisNet {
		amount = d.NetAmount
		if amount <= 0 {
			amount = d.AmountReceived - d.FeeAmount
		}
	}
	if amount <= 0 {
		return nil, 0, apperr.New(apperr.InvalidAmount, "Nothing was received on this deposit", 400)
	}
	if _, err := fundingWallet(ctx, d.UserID.Hex()); err != nil {
		return nil, 0, err
	}
	now := mongox.Now()
	set := bson.M{
		"credited": true, "amountCredited": amount, "creditedAt": now, "creditBasis": creditBasis(), "updatedAt": now,
		"manualCredit": models.UsdtManualCredit{By: admin, Note: note, Amount: amount, At: now},
	}
	fee := d.FeeAmount
	res, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{
		UserID: d.UserID.Hex(), AccountType: models.AccountFunding, Currency: models.CurrencyUSDT,
		Amount: amount, Type: models.WalletTxDeposit, ReferenceType: RefDeposit, ReferenceID: d.ID.Hex(),
		IdempotencyKey: "usdt:deposit:" + d.ID.Hex(),
		Description:    "USDT deposit " + d.OrderID + " (credited by an operator)",
		Network:        d.Network, Fee: &fee,
		Metadata: map[string]any{"orderId": d.OrderID, "status": d.Status, "manual": true, "creditedBy": adminID, "note": note},
		InTx: func(tctx context.Context, txID string) error {
			if oid, ok := mongox.ParseID(txID); ok {
				set["walletTransactionId"] = oid
			}
			r, err := depositsColl().UpdateOne(tctx, bson.M{"_id": d.ID, "credited": false, "status": d.Status}, bson.M{"$set": set})
			if err != nil {
				return err
			}
			if r.MatchedCount == 0 {
				return errAlreadyCredited
			}
			return nil
		},
	})
	if errors.Is(err, errAlreadyCredited) || (err == nil && res.Replayed) {
		return nil, 0, apperr.ConflictErr(apperr.TransactionAlreadyProcessed, "This deposit has already been credited")
	}
	if err != nil {
		return nil, 0, err
	}
	logx.Info("BEEX_PAYMENT_CONFIRMED", "depositId", d.ID.Hex(), "orderId", d.OrderID, "manual", true,
		"amountCredited", micro.Format(amount), "adminId", adminID)
	notifyDepositCredited(d, amount)
	emitUpdated(d.UserID.Hex(), KindDeposit, d.ID.Hex(), d.Status)
	after, err := loadDeposit(ctx, id)
	return after, amount, err
}

// ── listing ─────────────────────────────────────────────────────────────

// DepositQuery filters deposits. Nil = no filter.
type DepositQuery struct {
	UserID    *bson.ObjectID
	UserIDs   []bson.ObjectID // from a customer search; empty slice = no match
	Search    bool
	Status    *string
	Network   *string
	Reference *string
	OrderID   *string
	From, To  *time.Time
	Page      int64
	Limit     int64
}

func dateRange(from, to *time.Time) bson.M {
	rng := bson.M{}
	if from != nil {
		rng["$gte"] = *from
	}
	if to != nil {
		rng["$lte"] = *to
	}
	return rng
}

// userConds is the userId part of a listing filter.
func userConds(userID *bson.ObjectID, search bool, userIDs []bson.ObjectID) bson.A {
	conds := bson.A{}
	if userID != nil {
		conds = append(conds, bson.M{"userId": *userID})
	}
	if search {
		ids := bson.A{}
		for _, id := range userIDs {
			ids = append(ids, id)
		}
		conds = append(conds, bson.M{"userId": bson.M{"$in": ids}})
	}
	return conds
}

func andFilter(conds bson.A) bson.M {
	if len(conds) == 0 {
		return bson.M{}
	}
	return bson.M{"$and": conds}
}

// ListDeposits returns deposits newest first.
func ListDeposits(ctx context.Context, q DepositQuery) ([]models.UsdtDeposit, int64, error) {
	conds := userConds(q.UserID, q.Search, q.UserIDs)
	if q.Status != nil {
		conds = append(conds, bson.M{"status": *q.Status})
	}
	if q.Network != nil {
		conds = append(conds, bson.M{"network": NormNetwork(*q.Network)})
	}
	if q.Reference != nil && *q.Reference != "" {
		conds = append(conds, bson.M{"$or": bson.A{bson.M{"reference": *q.Reference}, bson.M{"invoiceId": *q.Reference}}})
	}
	if q.OrderID != nil && *q.OrderID != "" {
		conds = append(conds, bson.M{"orderId": *q.OrderID})
	}
	if q.From != nil || q.To != nil {
		conds = append(conds, bson.M{"createdAt": dateRange(q.From, q.To)})
	}
	filter := andFilter(conds)
	page, limit := max(q.Page, 1), max(q.Limit, 1)
	cur, err := depositsColl().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.UsdtDeposit
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	total, err := depositsColl().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// escapeSearch makes customer search text safe to use in a regex.
func escapeSearch(s string) string { return regexp.QuoteMeta(s) }
