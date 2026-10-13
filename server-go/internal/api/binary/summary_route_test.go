package binary

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	bsvc "binarytrading/internal/service/binary"
	"binarytrading/internal/testsupport"
)

type envelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string `json:"code"`
		Details []struct {
			Path string `json:"path"`
		} `json:"details"`
	} `json:"error"`
}

func TestTradeSummaryRoute(t *testing.T) {
	logx.Init("silent", "test")
	prev := config.E
	config.E = &config.Env{NodeEnv: "test", JWTSecret: strings.Repeat("k", 48), JWTAccessTTL: "15m"}
	t.Cleanup(func() { config.E = prev })
	testsupport.LiveStores(t)

	me, other := bson.NewObjectID(), bson.NewObjectID()
	btc, eth := bson.NewObjectID(), bson.NewObjectID()
	day := func(d int) time.Time { return time.Date(2026, 9, d, 12, 0, 0, 0, time.UTC) }
	amt := func(v int64) *int64 { return &v }
	type spec struct {
		user, asset bson.ObjectID
		dir         models.Direction
		account     string // "" = stored before accounts existed, i.e. LIVE
		status      models.TradeStatus
		stake       int64
		profit      int64
		settlement  *int64
		created     time.Time
	}
	up, down := models.DirectionUp, models.DirectionDown
	specs := []spec{
		{me, btc, up, "LIVE", models.TradeStatusOpen, 100_00, 82_00, nil, day(1)},
		{me, btc, down, "", models.TradeStatusSettling, 50_00, 41_00, nil, day(2)},
		{me, btc, up, "LIVE", models.TradeStatusWon, 200_00, 168_00, amt(168_00), day(3)},
		{me, eth, up, "LIVE", models.TradeStatusWon, 100_00, 84_00, nil, day(4)}, // counts its profit
		{me, eth, down, "LIVE", models.TradeStatusLost, 300_00, 250_00, amt(-300_00), day(5)},
		{me, btc, down, "DEMO", models.TradeStatusLost, 40_00, 33_00, nil, day(6)}, // counts minus its stake
		{me, btc, up, "LIVE", models.TradeStatusDraw, 70_00, 58_00, amt(0), day(7)},
		{me, eth, up, "LIVE", models.TradeStatusClosedEarly, 150_00, 120_00, amt(-37_45), day(8)},
		{me, btc, up, "DEMO", models.TradeStatusCancelled, 500_00, 400_00, amt(0), day(9)},
		{other, btc, up, "LIVE", models.TradeStatusWon, 999_00, 800_00, amt(800_00), day(3)},
	}
	docs := make([]any, 0, len(specs))
	for i, s := range specs {
		docs = append(docs, models.BinaryTrade{
			ID: bson.NewObjectID(), Reference: "TRD_test" + strconv.Itoa(i), UserID: s.user, AssetID: s.asset,
			Symbol: "TEST", Direction: s.dir, AccountType: s.account, Stake: s.stake, Currency: "INR",
			Profit: s.profit, Status: s.status, SettlementAmount: s.settlement,
			CreatedAt: s.created, UpdatedAt: s.created,
		})
	}
	if _, err := mongox.C(models.CollBinaryTrades).InsertMany(context.Background(), docs); err != nil {
		t.Fatal(err)
	}

	router := chi.NewRouter()
	router.Route("/binary", Mount)
	token, err := authtoken.SignAccess(me.Hex(), config.RoleUser, nil, "test-session")
	if err != nil {
		t.Fatal(err)
	}
	get := func(path, token string) (int, envelope) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		var env envelope
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: %v: %s", path, err, rec.Body.String())
		}
		return rec.Code, env
	}
	summary := func(query string) bsvc.TradeSummary {
		t.Helper()
		code, env := get("/binary/trades/summary"+query, token)
		if code != http.StatusOK || !env.Success {
			t.Fatalf("summary%s: %d %s", query, code, env.Data)
		}
		var s bsvc.TradeSummary
		if err := json.Unmarshal(env.Data, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// The exact JSON keys the client reads.
	_, env := get("/binary/trades/summary", token)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(env.Data, &raw); err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if want := []string{"cancelled", "count", "draw", "invested", "lost", "netResult", "open", "soldEarly", "won"}; !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys %v", keys)
	}

	cases := []struct {
		query string
		want  bsvc.TradeSummary
	}{
		// 168 + 84 - 300 - 40 + 0 - 37.45; the other user's trade is not counted.
		{"", bsvc.TradeSummary{Count: 9, Open: 2, Won: 2, Lost: 2, Draw: 1, SoldEarly: 1, Cancelled: 1, Invested: 1510_00, NetResult: -125_45}},
		// Paging parameters are not part of a summary.
		{"?page=0&limit=5000", bsvc.TradeSummary{Count: 9, Open: 2, Won: 2, Lost: 2, Draw: 1, SoldEarly: 1, Cancelled: 1, Invested: 1510_00, NetResult: -125_45}},
		// LIVE includes the trade stored without an account type.
		{"?accountType=LIVE", bsvc.TradeSummary{Count: 7, Open: 2, Won: 2, Lost: 1, Draw: 1, SoldEarly: 1, Invested: 970_00, NetResult: -85_45}},
		{"?accountType=DEMO", bsvc.TradeSummary{Count: 2, Lost: 1, Cancelled: 1, Invested: 540_00, NetResult: -40_00}},
		{"?phase=SETTLED", bsvc.TradeSummary{Count: 7, Won: 2, Lost: 2, Draw: 1, SoldEarly: 1, Cancelled: 1, Invested: 1360_00, NetResult: -125_45}},
		{"?phase=OPEN", bsvc.TradeSummary{Count: 2, Open: 2, Invested: 150_00}},
		{"?status=WON", bsvc.TradeSummary{Count: 2, Won: 2, Invested: 300_00, NetResult: 252_00}},
		// An explicit status wins over the phase, as in the list.
		{"?status=LOST&phase=OPEN", bsvc.TradeSummary{Count: 2, Lost: 2, Invested: 340_00, NetResult: -340_00}},
		{"?from=2026-09-03T00:00:00.000Z&to=2026-09-05T12:00:00.000Z", bsvc.TradeSummary{Count: 3, Won: 2, Lost: 1, Invested: 600_00, NetResult: -48_00}},
		{"?from=" + strconv.FormatInt(day(3).UnixMilli(), 10) + "&to=" + strconv.FormatInt(day(5).UnixMilli(), 10),
			bsvc.TradeSummary{Count: 3, Won: 2, Lost: 1, Invested: 600_00, NetResult: -48_00}},
		{"?assetId=" + eth.Hex() + "&direction=UP", bsvc.TradeSummary{Count: 2, Won: 1, SoldEarly: 1, Invested: 250_00, NetResult: 46_55}},
		{"?assetId=" + bson.NewObjectID().Hex(), bsvc.TradeSummary{}},
	}
	for _, c := range cases {
		if got := summary(c.query); got != c.want {
			t.Errorf("summary%s:\n got  %+v\n want %+v", c.query, got, c.want)
		}
	}

	// The list and the summary share one filter: same count, same stakes.
	for _, query := range []string{"", "accountType=LIVE", "phase=SETTLED", "status=WON&direction=UP", "from=2026-09-04&to=2026-09-08"} {
		code, env := get("/binary/trades?limit=100&"+query, token)
		if code != http.StatusOK {
			t.Fatalf("list %s: %d", query, code)
		}
		var page struct {
			Items []models.BinaryTradeView `json:"items"`
			Total int64                    `json:"total"`
		}
		if err := json.Unmarshal(env.Data, &page); err != nil {
			t.Fatal(err)
		}
		var stakes int64
		for _, it := range page.Items {
			stakes += it.Stake
		}
		s := summary("?" + query)
		if page.Total != s.Count || stakes != s.Invested {
			t.Errorf("%q: list total %d stakes %d, summary count %d invested %d", query, page.Total, stakes, s.Count, s.Invested)
		}
	}

	// Same validation as the list.
	for query, path := range map[string]string{
		"?status=NOPE": "status", "?phase=ALL": "phase", "?accountType=PAPER": "accountType",
		"?direction=SIDEWAYS": "direction", "?assetId=xyz": "assetId", "?from=yesterday": "from",
	} {
		code, env := get("/binary/trades/summary"+query, token)
		if code != http.StatusUnprocessableEntity || env.Error == nil || env.Error.Code != "VALIDATION_ERROR" ||
			len(env.Error.Details) != 1 || env.Error.Details[0].Path != path {
			t.Errorf("%s: %d %+v", query, code, env.Error)
		}
	}

	if code, env := get("/binary/trades/summary", ""); code != http.StatusUnauthorized || env.Error == nil || env.Error.Code != "UNAUTHENTICATED" {
		t.Errorf("unauthenticated: %d %+v", code, env.Error)
	}
}
