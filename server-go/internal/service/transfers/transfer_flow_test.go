package transfers

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/service/wallets"
	"binarytrading/internal/testsupport"
)

// ── harness ─────────────────────────────────────────────────────────────

func liveStores(t *testing.T, transactions bool) {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if transactions {
		uri := os.Getenv("MONGO_URI")
		if uri == "" {
			t.Skip("MONGO_URI not exported: cannot reconnect with transactions")
		}
		name := mongox.DB().Name()
		_ = mongox.Disconnect(ctx)
		if err := mongox.Connect(ctx, withDatabase(uri, name), true); err != nil {
			t.Fatal(err)
		}
		if !mongox.TransactionsAvailable() {
			t.Skip("MongoDB does not support transactions here (not a replica set)")
		}
	}
	models.EnsureIndexes(ctx)
	t.Cleanup(func() { testHook = nil })
}

func withDatabase(uri, name string) string {
	scheme := "mongodb://"
	if strings.HasPrefix(uri, "mongodb+srv://") {
		scheme = "mongodb+srv://"
	}
	rest, query := strings.TrimPrefix(uri, scheme), ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return scheme + rest + "/" + name + query
}

func forModes(t *testing.T, fn func(t *testing.T, tx bool)) {
	for _, tx := range []bool{false, true} {
		name := "without_transactions"
		if tx {
			name = "with_transactions"
		}
		t.Run(name, func(t *testing.T) {
			liveStores(t, tx)
			fn(t, tx)
		})
	}
}

var errInjected = errors.New("injected failure")

func failAt(points ...string) func(string) error {
	return func(p string) error {
		for _, want := range points {
			if p == want {
				return errInjected
			}
		}
		return nil
	}
}

// customer creates a LIVE wallet holding balance.
func customer(t *testing.T, balance int64) string {
	t.Helper()
	ctx := context.Background()
	uid := bson.NewObjectID().Hex()
	if _, err := wallets.GetOrCreateWallet(ctx, uid, models.AccountLive, ""); err != nil {
		t.Fatal(err)
	}
	if balance > 0 {
		if _, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{UserID: uid, Amount: balance,
			Type: models.WalletTxAdjustmentCredit, IdempotencyKey: "seed:" + uid}); err != nil {
			t.Fatal(err)
		}
	}
	return uid
}

func operator() string { return bson.NewObjectID().Hex() }

