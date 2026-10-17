package authtoken

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
)

// Session revocation.
//
// A JWT cannot be recalled, so every authenticated request (and every
// websocket, on subscribe and periodically) re-checks the token against the
// user's live state: status, role, permissions and a session version. Revoking
// a user's sessions increments the version in the same write that changes the
// account, so outstanding access tokens fail on their very next use instead of
// living out their TTL. The state is cached in Redis for a few seconds; a
// revocation overwrites the cache entry, so the cache never delays one.

// SessionState is what the auth middleware checks a token against.
type SessionState struct {
	Status      string   `json:"s"`
	Role        string   `json:"r"`
	Permissions []string `json:"p"`
	Version     int64    `json:"v"`
}

// EffectivePermissions is the role's set plus the user's granted extras, as
// it would be signed into a fresh token now.
func (s *SessionState) EffectivePermissions() []string {
	return config.PermissionsForRole(s.Role, s.Permissions)
}

// SessionCacheTTL bounds how long a change made outside the revocation path
// (a direct database edit) can go unnoticed.
const SessionCacheTTL = 30 * time.Second

// redisTimeout keeps a hung Redis from stalling every request; on a timeout
// the state is read from MongoDB instead.
const redisTimeout = 300 * time.Millisecond

// usersCollection is models.CollUsers, named here to keep this platform
// package free of the models package.
const usersCollection = "users"

// SessionStateProjection is the part of a user document a SessionState is
// built from.
var SessionStateProjection = bson.M{"status": 1, "role": 1, "permissions": 1, "sessionVersion": 1}

type sessionDoc struct {
	Status         string   `bson:"status"`
	Role           string   `bson:"role"`
	Permissions    []string `bson:"permissions"`
	SessionVersion int64    `bson:"sessionVersion"`
}

// StateFromRaw builds the state from a user document (or any document
// carrying the projected fields).
func StateFromRaw(raw bson.Raw) (*SessionState, error) {
	var d sessionDoc
	if err := bson.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	return d.state(), nil
}

func (d sessionDoc) state() *SessionState {
	perms := d.Permissions
	if perms == nil {
		perms = []string{}
	}
	return &SessionState{Status: d.Status, Role: d.Role, Permissions: perms, Version: d.SessionVersion}
}

// ReadSessionState reads the state from MongoDB, bypassing the cache. nil
// with no error is an unknown user.
func ReadSessionState(ctx context.Context, userID string) (*SessionState, error) {
	id, err := bson.ObjectIDFromHex(userID)
	if err != nil {
		return nil, nil
	}
	if mongox.DB() == nil {
		return nil, apperr.New(apperr.ServiceUnavailable, "Session store unavailable", 503)
	}
	var d sessionDoc
	err = mongox.C(usersCollection).FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(SessionStateProjection)).Decode(&d)
	if mongox.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d.state(), nil
}

// LoadSessionState returns the cached state, falling back to MongoDB (and
// filling the cache) on a miss or when Redis is unavailable.
func LoadSessionState(ctx context.Context, userID string) (*SessionState, error) {
	key := config.SecurityRedisKeys.SessionState(userID)
	rc := redisx.R()
	if rc != nil {
		cctx, cancel := context.WithTimeout(ctx, redisTimeout)
		raw, err := rc.Get(cctx, key).Bytes()
		cancel()
		if err == nil {
			var s SessionState
			if json.Unmarshal(raw, &s) == nil {
				return &s, nil
			}
		}
	}
	s, err := ReadSessionState(ctx, userID)
	if err != nil || s == nil {
		return s, err
	}
	if rc != nil {
		// SET NX: if a revocation wrote the entry after this read began, its
		// newer state wins over this one.
		if raw, err := json.Marshal(s); err == nil {
			cctx, cancel := context.WithTimeout(ctx, redisTimeout)
			_ = rc.SetNX(cctx, key, raw, SessionCacheTTL).Err()
			cancel()
		}
	}
	return s, nil
}

// StoreSessionState publishes a state just written to MongoDB, overwriting
// whatever the cache held. If the write fails the entry is deleted instead,
// and if that fails too the stale entry expires within SessionCacheTTL.
func StoreSessionState(ctx context.Context, userID string, s *SessionState) {
	rc := redisx.R()
	if rc == nil || s == nil {
		return
	}
	key := config.SecurityRedisKeys.SessionState(userID)
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	raw, err := json.Marshal(s)
	if err == nil {
		if err = rc.Set(cctx, key, raw, SessionCacheTTL).Err(); err == nil {
			return
		}
	}
	if derr := rc.Del(cctx, key).Err(); derr != nil {
		logx.Error("session cache could not be updated after a revocation - it expires on its own",
			"userId", userID, "ttlSeconds", int(SessionCacheTTL.Seconds()), "err", derr.Error())
	}
}

// ForgetSessionState drops the cached state so the next check reads MongoDB.
func ForgetSessionState(ctx context.Context, userID string) {
	rc := redisx.R()
	if rc == nil {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := rc.Del(cctx, config.SecurityRedisKeys.SessionState(userID)).Err(); err != nil {
		logx.Error("session cache entry could not be dropped - it expires on its own",
			"userId", userID, "ttlSeconds", int(SessionCacheTTL.Seconds()), "err", err.Error())
	}
}

// CheckSession validates a verified access token against the user's live
// state and returns that state (nil for a legacy token of an unknown user).
//
// Order matters: a revoked session answers 401 TOKEN_INVALID, which makes the
// clients try their refresh token; that fails too (the refresh tokens were
// revoked with it) and they sign out cleanly.
func CheckSession(ctx context.Context, c *AccessClaims) (*SessionState, error) {
	s, err := LoadSessionState(ctx, c.Subject)
	if err != nil {
		if ae, ok := apperr.As(err); ok {
			return nil, ae
		}
		logx.Error("session state could not be read", "userId", c.Subject, "err", err.Error())
		return nil, apperr.New(apperr.ServiceUnavailable, "Your session could not be verified, please retry", 503)
	}
	if s == nil {
		// Every token issued by this server carries a version. One that does
		// not predates versions, and an unknown user has nothing to revoke.
		if c.Ver != nil {
			return nil, apperr.Unauth("Account no longer exists")
		}
		return nil, nil
	}
	var ver int64
	if c.Ver != nil {
		ver = *c.Ver
	}
	if ver < s.Version {
		return nil, apperr.New(apperr.TokenInvalid, "Your session has ended, please sign in again", 401)
	}
	if s.Status != "ACTIVE" {
		return nil, apperr.New(apperr.AccountDisabled, "Account is "+strings.ToLower(s.Status), 403)
	}
	if s.Role != c.Role {
		return nil, apperr.New(apperr.TokenInvalid, "Session is stale, please sign in again", 401)
	}
	return s, nil
}
