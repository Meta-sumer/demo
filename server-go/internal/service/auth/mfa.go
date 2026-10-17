package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// Admin two-factor authentication (TOTP, RFC 6238).
//
// Every administrative role signs in with a password and then a 6-digit code
// from an authenticator app, or one of ten single-use recovery codes. With
// ADMIN_2FA_REQUIRED (the default) an admin who has not enrolled is not locked
// out: the sign-in turns into an enrolment, and the session is issued only
// once a first code proves the app holds the secret. Customers are never asked.
//
// Brute force: a challenge allows mfaChallengeMaxTries codes, and every wrong
// code also counts toward the account's password lockout (8 failures lock it
// for 15 minutes) - the password step does not reset that counter for an
// account that still has a second step to pass. A code is accepted once: the
// step it matched is recorded and never accepted again.

const (
	CollAdminMFA      = "admin_mfa"
	CollMFAChallenges = "mfa_challenges"

	mfaChallengeTTL      = 5 * time.Minute
	mfaChallengeMaxTries = 5
	mfaPendingTTL        = 15 * time.Minute
	recoveryCodeCount    = 10
	// recoveryCodeChars of base32 is 80 bits, so the stored SHA-256 cannot be
	// reversed by brute force.
	recoveryCodeChars = 16
)

// Error codes of the second step. 400 rather than 401: the consoles treat a
// 401 as an expired session and would try to refresh it.
const (
	CodeMFARequired         = "MFA_REQUIRED"
	CodeMFAInvalid          = "MFA_INVALID"
	CodeMFAChallengeInvalid = "MFA_CHALLENGE_INVALID"
	CodeMFANotEnrolled      = "MFA_NOT_ENROLLED"
	CodeMFAAlreadyEnabled   = "MFA_ALREADY_ENABLED"
	CodeMFAEnrolmentExpired = "MFA_ENROLMENT_EXPIRED"
	CodeMFAUnavailable      = "MFA_UNAVAILABLE"
)

