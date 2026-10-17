// Package auth is the /auth HTTP API (spec section 3; auth.routes.ts and
// auth.controller.ts) plus the admin sign-in route (POST /admin/login from
// admin.routes.ts), which is the ordinary login flow with a role gate.
package auth

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	svc "binarytrading/internal/service/auth"
)

// Mount registers the routes under /auth.
func Mount(r chi.Router) {
	r.With(httpx.RateLimit(httpx.RateLimits.Register)).Post("/register", httpx.H(registerHandler))
	r.With(httpx.RateLimit(httpx.RateLimits.Login)).Post("/login", httpx.H(loginHandler))
	r.With(httpx.RateLimit(httpx.RateLimits.Refresh)).Post("/refresh", httpx.H(refreshHandler))
	r.With(httpx.Authenticate).Post("/logout", httpx.H(logoutHandler))
	r.With(httpx.Authenticate).Get("/me", httpx.H(meHandler))
	r.With(httpx.Authenticate).Post("/change-password", httpx.H(changePasswordHandler))
}

// MountAdminLogin registers POST /login relative to the /admin prefix. It must
// be called from inside the admin router (a second r.Route("/admin") would
// clash in chi).
func MountAdminLogin(r chi.Router) {
	r.With(httpx.RateLimit(httpx.RateLimits.Login)).Post("/login", httpx.H(adminLoginHandler))
	// The second step (admin 2FA), authorised by the single-use mfaToken that
	// /login returned instead of a session.
	mfa := r.With(httpx.RateLimit(httpx.RateLimits.MFA))
	mfa.Post("/login/2fa", httpx.H(adminLoginMFAHandler))
	mfa.Post("/login/2fa/enrol", httpx.H(adminLoginEnrolHandler))
	mfa.Post("/login/2fa/enrol/confirm", httpx.H(adminLoginEnrolConfirmHandler))
}

// ── validation (auth.schemas.ts) ────────────────────────────────────────

var (
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
	lowerRe    = regexp.MustCompile(`[a-z]`)
	upperRe    = regexp.MustCompile(`[A-Z]`)
	digitRe    = regexp.MustCompile(`[0-9]`)
)

// password is the shared password rule set.
func (f *fields) password(key string) string {
	s, ok := f.str(key, true)
	if !ok {
		return ""
	}
	if jsLen(s) < 8 {
		f.v.Add(key, "Password must be at least 8 characters")
	}
	f.max(key, s, 128)
	f.regex(key, s, lowerRe, "Password must contain a lowercase letter")
	f.regex(key, s, upperRe, "Password must contain an uppercase letter")
	f.regex(key, s, digitRe, "Password must contain a digit")
	return s
}

// emailField: z.string().email().max(254).toLowerCase().trim()
func (f *fields) emailField(key string) string {
	s, ok := f.str(key, true)
	if !ok {
		return ""
	}
	f.email(key, s)
	f.max(key, s, 254)
	return strings.TrimSpace(strings.ToLower(s))
}

func parseRegister(r *http.Request) (svc.RegisterInput, error) {
	f, err := decodeFields(r)
	if err != nil {
		return svc.RegisterInput{}, err
	}
	var in svc.RegisterInput
	in.Email = f.emailField("email")
	if s, ok := f.str("username", true); ok {
		f.min("username", s, 3)
		f.max("username", s, 24)
		f.regex("username", s, usernameRe, "Username may only contain letters, digits and underscores")
		in.Username = strings.TrimSpace(s)
	}
	in.Password = f.password("password")
	if s, ok := f.str("fullName", false); ok {
		f.min("fullName", s, 2)
		f.max("fullName", s, 120)
		t := strings.TrimSpace(s)
		in.FullName = &t
	}
	if s, ok := f.str("country", false); ok {
		f.length("country", s, 2)
		u := strings.ToUpper(s)
		in.Country = &u
	}
	return in, f.err()
}

func parseLogin(r *http.Request) (svc.LoginInput, error) {
	f, err := decodeFields(r)
	if err != nil {
		return svc.LoginInput{}, err
	}
	var in svc.LoginInput
	in.Email = f.emailField("email")
	if s, ok := f.str("password", true); ok {
		f.min("password", s, 1)
		f.max("password", s, 128)
		in.Password = s
	}
	return in, f.err()
}

func sessionContext(r *http.Request) svc.SessionContext {
	ip := httpx.ClientIP(r)
	return svc.SessionContext{IP: &ip, UserAgent: httpx.UserAgent(r)}
}

func errMessage(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Message
	}
	return err.Error()
}

// ── handlers (auth.controller.ts) ───────────────────────────────────────

func registerHandler(w http.ResponseWriter, r *http.Request) error {
	in, err := parseRegister(r)
	if err != nil {
		return err
	}
	result, err := svc.Register(r.Context(), in, sessionContext(r))
	if err != nil {
		return err
	}

	e := audit.FromRequest(nil, httpx.AuditMeta(r))
	e.ActorID = result.User.ID
	e.ActorRole = result.User.Role
	e.ActorEmail = result.User.Email
	e.Action = audit.UserRegister
	e.Resource = "User"
	e.ResourceID = result.User.ID
	e.After = map[string]any{"email": result.User.Email, "username": result.User.Username}
	audit.Log(e)

	return httpx.Created(w, result)
}

