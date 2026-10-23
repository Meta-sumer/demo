// Package payouts is the payout configuration (spec section 24).
//
// A change never mutates the terms of a row: the current version is closed
// off and a new one is inserted. Trades store the payout percent and config
// version they were accepted under, so changing a payout can never revalue
// open or historical trades (rule 11).
//
// Replacing a version is failure-safe. With transactions the whole switch
// commits or nothing does. Without them the steps run in an order that can
// always be undone: the new version is inserted PENDING (trading reads active
// rows only, so it is invisible), the current row is closed, and the new row
// is switched on. A failure part way abandons the new row and restores the
// one it closed. The partial unique index on active rows means two versions
// can never be live at once, and a second one allows one replacement in
// flight per (asset, expiry). The worst case - the database failing during
// the rollback itself - leaves no active row, which blocks new trades on that
// expiry (PAYOUT_NOT_CONFIGURED) until the next change or
// RepairPayoutReplacements restores it. Trading never sees half-written terms.
package payouts

import (
	"context"
	"errors"
	"math"
	"slices"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
)

const cacheTTL = 2 * time.Second

var (
	cacheMu   sync.Mutex
	cacheAt   time.Time
	cacheRows []*models.PayoutConfig
	// cacheEpoch is bumped by every local invalidation, so a read that was in
	// flight across a change cannot cache what it read before the change.
	cacheEpoch uint64
	// cacheRemote is the shared generation (see publishChange) the rows were
	// loaded under; "" when Redis could not be read.
	cacheRemote string
)

// testHookLoaded runs between reading the active rows and caching them. Tests
// only.
var testHookLoaded func()

// InvalidatePayoutCache drops the cached active rows.
func InvalidatePayoutCache() {
	cacheMu.Lock()
	cacheRows = nil
	cacheEpoch++
	cacheMu.Unlock()
}

func generationKey() string { return config.PayoutGenerationKey(mongox.DB().Name()) }

// remoteGeneration reads the shared change counter. "" means Redis did not
// answer, and the cache falls back to its TTL.
func remoteGeneration(ctx context.Context) string {
	c := redisx.R()
	if c == nil || mongox.DB() == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	v, err := c.Get(ctx, generationKey()).Result()
	if err != nil {
		if redisx.IsNil(err) {
			return "0"
		}
		return ""
	}
	return v
}

// publishChange drops this instance's cache and bumps the shared generation,
// so every other instance reloads on its next read instead of serving the
// superseded terms until its TTL runs out.
func publishChange(ctx context.Context) {
	InvalidatePayoutCache()
	c := redisx.R()
	if c == nil || mongox.DB() == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := c.Incr(ctx, generationKey()).Err(); err != nil {
		logx.Warn("could not publish a payout change; other instances catch up within the cache TTL", "err", err.Error())
	}
}

func loadActive(ctx context.Context) ([]*models.PayoutConfig, error) {
	remote := remoteGeneration(ctx)
	cacheMu.Lock()
	if cacheRows != nil && time.Since(cacheAt) < cacheTTL && (remote == "" || remote == cacheRemote) {
		r := cacheRows
		cacheMu.Unlock()
		return r, nil
	}
	epoch := cacheEpoch
	cacheMu.Unlock()

	cur, err := mongox.C(models.CollPayoutConfigs).Find(ctx, bson.M{"active": true})
	if err != nil {
		return nil, err
	}
	rows := []*models.PayoutConfig{}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	if testHookLoaded != nil {
		testHookLoaded()
	}
	cacheMu.Lock()
	// A change landed while this read was in flight: serve the rows to this
	// caller, but do not keep them.
	if epoch == cacheEpoch {
		cacheRows, cacheAt, cacheRemote = rows, time.Now(), remote
	}
	cacheMu.Unlock()
	return rows, nil
}

// FindActivePayout returns the active row (shared, read-only) or nil.
func FindActivePayout(ctx context.Context, assetID string, expirySeconds int64) (*models.PayoutConfig, error) {
	rows, err := loadActive(ctx)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.AssetID.Hex() == assetID && r.ExpirySeconds == expirySeconds {
			return r, nil
		}
	}
	return nil, nil
}

// RequireActivePayout fails with PAYOUT_NOT_CONFIGURED (409).
func RequireActivePayout(ctx context.Context, assetID string, expirySeconds int64) (*models.PayoutConfig, error) {
	p, err := FindActivePayout(ctx, assetID, expirySeconds)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, apperr.New(apperr.PayoutNotConfigured, "No payout is configured for that expiry", 409)
	}
	return p, nil
}

