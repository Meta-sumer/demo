// Package auth is authentication (spec section 3; modules/auth/auth.service.ts).
//
// Passwords are bcrypt hashes - the plaintext never leaves the request handler.
// Refresh tokens are rotated on every use and stored hashed (SHA-256 hex, as
// the Node server stores them, so sessions move freely between the servers);
// presenting a token that was already rotated revokes the whole family, which
// is the standard defence against a stolen-and-replayed refresh token.
package auth

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/crypto/bcrypt"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/auth/sessions"
	"binarytrading/internal/service/wallets"
)

type SessionContext struct {
	IP        *string
	UserAgent *string
}

type AuthTokens struct {
	AccessToken           string `json:"accessToken"`
	RefreshToken          string `json:"refreshToken"`
	AccessTokenExpiresIn  int64  `json:"accessTokenExpiresIn"`
	RefreshTokenExpiresIn int64  `json:"refreshTokenExpiresIn"`
	TokenType             string `json:"tokenType"`
}

type AuthResult struct {
	User      models.UserView   `json:"user"`
	Wallet    models.WalletView `json:"wallet"`
	Tokens    AuthTokens        `json:"tokens"`
	SessionID string            `json:"sessionId"`
}

type IssuedSession struct {
	Tokens    AuthTokens
	SessionID string
}

type RegisterInput struct {
	Email    string
	Username string
	Password string
	FullName *string
	Country  *string
}

type LoginInput struct {
	Email    string
	Password string
}

type ChangePasswordInput struct {
	CurrentPassword string
	NewPassword     string
}

type LogoutInput struct {
	UserID       string
	RefreshToken *string
	AllSessions  bool
}

type MeResult struct {
	User   models.UserView   `json:"user"`
	Wallet models.WalletView `json:"wallet"`
}

const (
	LoginMaxAttempts = 8
	LoginLockMs      = 15 * 60 * 1000
)

// dummyHash is compared against when the account is unknown so that a missing
// account and a wrong password take the same time (cost 12, as in the Node
// server).
const dummyHash = "$2a$12$J6Dpq5PiC3smF6WYTbfI5OUVdjWME1MtyBy2yJrHEBp1VbNsQYQwm"

// bcryptKey: bcryptjs silently uses only the first 72 bytes; x/crypto rejects
// longer input, so truncate to keep hashes interchangeable.
func bcryptKey(plain string) []byte {
	b := []byte(plain)
	if len(b) > 72 {
		b = b[:72]
	}
	return b
}

func HashPassword(plain string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(bcryptKey(plain), config.E.BcryptRounds)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// ComparePassword reports whether plain matches a bcrypt ($2a/$2b/$2y) hash.
func ComparePassword(plain, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), bcryptKey(plain)) == nil
}

func invalidCredentials() error {
	return apperr.New(apperr.InvalidCredentials, "Email or password is incorrect", 401)
}

func accountDisabled(status models.UserStatus) error {
	return apperr.New(apperr.AccountDisabled, "Account is "+strings.ToLower(string(status)), 403)
}

func users() *mongo.Collection         { return mongox.C(models.CollUsers) }
func refreshTokens() *mongo.Collection { return mongox.C(models.CollRefreshTokens) }

func findUserView(ctx context.Context, id bson.ObjectID) (*models.User, error) {
	var u models.User
	err := users().FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(models.UserPasswordHashExcluded)).Decode(&u)
	if mongox.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func trimPtr(s *string, upper bool) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	if upper {
		v = strings.ToUpper(v)
	}
	return &v
}