func wallet(t *testing.T, uid string) models.WalletView {
	t.Helper()
	w, err := wallets.GetWallet(context.Background(), uid, models.AccountLive, "")
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func transfer(t *testing.T, id string) *models.Transfer {
	t.Helper()
	tr, err := GetTransfer(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func row(t *testing.T, key string) *models.WalletTransaction {
	t.Helper()
	r, err := wallets.LedgerRowByKey(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func transferRowCount(t *testing.T, transferID string) int64 {
	t.Helper()
	n, err := mongox.C(models.CollWalletTransactions).CountDocuments(context.Background(),
		bson.M{"referenceType": models.TransferLedgerReference, "referenceId": transferID})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func deposit(t *testing.T, uid string, amount int64) models.TransferView {
	t.Helper()
	utr := "UTR" + bson.NewObjectID().Hex()[12:]
	v, err := RequestDeposit(context.Background(), DepositRequestInput{UserID: uid, Amount: amount, Utr: &utr})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func withdrawal(t *testing.T, uid string, amount int64) (models.TransferView, error) {
	t.Helper()
	upi := "someone@bank"
	return RequestWithdrawal(context.Background(), WithdrawalRequestInput{UserID: uid, Amount: amount,
		Destination: models.PayoutDestination{Type: models.PaymentMethodUPI, UpiID: &upi}})
}

// reconcile runs the real reconciliation and fails on any alert.
func reconcile(t *testing.T) {
	t.Helper()
	res, err := reconciliation.RunReconciliation(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.ReconciliationOK {
		t.Fatalf("reconciliation %s: %v", res.Status, res.Alerts)
	}
}

// ── deposits ────────────────────────────────────────────────────────────

func TestDepositWithoutMethodIsHeldToDefaultLimits(t *testing.T) {
	liveStores(t, false)
	uid := customer(t, 0)
	for _, amount := range []int64{models.PaymentMethodDefaultMinAmount - 1, models.PaymentMethodDefaultMaxAmount + 1} {
		utr := "UTR" + bson.NewObjectID().Hex()[12:]
		if _, err := RequestDeposit(context.Background(), DepositRequestInput{UserID: uid, Amount: amount, Utr: &utr}); code(err) != apperr.ValidationError {
			t.Fatalf("amount %d: err = %v", amount, err)
		}
	}
	for _, amount := range []int64{models.PaymentMethodDefaultMinAmount, models.PaymentMethodDefaultMaxAmount} {
		deposit(t, uid, amount)
	}
}

// Many operators approving one deposit at once: exactly one credit.
func TestConcurrentDepositApprovalsCreditOnce(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		uid := customer(t, 0)
		d := deposit(t, uid, 1_000_00)
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = ApproveDeposit(context.Background(), d.ID, operator(), nil)
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case code(err) != apperr.Conflict:
				t.Fatalf("unexpected error %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d approvals succeeded, want 1", ok)
		}
		tr := transfer(t, d.ID)
		credit := row(t, models.DepositCreditKey(d.ID))
		if tr.Status != models.TransferStatusCompleted || credit == nil || tr.WalletTransactionID == nil || *tr.WalletTransactionID != credit.ID {
			t.Fatalf("transfer %s, credit %v", tr.Status, credit)
		}
		if n := transferRowCount(t, d.ID); n != 1 {
			t.Fatalf("%d ledger rows for the deposit", n)
		}
		if w := wallet(t, uid); w.Balance != 1_000_00 {
			t.Fatalf("balance = %d", w.Balance)
		}
		// A late duplicate approval is refused and moves nothing.
		if _, err := ApproveDeposit(context.Background(), d.ID, operator(), nil); code(err) != apperr.Conflict ||
			!strings.Contains(err.Error(), "already COMPLETED") {
			t.Fatalf("duplicate approval err = %v", err)
		}
		if w := wallet(t, uid); w.Balance != 1_000_00 {
			t.Fatalf("balance after duplicate = %d", w.Balance)
		}
		reconcile(t)
	})
}

// The wallet credit fails after the approval was made. With transactions the
// approval rolls back with it (still PENDING); without, it stays APPROVED - a
// durable, visible state - and completing it credits exactly once.
func TestDepositWalletFailureAfterApprovalRecovers(t *testing.T) {
	forModes(t, func(t *testing.T, tx bool) {
		ctx := context.Background()
		uid := customer(t, 0)
		d := deposit(t, uid, 2_000_00)
		testHook = failAt("approve.inTx")
		if _, err := ApproveDeposit(ctx, d.ID, operator(), nil); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v", err)
		}
		testHook = nil
		tr := transfer(t, d.ID)
		if tr.LastError == nil || tr.CompletionAttempts != 1 {
			t.Fatalf("failure not recorded: %+v", tr)
		}
		if tx {
			if tr.Status != models.TransferStatusPending || row(t, models.DepositCreditKey(d.ID)) != nil || wallet(t, uid).Balance != 0 {
				t.Fatalf("not atomic: status %s balance %d", tr.Status, wallet(t, uid).Balance)
			}
			if _, err := ApproveDeposit(ctx, d.ID, operator(), nil); err != nil {
				t.Fatal(err)
			}
		} else {
			// The credit landed but the record did not: the approval stays
			// APPROVED and cannot be rejected, only completed.
			if tr.Status != models.TransferStatusApproved || row(t, models.DepositCreditKey(d.ID)) == nil {
				t.Fatalf("status %s", tr.Status)
			}
			if _, err := RejectTransfer(ctx, d.ID, operator(), "changed my mind"); code(err) != apperr.Conflict {
				t.Fatalf("reject of a credited approval: %v", err)
			}
			res, err := RepairTransfers(ctx, operator(), time.Nanosecond)
			if err != nil || len(res.Completed) != 1 || len(res.Failed) != 0 {
				t.Fatalf("repair = %+v, %v", res, err)
			}
		}
		tr = transfer(t, d.ID)
		credit := row(t, models.DepositCreditKey(d.ID))
		if tr.Status != models.TransferStatusCompleted || *tr.WalletTransactionID != credit.ID {
			t.Fatalf("status %s", tr.Status)
		}
		if n := transferRowCount(t, d.ID); n != 1 {
			t.Fatalf("%d ledger rows", n)
		}
		if w := wallet(t, uid); w.Balance != 2_000_00 {
			t.Fatalf("balance = %d", w.Balance)
		}
		reconcile(t)
	})
}

// Without transactions, a failure between recording the approval and the
// credit leaves APPROVED with nothing moved: reconciliation reports it, the
// operator can complete it (credits once) or, since no money moved, reject it.
func TestApprovedDepositWithoutCreditIsReportedAndCanBeCompletedOrRejected(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := customer(t, 0)
	complete, reject := deposit(t, uid, 1_500_00), deposit(t, uid, 700_00)
	testHook = failAt("approve.claimed")
	for _, id := range []string{complete.ID, reject.ID} {
		if _, err := ApproveDeposit(ctx, id, operator(), nil); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v", err)
		}
	}
	testHook = nil
	for _, id := range []string{complete.ID, reject.ID} {
		if s := transfer(t, id).Status; s != models.TransferStatusApproved {
			t.Fatalf("status %s", s)
		}
	}
	// Age the approvals past the reconciliation threshold.
	old := time.Now().Add(-10 * time.Minute)
	if _, err := mongox.C(models.CollTransfers).UpdateMany(ctx, bson.M{"userId": mustOID(uid)},
		bson.M{"$set": bson.M{"reviewedAt": old, "updatedAt": old}}); err != nil {
		t.Fatal(err)
	}
	res, err := reconciliation.RunReconciliation(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.ReconciliationAlert || !hasFailedCheck(res, "transfers_stuck_approved") {
		t.Fatalf("stuck approvals not reported: %s %v", res.Status, res.Alerts)
	}

	if _, err := CompleteTransfer(ctx, complete.ID, operator(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := CompleteTransfer(ctx, complete.ID, operator(), nil); code(err) != apperr.Conflict {
		t.Fatalf("second complete: %v", err)
	}
	rv, err := RejectTransfer(ctx, reject.ID, operator(), "payment never arrived")
	if err != nil || rv.Status != models.TransferStatusRejected {
		t.Fatalf("reject = %+v, %v", rv, err)
	}
	if row(t, models.DepositCreditKey(reject.ID)) != nil {
		t.Fatal("a rejected deposit was credited")
	}
	if w := wallet(t, uid); w.Balance != 1_500_00 {
		t.Fatalf("balance = %d", w.Balance)
	}
	reconcile(t)
}

// Approving again is also a retry path for a stuck approval.
func TestApproveAgainCompletesAStuckApproval(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := customer(t, 0)
	d := deposit(t, uid, 900_00)
	testHook = failAt("approve.claimed")
	_, _ = ApproveDeposit(ctx, d.ID, operator(), nil)
	testHook = nil
	v, err := ApproveDeposit(ctx, d.ID, operator(), nil)
	if err != nil || v.Status != models.TransferStatusCompleted {
		t.Fatalf("retry = %+v, %v", v, err)
	}
	if w := wallet(t, uid); w.Balance != 900_00 {
		t.Fatalf("balance = %d", w.Balance)
	}
}

// ── withdrawals ─────────────────────────────────────────────────────────

func TestWithdrawalRequestReservesWithTheTransfer(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		uid := customer(t, 10_000_00)
		v, err := withdrawal(t, uid, 3_000_00)
		if err != nil {
			t.Fatal(err)
		}
		tr := transfer(t, v.ID)
		lock := row(t, models.WithdrawalLockKey(v.ID))
		if tr.Status != models.TransferStatusPending || lock == nil || lock.Amount != 3_000_00 || *tr.LockedAmount != 3_000_00 {
			t.Fatalf("transfer %+v lock %+v", tr, lock)
		}
		if w := wallet(t, uid); w.LockedBalance != 3_000_00 || w.Balance != 10_000_00 {
			t.Fatalf("wallet %+v", w)
		}
		reconcile(t)
	})
}

func userTransferCount(t *testing.T, uid string) int64 {
	t.Helper()
	n, err := mongox.C(models.CollTransfers).CountDocuments(context.Background(), bson.M{"userId": mustOID(uid)})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func mustOID(hex string) bson.ObjectID {
	id, _ := mongox.ParseID(hex)
	return id
}

// The transfer cannot be written: no transfer exists, and no reservation is
// left behind - rolled back with transactions, released without.
func TestWithdrawalReservationFailureLeavesNothingBehind(t *testing.T) {
	forModes(t, func(t *testing.T, tx bool) {
		uid := customer(t, 10_000_00)
		testHook = failAt("withdrawal.request.inTx")
		if _, err := withdrawal(t, uid, 3_000_00); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v", err)
		}
		testHook = nil
		if n := userTransferCount(t, uid); n != 0 {
			t.Fatalf("%d transfers exist", n)
		}
		if w := wallet(t, uid); w.LockedBalance != 0 || w.Balance != 10_000_00 {
			t.Fatalf("wallet %+v", w)
		}
		reconcile(t)
	})
}

// Without transactions, when even the compensation fails the orphaned
// reservation is found and released by the repair.
func TestOrphanedWithdrawalReservationIsReleasedByRepair(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := customer(t, 10_000_00)
	testHook = failAt("withdrawal.request.inTx", "withdrawal.request.compensate")
	if _, err := withdrawal(t, uid, 3_000_00); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v", err)
	}
	testHook = nil
	if w := wallet(t, uid); w.LockedBalance != 3_000_00 {
		t.Fatalf("expected the orphaned reservation, wallet %+v", w)
	}
	if n := userTransferCount(t, uid); n != 0 {
		t.Fatalf("%d transfers exist", n)
	}
	// A reservation this young may belong to a request still in flight.
	if res, err := RepairTransfers(ctx, operator(), time.Nanosecond); err != nil || len(res.ReleasedReservations) != 0 {
		t.Fatalf("a fresh reservation was released: %+v, %v", res, err)
	}
	if _, err := mongox.C(models.CollWalletTransactions).UpdateMany(ctx,
		bson.M{"userId": mustOID(uid), "movement": models.MovementLock},
		bson.M{"$set": bson.M{"createdAt": time.Now().Add(-2 * DefaultRepairGrace)}}); err != nil {
		t.Fatal(err)
	}
	res, err := RepairTransfers(ctx, operator(), time.Nanosecond)
	if err != nil || len(res.ReleasedReservations) != 1 || res.ReleasedReservations[0].Action != "orphan_reservation_released" {
		t.Fatalf("repair = %+v, %v", res, err)
	}
	if w := wallet(t, uid); w.LockedBalance != 0 || w.Balance != 10_000_00 {
		t.Fatalf("wallet after repair %+v", w)
	}
	if res, err := RepairTransfers(ctx, operator(), time.Nanosecond); err != nil || len(res.ReleasedReservations) != 0 {
		t.Fatalf("second repair = %+v, %v", res, err)
	}
	reconcile(t)
}

// Two withdrawals racing for one balance: one is reserved, the other fails
// and leaves no transfer behind.
func TestRacingWithdrawalsCannotOverReserve(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		uid := customer(t, 10_000_00)
		const n = 4
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = withdrawal(t, uid, 6_000_00)
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if code(err) != apperr.InsufficientBalance {
				t.Fatalf("unexpected error %v", err)
			}
		}
		if ok != 1 || userTransferCount(t, uid) != 1 {
			t.Fatalf("%d succeeded, %d transfers", ok, userTransferCount(t, uid))
		}
		if w := wallet(t, uid); w.LockedBalance != 6_000_00 {
			t.Fatalf("wallet %+v", w)
		}
		reconcile(t)
	})
}

