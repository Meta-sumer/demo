package usdt

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
)

// Deposit sandbox flows. Each ends by checking the exact ledger (one credit
// at most) and that the platform reconciliation passes.

func TestSandboxDepositPaidCreditsOnceAcrossDuplicateDeliveries(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(100))
	if d.Status != models.InvoiceAwaitingPayment || d.PaymentURL == nil || *d.PaymentURL != "https://beexpayments.com/pay/"+*d.InvoiceID {
		t.Fatalf("deposit after create: status=%s paymentUrl=%v", d.Status, d.PaymentURL)
	}
	h.mock.SetInvoice(*d.InvoiceID, "paid", "100", "1")
	body, _ := json.Marshal(invoiceEvent(d, "paid", "100", "1"))

	first, err := h.signed(body, time.Now())
	if err != nil || first.ProcessingStatus != models.WebhookProcessed || first.Duplicate {
		t.Fatalf("first delivery: %+v %v", first, err)
	}
	// BeeX retrying: the same event, signed again at a later time.
	retry, err := h.signed(body, time.Now().Add(time.Second))
	if err != nil || !retry.Duplicate || retry.Replay || retry.EventID != first.EventID {
		t.Fatalf("retry delivery: %+v %v", retry, err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:100000000")
	h.expectBalance(uid, usdtAmt(100), 0)
	var ev models.BeexWebhookEvent
	if err := eventsColl().FindOne(h.ctx, bson.M{"_id": mustOID(t, first.EventID)}).Decode(&ev); err != nil || ev.Deliveries != 2 {
		t.Fatalf("event deliveries = %d (%v), want 2", ev.Deliveries, err)
	}
	got := h.loadDeposit(d.ID)
	if !got.Credited || got.AmountCredited != usdtAmt(100) || got.Status != models.InvoicePaid || got.WalletTransactionID == nil {
		t.Fatalf("deposit after credit: %+v", got)
	}
	h.expectReconciled()
}

func TestSandboxDepositReplayedRequestIsRecognised(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(50))
	h.mock.SetInvoice(*d.InvoiceID, "paid", "50", "0")
	body, _ := json.Marshal(invoiceEvent(d, "paid", "50", "0"))
	header := http.Header{}
	header.Set(beex.SignatureHeader, beex.SignatureHeaderValue(testSecret, time.Now().Unix(), body))
	first, err := ReceiveWebhook(h.ctx, body, header, "203.0.113.7")
	if err != nil {
		t.Fatal(err)
	}
	// The identical signed request sent again (captured and replayed).
	replay, err := ReceiveWebhook(h.ctx, body, header, "198.51.100.9")
	if err != nil || !replay.Replay || !replay.Duplicate || replay.EventID != first.EventID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:50000000")
	h.expectAlert(AlertWebhookReplay)
	h.expectReconciled()
}

func TestSandboxDepositOverpaidCreditsWhatArrived(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(100))
	h.mock.SetInvoice(*d.InvoiceID, "overpaid", "120.5", "1")
	h.deliver(invoiceEvent(d, "overpaid", "120.5", "1"))
	got := h.loadDeposit(d.ID)
	if !got.Credited || got.Status != models.InvoiceOverpaid || got.AmountCredited != 120_500_000 {
		t.Fatalf("overpaid deposit: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:120500000")
	h.expectReconciled()
}