// ListActivePayouts: assetID "" lists every asset. Sorted by expiry.
func ListActivePayouts(ctx context.Context, assetID string) ([]models.PayoutView, error) {
	rows, err := loadActive(ctx)
	if err != nil {
		return nil, err
	}
	picked := make([]*models.PayoutConfig, 0, len(rows))
	for _, r := range rows {
		if assetID == "" || r.AssetID.Hex() == assetID {
			picked = append(picked, r)
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return picked[i].ExpirySeconds < picked[j].ExpirySeconds })
	out := make([]models.PayoutView, 0, len(picked))
	for _, r := range picked {
		out = append(out, models.ToPayoutView(r))
	}
	return out, nil
}

// ListPayoutVersions returns every version for an asset, optionally one expiry.
func ListPayoutVersions(ctx context.Context, assetID string, expirySeconds *int64) ([]models.PayoutView, error) {
	out := []models.PayoutView{}
	id, ok := mongox.ParseID(assetID)
	if !ok {
		return nil, apperr.Validation("Invalid asset id", nil)
	}
	filter := bson.M{"assetId": id}
	if expirySeconds != nil {
		filter["expirySeconds"] = *expirySeconds
	}
	cur, err := mongox.C(models.CollPayoutConfigs).Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "expirySeconds", Value: 1}, {Key: "version", Value: -1}}))
	if err != nil {
		return nil, err
	}
	var rows []models.PayoutConfig
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for i := range rows {
		out = append(out, models.ToPayoutView(&rows[i]))
	}
	return out, nil
}

type UpsertPayoutInput struct {
	AssetID       string
	ExpirySeconds int64
	PayoutPercent float64
	MinStake      *int64
	MaxStake      *int64
	Note          *string
	// Pricing-engine bounds (models.PayoutConfig); nil = default.
	MinPayoutPercent *float64
	MaxPayoutPercent *float64
	MarginPercent    *float64
	// When the change takes effect; nil or not in the future = now. A future
	// instant schedules it (schedule.go).
	EffectiveFrom *time.Time
}

// MaxPayoutPercent is the highest payout a version may offer.
const MaxPayoutPercent = 500

// PayoutStepPercent is the pricing engine's payout step (0.5 points): clamp
// bounds must sit on it.
const PayoutStepPercent = 0.5

func expiryLabel(s int64) string { return strconv.FormatInt(s, 10) + "s" }

// ValidatePayoutInput checks a payout against the asset it is for: the expiry
// must be one the asset offers, the percent must pay something and stay
// within the cap, and the stake bounds must be ordered and leave a range the
// asset's own bounds do not rule out entirely.
func ValidatePayoutInput(asset *models.Asset, in UpsertPayoutInput) error {
	if in.ExpirySeconds <= 0 || !slices.Contains(asset.SupportedExpiries, in.ExpirySeconds) {
		return apperr.Validation("Expiry "+expiryLabel(in.ExpirySeconds)+
			" is not in the supported expiries for "+asset.Symbol, nil)
	}
	p := in.PayoutPercent
	if math.IsNaN(p) || math.IsInf(p, 0) || p <= 0 || p > MaxPayoutPercent {
		return apperr.Validation("payoutPercent must be greater than 0 and at most 500", nil)
	}
	if in.MinStake != nil && *in.MinStake <= 0 {
		return apperr.Validation("minStake must be positive", nil)
	}
	if in.MaxStake != nil && *in.MaxStake <= 0 {
		return apperr.Validation("maxStake must be positive", nil)
	}
	if in.MinStake != nil && in.MaxStake != nil && *in.MinStake > *in.MaxStake {
		return apperr.Validation("minStake must not exceed maxStake", nil)
	}
	if in.MinStake != nil && asset.MaxStake > 0 && *in.MinStake > asset.MaxStake {
		return apperr.Validation("minStake is above the maximum stake for "+asset.Symbol+
			" ("+strconv.FormatInt(asset.MaxStake, 10)+"), so no stake could be accepted", nil)
	}
	if in.MaxStake != nil && *in.MaxStake < asset.MinStake {
		return apperr.Validation("maxStake is below the minimum stake for "+asset.Symbol+
			" ("+strconv.FormatInt(asset.MinStake, 10)+"), so no stake could be accepted", nil)
	}
	// Pricing-engine terms: the clamp bounds must be ordered and on the
	// 0.5-point payout step (so rounding down never leaves them), and the
	// margin a plausible deduction.
	pctOK := func(x *float64) bool {
		if x == nil {
			return true
		}
		steps := *x / PayoutStepPercent
		return !math.IsNaN(*x) && !math.IsInf(*x, 0) && *x > 0 && *x <= MaxPayoutPercent &&
			math.Abs(steps-math.Round(steps)) < 1e-6
	}
	if !pctOK(in.MinPayoutPercent) || !pctOK(in.MaxPayoutPercent) {
		return apperr.Validation("minPayoutPercent and maxPayoutPercent must be multiples of 0.5, greater than 0 and at most 500", nil)
	}
	if in.MinPayoutPercent != nil && in.MaxPayoutPercent != nil && *in.MinPayoutPercent > *in.MaxPayoutPercent {
		return apperr.Validation("minPayoutPercent must not exceed maxPayoutPercent", nil)
	}
	if m := in.MarginPercent; m != nil && (math.IsNaN(*m) || *m < 0 || *m > 50) {
		return apperr.Validation("marginPercent must be between 0 and 50", nil)
	}
	return nil
}

