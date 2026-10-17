package auth

import (
	"context"
	"crypto/sha1"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/testsupport"
)

const testPassword = "Correct-Horse-9"

func setupAuth(t *testing.T, require2FA bool) {
	t.Helper()
	logx.Init("silent", "test")
	prev := config.E
	config.E = &config.Env{
		NodeEnv: "test", JWTSecret: strings.Repeat("a", 48), JWTRefreshSecret: strings.Repeat("b", 48),
		JWTAccessTTL: "15m", JWTRefreshTTL: "30d", BcryptRounds: 8, Admin2FARequired: require2FA,
		Admin2FAIssuer: "Test Admin",
	}
	t.Cleanup(func() { config.E = prev })
	testsupport.LiveStores(t)
}

// newUser inserts an active account and cleans its Redis keys afterwards.
func newUser(t *testing.T, role string) models.User {
	t.Helper()
	ctx := context.Background()
	hash, err := HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	id := bson.NewObjectID()
	now := mongox.Now()
	u := models.User{
		ID: id, Email: "u" + id.Hex() + "@example.test", Username: "u" + id.Hex()[16:], PasswordHash: hash,
		Role: role, Permissions: []string{}, Status: models.UserStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := mongox.C(models.CollUsers).InsertOne(ctx, u); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if rc := redisx.R(); rc != nil {
			rc.Del(context.Background(), config.SecurityRedisKeys.SessionState(id.Hex()))
		}
	})
	return u
}

func codeNow(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := DecodeSecret(secret)
	if err != nil {
		t.Fatal(err)
	}
	return TOTPAt(key, at, TOTPPeriod, TOTPDigits, sha1.New)
}

