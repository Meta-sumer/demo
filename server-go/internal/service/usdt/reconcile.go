package usdt

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
	"binarytrading/internal/service/wallets"
)

// Provider reconciliation: three-way, for every recent invoice and payout -
// what BeeX says, what our record says, what the ledger says.
//
// It first applies BeeX's answer through the same single path the webhook
// uses (so a missed "paid" webhook is credited here, once), then compares
// what is left. Whatever still disagrees becomes an alert; nothing is ever
// "fixed" by adjusting a balance.

const (
	reconcileLookback = 7 * 24 * time.Hour
	reconcileBatch    = 100
	reconcileBudget   = 10 * time.Minute
)

// ReconcileResult reports one provider reconciliation run.
type ReconcileResult struct {
	Deposits          int      `json:"deposits"`
	Withdrawals       int      `json:"withdrawals"`
	Changed           int      `json:"changed"`
	Alerts            int      `json:"alerts"`
	Errors            int      `json:"errors"`
	ProviderReachable bool     `json:"providerReachable"`
	StartedAt         iso.Time `json:"startedAt"`
	DurationMs        int64    `json:"durationMs"`
}

// stillLeader is the cheap, local half of the worker fence, checked before
// each record: a former leader whose lease ran out stops between records.
// Outside a worker loop (API, tests) there is no fence and it always passes.
func stillLeader(ctx context.Context) error {
	if f := leader.FenceFromContext(ctx); f != nil && !f.LeaseValid() {
		return leader.ErrLeadershipLost
	}
	return nil
}

// checkFence verifies leadership in Redis at the start of a batch.
func checkFence(ctx context.Context) error {
	if f := leader.FenceFromContext(ctx); f != nil {
		return f.Check(ctx)
	}
	return nil
}

func idsOf(ctx context.Context, coll string, filter bson.M, limit int64) ([]bson.ObjectID, error) {
	cur, err := mongox.C(coll).Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "lastReconciledAt", Value: 1}, {Key: "createdAt", Value: 1}}).
		SetLimit(limit).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]bson.ObjectID, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

// providerFailure: BeeX did not answer (as opposed to answering "no").
func providerFailure(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := beex.AsError(err); ok {
		return e.Temporary || beex.IsConfigError(err)
	}
	return apperr.HasCode(err, apperr.ProviderUnavailable) || apperr.HasCode(err, apperr.ProviderError)
}

// ReconcileProvider compares up to reconcileBatch deposits and withdrawals
// (the ones compared longest ago first) with BeeX and the ledger.
func ReconcileProvider(ctx context.Context, now time.Time) (ReconcileResult, error) {
	res := ReconcileResult{StartedAt: iso.From(now), ProviderReachable: true}
	if !Enabled() {
		return res, nil
	}
	if err := checkFence(ctx); err != nil {
		return res, err
	}
	p, err := provider()
	if err != nil {
		return res, err
	}
	ctx, cancel := context.WithTimeout(ctx, reconcileBudget)
	defer cancel()
	since := now.Add(-reconcileLookback)
	calls, failures := 0, 0
	count := func(r recordOutcome) {
		calls += r.calls
		failures += r.failures
		res.Alerts += r.alerts
		if r.changed {
			res.Changed++
		}
		if r.err != nil {
			res.Errors++
		}
	}

	// Recent ones, and any that may still owe a credit however old.
	owing := append(toA(models.InvoiceOpenStatuses), models.InvoicePaid, models.InvoiceOverpaid)
	depositIDs, err := idsOf(ctx, models.CollUsdtDeposits, bson.M{
		"invoiceId": bson.M{"$ne": nil},
		"$or": bson.A{
			bson.M{"updatedAt": bson.M{"$gte": since}},
			bson.M{"credited": false, "status": bson.M{"$in": owing}},
		},
	}, reconcileBatch)
	if err != nil {
		return res, err
	}
	for _, id := range depositIDs {
		if err := stillLeader(ctx); err != nil {
			return res, err
		}
		res.Deposits++
		count(reconcileDeposit(ctx, p, id))
	}

	withdrawalIDs, err := idsOf(ctx, models.CollUsdtWithdrawals, bson.M{"$or": bson.A{
		bson.M{"updatedAt": bson.M{"$gte": since}},
		bson.M{"status": bson.M{"$in": toA(models.WithdrawalOpenStatuses)}},
	}}, reconcileBatch)
	if err != nil {
		return res, err
	}
	for _, id := range withdrawalIDs {
		if err := stillLeader(ctx); err != nil {
			return res, err
		}
		res.Withdrawals++
		count(reconcileWithdrawal(ctx, p, id))
	}

	unreachable := Alert{Kind: models.AlertKindProvider, Code: AlertProviderUnreachable, Subject: "beex"}
	if calls > 0 && failures == calls {
		res.ProviderReachable = false
		unreachable.Severity = models.AlertWarning
		unreachable.Message = "BeeX could not be reached during reconciliation; deposits and payouts are not being confirmed."
		unreachable.Details = map[string]any{"calls": calls}
		RaiseAlert(ctx, unreachable)
		res.Alerts++
	} else if calls > failures {
		resolveAlertsBySystem(ctx, unreachable, "BeeX answered again")
	}
	res.DurationMs = time.Since(now).Milliseconds()
	logx.Info("BEEX_RECONCILIATION provider run", "deposits", res.Deposits, "withdrawals", res.Withdrawals,
		"changed", res.Changed, "alerts", res.Alerts, "errors", res.Errors, "providerReachable", res.ProviderReachable)
	return res, nil
}