// loadAssetFresh reads the asset from the database rather than the 2 s asset
// cache: a payout must be checked against the expiries the asset offers now.
func loadAssetFresh(ctx context.Context, assetID string) (*models.Asset, error) {
	notFound := apperr.NotFoundErr(apperr.AssetNotFound, "Asset not found")
	id, ok := mongox.ParseID(assetID)
	if !ok {
		return nil, notFound
	}
	var a models.Asset
	if err := mongox.C(models.CollAssets).FindOne(ctx, bson.M{"_id": id}).Decode(&a); err != nil {
		if mongox.IsNotFound(err) {
			return nil, notFound
		}
		return nil, err
	}
	return &a, nil
}

const (
	// Longer than the slowest replacement, so the lock outlives its owner's work.
	slotLockTTL  = 60 * time.Second
	slotLockWait = 10 * time.Second
)

func slotName(assetID bson.ObjectID, expirySeconds int64) string {
	return "payout:" + assetID.Hex() + ":" + strconv.FormatInt(expirySeconds, 10)
}

// withSlotLock serialises changes to one (asset, expiry) across instances.
// The database guards hold without it; it turns concurrent admin edits into
// a queue instead of conflicts.
func withSlotLock(ctx context.Context, assetID bson.ObjectID, expirySeconds int64, wait time.Duration, fn func() error) error {
	err := redisx.WithLock(ctx, slotName(assetID, expirySeconds), slotLockTTL, wait, fn)
	if redisx.IsLockTimeout(err) {
		return apperr.ConflictErr(apperr.Conflict, "Another change to this payout is in progress. Try again in a moment.")
	}
	return err
}

func errSlotChanged() error {
	return apperr.ConflictErr(apperr.Conflict,
		"The payout for this expiry changed while it was being updated. Reload and try again.")
}

// testHook fails a replacement at a named step ("insert", "deactivate",
// "activate", "rollback"). Tests only.
var testHook func(step string) error

func step(name string) error {
	if testHook != nil {
		return testHook(name)
	}
	return nil
}

// UpsertPayout supersedes the active row and inserts the next version.
// before is nil when no version was active. Concurrent changes to one
// (asset, expiry) are applied one after the other and always leave exactly one
// active row; a failed change leaves the previous version active.
func UpsertPayout(ctx context.Context, in UpsertPayoutInput, actorID string) (before *models.PayoutView, after models.PayoutView, err error) {
	// A switch must not be abandoned half way because the HTTP client left.
	ctx = context.WithoutCancel(ctx)
	asset, err := loadAssetFresh(ctx, in.AssetID)
	if err != nil {
		return nil, after, err
	}
	if err := ValidatePayoutInput(asset, in); err != nil {
		return nil, after, err
	}
	if err := validateEffectiveFrom(in.EffectiveFrom, time.Now()); err != nil {
		return nil, after, err
	}
	var createdBy *bson.ObjectID
	if actor, ok := mongox.ParseID(actorID); ok {
		createdBy = &actor
	}

	scheduled := isScheduled(in.EffectiveFrom, time.Now())
	err = withSlotLock(ctx, asset.ID, in.ExpirySeconds, slotLockWait, func() error {
		// Anything a crash left half done is resolved before another change.
		if _, err := repairSlot(ctx, asset.ID, in.ExpirySeconds); err != nil {
			return err
		}
		// One change at a time: a scheduled one must be cancelled (or take
		// effect) before another is made.
		if err := assertNothingScheduled(ctx, asset.ID, in.ExpirySeconds); err != nil {
			return err
		}
		var err error
		if scheduled {
			before, after, err = schedulePayout(ctx, asset, in, createdBy)
		} else {
			before, after, err = replacePayout(ctx, asset, in, createdBy)
		}
		return err
	})
	if err != nil {
		return nil, models.PayoutView{}, err
	}
	logx.Info("payout configuration versioned", "symbol", asset.Symbol,
		"expirySeconds", in.ExpirySeconds, "payoutPercent", in.PayoutPercent, "version", after.Version)
	return before, after, nil
}