func TestConcurrentWithdrawalApprovalsSettleOnce(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		uid := customer(t, 10_000_00)
		v, err := withdrawal(t, uid, 4_000_00)
		if err != nil {
			t.Fatal(err)
		}
		const n = 6
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, errs[i] = ApproveWithdrawal(context.Background(), v.ID, operator(), WithdrawalApproval{Utr: "PAYOUT123456"})
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if code(err) != apperr.Conflict {
				t.Fatalf("unexpected error %v", err)
			}
		}
		if ok != 1 {
			t.Fatalf("%d approvals succeeded", ok)
		}
		tr := transfer(t, v.ID)
		if tr.Status != models.TransferStatusCompleted || tr.LockedAmount != nil || *tr.Utr != "PAYOUT123456" {
			t.Fatalf("transfer %+v", tr)
		}
		if w := wallet(t, uid); w.Balance != 6_000_00 || w.LockedBalance != 0 {
			t.Fatalf("wallet %+v", w)
		}
		if n := transferRowCount(t, v.ID); n != 2 {
			t.Fatalf("%d ledger rows, want lock + settle", n)
		}
		reconcile(t)
	})
}

func TestWithdrawalSettleFailureAfterApprovalRecovers(t *testing.T) {
	forModes(t, func(t *testing.T, tx bool) {
		ctx := context.Background()
		uid := customer(t, 10_000_00)
		v, err := withdrawal(t, uid, 2_500_00)
		if err != nil {
			t.Fatal(err)
		}
		point := "approve.claimed"
		if tx {
			point = "approve.inTx"
		}
		testHook = failAt(point)
		if _, err := ApproveWithdrawal(ctx, v.ID, operator(), WithdrawalApproval{Utr: "PAID-0000001"}); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v", err)
		}
		testHook = nil
		tr := transfer(t, v.ID)
		if tx {
			if tr.Status != models.TransferStatusPending || row(t, models.WithdrawalSettleKey(v.ID)) != nil {
				t.Fatalf("not atomic: %s", tr.Status)
			}
			if _, err := ApproveWithdrawal(ctx, v.ID, operator(), WithdrawalApproval{Utr: "PAID-0000001"}); err != nil {
				t.Fatal(err)
			}
		} else {
			if tr.Status != models.TransferStatusApproved || tr.Utr == nil || *tr.Utr != "PAID-0000001" {
				t.Fatalf("approval not recorded durably: %+v", tr)
			}
			// The reservation still stands while the approval is pending.
			if w := wallet(t, uid); w.LockedBalance != 2_500_00 {
				t.Fatalf("wallet %+v", w)
			}
			reconcile(t)
			// Completing uses the payout reference recorded at approval.
			if _, err := CompleteTransfer(ctx, v.ID, operator(), nil); err != nil {
				t.Fatal(err)
			}
		}
		tr = transfer(t, v.ID)
		if tr.Status != models.TransferStatusCompleted || *tr.Utr != "PAID-0000001" {
			t.Fatalf("transfer %+v", tr)
		}
		if w := wallet(t, uid); w.Balance != 7_500_00 || w.LockedBalance != 0 {
			t.Fatalf("wallet %+v", w)
		}
		reconcile(t)
	})
}

