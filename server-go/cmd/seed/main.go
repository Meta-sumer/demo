// Command seed is the idempotent development seed (server/src/scripts/seed.ts).
//
// Creates the super admin, a demo trading account with a funded wallet, the
// asset catalogue and its payout tables. Running it twice changes nothing.
//
// Forex and metals are only enabled for the simulated feed: neither Binance
// nor a bare generic websocket prices them, and an asset with no feed would
// simply refuse trades (which is the correct behaviour, but confusing in a
// demo).
package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/auth"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/riskconfig"
	"binarytrading/internal/service/wallets"
)

type seedAsset struct {
	symbol         string
	name           string
	category       models.AssetCategory
	providerSymbol string
	pricePrecision int64
	expiries       []int64
	payouts        [][2]float64 // {expirySeconds, percent}, in key order
}

// The 900 s (15 min) and 1800 s (30 min) payouts are PLACEHOLDERS that copy
// each asset's 300 s rate. The business has to set the real rates (admin
// console, asset payouts) before these durations are offered for real money.
var seedAssets = []seedAsset{
	{"BTC/USD", "Bitcoin / US Dollar", "CRYPTO", "btcusdt", 2, []int64{30, 60, 300, 900, 1800},
		[][2]float64{{30, 82}, {60, 84}, {300, 87}, {900, 87}, {1800, 87}}},
	{"ETH/USD", "Ethereum / US Dollar", "CRYPTO", "ethusdt", 2, []int64{30, 60, 300, 900, 1800},
		[][2]float64{{30, 80}, {60, 83}, {300, 86}, {900, 86}, {1800, 86}}},
	{"EUR/USD", "Euro / US Dollar", "FOREX", "eurusd", 5, []int64{30, 60, 300, 900, 1800},
		[][2]float64{{30, 78}, {60, 80}, {300, 84}, {900, 84}, {1800, 84}}},
	{"GBP/USD", "Pound Sterling / US Dollar", "FOREX", "gbpusd", 5, []int64{60, 300, 900, 1800},
		[][2]float64{{60, 79}, {300, 83}, {900, 83}, {1800, 83}}},
	{"XAU/USD", "Gold / US Dollar", "COMMODITY", "xauusd", 2, []int64{60, 300, 900, 1800},
		[][2]float64{{60, 77}, {300, 81}, {900, 81}, {1800, 81}}},
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	logx.Init(cfg.LogLevel, cfg.NodeEnv)

	ctx := context.Background()
	if err := run(ctx, cfg); err != nil {
		logx.Error("seed failed", "err", err.Error())
		_ = redisx.Disconnect()
		_ = mongox.Disconnect(ctx)
		os.Exit(1)
	}
	_ = redisx.Disconnect()
	_ = mongox.Disconnect(ctx)
}

func run(ctx context.Context, cfg *config.Env) error {
	if err := mongox.Connect(ctx, cfg.MongoURI, cfg.EnableMongoTransactions); err != nil {
		return err
	}
	if err := models.Migrate(ctx); err != nil {
		return err
	}
	models.EnsureIndexes(ctx)
	if err := redisx.Connect(ctx, cfg.RedisURL); err != nil {
		return err
	}

	rc, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return err
	}
	logx.Info("risk configuration ready", "version", rc.Version)

	type account struct {
		email, username, password, fullName string
		role                                config.Role
	}
	accounts := []account{
		{cfg.SeedSuperAdminEmail, "superadmin", cfg.SeedSuperAdminPassword, "Platform Owner", config.RoleSuperAdmin},
		{"risk@binary.local", "riskmanager", "Risk@12345", "Risk Manager", config.RoleRiskManager},
		{"finance@binary.local", "financeops", "Finance@12345", "Finance Operations", config.RoleFinance},
		{"trader@binary.local", "demotrader", "Trader@12345", "Demo Trader", config.RoleUser},
	}
	var traderID bson.ObjectID
	for _, a := range accounts {
		id, err := upsertUser(ctx, a.email, a.username, a.password, a.fullName, a.role)
		if err != nil {
			return err
		}
		if _, err := wallets.GetOrCreateWallet(ctx, id.Hex(), models.AccountLive, ""); err != nil {
			return err
		}
		if a.role == config.RoleUser {
			traderID = id
		}
	}

	// Idempotency key is fixed, so re-running the seed never re-funds the wallet.
	if _, err := wallets.CreditWallet(ctx, wallets.AdjustmentInput{
		UserID:         traderID.Hex(),
		Amount:         100_000_00,
		Type:           models.WalletTransactionType("ADJUSTMENT_CREDIT"),
		ReferenceType:  "Seed",
		ReferenceID:    "seed-initial-balance",
		IdempotencyKey: "seed:credit:" + traderID.Hex(),
		Description:    "Initial demo balance",
	}); err != nil {
		logx.Info("demo wallet already funded")
	}

	if err := seedAssetCatalogue(ctx, cfg); err != nil {
		return err
	}
	if err := seedPaymentMethods(ctx); err != nil {
		return err
	}

	logx.Info("seed complete",
		"superAdmin", cfg.SeedSuperAdminEmail,
		"riskManager", "risk@binary.local",
		"finance", "finance@binary.local",
		"trader", "trader@binary.local")
	return nil
}

