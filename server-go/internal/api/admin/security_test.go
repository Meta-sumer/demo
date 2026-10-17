package admin

import (
	"bytes"
	"context"
	"crypto/sha1"
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

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/audit"
	authsvc "binarytrading/internal/service/auth"
	"binarytrading/internal/testsupport"
)

const pw = "Admin-Test-Pass-1"

type harness struct {
	t      *testing.T
	router http.Handler
	ip     string
	ids    []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logx.Init("silent", "test")
	prev := config.E
	config.E = &config.Env{
		NodeEnv: "test", JWTSecret: strings.Repeat("e", 48), JWTRefreshSecret: strings.Repeat("f", 48),
		JWTAccessTTL: "15m", JWTRefreshTTL: "30d", BcryptRounds: 8, Admin2FARequired: true,
		Admin2FAIssuer: "Test Admin", WSMaxConnectionsPerUser: 5, WSMaxConnectionsTotal: 100,
	}
	t.Cleanup(func() { config.E = prev })
	testsupport.LiveStores(t)
	r := chi.NewRouter()
	r.Route("/admin", Mount)
	h := &harness{t: t, router: r, ip: fmt.Sprintf("198.18.%d.%d", rand.IntN(250)+1, rand.IntN(250)+1)}
	// Redis is shared: remove the rate-limit and session keys this test made.
	t.Cleanup(func() {
		rc := redisx.R()
		if rc == nil {
			return
		}
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
	})
	return h
}

func (h *harness) user(role string) models.User {
	h.t.Helper()
	hash, err := authsvc.HashPassword(pw)
	if err != nil {
		h.t.Fatal(err)
	}
	id := bson.NewObjectID()
	now := mongox.Now()
	u := models.User{
		ID: id, Email: "a" + id.Hex() + "@example.test", Username: "a" + id.Hex()[14:], PasswordHash: hash,
		Role: role, Permissions: config.PermissionsForRole(role, nil), Status: models.UserStatusActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := mongox.C(models.CollUsers).InsertOne(context.Background(), u); err != nil {
		h.t.Fatal(err)
	}
	h.ids = append(h.ids, id.Hex())
	return u
}

// token signs a 2FA-verified session for u directly (the HTTP sign-in is
// exercised by its own test).
func (h *harness) token(u models.User) string {
	h.t.Helper()
	s, err := authsvc.IssueSession(context.Background(), u.ID.Hex(), u.Role, u.Permissions, authsvc.SessionContext{}, "")
	if err != nil {
		h.t.Fatal(err)
	}
	return s.Tokens.AccessToken
}

func (h *harness) do(method, path, token string, body any) (int, map[string]json.RawMessage) {
	h.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.RemoteAddr = h.ip + ":5000"
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	var env map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env
}

func keys(raw json.RawMessage) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	return m
}

