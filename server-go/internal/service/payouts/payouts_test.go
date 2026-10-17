package payouts

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/testsupport"
)

func code(err error) string {
	if e, ok := apperr.As(err); ok {
		return e.Code
	}
	return ""
}

func i64p(v int64) *int64 { return &v }

func TestValidatePayoutInput(t *testing.T) {
	asset := &models.Asset{Symbol: "BTC/USD", SupportedExpiries: []int64{60, 300}, MinStake: 1_000, MaxStake: 10_000_000}
	ok := UpsertPayoutInput{ExpirySeconds: 60, PayoutPercent: 82}
	cases := []struct {
		name string
		mod  func(in *UpsertPayoutInput)
		msg  string
	}{
		{"valid", func(*UpsertPayoutInput) {}, ""},
		{"valid with bounds", func(in *UpsertPayoutInput) { in.MinStake, in.MaxStake = i64p(5_000), i64p(5_000) }, ""},
		{"unsupported expiry", func(in *UpsertPayoutInput) { in.ExpirySeconds = 90 }, "not in the supported expiries"},
		{"zero expiry", func(in *UpsertPayoutInput) { in.ExpirySeconds = 0 }, "not in the supported expiries"},
		{"zero percent", func(in *UpsertPayoutInput) { in.PayoutPercent = 0 }, "payoutPercent"},
		{"negative percent", func(in *UpsertPayoutInput) { in.PayoutPercent = -1 }, "payoutPercent"},
		{"percent above cap", func(in *UpsertPayoutInput) { in.PayoutPercent = 500.01 }, "payoutPercent"},
		{"NaN percent", func(in *UpsertPayoutInput) { in.PayoutPercent = math.NaN() }, "payoutPercent"},
		{"min above max", func(in *UpsertPayoutInput) { in.MinStake, in.MaxStake = i64p(9_000), i64p(8_000) }, "minStake must not exceed maxStake"},
		{"non-positive min", func(in *UpsertPayoutInput) { in.MinStake = i64p(0) }, "minStake must be positive"},
		{"min above asset max", func(in *UpsertPayoutInput) { in.MinStake = i64p(10_000_001) }, "above the maximum stake"},
		{"max below asset min", func(in *UpsertPayoutInput) { in.MaxStake = i64p(999) }, "below the minimum stake"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := ok
			c.mod(&in)
			err := ValidatePayoutInput(asset, in)
			if c.msg == "" {
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if code(err) != apperr.ValidationError || !strings.Contains(err.Error(), c.msg) {
				t.Fatalf("got %v, want a validation error mentioning %q", err, c.msg)
			}
		})
	}
}

// ── live tests ──────────────────────────────────────────────────────────

// liveStores connects a throwaway database (indexes included), with or
// without multi-document transactions.
func liveStores(t *testing.T, transactions bool) {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if transactions {
		uri := os.Getenv("MONGO_URI")
		if uri == "" {
			t.Skip("MONGO_URI not exported: cannot reconnect with transactions")
		}
		name := mongox.DB().Name()
		_ = mongox.Disconnect(ctx)
		if err := mongox.Connect(ctx, withDatabase(uri, name), true); err != nil {
			t.Fatal(err)
		}
		if !mongox.TransactionsAvailable() {
			t.Skip("MongoDB does not support transactions here (not a replica set)")
		}
	}
	models.EnsureIndexes(ctx)
	InvalidatePayoutCache()
	t.Cleanup(func() {
		testHook, testHookLoaded = nil, nil
		InvalidatePayoutCache()
		redisx.R().Del(context.Background(), generationKey())
	})
}

func withDatabase(uri, name string) string {
	scheme := "mongodb://"
	if strings.HasPrefix(uri, "mongodb+srv://") {
		scheme = "mongodb+srv://"
	}
	rest, query := strings.TrimPrefix(uri, scheme), ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return scheme + rest + "/" + name + query
}

func forModes(t *testing.T, fn func(t *testing.T, tx bool)) {
	for _, tx := range []bool{false, true} {
		name := "without_transactions"
		if tx {
			name = "with_transactions"
		}
		t.Run(name, func(t *testing.T) {
			liveStores(t, tx)
			fn(t, tx)
		})
	}
}