func TestSandboxDepositUnderpaidWaitsForAnOperator(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(100))
	h.mock.SetInvoice(*d.InvoiceID, "underpaid", "60", "0")
	h.deliver(invoiceEvent(d, "underpaid", "60", "0"))
	got := h.loadDeposit(d.ID)
	if got.Credited || got.Status != models.InvoiceUnderpaid || got.AmountReceived != usdtAmt(60) {
		t.Fatalf("underpaid deposit: %+v", got)
	}
	h.expectLedger(uid)

	admin := h.user()
	after, amount, err := AdminCreditDeposit(h.ctx, d.ID, admin, "Customer confirmed the short payment")
	if err != nil || amount != usdtAmt(60) || !after.Credited || after.ManualCredit == nil {
		t.Fatalf("manual credit: %+v %d %v", after, amount, err)
	}
	if _, _, err := AdminCreditDeposit(h.ctx, d.ID, admin, "again"); !hasCode(err, apperr.TransactionAlreadyProcessed) {
		t.Fatalf("second manual credit: %v", err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:60000000")
	h.expectReconciled()
}

func TestSandboxDepositPartialThenPaid(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(100))
	h.deliver(invoiceEvent(d, "partially_paid", "40", "0"))
	if w := h.wallet(uid); w.PendingDeposits != usdtAmt(40) || w.Balance != 0 {
		t.Fatalf("wallet while partially paid: %+v", w)
	}
	h.deliver(invoiceEvent(d, "paid", "100", "0"))
	// A late, out-of-order partial report changes nothing.
	h.deliver(invoiceEvent(d, "partially_paid", "70", "0"))
	got := h.loadDeposit(d.ID)
	if !got.Credited || got.Status != models.InvoicePaid || got.AmountCredited != usdtAmt(100) {
		t.Fatalf("deposit: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:100000000")
	h.expectReconciled()
}

func TestSandboxDepositExpiredThenLatePaymentStillCredits(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(25))
	h.deliver(invoiceEvent(d, "expired", "", ""))
	if got := h.loadDeposit(d.ID); got.Status != models.InvoiceExpired || got.Credited {
		t.Fatalf("expired deposit: %+v", got)
	}
	// The customer paid after the window closed; BeeX reports it as paid.
	h.deliver(invoiceEvent(d, "paid", "25", "0"))
	got := h.loadDeposit(d.ID)
	if !got.Credited || got.Status != models.InvoicePaid {
		t.Fatalf("late payment: %+v", got)
	}
	// A stale "expired" arriving after the credit cannot undo it.
	h.deliver(map[string]any{"event": "invoice.expired", "mode": "test", "reference": *d.Reference, "orderId": d.OrderID,
		"status": "expired", "amountReceived": "25.000000"})
	if got := h.loadDeposit(d.ID); !got.Credited || got.Status != models.InvoicePaid {
		t.Fatalf("after stale expired: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:25000000")
	h.expectReconciled()
}

func TestSandboxWebhookBadSignatureIsRefusedAndNothingStored(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(30))
	body, _ := json.Marshal(invoiceEvent(d, "paid", "30", "0"))
	cases := map[string]string{
		"wrong secret": beex.SignatureHeaderValue("not-the-secret-at-all", time.Now().Unix(), body),
		"missing":      "",
		"malformed":    "t=abc,v1=zz",
		"other body":   beex.SignatureHeaderValue(testSecret, time.Now().Unix(), []byte(`{"event":"invoice.paid"}`)),
	}
	for name, sig := range cases {
		header := http.Header{}
		if sig != "" {
			header.Set(beex.SignatureHeader, sig)
		}
		if _, err := ReceiveWebhook(h.ctx, body, header, "203.0.113.7"); !hasCode(err, apperr.Unauthenticated) {
			t.Fatalf("%s: err = %v, want UNAUTHENTICATED", name, err)
		}
	}
	// A tampered body under a genuine signature.
	tampered := []byte(string(body[:len(body)-1]) + `,"amountReceived":"3000"}`)
	header := http.Header{}
	header.Set(beex.SignatureHeader, beex.SignatureHeaderValue(testSecret, time.Now().Unix(), body))
	if _, err := ReceiveWebhook(h.ctx, tampered, header, "203.0.113.7"); !hasCode(err, apperr.Unauthenticated) {
		t.Fatalf("tampered body: %v", err)
	}
	if n, _ := eventsColl().CountDocuments(h.ctx, bson.M{}); n != 0 {
		t.Fatalf("%d events stored from refused deliveries", n)
	}
	h.expectLedger(uid)
}