func Register(ctx context.Context, in RegisterInput, sc SessionContext) (AuthResult, error) {
	passwordHash, err := HashPassword(in.Password)
	if err != nil {
		return AuthResult{}, err
	}

	now := mongox.Now()
	doc := models.User{
		Email:        strings.ToLower(strings.TrimSpace(in.Email)),
		Username:     strings.TrimSpace(in.Username),
		PasswordHash: passwordHash,
		FullName:     trimPtr(in.FullName, false),
		Country:      trimPtr(in.Country, true),
		Role:         config.RoleUser,
		Permissions:  []string{},
		Status:       models.UserStatusActive,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	res, err := users().InsertOne(ctx, doc)
	if err != nil {
		if mongox.IsDuplicateKey(err) {
			if duplicateOnUsername(err) {
				return AuthResult{}, apperr.ConflictErr(apperr.UsernameTaken, "That username is already taken")
			}
			return AuthResult{}, apperr.ConflictErr(apperr.EmailTaken, "An account with that email already exists")
		}
		return AuthResult{}, err
	}
	userID := res.InsertedID.(bson.ObjectID)

	user, err := findUserView(ctx, userID)
	if err != nil {
		return AuthResult{}, err
	}
	if user == nil {
		return AuthResult{}, apperr.Unauth("Account no longer exists")
	}
	wallet, err := wallets.GetOrCreateWallet(ctx, userID.Hex(), models.AccountLive, "")
	if err != nil {
		return AuthResult{}, err
	}
	session, err := IssueSession(ctx, userID.Hex(), config.RoleUser, nil, sc, "")
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: models.ToUserView(user), Wallet: wallet, Tokens: session.Tokens, SessionID: session.SessionID}, nil
}

// duplicateOnUsername inspects the server's keyPattern (falling back to the
// message) to tell which unique index was hit.
func duplicateOnUsername(err error) bool {
	var we mongo.WriteException
	if ok := asWriteException(err, &we); ok {
		for _, e := range we.WriteErrors {
			if e.Code != 11000 {
				continue
			}
			if e.Raw != nil {
				if kp, lerr := e.Raw.LookupErr("keyPattern"); lerr == nil {
					if doc, ok := kp.DocumentOK(); ok {
						_, uerr := doc.LookupErr("username")
						return uerr == nil
					}
				}
			}
			return strings.Contains(e.Message, "username")
		}
	}
	return strings.Contains(err.Error(), "username_1")
}

