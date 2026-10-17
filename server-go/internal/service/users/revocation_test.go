package users

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/ws"
	authsvc "binarytrading/internal/service/auth"
	"binarytrading/internal/testsupport"
)

func setupRevocation(t *testing.T) {
	t.Helper()
	logx.Init("silent", "test")
	prev := config.E
	config.E = &config.Env{
		NodeEnv: "test", JWTSecret: strings.Repeat("c", 48), JWTRefreshSecret: strings.Repeat("d", 48),
		JWTAccessTTL: "15m", JWTRefreshTTL: "30d", BcryptRounds: 8, Admin2FARequired: true,
		WSMaxConnectionsPerUser: 5, WSMaxConnectionsTotal: 1000,
	}
	t.Cleanup(func() { config.E = prev })
	testsupport.LiveStores(t)
}

func insertUser(t *testing.T, role string) models.User {
	t.Helper()
	id := bson.NewObjectID()
	now := mongox.Now()
	u := models.User{
		ID: id, Email: "r" + id.Hex() + "@example.test", Username: "r" + id.Hex()[14:], Role: role,
		Permissions: []string{}, Status: models.UserStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := mongox.C(models.CollUsers).InsertOne(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if rc := redisx.R(); rc != nil {
			rc.Del(context.Background(), config.SecurityRedisKeys.SessionState(id.Hex()))
		}
	})
	return u
}

func session(t *testing.T, u models.User) authsvc.IssuedSession {
	t.Helper()
	s, err := authsvc.IssueSession(context.Background(), u.ID.Hex(), u.Role, nil, authsvc.SessionContext{}, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// protected is any authenticated route.
var protected = httpx.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}))

func call(token string) (int, string) {
	r := httptest.NewRequest(http.MethodGet, "/me", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	protected.ServeHTTP(rec, r)
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec.Code, env.Error.Code
}

func status(s string) *string { return &s }

func adminActor(t *testing.T, role string) UpdateActor {
	a := insertUser(t, role)
	return UpdateActor{UserID: a.ID.Hex(), Role: role, Permissions: config.PermissionsForRole(role, nil)}
}

func TestSuspensionKillsAccessTokenImmediately(t *testing.T) {
	setupRevocation(t)
	ctx := context.Background()
	customer := insertUser(t, config.RoleUser)
	s := session(t, customer)
	if code, _ := call(s.Tokens.AccessToken); code != http.StatusNoContent {
		t.Fatalf("live token: %d", code)
	}

	_, after, rev, err := AdminUpdateUserBy(ctx, customer.ID.Hex(), AdminUpdateUserInput{Status: status("SUSPENDED")}, adminActor(t, config.RoleAdmin))
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "SUSPENDED" || rev == nil || rev.RefreshTokensRevoked != 1 {
		t.Fatalf("after %s, revocation %+v", after.Status, rev)
	}
	// The unexpired access token is refused on its very next use.
	if code, errCode := call(s.Tokens.AccessToken); code != http.StatusUnauthorized || errCode != apperr.TokenInvalid {
		t.Fatalf("suspended user's token: %d %s, want 401 TOKEN_INVALID", code, errCode)
	}
	// So is the refresh token, and reactivation does not bring it back.
	if _, err := authsvc.Refresh(ctx, s.Tokens.RefreshToken, authsvc.SessionContext{}); err == nil {
		t.Fatal("refresh after suspension succeeded")
	}
	if _, _, _, err := AdminUpdateUserBy(ctx, customer.ID.Hex(), AdminUpdateUserInput{Status: status("ACTIVE")}, adminActor(t, config.RoleAdmin)); err != nil {
		t.Fatal(err)
	}
	if code, _ := call(s.Tokens.AccessToken); code != http.StatusUnauthorized {
		t.Fatalf("old token after reactivation: %d", code)
	}
	// A status edited behind the API's back is caught once the cache entry
	// goes (at most authtoken.SessionCacheTTL later).
	fresh := session(t, customer)
	if _, err := mongox.C(models.CollUsers).UpdateOne(ctx, bson.M{"_id": customer.ID}, bson.M{"$set": bson.M{"status": "CLOSED"}}); err != nil {
		t.Fatal(err)
	}
	authtoken.ForgetSessionState(ctx, customer.ID.Hex())
	if code, errCode := call(fresh.Tokens.AccessToken); code != http.StatusForbidden || errCode != apperr.AccountDisabled {
		t.Fatalf("closed account: %d %s", code, errCode)
	}
}

func TestAdminUpdateLeastPrivilege(t *testing.T) {
	setupRevocation(t)
	ctx := context.Background()
	customer := insertUser(t, config.RoleUser)
	support := adminActor(t, config.RoleSupport)
	admin := adminActor(t, config.RoleAdmin)
	super := adminActor(t, config.RoleSuperAdmin)

	cases := []struct {
		name   string
		target string
		in     AdminUpdateUserInput
		actor  UpdateActor
		want   string
	}{
		{"support cannot suspend", customer.ID.Hex(), AdminUpdateUserInput{Status: status("SUSPENDED")}, support, apperr.Forbidden},
		{"admin cannot change roles", customer.ID.Hex(), AdminUpdateUserInput{Role: status(config.RoleAdmin)}, admin, apperr.Forbidden},
		{"admin cannot suspend an admin", super.UserID, AdminUpdateUserInput{Status: status("SUSPENDED")}, admin, apperr.Forbidden},
		{"no self-suspension", super.UserID, AdminUpdateUserInput{Status: status("SUSPENDED")}, super, apperr.Forbidden},
		{"unknown permission", customer.ID.Hex(), AdminUpdateUserInput{Permissions: &[]string{"admin:manage"}}, super, apperr.ValidationError},
	}
	for _, c := range cases {
		_, _, _, err := AdminUpdateUserBy(ctx, c.target, c.in, c.actor)
		if !apperr.HasCode(err, c.want) {
			t.Errorf("%s: got %v, want %s", c.name, err, c.want)
		}
	}

	// A promotion ends the access token (the next refresh signs the new role).
	s := session(t, customer)
	_, _, rev, err := AdminUpdateUserBy(ctx, customer.ID.Hex(), AdminUpdateUserInput{Role: status(config.RoleSupport)}, super)
	if err != nil || rev == nil {
		t.Fatalf("promotion: %v %+v", err, rev)
	}
	if code, _ := call(s.Tokens.AccessToken); code != http.StatusUnauthorized {
		t.Fatalf("token after a role change: %d", code)
	}
	// The promoted account now needs 2FA, which its session never passed.
	if _, err := authsvc.Refresh(ctx, s.Tokens.RefreshToken, authsvc.SessionContext{}); !apperr.HasCode(err, apperr.TokenInvalid) {
		t.Fatalf("refresh after promotion to an admin role: %v", err)
	}
}

// readEvent reads frames until one with the event arrives or the deadline.
func readEvent(t *testing.T, c *websocket.Conn, event string, within time.Duration) (map[string]any, error) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(within))
	for {
		_, raw, err := c.ReadMessage()
		if err != nil {
			return nil, err
		}
		var f struct {
			Event   string         `json:"event"`
			Payload map[string]any `json:"payload"`
		}
		if json.Unmarshal(raw, &f) == nil && f.Event == event {
			return f.Payload, nil
		}
	}
}