func toA(ss []string) bson.A {
	out := bson.A{}
	for _, s := range ss {
		out = append(out, s)
	}
	return out
}

type recordOutcome struct {
	changed         bool
	alerts          int
	calls, failures int
	err             error
}

func (o *recordOutcome) raise(ctx context.Context, a Alert) {
	RaiseAlert(ctx, a)
	o.alerts++
}

// markReconciled is bookkeeping, not a change to the record: updatedAt is
// left alone so a record does not stay "recently changed" (and in every run)
// only because it was looked at.
func markReconciled(ctx context.Context, coll string, id bson.ObjectID) {
	if _, err := mongox.C(coll).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"lastReconciledAt": mongox.Now()}}); err != nil {
		logx.Warn("could not mark a record reconciled", "collection", coll, "id", id.Hex(), "err", err.Error())
	}
}

func reconcileDeposit(ctx context.Context, p beex.Provider, id bson.ObjectID) (out recordOutcome) {
	defer markReconciled(ctx, models.CollUsdtDeposits, id)
	d, err := loadDeposit(ctx, id)
	if err != nil {
		out.err = err
		return out
	}
	alert := func(code, severity, msg string, details map[string]any) {
		out.raise(ctx, Alert{Kind: models.AlertKindDeposit, Code: code, Severity: severity, Subject: id.Hex(),
			Message: msg, Details: details, DepositID: &d.ID})
	}

	out.calls++
	inv, err := p.GetInvoice(ctx, d.OrderID)
	switch {
	case err != nil && beex.IsNotFound(err):
		severity := models.AlertWarning
		if d.Credited {
			severity = models.AlertCritical
		}
		alert(AlertDepositMissingAtProvider, severity, "BeeX has no invoice for deposit "+d.OrderID+" although it issued invoice "+
			deref(d.InvoiceID)+".", map[string]any{"orderId": d.OrderID, "invoiceId": deref(d.InvoiceID), "credited": d.Credited})
	case err != nil:
		out.failures++
		out.err = err
		return out
	default:
		before := d.Status
		wasCredited := d.Credited
		r, aerr := applyInvoice(ctx, id, inv, "reconcile")
		if ig, ok := AsIgnored(aerr); ok {
			if models.InvoiceCreditable(inv.Status) && !d.Credited {
				alert(AlertDepositPaidNotCredited, models.AlertCritical, "BeeX reports deposit "+d.OrderID+" as "+inv.Status+
					" but it cannot be credited: "+ig.Reason, map[string]any{"providerStatus": inv.Status, "reason": ig.Reason})
			} else {
				alert(AlertDepositProviderMismatch, models.AlertCritical, "BeeX's invoice for deposit "+d.OrderID+
					" does not match ours: "+ig.Reason, map[string]any{"providerStatus": inv.Status, "reason": ig.Reason})
			}
		} else if aerr != nil && !errors.Is(aerr, errNeedAmounts) {
			out.err = aerr
			return out
		}
		if d, err = loadDeposit(ctx, id); err != nil {
			out.err = err
			return out
		}
		out.changed = r.Changed || d.Status != before || d.Credited != wasCredited
		switch {
		case models.InvoiceCreditable(inv.Status) && !d.Credited:
			alert(AlertDepositPaidNotCredited, models.AlertCritical, "BeeX reports deposit "+d.OrderID+" as "+inv.Status+
				" but our wallet has not been credited.", map[string]any{"providerStatus": inv.Status, "status": d.Status})
		case d.Credited && !models.InvoiceCreditable(inv.Status):
			alert(AlertDepositCreditedUnpaid, models.AlertCritical, "Deposit "+d.OrderID+" was credited "+
				micro.Format(d.AmountCredited)+" USDT but BeeX now reports the invoice as "+inv.Status+".",
				map[string]any{"providerStatus": inv.Status, "amountCredited": d.AmountCredited})
		}
	}
	checkDepositLedger(ctx, d, &out)
	return out
}

