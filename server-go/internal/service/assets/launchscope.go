package assets

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/audit"
)

// Launch scope (owner decision, docs/audit/operations/feed-rules.md and
// docs/audit/pricing/pricing-spec.md section 0): only BTC/USD and ETH/USD
// trade at launch. EUR/USD, GBP/USD and XAU/USD have no live price source;
// the pricing engine refuses them (NOT_PRICED) and the apps do not offer
// them. ApplyLaunchScope also switches them off in an existing database.

// LaunchScopeOffSymbols are the assets that are off at launch.
var LaunchScopeOffSymbols = []string{"EUR/USD", "GBP/USD", "XAU/USD"}

// AuditLaunchScopeDisabled is the audit action of the migration.
const AuditLaunchScopeDisabled = "asset.launch_scope_disabled"

// LaunchScopeChange is one asset the migration switched off.
type LaunchScopeChange struct {
	AssetID    string `json:"assetId"`
	Symbol     string `json:"symbol"`
	OpenTrades int64  `json:"openTrades"`
}

// LaunchScopeResult reports one run.
type LaunchScopeResult struct {
	Disabled []LaunchScopeChange `json:"disabled"`
	// Assets already off (or absent): nothing to do.
	Unchanged []string `json:"unchanged"`
}

// ApplyLaunchScope sets the launch-scope-off assets inactive. Idempotent: an
// asset already inactive is left alone and writes no audit row; each asset
// it switches off gets one audit row (action asset.launch_scope_disabled,
// before/after, actor "system:launch-scope"). Open trades on those assets
// are not touched: they settle on their own terms (with no feed they void
// and refund under the no-price rule).
func ApplyLaunchScope(ctx context.Context) (LaunchScopeResult, error) {
	out := LaunchScopeResult{Disabled: []LaunchScopeChange{}, Unchanged: []string{}}
	coll := mongox.C(models.CollAssets)
	for _, symbol := range LaunchScopeOffSymbols {
		now := mongox.Now()
		var before models.Asset
		err := coll.FindOneAndUpdate(ctx, bson.M{"symbol": symbol, "active": true},
			bson.M{"$set": bson.M{"active": false, "updatedAt": now}},
			options.FindOneAndUpdate().SetReturnDocument(options.Before)).Decode(&before)
		if mongox.IsNotFound(err) {
			out.Unchanged = append(out.Unchanged, symbol)
			continue
		}
		if err != nil {
			return out, err
		}
		open, err := mongox.C(models.CollBinaryTrades).CountDocuments(ctx,
			bson.M{"assetId": before.ID, "status": bson.M{"$in": bson.A{models.TradeStatusOpen, models.TradeStatusSettling}}})
		if err != nil {
			open = -1
		}
		after := before
		after.Active, after.UpdatedAt = false, now
		audit.Write(ctx, audit.Entry{
			ActorID: "", ActorRole: "SYSTEM", ActorEmail: "system:launch-scope",
			Action: AuditLaunchScopeDisabled, Resource: "Asset", ResourceID: before.ID.Hex(),
			Before: models.ToAssetView(&before, now.UnixMilli()), After: models.ToAssetView(&after, now.UnixMilli()),
			Reason: "launch scope: only BTC/USD and ETH/USD trade at launch; " + symbol + " has no live price source",
		})
		logx.Warn("launch scope: asset switched off", "symbol", symbol, "assetId", before.ID.Hex(), "openTrades", open)
		out.Disabled = append(out.Disabled, LaunchScopeChange{AssetID: before.ID.Hex(), Symbol: symbol, OpenTrades: open})
	}
	if len(out.Disabled) > 0 {
		InvalidateAssetCache()
	}
	return out, nil
}

// ApplyLaunchScopeAtBoot runs ApplyLaunchScope when the deploy asks for it
// (APPLY_LAUNCH_SCOPE=true). A failure stops the boot: the operator asked
// for the change and must know it did not happen.
func ApplyLaunchScopeAtBoot(ctx context.Context, enabled bool) error {
	if !enabled {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := ApplyLaunchScope(ctx)
	if err != nil {
		return err
	}
	logx.Info("launch scope applied", "disabled", len(res.Disabled), "unchanged", res.Unchanged)
	return nil
}
