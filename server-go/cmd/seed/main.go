// Command seed is the idempotent development seed (server/src/scripts/seed.ts).
//
// Creates the super admin, a demo trading account with a funded wallet, the
// asset catalogue and its payout tables. Running it twice changes nothing.
//
// Passwords come from SEED_SUPER_ADMIN_PASSWORD, SEED_RISK_PASSWORD,
// SEED_FINANCE_PASSWORD and SEED_TRADER_PASSWORD; any left unset is generated
// and printed once when the account is created. `seed -reset-2fa <email>` is
// the break-glass reset of an admin's two-factor enrolment.
//
// Forex and metals are only enabled for the simulated feed: neither Binance
// nor a bare generic websocket prices them, and an asset with no feed would
// simply refuse trades (which is the correct behaviour, but confusing in a
// demo).
package main

import (
	"context"
	"crypto/rand"
	"flag"
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
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/auth"
	"binarytrading/internal/service/auth/sessions"
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
	reset2FA := flag.String("reset-2fa", "", "break-glass: clear the two-factor enrolment of the admin account with this email, end its sessions, and exit")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
	logx.Init(cfg.LogLevel, cfg.NodeEnv)

	ctx := context.Background()
	if *reset2FA != "" {
		if err := resetTwoFactor(ctx, cfg, *reset2FA); err != nil {
			logx.Error("2FA reset failed", "err", err.Error())
			os.Exit(1)
		}
		_ = redisx.Disconnect()
		_ = mongox.Disconnect(ctx)
		return
	}
	if err := run(ctx, cfg); err != nil {
		logx.Error("seed failed", "err", err.Error())
		_ = redisx.Disconnect()
		_ = mongox.Disconnect(ctx)
		os.Exit(1)
	}
	_ = redisx.Disconnect()
	_ = mongox.Disconnect(ctx)
}