func upsertUser(ctx context.Context, email, username, password, fullName string, role config.Role) (bson.ObjectID, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	users := mongox.C(models.CollUsers)
	var existing struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := users.FindOne(ctx, bson.M{"email": email}).Decode(&existing)
	if err == nil {
		logx.Info("user already exists", "email", email)
		return existing.ID, nil
	}
	if !mongox.IsNotFound(err) {
		return bson.NilObjectID, err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return bson.NilObjectID, err
	}
	now := mongox.Now()
	u := models.User{
		ID:            bson.NewObjectID(),
		Email:         email,
		Username:      strings.TrimSpace(username),
		PasswordHash:  hash,
		FullName:      &fullName,
		Role:          role,
		Permissions:   config.PermissionsForRole(role, nil),
		Status:        models.UserStatus("ACTIVE"),
		EmailVerified: true,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if _, err := users.InsertOne(ctx, u); err != nil {
		return bson.NilObjectID, err
	}
	logx.Info("user created", "email", email, "role", role)
	return u.ID, nil
}

func seedAssetCatalogue(ctx context.Context, cfg *config.Env) error {
	feedIsSimulated := cfg.MarketDataProvider == "simulated"

	for index, def := range seedAssets {
		active := def.category == models.AssetCategoryCrypto || feedIsSimulated
		now := mongox.Now()
		doc := models.Asset{
			Symbol:            def.symbol,
			Name:              def.name,
			Category:          def.category,
			Active:            active,
			ProviderSymbol:    def.providerSymbol,
			Provider:          cfg.MarketDataProvider,
			PricePrecision:    def.pricePrecision,
			MinStake:          100_00,
			MaxStake:          50_000_00,
			SupportedExpiries: def.expiries,
			ExposureLimit:     5_000_000_00,
			NetExposureLimit:  2_000_000_00,
			HedgeEnabled:      def.category == models.AssetCategoryCrypto,
			TargetHedgeRatio:  0.5,
			MinHedgeNotional:  1_000_00,
			MaxHedgeNotional:  1_000_000_00,
			TradingWindows:    []models.TradingWindow{},
			DisplayOrder:      int64(index),
			CreatedAt:         now,
		}
		insert, err := toSetOnInsert(doc, "updatedAt")
		if err != nil {
			return err
		}
		var asset models.Asset
		err = mongox.C(models.CollAssets).FindOneAndUpdate(ctx,
			bson.M{"symbol": def.symbol},
			bson.M{"$setOnInsert": insert, "$set": bson.M{"updatedAt": now}},
			options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
		).Decode(&asset)
		if err != nil {
			return err
		}

		for _, p := range def.payouts {
			expirySeconds := int64(p[0])
			// An existing asset keeps its own expiries ($setOnInsert above), so a
			// newer seed must not price one it does not offer.
			if !slices.Contains(asset.SupportedExpiries, expirySeconds) {
				continue
			}
			n, err := mongox.C(models.CollPayoutConfigs).CountDocuments(ctx,
				bson.M{"assetId": asset.ID, "expirySeconds": expirySeconds, "active": true})
			if err != nil {
				return err
			}
			if n > 0 {
				continue
			}
			note := "Seeded default payout"
			ts := mongox.Now()
			if _, err := mongox.C(models.CollPayoutConfigs).InsertOne(ctx, models.PayoutConfig{
				AssetID:       asset.ID,
				ExpirySeconds: expirySeconds,
				PayoutPercent: p[1],
				Version:       1,
				Active:        true,
				EffectiveFrom: ts,
				Note:          &note,
				CreatedAt:     ts,
				UpdatedAt:     ts,
			}); err != nil {
				return err
			}
		}
		logx.Info("asset ready", "symbol", def.symbol, "active", active)
	}

	assets.InvalidateAssetCache()
	payouts.InvalidatePayoutCache()
	return nil
}

// seedPaymentMethods: deposit destinations for the manual flow.
//
// Placeholder details - replace them from the admin console before taking a
// real payment, or customers will send money to an account that is not yours.
func seedPaymentMethods(ctx context.Context) error {
	s := func(v string) *string { return &v }
	methods := []models.PaymentMethod{
		{
			Key: "upi", Label: "UPI", Type: models.PaymentMethodType("UPI"),
			UpiID:        s("payments@examplebank"),
			Instructions: s("Pay to the UPI ID above, then submit the 12-digit UTR from your payment app."),
			MinAmount:    100_00, MaxAmount: 100_000_00, DisplayOrder: 0,
		},
		{
			Key: "bank", Label: "Bank transfer (IMPS / NEFT)", Type: models.PaymentMethodType("BANK"),
			AccountName:   s("Example Platform Pvt Ltd"),
			AccountNumber: s("000123456789"),
			Ifsc:          s("EXMP0000123"),
			BankName:      s("Example Bank"),
			Instructions:  s("Transfer by IMPS or NEFT, then submit the UTR shown on your bank receipt."),
			MinAmount:     500_00, MaxAmount: 500_000_00, DisplayOrder: 1,
		},
	}
	for _, m := range methods {
		now := mongox.Now()
		m.Active = true
		m.CreatedAt = now
		insert, err := toSetOnInsert(m, "updatedAt")
		if err != nil {
			return err
		}
		if _, err := mongox.C(models.CollPaymentMethods).UpdateOne(ctx,
			bson.M{"key": m.Key},
			bson.M{"$setOnInsert": insert, "$set": bson.M{"updatedAt": now}},
			options.UpdateOne().SetUpsert(true),
		); err != nil {
			return err
		}
	}
	logx.Warn("seeded PLACEHOLDER payment details - replace them before taking real deposits")
	return nil
}

// toSetOnInsert marshals a model (schema defaults applied) for $setOnInsert,
// dropping _id and the keys the update sets itself.
func toSetOnInsert(doc any, drop ...string) (bson.D, error) {
	raw, err := bson.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var d bson.D
	if err := bson.Unmarshal(raw, &d); err != nil {
		return nil, err
	}
	out := bson.D{}
	for _, e := range d {
		if e.Key == "_id" || slices.Contains(drop, e.Key) {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
