package risk

import (
	"math"
	"testing"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/exposure"
)

const nowMs = int64(1_800_000_000_000)

func atm(dir int8, payout int64, seconds int64) models.HedgeLeg {
	return models.HedgeLeg{Dir: dir, Payout: payout, Strike: 100, ExpiryMs: nowMs + seconds*1000}
}

func TestLegDeltaMatchesClosedForm(t *testing.T) {
	// sigma 0.001/sqrt(s), 100 s: v = 0.01, d2 = -0.005 at the money.
	leg := atm(1, 10_000_000, 100)
	got, ok := LegDelta(leg, 100, 0.001, nowMs, 10)
	want := 10_000_000 * NormPDF(-0.005) / 0.01
	if !ok || math.Abs(got-want) > 1e-6 {
		t.Fatalf("delta %v ok %v, want %v", got, ok, want)
	}
	// DOWN is the mirror image.
	leg.Dir = -1
	if d, _ := LegDelta(leg, 100, 0.001, nowMs, 10); math.Abs(d+want) > 1e-6 {
		t.Fatalf("down delta %v", d)
	}
	// Deep in or out of the money the delta vanishes.
	if d, _ := LegDelta(atm(1, 10_000_000, 100), 105, 0.001, nowMs, 10); d > want/10_000 {
		t.Fatalf("far from the strike: %v", d)
	}
}

func TestGammaWindowExcludesNearExpiry(t *testing.T) {
	legs := []models.HedgeLeg{atm(1, 1_000_000, 9), atm(1, 1_000_000, 10), atm(-1, 1_000_000, -1)}
	delta, eligible, excluded := BookDelta(legs, 100, 0.0001, nowMs, 10)
	if eligible != 1 || excluded != 2 {
		t.Fatalf("eligible %d excluded %d", eligible, excluded)
	}
	want, _ := LegDelta(legs[1], 100, 0.0001, nowMs, 10)
	if delta != want {
		t.Fatalf("delta %v want %v", delta, want)
	}
}

func TestOffsettingBookHasNoDelta(t *testing.T) {
	legs := []models.HedgeLeg{atm(1, 1_000_000, 60), atm(-1, 1_000_000, 60)}
	if d, _, _ := BookDelta(legs, 100, 0.0001, nowMs, 10); math.Abs(d) > 1e-9 {
		t.Fatalf("offsetting book delta %v", d)
	}
}

func TestHedgeRatioReplacesTimesTwo(t *testing.T) {
	band := &models.HedgeLevel{HedgeRatio: 0.7}
	if h := HedgeRatioFor(band, 0, 1); h != 0.7 {
		t.Fatalf("h %v", h)
	}
	// The asset ratio is a ceiling, not a multiplier.
	if h := HedgeRatioFor(band, 0, 0.5); h != 0.5 {
		t.Fatalf("ceiling: h %v", h)
	}
	if h := HedgeRatioFor(band, 0.5, 1); h != 1 {
		t.Fatalf("boost clamps at 1: h %v", h)
	}
	if h := HedgeRatioFor(&models.HedgeLevel{HedgeRatio: 0}, 0.1, 1); h != 0 {
		t.Fatalf("no-hedge band: h %v", h)
	}
}

func TestCapTarget(t *testing.T) {
	if v, c, by := CapTarget(500, 1_000, 0, 0); v != 500 || c != 1_000 || by != "" {
		t.Fatalf("%d %d %q", v, c, by)
	}
	if v, _, by := CapTarget(-5_000, 1_000, 0, 0); v != -1_000 || by != "MAX_HEDGE_NOTIONAL" {
		t.Fatalf("%d %q", v, by)
	}
	if v, c, by := CapTarget(5_000, 10_000, 2, 1_500); v != 3_000 || c != 3_000 || by != "LIABILITY_MULTIPLE" {
		t.Fatalf("%d %d %q", v, c, by)
	}
	// A book that cannot lose needs no hedge.
	if v, _, _ := CapTarget(5_000, 10_000, 2, 0); v != 0 {
		t.Fatalf("zero liability: %d", v)
	}
}

