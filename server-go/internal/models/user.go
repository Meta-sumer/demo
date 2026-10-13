package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

const CollUsers = "users"

// Schema limits (Mongoose maxlength).
const (
	UserCountryMaxLength = 2
	UserAddressMaxLength = 300
)

// UserPasswordHashExcluded is the projection that reproduces Mongoose's
// `select: false` on passwordHash; use it on every read that does not need
// the hash.
var UserPasswordHashExcluded = bson.D{{Key: "passwordHash", Value: 0}}

// User. Mongoose setters to apply in the service: email lowercase+trim,
// username/fullName/phone/address trim, country trim+uppercase.
type User struct {
	ID       bson.ObjectID `bson:"_id,omitempty"`
	Email    string        `bson:"email"`
	Username string        `bson:"username"`
	// select: false in Mongoose - excluded from reads unless asked for.
	PasswordHash string  `bson:"passwordHash"`
	FullName     *string `bson:"fullName,omitempty"`
	Phone        *string `bson:"phone,omitempty"`
	Country      *string `bson:"country,omitempty"`
	// Stored as a date-only value at UTC midnight; no time is ever meaningful.
	DateOfBirth *time.Time  `bson:"dateOfBirth"`
	Address     *string     `bson:"address,omitempty"`
	Role        config.Role `bson:"role"`
	// Explicit grants layered on top of the role defaults. Empty = role defaults.
	Permissions   []string   `bson:"permissions"`
	Status        UserStatus `bson:"status"`
	EmailVerified bool       `bson:"emailVerified"`
	// Per-user overrides of the global risk limits, minor units. Null = use global.
	MaxTradeAmount  *int64 `bson:"maxTradeAmount"`
	MaxUserExposure *int64 `bson:"maxUserExposure"`
	// Assets the user starred, in their chosen order.
	FavouriteAssetIDs   []bson.ObjectID `bson:"favouriteAssetIds,omitempty"`
	LastLoginAt         *time.Time      `bson:"lastLoginAt"`
	LastLoginIP         *string         `bson:"lastLoginIp"`
	FailedLoginAttempts int64           `bson:"failedLoginAttempts"`
	LockedUntil         *time.Time      `bson:"lockedUntil"`
	CreatedAt           time.Time       `bson:"createdAt"`
	UpdatedAt           time.Time       `bson:"updatedAt"`
	V                   int64           `bson:"__v"`
}

func init() {
	c := CollUsers
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "role", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "status", Value: 1}}},
		// Spec section 32 - lookup indexes.
		mongox.Index{Collection: c, Keys: bson.D{{Key: "email", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "username", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}

type UserView struct {
	ID       string  `json:"id"`
	Email    string  `json:"email"`
	Username string  `json:"username"`
	FullName *string `json:"fullName"`
	Phone    *string `json:"phone"`
	Country  *string `json:"country"`
	// ISO date only (YYYY-MM-DD), because a birthday has no time of day.
	DateOfBirth     *string     `json:"dateOfBirth"`
	Address         *string     `json:"address"`
	Role            config.Role `json:"role"`
	Permissions     []string    `json:"permissions"`
	Status          UserStatus  `json:"status"`
	EmailVerified   bool        `json:"emailVerified"`
	MaxTradeAmount  *int64      `json:"maxTradeAmount"`
	MaxUserExposure *int64      `json:"maxUserExposure"`
	LastLoginAt     *iso.Time   `json:"lastLoginAt"`
	CreatedAt       iso.Time    `json:"createdAt"`
}

func ToUserView(u *User) UserView {
	var dob *string
	if u.DateOfBirth != nil {
		s := u.DateOfBirth.UTC().Format("2006-01-02")
		dob = &s
	}
	return UserView{
		ID:              u.ID.Hex(),
		Email:           u.Email,
		Username:        u.Username,
		FullName:        u.FullName,
		Phone:           u.Phone,
		Country:         u.Country,
		DateOfBirth:     dob,
		Address:         u.Address,
		Role:            u.Role,
		Permissions:     config.PermissionsForRole(u.Role, u.Permissions),
		Status:          u.Status,
		EmailVerified:   u.EmailVerified,
		MaxTradeAmount:  u.MaxTradeAmount,
		MaxUserExposure: u.MaxUserExposure,
		LastLoginAt:     iso.Ptr(u.LastLoginAt),
		CreatedAt:       iso.From(u.CreatedAt),
	}
}

// ToSupportUserSummary is the support desk's user summary
// (support.service.ts userSummary, which reads email/username/fullName).
func ToSupportUserSummary(u *User) SupportUserSummary {
	return SupportUserSummary{ID: u.ID.Hex(), Email: u.Email, Username: u.Username, FullName: u.FullName}
}
