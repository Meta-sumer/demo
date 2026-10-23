package usdt

import (
	"sync"
	"testing"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/service/beex"
)

// mismatched sends a withdrawal to BeeX whose payout under our reference
// does not match it (BeeX holds 99 USDT for a 100 USDT withdrawal): the
// submission answer is refused, resubmission stops and the gross stays
// reserved - the state only an operator can finish.
func (h *harness) mismatched(uid string) *models.UsdtWithdrawal {
	h.t.Helper()
	w := h.withdraw(uid, usdtAmt(100))
	if _, err := h.mock.CreatePayout(h.ctx, beex.CreatePayoutRequest{Address: w.Address, Currency: w.Network,
		Amount: "99", Reference: w.Reference}); err != nil {
		h.t.Fatal(err)
	}
	w = h.approve(w.ID)
	if w.Status != models.WithdrawalRequested || w.SubmissionPending || w.ProviderMismatch == nil ||
		!models.WithdrawalOperatorResolvable(w) || w.LockedAmount != usdtAmt(100) {
		h.t.Fatalf("mismatched withdrawal %+v", w)
	}
	return w
}

// TestSandboxProviderMismatchResolvedByAnOperator: a withdrawal BeeX's
// answer did not match stays reserved and cannot be refreshed or rejected;
// an operator who checked it with BeeX marks it completed (settled once) or
// failed (released once). Repeats move nothing, the other outcome is refused,
// and the reconciliation no longer raises the mismatch once resolved.
func TestSandboxProviderMismatchResolvedByAnOperator(t *testing.T) {
	h := newHarness(t)
	uid, admin := h.user(), h.user()
	h.fund(uid, usdtAmt(500))
	w := h.mismatched(uid)
	h.expectAlert(AlertWithdrawalProviderMismatch)

	// The automatic paths cannot finish it.
	if res, _, err := RefreshWithdrawal(h.ctx, w.ID, "admin"); err != nil || res.Changed {
		t.Fatalf("refresh %+v %v", res, err)
	}
	if _, err := RejectWithdrawal(h.ctx, w.ID, admin, "not sent"); !hasCode(err, apperr.WithdrawalAlreadyProcessed) {
		t.Fatalf("reject %v", err)
	}
	// Only a mismatched withdrawal can be resolved by hand, with a note.
	other := h.withdraw(uid, usdtAmt(50))
	if _, _, err := ResolveWithdrawal(h.ctx, ResolveInput{ID: other.ID, AdminID: admin, Outcome: models.WithdrawalResolutionCompleted,
		Note: "checked"}); !hasCode(err, apperr.Conflict) {
		t.Fatalf("resolving a withdrawal under review: %v", err)
	}
	if _, _, err := ResolveWithdrawal(h.ctx, ResolveInput{ID: w.ID, AdminID: admin, Outcome: models.WithdrawalResolutionCompleted,
		Note: "  "}); !hasCode(err, apperr.ValidationError) {
		t.Fatalf("without a note: %v", err)
	}

	// Marked completed: the gross leaves once.
	in := ResolveInput{ID: w.ID, AdminID: admin, Outcome: models.WithdrawalResolutionCompleted,
		Note: "BeeX support confirmed the payout was sent for the full amount", TxHash: "0xabc123"}
	got, changed, err := ResolveWithdrawal(h.ctx, in)
	if err != nil || !changed || got.Status != models.WithdrawalCompleted || got.LockedAmount != 0 ||
		deref(got.Resolution) != models.WithdrawalResolutionCompleted || got.ResolvedBy == nil || deref(got.TxHash) != "0xabc123" {
		t.Fatalf("resolved %+v %v %v", got, changed, err)
	}
	if again, changed, err := ResolveWithdrawal(h.ctx, in); err != nil || changed || again.Status != models.WithdrawalCompleted {
		t.Fatalf("repeat %+v %v %v", again, changed, err)
	}
	in.Outcome = models.WithdrawalResolutionFailed
	if _, _, err := ResolveWithdrawal(h.ctx, in); !hasCode(err, apperr.WithdrawalAlreadyProcessed) {
		t.Fatalf("the other outcome after completion: %v", err)
	}
	h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/LOCK:50000000",
		"WITHDRAWAL/DEBIT:100000000")
	h.expectBalance(uid, usdtAmt(400), usdtAmt(50))

	// Resolved: provider reconciliation does not raise the mismatch again.
	for _, a := range h.alerts(AlertWithdrawalProviderMismatch) {
		if _, err := ResolveAlert(h.ctx, a.ID, admin, "settled by hand after checking BeeX"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReconcileProvider(h.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, a := range h.alerts(AlertWithdrawalProviderMismatch) {
		if a.WithdrawalID != nil && *a.WithdrawalID == w.ID {
			t.Fatalf("mismatch alert raised again after the resolution: %+v", a)
		}
	}
	if _, err := RejectWithdrawal(h.ctx, other.ID, admin, "not needed"); err != nil {
		t.Fatal(err)
	}
	h.expectReconciled()
}

// TestSandboxProviderMismatchConcurrentResolutionsMoveMoneyOnce: operators
// racing to mark one mismatched withdrawal completed and failed produce
// exactly one ledger effect, and the withdrawal ends in that state.
func TestSandboxProviderMismatchConcurrentResolutionsMoveMoneyOnce(t *testing.T) {
	h := newHarness(t)
	uid := h.user()
	h.fund(uid, usdtAmt(500))
	w := h.mismatched(uid)

	var wg sync.WaitGroup
	var mu sync.Mutex
	applied := 0
	for i := 0; i < 8; i++ {
		outcome := models.WithdrawalResolutionFailed
		if i%2 == 0 {
			outcome = models.WithdrawalResolutionCompleted
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, changed, err := ResolveWithdrawal(h.ctx, ResolveInput{ID: w.ID, AdminID: h.user(), Outcome: outcome, Note: "checked with BeeX"})
			if err != nil && !hasCode(err, apperr.WithdrawalAlreadyProcessed) {
				t.Errorf("resolve: %v", err)
			}
			if changed {
				mu.Lock()
				applied++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if applied != 1 {
		t.Fatalf("%d resolutions applied, want 1", applied)
	}
	got := h.loadWithdrawal(w.ID)
	switch got.Status {
	case models.WithdrawalCompleted:
		h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/DEBIT:100000000")
		h.expectBalance(uid, usdtAmt(400), 0)
	case models.WithdrawalFailed:
		h.expectLedger(uid, "DEPOSIT/CREDIT:500000000", "WITHDRAWAL/LOCK:100000000", "WITHDRAWAL/UNLOCK:100000000")
		h.expectBalance(uid, usdtAmt(500), 0)
	default:
		t.Fatalf("status %s", got.Status)
	}
}
