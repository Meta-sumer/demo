package binary

import (
	"math"
	"testing"

	"binarytrading/internal/models"
)

// The guard has two ways to be wrong, and both are silent: too strict refuses
// trades the customer wanted, too loose accepts materially worse fills. The
// direction asymmetry is the easy part to invert.
const tol = 0.1 // percent

func slip(quoted, server float64, dir models.Direction, tolerance float64) SlippageAssessment {
	return AssessSlippage(SlippageInput{QuotedPrice: quoted, ServerPrice: server, Direction: dir, TolerancePercent: tolerance})
}

func near(a, b float64, digits int) bool { return math.Abs(a-b) < math.Pow10(-digits)/2 }

func TestSlippageUnchanged(t *testing.T) {
	if got := slip(100, 100, models.DirectionUp, tol); got != (SlippageAssessment{Acceptable: true}) {
		t.Fatalf("%+v", got)
	}
}

func TestSlippageUp(t *testing.T) {
	r := slip(100, 100.2, models.DirectionUp, tol)
	if !r.Adverse || r.Acceptable || !near(r.MovePercent, 0.2, 10) {
		t.Fatalf("rise beyond tolerance: %+v", r)
	}
	r = slip(100, 100.05, models.DirectionUp, tol)
	if !r.Adverse || !r.Acceptable {
		t.Fatalf("rise inside tolerance: %+v", r)
	}
	r = slip(100, 80, models.DirectionUp, tol)
	if r.Adverse || !r.Acceptable {
		t.Fatalf("fall: %+v", r)
	}
}

func TestSlippageDown(t *testing.T) {
	r := slip(100, 99.8, models.DirectionDown, tol)
	if !r.Adverse || r.Acceptable {
		t.Fatalf("fall beyond tolerance: %+v", r)
	}
	r = slip(100, 120, models.DirectionDown, tol)
	if r.Adverse || !r.Acceptable {
		t.Fatalf("rise: %+v", r)
	}
	up := slip(100, 100.2, models.DirectionUp, tol)
	down := slip(100, 100.2, models.DirectionDown, tol)
	if !up.Adverse || down.Adverse || !near(up.MovePercent, down.MovePercent, 10) {
		t.Fatalf("mirror: %+v %+v", up, down)
	}
}

func TestSlippageBoundary(t *testing.T) {
	if !slip(100, 100.1, models.DirectionUp, tol).Acceptable {
		t.Fatal("exactly at tolerance must be accepted")
	}
	if slip(100, 100.1000001, models.DirectionUp, tol).Acceptable {
		t.Fatal("past tolerance must be refused")
	}
}

func TestSlippageDisabledAndDegenerate(t *testing.T) {
	r := slip(100, 500, models.DirectionUp, 0)
	if !r.Acceptable || !r.Adverse {
		t.Fatalf("zero tolerance: %+v", r)
	}
	if !slip(100, 500, models.DirectionUp, -5).Acceptable {
		t.Fatal("negative tolerance")
	}
	for _, q := range []float64{0, -1} {
		if got := slip(q, 100, models.DirectionUp, tol); got != (SlippageAssessment{Acceptable: true}) {
			t.Fatalf("quote %v: %+v", q, got)
		}
	}
	for _, q := range []float64{math.NaN(), math.Inf(1)} {
		if !slip(q, 100, models.DirectionUp, tol).Acceptable {
			t.Fatalf("quote %v", q)
		}
	}
}

func TestSlippageRealisticInstruments(t *testing.T) {
	r := slip(1.15563, 1.15575, models.DirectionUp, tol)
	if !r.Acceptable || !near(r.MovePercent, 0.01038, 4) {
		t.Fatalf("fx: %+v", r)
	}
	r = slip(91_000, 91_200, models.DirectionUp, tol)
	if r.Acceptable || !near(r.MovePercent, 0.2198, 3) {
		t.Fatalf("crypto: %+v", r)
	}
}