func replacePayout(ctx context.Context, asset *models.Asset, in UpsertPayoutInput, createdBy *bson.ObjectID) (*models.PayoutView, models.PayoutView, error) {
	coll := mongox.C(models.CollPayoutConfigs)
	pendingID := bson.NewObjectID()
	var current *models.PayoutConfig
	var doc models.PayoutConfig

	err := mongox.WithTransaction(ctx, func(tctx context.Context) error {
		current = nil
		var c models.PayoutConfig
		err := coll.FindOne(tctx, bson.M{"assetId": asset.ID, "expirySeconds": in.ExpirySeconds, "active": true}).Decode(&c)
		if err == nil {
			current = &c
		} else if !mongox.IsNotFound(err) {
			return err
		}
		var latestVersion int64
		var l models.PayoutConfig
		err = coll.FindOne(tctx, bson.M{"assetId": asset.ID, "expirySeconds": in.ExpirySeconds},
			options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})).Decode(&l)
		if err == nil {
			latestVersion = l.Version
		} else if !mongox.IsNotFound(err) {
			return err
		}

		now := mongox.Now()
		doc = models.PayoutConfig{
			ID:            pendingID,
			AssetID:       asset.ID,
			ExpirySeconds: in.ExpirySeconds,
			PayoutPercent: in.PayoutPercent,
			MinStake:      in.MinStake,
			MaxStake:      in.MaxStake,
			// Pricing-engine bounds.
			MinPayoutPercent: in.MinPayoutPercent,
			MaxPayoutPercent: in.MaxPayoutPercent,
			MarginPercent:    in.MarginPercent,
			Version:          latestVersion + 1,
			Active:           false,
			EffectiveFrom:    now,
			EffectiveTo:      nil,
			CreatedBy:        createdBy,
			Note:             in.Note,
			Activation:       models.Str(models.PayoutActivationPending),
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if current != nil {
			id := current.ID
			doc.Supersedes = &id
		}

		// 1. The new version, not yet live.
		if err := step("insert"); err != nil {
			return err
		}
		if _, err := coll.InsertOne(tctx, doc); err != nil {
			return err
		}
		// 2. Close the current version. Conditional: if it is no longer the
		// active row, someone else changed this payout and this attempt stops.
		if current != nil {
			if err := step("deactivate"); err != nil {
				return err
			}
			res, err := coll.UpdateOne(tctx, bson.M{"_id": current.ID, "active": true},
				bson.M{"$set": bson.M{"active": false, "effectiveTo": now, "supersededBy": pendingID, "updatedAt": now}})
			if err != nil {
				return err
			}
			if res.MatchedCount == 0 {
				return errSlotChanged()
			}
		}
		// 3. Switch the new version on, unless a repair abandoned it meanwhile.
		if err := step("activate"); err != nil {
			return err
		}
		res, err := coll.UpdateOne(tctx, bson.M{"_id": pendingID, "activation": models.PayoutActivationPending},
			bson.M{"$set": bson.M{"active": true, "activation": models.PayoutActivationActivated, "effectiveFrom": now, "updatedAt": now}})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return errSlotChanged()
		}
		doc.Active = true
		doc.Activation = models.Str(models.PayoutActivationActivated)
		return nil
	})
	if err != nil {
		// With a transaction nothing was written and this finds nothing to
		// undo. Without one it abandons the new row and restores the old.
		if _, rerr := rollbackReplacement(ctx, pendingID); rerr != nil {
			logx.Error("payout replacement could not be rolled back; new trades on this expiry stay blocked until repaired",
				"symbol", asset.Symbol, "expirySeconds", in.ExpirySeconds, "pendingId", pendingID.Hex(), "err", rerr.Error())
		}
		publishChange(ctx)
		if mongox.IsDuplicateKey(err) || isWriteConflict(err) {
			return nil, models.PayoutView{}, errSlotChanged()
		}
		return nil, models.PayoutView{}, err
	}
	publishChange(ctx)

	var before *models.PayoutView
	if current != nil {
		v := models.ToPayoutView(current)
		before = &v
	}
	return before, models.ToPayoutView(&doc), nil
}

