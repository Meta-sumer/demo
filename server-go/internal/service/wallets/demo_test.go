package wallets

import (
	"testing"

	"binarytrading/internal/models"
)

func TestDemoResetDelta(t *testing.T) {
	cases := []struct{ balance, want int64 }{
		{1_000_000, 0},
		{0, 1_000_000},
		{250_050, 749_950},
		{1_830_000, -830_000},
	}
	for _, c := range cases {
		if got := DemoResetDelta(c.balance); got != c.want {
			t.Errorf("DemoResetDelta(%d) = %d, want %d", c.balance, got, c.want)
		}
		if c.balance+DemoResetDelta(c.balance) != models.DemoOpeningBalance {
			t.Errorf("reset of %d does not land on the opening balance", c.balance)
		}
	}
}

func TestMutationsCarryAccount(t *testing.T) {
	in := TradeFundsInput{UserID: "u", AccountType: models.AccountDemo, Stake: 100, TradeID: "t"}
	for name, m := range map[string]mutationInput{
		"lock": tradeLockInput(in), "loss": tradeLossInput(in), "win": tradeWinInput(in, 80),
		"release": tradeReleaseInput(in, ReleaseDraw), "early": tradeEarlyCloseInput(in, 90),
	} {
		if m.AccountType != models.AccountDemo {
			t.Errorf("%s mutation lost the account type", name)
		}
	}
	if walletLockKey("u", models.AccountLive, "INR") != "wallet:u:INR" {
		t.Error("LIVE lock key must keep its pre-account form")
	}
	if walletLockKey("u", models.AccountDemo, "INR") == walletLockKey("u", models.AccountLive, "INR") {
		t.Error("DEMO and LIVE wallets must not share a lock")
	}
}
