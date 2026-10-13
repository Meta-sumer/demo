package settlement

import (
	"testing"

	"binarytrading/internal/models"
)

func TestDetermineResult(t *testing.T) {
	cases := []struct {
		dir          models.Direction
		entry, final float64
		want         models.TradeResult
	}{
		{models.DirectionUp, 100, 101, models.TradeResultWin},
		{models.DirectionUp, 100, 99, models.TradeResultLoss},
		{models.DirectionUp, 100, 100, models.TradeResultDraw},
		{models.DirectionDown, 100, 99, models.TradeResultWin},
		{models.DirectionDown, 100, 101, models.TradeResultLoss},
		{models.DirectionDown, 100, 100, models.TradeResultDraw},
		// The smallest possible move is a decisive one.
		{models.DirectionUp, 104_250.01, 104_250.02, models.TradeResultWin},
		{models.DirectionDown, 104_250.01, 104_250.02, models.TradeResultLoss},
	}
	for _, c := range cases {
		if got := DetermineResult(c.dir, c.entry, c.final); got != c.want {
			t.Fatalf("%s %v->%v = %s, want %s", c.dir, c.entry, c.final, got, c.want)
		}
	}
}

func TestApplyDrawPolicy(t *testing.T) {
	if ApplyDrawPolicy(models.TradeResultDraw, models.DrawPolicyRefund) != models.TradeResultDraw ||
		ApplyDrawPolicy(models.TradeResultDraw, models.DrawPolicyLoss) != models.TradeResultLoss ||
		ApplyDrawPolicy(models.TradeResultDraw, models.DrawPolicyWin) != models.TradeResultWin {
		t.Fatal("draw policy not applied")
	}
	// The draw policy never changes a decided result.
	for _, p := range []models.DrawPolicy{models.DrawPolicyRefund, models.DrawPolicyLoss, models.DrawPolicyWin} {
		if ApplyDrawPolicy(models.TradeResultWin, p) != models.TradeResultWin ||
			ApplyDrawPolicy(models.TradeResultLoss, p) != models.TradeResultLoss {
			t.Fatalf("policy %s changed a decided result", p)
		}
	}
}
