package usdt

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
	"binarytrading/internal/service/reconciliation"
)

// Provider reconciliation, invoice expiry, payout polling and the worker
// fence.

func (h *harness) reconcile() ReconcileResult {
	h.t.Helper()
	res, err := ReconcileProvider(h.ctx, time.Now())
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (h *harness) check(name string) reconciliation.CheckView {
	h.t.Helper()
	run, err := reconciliation.RunReconciliation(h.ctx, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range run.Checks {
		if c.Name == name {
			return c
		}
	}
	h.t.Fatalf("no check %s", name)
	return reconciliation.CheckView{}
}

func TestSandboxReconciliationCreditsAMissedWebhookOnce(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(90))
	// Paid at BeeX; the webhook never arrives.
	h.mock.SetInvoice(*d.InvoiceID, "paid", "90", "0")
	if res := h.reconcile(); res.Changed != 1 || res.Alerts != 0 {
		t.Fatalf("first run: %+v", res)
	}
	if res := h.reconcile(); res.Changed != 0 || res.Alerts != 0 {
		t.Fatalf("second run: %+v", res)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:90000000")
	h.expectReconciled()
}

func TestSandboxReconciliationFlagsCreditedDepositBeexCallsUnpaid(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.fund(uid, usdtAmt(60))
	h.mock.SetInvoice(*d.InvoiceID, "expired", "", "")
	if res := h.reconcile(); res.Alerts == 0 {
		t.Fatalf("no alert: %+v", res)
	}
	a := h.expectAlert(AlertDepositCreditedUnpaid)
	if a.Severity != models.AlertCritical || a.DepositID == nil || *a.DepositID != d.ID {
		t.Fatalf("alert %+v", a)
	}
	// A second run does not pile up alerts: the open one is updated.
	h.reconcile()
	if rows := h.alerts(AlertDepositCreditedUnpaid); len(rows) != 1 || rows[0].Occurrences != 2 {
		t.Fatalf("alerts after a repeat: %+v", rows)
	}
	if c := h.check("usdt_provider_alerts"); c.Passed {
		t.Fatalf("reconciliation run passed with an open critical alert: %+v", c)
	}
	// Nothing was taken back automatically.
	h.expectLedger(uid, "DEPOSIT/CREDIT:60000000")
	if _, err := ResolveAlert(h.ctx, a.ID, h.user(), "Confirmed with BeeX support: the invoice was paid"); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveAlert(h.ctx, a.ID, h.user(), "again"); !hasCode(err, apperr.Conflict) {
		t.Fatalf("second resolve: %v", err)
	}
	if c := h.check("usdt_provider_alerts"); !c.Passed {
		t.Fatalf("after resolving: %+v", c)
	}
}

func TestSandboxReconciliationFlagsSettledWithdrawalBeexCallsFailed(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.approve(h.withdraw(uid, usdtAmt(100)).ID)
	h.deliver(payoutEvent(w, "completed", "0x01"))
	h.mock.SetPayout(w.Reference, "failed", "")
	h.reconcile()
	if a := h.expectAlert(AlertWithdrawalSettledFailed); a.WithdrawalID == nil || *a.WithdrawalID != w.ID {
		t.Fatalf("alert %+v", a)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/DEBIT:100000000")
}

func TestSandboxReconciliationFlagsLedgerMismatch(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.approve(h.withdraw(uid, usdtAmt(100)).ID)
	h.mock.SetPayout(w.Reference, "completed", "0x02")
	h.deliver(payoutEvent(w, "completed", "0x02"))
	// The settlement row disappears (a bad manual edit, a partial restore).
	if _, err := mongox.C(models.CollWalletTransactions).DeleteOne(h.ctx,
		bson.M{"idempotencyKey": "usdt:withdrawal:settle:" + w.ID.Hex()}); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if a := h.expectAlert(AlertLedgerMismatch); a.WithdrawalID == nil || a.Severity != models.AlertCritical {
		t.Fatalf("alert %+v", a)
	}
	for _, name := range []string{"usdt_wallet_ledger_vs_balance", "usdt_completed_withdrawals_settled", "usdt_provider_alerts"} {
		if c := h.check(name); c.Passed {
			t.Fatalf("%s passed with a missing settlement row", name)
		}
	}
}

func TestSandboxReconciliationProviderOutageRaisesAlertThenClears(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(50))
	h.mock.FailNext(beex.OpGetInvoice, beex.MockFailure{Err: beex.TimeoutError()})
	if res := h.reconcile(); res.ProviderReachable || res.Errors != 1 {
		t.Fatalf("during outage: %+v", res)
	}
	h.expectAlert(AlertProviderUnreachable)
	if res := h.reconcile(); !res.ProviderReachable {
		t.Fatalf("after outage: %+v", res)
	}
	if rows := h.alerts(AlertProviderUnreachable); len(rows) != 0 {
		t.Fatalf("outage alert still open: %+v", rows)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:50000000")
	h.expectReconciled()
}

func TestSandboxInvoiceExpiryAsksBeex(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(30))
	h.setDeposit(d.ID, bson.M{"expiresAt": time.Now().Add(-10 * time.Minute)})
	h.mock.SetInvoice(*d.InvoiceID, "expired", "", "")
	res, err := ExpireOverdueDeposits(h.ctx, time.Now())
	if err != nil || res.Deposits != 1 || res.Changed != 1 {
		t.Fatalf("expiry run: %+v %v", res, err)
	}
	if got := h.loadDeposit(d.ID); got.Status != models.InvoiceExpired || got.ExpiredLocallyAt != nil {
		t.Fatalf("deposit: %+v", got)
	}
	// Recently checked: not asked again straight away.
	if res, _ := ExpireOverdueDeposits(h.ctx, time.Now()); res.Deposits != 0 {
		t.Fatalf("re-checked at once: %+v", res)
	}
	h.expectLedger(uid)
}

func TestSandboxInvoiceExpiryDuringOutageThenLatePaymentStillCounts(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(30))
	h.setDeposit(d.ID, bson.M{"expiresAt": time.Now().Add(-7 * time.Hour)})
	h.mock.FailNext(beex.OpGetInvoice, beex.MockFailure{Err: beex.TimeoutError()})
	res, err := ExpireOverdueDeposits(h.ctx, time.Now())
	if err != nil || res.Changed != 1 || res.Errors != 1 {
		t.Fatalf("expiry during outage: %+v %v", res, err)
	}
	if got := h.loadDeposit(d.ID); got.Status != models.InvoiceExpired || got.ExpiredLocallyAt == nil {
		t.Fatalf("deposit: %+v", got)
	}
	h.deliver(invoiceEvent(d, "paid", "30", "0"))
	if got := h.loadDeposit(d.ID); !got.Credited {
		t.Fatalf("late payment after a local expiry: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:30000000")
	h.expectReconciled()
}

func TestSandboxPayoutPollingCompletesWithoutWebhook(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.approve(h.withdraw(uid, usdtAmt(100)).ID)
	h.mock.SetPayout(w.Reference, "completed", "0x03")
	// Too recent to poll yet.
	if res, _ := PollPayouts(h.ctx, time.Now()); res.Withdrawals != 0 {
		t.Fatalf("polled a fresh payout: %+v", res)
	}
	h.setWithdrawal(w.ID, bson.M{"updatedAt": time.Now().Add(-5 * time.Minute), "lastRefreshedAt": nil})
	res, err := PollPayouts(h.ctx, time.Now())
	if err != nil || res.Withdrawals != 1 || res.Changed != 1 {
		t.Fatalf("poll: %+v %v", res, err)
	}
	if res, _ := PollPayouts(h.ctx, time.Now().Add(10*time.Minute)); res.Withdrawals != 0 {
		t.Fatalf("polled a completed payout: %+v", res)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/DEBIT:100000000")
	h.expectReconciled()
}

func TestSandboxWorkerStopsWhenItsLeaseIsGone(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(90))
	h.mock.SetInvoice(*d.InvoiceID, "paid", "90", "0")
	stale := leader.WithFence(context.Background(), &leader.Fence{Name: "usdt-provider-reconcile", Key: "lock:test:" + uid,
		Token: "gone", LeaseUntil: time.Now().Add(-time.Second)})
	if _, err := ReconcileProvider(stale, time.Now()); !errors.Is(err, leader.ErrLeadershipLost) {
		t.Fatalf("stale leader reconciled: %v", err)
	}
	if _, err := ExpireOverdueDeposits(stale, time.Now()); !errors.Is(err, leader.ErrLeadershipLost) {
		t.Fatalf("stale leader expired invoices: %v", err)
	}
	if _, err := PollPayouts(stale, time.Now()); !errors.Is(err, leader.ErrLeadershipLost) {
		t.Fatalf("stale leader polled payouts: %v", err)
	}
	h.expectLedger(uid)
}

func TestSandboxSummaryShowsAlertsAndSandbox(t *testing.T) {
	h := newHarness(t)
	h.deliver(map[string]any{"event": "invoice.paid", "mode": "test", "reference": "IN-NOPE", "orderId": "dep-nope",
		"status": "paid", "amountReceived": "10"})
	s, err := GetSummary(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Sandbox || s.Alerts.Open != 1 || len(s.Alerts.Latest) != 1 || s.Alerts.Latest[0].Code != AlertWebhookUnmatched {
		t.Fatalf("summary: sandbox=%v alerts=%+v", s.Sandbox, s.Alerts)
	}
	if s.Provider.Name != "mock" || s.Provider.Mode != "test" || !s.Provider.Configured {
		t.Fatalf("provider: %+v", s.Provider)
	}
}
