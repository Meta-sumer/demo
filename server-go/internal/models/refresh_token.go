package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
)

// CollRefreshTokens: refresh tokens are persisted as SHA-256 hashes so a
// database leak does not hand out sessions. Rotation replaces the row; reuse
// of a rotated token revokes the whole family (a standard replay defence).
const CollRefreshTokens = "refresh_tokens"

type RefreshToken struct {
	ID         bson.ObjectID `bson:"_id,omitempty"`
	UserID     bson.ObjectID `bson:"userId"`
	SessionID  string        `bson:"sessionId"`
	TokenHash  string        `bson:"tokenHash"`
	FamilyID   string        `bson:"familyId"`
	ExpiresAt  time.Time     `bson:"expiresAt"`
	RevokedAt  *time.Time    `bson:"revokedAt"`
	ReplacedBy *string       `bson:"replacedBy"`
	IP         *string       `bson:"ip"`
	UserAgent  *string       `bson:"userAgent"`
	CreatedAt  time.Time     `bson:"createdAt"`
	UpdatedAt  time.Time     `bson:"updatedAt"`
	V          int64         `bson:"__v"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollRefreshTokens, Keys: bson.D{{Key: "userId", Value: 1}}},
		mongox.Index{Collection: CollRefreshTokens, Keys: bson.D{{Key: "familyId", Value: 1}}},
		mongox.Index{Collection: CollRefreshTokens, Keys: bson.D{{Key: "tokenHash", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: CollRefreshTokens, Keys: bson.D{{Key: "sessionId", Value: 1}}},
		// Expired rows are removed by MongoDB itself.
		mongox.Index{Collection: CollRefreshTokens, Keys: bson.D{{Key: "expiresAt", Value: 1}}, Options: options.Index().SetExpireAfterSeconds(0)},
	)
}
