package pricing

import (
	"math"
	"testing"

	"binarytrading/internal/models"
)

func TestComputeFeatures(t *testing.T) {
	spec := models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 40, MomentumSteps: 10, MinObservedSteps: 30, DrawLagSteps: 5}
	closes := make([]float64, 40)
	observed := make([]bool, 40)
	price := 100.0
	for i := range closes {
		// Alternating +-0.1%: zero drift, known volatility.
		if i%2 == 0 {
			price *= 1.001
		} else {
			price /= 1.001
		}
		closes[i], observed[i] = price, true
	}
	// Two gaps: carried forward, not observed.
	closes[20], observed[20] = math.NaN(), false
	closes[21], observed[21] = math.NaN(), false
	f, ok := ComputeFeatures(spec, closes, observed, closes[39])
	if !ok {
		t.Fatalf("features refused: %+v", f)
	}
	if f.ObservedSteps != 38 || f.Returns != 40 {
		t.Fatalf("observed %d returns %d", f.ObservedSteps, f.Returns)
	}
	if !(f.Sigma > 0.0009 && f.Sigma < 0.0011) {
		t.Fatalf("sigma %v, want about 0.001", f.Sigma)
	}
	if math.Abs(f.Momentum) > 0.5 {
		t.Fatalf("momentum %v on a driftless series", f.Momentum)
	}
	if !(f.LocalDrawRate > 0 && f.LocalDrawRate < 1) {
		t.Fatalf("local draw rate %v", f.LocalDrawRate)
	}

	// Too few observed steps: no features (the engine refuses).
	for i := 0; i < 15; i++ {
		observed[i] = false
		closes[i] = math.NaN()
	}
	if _, ok := ComputeFeatures(spec, closes, observed, closes[39]); ok {
		t.Fatal("23 observed of 30 required must be refused")
	}
	// A flat market has no volatility to price.
	flat := make([]float64, 40)
	for i := range flat {
		flat[i], observed[i] = 100, true
	}
	if _, ok := ComputeFeatures(spec, flat, observed, 100); ok {
		t.Fatal("zero movement must be refused")
	}
	if _, ok := ComputeFeatures(spec, flat, observed, math.NaN()); ok {
		t.Fatal("no price must be refused")
	}
}

func TestComputeFeaturesMomentumSign(t *testing.T) {
	spec := models.PricingFeatureSpec{ResolutionSeconds: 1, WindowSteps: 60, MomentumSteps: 20, MinObservedSteps: 30}
	closes := make([]float64, 60)
	observed := make([]bool, 60)
	p := 100.0
	for i := range closes {
		p *= 1 + 0.0005 + 0.001*math.Sin(float64(i))
		closes[i], observed[i] = p, true
	}
	f, ok := ComputeFeatures(spec, closes, observed, p*1.0005)
	if !ok || f.Momentum <= 0 {
		t.Fatalf("rising market: %+v", f)
	}
	if f.Momentum > MomentumClip {
		t.Fatalf("momentum not clipped: %v", f.Momentum)
	}
}
