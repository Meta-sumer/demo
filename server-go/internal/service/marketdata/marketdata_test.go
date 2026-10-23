package marketdata

import (
	"encoding/json"
	"math"
	"testing"

	"binarytrading/internal/models"
)

// Ported from server/tests/unit/feed-health.test.ts.

var thresholds = FeedClassificationInput{HealthyMaxMs: 100, DegradedMaxMs: 300, TickMaxAgeMs: 5000}

func classify(age, latency int64, t FeedClassificationInput) models.FeedStatus {
	t.AgeMs, t.LatencyMs = age, latency
	return ClassifyFeed(t)
}

func TestFeedHealthClassification(t *testing.T) {
	strict := FeedClassificationInput{HealthyMaxMs: 10, DegradedMaxMs: 20, TickMaxAgeMs: 40}
	cases := []struct {
		name         string
		age, latency int64
		th           FeedClassificationInput
		want         models.FeedStatus
	}{
		{"healthy for a fresh, low latency tick", 40, 48, thresholds, models.FeedStatusHealthy},
		{"degrades on latency alone", 10, 250, thresholds, models.FeedStatusDegraded},
		{"goes stale on latency alone", 10, 800, thresholds, models.FeedStatusStale},
		// A feed that delivered a 40ms tick 250ms ago is not healthy any more.
		{"degrades on staleness", 250, 40, thresholds, models.FeedStatusDegraded},
		{"stale on staleness", 900, 40, thresholds, models.FeedStatusStale},
		{"offline past the maximum age", 5001, 10, thresholds, models.FeedStatusOffline},
		{"worse of the two (age)", 500, 20, thresholds, models.FeedStatusStale},
		{"worse of the two (latency)", 20, 500, thresholds, models.FeedStatusStale},
		{"reconfigured healthy", 5, 5, strict, models.FeedStatusHealthy},
		{"reconfigured degraded", 15, 5, strict, models.FeedStatusDegraded},
		{"reconfigured offline", 41, 5, strict, models.FeedStatusOffline},
	}
	for _, c := range cases {
		if got := classify(c.age, c.latency, c.th); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestJSString(t *testing.T) {
	cases := map[float64]string{
		104250.5: "104250.5", 1.0842: "1.0842", 100: "100", 0.30000000000000004: "0.30000000000000004",
		1e21: "1e+21", 0.0000001: "1e-7", 0.000001: "0.000001",
	}
	for in, want := range cases {
		if got := jsString(in); got != want {
			t.Errorf("jsString(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTickJSONKeyOrder(t *testing.T) {
	bid := 1.5
	pt := int64(1000)
	n := &models.NormalisedTick{
		MarketTick: models.MarketTick{Asset: "BTCUSD", Price: 2, Bid: &bid, Timestamp: 1000, ProviderTimestamp: &pt, ReceivedAt: 1005},
		AssetID:    "a", Symbol: "BTCUSD", ProcessedAt: 1006, FeedLatencyMs: 5, ProcessingLatencyMs: 1, TotalLatencyMs: 6,
	}
	b, _ := json.Marshal(toTickJSON(n))
	want := `{"assetId":"a","asset":"BTCUSD","symbol":"BTCUSD","price":2,"bid":1.5,"timestamp":1000,"providerTimestamp":1000,"receivedAt":1005,"processedAt":1006,"feedLatencyMs":5,"processingLatencyMs":1,"totalLatencyMs":6}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}

	// Hash round trip.
	raw := map[string]string{}
	for k, v := range serialiseTick(n) {
		raw[k] = v.(string)
	}
	if raw["ask"] != "" || raw["sequence"] != "" || raw["price"] != "2" {
		t.Fatalf("unexpected hash %v", raw)
	}
	back := deserialiseTick(raw)
	if back.Price != 2 || back.Ask != nil || *back.Bid != 1.5 || back.TotalLatencyMs != 6 || back.Sequence != nil {
		t.Fatalf("round trip mismatch %+v", back)
	}
}

func TestFoldCandles(t *testing.T) {
	src := []models.CandleView{
		{Time: 0, Open: 1, High: 2, Low: 1, Close: 2},
		{Time: 60_000, Open: 2, High: 5, Low: 0.5, Close: 3},
		{Time: 300_000, Open: 3, High: 3, Low: 3, Close: 3},
	}
	out := foldCandles(src, 300_000)
	if len(out) != 2 {
		t.Fatalf("got %d candles", len(out))
	}
	if out[0] != (models.CandleView{Time: 0, Open: 1, High: 5, Low: 0.5, Close: 3}) {
		t.Fatalf("bad fold %+v", out[0])
	}
	if got := lastN(out, 1); len(got) != 1 || got[0].Time != 300_000 {
		t.Fatalf("lastN %+v", got)
	}
}

func TestRealisedVolatility(t *testing.T) {
	if v := realisedVolatility([]float64{1, 1}); v != 0 {
		t.Fatalf("want 0 for short series, got %v", v)
	}
	if v := realisedVolatility([]float64{100, 100, 100, 100}); v != 0 {
		t.Fatalf("want 0 for a flat series, got %v", v)
	}
	r1, r2 := math.Log(1.01), math.Log(0.99)
	mean := (r1 + r2) / 2
	want := math.Sqrt(((r1-mean)*(r1-mean) + (r2-mean)*(r2-mean)) / 1)
	if v := realisedVolatility([]float64{100, 101, 99.99}); math.Abs(v-want) > 1e-12 {
		t.Fatalf("got %v want %v", v, want)
	}
}
