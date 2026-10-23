package usdt

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	walletapi "binarytrading/internal/api/wallet"
	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	authsvc "binarytrading/internal/service/auth"
	"binarytrading/internal/service/beex"
	usdtsvc "binarytrading/internal/service/usdt"
	"binarytrading/internal/testsupport"
)

const (
	secret = "whsec_route_test_0123456789"
	pw     = "Usdt-Test-Pass-1"
)

type harness struct {
	t      *testing.T
	router http.Handler
	ip     string
	ids    []string
	mock   *beex.Mock
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logx.Init("silent", "test")
	prev := config.E
	config.E = &config.Env{
		NodeEnv: "test", JWTSecret: strings.Repeat("u", 48), JWTRefreshSecret: strings.Repeat("v", 48),
		JWTAccessTTL: "15m", JWTRefreshTTL: "30d", BcryptRounds: 8, Admin2FARequired: false,
		WSMaxConnectionsPerUser: 5, WSMaxConnectionsTotal: 100, MaxRequestBodyBytes: 262_144,
		UsdtEnabled: true, BeexProvider: "mock", BeexWebhookSecret: secret, BeexAcceptTestMode: true,
		BeexPayURLBase: "https://beexpayments.com/pay/", ClientURL: "http://localhost:5180", UsdtDepositCreditBasis: "received",
	}
	testsupport.LiveStores(t)
	models.EnsureIndexes(context.Background())
	if err := models.SeedUsdtNetworks(context.Background()); err != nil {
		t.Fatal(err)
	}
	m := beex.NewMock()
	beex.Set(m)
	usdtsvc.ResetLimitsCache()

	r := chi.NewRouter()
	r.NotFound(httpx.NotFound)
	r.MethodNotAllowed(httpx.NotFound)
	r.Use(httpx.RequestContext, httpx.BodyLimit)
	// The same mounts as cmd/server: the INR wallet and the USDT wallet side
	// by side, the public webhook, and the admin routes under /admin/usdt.
	r.Route("/wallet", walletapi.Mount)
	r.Route("/wallet/usdt", Mount)
	MountWebhook(r)
	r.Route("/admin", func(r chi.Router) {
		r.Route("/usdt", func(r chi.Router) { r.Group(MountAdmin) })
	})
	h := &harness{t: t, router: r, ip: fmt.Sprintf("198.18.%d.%d", rand.IntN(250)+1, rand.IntN(250)+1), mock: m}
	t.Cleanup(func() {
		time.Sleep(300 * time.Millisecond)
		beex.Set(nil)
		usdtsvc.ResetLimitsCache()
		rc := redisx.R()
		if rc != nil {
			ctx := context.Background()
			patterns := []string{"rl:*:ip:" + h.ip + ":*"}
			for _, id := range h.ids {
				patterns = append(patterns, "rl:*:user:"+id+":*")
				rc.Del(ctx, config.SecurityRedisKeys.SessionState(id))
			}
			for _, p := range patterns {
				iter := rc.Scan(ctx, 0, p, 200).Iterator()
				for iter.Next(ctx) {
					rc.Del(ctx, iter.Val())
				}
			}
		}
		config.E = prev
	})
	return h
}