func seedAsset(t *testing.T) *models.Asset {
	t.Helper()
	a := &models.Asset{
		ID: bson.NewObjectID(), Symbol: "T" + bson.NewObjectID().Hex()[16:], Name: "Test", Category: models.AssetCategoryCrypto,
		Active: true, ProviderSymbol: "p" + bson.NewObjectID().Hex(), Provider: "simulated", PricePrecision: 2,
		MinStake: 1_000, MaxStake: 10_000_000, SupportedExpiries: []int64{60, 300},
	}
	if _, err := mongox.C(models.CollAssets).InsertOne(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

func upsert(t *testing.T, a *models.Asset, percent float64) models.PayoutView {
	t.Helper()
	_, after, err := UpsertPayout(context.Background(), UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: percent}, "")
	if err != nil {
		t.Fatalf("upsert %v%%: %v", percent, err)
	}
	return after
}

func slotRows(t *testing.T, a *models.Asset) []models.PayoutConfig {
	t.Helper()
	cur, err := mongox.C(models.CollPayoutConfigs).Find(context.Background(), bson.M{"assetId": a.ID, "expirySeconds": int64(60)})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.PayoutConfig
	if err := cur.All(context.Background(), &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

// assertOneActive checks the slot invariant - exactly one active row, no
// replacement left PENDING, unique versions - and returns the active row.
func assertOneActive(t *testing.T, a *models.Asset) models.PayoutConfig {
	t.Helper()
	var active []models.PayoutConfig
	versions := map[int64]bool{}
	for _, r := range slotRows(t, a) {
		if versions[r.Version] {
			t.Fatalf("version %d appears twice", r.Version)
		}
		versions[r.Version] = true
		if r.Activation != nil && *r.Activation == models.PayoutActivationPending {
			t.Fatalf("version %d left PENDING", r.Version)
		}
		if r.Active {
			active = append(active, r)
		}
	}
	if len(active) != 1 {
		t.Fatalf("%d active rows, want exactly 1", len(active))
	}
	return active[0]
}

func TestUpsertPayoutVersionsAndKeepsOldTerms(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		a := seedAsset(t)
		v1 := upsert(t, a, 82)
		if v1.Version != 1 || !v1.Active || v1.Activation == nil || *v1.Activation != models.PayoutActivationActivated {
			t.Fatalf("v1 = %+v", v1)
		}
		// A trade accepted under v1 stores its terms.
		trade := bson.M{"_id": bson.NewObjectID(), "payoutConfigId": v1.ID, "payoutConfigVersion": int64(1), "payoutPercent": 82.0}
		if _, err := mongox.C(models.CollBinaryTrades).InsertOne(ctx, trade); err != nil {
			t.Fatal(err)
		}

		before, v2, err := UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 85,
			MinStake: i64p(2_000), MaxStake: i64p(50_000)}, bson.NewObjectID().Hex())
		if err != nil {
			t.Fatal(err)
		}
		if before == nil || before.ID != v1.ID || v2.Version != 2 || v2.PayoutPercent != 85 {
			t.Fatalf("before=%+v after=%+v", before, v2)
		}
		if got := assertOneActive(t, a); got.ID.Hex() != v2.ID {
			t.Fatalf("active row is v%d", got.Version)
		}
		var old models.PayoutConfig
		if err := mongox.C(models.CollPayoutConfigs).FindOne(ctx, bson.M{"_id": mustID(t, v1.ID)}).Decode(&old); err != nil {
			t.Fatal(err)
		}
		if old.Active || old.EffectiveTo == nil || old.PayoutPercent != 82 || old.Version != 1 || old.MinStake != nil ||
			old.SupersededBy == nil || old.SupersededBy.Hex() != v2.ID {
			t.Fatalf("superseded row changed terms or lacks its close-out: %+v", old)
		}
		var stored bson.M
		if err := mongox.C(models.CollBinaryTrades).FindOne(ctx, bson.M{"_id": trade["_id"]}).Decode(&stored); err != nil {
			t.Fatal(err)
		}
		if stored["payoutPercent"] != 82.0 || stored["payoutConfigId"] != v1.ID {
			t.Fatalf("the trade's stored terms changed: %v", stored)
		}
		p, err := RequireActivePayout(ctx, a.ID.Hex(), 60)
		if err != nil || p.Version != 2 {
			t.Fatalf("active payout = %+v, %v", p, err)
		}
	})
}

func mustID(t *testing.T, hex string) bson.ObjectID {
	t.Helper()
	id, ok := mongox.ParseID(hex)
	if !ok {
		t.Fatalf("bad id %q", hex)
	}
	return id
}

var errInjected = errors.New("injected database failure")

