package models

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// Migrate brings stored documents up to the current schema. It is idempotent
// and must run before EnsureIndexes: the wallet index it replaces would
// otherwise make a user's second (DEMO) wallet a duplicate key.
func Migrate(ctx context.Context) error {
	return migrateWalletAccountTypes(ctx)
}

// migrateWalletAccountTypes: every wallet that predates accounts is the LIVE
// wallet, and the one-wallet-per-(user, currency) index becomes
// one-wallet-per-(user, account, currency).
func migrateWalletAccountTypes(ctx context.Context) error {
	coll := mongox.C(CollWallets)
	res, err := coll.UpdateMany(ctx,
		bson.M{"accountType": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"accountType": AccountLive}})
	if err != nil {
		return fmt.Errorf("back-fill wallet accountType: %w", err)
	}
	logx.Info("migration: wallets back-filled with accountType LIVE", "matched", res.MatchedCount, "modified", res.ModifiedCount)

	// Found by key pattern, not name: the name depends on who created it.
	cur, err := coll.Indexes().List(ctx)
	if err != nil {
		return fmt.Errorf("list wallet indexes: %w", err)
	}
	var specs []struct {
		Name string `bson:"name"`
		Key  bson.D `bson:"key"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		return fmt.Errorf("list wallet indexes: %w", err)
	}
	for _, spec := range specs {
		if !IsLegacyWalletIndexKey(spec.Key) {
			continue
		}
		if err := coll.Indexes().DropOne(ctx, spec.Name); err != nil {
			return fmt.Errorf("drop legacy wallet index %s: %w", spec.Name, err)
		}
		logx.Info("migration: dropped legacy wallet index", "name", spec.Name)
	}
	return nil
}

// IsLegacyWalletIndexKey reports whether key is exactly {userId: 1, currency: 1}.
func IsLegacyWalletIndexKey(key bson.D) bool {
	if len(key) != 2 || key[0].Key != "userId" || key[1].Key != "currency" {
		return false
	}
	return isAscending(key[0].Value) && isAscending(key[1].Value)
}

func isAscending(v any) bool {
	switch n := v.(type) {
	case int32:
		return n == 1
	case int64:
		return n == 1
	case float64:
		return n == 1
	}
	return false
}