// isWriteConflict: a concurrent transaction touched the same rows and the
// retries ran out - another change won.
func isWriteConflict(err error) bool {
	var se mongo.ServerError
	return errors.As(err, &se) && (se.HasErrorCode(112) || se.HasErrorLabel("TransientTransactionError"))
}

// rollbackReplacement undoes a replacement that did not finish: the pending
// row is abandoned and, unless it had already gone live, the row it closed is
// restored. Every write is conditional, so it can run more than once, and
// alongside a slow original attempt, without ever producing two live rows.
// restored reports whether a previous version was put back in force.
func rollbackReplacement(ctx context.Context, pendingID bson.ObjectID) (restored bool, err error) {
	if err := step("rollback"); err != nil {
		return false, err
	}
	coll := mongox.C(models.CollPayoutConfigs)
	now := mongox.Now()
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": pendingID, "activation": models.PayoutActivationPending},
		bson.M{"$set": bson.M{"activation": models.PayoutActivationAbandoned, "active": false, "effectiveTo": now, "updatedAt": now}}); err != nil {
		return false, err
	}
	var p models.PayoutConfig
	if err := coll.FindOne(ctx, bson.M{"_id": pendingID}).Decode(&p); err != nil {
		if mongox.IsNotFound(err) {
			// Never written, so nothing was closed on its behalf.
			return false, nil
		}
		return false, err
	}
	if p.Activation != nil && *p.Activation == models.PayoutActivationActivated {
		return false, nil
	}
	return restoreSuperseded(ctx, pendingID)
}

// restoreSuperseded reopens the row an abandoned replacement had closed.
func restoreSuperseded(ctx context.Context, abandonedID bson.ObjectID) (bool, error) {
	res, err := mongox.C(models.CollPayoutConfigs).UpdateOne(ctx,
		bson.M{"supersededBy": abandonedID, "active": false},
		bson.M{"$set": bson.M{"active": true, "effectiveTo": nil, "updatedAt": mongox.Now()}, "$unset": bson.M{"supersededBy": ""}})
	if err != nil {
		if mongox.IsDuplicateKey(err) {
			// Another version is live already: leave it.
			return false, nil
		}
		return false, err
	}
	return res.ModifiedCount > 0, nil
}

// PayoutRepairResult reports what RepairPayoutReplacements resolved.
type PayoutRepairResult struct {
	// Replacements found half done and abandoned.
	Abandoned int `json:"abandoned"`
	// Previous versions put back in force.
	Restored int `json:"restored"`
	// Slots ("<assetId>:<expirySeconds>") that were touched or skipped.
	Repaired []string `json:"repaired"`
	Skipped  []string `json:"skipped"`
}

