package wallets

import (
	"context"
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
	"binarytrading/internal/testsupport"
)

func errCode(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Code
	}
	return ""
}

func TestValidateClientIdempotencyKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"":                                     false,
		"short":                                false,
		"has space in it":                      false,
		strings.Repeat("a", 201):               false,
		"9b2f6a1e-4c55-4d0e-9a51-0c1f2b3c4d5e": true,
		"adj:2026-10-16:0001":                  true,
	} {
		err := ValidateClientIdempotencyKey(key)
		if ok != (err == nil) || (err != nil && errCode(err) != apperr.ValidationError) {
			t.Errorf("%q: err = %v", key, err)
		}
	}
}

func TestAdjustmentLedgerKeyIsScopedToTheOperator(t *testing.T) {
	a, b := AdjustmentLedgerKey("actor1", "k-00000001"), AdjustmentLedgerKey("actor2", "k-00000001")
	if a == b || !strings.HasPrefix(a, "adjust:actor1:") || a != AdjustmentLedgerKey("actor1", "k-00000001") {
		t.Fatalf("keys %q %q", a, b)
	}
}

// ── live tests ──────────────────────────────────────────────────────────

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

func forModes(t *testing.T, fn func(t *testing.T)) {
	for _, tx := range []bool{false, true} {
		name := "without_transactions"
		if tx {
			name = "with_transactions"
		}
		t.Run(name, func(t *testing.T) {
			liveStores(t, tx)
			fn(t)
		})
	}
}

func newUser(t *testing.T) string {
	t.Helper()
	uid := bson.NewObjectID().Hex()
	if _, err := GetOrCreateWallet(context.Background(), uid, models.AccountLive, ""); err != nil {
		t.Fatal(err)
	}
	return uid
}

func adjustmentRows(t *testing.T, uid string) []models.WalletTransaction {
	t.Helper()
	id, _ := mongox.ParseID(uid)
	cur, err := mongox.C(models.CollWalletTransactions).Find(context.Background(), bson.M{"userId": id, "referenceType": refAdminAdjustment})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.WalletTransaction
	if err := cur.All(context.Background(), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// A resent adjustment (same Idempotency-Key, same body) replays the original
// result and moves no money.
func TestAdminAdjustWalletRepeatedRequestAppliesOnce(t *testing.T) {
	forModes(t, func(t *testing.T) {
		ctx := context.Background()
		uid, actor := newUser(t), bson.NewObjectID().Hex()
		in := AdminAdjustmentInput{UserID: uid, Direction: "CREDIT", Amount: 500_00, Reason: "goodwill", ActorID: actor,
			IdempotencyKey: "9b2f6a1e-4c55-4d0e-9a51-0c1f2b3c4d5e"}
		first, err := AdminAdjustWallet(ctx, in)
		if err != nil || first.Replayed || first.Wallet.Balance != 500_00 {
			t.Fatalf("first = %+v, %v", first, err)
		}
		// Something else moves the balance in between: the replay still
		// returns what the adjustment itself produced.
		if _, err := CreditWallet(ctx, AdjustmentInput{UserID: uid, Amount: 1, Type: models.WalletTxDeposit, IdempotencyKey: "other:" + uid}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			again, err := AdminAdjustWallet(ctx, in)
			if err != nil || !again.Replayed {
				t.Fatalf("replay %d = %+v, %v", i, again, err)
			}
			if again.TransactionID != first.TransactionID || again.Reference != first.Reference ||
				again.Wallet.Balance != first.Wallet.Balance || again.Wallet.ID != first.Wallet.ID {
				t.Fatalf("replay %+v differs from the original %+v", again, first)
			}
		}
		if rows := adjustmentRows(t, uid); len(rows) != 1 {
			t.Fatalf("%d adjustment rows, want 1", len(rows))
		}
		w, _ := GetWallet(ctx, uid, "", "")
		if w.Balance != 500_01 {
			t.Fatalf("balance = %d", w.Balance)
		}
	})
}

func TestAdminAdjustWalletConcurrentDuplicatesApplyOnce(t *testing.T) {
	forModes(t, func(t *testing.T) {
		ctx := context.Background()
		uid, actor := newUser(t), bson.NewObjectID().Hex()
		in := AdminAdjustmentInput{UserID: uid, Direction: "CREDIT", Amount: 250_00, Reason: "ticket 42", ActorID: actor,
			IdempotencyKey: "dup-key-0000001"}
		const n = 10
		var wg sync.WaitGroup
		results := make([]AdminAdjustmentResult, n)
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = AdminAdjustWallet(ctx, in)
			}(i)
		}
		wg.Wait()
		applied := 0
		for i := range results {
			if errs[i] != nil {
				t.Fatalf("request %d: %v", i, errs[i])
			}
			if !results[i].Replayed {
				applied++
			}
			if results[i].TransactionID != results[0].TransactionID || results[i].Wallet.Balance != 250_00 {
				t.Fatalf("result %d = %+v", i, results[i])
			}
		}
		if applied != 1 {
			t.Fatalf("%d requests applied, want 1", applied)
		}
		if rows := adjustmentRows(t, uid); len(rows) != 1 {
			t.Fatalf("%d rows", len(rows))
		}
		if w, _ := GetWallet(ctx, uid, "", ""); w.Balance != 250_00 {
			t.Fatalf("balance = %d", w.Balance)
		}
	})
}

