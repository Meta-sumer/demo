// Package users is the user profile and admin user-management service
// (modules/users/user.service.ts).
package users

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/wallets"
)

func userNotFound() error { return apperr.NotFoundErr(apperr.NotFound, "User not found") }

// findUser reads a user without the password hash (Mongoose select: false).
// A malformed id reads as "not found".
func findUser(ctx context.Context, userID string) (*models.User, error) {
	id, ok := mongox.ParseID(userID)
	if !ok {
		return nil, nil
	}
	var u models.User
	err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"_id": id},
		options.FindOne().SetProjection(models.UserPasswordHashExcluded)).Decode(&u)
	if mongox.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func GetUser(ctx context.Context, userID string) (models.UserView, error) {
	u, err := findUser(ctx, userID)
	if err != nil {
		return models.UserView{}, err
	}
	if u == nil {
		return models.UserView{}, userNotFound()
	}
	return models.ToUserView(u), nil
}

// UpdateProfileInput: nil = not supplied. Strings arrive already trimmed /
// upper-cased by the route validation.
type UpdateProfileInput struct {
	FullName *string
	Username *string
	Phone    *string
	Country  *string
	// YYYY-MM-DD, stored at UTC midnight; "" clears the stored date.
	DateOfBirth *string
	Address     *string
}

func UpdateProfile(ctx context.Context, userID string, in UpdateProfileInput) (models.UserView, error) {
	id, ok := mongox.ParseID(userID)
	if !ok {
		return models.UserView{}, userNotFound()
	}
	patch := bson.M{"updatedAt": mongox.Now()}
	setStr := func(key string, v *string, transform func(string) string) {
		if v != nil {
			patch[key] = transform(*v)
		}
	}
	setStr("fullName", in.FullName, strings.TrimSpace)
	setStr("username", in.Username, strings.TrimSpace)
	setStr("phone", in.Phone, strings.TrimSpace)
	setStr("country", in.Country, func(s string) string { return strings.ToUpper(strings.TrimSpace(s)) })
	setStr("address", in.Address, strings.TrimSpace)

	// An empty string clears the date; a value is pinned to UTC midnight so the
	// day never shifts with the reader's zone.
	if in.DateOfBirth != nil {
		if *in.DateOfBirth == "" {
			patch["dateOfBirth"] = nil
		} else {
			d, err := time.Parse("2006-01-02", *in.DateOfBirth)
			if err != nil {
				return models.UserView{}, apperr.Validation("Invalid request body",
					[]apperr.FieldIssue{{Path: "dateOfBirth", Message: "Enter a real date of birth"}})
			}
			patch["dateOfBirth"] = d.UTC()
		}
	}

	var u models.User
	err := mongox.C(models.CollUsers).FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{"$set": patch},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(models.UserPasswordHashExcluded),
	).Decode(&u)
	if err != nil {
		if mongox.IsNotFound(err) {
			return models.UserView{}, userNotFound()
		}
		// The unique index on username is the authority - checking first would
		// race two people claiming the same nickname at once.
		if mongox.IsDuplicateKey(err) {
			return models.UserView{}, apperr.ConflictErr(apperr.Conflict, "That nickname is already taken")
		}
		return models.UserView{}, err
	}
	return models.ToUserView(&u), nil
}

type ListUsersQuery struct {
	Search *string
	Role   *string
	Status *string
	Page   int64
	Limit  int64
}

