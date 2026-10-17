// Package sessions revokes a user's sessions: refresh tokens, outstanding
// access tokens and open websockets.
//
// Access tokens are JWTs and cannot be recalled, so each carries the user's
// session version ("ver") and every request checks it against the stored one
// (authtoken.CheckSession). Revoking increments the stored version - ideally
// in the same update that changes the account (merge IncVersion into it) - so
// the change and the revocation cannot be separated. Publish then overwrites
// the session cache and makes every instance re-check that user's sockets.
//
// It sits below service/auth and service/users so both can use it.
package sessions

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/ws"
)

// VersionField is the user document field holding the session version.
const VersionField = "sessionVersion"

// IncVersion is the $inc fragment that revokes every outstanding access token
// of the user it is applied to.
func IncVersion() bson.M { return bson.M{VersionField: int64(1)} }

// CurrentVersion reads the user's session version from MongoDB (never from
// the cache: a token signed with a stale version would be dead on arrival).
func CurrentVersion(ctx context.Context, userID bson.ObjectID) (int64, error) {
	var d struct {
		V int64 `bson:"sessionVersion"`
	}
	err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"_id": userID},
		options.FindOne().SetProjection(bson.M{VersionField: 1})).Decode(&d)
	if mongox.IsNotFound(err) {
		return 0, nil
	}
	return d.V, err
}

// RevokeRefreshTokens revokes every live refresh token of the user and
// returns how many there were.
func RevokeRefreshTokens(ctx context.Context, userID bson.ObjectID) (int64, error) {
	now := mongox.Now()
	res, err := mongox.C(models.CollRefreshTokens).UpdateMany(ctx,
		bson.M{"userId": userID, "revokedAt": nil},
		bson.M{"$set": bson.M{"revokedAt": now, "updatedAt": now}})
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// Publish makes a state already written to MongoDB effective everywhere: the
// session cache is overwritten and every instance re-checks the user's
// sockets. Best effort: the database write is the source of truth, and the
// cache and the periodic socket check converge on it by themselves.
func Publish(ctx context.Context, userID bson.ObjectID, state *authtoken.SessionState) {
	// Detached: a client that hangs up must not leave the cache stale.
	ctx = context.WithoutCancel(ctx)
	if state != nil {
		authtoken.StoreSessionState(ctx, userID.Hex(), state)
	} else {
		authtoken.ForgetSessionState(ctx, userID.Hex())
	}
	ws.RevalidateUser(userID.Hex())
}

// Result describes a revocation, for the audit trail.
type Result struct {
	Version              int64 `json:"sessionVersion"`
	RefreshTokensRevoked int64 `json:"refreshTokensRevoked"`
}

// RevokeAll ends every session of the user: refresh tokens revoked, access
// tokens invalidated, sockets closed. For flows with no account write of
// their own to merge IncVersion into (force sign-out, 2FA reset, logout of
// every session).
func RevokeAll(ctx context.Context, userID bson.ObjectID) (Result, error) {
	revoked, err := RevokeRefreshTokens(ctx, userID)
	if err != nil {
		return Result{}, err
	}
	raw, err := mongox.C(models.CollUsers).FindOneAndUpdate(ctx, bson.M{"_id": userID},
		bson.M{"$inc": IncVersion(), "$set": bson.M{"updatedAt": mongox.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(authtoken.SessionStateProjection),
	).Raw()
	if err != nil {
		if mongox.IsNotFound(err) {
			return Result{RefreshTokensRevoked: revoked}, nil
		}
		return Result{}, err
	}
	state, err := authtoken.StateFromRaw(raw)
	if err != nil {
		return Result{}, err
	}
	Publish(ctx, userID, state)
	logx.Info("user sessions revoked", "userId", userID.Hex(), "sessionVersion", state.Version, "refreshTokens", revoked)
	return Result{Version: state.Version, RefreshTokensRevoked: revoked}, nil
}

// PublishAfterWrite is Publish for callers that applied IncVersion in their
// own update and decoded the raw document it returned.
func PublishAfterWrite(ctx context.Context, userID bson.ObjectID, raw bson.Raw) {
	state, err := authtoken.StateFromRaw(raw)
	if err != nil {
		logx.Error("could not read the session state after a revocation", "userId", userID.Hex(), "err", err.Error())
		state = nil // Publish then drops the cache entry instead
	}
	Publish(ctx, userID, state)
}
