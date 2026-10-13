package wallets

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
)

// AdminWalletList is GET /admin/wallets: lean wallet rows with userId
// populated as {_id, email, username, status} (null when the user is gone,
// as Mongoose populate does), plus platform totals.
type AdminWalletList struct {
	// Each item is a lean wallet document (_id, userId, currency, balance,
	// lockedBalance, version, createdAt, updatedAt, __v when stored).
	Items        []map[string]any
	Total        int64
	TotalBalance int64
	TotalLocked  int64
}

// ListWalletsForAdmin ports the admin wallets listing: sorted by balance
// descending, `.populate('userId', 'email username status')`.
// accountType "" lists both accounts. The totals are real money only (LIVE)
// unless the listing is filtered to one account, in which case they are that
// account's. Only trading-currency (INR) wallets are listed: the USDT
// FUNDING wallets have their own admin view, and balances of different
// currencies are never sorted or summed together.
func ListWalletsForAdmin(ctx context.Context, accountType string, page, limit int64) (AdminWalletList, error) {
	coll := mongox.C(models.CollWallets)
	filter := bson.M{"accountType": bson.M{"$ne": models.AccountFunding}, "currency": models.INRCurrency()}
	totalsMatch := bson.M{"accountType": models.NotDemo(), "currency": models.INRCurrency()}
	if accountType != "" {
		filter["accountType"] = models.AccountFilter(accountType)
		totalsMatch = filter
	}
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "balance", Value: -1}}).
		SetSkip((page-1)*limit).
		SetLimit(limit))
	if err != nil {
		return AdminWalletList{}, err
	}
	var rows []bson.M
	if err := cur.All(ctx, &rows); err != nil {
		return AdminWalletList{}, err
	}

	ids := bson.A{}
	for _, r := range rows {
		if id, ok := r["userId"].(bson.ObjectID); ok {
			ids = append(ids, id)
		}
	}
	users := map[bson.ObjectID]bson.M{}
	if len(ids) > 0 {
		ucur, err := mongox.C(models.CollUsers).Find(ctx, bson.M{"_id": bson.M{"$in": ids}},
			options.Find().SetProjection(bson.M{"email": 1, "username": 1, "status": 1}))
		if err != nil {
			return AdminWalletList{}, err
		}
		var found []bson.M
		if err := ucur.All(ctx, &found); err != nil {
			return AdminWalletList{}, err
		}
		for _, u := range found {
			if id, ok := u["_id"].(bson.ObjectID); ok {
				users[id] = u
			}
		}
	}

	items := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		acct, _ := r["accountType"].(string)
		r["accountType"] = models.NormAccountType(acct)
		if id, ok := r["userId"].(bson.ObjectID); ok {
			if u, ok := users[id]; ok {
				r["userId"] = u
			} else {
				r["userId"] = nil
			}
		}
		items = append(items, mongox.JSONValue(r).(map[string]any))
	}

	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return AdminWalletList{}, err
	}

	acur, err := coll.Aggregate(ctx, bson.A{
		bson.M{"$match": totalsMatch},
		bson.M{"$group": bson.M{"_id": nil, "balance": bson.M{"$sum": "$balance"}, "locked": bson.M{"$sum": "$lockedBalance"}}},
	})
	if err != nil {
		return AdminWalletList{}, err
	}
	var totals []struct {
		Balance float64 `bson:"balance"`
		Locked  float64 `bson:"locked"`
	}
	if err := acur.All(ctx, &totals); err != nil {
		return AdminWalletList{}, err
	}
	out := AdminWalletList{Items: items, Total: total}
	if len(totals) > 0 {
		out.TotalBalance = int64(totals[0].Balance)
		out.TotalLocked = int64(totals[0].Locked)
	}
	return out, nil
}
