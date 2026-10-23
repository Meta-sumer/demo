package usdt

import (
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/service/beex"
)

// Withdrawal sandbox flows: reserve with the request, operator review,
// payout with our reference as the idempotency key, provider outcome.

func (h *harness) approve(id bson.ObjectID) *models.UsdtWithdrawal {
	h.t.Helper()
	w, err := ApproveWithdrawal(h.ctx, id, h.user(), nil)
	if err != nil {
		h.t.Fatalf("approve: %v", err)
	}
	return w
}

func autoSubmit(e *config.Env) { e.UsdtWithdrawalReviewAbove = usdtAmt(1000) }

func TestSandboxWithdrawalApproveThenPaidSettlesOnce(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.withdraw(uid, usdtAmt(100))
	if w.Status != models.WithdrawalPendingReview || w.LockedAmount != usdtAmt(100) || deref(w.Mode) != "test" {
		t.Fatalf("requested withdrawal: %+v", w)
	}
	// Reserved with the request: not spendable while it waits.
	h.expectBalance(uid, usdtAmt(500), usdtAmt(100))
	if h.mock.PayoutCount() != 0 {
		t.Fatal("a reviewed withdrawal reached BeeX before approval")
	}
	w = h.approve(w.ID)
	if w.ProviderReference == nil || w.SubmissionPending || w.Status != models.WithdrawalRequested {
		t.Fatalf("approved withdrawal: %+v", w)
	}
	completed := payoutEvent(w, "completed", "0xabc123")
	completed["feeAmount"] = "4.5"
	if res := h.deliver(completed); res.ProcessingStatus != models.WebhookProcessed {
		t.Fatalf("completed webhook: %+v", res)
	}
	// BeeX retrying the same event, and a poll returning the same answer.
	h.mock.SetPayout(w.Reference, "completed", "0xabc123")
	h.deliver(completed)
	if _, _, err := RefreshWithdrawal(h.ctx, w.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	got := h.loadWithdrawal(w.ID)
	if got.Status != models.WithdrawalCompleted || got.FeeAmount != 4_500_000 || got.FeeIsEstimate || got.LockedAmount != 0 ||
		deref(got.TxHash) != "0xabc123" {
		t.Fatalf("completed withdrawal: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/DEBIT:100000000")
	h.expectBalance(uid, usdtAmt(400), 0)
	h.expectReconciled()
}

func TestSandboxWithdrawalFailedReturnsFundsOnce(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(300))
	w := h.approve(h.withdraw(uid, usdtAmt(200)).ID)
	failed := payoutEvent(w, "failed", "")
	h.deliver(failed)
	h.deliver(failed) // a retry of the same event
	h.mock.SetPayout(w.Reference, "failed", "")
	if _, _, err := RefreshWithdrawal(h.ctx, w.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := ReconcileProvider(h.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	got := h.loadWithdrawal(w.ID)
	if got.Status != models.WithdrawalFailed || got.LockedAmount != 0 || got.FailureReason == nil {
		t.Fatalf("failed withdrawal: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:300000000", "WITHDRAWAL/LOCK:200000000", "WITHDRAWAL/UNLOCK:200000000")
	h.expectBalance(uid, usdtAmt(300), 0)
	h.expectReconciled()
}

func TestSandboxWithdrawalRejectReturnsFundsOnce(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(300))
	w := h.withdraw(uid, usdtAmt(120))
	got, err := RejectWithdrawal(h.ctx, w.ID, h.user(), "Destination failed our checks")
	if err != nil || got.Status != models.WithdrawalRejected || deref(got.ReviewNote) != "Destination failed our checks" {
		t.Fatalf("reject: %+v %v", got, err)
	}
	if _, err := RejectWithdrawal(h.ctx, w.ID, h.user(), "again"); !hasCode(err, apperr.WithdrawalAlreadyProcessed) {
		t.Fatalf("second reject: %v", err)
	}
	if _, err := ApproveWithdrawal(h.ctx, w.ID, h.user(), nil); !hasCode(err, apperr.WithdrawalAlreadyProcessed) {
		t.Fatalf("approve after reject: %v", err)
	}
	if h.mock.PayoutCount() != 0 {
		t.Fatal("a rejected withdrawal reached BeeX")
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:300000000", "WITHDRAWAL/LOCK:120000000", "WITHDRAWAL/UNLOCK:120000000")
	h.expectBalance(uid, usdtAmt(300), 0)
	h.expectReconciled()
}

func TestSandboxConcurrentApprovalsSendOnePayout(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(1000))
	w := h.withdraw(uid, usdtAmt(100))
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = ApproveWithdrawal(h.ctx, w.ID, h.user(), nil)
		}(i)
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !hasCode(err, apperr.WithdrawalAlreadyProcessed):
			t.Fatalf("unexpected approval error: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d approvals succeeded, want 1", ok)
	}
	// Whatever the approvals left for the retry loop is sent now - once.
	for i := 0; i < 2; i++ {
		h.setWithdrawal(w.ID, bson.M{"nextSubmitAt": time.Now().Add(-time.Second)})
		if _, err := RetryPendingSubmissions(h.ctx, 20); err != nil {
			t.Fatal(err)
		}
	}
	if n := h.mock.PayoutCount(); n != 1 {
		t.Fatalf("%d payouts at BeeX, want 1", n)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:1000000000", "WITHDRAWAL/LOCK:100000000")
	h.expectReconciled()
}

func TestSandboxConcurrentApproveAndRejectOneWins(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(1000))
	w := h.withdraw(uid, usdtAmt(100))
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_, _ = ApproveWithdrawal(h.ctx, w.ID, h.user(), nil)
			} else {
				_, _ = RejectWithdrawal(h.ctx, w.ID, h.user(), "Rejected in the race")
			}
		}(i)
	}
	wg.Wait()
	h.setWithdrawal(w.ID, bson.M{"nextSubmitAt": time.Now().Add(-time.Second)})
	if _, err := RetryPendingSubmissions(h.ctx, 20); err != nil {
		t.Fatal(err)
	}
	got := h.loadWithdrawal(w.ID)
	switch got.Status {
	case models.WithdrawalRejected:
		h.expectLedger(uid, "DEPOSIT/CREDIT:1000000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/UNLOCK:100000000")
		if h.mock.PayoutCount() != 0 {
			t.Fatal("rejected yet sent")
		}
	case models.WithdrawalRequested:
		h.expectLedger(uid, "DEPOSIT/CREDIT:1000000000", "WITHDRAWAL/LOCK:100000000")
		if h.mock.PayoutCount() != 1 {
			t.Fatalf("approved: %d payouts", h.mock.PayoutCount())
		}
	default:
		t.Fatalf("status %s", got.Status)
	}
	h.expectReconciled()
}