func asWriteException(err error, target *mongo.WriteException) bool {
	for e := err; e != nil; {
		if we, ok := e.(mongo.WriteException); ok {
			*target = we
			return true
		}
		if we, ok := e.(*mongo.WriteException); ok && we != nil {
			*target = *we
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}

// Login is POST /auth/login. An account that must pass a second factor
// (administrative roles, see MFARequired) cannot finish here: it gets
// MFA_REQUIRED and signs in through the admin console instead.
func Login(ctx context.Context, in LoginInput, sc SessionContext) (AuthResult, error) {
	user, err := checkPassword(ctx, in)
	if err != nil {
		return AuthResult{}, err
	}
	need, err := secondFactorNeeded(ctx, user)
	if err != nil {
		return AuthResult{}, err
	}
	if need {
		return AuthResult{}, apperr.New(CodeMFARequired,
			"This account uses two-factor authentication. Sign in through the admin console.", 403)
	}
	return finishLogin(ctx, user, sc, nil)
}

func secondFactorNeeded(ctx context.Context, user *models.User) (bool, error) {
	if !config.IsAdminRole(user.Role) {
		return false, nil
	}
	enabled, err := MFAEnabled(ctx, user.ID)
	if err != nil {
		return false, err
	}
	return MFARequired(user.Role, enabled), nil
}

// MFAChallengeView is POST /admin/login's answer when a second step follows
// (instead of the session). mfaToken is single-use and short-lived.
type MFAChallengeView struct {
	MFARequired       bool     `json:"mfaRequired"`
	EnrolmentRequired bool     `json:"enrolmentRequired"`
	MFAToken          string   `json:"mfaToken"`
	ExpiresIn         int64    `json:"expiresIn"`
	Methods           []string `json:"methods"`
	User              struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	} `json:"user"`
}

// AdminLoginOutcome is either a session or a second-step challenge. User is
// set whenever the password was right (for the audit trail).
type AdminLoginOutcome struct {
	User      *models.User
	Session   *AuthResult
	Challenge *MFAChallengeView
}

// BeginAdminLogin is POST /admin/login: the password, then either a session
// (2FA not required and not enrolled) or the second-step challenge. A
// non-administrative account gets errNotAdmin and no session at all.
func BeginAdminLogin(ctx context.Context, in LoginInput, sc SessionContext) (AdminLoginOutcome, error) {
	user, err := checkPassword(ctx, in)
	if err != nil {
		return AdminLoginOutcome{}, err
	}
	out := AdminLoginOutcome{User: user}
	if !config.IsAdminRole(user.Role) {
		return out, errNotAdmin
	}
	enabled, err := MFAEnabled(ctx, user.ID)
	if err != nil {
		return out, err
	}
	if !MFARequired(user.Role, enabled) {
		res, err := finishLogin(ctx, user, sc, nil)
		if err != nil {
			return out, err
		}
		out.Session = &res
		return out, nil
	}
	token, err := newChallenge(ctx, user.ID, !enabled, sc.IP)
	if err != nil {
		return out, err
	}
	ch := &MFAChallengeView{
		MFARequired: true, EnrolmentRequired: !enabled, MFAToken: token,
		ExpiresIn: int64(mfaChallengeTTL.Seconds()), Methods: []string{"totp", "recovery"},
	}
	if !enabled {
		ch.Methods = []string{"enrol"}
	}
	ch.User.ID, ch.User.Email, ch.User.Role = user.ID.Hex(), user.Email, user.Role
	out.Challenge = ch
	return out, nil
}

// SecondStep identifies whose challenge was presented, for the audit trail
// (set whenever the challenge was valid, even if the code was wrong).
type SecondStep struct {
	UserID string
	Email  string
	Role   string
	Factor SecondFactor
}

// challengeUser loads the challenge and re-checks the account: still active,
// not locked, still administrative.
func challengeUser(ctx context.Context, token string) (*mfaChallenge, *models.User, SecondStep, error) {
	ch, err := loadChallenge(ctx, token)
	if err != nil {
		return nil, nil, SecondStep{}, err
	}
	var u models.User
	if err := users().FindOne(ctx, bson.M{"_id": ch.UserID}).Decode(&u); err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil, SecondStep{}, challengeInvalid()
		}
		return nil, nil, SecondStep{}, err
	}
	step := SecondStep{UserID: u.ID.Hex(), Email: u.Email, Role: u.Role}
	if u.LockedUntil != nil && u.LockedUntil.After(time.Now()) {
		return nil, nil, step, apperr.New(apperr.AccountDisabled,
			"Too many failed attempts. Try again after "+iso.Format(*u.LockedUntil), 403)
	}
	if u.Status != models.UserStatusActive {
		return nil, nil, step, accountDisabled(u.Status)
	}
	if !config.IsAdminRole(u.Role) {
		return nil, nil, step, challengeInvalid()
	}
	return ch, &u, step, nil
}

// wrongCode counts a failed second step against the challenge and the
// account's lockout.
func wrongCode(ctx context.Context, ch *mfaChallenge, uid bson.ObjectID, cause error) error {
	failChallenge(ctx, ch)
	if err := registerFailedLogin(ctx, uid); err != nil {
		return err
	}
	return cause
}

// CompleteAdminLogin is POST /admin/login/2fa: a TOTP code or a recovery
// code for a challenge from BeginAdminLogin.
func CompleteAdminLogin(ctx context.Context, token, code, recovery string, sc SessionContext) (AuthResult, SecondStep, error) {
	ch, user, step, err := challengeUser(ctx, token)
	if err != nil {
		return AuthResult{}, step, err
	}
	if ch.Enrol {
		return AuthResult{}, step, apperr.New(CodeMFANotEnrolled, "Set up two-factor authentication first", 400)
	}
	factor, err := VerifySecondFactor(ctx, user.ID, code, recovery)
	if err != nil {
		if apperr.HasCode(err, CodeMFAInvalid) {
			return AuthResult{}, step, wrongCode(ctx, ch, user.ID, err)
		}
		return AuthResult{}, step, err
	}
	step.Factor = factor
	if ok, err := consumeChallenge(ctx, ch); err != nil || !ok {
		if err == nil {
			err = challengeInvalid()
		}
		return AuthResult{}, step, err
	}
	now := mongox.Now()
	res, err := finishLogin(ctx, user, sc, &now)
	return res, step, err
}

