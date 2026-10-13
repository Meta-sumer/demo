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

func Login(ctx context.Context, in LoginInput, sc SessionContext) (AuthResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	var user *models.User
	var u models.User
	err := users().FindOne(ctx, bson.M{"email": email}).Decode(&u)
	switch {
	case err == nil:
		user = &u
	case mongox.IsNotFound(err):
	default:
		return AuthResult{}, err
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
				return AuthResult{}, err
			}
		}
		return AuthResult{}, invalidCredentials()
	}

	if user.LockedUntil != nil && user.LockedUntil.After(time.Now()) {
		return AuthResult{}, apperr.New(apperr.AccountDisabled,
			"Too many failed attempts. Try again after "+iso.Format(*user.LockedUntil), 403)
	}

	if user.Status != models.UserStatusActive {
		return AuthResult{}, accountDisabled(user.Status)
	}

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
	session, err := IssueSession(ctx, user.ID.Hex(), user.Role, user.Permissions, sc, "")
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

// IssueSession signs an access/refresh pair and stores the refresh token's
// hash. familyID "" starts a new token family.
func IssueSession(ctx context.Context, userID string, role string, permissions []string, sc SessionContext, familyID string) (IssuedSession, error) {
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

	accessToken, err := authtoken.SignAccess(userID, role, effective, sessionID)
	if err != nil {
		return IssuedSession{}, err
	}
	refreshToken, err := authtoken.SignRefresh(userID, sessionID, familyID)
	if err != nil {
		return IssuedSession{}, err
	}

	now := mongox.Now()
	if _, err := refreshTokens().InsertOne(ctx, models.RefreshToken{
		UserID:    uid,
		SessionID: sessionID,
		TokenHash: util.SHA256(refreshToken),
		FamilyID:  familyID,
		ExpiresAt: now.Add(time.Duration(authtoken.RefreshTTLMs()) * time.Millisecond),
		IP:        sc.IP,
		UserAgent: sc.UserAgent,
		CreatedAt: now,
		UpdatedAt: now,
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

	var stored models.RefreshToken
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

	next, err := IssueSession(ctx, user.ID.Hex(), user.Role, user.Permissions, sc, stored.FamilyID)
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
		return revokeWhere(ctx, bson.M{"userId": uid})
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

func ChangePassword(ctx context.Context, userID string, in ChangePasswordInput) error {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return apperr.Unauth("Account no longer exists")
	}
	var user models.User
	err := users().FindOne(ctx, bson.M{"_id": uid}).Decode(&user)
	if mongox.IsNotFound(err) {
		return apperr.Unauth("Account no longer exists")
	}
	if err != nil {
		return err
	}

	if !ComparePassword(in.CurrentPassword, user.PasswordHash) {
		return apperr.New(apperr.InvalidCredentials, "Current password is incorrect", 401)
	}

	hash, err := HashPassword(in.NewPassword)
	if err != nil {
		return err
	}
	if _, err := users().UpdateOne(ctx, bson.M{"_id": uid}, bson.M{"$set": bson.M{
		"passwordHash": hash, "updatedAt": mongox.Now(),
	}}); err != nil {
		return err
	}

	// Changing a password ends every other session.
	return revokeWhere(ctx, bson.M{"userId": uid})
}
