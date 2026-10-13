package users

import (
	"context"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
)

// MaxFavourites caps a user's starred assets.
const MaxFavourites = 50

var favouriteIDRe = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// NormaliseFavouriteIDs validates and de-duplicates a favourites list,
// keeping the first occurrence of each id in the order given. invalid holds
// the indexes of entries that are not 24-hex ids (they are left out).
func NormaliseFavouriteIDs(ids []string) (out []string, invalid []int) {
	out = make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for i, raw := range ids {
		if !favouriteIDRe.MatchString(raw) {
			invalid = append(invalid, i)
			continue
		}
		id := strings.ToLower(raw)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out, invalid
}

func favouriteHexes(ids []bson.ObjectID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Hex())
	}
	return out
}

// GetFavourites returns the user's favourite asset ids in their stored order.
func GetFavourites(ctx context.Context, userID string) ([]string, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return nil, apperr.Unauth("Account no longer exists")
	}
	var doc struct {
		FavouriteAssetIDs []bson.ObjectID `bson:"favouriteAssetIds"`
	}
	err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"_id": uid},
		options.FindOne().SetProjection(bson.M{"favouriteAssetIds": 1})).Decode(&doc)
	if err != nil {
		if mongox.IsNotFound(err) {
			return nil, apperr.Unauth("Account no longer exists")
		}
		return nil, err
	}
	return favouriteHexes(doc.FavouriteAssetIDs), nil
}

// SetFavourites replaces the favourites with ids (already normalised), keeping
// their order and silently dropping ids that are not existing assets.
func SetFavourites(ctx context.Context, userID string, ids []string) ([]string, error) {
	uid, ok := mongox.ParseID(userID)
	if !ok {
		return nil, apperr.Unauth("Account no longer exists")
	}
	oids := make([]bson.ObjectID, 0, len(ids))
	for _, id := range ids {
		if oid, ok := mongox.ParseID(id); ok {
			oids = append(oids, oid)
		}
	}
	existing := map[bson.ObjectID]bool{}
	if len(oids) > 0 {
		cur, err := mongox.C(models.CollAssets).Find(ctx, bson.M{"_id": bson.M{"$in": oids}},
			options.Find().SetProjection(bson.M{"_id": 1}))
		if err != nil {
			return nil, err
		}
		var found []struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := cur.All(ctx, &found); err != nil {
			return nil, err
		}
		for _, f := range found {
			existing[f.ID] = true
		}
	}
	kept := make([]bson.ObjectID, 0, len(oids))
	for _, oid := range oids {
		if existing[oid] {
			kept = append(kept, oid)
		}
	}
	res, err := mongox.C(models.CollUsers).UpdateOne(ctx, bson.M{"_id": uid},
		bson.M{"$set": bson.M{"favouriteAssetIds": kept, "updatedAt": mongox.Now()}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.Unauth("Account no longer exists")
	}
	return favouriteHexes(kept), nil
}