func TestRejectWithdrawalReleasesReservationOnce(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		uid := customer(t, 10_000_00)
		v, err := withdrawal(t, uid, 1_000_00)
		if err != nil {
			t.Fatal(err)
		}
		rv, err := RejectTransfer(ctx, v.ID, operator(), "account closed")
		if err != nil || rv.Status != models.TransferStatusRejected {
			t.Fatalf("reject = %+v, %v", rv, err)
		}
		if _, err := RejectTransfer(ctx, v.ID, operator(), "again"); code(err) != apperr.Conflict {
			t.Fatalf("second reject: %v", err)
		}
		tr := transfer(t, v.ID)
		if tr.LockedAmount != nil || row(t, models.WithdrawalUnlockKey(v.ID)) == nil {
			t.Fatalf("reservation not released: %+v", tr)
		}
		if w := wallet(t, uid); w.LockedBalance != 0 || w.Balance != 10_000_00 {
			t.Fatalf("wallet %+v", w)
		}
		reconcile(t)
	})
}

// Without transactions the rejection lands before the release; if the
// release fails, the repair finishes it.
func TestRejectedWithdrawalWhoseReleaseFailedIsRepaired(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := customer(t, 10_000_00)
	v, err := withdrawal(t, uid, 1_000_00)
	if err != nil {
		t.Fatal(err)
	}
	testHook = failAt("reject.flipped")
	if _, err := RejectTransfer(ctx, v.ID, operator(), "account closed"); !errors.Is(err, errInjected) {
		t.Fatalf("err = %v", err)
	}
	testHook = nil
	if tr := transfer(t, v.ID); tr.Status != models.TransferStatusRejected || tr.LockedAmount == nil {
		t.Fatalf("transfer %+v", tr)
	}
	res, err := RepairTransfers(ctx, operator(), time.Nanosecond)
	if err != nil || len(res.ReleasedReservations) != 1 || res.ReleasedReservations[0].Action != "reservation_released" {
		t.Fatalf("repair = %+v, %v", res, err)
	}
	if w := wallet(t, uid); w.LockedBalance != 0 {
		t.Fatalf("wallet %+v", w)
	}
	if tr := transfer(t, v.ID); tr.LockedAmount != nil {
		t.Fatalf("lockedAmount still set")
	}
	reconcile(t)
}