// StartAdminLoginEnrolment is POST /admin/login/2fa/enrol: the secret for an
// admin who must enrol before the session is issued.
func StartAdminLoginEnrolment(ctx context.Context, token string) (MFAEnrolment, SecondStep, error) {
	ch, user, step, err := challengeUser(ctx, token)
	if err != nil {
		return MFAEnrolment{}, step, err
	}
	if !ch.Enrol {
		return MFAEnrolment{}, step, apperr.ConflictErr(CodeMFAAlreadyEnabled, "Two-factor authentication is already enabled")
	}
	enr, err := StartMFAEnrolment(ctx, user.ID, user.Email)
	return enr, step, err
}

// ConfirmAdminLoginEnrolment is POST /admin/login/2fa/enrol/confirm: the
// first code enables 2FA and completes the sign-in. The recovery codes are
// returned once.
func ConfirmAdminLoginEnrolment(ctx context.Context, token, code string, sc SessionContext) (AuthResult, []string, SecondStep, error) {
	ch, user, step, err := challengeUser(ctx, token)
	if err != nil {
		return AuthResult{}, nil, step, err
	}
	if !ch.Enrol {
		return AuthResult{}, nil, step, apperr.ConflictErr(CodeMFAAlreadyEnabled, "Two-factor authentication is already enabled")
	}
	codes, err := ConfirmMFAEnrolment(ctx, user.ID, code)
	if err != nil {
		if apperr.HasCode(err, CodeMFAInvalid) {
			return AuthResult{}, nil, step, wrongCode(ctx, ch, user.ID, err)
		}
		return AuthResult{}, nil, step, err
	}
	step.Factor = SecondFactor{Method: "enrol", RecoveryCodesRemaining: len(codes)}
	if _, err := consumeChallenge(ctx, ch); err != nil {
		return AuthResult{}, nil, step, err
	}
	now := mongox.Now()
	res, err := finishLogin(ctx, user, sc, &now)
	return res, codes, step, err
}

// checkPassword verifies the credentials, then the lockout, then the status.
// Credentials come first so a locked or disabled state is never revealed to
// someone without the password. A wrong password counts toward the lockout.
func checkPassword(ctx context.Context, in LoginInput) (*models.User, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	var user *models.User
	var u models.User
	err := users().FindOne(ctx, bson.M{"email": email}).Decode(&u)
	switch {
	case err == nil:
		user = &u
	case mongox.IsNotFound(err):
	default:
		return nil, err
	}

	// Compare against a dummy hash when the account is unknown so that a missing
	// account and a wrong password take the same time.
	hash := dummyHash
	if user != nil && user.PasswordHash != "" {
		hash = user.PasswordHash
	}
	passwordMatches := ComparePassword(in.Password, hash)

	if user == nil || !passwordMatches {
		if user != nil {
			if err := registerFailedLogin(ctx, user.ID); err != nil {
				return nil, err
			}
		}
		return nil, invalidCredentials()
	}

	if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
		return nil, apperr.New(apperr.AccountDisabled,
			"Too many failed attempts. Try again after "+iso.Format(*user.LockedUntil), 403)
	}

	if user.Status != models.UserStatusActive {
		return nil, accountDisabled(user.Status)
	}
	return user, nil
}