func TestSuspensionClosesWebsocketImmediately(t *testing.T) {
	setupRevocation(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)
	t.Cleanup(ws.Stop)
	srv := httptest.NewServer(http.HandlerFunc(ws.Handler))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?token="

	customer := insertUser(t, config.RoleUser)
	s := session(t, customer)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+s.Tokens.AccessToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := readEvent(t, conn, config.EvConnected, 3*time.Second); err != nil {
		t.Fatalf("connection.ready: %v", err)
	}

	started := time.Now()
	if _, _, _, err := AdminUpdateUserBy(context.Background(), customer.ID.Hex(),
		AdminUpdateUserInput{Status: status("SUSPENDED")}, adminActor(t, config.RoleAdmin)); err != nil {
		t.Fatal(err)
	}
	payload, err := readEvent(t, conn, config.EvError, 3*time.Second)
	if err != nil {
		t.Fatalf("no connection.error after suspension: %v", err)
	}
	if payload["code"] != "SESSION_REVOKED" {
		t.Fatalf("payload %v", payload)
	}
	// Then the server closes the socket with the session-ended code.
	_, _, err = conn.ReadMessage()
	var ce *websocket.CloseError
	if !asCloseError(err, &ce) || ce.Code != 4401 {
		t.Fatalf("close: %v", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("socket closed only after %s", time.Since(started))
	}
	// Reconnecting with the same token is refused at the handshake.
	_, resp, err := websocket.DefaultDialer.Dial(wsURL+s.Tokens.AccessToken, nil)
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("handshake with a revoked token: %v %v", err, resp)
	}
}

func TestWebsocketRevalidatesOnSubscribe(t *testing.T) {
	setupRevocation(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ws.Start(ctx)
	t.Cleanup(ws.Stop)
	srv := httptest.NewServer(http.HandlerFunc(ws.Handler))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws?token="

	risk := insertUser(t, config.RoleRiskManager)
	s := session(t, risk)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL+s.Tokens.AccessToken, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := readEvent(t, conn, config.EvConnected, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	subscribe := func() (map[string]any, error) {
		_ = conn.WriteJSON(map[string]any{"type": "subscribe", "channels": []string{config.ChAdminRisk, config.ChAdminOps}})
		return readEvent(t, conn, config.EvSubscribed, 3*time.Second)
	}
	p, err := subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if acc, _ := p["accepted"].([]any); len(acc) != 2 {
		t.Fatalf("risk manager: accepted %v", p["accepted"])
	}
	// Disabled out of band (no revocation published): the next subscribe
	// re-reads the account and ends the socket.
	if _, err := mongox.C(models.CollUsers).UpdateOne(context.Background(), bson.M{"_id": risk.ID},
		bson.M{"$set": bson.M{"status": "SUSPENDED"}}); err != nil {
		t.Fatal(err)
	}
	authtoken.ForgetSessionState(context.Background(), risk.ID.Hex())
	_ = conn.WriteJSON(map[string]any{"type": "subscribe", "channels": []string{config.ChAdminMarket}})
	payload, err := readEvent(t, conn, config.EvError, 3*time.Second)
	if err != nil || payload["code"] != apperr.AccountDisabled {
		t.Fatalf("subscribe on a disabled account: %v %v", payload, err)
	}
}

func asCloseError(err error, target **websocket.CloseError) bool {
	ce, ok := err.(*websocket.CloseError)
	if ok {
		*target = ce
	}
	return ok
}