func mustCode(t *testing.T, err error, code string) {
	t.Helper()
	if !apperr.HasCode(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// enrolViaLogin runs the forced enrolment of a first admin sign-in and returns
// the base32 secret and the recovery codes.
func enrolViaLogin(t *testing.T, u models.User) (string, []string, AuthResult) {
	t.Helper()
	ctx := context.Background()
	out, err := BeginAdminLogin(ctx, LoginInput{Email: u.Email, Password: testPassword}, SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Session != nil || out.Challenge == nil || !out.Challenge.EnrolmentRequired {
		t.Fatalf("first admin sign-in must require enrolment, got %+v", out)
	}
	enr, _, err := StartAdminLoginEnrolment(ctx, out.Challenge.MFAToken)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enr.OTPAuthURI, "otpauth://totp/Test%20Admin:") || enr.Digits != 6 || enr.Period != 30 {
		t.Fatalf("enrolment %+v", enr)
	}
	// A wrong code does not enable anything.
	_, _, _, err = ConfirmAdminLoginEnrolment(ctx, out.Challenge.MFAToken, "000000", SessionContext{})
	if err == nil || !apperr.HasCode(err, CodeMFAInvalid) {
		if codeNow(t, enr.Secret, time.Now()) != "000000" {
			t.Fatalf("wrong code: %v", err)
		}
	}
	res, codes, _, err := ConfirmAdminLoginEnrolment(ctx, out.Challenge.MFAToken, codeNow(t, enr.Secret, time.Now()), SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != recoveryCodeCount || res.Tokens.AccessToken == "" {
		t.Fatalf("enrolment result: %d codes", len(codes))
	}
	return enr.Secret, codes, res
}

func TestAdminLoginEnrolmentAndTwoFactorFlow(t *testing.T) {
	setupAuth(t, true)
	ctx := context.Background()
	admin := newUser(t, config.RoleFinance)

	secret, recovery, first := enrolViaLogin(t, admin)
	claims, err := authtoken.VerifyAccess(first.Tokens.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authtoken.CheckSession(ctx, claims); err != nil {
		t.Fatalf("session after enrolment: %v", err)
	}

	// The challenge was single-use.
	if _, _, err := StartAdminLoginEnrolment(ctx, "never-issued-token"); !apperr.HasCode(err, CodeMFAChallengeInvalid) {
		t.Fatalf("unknown challenge: %v", err)
	}

	// Next sign-in: password, then a code.
	out, err := BeginAdminLogin(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Challenge == nil || out.Challenge.EnrolmentRequired || out.Session != nil {
		t.Fatalf("enrolled admin: %+v", out)
	}
	// The code that completed the enrolment was consumed and cannot be replayed;
	// the next step's code is fine.
	next := codeNow(t, secret, time.Now().Add(30*time.Second))
	res, step, err := CompleteAdminLogin(ctx, out.Challenge.MFAToken, next, "", SessionContext{})
	if err != nil {
		t.Fatalf("totp sign-in: %v", err)
	}
	if step.Factor.Method != "totp" || res.Tokens.RefreshToken == "" {
		t.Fatalf("step %+v", step)
	}
	// Replaying the same code on a fresh challenge fails.
	out, _ = BeginAdminLogin(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	_, _, err = CompleteAdminLogin(ctx, out.Challenge.MFAToken, next, "", SessionContext{})
	mustCode(t, err, CodeMFAInvalid)

	// A recovery code works once.
	_, step, err = CompleteAdminLogin(ctx, out.Challenge.MFAToken, "", strings.ToLower(recovery[0]), SessionContext{})
	if err != nil {
		t.Fatalf("recovery code: %v", err)
	}
	if step.Factor.Method != "recovery" || step.Factor.RecoveryCodesRemaining != recoveryCodeCount-1 {
		t.Fatalf("recovery step %+v", step.Factor)
	}
	out, _ = BeginAdminLogin(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	_, _, err = CompleteAdminLogin(ctx, out.Challenge.MFAToken, "", recovery[0], SessionContext{})
	mustCode(t, err, CodeMFAInvalid)
	// ... and the session refreshes, keeping its second-factor mark.
	if _, err := Refresh(ctx, res.Tokens.RefreshToken, SessionContext{}); err != nil {
		t.Fatalf("refresh of a 2FA session: %v", err)
	}

	status, err := GetMFAStatus(ctx, admin.ID, admin.Role)
	if err != nil || !status.Enabled || !status.Required || status.RecoveryCodesRemaining != recoveryCodeCount-1 {
		t.Fatalf("status %+v %v", status, err)
	}
}

func TestChallengeDiesAfterTooManyWrongCodes(t *testing.T) {
	setupAuth(t, true)
	ctx := context.Background()
	admin := newUser(t, config.RoleRiskManager)
	secret, _, _ := enrolViaLogin(t, admin)
	out, err := BeginAdminLogin(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	wrong := "000000"
	if codeNow(t, secret, time.Now()) == wrong {
		wrong = "111111"
	}
	for i := 0; i < mfaChallengeMaxTries; i++ {
		_, _, err := CompleteAdminLogin(ctx, out.Challenge.MFAToken, wrong, "", SessionContext{})
		mustCode(t, err, CodeMFAInvalid)
	}
	_, _, err = CompleteAdminLogin(ctx, out.Challenge.MFAToken, codeNow(t, secret, time.Now().Add(30*time.Second)), "", SessionContext{})
	mustCode(t, err, CodeMFAChallengeInvalid)

	var u models.User
	if err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"_id": admin.ID}).Decode(&u); err != nil {
		t.Fatal(err)
	}
	if u.FailedLoginAttempts != mfaChallengeMaxTries {
		t.Fatalf("wrong codes counted %d times toward the lockout, want %d", u.FailedLoginAttempts, mfaChallengeMaxTries)
	}
}

func TestCustomerLoginUnaffectedAdminNeedsConsole(t *testing.T) {
	setupAuth(t, true)
	ctx := context.Background()
	customer := newUser(t, config.RoleUser)
	res, err := Login(ctx, LoginInput{Email: customer.Email, Password: testPassword}, SessionContext{})
	if err != nil || res.Tokens.AccessToken == "" || res.User.Role != config.RoleUser {
		t.Fatalf("customer login: %v", err)
	}
	// A customer cannot use the admin sign-in at all.
	if _, err := BeginAdminLogin(ctx, LoginInput{Email: customer.Email, Password: testPassword}, SessionContext{}); !IsNotAdmin(err) {
		t.Fatalf("customer on /admin/login: %v", err)
	}
	// An admin cannot skip the second step through the customer endpoint.
	admin := newUser(t, config.RoleSupport)
	_, err = Login(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	mustCode(t, err, CodeMFARequired)
}

func TestTwoFactorNotRequiredWhenDisabledAndNotEnrolled(t *testing.T) {
	setupAuth(t, false)
	ctx := context.Background()
	admin := newUser(t, config.RoleSupport)
	out, err := BeginAdminLogin(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	if err != nil || out.Session == nil || out.Challenge != nil {
		t.Fatalf("2FA off and not enrolled: want a session, got %+v %v", out, err)
	}
	// Voluntary enrolment makes it required for this account.
	enr, err := StartMFAEnrolment(ctx, admin.ID, admin.Email)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ConfirmMFAEnrolment(ctx, admin.ID, codeNow(t, enr.Secret, time.Now())); err != nil {
		t.Fatal(err)
	}
	// The session from before enrolment can no longer be refreshed...
	_, err = Refresh(ctx, out.Session.Tokens.RefreshToken, SessionContext{})
	mustCode(t, err, apperr.TokenInvalid)
	// ... and the next sign-in asks for a code.
	out, err = BeginAdminLogin(ctx, LoginInput{Email: admin.Email, Password: testPassword}, SessionContext{})
	if err != nil || out.Challenge == nil || out.Challenge.EnrolmentRequired {
		t.Fatalf("after voluntary enrolment: %+v %v", out, err)
	}
	// A reset removes it again.
	if was, err := ResetMFA(ctx, admin.ID); err != nil || !was {
		t.Fatalf("reset: %v %v", was, err)
	}
	if on, _ := MFAEnabled(ctx, admin.ID); on {
		t.Fatal("still enabled after reset")
	}
}

func TestRefreshRejectsPreEnrolmentAdminSessionWhenRequired(t *testing.T) {
	setupAuth(t, true)
	ctx := context.Background()
	admin := newUser(t, config.RoleAdmin)
	// A session issued without a second factor (e.g. before 2FA was required).
	s, err := IssueSession(ctx, admin.ID.Hex(), admin.Role, nil, SessionContext{}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Refresh(ctx, s.Tokens.RefreshToken, SessionContext{})
	mustCode(t, err, apperr.TokenInvalid)
}

func TestChangePasswordEndsEverySessionAndReissues(t *testing.T) {
	setupAuth(t, true)
	ctx := context.Background()
	u := newUser(t, config.RoleUser)
	a, _ := Login(ctx, LoginInput{Email: u.Email, Password: testPassword}, SessionContext{})
	b, _ := Login(ctx, LoginInput{Email: u.Email, Password: testPassword}, SessionContext{})

	next, err := ChangePasswordAndReissue(ctx, u.ID.Hex(), a.SessionID,
		ChangePasswordInput{CurrentPassword: testPassword, NewPassword: "Another-Pass-7"}, SessionContext{}, true)
	if err != nil || next == nil {
		t.Fatalf("change password: %v", err)
	}
	for name, tok := range map[string]string{"this device": a.Tokens.AccessToken, "other device": b.Tokens.AccessToken} {
		claims, err := authtoken.VerifyAccess(tok)
		if err != nil {
			t.Fatal(err)
		}
		_, err = authtoken.CheckSession(ctx, claims)
		mustCode(t, err, apperr.TokenInvalid)
		_ = name
	}
	if _, err := Refresh(ctx, b.Tokens.RefreshToken, SessionContext{}); err == nil {
		t.Fatal("the other device's refresh token survived a password change")
	}
	claims, _ := authtoken.VerifyAccess(next.Tokens.AccessToken)
	if _, err := authtoken.CheckSession(ctx, claims); err != nil {
		t.Fatalf("the reissued session must work: %v", err)
	}
}