func init() {
	models.RegisterIndexes(
		// Challenges expire on their own.
		mongox.Index{Collection: CollMFAChallenges, Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
		mongox.Index{Collection: CollMFAChallenges, Keys: bson.D{{Key: "userId", Value: 1}}},
	)
}

func mfaColl() *mongo.Collection       { return mongox.C(CollAdminMFA) }
func challengeColl() *mongo.Collection { return mongox.C(CollMFAChallenges) }

type recoveryCode struct {
	Hash   string     `bson:"hash"`
	UsedAt *time.Time `bson:"usedAt"`
}

// mfaDoc is one admin's enrolment, keyed by user id.
type mfaDoc struct {
	UserID        bson.ObjectID  `bson:"_id"`
	Enabled       bool           `bson:"enabled"`
	SecretEnc     string         `bson:"secretEnc,omitempty"`
	PendingEnc    string         `bson:"pendingSecretEnc,omitempty"`
	PendingAt     *time.Time     `bson:"pendingCreatedAt,omitempty"`
	EnrolledAt    *time.Time     `bson:"enrolledAt,omitempty"`
	LastUsedStep  int64          `bson:"lastUsedStep"`
	RecoveryCodes []recoveryCode `bson:"recoveryCodes"`
	CreatedAt     time.Time      `bson:"createdAt"`
	UpdatedAt     time.Time      `bson:"updatedAt"`
}

func (d *mfaDoc) recoveryRemaining() int {
	n := 0
	for _, c := range d.RecoveryCodes {
		if c.UsedAt == nil {
			n++
		}
	}
	return n
}

func loadMFA(ctx context.Context, uid bson.ObjectID) (*mfaDoc, error) {
	var d mfaDoc
	err := mfaColl().FindOne(ctx, bson.M{"_id": uid}).Decode(&d)
	if mongox.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// MFAEnabled reports whether the user has completed enrolment.
func MFAEnabled(ctx context.Context, uid bson.ObjectID) (bool, error) {
	d, err := loadMFA(ctx, uid)
	if err != nil {
		return false, err
	}
	return d != nil && d.Enabled, nil
}

// MFARequired: administrative roles must pass a second factor when the
// platform requires it, or when they enrolled voluntarily.
func MFARequired(role string, enabled bool) bool {
	return config.IsAdminRole(role) && (enabled || config.Admin2FAEnforced())
}

func mfaInvalid() error {
	return apperr.New(CodeMFAInvalid, "The verification code is not valid", 400)
}

func challengeInvalid() error {
	return apperr.New(CodeMFAChallengeInvalid, "This sign-in has expired, please sign in again", 400)
}

func secretUnreadable(uid bson.ObjectID) error {
	logx.Error("admin 2FA secret cannot be decrypted - MFA_ENCRYPTION_KEY (or JWT_SECRET) changed?", "userId", uid.Hex())
	return apperr.New(CodeMFAUnavailable,
		"Two-factor authentication cannot be verified for this account. Ask a super admin to reset it.", 400)
}

// ── status and enrolment ────────────────────────────────────────────────

// MFAStatus is the caller's own two-factor state.
type MFAStatus struct {
	Enabled                bool      `json:"enabled"`
	Required               bool      `json:"required"`
	EnrolledAt             *iso.Time `json:"enrolledAt"`
	RecoveryCodesRemaining int       `json:"recoveryCodesRemaining"`
}

func GetMFAStatus(ctx context.Context, uid bson.ObjectID, role string) (MFAStatus, error) {
	d, err := loadMFA(ctx, uid)
	if err != nil {
		return MFAStatus{}, err
	}
	s := MFAStatus{}
	if d != nil && d.Enabled {
		s.Enabled = true
		s.RecoveryCodesRemaining = d.recoveryRemaining()
		if d.EnrolledAt != nil {
			t := iso.From(*d.EnrolledAt)
			s.EnrolledAt = &t
		}
	}
	s.Required = MFARequired(role, s.Enabled)
	return s, nil
}

// MFAEnrolment is what the authenticator app needs. The secret is shown as
// text (base32, and grouped for typing) and as an otpauth:// URI.
type MFAEnrolment struct {
	Secret        string `json:"secret"`
	SecretGrouped string `json:"secretGrouped"`
	OTPAuthURI    string `json:"otpauthUri"`
	Issuer        string `json:"issuer"`
	Account       string `json:"account"`
	Algorithm     string `json:"algorithm"`
	Digits        int    `json:"digits"`
	Period        int    `json:"period"`
	ExpiresIn     int64  `json:"expiresIn"`
}

func issuer() string {
	if config.E != nil && config.E.Admin2FAIssuer != "" {
		return config.E.Admin2FAIssuer
	}
	return "Binary Trading Admin"
}

// StartMFAEnrolment creates a pending secret. It becomes the account's secret
// only when ConfirmMFAEnrolment sees a valid code from it.
func StartMFAEnrolment(ctx context.Context, uid bson.ObjectID, account string) (MFAEnrolment, error) {
	d, err := loadMFA(ctx, uid)
	if err != nil {
		return MFAEnrolment{}, err
	}
	if d != nil && d.Enabled {
		return MFAEnrolment{}, apperr.ConflictErr(CodeMFAAlreadyEnabled, "Two-factor authentication is already enabled")
	}
	secret := make([]byte, totpSecretBytes)
	if _, err := rand.Read(secret); err != nil {
		return MFAEnrolment{}, err
	}
	sealed, err := sealSecret(uid.Hex(), secret)
	if err != nil {
		return MFAEnrolment{}, err
	}
	now := mongox.Now()
	if _, err := mfaColl().UpdateOne(ctx, bson.M{"_id": uid, "enabled": bson.M{"$ne": true}}, bson.M{
		"$set":         bson.M{"pendingSecretEnc": sealed, "pendingCreatedAt": now, "updatedAt": now},
		"$setOnInsert": bson.M{"enabled": false, "lastUsedStep": int64(0), "recoveryCodes": bson.A{}, "createdAt": now},
	}, options.UpdateOne().SetUpsert(true)); err != nil {
		if mongox.IsDuplicateKey(err) {
			// Enabled between the read and the write.
			return MFAEnrolment{}, apperr.ConflictErr(CodeMFAAlreadyEnabled, "Two-factor authentication is already enabled")
		}
		return MFAEnrolment{}, err
	}
	enc := EncodeSecret(secret)
	return MFAEnrolment{
		Secret:        enc,
		SecretGrouped: groupSecret(enc),
		OTPAuthURI:    OTPAuthURI(issuer(), account, secret),
		Issuer:        issuer(),
		Account:       account,
		Algorithm:     "SHA1",
		Digits:        TOTPDigits,
		Period:        TOTPPeriod,
		ExpiresIn:     int64(mfaPendingTTL.Seconds()),
	}, nil
}

// ConfirmMFAEnrolment enables two-factor authentication once a code from the
// pending secret checks out, and returns the recovery codes (shown once).
func ConfirmMFAEnrolment(ctx context.Context, uid bson.ObjectID, code string) ([]string, error) {
	d, err := loadMFA(ctx, uid)
	if err != nil {
		return nil, err
	}
	if d != nil && d.Enabled {
		return nil, apperr.ConflictErr(CodeMFAAlreadyEnabled, "Two-factor authentication is already enabled")
	}
	if d == nil || d.PendingEnc == "" || d.PendingAt == nil {
		return nil, apperr.New(CodeMFANotEnrolled, "Start the enrolment first", 400)
	}
	if time.Since(*d.PendingAt) > mfaPendingTTL {
		return nil, apperr.New(CodeMFAEnrolmentExpired, "The enrolment expired, start again", 400)
	}
	secret, err := openSecret(uid.Hex(), d.PendingEnc)
	if err != nil {
		return nil, secretUnreadable(uid)
	}
	step, ok := verifyTOTP(secret, code, time.Now())
	if !ok {
		return nil, mfaInvalid()
	}
	codes, hashed, err := newRecoveryCodes(uid)
	if err != nil {
		return nil, err
	}
	now := mongox.Now()
	res, err := mfaColl().UpdateOne(ctx,
		bson.M{"_id": uid, "enabled": false, "pendingSecretEnc": d.PendingEnc},
		bson.M{
			"$set": bson.M{
				"enabled": true, "secretEnc": d.PendingEnc, "enrolledAt": now,
				"lastUsedStep": step, "recoveryCodes": hashed, "updatedAt": now,
			},
			"$unset": bson.M{"pendingSecretEnc": "", "pendingCreatedAt": ""},
		})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.ConflictErr(CodeMFAAlreadyEnabled, "The enrolment changed, start again")
	}
	return codes, nil
}

// ── verification ────────────────────────────────────────────────────────

// SecondFactor is what a successful verification used.
type SecondFactor struct {
	Method                 string `json:"method"` // "totp" | "recovery"
	RecoveryCodesRemaining int    `json:"recoveryCodesRemaining"`
}

// VerifySecondFactor checks a TOTP code, or else a recovery code, and
// consumes it: the same code is never accepted twice.
func VerifySecondFactor(ctx context.Context, uid bson.ObjectID, code, recovery string) (SecondFactor, error) {
	d, err := loadMFA(ctx, uid)
	if err != nil {
		return SecondFactor{}, err
	}
	if d == nil || !d.Enabled {
		return SecondFactor{}, apperr.New(CodeMFANotEnrolled, "Two-factor authentication is not set up for this account", 400)
	}
	now := mongox.Now()
	if strings.TrimSpace(code) != "" {
		secret, err := openSecret(uid.Hex(), d.SecretEnc)
		if err != nil {
			return SecondFactor{}, secretUnreadable(uid)
		}
		step, ok := verifyTOTP(secret, code, time.Now())
		if !ok {
			return SecondFactor{}, mfaInvalid()
		}
		res, err := mfaColl().UpdateOne(ctx,
			bson.M{"_id": uid, "enabled": true, "lastUsedStep": bson.M{"$lt": step}},
			bson.M{"$set": bson.M{"lastUsedStep": step, "updatedAt": now}})
		if err != nil {
			return SecondFactor{}, err
		}
		if res.MatchedCount == 0 {
			// Already used (replay) or an older code than one already accepted.
			return SecondFactor{}, mfaInvalid()
		}
		return SecondFactor{Method: "totp", RecoveryCodesRemaining: d.recoveryRemaining()}, nil
	}
	norm := normaliseRecoveryCode(recovery)
	if len(norm) != recoveryCodeChars {
		return SecondFactor{}, mfaInvalid()
	}
	res, err := mfaColl().UpdateOne(ctx,
		bson.M{"_id": uid, "enabled": true, "recoveryCodes": bson.M{"$elemMatch": bson.M{"hash": hashRecoveryCode(uid, norm), "usedAt": nil}}},
		bson.M{"$set": bson.M{"recoveryCodes.$.usedAt": now, "updatedAt": now}})
	if err != nil {
		return SecondFactor{}, err
	}
	if res.MatchedCount == 0 {
		return SecondFactor{}, mfaInvalid()
	}
	return SecondFactor{Method: "recovery", RecoveryCodesRemaining: d.recoveryRemaining() - 1}, nil
}

// RegenerateRecoveryCodes replaces every recovery code; it takes a current
// authenticator code, not a recovery code.
func RegenerateRecoveryCodes(ctx context.Context, uid bson.ObjectID, code string) ([]string, error) {
	if strings.TrimSpace(code) == "" {
		return nil, mfaInvalid()
	}
	if _, err := VerifySecondFactor(ctx, uid, code, ""); err != nil {
		return nil, err
	}
	codes, hashed, err := newRecoveryCodes(uid)
	if err != nil {
		return nil, err
	}
	if _, err := mfaColl().UpdateOne(ctx, bson.M{"_id": uid, "enabled": true},
		bson.M{"$set": bson.M{"recoveryCodes": hashed, "updatedAt": mongox.Now()}}); err != nil {
		return nil, err
	}
	return codes, nil
}

// ResetMFA removes the enrolment (a super admin's action, or the seed's
// break-glass reset). The account enrols again at its next sign-in.
func ResetMFA(ctx context.Context, uid bson.ObjectID) (wasEnabled bool, err error) {
	d, err := loadMFA(ctx, uid)
	if err != nil {
		return false, err
	}
	if _, err := mfaColl().DeleteOne(ctx, bson.M{"_id": uid}); err != nil {
		return false, err
	}
	if _, err := challengeColl().DeleteMany(ctx, bson.M{"userId": uid}); err != nil {
		return false, err
	}
	return d != nil && d.Enabled, nil
}

// MarkSessionMFA records that the caller's current session has proved the
// second factor (by enrolling while signed in), so it can still be refreshed
// now that the account requires one.
func MarkSessionMFA(ctx context.Context, uid bson.ObjectID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	now := mongox.Now()
	_, err := refreshTokens().UpdateMany(ctx,
		bson.M{"userId": uid, "sessionId": sessionID, "revokedAt": nil},
		bson.M{"$set": bson.M{"mfaAt": now, "updatedAt": now}})
	return err
}

// ── recovery codes ──────────────────────────────────────────────────────

func newRecoveryCodes(uid bson.ObjectID) ([]string, []recoveryCode, error) {
	codes := make([]string, 0, recoveryCodeCount)
	hashed := make([]recoveryCode, 0, recoveryCodeCount)
	for i := 0; i < recoveryCodeCount; i++ {
		raw := make([]byte, 10) // 80 bits = 16 base32 characters
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		c := b32.EncodeToString(raw)
		codes = append(codes, c[0:4]+"-"+c[4:8]+"-"+c[8:12]+"-"+c[12:16])
		hashed = append(hashed, recoveryCode{Hash: hashRecoveryCode(uid, c)})
	}
	return codes, hashed, nil
}

func normaliseRecoveryCode(s string) string {
	return strings.ToUpper(normaliseOTP(s))
}

func hashRecoveryCode(uid bson.ObjectID, norm string) string {
	sum := sha256.Sum256([]byte("admin-mfa-recovery:" + uid.Hex() + ":" + norm))
	return hex.EncodeToString(sum[:])
}

// ── sign-in challenges ──────────────────────────────────────────────────

// mfaChallenge is a password-verified sign-in waiting for its second step.
// Only the token's hash is stored.
type mfaChallenge struct {
	ID        string        `bson:"_id"`
	UserID    bson.ObjectID `bson:"userId"`
	Enrol     bool          `bson:"enrol"`
	Attempts  int64         `bson:"attempts"`
	IP        *string       `bson:"ip"`
	ExpiresAt time.Time     `bson:"expiresAt"`
	CreatedAt time.Time     `bson:"createdAt"`
}

func challengeID(token string) string {
	sum := sha256.Sum256([]byte("mfa-challenge:" + token))
	return hex.EncodeToString(sum[:])
}

// newChallenge replaces any earlier challenge of the user.
func newChallenge(ctx context.Context, uid bson.ObjectID, enrol bool, ip *string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if _, err := challengeColl().DeleteMany(ctx, bson.M{"userId": uid}); err != nil {
		return "", err
	}
	now := mongox.Now()
	if _, err := challengeColl().InsertOne(ctx, mfaChallenge{
		ID: challengeID(token), UserID: uid, Enrol: enrol, IP: ip,
		ExpiresAt: now.Add(mfaChallengeTTL), CreatedAt: now,
	}); err != nil {
		return "", err
	}
	return token, nil
}

func loadChallenge(ctx context.Context, token string) (*mfaChallenge, error) {
	token = strings.TrimSpace(token)
	if token == "" || len(token) > 128 {
		return nil, challengeInvalid()
	}
	var ch mfaChallenge
	err := challengeColl().FindOne(ctx, bson.M{"_id": challengeID(token)}).Decode(&ch)
	if mongox.IsNotFound(err) {
		return nil, challengeInvalid()
	}
	if err != nil {
		return nil, err
	}
	if !ch.ExpiresAt.After(time.Now()) {
		_, _ = challengeColl().DeleteOne(ctx, bson.M{"_id": ch.ID})
		return nil, challengeInvalid()
	}
	return &ch, nil
}

// failChallenge counts a wrong code; the challenge dies after the last try.
func failChallenge(ctx context.Context, ch *mfaChallenge) {
	var after mfaChallenge
	err := challengeColl().FindOneAndUpdate(ctx, bson.M{"_id": ch.ID}, bson.M{"$inc": bson.M{"attempts": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&after)
	if err == nil && after.Attempts >= mfaChallengeMaxTries {
		_, _ = challengeColl().DeleteOne(ctx, bson.M{"_id": ch.ID})
	}
}

// consumeChallenge deletes it; false when another request already did.
func consumeChallenge(ctx context.Context, ch *mfaChallenge) (bool, error) {
	res, err := challengeColl().DeleteOne(ctx, bson.M{"_id": ch.ID})
	if err != nil {
		return false, err
	}
	return res.DeletedCount == 1, nil
}

// ── listing (super admin) ───────────────────────────────────────────────

// AdminMFARow is one administrative account and its two-factor state.
type AdminMFARow struct {
	ID                     string    `json:"id"`
	Email                  string    `json:"email"`
	Username               string    `json:"username"`
	FullName               *string   `json:"fullName"`
	Role                   string    `json:"role"`
	Status                 string    `json:"status"`
	Enabled                bool      `json:"enabled"`
	EnrolledAt             *iso.Time `json:"enrolledAt"`
	RecoveryCodesRemaining int       `json:"recoveryCodesRemaining"`
	LastLoginAt            *iso.Time `json:"lastLoginAt"`
}

// ListAdminMFA lists every administrative account with its 2FA state.
func ListAdminMFA(ctx context.Context) ([]AdminMFARow, error) {
	cur, err := users().Find(ctx, bson.M{"role": bson.M{"$in": config.AdminRoles}},
		options.Find().SetSort(bson.D{{Key: "role", Value: 1}, {Key: "email", Value: 1}}).
			SetProjection(models.UserPasswordHashExcluded))
	if err != nil {
		return nil, err
	}
	var docs []models.User
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	ids := make([]bson.ObjectID, 0, len(docs))
	for _, u := range docs {
		ids = append(ids, u.ID)
	}
	mcur, err := mfaColl().Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	var mfas []mfaDoc
	if err := mcur.All(ctx, &mfas); err != nil {
		return nil, err
	}
	byUser := map[bson.ObjectID]*mfaDoc{}
	for i := range mfas {
		byUser[mfas[i].UserID] = &mfas[i]
	}
	out := make([]AdminMFARow, 0, len(docs))
	for _, u := range docs {
		row := AdminMFARow{
			ID: u.ID.Hex(), Email: u.Email, Username: u.Username, FullName: u.FullName,
			Role: u.Role, Status: string(u.Status),
		}
		if u.LastLoginAt != nil {
			t := iso.From(*u.LastLoginAt)
			row.LastLoginAt = &t
		}
		if d := byUser[u.ID]; d != nil && d.Enabled {
			row.Enabled = true
			row.RecoveryCodesRemaining = d.recoveryRemaining()
			if d.EnrolledAt != nil {
				t := iso.From(*d.EnrolledAt)
				row.EnrolledAt = &t
			}
		}
		out = append(out, row)
	}
	return out, nil
}

var errNotAdmin = errors.New("not an administrative account")

// IsNotAdmin reports the error BeginAdminLogin returns for a non-admin.
func IsNotAdmin(err error) bool { return errors.Is(err, errNotAdmin) }
