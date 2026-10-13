package exposure

import (
	"testing"

	"binarytrading/internal/platform/util"
)

func derive(c RawCounters) (s struct {
	net, gross, liability, upPayout, downPayout, up, down, open int64
}) {
	snap := DeriveExposure("asset-1", "BTC/USD", c, util.NowMs())
	s.net, s.gross, s.liability = snap.NetExposure, snap.GrossExposure, snap.PotentialLiability
	s.upPayout, s.downPayout = snap.UpPotentialPayout, snap.DownPotentialPayout
	s.up, s.down, s.open = snap.UpStake, snap.DownStake, snap.OpenTrades
	return
}

func eq(t *testing.T, name string, got, want int64) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %d, want %d", name, got, want)
	}
}

// The worked example from the specification: UP 10,000, DOWN 4,000.
func TestSpecifiedNetAndGross(t *testing.T) {
	s := derive(RawCounters{UpStake: util.ToMinor(10_000), DownStake: util.ToMinor(4_000), OpenTrades: 2})
	eq(t, "net", s.net, util.ToMinor(6_000))
	eq(t, "gross", s.gross, util.ToMinor(14_000))
}

func TestNegativeNetWhenBookLeansDown(t *testing.T) {
	s := derive(RawCounters{UpStake: util.ToMinor(2_000), DownStake: util.ToMinor(9_000), OpenTrades: 3})
	eq(t, "net", s.net, util.ToMinor(-7_000))
	eq(t, "gross", s.gross, util.ToMinor(11_000))
}

func TestBalancedBook(t *testing.T) {
	s := derive(RawCounters{
		UpStake: util.ToMinor(5_000), DownStake: util.ToMinor(5_000),
		UpProfit: util.ToMinor(4_100), DownProfit: util.ToMinor(4_100), OpenTrades: 2,
	})
	eq(t, "net", s.net, 0)
	eq(t, "gross", s.gross, util.ToMinor(10_000))
	// Each side's profit is covered by the other side's forfeited stake.
	eq(t, "liability", s.liability, 0)
}

func TestLiabilityIsWorstCase(t *testing.T) {
	upStake, downStake := util.ToMinor(10_000), util.ToMinor(4_000)
	upProfit, downProfit := util.CalculateProfit(upStake, 82), util.CalculateProfit(downStake, 82)
	s := derive(RawCounters{UpStake: upStake, DownStake: downStake, UpProfit: upProfit, DownProfit: downProfit, OpenTrades: 2})
	eq(t, "liability", s.liability, upProfit-downStake)
	eq(t, "liability", s.liability, util.ToMinor(4_200))
	eq(t, "upPayout", s.upPayout, upStake+upProfit)
	eq(t, "downPayout", s.downPayout, downStake+downProfit)
}

func TestNeverNegative(t *testing.T) {
	s := derive(RawCounters{UpStake: -500, DownStake: -1, UpProfit: -10, OpenTrades: -3})
	eq(t, "upStake", s.up, 0)
	eq(t, "downStake", s.down, 0)
	eq(t, "gross", s.gross, 0)
	eq(t, "openTrades", s.open, 0)
}

// Ported from tests/unit/early-close.test.ts ("trader sentiment").
func TestDeriveSentiment(t *testing.T) {
	split := DeriveSentiment(util.ToMinor(10_000), util.ToMinor(4_000))
	if split == nil || split.Up != 71 || split.Up+split.Down != 100 {
		t.Fatalf("split = %+v", split)
	}
	if DeriveSentiment(0, 0) != nil {
		t.Fatal("empty book must report nil")
	}
	if s := DeriveSentiment(util.ToMinor(500), 0); *s != (SentimentSplit{Up: 100, Down: 0}) {
		t.Fatalf("one-sided up = %+v", s)
	}
	if s := DeriveSentiment(0, util.ToMinor(500)); *s != (SentimentSplit{Up: 0, Down: 100}) {
		t.Fatalf("one-sided down = %+v", s)
	}
}