func ListUsers(ctx context.Context, q ListUsersQuery) ([]models.UserView, int64, error) {
	filter := bson.M{}
	if q.Role != nil && *q.Role != "" {
		filter["role"] = *q.Role
	}
	if q.Status != nil && *q.Status != "" {
		filter["status"] = *q.Status
	}
	if q.Search != nil && *q.Search != "" {
		// Escaped so a search string can never be injected as a regex.
		escaped := EscapeRegex(*q.Search)
		re := bson.Regex{Pattern: escaped, Options: "i"}
		filter["$or"] = bson.A{
			bson.M{"email": re},
			bson.M{"username": re},
			bson.M{"fullName": re},
		}
	}
	page, limit := q.Page, q.Limit
	if page < 1 {
		page = 1
	}
	if limit < 1 {
		limit = 20
	}

	coll := mongox.C(models.CollUsers)
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).
		SetSkip((page-1)*limit).
		SetLimit(limit).
		SetProjection(models.UserPasswordHashExcluded))
	if err != nil {
		return nil, 0, err
	}
	var docs []models.User
	if err := cur.All(ctx, &docs); err != nil {
		return nil, 0, err
	}
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	items := make([]models.UserView, 0, len(docs))
	for i := range docs {
		items = append(items, models.ToUserView(&docs[i]))
	}
	return items, total, nil
}

type UserStats struct {
	TotalTrades       int64 `json:"totalTrades"`
	OpenTrades        int64 `json:"openTrades"`
	WonTrades         int64 `json:"wonTrades"`
	LostTrades        int64 `json:"lostTrades"`
	TotalStaked       int64 `json:"totalStaked"`
	NetCustomerResult int64 `json:"netCustomerResult"`
}

type UserDetail struct {
	User models.UserView `json:"user"`
	// The LIVE wallet, as before accounts existed.
	Wallet models.WalletView `json:"wallet"`
	// [LIVE, DEMO].
	Wallets      []models.WalletView `json:"wallets"`
	OpenExposure int64               `json:"openExposure"`
	Stats        UserStats           `json:"stats"`
}

func GetUserDetail(ctx context.Context, userID string) (UserDetail, error) {
	user, err := GetUser(ctx, userID)
	if err != nil {
		return UserDetail{}, err
	}
	accounts, err := wallets.ListAccounts(ctx, userID)
	if err != nil {
		return UserDetail{}, err
	}
	wallet := accounts[0]
	exp, err := exposure.GetUserExposure(ctx, userID)
	if err != nil {
		return UserDetail{}, err
	}
	stats, err := aggregateUserStats(ctx, user.ID)
	if err != nil {
		return UserDetail{}, err
	}
	return UserDetail{User: user, Wallet: wallet, Wallets: accounts, OpenExposure: exp.Total, Stats: stats}, nil
}