func TestSandboxWebhookOldOrFutureTimestampIsRefused(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(30))
	body, _ := json.Marshal(invoiceEvent(d, "paid", "30", "0"))
	for _, ts := range []time.Time{time.Now().Add(-6 * time.Minute), time.Now().Add(6 * time.Minute)} {
		if _, err := h.signed(body, ts); !hasCode(err, apperr.Unauthenticated) {
			t.Fatalf("timestamp %v: %v", ts, err)
		}
	}
	if _, err := h.signed(body, time.Now().Add(-4*time.Minute)); err != nil {
		t.Fatalf("inside the tolerance: %v", err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:30000000")
}

func TestSandboxWebhookWithoutSecretIsUnavailable(t *testing.T) {
	h := newHarness(t)
	prev := env().BeexWebhookSecret
	env().BeexWebhookSecret = ""
	defer func() { env().BeexWebhookSecret = prev }()
	if _, err := h.signed([]byte(`{"event":"invoice.paid"}`), time.Now()); !hasCode(err, apperr.ServiceUnavailable) {
		t.Fatalf("no secret: %v", err)
	}
}

func TestSandboxWebhookForUnknownInvoiceIsStoredForReviewNeverCredited(t *testing.T) {
	h := newHarness(t)
	res := h.deliver(map[string]any{"event": "invoice.paid", "mode": "test", "reference": "IN2026-UNKNOWN",
		"orderId": "dep-" + bson.NewObjectID().Hex(), "status": "paid", "amountReceived": "500"})
	if res.ProcessingStatus != models.WebhookIgnored {
		t.Fatalf("unknown invoice: %+v", res)
	}
	payout := h.deliver(map[string]any{"event": "payout.completed", "reference": "OUT2026-UNKNOWN",
		"clientReference": "withdrawal-" + bson.NewObjectID().Hex(), "status": "completed", "amount": "75"})
	if payout.ProcessingStatus != models.WebhookIgnored {
		t.Fatalf("unknown payout: %+v", payout)
	}
	if n := len(h.alerts(AlertWebhookUnmatched)); n != 2 {
		t.Fatalf("%d unmatched alerts, want 2", n)
	}
	if n, _ := mongoCount(h, models.CollWalletTransactions); n != 0 {
		t.Fatalf("%d ledger rows from unknown events", n)
	}
}

func TestSandboxWebhookReferenceOrModeMismatchIsNotCredited(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(80))
	wrongRef := invoiceEvent(d, "paid", "80", "0")
	wrongRef["reference"] = "IN2026-SOMEONE-ELSE"
	if res := h.deliver(wrongRef); res.ProcessingStatus != models.WebhookIgnored {
		t.Fatalf("reference mismatch: %+v", res)
	}
	wrongMode := invoiceEvent(d, "paid", "80", "0")
	wrongMode["mode"] = "live"
	if res := h.deliver(wrongMode); res.ProcessingStatus != models.WebhookIgnored {
		t.Fatalf("mode mismatch: %+v", res)
	}
	if a := h.expectAlert(AlertWebhookMismatch); a.Severity != models.AlertCritical {
		t.Fatalf("mismatch alert severity %s", a.Severity)
	}
	h.expectLedger(uid)
	if got := h.loadDeposit(d.ID); got.Credited {
		t.Fatal("credited on a mismatched event")
	}
}

func TestSandboxDepositPaidWithoutAmountsAsksBeexFirst(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(40))
	h.mock.SetInvoice(*d.InvoiceID, "paid", "40", "0.4")
	// The event says "paid" but carries no figures: the full invoice is read.
	h.deliver(invoiceEvent(d, "paid", "", ""))
	got := h.loadDeposit(d.ID)
	if !got.Credited || got.AmountCredited != usdtAmt(40) || got.FeeAmount != 400_000 {
		t.Fatalf("deposit: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:40000000")
}

func TestSandboxDepositProviderTimeoutThenRetryReusesTheInvoice(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	// BeeX creates the invoice but the answer is lost.
	h.mock.FailNext(beex.OpCreateInvoice, beex.MockFailure{Err: beex.TimeoutError(), AfterEffect: true})
	k := key()
	_, err := CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTTRC20", Amount: usdtAmt(20), IdempotencyKey: k})
	if !hasCode(err, apperr.ProviderUnavailable) {
		t.Fatalf("timed out create: %v", err)
	}
	// The customer retries the same attempt (same key, same body).
	d, err := CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTTRC20", Amount: usdtAmt(20), IdempotencyKey: k})
	if err != nil || d.InvoiceID == nil {
		t.Fatalf("retry: %+v %v", d, err)
	}
	if n, _ := mongoCount(h, models.CollUsdtDeposits); n != 1 {
		t.Fatalf("%d deposits, want 1", n)
	}
	if calls := h.mock.Calls(beex.OpCreateInvoice); calls != 2 {
		t.Fatalf("CreateInvoice called %d times", calls)
	}
	// Same orderId, so BeeX answered with the invoice it already had.
	inv, err := h.mock.GetInvoice(h.ctx, d.OrderID)
	if err != nil || inv.ID != *d.InvoiceID {
		t.Fatalf("invoice %v / %v", inv, err)
	}
	// A different body under the same key is refused.
	if _, err := CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTTRC20", Amount: usdtAmt(21), IdempotencyKey: k}); !hasCode(err, apperr.IdempotencyPayloadMismatch) {
		t.Fatalf("payload mismatch: %v", err)
	}
}