// repairSlot resolves a replacement left half done on one (asset, expiry).
// Run under the slot lock, where a PENDING row is by definition stale.
func repairSlot(ctx context.Context, assetID bson.ObjectID, expirySeconds int64) (PayoutRepairResult, error) {
	var out PayoutRepairResult
	coll := mongox.C(models.CollPayoutConfigs)
	inSlot := func(extra bson.M) bson.M {
		f := bson.M{"assetId": assetID, "expirySeconds": expirySeconds}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}

	cur, err := coll.Find(ctx, inSlot(bson.M{"activation": models.PayoutActivationPending}))
	if err != nil {
		return out, err
	}
	var pending []models.PayoutConfig
	if err := cur.All(ctx, &pending); err != nil {
		return out, err
	}
	for _, p := range pending {
		restored, err := rollbackReplacement(ctx, p.ID)
		if err != nil {
			return out, err
		}
		out.Abandoned++
		if restored {
			out.Restored++
		}
		logx.Warn("abandoned a payout replacement that never finished", "assetId", assetID.Hex(),
			"expirySeconds", expirySeconds, "version", p.Version, "restoredPrevious", restored)
	}

	// A rollback cut short after abandoning: no live row, and the latest
	// closed row was closed for a replacement that was abandoned. A slot an
	// operator deactivated on purpose does not match: its closed rows point at
	// versions that went live, or at nothing.
	n, err := coll.CountDocuments(ctx, inSlot(bson.M{"active": true}))
	if err != nil {
		return out, err
	}
	if n == 0 {
		var closed models.PayoutConfig
		err := coll.FindOne(ctx, inSlot(bson.M{"active": false, "supersededBy": bson.M{"$type": "objectId"}}),
			options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})).Decode(&closed)
		if err != nil && !mongox.IsNotFound(err) {
			return out, err
		}
		if err == nil {
			var by models.PayoutConfig
			err := coll.FindOne(ctx, bson.M{"_id": *closed.SupersededBy}).Decode(&by)
			if err != nil && !mongox.IsNotFound(err) {
				return out, err
			}
			if err == nil && by.Activation != nil && *by.Activation == models.PayoutActivationAbandoned {
				restored, err := restoreSuperseded(ctx, by.ID)
				if err != nil {
					return out, err
				}
				if restored {
					out.Restored++
					logx.Warn("restored a payout version whose replacement was abandoned", "assetId", assetID.Hex(),
						"expirySeconds", expirySeconds, "version", closed.Version)
				}
			}
		}
	}
	if out.Abandoned > 0 || out.Restored > 0 {
		publishChange(ctx)
	}
	return out, nil
}

// RepairPayoutReplacements resolves every replacement a crash or a database
// failure left half done: in-flight rows are abandoned and the versions they
// closed are restored. Safe to run at any time; an operator runs it from
// POST /admin/assets/payouts/repair.
func RepairPayoutReplacements(ctx context.Context) (PayoutRepairResult, error) {
	ctx = context.WithoutCancel(ctx)
	out := PayoutRepairResult{Repaired: []string{}, Skipped: []string{}}
	coll := mongox.C(models.CollPayoutConfigs)
	cur, err := coll.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"activation": bson.M{"$in": bson.A{
			models.PayoutActivationPending, models.PayoutActivationAbandoned,
		}}}}},
		{{Key: "$group", Value: bson.M{"_id": bson.M{"assetId": "$assetId", "expirySeconds": "$expirySeconds"}}}},
	})
	if err != nil {
		return out, err
	}
	var slots []struct {
		ID struct {
			AssetID       bson.ObjectID `bson:"assetId"`
			ExpirySeconds int64         `bson:"expirySeconds"`
		} `bson:"_id"`
	}
	if err := cur.All(ctx, &slots); err != nil {
		return out, err
	}
	for _, s := range slots {
		name := s.ID.AssetID.Hex() + ":" + strconv.FormatInt(s.ID.ExpirySeconds, 10)
		var res PayoutRepairResult
		err := withSlotLock(ctx, s.ID.AssetID, s.ID.ExpirySeconds, 2*time.Second, func() error {
			var err error
			res, err = repairSlot(ctx, s.ID.AssetID, s.ID.ExpirySeconds)
			return err
		})
		if err != nil {
			if apperr.HasCode(err, apperr.Conflict) {
				out.Skipped = append(out.Skipped, name)
				continue
			}
			return out, err
		}
		out.Abandoned += res.Abandoned
		out.Restored += res.Restored
		if res.Abandoned > 0 || res.Restored > 0 {
			out.Repaired = append(out.Repaired, name)
		}
	}
	return out, nil
}

// DeactivatePayout closes a payout row; on a scheduled change it cancels it.
func DeactivatePayout(ctx context.Context, payoutID string) (models.PayoutView, error) {
	notFound := apperr.NotFoundErr(apperr.NotFound, "Payout configuration not found")
	id, ok := mongox.ParseID(payoutID)
	if !ok {
		return models.PayoutView{}, notFound
	}
	if v, done, err := cancelScheduled(ctx, id); err != nil || done {
		return v, err
	}
	now := mongox.Now()
	var row models.PayoutConfig
	err := mongox.C(models.CollPayoutConfigs).FindOneAndUpdate(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"active": false, "effectiveTo": now, "updatedAt": now}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&row)
	if err != nil {
		if mongox.IsNotFound(err) {
			return models.PayoutView{}, notFound
		}
		return models.PayoutView{}, err
	}
	publishChange(ctx)
	return models.ToPayoutView(&row), nil
}
