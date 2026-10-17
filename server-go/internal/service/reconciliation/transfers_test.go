package reconciliation

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/testsupport"
)

type fixture struct {
	t   *testing.T
	ctx context.Context
	uid bson.ObjectID
}

func newFixture(t *testing.T) *fixture {
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	return &fixture{t: t, ctx: context.Background(), uid: bson.NewObjectID()}
}

func (f *fixture) transfer(kind models.TransferKind, status models.TransferStatus, amount int64, mod ...func(*models.Transfer)) *models.Transfer {
	f.t.Helper()
	now := mongox.Now()
	tr := &models.Transfer{ID: bson.NewObjectID(), Reference: "T_" + bson.NewObjectID().Hex(), Kind: kind, UserID: f.uid,
		Amount: amount, Currency: "INR", Status: status, CreatedAt: now, UpdatedAt: now}
	for _, m := range mod {
		m(tr)
	}
	if _, err := mongox.C(models.CollTransfers).InsertOne(f.ctx, tr); err != nil {
		f.t.Fatal(err)
	}
	return tr
}

func (f *fixture) ledger(transferID, key string, movement models.MoneyMovement, amount int64, mod ...func(*models.WalletTransaction)) *models.WalletTransaction {
	f.t.Helper()
	typ := models.WalletTxWithdrawal
	if strings.HasPrefix(key, "deposit:") {
		typ = models.WalletTxDeposit
	}
	r := &models.WalletTransaction{ID: bson.NewObjectID(), WalletID: bson.NewObjectID(), UserID: f.uid, AccountType: models.AccountLive,
		Type: typ, Movement: movement, Amount: amount, Currency: "INR",
		ReferenceType: models.Str(models.TransferLedgerReference), ReferenceID: models.Str(transferID),
		IdempotencyKey: models.Str(key), Metadata: map[string]any{}, CreatedAt: mongox.Now().Add(-10 * time.Minute)}
	for _, m := range mod {
		m(r)
	}
	if _, err := mongox.C(models.CollWalletTransactions).InsertOne(f.ctx, r); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func problems(c models.ReconciliationCheck) []string {
	out := []string{}
	if ms, ok := c.Details["mismatches"].([]map[string]any); ok {
		for _, m := range ms {
			out = append(out, m["problem"].(string))
		}
	}
	return out
}

// A consistent set of transfers in every state passes.
func TestTransfersAgainstLedgerPassesWhenConsistent(t *testing.T) {
	f := newFixture(t)
	d := f.transfer(models.TransferKindDeposit, models.TransferStatusCompleted, 1_000_00)
	credit := f.ledger(d.ID.Hex(), models.DepositCreditKey(d.ID.Hex()), models.MovementCredit, 1_000_00)
	if _, err := mongox.C(models.CollTransfers).UpdateOne(f.ctx, bson.M{"_id": d.ID}, bson.M{"$set": bson.M{"walletTransactionId": credit.ID}}); err != nil {
		t.Fatal(err)
	}
	f.transfer(models.TransferKindDeposit, models.TransferStatusPending, 500_00)
	f.transfer(models.TransferKindDeposit, models.TransferStatusRejected, 500_00)
	// An approval in progress, credited or not yet.
	a := f.transfer(models.TransferKindDeposit, models.TransferStatusApproved, 300_00)
	f.ledger(a.ID.Hex(), models.DepositCreditKey(a.ID.Hex()), models.MovementCredit, 300_00)
	f.transfer(models.TransferKindDeposit, models.TransferStatusApproved, 300_00)

	p := f.transfer(models.TransferKindWithdrawal, models.TransferStatusPending, 200_00)
	f.ledger(p.ID.Hex(), models.WithdrawalLockKey(p.ID.Hex()), models.MovementLock, 200_00)
	c := f.transfer(models.TransferKindWithdrawal, models.TransferStatusCompleted, 400_00)
	f.ledger(c.ID.Hex(), models.WithdrawalLockKey(c.ID.Hex()), models.MovementLock, 400_00)
	f.ledger(c.ID.Hex(), models.WithdrawalSettleKey(c.ID.Hex()), models.MovementDebit, 400_00)
	r := f.transfer(models.TransferKindWithdrawal, models.TransferStatusRejected, 100_00)
	f.ledger(r.ID.Hex(), models.WithdrawalLockKey(r.ID.Hex()), models.MovementLock, 100_00)
	f.ledger(r.ID.Hex(), models.WithdrawalUnlockKey(r.ID.Hex()), models.MovementUnlock, 100_00)
	// Legacy: a request that could not reserve was marked FAILED.
	f.transfer(models.TransferKindWithdrawal, models.TransferStatusFailed, 100_00)
	// A request whose transfer was never written, compensated: a balanced pair.
	ghost := bson.NewObjectID().Hex()
	f.ledger(ghost, models.WithdrawalLockKey(ghost), models.MovementLock, 50_00)
	f.ledger(ghost, models.WithdrawalUnlockKey(ghost), models.MovementUnlock, 50_00)

	check, err := transfersAgainstLedger(f.ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed || check.Actual != 0 {
		t.Fatalf("consistent data flagged: %v", problems(check))
	}
	if check.Details["depositsCompletedTotal"] != int64(1_000_00) || check.Details["depositCreditsTotal"] != int64(1_000_00) ||
		check.Details["withdrawalsCompletedTotal"] != int64(400_00) || check.Details["withdrawalSettlesTotal"] != int64(400_00) {
		t.Fatalf("totals %v", check.Details)
	}
}

// Every way a transfer and its ledger rows can disagree is reported.
func TestTransfersAgainstLedgerFlagsMismatches(t *testing.T) {
	f := newFixture(t)
	want := map[string]string{}

	// COMPLETED deposit that was never credited.
	d1 := f.transfer(models.TransferKindDeposit, models.TransferStatusCompleted, 1_000_00)
	want[d1.ID.Hex()] = "missing ledger row deposit:credit:"
	// A deposit credited although it is still PENDING.
	d2 := f.transfer(models.TransferKindDeposit, models.TransferStatusPending, 500_00)
	f.ledger(d2.ID.Hex(), models.DepositCreditKey(d2.ID.Hex()), models.MovementCredit, 500_00)
	want[d2.ID.Hex()] = "unexpected ledger row deposit:credit:"
	// Credited for the wrong amount.
	d3 := f.transfer(models.TransferKindDeposit, models.TransferStatusCompleted, 700_00)
	f.ledger(d3.ID.Hex(), models.DepositCreditKey(d3.ID.Hex()), models.MovementCredit, 70_00)
	want[d3.ID.Hex()] = "does not match the transfer"
	// A PENDING withdrawal with no reservation (the old two-step request).
	w1 := f.transfer(models.TransferKindWithdrawal, models.TransferStatusPending, 200_00)
	want[w1.ID.Hex()] = "missing ledger row withdrawal:lock:"
	// A paid withdrawal whose reservation was also released.
	w2 := f.transfer(models.TransferKindWithdrawal, models.TransferStatusCompleted, 300_00)
	f.ledger(w2.ID.Hex(), models.WithdrawalLockKey(w2.ID.Hex()), models.MovementLock, 300_00)
	f.ledger(w2.ID.Hex(), models.WithdrawalSettleKey(w2.ID.Hex()), models.MovementDebit, 300_00)
	f.ledger(w2.ID.Hex(), models.WithdrawalUnlockKey(w2.ID.Hex()), models.MovementUnlock, 300_00)
	want[w2.ID.Hex()] = "unexpected ledger row withdrawal:unlock:"
	// A rejected withdrawal still holding its reservation.
	w3 := f.transfer(models.TransferKindWithdrawal, models.TransferStatusRejected, 100_00)
	f.ledger(w3.ID.Hex(), models.WithdrawalLockKey(w3.ID.Hex()), models.MovementLock, 100_00)
	want[w3.ID.Hex()] = "missing ledger row withdrawal:unlock:"
	// A reservation for a transfer that does not exist, never released.
	ghost := bson.NewObjectID().Hex()
	f.ledger(ghost, models.WithdrawalLockKey(ghost), models.MovementLock, 50_00)
	want[ghost] = "references a transfer that does not exist"
	// A row of another customer's under this transfer's key.
	d4 := f.transfer(models.TransferKindDeposit, models.TransferStatusCompleted, 900_00)
	f.ledger(d4.ID.Hex(), models.DepositCreditKey(d4.ID.Hex()), models.MovementCredit, 900_00,
		func(r *models.WalletTransaction) { r.UserID = bson.NewObjectID() })
	want[d4.ID.Hex()] = "does not match the transfer"
	// A fresh orphan (a request in flight) is not reported yet.
	fresh := bson.NewObjectID().Hex()
	f.ledger(fresh, models.WithdrawalLockKey(fresh), models.MovementLock, 10_00,
		func(r *models.WalletTransaction) { r.CreatedAt = mongox.Now() })

	check, err := transfersAgainstLedger(f.ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if check.Passed {
		t.Fatal("mismatches not reported")
	}
	got := map[string][]string{}
	for _, m := range check.Details["mismatches"].([]map[string]any) {
		id := m["transferId"].(string)
		got[id] = append(got[id], m["problem"].(string))
	}
	for id, problem := range want {
		found := false
		for _, p := range got[id] {
			if strings.Contains(p, problem) {
				found = true
			}
		}
		if !found {
			t.Errorf("transfer %s: problems %v, want one containing %q", id, got[id], problem)
		}
	}
	if _, ok := got[fresh]; ok {
		t.Errorf("an in-flight reservation was reported: %v", got[fresh])
	}
	if check.Actual != int64(len(check.Details["mismatches"].([]map[string]any))) {
		t.Errorf("actual %d vs %d mismatches", check.Actual, len(got))
	}
}

func TestStuckApprovedTransfersAreReported(t *testing.T) {
	f := newFixture(t)
	old := mongox.Now().Add(-10 * time.Minute)
	stuck := f.transfer(models.TransferKindDeposit, models.TransferStatusApproved, 1_000_00, func(tr *models.Transfer) {
		tr.ReviewedAt = &old
		tr.LastError = models.Str("SERVICE_UNAVAILABLE: Your account is busy")
		tr.CompletionAttempts = 2
	})
	// Legacy claim with no reviewedAt, stale by updatedAt.
	f.transfer(models.TransferKindWithdrawal, models.TransferStatusApproved, 500_00, func(tr *models.Transfer) { tr.UpdatedAt = old })
	// In progress: not stuck yet.
	now := mongox.Now()
	f.transfer(models.TransferKindDeposit, models.TransferStatusApproved, 200_00, func(tr *models.Transfer) { tr.ReviewedAt = &now })

	check, err := stuckApprovedTransfers(f.ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if check.Passed || check.Actual != 2 {
		t.Fatalf("check = %+v", check)
	}
	found := false
	for _, it := range check.Details["transfers"].([]map[string]any) {
		if it["transferId"] == stuck.ID.Hex() {
			found = true
			if le, _ := it["lastError"].(*string); le == nil || *le == "" || it["completionAttempts"] != int64(2) {
				t.Fatalf("item = %v", it)
			}
		}
	}
	if !found {
		t.Fatal("the stuck approval is not listed")
	}
}

// An APPROVED withdrawal still holds its reservation: that is expected, not
// a lock mismatch.
func TestLockedFundsCountApprovedWithdrawals(t *testing.T) {
	f := newFixture(t)
	locked := int64(300_00)
	f.transfer(models.TransferKindWithdrawal, models.TransferStatusApproved, locked, func(tr *models.Transfer) { tr.LockedAmount = &locked })
	f.transfer(models.TransferKindWithdrawal, models.TransferStatusPending, 200_00, func(tr *models.Transfer) {
		l := int64(200_00)
		tr.LockedAmount = &l
	})
	w := models.Wallet{ID: bson.NewObjectID(), UserID: f.uid, AccountType: models.AccountLive, Currency: "INR",
		Balance: 1_000_00, LockedBalance: 500_00, CreatedAt: mongox.Now(), UpdatedAt: mongox.Now()}
	if _, err := mongox.C(models.CollWallets).InsertOne(f.ctx, w); err != nil {
		t.Fatal(err)
	}
	check, err := checkLockedFundsAgainstOpenPositions(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !check.Passed || check.Expected != 500_00 {
		t.Fatalf("check = %+v", check)
	}
}