// checkDepositLedger: a credited deposit has exactly one DEPOSIT credit for
// what it says it credited; an uncredited one has none.
func checkDepositLedger(ctx context.Context, d *models.UsdtDeposit, out *recordOutcome) {
	rows, err := wallets.LedgerRowsFor(ctx, RefDeposit, d.ID.Hex())
	if err != nil {
		out.err = err
		return
	}
	var credits, amount int64
	for _, r := range rows {
		if r.Type == models.WalletTxDeposit && r.Movement == models.MovementCredit {
			credits++
			amount += r.Amount
		}
	}
	ok := (d.Credited && credits == 1 && amount == d.AmountCredited) || (!d.Credited && len(rows) == 0)
	if !ok {
		out.raise(ctx, Alert{
			Kind: models.AlertKindDeposit, Code: AlertLedgerMismatch, Severity: models.AlertCritical, Subject: d.ID.Hex(),
			Message: "The ledger does not match deposit " + d.OrderID + ".",
			Details: map[string]any{"credited": d.Credited, "amountCredited": d.AmountCredited, "ledgerRows": len(rows),
				"ledgerCredits": credits, "ledgerAmount": amount},
			DepositID: &d.ID,
		})
	}
}

func reconcileWithdrawal(ctx context.Context, p beex.Provider, id bson.ObjectID) (out recordOutcome) {
	defer markReconciled(ctx, models.CollUsdtWithdrawals, id)
	w, err := loadWithdrawal(ctx, id)
	if err != nil {
		out.err = err
		return out
	}
	alert := func(code, severity, msg string, details map[string]any) {
		out.raise(ctx, Alert{Kind: models.AlertKindWithdrawal, Code: code, Severity: severity, Subject: id.Hex(),
			Message: msg, Details: details, WithdrawalID: &w.ID})
	}
	// Never sent: pending review, or closed before BeeX ever answered.
	neverSent := w.Status == models.WithdrawalPendingReview ||
		(models.WithdrawalFinal(w.Status) && w.SubmittedAt == nil && w.ProviderReference == nil && w.SubmitAttempts == 0)
	if !neverSent {
		out.calls++
		payout, err := p.GetPayout(ctx, w.Reference)
		switch {
		case err != nil && beex.IsNotFound(err):
			if w.SubmittedAt != nil || w.ProviderReference != nil {
				severity := models.AlertWarning
				if w.Status == models.WithdrawalCompleted {
					severity = models.AlertCritical
				}
				alert(AlertWithdrawalMissing, severity, "BeeX has no payout under "+w.Reference+" although it accepted one.",
					map[string]any{"status": w.Status, "providerReference": deref(w.ProviderReference)})
			}
		case err != nil:
			out.failures++
			out.err = err
			return out
		default:
			before := w.Status
			_, aerr := applyPayout(ctx, id, payout, "reconcile")
			if ig, ok := AsIgnored(aerr); ok {
				// A completed-after-release conflict raised its own alert. A
				// withdrawal an operator already resolved after checking BeeX
				// is not alerted again for the same mismatch.
				if !containsConflict(ig.Reason) && w.Status != models.WithdrawalPendingReview && w.Resolution == nil {
					alert(AlertWithdrawalProviderMismatch, models.AlertCritical, "BeeX's payout for "+w.Reference+
						" does not match the withdrawal: "+ig.Reason, map[string]any{"providerStatus": payout.Status, "reason": ig.Reason})
					markProviderMismatch(ctx, id, ig.Reason)
				}
			} else if aerr != nil {
				out.err = aerr
				return out
			}
			if w, err = loadWithdrawal(ctx, id); err != nil {
				out.err = err
				return out
			}
			out.changed = w.Status != before
			providerFinal := payout.Status == models.WithdrawalCompleted || payout.Status == models.WithdrawalFailed ||
				payout.Status == models.WithdrawalRejected
			switch {
			case w.Status == models.WithdrawalCompleted && (payout.Status == models.WithdrawalFailed || payout.Status == models.WithdrawalRejected):
				alert(AlertWithdrawalSettledFailed, models.AlertCritical, "Withdrawal "+w.Reference+" was settled (debited "+
					micro.Format(w.Amount)+" USDT) but BeeX now reports the payout as "+payout.Status+".",
					map[string]any{"providerStatus": payout.Status, "amount": w.Amount})
			case !models.WithdrawalFinal(w.Status) && providerFinal:
				alert(AlertWithdrawalStateMismatch, models.AlertCritical, "BeeX reports payout "+w.Reference+" as "+payout.Status+
					" but the withdrawal is still "+w.Status+".", map[string]any{"providerStatus": payout.Status, "status": w.Status})
			}
		}
	}
	checkWithdrawalLedger(ctx, w, &out)
	return out
}