// A PENDING withdrawal with no reservation (left by the old two-step request)
// is never settled - it would consume funds locked for something else - but
// can be rejected without touching the wallet.
func TestLegacyWithdrawalWithoutReservationIsNotSettled(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid := customer(t, 10_000_00)
	// Funds locked for an open trade.
	if _, err := wallets.LockTradeFunds(ctx, wallets.TradeFundsInput{UserID: uid, Stake: 5_000_00, TradeID: bson.NewObjectID().Hex()}); err != nil {
		t.Fatal(err)
	}
	locked := int64(2_000_00)
	upi := "someone@bank"
	legacy := models.Transfer{ID: bson.NewObjectID(), Reference: "WDR_LEGACY" + bson.NewObjectID().Hex()[18:], Kind: models.TransferKindWithdrawal,
		UserID: mustOID(uid), Amount: locked, Currency: "INR", Status: models.TransferStatusPending, LockedAmount: &locked,
		Destination: &models.PayoutDestination{Type: models.PaymentMethodUPI, UpiID: &upi}, CreatedAt: mongox.Now(), UpdatedAt: mongox.Now()}
	if _, err := mongox.C(models.CollTransfers).InsertOne(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := ApproveWithdrawal(ctx, legacy.ID.Hex(), operator(), WithdrawalApproval{Utr: "PAID-0000002"}); code(err) != apperr.Conflict {
		t.Fatalf("approve err = %v", err)
	}
	if s := transfer(t, legacy.ID.Hex()).Status; s != models.TransferStatusPending {
		t.Fatalf("a refused approval left the transfer %s", s)
	}
	if w := wallet(t, uid); w.LockedBalance != 5_000_00 || w.Balance != 10_000_00 {
		t.Fatalf("wallet touched: %+v", w)
	}
	if _, err := RejectTransfer(ctx, legacy.ID.Hex(), operator(), "no reservation"); err != nil {
		t.Fatal(err)
	}
	if w := wallet(t, uid); w.LockedBalance != 5_000_00 {
		t.Fatalf("wallet touched: %+v", w)
	}
}

// An approval and a rejection racing on one withdrawal: exactly one decision
// wins and the wallet reflects only that one.
func TestApproveAndRejectRaceHasOneOutcome(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		uid := customer(t, 10_000_00)
		var paid, released int64
		for round := 0; round < 5; round++ {
			v, err := withdrawal(t, uid, 1_000_00)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var approveErr, rejectErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				_, approveErr = ApproveWithdrawal(ctx, v.ID, operator(), WithdrawalApproval{Utr: "RACE-000000" + string(rune('0'+round))})
			}()
			go func() {
				defer wg.Done()
				_, rejectErr = RejectTransfer(ctx, v.ID, operator(), "race")
			}()
			wg.Wait()
			if (approveErr == nil) == (rejectErr == nil) {
				t.Fatalf("round %d: approve=%v reject=%v, want exactly one to win", round, approveErr, rejectErr)
			}
			settle, unlock := row(t, models.WithdrawalSettleKey(v.ID)), row(t, models.WithdrawalUnlockKey(v.ID))
			tr := transfer(t, v.ID)
			switch {
			case approveErr == nil && tr.Status == models.TransferStatusCompleted && settle != nil && unlock == nil:
				paid += 1_000_00
			case rejectErr == nil && tr.Status == models.TransferStatusRejected && settle == nil && unlock != nil:
				released += 1_000_00
			default:
				t.Fatalf("round %d: status %s settle %v unlock %v", round, tr.Status, settle != nil, unlock != nil)
			}
		}
		if w := wallet(t, uid); w.Balance != 10_000_00-paid || w.LockedBalance != 0 {
			t.Fatalf("wallet %+v after paying %d and releasing %d", w, paid, released)
		}
		reconcile(t)
	})
}

func hasFailedCheck(res *reconciliation.ReconciliationResult, name string) bool {
	for _, c := range res.Checks {
		if c.Name == name && !c.Passed {
			return true
		}
	}
	return false
}
