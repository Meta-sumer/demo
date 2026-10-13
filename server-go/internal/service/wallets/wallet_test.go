package wallets

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
)

// The lifecycle invariants from the wallet model:
//
//	open : locked += stake                 (balance unchanged)
//	lose : locked -= stake, balance -= stake
//	win  : locked -= stake, balance += profit
//	draw : locked -= stake
func TestTradeMutationsKeepTheLifecycle(t *testing.T) {
	in := TradeFundsInput{UserID: bson.NewObjectID().Hex(), Stake: 500_00, TradeID: "t1"}

	cases := []struct {
		name            string
		m               mutationInput
		balance, locked int64
		typ             models.WalletTransactionType
		movement        models.MoneyMovement
		amount          int64
		key             string
		reqAvail        *int64
		reqLocked       *int64
	}{
		{"lock", tradeLockInput(in), 0, 500_00, models.WalletTxTradeLock, models.MovementLock, 500_00, "trade:lock:t1", i64(500_00), nil},
		{"loss", tradeLossInput(in), -500_00, -500_00, models.WalletTxTradeLoss, models.MovementDebit, 500_00, "trade:settle:t1", nil, i64(500_00)},
		{"win", tradeWinInput(in, 410_00), 410_00, -500_00, models.WalletTxTradeWin, models.MovementCredit, 410_00, "trade:settle:t1", nil, i64(500_00)},
		{"draw", tradeReleaseInput(in, ReleaseDraw), 0, -500_00, models.WalletTxTradeRefund, models.MovementUnlock, 500_00, "trade:settle:t1", nil, i64(500_00)},
		{"cancel", tradeReleaseInput(in, ReleaseCancelled), 0, -500_00, models.WalletTxTradeCancel, models.MovementUnlock, 500_00, "trade:settle:t1", nil, i64(500_00)},
		{"early close up", tradeEarlyCloseInput(in, 650_00), 150_00, -500_00, models.WalletTxTradeEarlyClose, models.MovementCredit, 150_00, "trade:settle:t1", nil, i64(500_00)},
		{"early close down", tradeEarlyCloseInput(in, 200_00), -300_00, -500_00, models.WalletTxTradeEarlyClose, models.MovementDebit, 300_00, "trade:settle:t1", nil, i64(500_00)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.m.BalanceDelta != c.balance || c.m.LockedDelta != c.locked {
				t.Fatalf("deltas = %d/%d, want %d/%d", c.m.BalanceDelta, c.m.LockedDelta, c.balance, c.locked)
			}
			if c.m.Type != c.typ || c.m.Movement != c.movement || c.m.Amount != c.amount {
				t.Fatalf("got %s/%s/%d", c.m.Type, c.m.Movement, c.m.Amount)
			}
			if *c.m.IdempotencyKey != c.key || *c.m.ReferenceType != "BinaryTrade" || *c.m.ReferenceID != "t1" {
				t.Fatalf("key/ref = %s %s %s", *c.m.IdempotencyKey, *c.m.ReferenceType, *c.m.ReferenceID)
			}
			if !eqPtr(c.m.RequireAvailable, c.reqAvail) || !eqPtr(c.m.RequireLocked, c.reqLocked) {
				t.Fatalf("guards = %v/%v", c.m.RequireAvailable, c.m.RequireLocked)
			}
		})
	}
}

func TestSettlementPathsShareOneKey(t *testing.T) {
	in := TradeFundsInput{UserID: bson.NewObjectID().Hex(), Stake: 100, TradeID: "abc"}
	keys := map[string]bool{
		*tradeLossInput(in).IdempotencyKey:                      true,
		*tradeWinInput(in, 80).IdempotencyKey:                   true,
		*tradeReleaseInput(in, ReleaseDraw).IdempotencyKey:      true,
		*tradeEarlyCloseInput(in, 90).IdempotencyKey:            true,
		*tradeReleaseInput(in, ReleaseCancelled).IdempotencyKey: true,
	}
	if len(keys) != 1 || !keys["trade:settle:abc"] {
		t.Fatalf("settlement keys differ: %v", keys)
	}
}

func TestAdjustmentMutations(t *testing.T) {
	a := AdjustmentInput{UserID: "u", Amount: 1000, Type: models.WalletTxWithdrawal, IdempotencyKey: "k"}
	check := func(name string, m mutationInput, bal, locked int64, mv models.MoneyMovement, avail, lck *int64) {
		t.Helper()
		if m.BalanceDelta != bal || m.LockedDelta != locked || m.Movement != mv ||
			!eqPtr(m.RequireAvailable, avail) || !eqPtr(m.RequireLocked, lck) {
			t.Errorf("%s: %+v", name, m)
		}
		if m.ReferenceType != nil || m.Description != nil || *m.IdempotencyKey != "k" {
			t.Errorf("%s: optional strings not mapped to null", name)
		}
	}
	check("credit", creditInput(a), 1000, 0, models.MovementCredit, nil, nil)
	check("debit", debitInput(a), -1000, 0, models.MovementDebit, i64(1000), nil)
	check("withdrawal lock", withdrawalLockInput(a), 0, 1000, models.MovementLock, i64(1000), nil)
	check("withdrawal unlock", withdrawalUnlockInput(a), 0, -1000, models.MovementUnlock, nil, i64(1000))
	check("withdrawal settle", withdrawalSettleInput(a), -1000, -1000, models.MovementDebit, nil, i64(1000))
}

func TestCreditDebitRejectNonPositive(t *testing.T) {
	for _, amt := range []int64{0, -5} {
		if _, err := CreditWallet(t.Context(), AdjustmentInput{Amount: amt}); err == nil || err.Error() != "INVALID_AMOUNT: Credit amount must be positive" {
			t.Errorf("credit %d: %v", amt, err)
		}
		if _, err := DebitWallet(t.Context(), AdjustmentInput{Amount: amt}); err == nil || err.Error() != "INVALID_AMOUNT: Debit amount must be positive" {
			t.Errorf("debit %d: %v", amt, err)
		}
	}
}

func TestGuardFilter(t *testing.T) {
	uid := bson.NewObjectID()
	f := guardFilter(uid, "INR", tradeLossInput(TradeFundsInput{Stake: 50, TradeID: "x"}))
	and := f["$expr"].(bson.M)["$and"].(bson.A)
	// requireLocked, balance >= 50, locked >= 50
	if len(and) != 3 {
		t.Fatalf("guards = %v", and)
	}
	f = guardFilter(uid, "INR", tradeLockInput(TradeFundsInput{Stake: 50, TradeID: "x"}))
	and = f["$expr"].(bson.M)["$and"].(bson.A)
	if len(and) != 1 {
		t.Fatalf("lock guards = %v", and)
	}
	f = guardFilter(uid, "INR", creditInput(AdjustmentInput{Amount: 5}))
	if _, ok := f["$expr"]; ok {
		t.Fatal("a credit must not be guarded")
	}
	if f["userId"] != uid || f["currency"] != "INR" {
		t.Fatalf("filter = %v", f)
	}
}

func TestNormCurrency(t *testing.T) {
	if normCurrency("") != "INR" || normCurrency("usd") != "USD" {
		t.Fatal("currency not normalised")
	}
}

func eqPtr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