// finishLogin resets the failure counter, stamps the sign-in and issues the
// session. mfaAt is when a second factor was passed (nil: none).
func finishLogin(ctx context.Context, user *models.User, sc SessionContext, mfaAt *time.Time) (AuthResult, error) {
	now := mongox.Now()
	if _, err := users().UpdateOne(ctx, bson.M{"_id": user.ID}, bson.M{"$set": bson.M{
		"failedLoginAttempts": int64(0),
		"lockedUntil":         nil,
		"lastLoginAt":         now,
		"lastLoginIp":         sc.IP,
		"updatedAt":           now,
	}}); err != nil {
		return AuthResult{}, err
	}

	wallet, err := wallets.GetOrCreateWallet(ctx, user.ID.Hex(), models.AccountLive, "")
	if err != nil {
		return AuthResult{}, err
	}
	session, err := issueSession(ctx, user.ID.Hex(), user.Role, user.Permissions, sc, "", mfaAt)
	if err != nil {
		return AuthResult{}, err
	}
	fresh, err := findUserView(ctx, user.ID)
	if err != nil {
		return AuthResult{}, err
	}
	if fresh == nil {
		return AuthResult{}, apperr.Unauth("Account no longer exists")
	}
	return AuthResult{User: models.ToUserView(fresh), Wallet: wallet, Tokens: session.Tokens, SessionID: session.SessionID}, nil
}

func registerFailedLogin(ctx context.Context, userID bson.ObjectID) error {
	var u struct {
		FailedLoginAttempts int64 `bson:"failedLoginAttempts"`
	}
	err := users().FindOneAndUpdate(ctx, bson.M{"_id": userID},
		bson.M{"$inc": bson.M{"failedLoginAttempts": 1}, "$set": bson.M{"updatedAt": mongox.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).
			SetProjection(bson.M{"failedLoginAttempts": 1}),
	).Decode(&u)
	if mongox.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if u.FailedLoginAttempts >= LoginMaxAttempts {
		now := mongox.Now()
		if _, err := users().UpdateOne(ctx, bson.M{"_id": userID}, bson.M{"$set": bson.M{
			"lockedUntil":         now.Add(LoginLockMs * time.Millisecond),
			"failedLoginAttempts": int64(0),
			"updatedAt":           now,
		}}); err != nil {
			return err
		}
		logx.Warn("account temporarily locked after repeated failed logins", "userId", userID.Hex())
	}
	return nil
}

// refreshRow is a refresh_tokens row plus the second-factor mark: an admin
// session may only be refreshed if its sign-in passed 2FA (once 2FA applies
// to the account), so a session from before enrolment cannot outlive it.
type refreshRow struct {
	models.RefreshToken `bson:",inline"`
	// MFAAt is when this session's sign-in passed the second factor.
	MFAAt *time.Time `bson:"mfaAt,omitempty"`
}

// IssueSession signs an access/refresh pair and stores the refresh token's
// hash. familyID "" starts a new token family.
func IssueSession(ctx context.Context, userID string, role string, permissions []string, sc SessionContext, familyID string) (IssuedSession, error) {
	return issueSession(ctx, userID, role, permissions, sc, familyID, nil)
}

func issueSession(ctx context.Context, userID string, role string, permissions []string, sc SessionContext, familyID string, mfaAt *time.Time) (IssuedSession, error) {
	if familyID == "" {
		familyID = util.UUID()
	}
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return IssuedSession{}, apperr.Unauth("Account no longer exists")
	}
	sessionID := util.UUID()
	// Role permissions plus any granted extras: a permission added to a role
	// later reaches existing users without rewriting their stored lists.
	effective := config.PermissionsForRole(role, permissions)
	// The token is bound to the current session version, so revoking the
	// user's sessions later kills it at once (authtoken.CheckSession).
	version, err := sessions.CurrentVersion(ctx, uid)
	if err != nil {
		return IssuedSession{}, err
	}

	accessToken, err := authtoken.SignAccessVersioned(userID, role, effective, sessionID, version)
	if err != nil {
		return IssuedSession{}, err
	}
	refreshToken, err := authtoken.SignRefresh(userID, sessionID, familyID)
	if err != nil {
		return IssuedSession{}, err
	}

	now := mongox.Now()
	if _, err := refreshTokens().InsertOne(ctx, refreshRow{
		RefreshToken: models.RefreshToken{
			UserID:    uid,
			SessionID: sessionID,
			TokenHash: util.SHA256(refreshToken),
			FamilyID:  familyID,
			ExpiresAt: now.Add(time.Duration(authtoken.RefreshTTLMs()) * time.Millisecond),
			IP:        sc.IP,
			UserAgent: sc.UserAgent,
			CreatedAt: now,
			UpdatedAt: now,
		},
		MFAAt: mfaAt,
	}); err != nil {
		return IssuedSession{}, err
	}

	return IssuedSession{
		SessionID: sessionID,
		Tokens: AuthTokens{
			AccessToken:           accessToken,
			RefreshToken:          refreshToken,
			AccessTokenExpiresIn:  authtoken.AccessTTLMs() / 1000,
			RefreshTokenExpiresIn: authtoken.RefreshTTLMs() / 1000,
			TokenType:             "Bearer",
		},
	}, nil
}