func hedgeInputs() RiskInputs {
	cfg := models.DefaultRiskConfig()
	return RiskInputs{
		AssetID: "a1", Symbol: "BTC/USD",
		Exposure:              book(exposure.RawCounters{UpStake: util.ToMinor(700_000), OpenTrades: 5}),
		AssetExposureLimit:    util.ToMinor(1_000_000),
		AssetNetExposureLimit: util.ToMinor(1_000_000),
		HedgeEnabled:          true,
		AssetTargetHedgeRatio: 0.95,
		MinHedgeNotional:      util.ToMinor(1_000),
		MaxHedgeNotional:      util.ToMinor(1_000_000),
		Config:                &cfg,
		GlobalGrossExposure:   util.ToMinor(700_000),
		FeedStatus:            models.FeedStatusHealthy,
		HedgeBook: &HedgeBook{
			Legs:  []models.HedgeLeg{atm(1, util.ToMinor(10_000), 100)},
			Price: 100, Sigma: 0.001, SigmaSource: "test", NowMs: nowMs,
		},
	}
}

func TestAssessRiskHedgeSizing(t *testing.T) {
	in := hedgeInputs()
	a := AssessRisk(in)
	// 70% gross utilisation -> HIGH band, h = min(0.7, 0.95) = 0.7.
	if a.Level != models.RiskLevelHigh || a.HedgeDecision != models.HedgeDecisionHighHedge {
		t.Fatalf("level %s decision %s", a.Level, a.HedgeDecision)
	}
	delta := float64(util.ToMinor(10_000)) * NormPDF(-0.005) / 0.01
	d := a.HedgeTarget
	if d == nil || d.BookDelta != int64(util.RoundHalfUp(delta)) || d.HedgeRatio != 0.7 {
		t.Fatalf("detail %+v", d)
	}
	if d.TargetSigned != int64(util.RoundHalfUp(0.7*float64(d.BookDelta))) || d.CappedBy != "" {
		t.Fatalf("target %+v", d)
	}
	if a.TargetHedgeRatio != 0.7 || a.TargetHedgeNotional != d.TargetSigned {
		t.Fatalf("ratio %v notional %d", a.TargetHedgeRatio, a.TargetHedgeNotional)
	}
	if len(a.Reasons) != 1 || a.Reasons[0] != "gross exposure at 70.0% of the asset limit" {
		t.Fatalf("reasons %v", a.Reasons)
	}

	// A DOWN-leaning sensitivity is a short target.
	in.HedgeBook.Legs = []models.HedgeLeg{atm(-1, util.ToMinor(10_000), 100)}
	if b := AssessRisk(in); b.HedgeTarget.TargetSigned >= 0 || b.TargetHedgeNotional != -b.HedgeTarget.TargetSigned {
		t.Fatalf("short target %+v", b.HedgeTarget)
	}

	// The asset's maximum hedge notional binds.
	in.HedgeBook.Legs = []models.HedgeLeg{atm(1, util.ToMinor(1_000_000), 100)}
	if b := AssessRisk(in); b.TargetHedgeNotional != util.ToMinor(1_000_000) || b.HedgeTarget.CappedBy != "MAX_HEDGE_NOTIONAL" {
		t.Fatalf("capped %+v", b.HedgeTarget)
	}

	in.HedgeEnabled = false
	if b := AssessRisk(in); b.HedgeDecision != models.HedgeDecisionNoHedge || b.TargetHedgeNotional != 0 ||
		b.HedgeTarget.Reason != "hedging disabled for this asset" {
		t.Fatalf("disabled hedge: %+v", b)
	}
}

func TestBelowThresholdTargetsFlat(t *testing.T) {
	in := hedgeInputs()
	in.Exposure = book(exposure.RawCounters{UpStake: util.ToMinor(200_000), OpenTrades: 1})
	in.GlobalGrossExposure = util.ToMinor(200_000)
	a := AssessRisk(in)
	if a.TargetHedgeNotional != 0 || a.HedgeTarget.Reason != "utilisation in a band with no hedge" {
		t.Fatalf("below threshold %+v", a.HedgeTarget)
	}
	// The delta is still reported, so the screen shows what is left unhedged.
	if a.HedgeTarget.BookDelta == 0 {
		t.Fatal("book delta not reported")
	}
}

func TestUnavailableHedgeAddsReason(t *testing.T) {
	in := hedgeInputs()
	in.HedgeUnavailableReason = "FX_STALE"
	a := AssessRisk(in)
	if len(a.Reasons) != 2 || a.Reasons[1] != "hedge required but unavailable: FX_STALE" {
		t.Fatalf("reasons %v", a.Reasons)
	}
}