// A failure at any step of a replacement leaves the previous version active,
// with its terms and open-endedness intact, and the next change succeeds.
func TestUpsertPayoutFailureMidUpdateKeepsPreviousVersion(t *testing.T) {
	for _, failAt := range []string{"insert", "deactivate", "activate"} {
		t.Run(failAt, func(t *testing.T) {
			forModes(t, func(t *testing.T, _ bool) {
				ctx := context.Background()
				a := seedAsset(t)
				v1 := upsert(t, a, 82)
				testHook = func(step string) error {
					if step == failAt {
						return errInjected
					}
					return nil
				}
				_, _, err := UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 90}, "")
				if !errors.Is(err, errInjected) {
					t.Fatalf("err = %v, want the injected failure", err)
				}
				testHook = nil
				active := assertOneActive(t, a)
				if active.ID.Hex() != v1.ID || active.PayoutPercent != 82 || active.EffectiveTo != nil || active.SupersededBy != nil {
					t.Fatalf("active row after the failed update: %+v", active)
				}
				for _, r := range slotRows(t, a) {
					if r.ID.Hex() != v1.ID && (r.Activation == nil || *r.Activation != models.PayoutActivationAbandoned) {
						t.Fatalf("leftover row v%d activation=%v", r.Version, r.Activation)
					}
				}
				p, err := RequireActivePayout(ctx, a.ID.Hex(), 60)
				if err != nil || p.ID.Hex() != v1.ID {
					t.Fatalf("trading reads %+v, %v; want v1", p, err)
				}
				next := upsert(t, a, 88)
				if got := assertOneActive(t, a); got.ID.Hex() != next.ID || got.PayoutPercent != 88 {
					t.Fatalf("follow-up update did not take: %+v", got)
				}
			})
		})
	}
}

// When the rollback itself fails too, trading is blocked rather than served
// half-written terms, and a repair (or the next change) restores the previous
// version.
func TestUpsertPayoutRollbackFailureBlocksTradesUntilRepaired(t *testing.T) {
	forModes(t, func(t *testing.T, tx bool) {
		ctx := context.Background()
		a := seedAsset(t)
		v1 := upsert(t, a, 82)
		testHook = func(step string) error {
			if step == "activate" || step == "rollback" {
				return errInjected
			}
			return nil
		}
		if _, _, err := UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 90}, ""); !errors.Is(err, errInjected) {
			t.Fatalf("err = %v", err)
		}
		testHook = nil

		if tx {
			// The transaction rolled everything back on its own.
			if got := assertOneActive(t, a); got.ID.Hex() != v1.ID {
				t.Fatalf("active = v%d", got.Version)
			}
			return
		}
		if _, err := RequireActivePayout(ctx, a.ID.Hex(), 60); code(err) != apperr.PayoutNotConfigured {
			t.Fatalf("trading on a half-replaced payout: err = %v, want PAYOUT_NOT_CONFIGURED", err)
		}
		res, err := RepairPayoutReplacements(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if res.Abandoned != 1 || res.Restored != 1 || len(res.Repaired) != 1 {
			t.Fatalf("repair = %+v", res)
		}
		if got := assertOneActive(t, a); got.ID.Hex() != v1.ID || got.PayoutPercent != 82 || got.EffectiveTo != nil {
			t.Fatalf("restored row = %+v", got)
		}
		if p, err := RequireActivePayout(ctx, a.ID.Hex(), 60); err != nil || p.ID.Hex() != v1.ID {
			t.Fatalf("after repair trading reads %+v, %v", p, err)
		}
		// Running it again finds nothing to do.
		if res, err := RepairPayoutReplacements(ctx); err != nil || res.Abandoned != 0 || res.Restored != 0 {
			t.Fatalf("second repair = %+v, %v", res, err)
		}
	})
}

// The next change repairs a half-finished replacement before applying itself.
func TestUpsertPayoutRepairsLeftoverReplacementFirst(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	a := seedAsset(t)
	upsert(t, a, 82)
	testHook = func(step string) error {
		if step == "activate" || step == "rollback" {
			return errInjected
		}
		return nil
	}
	_, _, _ = UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 90}, "")
	testHook = nil
	v3 := upsert(t, a, 86)
	if got := assertOneActive(t, a); got.ID.Hex() != v3.ID || got.Version != 3 {
		t.Fatalf("active = %+v", got)
	}
}

// Concurrent admin updates are applied one after another; whatever the
// interleaving, the slot ends with exactly one active row.
func TestConcurrentUpsertsLeaveOneActiveRow(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		a := seedAsset(t)
		upsert(t, a, 80)
		const n = 8
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _, errs[i] = UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: float64(81 + i)}, "")
			}(i)
		}
		wg.Wait()
		ok := 0
		for _, err := range errs {
			if err == nil {
				ok++
			} else if code(err) != apperr.Conflict {
				t.Fatalf("unexpected error %v", err)
			}
		}
		active := assertOneActive(t, a)
		if ok == 0 || active.Version < 2 {
			t.Fatalf("%d updates succeeded, active v%d", ok, active.Version)
		}
	})
}