func aggregateUserStats(ctx context.Context, userID string) (UserStats, error) {
	id, _ := mongox.ParseID(userID)
	pipeline := bson.A{
		// Real-money trading only; DEMO practice is not part of the customer's record.
		bson.M{"$match": bson.M{"userId": id, "accountType": models.NotDemo()}},
		bson.M{"$group": bson.M{
			"_id":            nil,
			"totalTrades":    bson.M{"$sum": 1},
			"openTrades":     bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$in": bson.A{"$status", bson.A{"OPEN", "SETTLING"}}}, 1, 0}}},
			"wonTrades":      bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$status", "WON"}}, 1, 0}}},
			"lostTrades":     bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$status", "LOST"}}, 1, 0}}},
			"totalStaked":    bson.M{"$sum": "$stake"},
			"platformResult": bson.M{"$sum": bson.M{"$ifNull": bson.A{"$platformResult", 0}}},
		}},
	}
	cur, err := mongox.C(models.CollBinaryTrades).Aggregate(ctx, pipeline)
	if err != nil {
		return UserStats{}, err
	}
	var rows []struct {
		TotalTrades    float64 `bson:"totalTrades"`
		OpenTrades     float64 `bson:"openTrades"`
		WonTrades      float64 `bson:"wonTrades"`
		LostTrades     float64 `bson:"lostTrades"`
		TotalStaked    float64 `bson:"totalStaked"`
		PlatformResult float64 `bson:"platformResult"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return UserStats{}, err
	}
	if len(rows) == 0 {
		return UserStats{}, nil
	}
	r := rows[0]
	return UserStats{
		TotalTrades: int64(r.TotalTrades),
		OpenTrades:  int64(r.OpenTrades),
		WonTrades:   int64(r.WonTrades),
		LostTrades:  int64(r.LostTrades),
		TotalStaked: int64(r.TotalStaked),
		// The customer's result is the mirror image of the platform's.
		NetCustomerResult: -int64(r.PlatformResult),
	}, nil
}

// AdminUpdateUserInput: nil = not supplied. MaxTradeAmount/MaxUserExposure
// distinguish absent (Set=false) from an explicit null (clear the override).
type AdminUpdateUserInput struct {
	Status          *string
	Role            *string
	Permissions     *[]string
	MaxTradeAmount  OptInt
	MaxUserExposure OptInt
	EmailVerified   *bool
}

// OptInt is an optional, nullable integer.
type OptInt struct {
	Set   bool
	Value *int64 // nil with Set = null
}

func AdminUpdateUser(ctx context.Context, userID string, in AdminUpdateUserInput, actorRole string) (before, after models.UserView, err error) {
	b, err := findUser(ctx, userID)
	if err != nil {
		return before, after, err
	}
	if b == nil {
		return before, after, userNotFound()
	}

	// Only a SUPER_ADMIN may alter administrative privileges, so an ADMIN cannot
	// escalate their own or anyone else's access.
	touchesPrivileges := in.Role != nil || in.Permissions != nil
	if touchesPrivileges && actorRole != config.RoleSuperAdmin {
		return before, after, apperr.Forbid("Only a super admin can change roles or permissions")
	}

	patch := bson.M{"updatedAt": mongox.Now()}
	if in.Status != nil {
		patch["status"] = *in.Status
	}
	if in.Role != nil {
		patch["role"] = *in.Role
	}
	if in.Permissions != nil {
		perms := *in.Permissions
		if perms == nil {
			perms = []string{}
		}
		patch["permissions"] = perms
	}
	if in.MaxTradeAmount.Set {
		patch["maxTradeAmount"] = in.MaxTradeAmount.Value
	}
	if in.MaxUserExposure.Set {
		patch["maxUserExposure"] = in.MaxUserExposure.Value
	}
	if in.EmailVerified != nil {
		patch["emailVerified"] = *in.EmailVerified
	}
	if in.Role != nil && *in.Role != "" && in.Permissions == nil {
		patch["permissions"] = config.PermissionsForRole(*in.Role, nil)
	}

	var a models.User
	err = mongox.C(models.CollUsers).FindOneAndUpdate(ctx, bson.M{"_id": b.ID}, bson.M{"$set": patch},
		options.FindOneAndUpdate().SetReturnDocument(options.After).SetProjection(models.UserPasswordHashExcluded),
	).Decode(&a)
	if err != nil {
		if mongox.IsNotFound(err) {
			return before, after, userNotFound()
		}
		return before, after, err
	}
	return models.ToUserView(b), models.ToUserView(&a), nil
}

// AuditDoc turns a UserView into the object Mongoose stores for an audit
// row's before/after (JSON field names, dates as BSON dates).
func AuditDoc(v models.UserView) bson.M {
	var lastLogin any
	if v.LastLoginAt != nil {
		lastLogin = v.LastLoginAt.Time
	}
	perms := v.Permissions
	if perms == nil {
		perms = []string{}
	}
	return bson.M{
		"id":              v.ID,
		"email":           v.Email,
		"username":        v.Username,
		"fullName":        v.FullName,
		"phone":           v.Phone,
		"country":         v.Country,
		"dateOfBirth":     v.DateOfBirth,
		"address":         v.Address,
		"role":            v.Role,
		"permissions":     perms,
		"status":          string(v.Status),
		"emailVerified":   v.EmailVerified,
		"maxTradeAmount":  v.MaxTradeAmount,
		"maxUserExposure": v.MaxUserExposure,
		"lastLoginAt":     lastLogin,
		"createdAt":       v.CreatedAt.Time,
	}
}

// regexSpecials are escaped so a search string is matched literally.
const regexSpecials = `.*+?^${}()|[]/\`

func EscapeRegex(value string) string {
	var b strings.Builder
	for _, r := range value {
		if strings.ContainsRune(regexSpecials, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