func loginHandler(w http.ResponseWriter, r *http.Request) error {
	in, err := parseLogin(r)
	if err != nil {
		return err
	}
	result, err := svc.Login(r.Context(), in, sessionContext(r))
	if err != nil {
		e := audit.FromRequest(nil, httpx.AuditMeta(r))
		e.ActorRole = "ANONYMOUS"
		e.ActorEmail = in.Email
		e.Action = audit.UserLoginFailed
		e.Resource = "User"
		e.Failed = true
		e.Reason = errMessage(err)
		audit.Log(e)
		return err
	}

	e := audit.FromRequest(nil, httpx.AuditMeta(r))
	e.ActorID = result.User.ID
	e.ActorRole = result.User.Role
	e.ActorEmail = result.User.Email
	if result.User.Role == config.RoleUser {
		e.Action = audit.UserLogin
	} else {
		e.Action = audit.AdminLogin
	}
	e.Resource = "User"
	e.ResourceID = result.User.ID
	audit.Log(e)

	return httpx.OK(w, result)
}

func refreshHandler(w http.ResponseWriter, r *http.Request) error {
	f, err := decodeFields(r)
	if err != nil {
		return err
	}
	token, ok := f.str("refreshToken", true)
	if ok {
		f.min("refreshToken", token, 10)
	}
	if err := f.err(); err != nil {
		return err
	}
	result, err := svc.Refresh(r.Context(), token, sessionContext(r))
	if err != nil {
		return err
	}

	e := audit.FromRequest(nil, httpx.AuditMeta(r))
	e.ActorID = result.User.ID
	e.ActorRole = result.User.Role
	e.Action = audit.UserTokenRefresh
	e.Resource = "Session"
	e.ResourceID = result.SessionID
	audit.Log(e)

	return httpx.OK(w, result)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	f, err := decodeFields(r)
	if err != nil {
		return err
	}
	in := svc.LogoutInput{UserID: actor.UserID}
	if s, ok := f.str("refreshToken", false); ok {
		f.min("refreshToken", s, 10)
		in.RefreshToken = &s
	}
	if b, ok := f.boolean("allSessions"); ok {
		in.AllSessions = b
	}
	if err := f.err(); err != nil {
		return err
	}
	if err := svc.Logout(r.Context(), in); err != nil {
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.UserLogout
	e.Resource = "Session"
	e.ResourceID = actor.SessionID
	audit.Log(e)

	return httpx.OK(w, map[string]bool{"loggedOut": true})
}

func meHandler(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	result, err := svc.Me(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, result)
}

func changePasswordHandler(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	f, err := decodeFields(r)
	if err != nil {
		return err
	}
	var in svc.ChangePasswordInput
	if s, ok := f.str("currentPassword", true); ok {
		f.min("currentPassword", s, 1)
		f.max("currentPassword", s, 128)
		in.CurrentPassword = s
	}
	in.NewPassword = f.password("newPassword")
	if err := f.err(); err != nil {
		return err
	}
	// Every session of the account ends, this one included; the response
	// carries a fresh session so the device that made the change can stay
	// signed in by storing it (older clients ignore it and sign in again).
	next, err := svc.ChangePasswordAndReissue(r.Context(), actor.UserID, actor.SessionID, in, sessionContext(r), true)
	if err != nil {
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.UserPasswordChanged
	e.Resource = "User"
	e.ResourceID = actor.UserID
	e.After = map[string]any{"sessionsRevoked": true}
	audit.Log(e)

	out := struct {
		Changed   bool            `json:"changed"`
		Tokens    *svc.AuthTokens `json:"tokens,omitempty"`
		SessionID string          `json:"sessionId,omitempty"`
	}{Changed: true}
	if next != nil {
		out.Tokens, out.SessionID = &next.Tokens, next.SessionID
	}
	return httpx.OK(w, out)
}

// adminLoginHandler: admin sign-in is the password step with a role gate, so
// a trading account cannot obtain an admin session through this endpoint.
// When the account needs a second factor the answer is a challenge
// ({mfaRequired: true, mfaToken, enrolmentRequired, ...}) instead of the
// session; the console completes it through /admin/login/2fa[...].
func adminLoginHandler(w http.ResponseWriter, r *http.Request) error {
	in, err := parseLogin(r)
	if err != nil {
		return err
	}
	out, err := svc.BeginAdminLogin(r.Context(), in, sessionContext(r))
	if err != nil {
		e := audit.FromRequest(nil, httpx.AuditMeta(r))
		e.ActorRole = "ANONYMOUS"
		e.ActorEmail = in.Email
		if out.User != nil {
			e.ActorID, e.ActorRole, e.ResourceID = out.User.ID.Hex(), out.User.Role, out.User.ID.Hex()
		}
		e.Action = audit.AdminLoginFailed
		e.Resource = "User"
		e.Failed = true
		e.Reason = errMessage(err)
		if svc.IsNotAdmin(err) {
			e.Reason = "not an administrative account"
			audit.Log(e)
			return apperr.Forbid("This account cannot access the administration panel")
		}
		audit.Log(e)
		return err
	}

	e := audit.FromRequest(nil, httpx.AuditMeta(r))
	e.ActorID = out.User.ID.Hex()
	e.ActorRole = out.User.Role
	e.ActorEmail = out.User.Email
	e.Resource = "User"
	e.ResourceID = out.User.ID.Hex()
	if out.Challenge != nil {
		// The password was right; the session waits for the second factor.
		e.Action = audit.AdminMFAChallenge
		e.After = map[string]any{"enrolmentRequired": out.Challenge.EnrolmentRequired}
		audit.Log(e)
		return httpx.OK(w, out.Challenge)
	}
	e.Action = audit.AdminLogin
	audit.Log(e)
	return httpx.OK(w, out.Session)
}

// parseSecondStep reads {mfaToken, code?, recoveryCode?}.
func parseSecondStep(r *http.Request, allowRecovery bool) (token, code, recovery string, err error) {
	f, err := decodeFields(r)
	if err != nil {
		return "", "", "", err
	}
	if s, ok := f.str("mfaToken", true); ok {
		f.min("mfaToken", s, 10)
		f.max("mfaToken", s, 128)
		token = s
	}
	if s, ok := f.str("code", false); ok {
		f.max("code", s, 16)
		code = s
	}
	if allowRecovery {
		if s, ok := f.str("recoveryCode", false); ok {
			f.max("recoveryCode", s, 64)
			recovery = s
		}
	}
	if strings.TrimSpace(code) == "" && strings.TrimSpace(recovery) == "" {
		f.v.Add("code", "Enter the 6-digit code from your authenticator app")
	}
	return token, code, recovery, f.err()
}

// secondStepAudit starts an audit row for a second-step request.
func secondStepAudit(r *http.Request, step svc.SecondStep, action string) audit.Entry {
	e := audit.FromRequest(nil, httpx.AuditMeta(r))
	e.ActorRole = "ANONYMOUS"
	if step.UserID != "" {
		e.ActorID, e.ActorRole, e.ActorEmail, e.ResourceID = step.UserID, step.Role, step.Email, step.UserID
	}
	e.Action = action
	e.Resource = "User"
	return e
}

func adminLoginMFAHandler(w http.ResponseWriter, r *http.Request) error {
	token, code, recovery, err := parseSecondStep(r, true)
	if err != nil {
		return err
	}
	result, step, err := svc.CompleteAdminLogin(r.Context(), token, code, recovery, sessionContext(r))
	if err != nil {
		e := secondStepAudit(r, step, audit.AdminMFAFailed)
		e.Failed, e.Reason = true, errMessage(err)
		audit.Log(e)
		return err
	}
	if step.Factor.Method == "recovery" {
		e := secondStepAudit(r, step, audit.AdminMFARecoveryCodeUsed)
		e.After = map[string]any{"recoveryCodesRemaining": step.Factor.RecoveryCodesRemaining}
		audit.Log(e)
	}
	e := secondStepAudit(r, step, audit.AdminLogin)
	e.After = map[string]any{"secondFactor": step.Factor.Method}
	audit.Log(e)
	return httpx.OK(w, struct {
		svc.AuthResult
		RecoveryCodesRemaining int `json:"recoveryCodesRemaining"`
	}{result, step.Factor.RecoveryCodesRemaining})
}

func adminLoginEnrolHandler(w http.ResponseWriter, r *http.Request) error {
	f, err := decodeFields(r)
	if err != nil {
		return err
	}
	token, ok := f.str("mfaToken", true)
	if ok {
		f.min("mfaToken", token, 10)
		f.max("mfaToken", token, 128)
	}
	if err := f.err(); err != nil {
		return err
	}
	enr, step, err := svc.StartAdminLoginEnrolment(r.Context(), token)
	if err != nil {
		if step.UserID != "" {
			e := secondStepAudit(r, step, audit.AdminMFAFailed)
			e.Failed, e.Reason = true, errMessage(err)
			audit.Log(e)
		}
		return err
	}
	return httpx.OK(w, enr)
}

func adminLoginEnrolConfirmHandler(w http.ResponseWriter, r *http.Request) error {
	token, code, _, err := parseSecondStep(r, false)
	if err != nil {
		return err
	}
	result, codes, step, err := svc.ConfirmAdminLoginEnrolment(r.Context(), token, code, sessionContext(r))
	if err != nil {
		e := secondStepAudit(r, step, audit.AdminMFAFailed)
		e.Failed, e.Reason = true, errMessage(err)
		audit.Log(e)
		return err
	}
	e := secondStepAudit(r, step, audit.AdminMFAEnrolled)
	e.After = map[string]any{"during": "sign-in"}
	audit.Log(e)
	e = secondStepAudit(r, step, audit.AdminLogin)
	e.After = map[string]any{"secondFactor": "enrol"}
	audit.Log(e)
	return httpx.OK(w, struct {
		svc.AuthResult
		RecoveryCodes []string `json:"recoveryCodes"`
	}{result, codes})
}
