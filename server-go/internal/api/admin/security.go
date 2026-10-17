package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/audit"
	authsvc "binarytrading/internal/service/auth"
	"binarytrading/internal/service/auth/sessions"
)

// mountSecurity is /admin/security: the caller's own two-factor
// authentication, and for a SUPER_ADMIN the 2FA state of every administrative
// account and the reset that clears it. Forced sign-out needs users:suspend.
func mountSecurity(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))
	write := r.With(httpx.RateLimit(httpx.RateLimits.AdminWrite))

	r.Get("/2fa", httpx.H(getOwnMFA))
	write.Post("/2fa/enrol", httpx.H(startOwnMFA))
	write.Post("/2fa/enrol/confirm", httpx.H(confirmOwnMFA))
	write.Post("/2fa/recovery-codes", httpx.H(regenerateRecoveryCodes))

	superAdmin := r.With(httpx.RequireRole(config.RoleSuperAdmin))
	superAdmin.Get("/admins", httpx.H(listAdminMFA))
	superAdmin.With(httpx.RateLimit(httpx.RateLimits.AdminWrite)).Post("/admins/{id}/2fa/reset", httpx.H(resetAdminMFA))

	r.With(httpx.RequirePermission(config.PermUsersWrite, config.PermUsersSuspend), httpx.RateLimit(httpx.RateLimits.AdminWrite)).
		Post("/users/{id}/sessions/revoke", httpx.H(revokeUserSessions))
}

func getOwnMFA(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	s, err := authsvc.GetMFAStatus(r.Context(), actor.ObjectID(), actor.Role)
	if err != nil {
		return err
	}
	return httpx.OK(w, s)
}

func actorEmail(r *http.Request, id bson.ObjectID) string {
	var u struct {
		Email string `bson:"email"`
	}
	_ = mongox.C(models.CollUsers).FindOne(r.Context(), bson.M{"_id": id}).Decode(&u)
	return u.Email
}

func startOwnMFA(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	enr, err := authsvc.StartMFAEnrolment(r.Context(), actor.ObjectID(), actorEmail(r, actor.ObjectID()))
	if err != nil {
		return err
	}
	return httpx.OK(w, enr)
}

// readCode reads {code} (a 6-digit authenticator code).
func readCode(r *http.Request) (string, error) {
	b, err := readBody(r)
	if err != nil {
		return "", err
	}
	code, _ := b.reqStr("code", 6, 16)
	return code, b.err()
}

func confirmOwnMFA(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	code, err := readCode(r)
	if err != nil {
		return err
	}
	codes, err := authsvc.ConfirmMFAEnrolment(r.Context(), actor.ObjectID(), code)
	if err != nil {
		e := entry(r, actor, audit.AdminMFAFailed, "User", actor.UserID)
		e.Failed, e.Reason = true, apperrMessage(err)
		audit.Log(e)
		return err
	}
	// This session just proved the second factor, so it stays refreshable.
	if err := authsvc.MarkSessionMFA(r.Context(), actor.ObjectID(), actor.SessionID); err != nil {
		return err
	}
	e := entry(r, actor, audit.AdminMFAEnrolled, "User", actor.UserID)
	e.After = map[string]any{"during": "self-service"}
	audit.Write(r.Context(), e)
	return httpx.OK(w, map[string]any{"enabled": true, "recoveryCodes": codes})
}

func regenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	code, err := readCode(r)
	if err != nil {
		return err
	}
	codes, err := authsvc.RegenerateRecoveryCodes(r.Context(), actor.ObjectID(), code)
	if err != nil {
		e := entry(r, actor, audit.AdminMFAFailed, "User", actor.UserID)
		e.Failed, e.Reason = true, apperrMessage(err)
		audit.Log(e)
		return err
	}
	e := entry(r, actor, audit.AdminMFARecoveryRegenerate, "User", actor.UserID)
	audit.Write(r.Context(), e)
	return httpx.OK(w, map[string]any{"recoveryCodes": codes})
}

func listAdminMFA(w http.ResponseWriter, r *http.Request) error {
	rows, err := authsvc.ListAdminMFA(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]any{"required": config.Admin2FAEnforced(), "admins": rows})
}

// targetUser loads the {id} account (role and email for the audit row).
func targetUser(r *http.Request) (bson.ObjectID, *models.User, error) {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return id, nil, err
	}
	var u models.User
	err = mongox.C(models.CollUsers).FindOne(r.Context(), bson.M{"_id": id}).Decode(&u)
	if mongox.IsNotFound(err) {
		return id, nil, apperr.NotFoundErr(apperr.NotFound, "User not found")
	}
	if err != nil {
		return id, nil, err
	}
	return id, &u, nil
}

// resetAdminMFA clears an account's enrolment (lost phone) and ends its
// sessions; it enrols again at its next sign-in.
func resetAdminMFA(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, target, err := targetUser(r)
	if err != nil {
		return err
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	reason, _ := b.reqStr("reason", 3, 500)
	if err := b.err(); err != nil {
		return err
	}
	wasEnabled, err := authsvc.ResetMFA(r.Context(), id)
	if err != nil {
		return err
	}
	rev, err := sessions.RevokeAll(r.Context(), id)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.AdminMFAReset, "User", chi.URLParam(r, "id"))
	e.Reason = reason
	e.Before = map[string]any{"mfaEnabled": wasEnabled, "email": target.Email, "role": target.Role}
	e.After = map[string]any{"mfaEnabled": false, "sessionVersion": rev.Version, "refreshTokensRevoked": rev.RefreshTokensRevoked}
	audit.Write(r.Context(), e)
	return httpx.OK(w, map[string]any{"reset": true, "wasEnabled": wasEnabled, "sessionsRevoked": true})
}

// revokeUserSessions signs a user out everywhere (incident response). An
// administrative account can only be signed out by a SUPER_ADMIN.
func revokeUserSessions(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, target, err := targetUser(r)
	if err != nil {
		return err
	}
	if config.IsAdminRole(target.Role) && actor.Role != config.RoleSuperAdmin {
		return apperr.Forbid("Only a super admin can sign out an administrative account")
	}
	b, err := readBody(r)
	if err != nil {
		return err
	}
	reason, _ := b.reqStr("reason", 3, 500)
	if err := b.err(); err != nil {
		return err
	}
	rev, err := sessions.RevokeAll(r.Context(), id)
	if err != nil {
		return err
	}
	e := entry(r, actor, audit.UserSessionsRevoked, "User", chi.URLParam(r, "id"))
	e.Reason = reason
	e.After = map[string]any{"sessionVersion": rev.Version, "refreshTokensRevoked": rev.RefreshTokensRevoked}
	audit.Write(r.Context(), e)
	return httpx.OK(w, map[string]any{"revoked": true, "refreshTokensRevoked": rev.RefreshTokensRevoked})
}

func apperrMessage(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Message
	}
	return err.Error()
}