func TestSandboxPayoutTimeoutAfterBeexAcceptedIsResentWithSameReference(t *testing.T) {
	h := newHarness(t, autoSubmit)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	// BeeX accepts the payout but the answer is lost.
	h.mock.FailNext(beex.OpCreatePayout, beex.MockFailure{Err: beex.TimeoutError(), AfterEffect: true})
	w := h.withdraw(uid, usdtAmt(100))
	if w.Status != models.WithdrawalRequested || !w.SubmissionPending || w.ProviderReference != nil {
		t.Fatalf("after timeout: %+v", w)
	}
	h.setWithdrawal(w.ID, bson.M{"nextSubmitAt": time.Now().Add(-time.Second)})
	if _, err := RetryPendingSubmissions(h.ctx, 20); err != nil {
		t.Fatal(err)
	}
	w = h.loadWithdrawal(w.ID)
	if w.SubmissionPending || w.ProviderReference == nil {
		t.Fatalf("after retry: %+v", w)
	}
	if h.mock.PayoutCount() != 1 || h.mock.Calls(beex.OpCreatePayout) != 2 {
		t.Fatalf("payouts=%d calls=%d", h.mock.PayoutCount(), h.mock.Calls(beex.OpCreatePayout))
	}
	h.deliver(payoutEvent(w, "completed", "TXHASH1"))
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/DEBIT:100000000")
	h.expectReconciled()
}