func containsConflict(reason string) bool {
	return len(reason) >= 8 && reason[:8] == "CONFLICT"
}

// checkWithdrawalLedger: every withdrawal reserved its gross once; a
// completed one was settled once and never released, a failed or rejected
// one released once and never settled, an open one neither.
func checkWithdrawalLedger(ctx context.Context, w *models.UsdtWithdrawal, out *recordOutcome) {
	rows, err := wallets.LedgerRowsFor(ctx, RefWithdrawal, w.ID.Hex())
	if err != nil {
		out.err = err
		return
	}
	id := w.ID.Hex()
	amounts := map[string]int64{}
	counts := map[string]int{}
	for _, r := range rows {
		if r.IdempotencyKey == nil {
			continue
		}
		switch *r.IdempotencyKey {
		case "usdt:withdrawal:lock:" + id:
			counts["lock"]++
			amounts["lock"] += r.Amount
		case "usdt:withdrawal:settle:" + id:
			counts["settle"]++
			amounts["settle"] += r.Amount
		case "usdt:withdrawal:unlock:" + id:
			counts["unlock"]++
			amounts["unlock"] += r.Amount
		}
	}
	once := func(k string) bool { return counts[k] == 1 && amounts[k] == w.Amount }
	none := func(k string) bool { return counts[k] == 0 }
	var ok bool
	switch w.Status {
	case models.WithdrawalCompleted:
		ok = once("lock") && once("settle") && none("unlock") && w.LockedAmount == 0
	case models.WithdrawalFailed, models.WithdrawalRejected:
		ok = once("lock") && once("unlock") && none("settle") && w.LockedAmount == 0
	default:
		ok = once("lock") && none("settle") && none("unlock") && w.LockedAmount == w.Amount
	}
	if !ok {
		out.raise(ctx, Alert{
			Kind: models.AlertKindWithdrawal, Code: AlertLedgerMismatch, Severity: models.AlertCritical, Subject: id,
			Message: "The ledger does not match withdrawal " + w.Reference + " (" + w.Status + ").",
			Details: map[string]any{"status": w.Status, "amount": w.Amount, "lockedAmount": w.LockedAmount,
				"locks": counts["lock"], "settles": counts["settle"], "unlocks": counts["unlock"]},
			WithdrawalID: &w.ID,
		})
	}
}

// ── invoice expiry ──────────────────────────────────────────────────────

const (
	expiryGrace      = 2 * time.Minute
	expiryRecheck    = 5 * time.Minute
	localExpireAfter = 6 * time.Hour
	expiryBatch      = 50
)