func TestSandboxDepositProviderOutageThenRecovery(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.mock.FailNext(beex.OpCreateInvoice, beex.MockFailure{Err: &beex.Error{Status: 503, Code: beex.CodeProviderError, Temporary: true}})
	k := key()
	_, err := CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTBSC", Amount: usdtAmt(15), IdempotencyKey: k})
	if !hasCode(err, apperr.ProviderUnavailable) {
		t.Fatalf("outage: %v", err)
	}
	var stored models.UsdtDeposit
	if err := depositsColl().FindOne(h.ctx, bson.M{"idempotencyKey": k}).Decode(&stored); err != nil ||
		stored.Status != models.InvoiceCreated || stored.InvoiceID != nil {
		t.Fatalf("after outage: %+v %v", stored, err)
	}
	d, err := CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTBSC", Amount: usdtAmt(15), IdempotencyKey: k})
	if err != nil || d.ID != stored.ID || d.Status != models.InvoiceAwaitingPayment {
		t.Fatalf("after recovery: %+v %v", d, err)
	}
	// A definite refusal fails the deposit instead.
	h.mock.FailNext(beex.OpCreateInvoice, beex.MockFailure{Err: beex.RejectError(400, beex.CodeCurrencyNotEnabled)})
	_, err = CreateDeposit(h.ctx, DepositRequest{UserID: uid, Network: "USDTBSC", Amount: usdtAmt(15), IdempotencyKey: key()})
	if !hasCode(err, apperr.InvalidNetwork) {
		t.Fatalf("refused: %v", err)
	}
}

func TestSandboxConcurrentCreditPathsCreditOnce(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(70))
	h.mock.SetInvoice(*d.InvoiceID, "paid", "70", "0")
	body, _ := json.Marshal(invoiceEvent(d, "paid", "70", "0"))
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			switch i % 4 {
			case 0, 1:
				_, _ = h.signed(body, time.Now().Add(-time.Duration(i)*time.Second))
			case 2:
				_, _, _ = RefreshDeposit(h.ctx, d.ID, "admin")
			case 3:
				_, _ = ReconcileProvider(h.ctx, time.Now())
			}
		}(i)
	}
	wg.Wait()
	h.expectLedger(uid, "DEPOSIT/CREDIT:70000000")
	h.expectBalance(uid, usdtAmt(70), 0)
	h.expectReconciled()
}

func TestSandboxWebhookStoredButUnprocessedIsRecoveredByRetryLoop(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	d := h.deposit(uid, usdtAmt(45))
	// While processing is off the event is stored and parked - the same state
	// a process dying right after storing it leaves behind.
	env().UsdtEnabled = false
	res := h.deliver(invoiceEvent(d, "paid", "45", "0"))
	env().UsdtEnabled = true
	if res.ProcessingStatus != models.WebhookPending {
		t.Fatalf("parked event: %+v", res)
	}
	h.expectLedger(uid)
	if _, err := eventsColl().UpdateOne(h.ctx, bson.M{"_id": mustOID(t, res.EventID)},
		bson.M{"$set": bson.M{"nextAttemptAt": time.Now().Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := RetryWebhookEvents(h.ctx, 50); err != nil {
			t.Fatal(err)
		}
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:45000000")
	h.expectReconciled()
}

func TestSandboxDepositBelowNetworkMinimumIsRefused(t *testing.T) {
	h := newHarness(t)
	_, err := CreateDeposit(h.ctx, DepositRequest{UserID: h.user(), Network: "USDTTRC20", Amount: usdtAmt(9), IdempotencyKey: key()})
	if !hasCode(err, apperr.InvalidAmount) {
		t.Fatalf("below minimum: %v", err)
	}
	_, err = CreateDeposit(h.ctx, DepositRequest{UserID: h.user(), Network: "USDTERC20", Amount: usdtAmt(90), IdempotencyKey: key()})
	if !hasCode(err, apperr.InvalidNetwork) {
		t.Fatalf("unsupported network: %v", err)
	}
	if micro.Format(usdtAmt(9)) != "9" {
		t.Fatal("format")
	}
}

func mustOID(t *testing.T, hex string) bson.ObjectID {
	t.Helper()
	id, err := bson.ObjectIDFromHex(hex)
	if err != nil {
		t.Fatalf("bad id %q", hex)
	}
	return id
}

func mongoCount(h *harness, coll string) (int64, error) {
	return mongox.C(coll).CountDocuments(h.ctx, bson.M{})
}