func (h *harness) user(role string) (models.User, string) {
	h.t.Helper()
	hash, err := authsvc.HashPassword(pw)
	if err != nil {
		h.t.Fatal(err)
	}
	id := bson.NewObjectID()
	now := mongox.Now()
	u := models.User{
		ID: id, Email: "u" + id.Hex() + "@example.test", Username: "u" + id.Hex()[14:], PasswordHash: hash,
		Role: role, Permissions: config.PermissionsForRole(role, nil), Status: models.UserStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := mongox.C(models.CollUsers).InsertOne(context.Background(), u); err != nil {
		h.t.Fatal(err)
	}
	h.ids = append(h.ids, id.Hex())
	s, err := authsvc.IssueSession(context.Background(), u.ID.Hex(), u.Role, u.Permissions, authsvc.SessionContext{}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return u, s.Tokens.AccessToken
}

type reply struct {
	code int
	data json.RawMessage
	err  struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
}

func (h *harness) send(method, path, token string, body []byte, header map[string]string) reply {
	h.t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = h.ip + ":5000"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var env struct {
		Data  json.RawMessage `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	out := reply{code: rec.Code, data: env.Data}
	_ = json.Unmarshal(env.Error, &out.err)
	return out
}

func (h *harness) do(method, path, token string, body any, header map[string]string) reply {
	h.t.Helper()
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	return h.send(method, path, token, raw, header)
}

func (h *harness) webhook(body []byte, ts time.Time, signedBody []byte) reply {
	h.t.Helper()
	if signedBody == nil {
		signedBody = body
	}
	return h.send(http.MethodPost, WebhookPath, "", body, map[string]string{
		beex.SignatureHeader: beex.SignatureHeaderValue(secret, ts.Unix(), signedBody),
	})
}

func obj(t *testing.T, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	m := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %s", raw)
	}
	return m
}

func str(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func num(raw json.RawMessage) float64 {
	var f float64
	_ = json.Unmarshal(raw, &f)
	return f
}

func idem() map[string]string {
	return map[string]string{"Idempotency-Key": "idem-" + bson.NewObjectID().Hex()}
}

func TestRoutesWebhookVerifiesTheRawBody(t *testing.T) {
	h := newHarness(t)
	body := []byte(`{"event":"invoice.paid","mode":"test","reference":"IN-X","orderId":"dep-unknown","status":"paid","amountReceived":"10"}`)
	ts := time.Now()
	first := h.webhook(body, ts, nil)
	if first.code != 200 || str(obj(t, first.data)["processingStatus"]) != models.WebhookIgnored {
		t.Fatalf("signed unknown invoice: %d %s %+v", first.code, first.data, first.err)
	}
	again := h.webhook(body, ts, nil)
	if again.code != 200 || string(obj(t, again.data)["replay"]) != "true" {
		t.Fatalf("replayed request: %d %s", again.code, again.data)
	}
	// The same JSON re-encoded is not the signed bytes.
	var v any
	_ = json.Unmarshal(body, &v)
	reencoded, _ := json.MarshalIndent(v, "", " ")
	if r := h.webhook(reencoded, ts, body); r.code != 401 || r.err.Code != "UNAUTHENTICATED" {
		t.Fatalf("re-encoded body: %d %+v", r.code, r.err)
	}
	if r := h.send(http.MethodPost, WebhookPath, "", body, nil); r.code != 401 {
		t.Fatalf("unsigned: %d", r.code)
	}
	if r := h.webhook(body, ts.Add(-10*time.Minute), nil); r.code != 401 {
		t.Fatalf("stale: %d", r.code)
	}
	big := []byte(`{"event":"invoice.paid","pad":"` + strings.Repeat("x", 70<<10) + `"}`)
	if r := h.webhook(big, ts, nil); r.code != 413 {
		t.Fatalf("oversized: %d", r.code)
	}
	if r := h.send(http.MethodGet, WebhookPath, "", nil, nil); r.code != 404 {
		t.Fatalf("GET webhook: %d", r.code)
	}
}

func TestRoutesCustomerWalletContract(t *testing.T) {
	h := newHarness(t)
	_, token := h.user(string(config.RoleUser))
	_, other := h.user(string(config.RoleUser))

	ov := h.do(http.MethodGet, "/wallet/usdt", token, nil, nil)
	if ov.code != 200 {
		t.Fatalf("overview: %d %+v", ov.code, ov.err)
	}
	o := obj(t, ov.data)
	for _, k := range []string{"wallet", "trading", "networks", "enabled", "transferToTradingEnabled"} {
		if _, ok := o[k]; !ok {
			t.Fatalf("overview has no %q: %s", k, ov.data)
		}
	}
	if string(o["enabled"]) != "true" || string(o["transferToTradingEnabled"]) != "false" {
		t.Fatalf("overview flags: %s", ov.data)
	}
	var nets []map[string]any
	_ = json.Unmarshal(o["networks"], &nets)
	if len(nets) != 2 || nets[0]["code"] != "USDTTRC20" || nets[1]["code"] != "USDTBSC" {
		t.Fatalf("networks: %s", o["networks"])
	}
	// The INR wallet route still answers beside it.
	if r := h.do(http.MethodGet, "/wallet", token, nil, nil); r.code != 200 {
		t.Fatalf("GET /wallet: %d", r.code)
	}

	input := map[string]any{"network": "USDTTRC20", "amount": 100_000_000}
	if r := h.do(http.MethodPost, "/wallet/usdt/deposit", token, input, nil); r.code != 422 {
		t.Fatalf("deposit without Idempotency-Key: %d %+v", r.code, r.err)
	}
	if r := h.send(http.MethodPost, "/wallet/usdt/deposit", token, []byte(`{"network":"USDTTRC20","amount":1.5}`), idem()); r.code != 422 {
		t.Fatalf("fractional micro amount: %d", r.code)
	}
	key := idem()
	dep := h.do(http.MethodPost, "/wallet/usdt/deposit", token, input, key)
	if dep.code != 200 {
		t.Fatalf("deposit: %d %+v", dep.code, dep.err)
	}
	d := obj(t, dep.data)
	if str(d["status"]) != "awaiting_payment" || !strings.HasPrefix(str(d["paymentUrl"]), "https://beexpayments.com/pay/") ||
		num(d["amountExpected"]) != 100_000_000 {
		t.Fatalf("deposit view: %s", dep.data)
	}
	if again := h.do(http.MethodPost, "/wallet/usdt/deposit", token, input, key); str(obj(t, again.data)["id"]) != str(d["id"]) {
		t.Fatalf("same key made another deposit: %s", again.data)
	}
	id := str(d["id"])
	if r := h.do(http.MethodGet, "/wallet/usdt/deposits/"+id, other, nil, nil); r.code != 404 {
		t.Fatalf("another customer's deposit: %d", r.code)
	}
	list := h.do(http.MethodGet, "/wallet/usdt/deposits?page=1&limit=15", token, nil, nil)
	if l := obj(t, list.data); num(l["total"]) != 1 || num(l["totalPages"]) != 1 {
		t.Fatalf("deposit list: %s", list.data)
	}

	// BeeX reports the payment through the webhook route.
	paid, _ := json.Marshal(map[string]any{"event": "invoice.paid", "mode": "test", "reference": str(d["reference"]),
		"orderId": str(d["orderId"]), "status": "paid", "amountReceived": "100", "feeAmount": "0", "netAmount": "100"})
	if r := h.webhook(paid, time.Now(), nil); r.code != 200 || str(obj(t, r.data)["processingStatus"]) != models.WebhookProcessed {
		t.Fatalf("paid webhook: %d %s", r.code, r.data)
	}
	bal := obj(t, h.do(http.MethodGet, "/wallet/usdt/balance", token, nil, nil).data)
	if num(bal["balance"]) != 100_000_000 || str(bal["currency"]) != "USDT" || num(bal["decimals"]) != 6 {
		t.Fatalf("balance: %v", bal)
	}

	quote := h.do(http.MethodGet, "/wallet/usdt/withdrawal-quote?network=USDTTRC20&amount=60000000", token, nil, nil)
	q := obj(t, quote.data)
	if quote.code != 200 || string(q["feePercent"]) != "0.5" || num(q["feeAmount"]) != 4_300_000 || string(q["needsReview"]) != "true" {
		t.Fatalf("quote: %d %s", quote.code, quote.data)
	}
	bad := h.do(http.MethodPost, "/wallet/usdt/withdraw", token, map[string]any{"network": "USDTTRC20", "address": "0x123", "amount": 60_000_000}, idem())
	if bad.code != 400 || bad.err.Code != "INVALID_ADDRESS" {
		t.Fatalf("bad address: %d %+v", bad.code, bad.err)
	}
	wd := h.do(http.MethodPost, "/wallet/usdt/withdraw", token, map[string]any{
		"network": "USDTTRC20", "address": h.mockAddress(), "amount": 60_000_000}, idem())
	if wd.code != 200 || str(obj(t, wd.data)["status"]) != "pending_review" {
		t.Fatalf("withdraw: %d %s %+v", wd.code, wd.data, wd.err)
	}
	tx := obj(t, h.do(http.MethodGet, "/wallet/usdt/transactions", token, nil, nil).data)
	if num(tx["total"]) != 2 {
		t.Fatalf("ledger: %s", tx["items"])
	}
	tr := h.do(http.MethodPost, "/wallet/usdt/transfer-to-trading", token, map[string]any{"amount": 1_000_000}, idem())
	if tr.code != 409 || tr.err.Code != "USDT_TRANSFER_DISABLED" {
		t.Fatalf("transfer to trading: %d %+v", tr.code, tr.err)
	}
	if r := h.do(http.MethodGet, "/wallet/usdt", "", nil, nil); r.code != 401 {
		t.Fatalf("anonymous: %d", r.code)
	}
}

// mockAddress is a valid TRON address.
func (h *harness) mockAddress() string {
	inv, err := h.mock.CreateInvoice(context.Background(), beex.CreateInvoiceRequest{
		OrderID: "addr-" + bson.NewObjectID().Hex(), Amount: "10", PayCurrency: "USDTTRC20",
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return inv.PayAddress
}

func TestRoutesAdminPermissionsAndAudit(t *testing.T) {
	h := newHarness(t)
	customer, token := h.user(string(config.RoleUser))
	_, support := h.user(string(config.RoleSupport))
	finance, financeToken := h.user(string(config.RoleFinance))
	_, superToken := h.user(string(config.RoleSuperAdmin))

	// A funded customer with a withdrawal waiting for review.
	dep := obj(t, h.do(http.MethodPost, "/wallet/usdt/deposit", token, map[string]any{"network": "USDTTRC20", "amount": 200_000_000}, idem()).data)
	paid, _ := json.Marshal(map[string]any{"event": "invoice.paid", "mode": "test", "reference": str(dep["reference"]),
		"orderId": str(dep["orderId"]), "status": "paid", "amountReceived": "200"})
	h.webhook(paid, time.Now(), nil)
	wd := obj(t, h.do(http.MethodPost, "/wallet/usdt/withdraw", token, map[string]any{
		"network": "USDTTRC20", "address": h.mockAddress(), "amount": 80_000_000}, idem()).data)
	wid := str(wd["id"])

	if r := h.do(http.MethodGet, "/admin/usdt/summary", token, nil, nil); r.code != 403 {
		t.Fatalf("customer on admin: %d", r.code)
	}
	if r := h.do(http.MethodGet, "/admin/usdt/withdrawals?status=pending_review", support, nil, nil); r.code != 200 ||
		num(obj(t, r.data)["total"]) != 1 {
		t.Fatalf("support list: %d %s", r.code, r.data)
	}
	if r := h.do(http.MethodPost, "/admin/usdt/withdrawals/"+wid+"/approve", support, map[string]any{}, nil); r.code != 403 {
		t.Fatalf("support approve: %d", r.code)
	}
	if r := h.do(http.MethodPost, "/admin/usdt/withdrawals/"+wid+"/reject", financeToken, map[string]any{}, nil); r.code != 422 {
		t.Fatalf("reject without a note: %d", r.code)
	}
	ok := h.do(http.MethodPost, "/admin/usdt/withdrawals/"+wid+"/approve", financeToken, map[string]any{"note": "checked"}, nil)
	if ok.code != 200 || str(obj(t, ok.data)["status"]) != "requested" {
		t.Fatalf("finance approve: %d %s %+v", ok.code, ok.data, ok.err)
	}
	if again := h.do(http.MethodPost, "/admin/usdt/withdrawals/"+wid+"/approve", financeToken, map[string]any{}, nil); again.code != 409 ||
		again.err.Code != "WITHDRAWAL_ALREADY_PROCESSED" {
		t.Fatalf("second approve: %d %+v", again.code, again.err)
	}
	var row bson.M
	if err := mongox.C(models.CollAuditLogs).FindOne(context.Background(), bson.M{
		"action": usdtsvc.AuditWithdrawalApproved, "resourceId": wid, "actorId": finance.ID,
	}).Decode(&row); err != nil {
		t.Fatalf("no audit row for the approval: %v", err)
	}

	detail := obj(t, h.do(http.MethodGet, "/admin/usdt/withdrawals/"+wid, support, nil, nil).data)
	for _, k := range []string{"withdrawal", "user", "events", "ledger"} {
		if _, ok := detail[k]; !ok {
			t.Fatalf("withdrawal detail has no %q", k)
		}
	}
	limits := obj(t, h.do(http.MethodGet, "/admin/usdt/payout-limits", support, nil, nil).data)
	if string(limits["feePercent"]) != "0.5" || num(limits["minWithdrawal"]) != 50_000_000 {
		t.Fatalf("payout limits: %v", limits)
	}
	sum := obj(t, h.do(http.MethodGet, "/admin/usdt/summary", support, nil, nil).data)
	for _, k := range []string{"deposits", "withdrawals", "byNetwork", "walletTotals", "provider", "alerts", "sandbox"} {
		if _, ok := sum[k]; !ok {
			t.Fatalf("summary has no %q", k)
		}
	}

	patch := map[string]any{"depositEnabled": false}
	if r := h.do(http.MethodPatch, "/admin/usdt/networks/USDTBSC", financeToken, patch, nil); r.code != 403 {
		t.Fatalf("finance network change: %d", r.code)
	}
	if r := h.do(http.MethodPatch, "/admin/usdt/networks/USDTBSC", superToken, patch, nil); r.code != 200 ||
		string(obj(t, r.data)["depositEnabled"]) != "false" {
		t.Fatalf("network change: %d %s", r.code, r.data)
	}
	if err := mongox.C(models.CollAuditLogs).FindOne(context.Background(), bson.M{
		"action": usdtsvc.AuditNetworkUpdated, "resourceId": "USDTBSC",
	}).Err(); err != nil {
		t.Fatalf("no audit row for the network change: %v", err)
	}
	if r := h.do(http.MethodGet, "/admin/usdt/alerts?status=open", support, nil, nil); r.code != 200 {
		t.Fatalf("alerts: %d", r.code)
	}
	if r := h.do(http.MethodPost, "/admin/usdt/reconcile", financeToken, nil, nil); r.code != 200 {
		t.Fatalf("reconcile: %d %+v", r.code, r.err)
	}
	events := obj(t, h.do(http.MethodGet, "/admin/usdt/webhook-events", support, nil, nil).data)
	var items []map[string]any
	_ = json.Unmarshal(events["items"], &items)
	if len(items) == 0 {
		t.Fatal("no webhook events listed")
	}
	if r := h.do(http.MethodPost, "/admin/usdt/webhook-events/"+items[0]["id"].(string)+"/reprocess", support, nil, nil); r.code != 403 {
		t.Fatalf("support reprocess: %d", r.code)
	}
	if r := h.do(http.MethodPost, "/admin/usdt/webhook-events/"+items[0]["id"].(string)+"/reprocess", financeToken, nil, nil); r.code != 200 {
		t.Fatalf("finance reprocess: %d %+v", r.code, r.err)
	}
	_ = customer
}