func revokeWhere(ctx context.Context, filter bson.M) error {
	now := mongox.Now()
	filter["revokedAt"] = nil
	_, err := refreshTokens().UpdateMany(ctx, filter, bson.M{"$set": bson.M{"revokedAt": now, "updatedAt": now}})
	return err
}

func Refresh(ctx context.Context, token string, sc SessionContext) (AuthResult, error) {
	claims, err := authtoken.VerifyRefresh(token)
	if err != nil {
		return AuthResult{}, err
	}
	tokenHash := util.SHA256(token)

	var stored refreshRow
	err = refreshTokens().FindOne(ctx, bson.M{"tokenHash": tokenHash}).Decode(&stored)
	if mongox.IsNotFound(err) {
		// The signature is valid but the row is gone: either it was already rotated
		// or the session was revoked. Treat it as replay and kill the family.
		if err := revokeWhere(ctx, bson.M{"familyId": claims.FID}); err != nil {
			return AuthResult{}, err
		}
		logx.Warn("refresh token replay detected - family revoked", "userId", claims.Subject, "familyId", claims.FID)
		return AuthResult{}, apperr.New(apperr.TokenInvalid, "Refresh token is no longer valid", 401)
	}
	if err != nil {
		return AuthResult{}, err
	}

	if stored.RevokedAt != nil {
		if err := revokeWhere(ctx, bson.M{"familyId": stored.FamilyID}); err != nil {
			return AuthResult{}, err
		}
		return AuthResult{}, apperr.New(apperr.TokenInvalid, "Refresh token has been revoked", 401)
	}

	if !stored.ExpiresAt.After(time.Now()) {
		return AuthResult{}, apperr.New(apperr.TokenExpired, "Refresh token has expired", 401)
	}

	user, err := findUserView(ctx, stored.UserID)
	if err != nil {
		return AuthResult{}, err
	}
	if user == nil {
		return AuthResult{}, apperr.Unauth("Account no longer exists")
	}
	if user.Status != models.UserStatusActive {
		return AuthResult{}, accountDisabled(user.Status)
	}

	// A session whose sign-in never passed a second factor cannot be renewed
	// once the account needs one (2FA became required, the user enrolled, or
	// a customer was promoted to an admin role).
	if stored.MFAAt == nil {
		need, err := secondFactorNeeded(ctx, user)
		if err != nil {
			return AuthResult{}, err
		}
		if need {
			if err := revokeWhere(ctx, bson.M{"familyId": stored.FamilyID}); err != nil {
				return AuthResult{}, err
			}
			return AuthResult{}, apperr.New(apperr.TokenInvalid, "Two-factor authentication is required, please sign in again", 401)
		}
	}

	next, err := issueSession(ctx, user.ID.Hex(), user.Role, user.Permissions, sc, stored.FamilyID, stored.MFAAt)
	if err != nil {
		return AuthResult{}, err
	}

	// Rotate: the presented token can never be used again.
	now := mongox.Now()
	if _, err := refreshTokens().UpdateOne(ctx, bson.M{"_id": stored.ID}, bson.M{"$set": bson.M{
		"revokedAt": now, "replacedBy": next.SessionID, "updatedAt": now,
	}}); err != nil {
		return AuthResult{}, err
	}

	wallet, err := wallets.GetOrCreateWallet(ctx, user.ID.Hex(), models.AccountLive, "")
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{User: models.ToUserView(user), Wallet: wallet, Tokens: next.Tokens, SessionID: next.SessionID}, nil
}