func TestAdminAdjustWalletKeyReusedForAnotherBodyIsRefused(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid, actor := newUser(t), bson.NewObjectID().Hex()
	in := AdminAdjustmentInput{UserID: uid, Direction: "CREDIT", Amount: 100_00, Reason: "first", ActorID: actor, IdempotencyKey: "reuse-key-001"}
	if _, err := AdminAdjustWallet(ctx, in); err != nil {
		t.Fatal(err)
	}
	for _, mod := range []func(*AdminAdjustmentInput){
		func(in *AdminAdjustmentInput) { in.Amount = 200_00 },
		func(in *AdminAdjustmentInput) { in.Direction = "DEBIT" },
		func(in *AdminAdjustmentInput) { in.Reason = "second" },
		func(in *AdminAdjustmentInput) { in.UserID = newUser(t) },
	} {
		other := in
		mod(&other)
		if _, err := AdminAdjustWallet(ctx, other); errCode(err) != apperr.IdempotencyPayloadMismatch {
			t.Fatalf("err = %v, want IDEMPOTENCY_PAYLOAD_MISMATCH", err)
		}
	}
	// A new key is a new adjustment; another operator's identical key too.
	in2 := in
	in2.IdempotencyKey = "reuse-key-002"
	if r, err := AdminAdjustWallet(ctx, in2); err != nil || r.Replayed {
		t.Fatalf("new key = %+v, %v", r, err)
	}
	in3 := in
	in3.ActorID = bson.NewObjectID().Hex()
	if r, err := AdminAdjustWallet(ctx, in3); err != nil || r.Replayed {
		t.Fatalf("other operator = %+v, %v", r, err)
	}
	if rows := adjustmentRows(t, uid); len(rows) != 3 {
		t.Fatalf("%d rows, want 3", len(rows))
	}
}

// A debit that failed wrote nothing, so the same attempt can be retried once
// the funds are there.
func TestAdminAdjustWalletFailedDebitCanBeRetried(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	uid, actor := newUser(t), bson.NewObjectID().Hex()
	debit := AdminAdjustmentInput{UserID: uid, Direction: "DEBIT", Amount: 100_00, Reason: "fee", ActorID: actor, IdempotencyKey: "debit-key-001"}
	if _, err := AdminAdjustWallet(ctx, debit); errCode(err) != apperr.InsufficientBalance {
		t.Fatalf("err = %v", err)
	}
	if _, err := AdminAdjustWallet(ctx, AdminAdjustmentInput{UserID: uid, Direction: "CREDIT", Amount: 300_00, Reason: "top up",
		ActorID: actor, IdempotencyKey: "credit-key-001"}); err != nil {
		t.Fatal(err)
	}
	r, err := AdminAdjustWallet(ctx, debit)
	if err != nil || r.Replayed || r.Wallet.Balance != 200_00 {
		t.Fatalf("retry = %+v, %v", r, err)
	}
	if r2, err := AdminAdjustWallet(ctx, debit); err != nil || !r2.Replayed || r2.Wallet.Balance != 200_00 {
		t.Fatalf("replay = %+v, %v", r2, err)
	}
}

func TestAdminAdjustWalletRequiresAKey(t *testing.T) {
	liveStores(t, false)
	uid := newUser(t)
	_, err := AdminAdjustWallet(context.Background(), AdminAdjustmentInput{UserID: uid, Direction: "CREDIT", Amount: 1, Reason: "x",
		ActorID: bson.NewObjectID().Hex()})
	if errCode(err) != apperr.ValidationError {
		t.Fatalf("err = %v", err)
	}
	if rows := adjustmentRows(t, uid); len(rows) != 0 {
		t.Fatalf("%d rows", len(rows))
	}
}
