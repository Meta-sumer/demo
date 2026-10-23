package pricing

import (
	"testing"
	"time"

	"binarytrading/internal/models"
)

func TestPublicPayoutsAndRange(t *testing.T) {
	row := func(expiry int64, pct float64) CustomerPayout {
		return CustomerPayout{PayoutView: models.PayoutView{ID: "internal-id", AssetID: "asset-id", ExpirySeconds: expiry, PayoutPercent: pct},
			ConfiguredPayoutPercent: 86, PricingMode: models.PricingModeDynamic}
	}
	got := publicPayouts([]CustomerPayout{row(300, 80.5), row(30, 74), row(60, 0), row(120, 77.5)})
	want := []PublicPayout{{30, 74}, {120, 77.5}, {300, 80.5}}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%d: %v, want %v (shortest first, no unpriced row)", i, got[i], want[i])
		}
	}
	lo, hi := payoutRange(got)
	if lo == nil || hi == nil || *lo != 74 || *hi != 80.5 {
		t.Fatalf("range %v %v", lo, hi)
	}
	if lo, hi := payoutRange(nil); lo != nil || hi != nil {
		t.Fatal("an empty list has no range")
	}
	if out := publicPayouts(nil); out == nil || len(out) != 0 {
		t.Fatal("an empty list marshals as [], not null")
	}
	if PublicPayoutBasis != "LOWER_OF_UP_AND_DOWN" || PublicMarketsTTL > 15*time.Second {
		t.Fatal("the landing pages poll every 10-15 s and say the figure is the lower of UP and DOWN")
	}
}