func Logout(ctx context.Context, in LogoutInput) error {
	if in.AllSessions {
		uid, ok := mongox.ParseID(in.UserID)
		if !ok {
			return nil
		}
		// Every session means every session: the access tokens and sockets of
		// other devices end now, not when their tokens expire.
		_, err := sessions.RevokeAll(ctx, uid)
		return err
	}
	if in.RefreshToken != nil && *in.RefreshToken != "" {
		now := mongox.Now()
		_, err := refreshTokens().UpdateOne(ctx,
			bson.M{"tokenHash": util.SHA256(*in.RefreshToken), "revokedAt": nil},
			bson.M{"$set": bson.M{"revokedAt": now, "updatedAt": now}})
		return err
	}
	return nil
}

func Me(ctx context.Context, userID string) (MeResult, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return MeResult{}, apperr.Unauth("Account no longer exists")
	}
	user, err := findUserView(ctx, uid)
	if err != nil {
		return MeResult{}, err
	}
	if user == nil {
		return MeResult{}, apperr.Unauth("Account no longer exists")
	}
	wallet, err := wallets.GetOrCreateWallet(ctx, userID, models.AccountLive, "")
	if err != nil {
		return MeResult{}, err
	}
	return MeResult{User: models.ToUserView(user), Wallet: wallet}, nil
}

// ChangePassword changes the password and ends every session of the user.
func ChangePassword(ctx context.Context, userID string, in ChangePasswordInput) error {
	_, err := ChangePasswordAndReissue(ctx, userID, "", in, SessionContext{}, false)
	return err
}

// ChangePasswordAndReissue changes the password and ends every session of the
// user - refresh tokens revoked, access tokens and sockets invalidated at once,
// the caller's own included - then, when reissue is set, signs the caller a
// fresh session so the device that changed the password stays signed in.
// currentSessionID carries the second-factor mark over to the new session.
func ChangePasswordAndReissue(ctx context.Context, userID, currentSessionID string, in ChangePasswordInput, sc SessionContext, reissue bool) (*IssuedSession, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return nil, apperr.Unauth("Account no longer exists")
	}
	var user models.User
	err := users().FindOne(ctx, bson.M{"_id": uid}).Decode(&user)
	if mongox.IsNotFound(err) {
		return nil, apperr.Unauth("Account no longer exists")
	}
	if err != nil {
		return nil, err
	}

	if !ComparePassword(in.CurrentPassword, user.PasswordHash) {
		return nil, apperr.New(apperr.InvalidCredentials, "Current password is incorrect", 401)
	}

	hash, err := HashPassword(in.NewPassword)
	if err != nil {
		return nil, err
	}

	var mfaAt *time.Time
	if currentSessionID != "" {
		var cur refreshRow
		if err := refreshTokens().FindOne(ctx, bson.M{"userId": uid, "sessionId": currentSessionID}).Decode(&cur); err == nil {
			mfaAt = cur.MFAAt
		}
	}

	// The new hash and the session-version bump are one write: there is no
	// moment where the password changed but old access tokens still work.
	raw, err := users().FindOneAndUpdate(ctx, bson.M{"_id": uid}, bson.M{
		"$set": bson.M{"passwordHash": hash, "updatedAt": mongox.Now()},
		"$inc": sessions.IncVersion(),
	}, options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(authtoken.SessionStateProjection)).Raw()
	if err != nil {
		return nil, err
	}
	if _, err := sessions.RevokeRefreshTokens(ctx, uid); err != nil {
		return nil, err
	}
	sessions.PublishAfterWrite(ctx, uid, raw)

	if !reissue {
		return nil, nil
	}
	next, err := issueSession(ctx, userID, user.Role, user.Permissions, sc, "", mfaAt)
	if err != nil {
		return nil, err
	}
	return &next, nil
}