// The database guards hold on their own: replacements racing with no slot
// lock at all still never leave two active rows (or none).
func TestRacingReplacementsWithoutTheLockStillLeaveOneActiveRow(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		a := seedAsset(t)
		upsert(t, a, 80)
		const n = 10
		var wg sync.WaitGroup
		errs := make([]error, n)
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				_, _, errs[i] = replacePayout(ctx, a, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: float64(81 + i)}, nil)
			}(i)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil && code(err) != apperr.Conflict {
				t.Fatalf("unexpected error %v", err)
			}
		}
		assertOneActive(t, a)
	})
}

// A read that was in flight across a change must not cache what it read
// before the change.
func TestInFlightReadDoesNotCacheSupersededTerms(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	a := seedAsset(t)
	upsert(t, a, 82)
	InvalidatePayoutCache()

	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	testHookLoaded = func() {
		once.Do(func() {
			close(entered)
			<-release
		})
	}
	stale := make(chan *models.PayoutConfig, 1)
	go func() {
		p, _ := FindActivePayout(ctx, a.ID.Hex(), 60)
		stale <- p
	}()
	<-entered
	v2 := upsert(t, a, 85)
	close(release)
	if p := <-stale; p == nil || p.PayoutPercent != 82 {
		t.Fatalf("in-flight read = %+v", p)
	}
	testHookLoaded = nil
	p, err := FindActivePayout(ctx, a.ID.Hex(), 60)
	if err != nil || p == nil || p.ID.Hex() != v2.ID {
		t.Fatalf("read after the change = %+v, %v; want v2", p, err)
	}
}

// Another instance's change is seen at once through the shared generation,
// not when this instance's cache TTL runs out.
func TestOtherInstanceChangeIsSeenWithoutWaitingForTheTTL(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	a := seedAsset(t)
	v1 := upsert(t, a, 82)
	if p, _ := FindActivePayout(ctx, a.ID.Hex(), 60); p == nil || p.ID.Hex() != v1.ID {
		t.Fatal("cache not warmed with v1")
	}

	// "Another instance" replaces the row directly in the database.
	coll := mongox.C(models.CollPayoutConfigs)
	now := mongox.Now()
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": mustID(t, v1.ID)}, bson.M{"$set": bson.M{"active": false, "effectiveTo": now}}); err != nil {
		t.Fatal(err)
	}
	v2 := models.PayoutConfig{ID: bson.NewObjectID(), AssetID: a.ID, ExpirySeconds: 60, PayoutPercent: 91, Version: 2,
		Active: true, EffectiveFrom: now, CreatedAt: now, UpdatedAt: now}
	if _, err := coll.InsertOne(ctx, v2); err != nil {
		t.Fatal(err)
	}
	// Until it publishes, this instance may still serve v1 from its cache.
	if p, _ := FindActivePayout(ctx, a.ID.Hex(), 60); p == nil || p.ID.Hex() != v1.ID {
		t.Fatalf("expected the cached v1 before the change is published, got %+v", p)
	}
	if err := redisx.R().Incr(ctx, generationKey()).Err(); err != nil {
		t.Fatal(err)
	}
	if p, _ := FindActivePayout(ctx, a.ID.Hex(), 60); p == nil || p.ID != v2.ID {
		t.Fatalf("after the published change got %+v, want v2", p)
	}
}

func TestDeactivatePayoutBlocksNewTrades(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	a := seedAsset(t)
	v1 := upsert(t, a, 82)
	if _, err := FindActivePayout(ctx, a.ID.Hex(), 60); err != nil {
		t.Fatal(err)
	}
	if _, err := DeactivatePayout(ctx, v1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireActivePayout(ctx, a.ID.Hex(), 60); code(err) != apperr.PayoutNotConfigured {
		t.Fatalf("err = %v", err)
	}
	// A deliberately deactivated slot is not "repaired" back to life.
	if res, err := RepairPayoutReplacements(ctx); err != nil || res.Restored != 0 {
		t.Fatalf("repair = %+v, %v", res, err)
	}
}

func TestUpsertPayoutRejectsInvalidInputAgainstTheStoredAsset(t *testing.T) {
	liveStores(t, false)
	ctx := context.Background()
	a := seedAsset(t)
	_, _, err := UpsertPayout(ctx, UpsertPayoutInput{AssetID: bson.NewObjectID().Hex(), ExpirySeconds: 60, PayoutPercent: 80}, "")
	if code(err) != apperr.AssetNotFound {
		t.Fatalf("unknown asset: %v", err)
	}
	_, _, err = UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 80,
		MinStake: i64p(5_000), MaxStake: i64p(4_000)}, "")
	if code(err) != apperr.ValidationError {
		t.Fatalf("min > max: %v", err)
	}
	if n := len(slotRows(t, a)); n != 0 {
		t.Fatalf("a rejected update wrote %d rows", n)
	}
}