// resetTwoFactor is the break-glass path for when no SUPER_ADMIN can sign in
// (the last one lost their authenticator and recovery codes, or the
// encryption key changed). It needs shell access to the server and is
// audited like the console's reset.
func resetTwoFactor(ctx context.Context, cfg *config.Env, email string) error {
	if err := mongox.Connect(ctx, cfg.MongoURI, cfg.EnableMongoTransactions); err != nil {
		return err
	}
	if err := redisx.Connect(ctx, cfg.RedisURL); err != nil {
		logx.Warn("redis unavailable - sessions are still revoked in MongoDB", "err", err.Error())
	}
	var u models.User
	email = strings.ToLower(strings.TrimSpace(email))
	if err := mongox.C(models.CollUsers).FindOne(ctx, bson.M{"email": email}).Decode(&u); err != nil {
		if mongox.IsNotFound(err) {
			return fmt.Errorf("no account with email %s", email)
		}
		return err
	}
	wasEnabled, err := auth.ResetMFA(ctx, u.ID)
	if err != nil {
		return err
	}
	rev, err := sessions.RevokeAll(ctx, u.ID)
	if err != nil {
		return err
	}
	audit.Write(ctx, audit.Entry{
		ActorRole:  "SYSTEM",
		Action:     audit.AdminMFAReset,
		Resource:   "User",
		ResourceID: u.ID.Hex(),
		Reason:     "break-glass reset from the seed command line",
		Before:     map[string]any{"mfaEnabled": wasEnabled, "email": u.Email, "role": u.Role},
		After:      map[string]any{"mfaEnabled": false, "sessionVersion": rev.Version, "refreshTokensRevoked": rev.RefreshTokensRevoked},
	})
	fmt.Printf("Two-factor authentication cleared for %s (%s); every session ended. It enrols again at its next sign-in.\n", u.Email, u.Role)
	return nil
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
		email, username, fullName string
		password                  string // "" = generate one
		passwordEnv               string
		role                      config.Role
	}
	// No password lives in this file: each comes from the environment, or is
	// generated here and printed once (on stdout, not the log stream).
	accounts := []account{
		{cfg.SeedSuperAdminEmail, "superadmin", "Platform Owner", cfg.SeedSuperAdminPassword, "SEED_SUPER_ADMIN_PASSWORD", config.RoleSuperAdmin},
		{"risk@binary.local", "riskmanager", "Risk Manager", cfg.SeedRiskPassword, "SEED_RISK_PASSWORD", config.RoleRiskManager},
		{"finance@binary.local", "financeops", "Finance Operations", cfg.SeedFinancePassword, "SEED_FINANCE_PASSWORD", config.RoleFinance},
		{"trader@binary.local", "demotrader", "Demo Trader", cfg.SeedTraderPassword, "SEED_TRADER_PASSWORD", config.RoleUser},
	}
	var traderID bson.ObjectID
	var generated []string
	for _, a := range accounts {
		password := a.password
		if password != "" {
			if err := checkPasswordPolicy(password); err != nil {
				return fmt.Errorf("%s: %w", a.passwordEnv, err)
			}
		} else {
			if password, err = randomPassword(); err != nil {
				return err
			}
		}
		id, created, err := upsertUser(ctx, a.email, a.username, password, a.fullName, a.role)
		if err != nil {
			return err
		}
		if created && a.password == "" {
			generated = append(generated, fmt.Sprintf("  %-24s %-14s %s", a.email, a.role, password))
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
	if len(generated) > 0 {
		// Printed once, never logged, never stored in plain text: copy them now.
		fmt.Println()
		fmt.Println("Generated passwords for the accounts created by this run (shown once):")
		for _, line := range generated {
			fmt.Println(line)
		}
		fmt.Println("Administrative accounts enrol in two-factor authentication at their first sign-in.")
		fmt.Println()
	}
	return nil
}

// checkPasswordPolicy is the API's password rule (8..128 characters with a
// lowercase letter, an uppercase letter and a digit).
func checkPasswordPolicy(p string) error {
	if len(p) < 8 || len(p) > 128 {
		return fmt.Errorf("password must be 8 to 128 characters")
	}
	if !strings.ContainsAny(p, "abcdefghijklmnopqrstuvwxyz") || !strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") ||
		!strings.ContainsAny(p, "0123456789") {
		return fmt.Errorf("password must contain a lowercase letter, an uppercase letter and a digit")
	}
	for _, published := range publishedPasswords {
		if p == published {
			return fmt.Errorf("that password was published in this project's documentation; choose another")
		}
	}
	return nil
}

// publishedPasswords were once printed in the README and must never be used.
var publishedPasswords = []string{"Admin@12345", "Risk@12345", "Finance@12345", "Trader@12345"}

// randomPassword is 20 characters (~119 bits) that satisfy the policy.
func randomPassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"
	// Rejection sampling keeps every character equally likely.
	limit := byte(256 - 256%len(alphabet))
	for {
		out := make([]byte, 0, 20)
		buf := make([]byte, 64)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, b := range buf {
			if b < limit && len(out) < 20 {
				out = append(out, alphabet[int(b)%len(alphabet)])
			}
		}
		if p := string(out); len(out) == 20 && checkPasswordPolicy(p) == nil {
			return p, nil
		}
	}
}

// upsertUser creates the account unless one with that email exists (an
// existing account keeps its password). created reports which happened.
func upsertUser(ctx context.Context, email, username, password, fullName string, role config.Role) (bson.ObjectID, bool, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	users := mongox.C(models.CollUsers)
	var existing struct {
		ID bson.ObjectID `bson:"_id"`
	}
	err := users.FindOne(ctx, bson.M{"email": email}).Decode(&existing)
	if err == nil {
		logx.Info("user already exists", "email", email)
		return existing.ID, false, nil
	}
	if !mongox.IsNotFound(err) {
		return bson.NilObjectID, false, err
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return bson.NilObjectID, false, err
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
		return bson.NilObjectID, false, err
	}
	logx.Info("user created", "email", email, "role", role)
	return u.ID, true, nil
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