// waitAudit polls for an audit row (most are written fire-and-forget).
func waitAudit(t *testing.T, filter bson.M) bson.M {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var row bson.M
		err := mongox.C(models.CollAuditLogs).FindOne(context.Background(), filter).Decode(&row)
		if err == nil {
			return row
		}
		if time.Now().After(deadline) {
			t.Fatalf("no audit row matching %v", filter)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSupportCannotReadExposureOrPnl(t *testing.T) {
	h := newHarness(t)
	support := h.token(h.user(config.RoleSupport))
	risk := h.token(h.user(config.RoleRiskManager))

	if code, _ := h.do(http.MethodGet, "/admin/exposure", support, nil); code != http.StatusForbidden {
		t.Fatalf("SUPPORT GET /admin/exposure: %d", code)
	}
	if code, _ := h.do(http.MethodGet, "/admin/reports/pnl", support, nil); code != http.StatusForbidden {
		t.Fatalf("SUPPORT GET /admin/reports/pnl: %d", code)
	}
	code, env := h.do(http.MethodGet, "/admin/dashboard", support, nil)
	if code != http.StatusOK {
		t.Fatalf("SUPPORT dashboard: %d", code)
	}
	d := keys(env["data"])
	for _, k := range []string{"currentExposure", "currentNetExposure", "potentialLiability", "hedgedExposure",
		"unhedgedExposure", "todayPlatformResult", "totalCustomerBalance", "totalLockedBalance"} {
		if _, ok := d[k]; ok {
			t.Errorf("SUPPORT dashboard exposes %s", k)
		}
	}
	for _, k := range []string{"openTrades", "todayVolume", "pendingDeposits", "pendingWithdrawals", "redacted"} {
		if _, ok := d[k]; !ok {
			t.Errorf("SUPPORT dashboard lost %s", k)
		}
	}
	if !strings.Contains(string(d["redacted"]), `"exposure"`) || !strings.Contains(string(d["redacted"]), `"pnl"`) {
		t.Errorf("redacted %s", d["redacted"])
	}
	_, env = h.do(http.MethodGet, "/admin/dashboard/series?days=2", support, nil)
	if strings.Contains(string(env["data"]), "platformResult") || strings.Contains(string(env["data"]), "customerPayout") {
		t.Errorf("SUPPORT series exposes P&L: %s", env["data"])
	}
	_, env = h.do(http.MethodGet, "/admin/dashboard/assets?days=2", support, nil)
	if strings.Contains(string(env["data"]), "platformResult") {
		t.Errorf("SUPPORT asset volume exposes P&L: %s", env["data"])
	}

	code, env = h.do(http.MethodGet, "/admin/dashboard", risk, nil)
	if code != http.StatusOK {
		t.Fatalf("RISK dashboard: %d", code)
	}
	d = keys(env["data"])
	for _, k := range []string{"currentExposure", "potentialLiability", "todayPlatformResult"} {
		if _, ok := d[k]; !ok {
			t.Errorf("RISK_MANAGER dashboard lacks %s", k)
		}
	}
}

func TestAdminChangesAreAudited(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	superU := h.user(config.RoleSuperAdmin)
	super := h.token(superU)
	admin := h.token(h.user(config.RoleAdmin))
	customer := h.user(config.RoleUser)
	finance := h.user(config.RoleFinance)

	// Status change: audited with the revocation it caused.
	code, _ := h.do(http.MethodPatch, "/admin/users/"+customer.ID.Hex(), admin, map[string]any{"status": "SUSPENDED"})
	if code != http.StatusOK {
		t.Fatalf("suspend: %d", code)
	}
	row := waitAudit(t, bson.M{"action": audit.UserStatusChanged, "resourceId": customer.ID.Hex()})
	// mongox decodes embedded documents as plain maps (DefaultDocumentMap), not bson.M.
	after, _ := row["after"].(map[string]any)
	if after["status"] != "SUSPENDED" || after["sessionsRevoked"] == nil {
		t.Fatalf("status audit after=%v", after)
	}

	// ADMIN may not touch an administrative account.
	if code, _ := h.do(http.MethodPatch, "/admin/users/"+finance.ID.Hex(), admin, map[string]any{"status": "SUSPENDED"}); code != http.StatusForbidden {
		t.Fatalf("ADMIN suspending FINANCE: %d", code)
	}

	// Forced sign-out.
	code, _ = h.do(http.MethodPost, "/admin/security/users/"+customer.ID.Hex()+"/sessions/revoke", admin, map[string]any{"reason": "ticket 42"})
	if code != http.StatusOK {
		t.Fatalf("revoke sessions: %d", code)
	}
	waitAudit(t, bson.M{"action": audit.UserSessionsRevoked, "resourceId": customer.ID.Hex(), "reason": "ticket 42"})

	// 2FA reset: SUPER_ADMIN only, audited.
	if code, _ := h.do(http.MethodPost, "/admin/security/admins/"+finance.ID.Hex()+"/2fa/reset", admin, map[string]any{"reason": "lost phone"}); code != http.StatusForbidden {
		t.Fatalf("ADMIN resetting 2FA: %d", code)
	}
	code, _ = h.do(http.MethodPost, "/admin/security/admins/"+finance.ID.Hex()+"/2fa/reset", super, map[string]any{"reason": "lost phone"})
	if code != http.StatusOK {
		t.Fatalf("SUPER_ADMIN 2FA reset: %d", code)
	}
	waitAudit(t, bson.M{"action": audit.AdminMFAReset, "resourceId": finance.ID.Hex(), "actorId": superU.ID})

	// A reconciliation run is attributed.
	code, _ = h.do(http.MethodPost, "/admin/reconciliation/run", admin, nil)
	if code != http.StatusOK {
		t.Fatalf("reconciliation run: %d", code)
	}
	waitAudit(t, bson.M{"action": audit.ReconciliationRun})

	// The super admin listing shows every admin account.
	code, env := h.do(http.MethodGet, "/admin/security/admins", super, nil)
	if code != http.StatusOK || !strings.Contains(string(env["data"]), finance.Email) {
		t.Fatalf("admin listing: %d %s", code, env["data"])
	}
	if code, _ := h.do(http.MethodGet, "/admin/security/admins", admin, nil); code != http.StatusForbidden {
		t.Fatalf("ADMIN listing admins: %d", code)
	}
	_ = ctx
}

func TestAdminSignInWithEnrolmentOverHTTP(t *testing.T) {
	h := newHarness(t)
	finance := h.user(config.RoleFinance)
	customer := h.user(config.RoleUser)

	if code, _ := h.do(http.MethodPost, "/admin/login", "", map[string]any{"email": customer.Email, "password": pw}); code != http.StatusForbidden {
		t.Fatalf("customer on /admin/login: %d", code)
	}
	waitAudit(t, bson.M{"action": audit.AdminLoginFailed, "resourceId": customer.ID.Hex()})

	code, env := h.do(http.MethodPost, "/admin/login", "", map[string]any{"email": finance.Email, "password": pw})
	if code != http.StatusOK {
		t.Fatalf("admin login: %d", code)
	}
	var ch struct {
		MFARequired       bool   `json:"mfaRequired"`
		EnrolmentRequired bool   `json:"enrolmentRequired"`
		MFAToken          string `json:"mfaToken"`
		Tokens            any    `json:"tokens"`
	}
	_ = json.Unmarshal(env["data"], &ch)
	if !ch.MFARequired || !ch.EnrolmentRequired || ch.MFAToken == "" || ch.Tokens != nil {
		t.Fatalf("challenge %s", env["data"])
	}
	code, env = h.do(http.MethodPost, "/admin/login/2fa/enrol", "", map[string]any{"mfaToken": ch.MFAToken})
	if code != http.StatusOK {
		t.Fatalf("enrol: %d %s", code, env["error"])
	}
	var enr authsvc.MFAEnrolment
	_ = json.Unmarshal(env["data"], &enr)
	key, err := authsvc.DecodeSecret(enr.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if code, env = h.do(http.MethodPost, "/admin/login/2fa/enrol/confirm", "", map[string]any{"mfaToken": ch.MFAToken, "code": "abc"}); code != http.StatusBadRequest {
		t.Fatalf("bad code: %d %s", code, env["error"])
	}
	code, env = h.do(http.MethodPost, "/admin/login/2fa/enrol/confirm", "", map[string]any{
		"mfaToken": ch.MFAToken, "code": authsvc.TOTPAt(key, time.Now(), 30, 6, sha1.New),
	})
	if code != http.StatusOK {
		t.Fatalf("confirm: %d %s", code, env["error"])
	}
	var done struct {
		Tokens struct {
			AccessToken string `json:"accessToken"`
		} `json:"tokens"`
		RecoveryCodes []string `json:"recoveryCodes"`
	}
	_ = json.Unmarshal(env["data"], &done)
	if done.Tokens.AccessToken == "" || len(done.RecoveryCodes) != 10 {
		t.Fatalf("confirm result %s", env["data"])
	}
	code, env = h.do(http.MethodGet, "/admin/security/2fa", done.Tokens.AccessToken, nil)
	if code != http.StatusOK || !strings.Contains(string(env["data"]), `"enabled":true`) {
		t.Fatalf("own 2FA status: %d %s", code, env["data"])
	}
	waitAudit(t, bson.M{"action": audit.AdminMFAChallenge, "resourceId": finance.ID.Hex()})
	waitAudit(t, bson.M{"action": audit.AdminMFAEnrolled, "resourceId": finance.ID.Hex()})
	waitAudit(t, bson.M{"action": audit.AdminLogin, "resourceId": finance.ID.Hex()})
}