// ExpireOverdueDeposits asks BeeX about open deposits whose payment window
// has passed - BeeX decides whether an invoice expired, and a late payment
// still counts. Only when BeeX stays unreachable long after the window, and
// no money was ever seen, is the deposit closed as expired here (no money
// moves; a payment reported later still moves it on to paid).
func ExpireOverdueDeposits(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	if !Enabled() {
		return res, nil
	}
	if err := checkFence(ctx); err != nil {
		return res, err
	}
	cur, err := depositsColl().Find(ctx, bson.M{
		"credited":  false,
		"status":    bson.M{"$in": toA(models.InvoiceOpenStatuses)},
		"expiresAt": bson.M{"$lt": now.Add(-expiryGrace)},
		"$or":       bson.A{bson.M{"lastRefreshedAt": nil}, bson.M{"lastRefreshedAt": bson.M{"$lt": now.Add(-expiryRecheck)}}},
	}, options.Find().SetSort(bson.D{{Key: "expiresAt", Value: 1}}).SetLimit(expiryBatch).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return res, err
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return res, err
	}
	for _, r := range rows {
		if err := stillLeader(ctx); err != nil {
			return res, err
		}
		res.Deposits++
		applied, d, err := RefreshDeposit(ctx, r.ID, "expiry")
		if err != nil {
			res.Errors++
			if providerFailure(err) && d != nil {
				if expireLocally(ctx, d, now) {
					res.Changed++
				}
			}
			continue
		}
		if applied.Changed {
			res.Changed++
		}
	}
	return res, nil
}

func expireLocally(ctx context.Context, d *models.UsdtDeposit, now time.Time) bool {
	if d.ExpiresAt == nil || now.Sub(*d.ExpiresAt) < localExpireAfter || d.AmountReceived != 0 ||
		(d.Status != models.InvoiceCreated && d.Status != models.InvoiceAwaitingPayment) {
		return false
	}
	ts := mongox.Now()
	res, err := depositsColl().UpdateOne(ctx,
		bson.M{"_id": d.ID, "credited": false, "status": d.Status, "amountReceived": int64(0)},
		bson.M{"$set": bson.M{"status": models.InvoiceExpired, "expiredLocallyAt": ts, "updatedAt": ts}})
	if err != nil || res.ModifiedCount == 0 {
		return false
	}
	logx.Info("BEEX_PAYMENT_FAILED", "depositId", d.ID.Hex(), "orderId", d.OrderID, "status", models.InvoiceExpired,
		"source", "local-expiry", "reason", "BeeX unreachable long after the invoice window closed")
	emitUpdated(d.UserID.Hex(), KindDeposit, d.ID.Hex(), models.InvoiceExpired)
	return true
}

// ── payout polling ──────────────────────────────────────────────────────

const (
	payoutPollEvery = 2 * time.Minute
	payoutPollBatch = 20
)

// PollPayouts asks BeeX about submitted payouts nothing has been heard of
// for a while, so a withdrawal completes (or is refunded) even when its
// webhook never arrives.
func PollPayouts(ctx context.Context, now time.Time) (SweepResult, error) {
	var res SweepResult
	if !Enabled() {
		return res, nil
	}
	if err := checkFence(ctx); err != nil {
		return res, err
	}
	quiet := now.Add(-payoutPollEvery)
	cur, err := withdrawalsColl().Find(ctx, bson.M{
		"status":            bson.M{"$in": submittedStatuses},
		"submissionPending": false,
		"updatedAt":         bson.M{"$lt": quiet},
		"$and": bson.A{
			bson.M{"$or": bson.A{bson.M{"lastRefreshedAt": nil}, bson.M{"lastRefreshedAt": bson.M{"$lt": quiet}}}},
			bson.M{"$or": bson.A{bson.M{"lastWebhookAt": nil}, bson.M{"lastWebhookAt": bson.M{"$lt": quiet}}}},
		},
	}, options.Find().SetSort(bson.D{{Key: "lastRefreshedAt", Value: 1}, {Key: "createdAt", Value: 1}}).
		SetLimit(payoutPollBatch).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return res, err
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return res, err
	}
	for _, r := range rows {
		if err := stillLeader(ctx); err != nil {
			return res, err
		}
		res.Withdrawals++
		applied, _, err := RefreshWithdrawal(ctx, r.ID, "poll")
		if err != nil {
			res.Errors++
			continue
		}
		if applied.Changed {
			res.Changed++
		}
	}
	return res, nil
}
