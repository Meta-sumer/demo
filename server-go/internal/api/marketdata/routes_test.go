package marketdata

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/testsupport"
)

func ms(v int64) string { return strconv.FormatInt(v, 10) }

// TestWindowValidation runs the handlers directly: the query is rejected
// before any store is touched.
func TestWindowValidation(t *testing.T) {
	id := bson.NewObjectID().Hex()
	cases := []struct {
		handler       func(http.ResponseWriter, *http.Request) error
		query         string
		path, message string
	}{
		{ticks, "from=1790000000000&to=1790000000000", "from", "from must be before to"},
		{ticks, "from=1790000060000&to=1790000000000", "from", "from must be before to"},
		{ticks, "from=1790000000000&to=1790000900001", "to", "The window from..to must not exceed 15 minutes"},
		{ticks, "from=soon", "from", "Invalid date"},
		{ticks, "to=later&from=1790000000000", "to", "Invalid date"},
		{ticks, "windowMs=100", "windowMs", "Number must be greater than or equal to 1000"},
		{ticks, "windowMs=900001", "windowMs", "Number must be less than or equal to 900000"},
		{candles, "to=nope", "to", "Invalid date"},
		{candles, "interval=7", "interval", "Invalid input"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x?"+c.query, nil)
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
		err := c.handler(httptest.NewRecorder(), req)
		ae, ok := apperr.As(err)
		if !ok || ae.StatusCode != 422 || ae.Message != "Invalid query parameters" {
			t.Errorf("%s: want a 422, got %v", c.query, err)
			continue
		}
		issues := ae.Details.([]apperr.FieldIssue)
		if len(issues) != 1 || issues[0].Path != c.path || issues[0].Message != c.message {
			t.Errorf("%s: issues %+v", c.query, issues)
		}
	}
}

func TestCandlesAndTicksRoutes(t *testing.T) {
	logx.Init("silent", "test")
	prev := config.E
	config.E = &config.Env{NodeEnv: "test", JWTSecret: strings.Repeat("k", 48), JWTAccessTTL: "15m"}
	t.Cleanup(func() { config.E = prev })
	testsupport.LiveStores(t)
	ctx := context.Background()

	asset := models.Asset{ID: bson.NewObjectID(), Symbol: "TEST/MD", Name: "Test", Category: models.AssetCategoryCrypto,
		Active: true, ProviderSymbol: "testmd", Provider: "simulated", PricePrecision: 2}
	if _, err := mongox.C(models.CollAssets).InsertOne(ctx, asset); err != nil {
		t.Fatal(err)
	}
	// Stored minutes 10:00 .. 10:29; minute i opens at i and closes at i + 0.25.
	base := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC).UnixMilli()
	minute := func(i int64) int64 { return base + i*60_000 }
	var stored []any
	for i := int64(0); i < 30; i++ {
		f := float64(i)
		stored = append(stored, models.Candle{AssetID: asset.ID, Symbol: asset.Symbol, Interval: 60, OpenTime: minute(i),
			Open: f, High: f + 0.5, Low: f - 0.5, Close: f + 0.25, Ticks: 1})
	}
	if _, err := mongox.C(models.CollCandles).InsertMany(ctx, stored); err != nil {
		t.Fatal(err)
	}
	// A minute of ticks, one a second, the last one now; tick k is priced 100 + k.
	now := util.NowMs()
	t0 := now - now%1000 - 59_000
	key := config.RedisKeys.TickHistory(asset.ID.Hex())
	t.Cleanup(func() { redisx.R().Del(context.Background(), key) })
	for k := int64(0); k < 60; k++ {
		ts := t0 + k*1000
		member, _ := json.Marshal(models.NormalisedTick{
			MarketTick: models.MarketTick{Asset: asset.Symbol, Price: float64(100 + k), Timestamp: ts, ReceivedAt: ts + 5},
			AssetID:    asset.ID.Hex(), Symbol: asset.Symbol, ProcessedAt: ts + 6, FeedLatencyMs: 5, ProcessingLatencyMs: 1, TotalLatencyMs: 6,
		})
		if err := redisx.R().ZAdd(ctx, key, redis.Z{Score: float64(ts), Member: string(member)}).Err(); err != nil {
			t.Fatal(err)
		}
	}
	// Redis is shared with the dev server: expire the key even if cleanup never runs.
	redisx.R().PExpire(ctx, key, 5*time.Minute)

	router := chi.NewRouter()
	router.Route("/market-data", Mount)
	token, err := authtoken.SignAccess(bson.NewObjectID().Hex(), config.RoleUser, nil, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) (int, json.RawMessage) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/market-data/"+asset.ID.Hex()+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		return rec.Code, env.Data
	}
	candleList := func(query string) []models.CandleView {
		t.Helper()
		code, data := get("/candles?" + query)
		if code != http.StatusOK || !strings.HasPrefix(string(data), "[") {
			t.Fatalf("candles?%s: %d %s", query, code, data)
		}
		var out []models.CandleView
		_ = json.Unmarshal(data, &out)
		return out
	}
	tickList := func(query string) ([]models.NormalisedTick, []json.RawMessage) {
		t.Helper()
		code, data := get("/ticks?" + query)
		if code != http.StatusOK || !strings.HasPrefix(string(data), "[") {
			t.Fatalf("ticks?%s: %d %s", query, code, data)
		}
		var out []models.NormalisedTick
		var raw []json.RawMessage
		_ = json.Unmarshal(data, &out)
		_ = json.Unmarshal(data, &raw)
		return out, raw
	}
	times := func(cs []models.CandleView) []int64 {
		out := make([]int64, len(cs))
		for i, c := range cs {
			out[i] = c.Time
		}
		return out
	}
	// sameTimes: count consecutive stored minutes starting at minute(from).
	sameTimes := func(label string, got []int64, from, count int64) {
		t.Helper()
		if int64(len(got)) != count {
			t.Fatalf("%s: %d candles, want %d", label, len(got), count)
		}
		for i, v := range got {
			if v != minute(from+int64(i)) {
				t.Fatalf("%s: candle %d at %v", label, i, time.UnixMilli(v).UTC())
			}
		}
	}

	// ── candles ──────────────────────────────────────────────────────────
	// Without `to`: unchanged, the latest candles.
	sameTimes("latest", times(candleList("limit=10")), 20, 10)
	// Up to a time mid-minute: the minutes that opened by then.
	to := minute(14) + 30_000
	upTo := candleList("limit=10&to=" + ms(to))
	sameTimes("to epoch ms", times(upTo), 5, 10)
	if last := upTo[9]; last != (models.CandleView{Time: minute(14), Open: 14, High: 14.5, Low: 13.5, Close: 14.25}) {
		t.Fatalf("last candle up to %d: %+v", to, last)
	}
	sameTimes("to ISO", times(candleList("limit=10&to="+time.UnixMilli(to).UTC().Format("2006-01-02T15:04:05.000Z"))), 5, 10)
	sameTimes("to on an open time", times(candleList("limit=10&to="+ms(minute(14)))), 5, 10)
	if got := candleList("limit=10&to=" + ms(base-1)); len(got) != 0 {
		t.Fatalf("before any data: %+v", got)
	}
	// Folded 5-minute candles: the last one holds `to` and only the minutes up to it.
	folded := candleList("interval=300&limit=10&to=" + ms(minute(12)))
	want := []models.CandleView{
		{Time: minute(0), Open: 0, High: 4.5, Low: -0.5, Close: 4.25},
		{Time: minute(5), Open: 5, High: 9.5, Low: 4.5, Close: 9.25},
		{Time: minute(10), Open: 10, High: 12.5, Low: 9.5, Close: 12.25},
	}
	if len(folded) != 3 || folded[0] != want[0] || folded[1] != want[1] || folded[2] != want[2] {
		t.Fatalf("folded up to 10:12: %+v", folded)
	}
	if got := times(candleList("interval=300&limit=10")); len(got) != 6 || got[0] != minute(0) || got[5] != minute(25) {
		t.Fatalf("folded latest: %v", got)
	}
	// Sub-minute candles come from the ticks at or before `to`.
	tickTo := t0 + 30_500
	fine := candleList("interval=5&limit=100&to=" + ms(tickTo))
	if len(fine) == 0 || fine[len(fine)-1].Close != 130 {
		t.Fatalf("5s candles up to tick 30: %+v", fine)
	}
	for _, c := range fine {
		if c.Time > tickTo || c.High > 130 {
			t.Fatalf("5s candle past `to`: %+v", c)
		}
	}
	if latest := candleList("interval=5&limit=100"); len(latest) == 0 || latest[len(latest)-1].Close != 159 {
		t.Fatalf("5s candles without `to`: %+v", latest)
	}

	// ── ticks ────────────────────────────────────────────────────────────
	all, allRaw := tickList("")
	if len(all) != 60 {
		t.Fatalf("default window: %d ticks", len(all))
	}
	window := func(query string, firstK, count int64) {
		t.Helper()
		got, raw := tickList(query)
		if int64(len(got)) != count {
			t.Fatalf("ticks?%s: %d ticks, want %d", query, len(got), count)
		}
		for i := range got {
			// Byte for byte what the window-less request returns for that tick.
			if string(raw[i]) != string(allRaw[firstK+int64(i)]) {
				t.Fatalf("ticks?%s: tick %d is %s, want %s", query, i, raw[i], allRaw[firstK+int64(i)])
			}
		}
	}
	window("from="+ms(t0+10_000)+"&to="+ms(t0+30_000), 10, 21)
	window("from="+ms(t0+10_000)+"&to="+ms(t0+10_000+15*60_000), 10, 50) // exactly 15 minutes is allowed
	window("from="+ms(t0+50_000), 50, 10)
	window("to="+ms(t0+30_000)+"&windowMs=5000", 25, 6)
	window("to="+ms(t0+30_000), 0, 31)
	if old, _ := tickList("from=" + ms(now-2*3_600_000) + "&to=" + ms(now-2*3_600_000+600_000)); len(old) != 0 {
		t.Fatalf("outside the retention: %d ticks", len(old))
	}
	if code, _ := get("/ticks?from=" + ms(t0) + "&to=" + ms(t0+15*60_000+1)); code != http.StatusUnprocessableEntity {
		t.Fatalf("16-minute window: %d", code)
	}
}