func TestSandboxPayoutOutageKeepsFundsReservedAndRetries(t *testing.T) {
	h := newHarness(t, autoSubmit)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	down := &beex.Error{Status: 503, Code: beex.CodeProviderError, Temporary: true}
	h.mock.FailNext(beex.OpCreatePayout, beex.MockFailure{Err: down})
	h.mock.FailNext(beex.OpCreatePayout, beex.MockFailure{Err: down})
	w := h.withdraw(uid, usdtAmt(150))
	for i := 0; i < 2; i++ {
		got := h.loadWithdrawal(w.ID)
		if got.Status != models.WithdrawalRequested || !got.SubmissionPending || got.LockedAmount != usdtAmt(150) {
			t.Fatalf("during outage: %+v", got)
		}
		h.expectBalance(uid, usdtAmt(500), usdtAmt(150))
		h.setWithdrawal(w.ID, bson.M{"nextSubmitAt": time.Now().Add(-time.Second)})
		if _, err := RetryPendingSubmissions(h.ctx, 20); err != nil {
			t.Fatal(err)
		}
	}
	got := h.loadWithdrawal(w.ID)
	if got.SubmissionPending || got.ProviderReference == nil || got.SubmitAttempts != 3 {
		t.Fatalf("after recovery: %+v", got)
	}
	if h.mock.PayoutCount() != 1 {
		t.Fatalf("%d payouts", h.mock.PayoutCount())
	}
	h.expectReconciled()
}

func TestSandboxPayoutDefiniteRefusalReturnsFundsOnce(t *testing.T) {
	h := newHarness(t, autoSubmit)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	h.mock.FailNext(beex.OpCreatePayout, beex.MockFailure{Err: beex.RejectError(400, beex.CodeInvalidAddress)})
	w := h.withdraw(uid, usdtAmt(100))
	if w.Status != models.WithdrawalFailed || w.LockedAmount != 0 {
		t.Fatalf("refused payout: %+v", w)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/UNLOCK:100000000")
	h.expectReconciled()
}

func TestSandboxPayoutRefusedAfterUnknownAttemptChecksBeexFirst(t *testing.T) {
	h := newHarness(t, autoSubmit)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	// Attempt 1 reached BeeX but timed out; attempt 2 is refused outright -
	// which must not be read as "no payout exists".
	h.mock.FailNext(beex.OpCreatePayout, beex.MockFailure{Err: beex.TimeoutError(), AfterEffect: true})
	h.mock.FailNext(beex.OpCreatePayout, beex.MockFailure{Err: beex.RejectError(400, "validation_error")})
	w := h.withdraw(uid, usdtAmt(100))
	h.setWithdrawal(w.ID, bson.M{"nextSubmitAt": time.Now().Add(-time.Second)})
	if _, err := RetryPendingSubmissions(h.ctx, 20); err != nil {
		t.Fatal(err)
	}
	got := h.loadWithdrawal(w.ID)
	if got.Status != models.WithdrawalRequested || got.ProviderReference == nil || got.SubmissionPending {
		t.Fatalf("withdrawal: %+v", got)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000")
	h.expectReconciled()
}

func TestSandboxApprovedButNotSubmittedIsRecoveredByRetryLoop(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.withdraw(uid, usdtAmt(100))
	// The approval committed and the process died before the payout call.
	h.setWithdrawal(w.ID, bson.M{"status": models.WithdrawalRequested, "submissionPending": true,
		"nextSubmitAt": time.Now().Add(-time.Second), "reviewedAt": time.Now()})
	for i := 0; i < 3; i++ {
		if _, err := RetryPendingSubmissions(h.ctx, 20); err != nil {
			t.Fatal(err)
		}
	}
	got := h.loadWithdrawal(w.ID)
	if got.ProviderReference == nil || got.SubmissionPending || h.mock.PayoutCount() != 1 {
		t.Fatalf("recovered: %+v payouts=%d", got, h.mock.PayoutCount())
	}
	h.expectReconciled()
}

func TestSandboxWithdrawalAddressIsValidatedPerNetwork(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	good := tronAddress("x")
	bad := []struct{ network, address string }{
		{"USDTTRC20", good[:33] + map[bool]string{true: "1", false: "2"}[good[33] != '1']}, // checksum
		{"USDTTRC20", bscAddress("x")},                                 // another chain's address
		{"USDTBSC", good},                                              // and the other way round
		{"USDTBSC", "0x0000000000000000000000000000000000000000"},      // zero address
		{"USDTBSC", "0x" + "AbCdEf0123456789abcdef0123456789ABCDEF01"}, // bad EIP-55 case
		{"USDTTRC20", "T123"},
	}
	for _, c := range bad {
		_, _, err := RequestWithdrawal(h.ctx, WithdrawalRequest{UserID: uid, Network: c.network, Address: c.address,
			Amount: usdtAmt(100), IdempotencyKey: key()})
		if !hasCode(err, apperr.InvalidAddress) {
			t.Fatalf("%s %s: %v", c.network, c.address, err)
		}
	}
	if _, _, err := RequestWithdrawal(h.ctx, WithdrawalRequest{UserID: uid, Network: "USDTBSC", Address: bscAddress("y"),
		Amount: usdtAmt(100), IdempotencyKey: key()}); err != nil {
		t.Fatalf("valid BSC address: %v", err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000")
}

func TestSandboxWithdrawalLimitsAndBalance(t *testing.T) {
	h := newHarness(t, func(e *config.Env) { e.UsdtUserDailyWithdrawalLimit = usdtAmt(150) })
	uid := h.user()
	h.fund(uid, usdtAmt(400))
	try := func(amount int64) error {
		_, _, err := RequestWithdrawal(h.ctx, WithdrawalRequest{UserID: uid, Network: "USDTTRC20", Address: tronAddress(uid),
			Amount: amount, IdempotencyKey: key()})
		return err
	}
	if err := try(usdtAmt(49)); !hasCode(err, apperr.MinWithdrawalNotMet) {
		t.Fatalf("below minimum: %v", err)
	}
	if err := try(usdtAmt(10001)); !hasCode(err, apperr.MaxWithdrawalExceeded) {
		t.Fatalf("above maximum: %v", err)
	}
	if err := try(usdtAmt(100)); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := try(usdtAmt(60)); !hasCode(err, apperr.DailyCapExceeded) {
		t.Fatalf("over the daily limit: %v", err)
	}
	q, err := GetQuote(h.ctx, uid, "USDTTRC20", usdtAmt(100))
	if err != nil || q.DailyUsed != usdtAmt(100) || q.DailyCap != usdtAmt(150) || q.FeeAmount != 4_500_000 ||
		string(q.FeePercent) != "0.5" || !q.NeedsReview || q.AvailableBalance != usdtAmt(300) {
		t.Fatalf("quote: %+v %v", q, err)
	}
	env().UsdtUserDailyWithdrawalLimit = 0
	if err := try(usdtAmt(350)); !hasCode(err, apperr.InsufficientBalance) {
		t.Fatalf("more than available: %v", err)
	}
	if _, _, err := UpdateNetwork(h.ctx, "USDTTRC20", NetworkPatch{WithdrawEnabled: ptr(false)}); err != nil {
		t.Fatal(err)
	}
	if err := try(usdtAmt(60)); !hasCode(err, apperr.InvalidNetwork) {
		t.Fatalf("paused network: %v", err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:400000000", "WITHDRAWAL/LOCK:100000000")
	h.expectReconciled()
}

func TestSandboxWithdrawalSameKeyIsOneWithdrawal(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(400))
	in := WithdrawalRequest{UserID: uid, Network: "USDTTRC20", Address: tronAddress(uid), Amount: usdtAmt(100), IdempotencyKey: key()}
	var wg sync.WaitGroup
	ids := make([]bson.ObjectID, 5)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if w, _, err := RequestWithdrawal(h.ctx, in); err == nil {
				ids[i] = w.ID
			}
		}(i)
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || id.IsZero() {
			t.Fatalf("withdrawal ids %v", ids)
		}
	}
	changed := in
	changed.Amount = usdtAmt(101)
	if _, _, err := RequestWithdrawal(h.ctx, changed); !hasCode(err, apperr.IdempotencyPayloadMismatch) {
		t.Fatalf("same key, other body: %v", err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:400000000", "WITHDRAWAL/LOCK:100000000")
	h.expectReconciled()
}

func TestSandboxLiveProviderOnlyPaysOutLiveDeposits(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(300)) // credited while the provider ran in test mode
	h.mock.SetMode("live")    // real keys are in place now
	if _, _, err := RequestWithdrawal(h.ctx, WithdrawalRequest{UserID: uid, Network: "USDTTRC20", Address: tronAddress(uid),
		Amount: usdtAmt(100), IdempotencyKey: key()}); !hasCode(err, apperr.InsufficientBalance) {
		t.Fatalf("sandbox money withdrawn live: %v", err)
	}
	live := h.fund(uid, usdtAmt(200))
	if deref(live.Mode) != "live" {
		t.Fatalf("deposit mode %v", deref(live.Mode))
	}
	w := h.withdraw(uid, usdtAmt(150))
	if deref(w.Mode) != "live" {
		t.Fatalf("withdrawal mode %v", deref(w.Mode))
	}
	if _, _, err := RequestWithdrawal(h.ctx, WithdrawalRequest{UserID: uid, Network: "USDTTRC20", Address: tronAddress(uid),
		Amount: usdtAmt(100), IdempotencyKey: key()}); !hasCode(err, apperr.InsufficientBalance) {
		t.Fatalf("over the live funds: %v", err)
	}
	q, err := GetQuote(h.ctx, uid, "USDTTRC20", 0)
	if err != nil || q.WithdrawableBalance != usdtAmt(50) || q.AvailableBalance != usdtAmt(350) {
		t.Fatalf("quote: %+v %v", q, err)
	}
	h.expectReconciled()
}

func TestSandboxPayoutAmountMismatchIsNotSettled(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.approve(h.withdraw(uid, usdtAmt(100)).ID)
	wrong := payoutEvent(w, "completed", "0xfeed")
	wrong["amount"] = "1000"
	if res := h.deliver(wrong); res.ProcessingStatus != models.WebhookIgnored {
		t.Fatalf("mismatched payout event: %+v", res)
	}
	got := h.loadWithdrawal(w.ID)
	if got.Status != models.WithdrawalRequested || got.LockedAmount != usdtAmt(100) {
		t.Fatalf("withdrawal after mismatch: %+v", got)
	}
	if a := h.expectAlert(AlertWebhookMismatch); a.Severity != models.AlertCritical || a.WithdrawalID == nil {
		t.Fatalf("alert: %+v", a)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000")
}

func TestSandboxPayoutCompletedAfterReleaseRaisesCriticalAlert(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.approve(h.withdraw(uid, usdtAmt(100)).ID)
	h.deliver(payoutEvent(w, "failed", ""))
	// BeeX contradicts itself: the payout it failed is reported completed.
	if res := h.deliver(payoutEvent(w, "completed", "0xdead")); res.ProcessingStatus != models.WebhookIgnored {
		t.Fatalf("conflicting event: %+v", res)
	}
	h.expectAlert(AlertWithdrawalCompletedAfter)
	// No debit for money we already returned: an operator has to act.
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/UNLOCK:100000000")
	h.expectBalance(uid, usdtAmt(500), 0)
}
