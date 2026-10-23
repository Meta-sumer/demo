package payouts

import (
	"context"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/leader"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// Scheduled payout changes (audit F-13: effective timestamps).
//
// A change with a future effectiveFrom is stored as a version with
// activation SCHEDULED: inactive, so trading never sees it early. The
// payout scheduler (a leader loop, every second) switches it on once
// effectiveFrom has passed, through the same failure-safe steps as an
// immediate change: under the slot lock, the version in force is closed and
// the scheduled one activated (in one transaction when the deployment has
// them; otherwise in an order a failure can undo, and a failed switch stays
// SCHEDULED and is retried). So there is exactly one active row per
// (asset, expiry) at any instant: the old row's effectiveTo and the new
// row's effectiveFrom are the same instant, the actual switch time (at most
// one scheduler tick after the requested one, which stays in
// scheduledFrom). Trades keep the terms they were accepted on.
//
// One change at a time per (asset, expiry): while a change is scheduled,
// another change (immediate or scheduled) is refused until the scheduled
// one is cancelled (DELETE /admin/assets/payouts/:id) or takes effect.

// MaxScheduleAhead is how far ahead a change may be scheduled.
const MaxScheduleAhead = 90 * 24 * time.Hour

// scheduleMinLead: an effectiveFrom closer than this is "now".
const scheduleMinLead = time.Second

func isScheduled(at *time.Time, now time.Time) bool {
	return at != nil && at.After(now.Add(scheduleMinLead))
}

func validateEffectiveFrom(at *time.Time, now time.Time) error {
	if at != nil && at.After(now.Add(MaxScheduleAhead)) {
		return apperr.Validation("effectiveFrom must be within 90 days", nil)
	}
	return nil
}

// assertNothingScheduled refuses a change while another is scheduled on the
// slot (run under the slot lock).
func assertNothingScheduled(ctx context.Context, assetID bson.ObjectID, expirySeconds int64) error {
	var s models.PayoutConfig
	err := mongox.C(models.CollPayoutConfigs).FindOne(ctx, bson.M{"assetId": assetID, "expirySeconds": expirySeconds,
		"activation": models.PayoutActivationScheduled}).Decode(&s)
	if mongox.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	when := s.EffectiveFrom.UTC().Format(time.RFC3339)
	return apperr.ConflictErr(apperr.Conflict, "A payout change (version "+strconv.FormatInt(s.Version, 10)+
		") is scheduled for "+when+" on this expiry. Cancel it first, or wait until it takes effect.")
}

// schedulePayout stores the next version as SCHEDULED (run under the slot
// lock). before is the version in force now.
func schedulePayout(ctx context.Context, asset *models.Asset, in UpsertPayoutInput, createdBy *bson.ObjectID) (*models.PayoutView, models.PayoutView, error) {
	coll := mongox.C(models.CollPayoutConfigs)
	var current *models.PayoutConfig
	var c models.PayoutConfig
	err := coll.FindOne(ctx, bson.M{"assetId": asset.ID, "expirySeconds": in.ExpirySeconds, "active": true}).Decode(&c)
	switch {
	case err == nil:
		current = &c
	case !mongox.IsNotFound(err):
		return nil, models.PayoutView{}, err
	}
	var latestVersion int64
	var l models.PayoutConfig
	err = coll.FindOne(ctx, bson.M{"assetId": asset.ID, "expirySeconds": in.ExpirySeconds},
		options.FindOne().SetSort(bson.D{{Key: "version", Value: -1}})).Decode(&l)
	if err == nil {
		latestVersion = l.Version
	} else if !mongox.IsNotFound(err) {
		return nil, models.PayoutView{}, err
	}
	now := mongox.Now()
	at := in.EffectiveFrom.UTC().Truncate(time.Millisecond)
	doc := models.PayoutConfig{
		ID: bson.NewObjectID(), AssetID: asset.ID, ExpirySeconds: in.ExpirySeconds, PayoutPercent: in.PayoutPercent,
		MinStake: in.MinStake, MaxStake: in.MaxStake, MinPayoutPercent: in.MinPayoutPercent, MaxPayoutPercent: in.MaxPayoutPercent,
		MarginPercent: in.MarginPercent, Version: latestVersion + 1, Active: false, EffectiveFrom: at, EffectiveTo: nil,
		CreatedBy: createdBy, Note: in.Note, Activation: models.Str(models.PayoutActivationScheduled), ScheduledFrom: &at,
		CreatedAt: now, UpdatedAt: now,
	}
	if current != nil {
		id := current.ID
		doc.Supersedes = &id
	}
	if _, err := coll.InsertOne(ctx, doc); err != nil {
		if mongox.IsDuplicateKey(err) {
			return nil, models.PayoutView{}, errSlotChanged()
		}
		return nil, models.PayoutView{}, err
	}
	logx.Info("payout change scheduled", "symbol", asset.Symbol, "expirySeconds", in.ExpirySeconds,
		"version", doc.Version, "effectiveFrom", at.Format(time.RFC3339))
	var before *models.PayoutView
	if current != nil {
		v := models.ToPayoutView(current)
		before = &v
	}
	return before, models.ToPayoutView(&doc), nil
}

// cancelScheduled withdraws a scheduled change. done is false when id is
// not a scheduled row (the caller then deactivates it as before).
func cancelScheduled(ctx context.Context, id bson.ObjectID) (models.PayoutView, bool, error) {
	var row models.PayoutConfig
	err := mongox.C(models.CollPayoutConfigs).FindOneAndUpdate(ctx,
		bson.M{"_id": id, "activation": models.PayoutActivationScheduled},
		bson.M{"$set": bson.M{"activation": models.PayoutActivationCancelled, "updatedAt": mongox.Now()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&row)
	if mongox.IsNotFound(err) {
		return models.PayoutView{}, false, nil
	}
	if err != nil {
		return models.PayoutView{}, true, err
	}
	logx.Info("scheduled payout change cancelled", "payoutId", id.Hex(), "version", row.Version)
	return models.ToPayoutView(&row), true, nil
}

// ActivationResult reports one ActivateDuePayouts pass.
type ActivationResult struct {
	Activated []string `json:"activated"`
	Retried   []string `json:"retried"`
}

// testHookActivate fails a scheduled activation at a named step
// ("deactivate", "activate"). Tests only.
var testHookActivate func(step string) error

func activateStep(name string) error {
	if testHookActivate != nil {
		return testHookActivate(name)
	}
	return nil
}

// ActivateDuePayouts switches on every scheduled change whose effectiveFrom
// has passed. Safe to run on any instance at any time.
func ActivateDuePayouts(ctx context.Context, now time.Time) (ActivationResult, error) {
	ctx = context.WithoutCancel(ctx)
	out := ActivationResult{Activated: []string{}, Retried: []string{}}
	cur, err := mongox.C(models.CollPayoutConfigs).Find(ctx, bson.M{"activation": models.PayoutActivationScheduled,
		"effectiveFrom": bson.M{"$lte": now.UTC()}}, options.Find().SetSort(bson.D{{Key: "effectiveFrom", Value: 1}}))
	if err != nil {
		return out, err
	}
	var due []models.PayoutConfig
	if err := cur.All(ctx, &due); err != nil {
		return out, err
	}
	for i := range due {
		row := &due[i]
		err := withSlotLock(ctx, row.AssetID, row.ExpirySeconds, 2*time.Second, func() error {
			if _, err := repairSlot(ctx, row.AssetID, row.ExpirySeconds); err != nil {
				return err
			}
			return activateScheduled(ctx, row)
		})
		if err != nil {
			out.Retried = append(out.Retried, row.ID.Hex())
			logx.Warn("scheduled payout change not activated yet; retried next tick", "payoutId", row.ID.Hex(), "err", err.Error())
			continue
		}
		out.Activated = append(out.Activated, row.ID.Hex())
	}
	if len(out.Activated) > 0 {
		publishChange(ctx)
	}
	return out, nil
}

// activateScheduled closes the version in force and switches the scheduled
// one on, at one instant (run under the slot lock).
func activateScheduled(ctx context.Context, row *models.PayoutConfig) error {
	coll := mongox.C(models.CollPayoutConfigs)
	var closedID *bson.ObjectID
	err := mongox.WithTransaction(ctx, func(tctx context.Context) error {
		closedID = nil
		now := mongox.Now()
		var c models.PayoutConfig
		err := coll.FindOne(tctx, bson.M{"assetId": row.AssetID, "expirySeconds": row.ExpirySeconds, "active": true}).Decode(&c)
		if err != nil && !mongox.IsNotFound(err) {
			return err
		}
		if err == nil {
			if err := activateStep("deactivate"); err != nil {
				return err
			}
			res, err := coll.UpdateOne(tctx, bson.M{"_id": c.ID, "active": true},
				bson.M{"$set": bson.M{"active": false, "effectiveTo": now, "supersededBy": row.ID, "updatedAt": now}})
			if err != nil {
				return err
			}
			if res.MatchedCount == 0 {
				return errSlotChanged()
			}
			id := c.ID
			closedID = &id
		}
		if err := activateStep("activate"); err != nil {
			return err
		}
		set := bson.M{"active": true, "activation": models.PayoutActivationActivated, "effectiveFrom": now, "updatedAt": now}
		update := bson.M{"$set": set}
		if closedID != nil {
			set["supersedes"] = *closedID
		} else {
			update["$unset"] = bson.M{"supersedes": ""}
		}
		res, err := coll.UpdateOne(tctx, bson.M{"_id": row.ID, "activation": models.PayoutActivationScheduled}, update)
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			// Cancelled meanwhile.
			return errSlotChanged()
		}
		return nil
	})
	if err != nil {
		// Without a transaction the closed row may be closed already:
		// reopen it; the scheduled row is still SCHEDULED and is retried.
		if closedID != nil {
			if _, rerr := mongox.C(models.CollPayoutConfigs).UpdateOne(context.WithoutCancel(ctx),
				bson.M{"_id": *closedID, "active": false, "supersededBy": row.ID},
				bson.M{"$set": bson.M{"active": true, "effectiveTo": nil, "updatedAt": mongox.Now()}, "$unset": bson.M{"supersededBy": ""}}); rerr != nil {
				logx.Error("could not reopen the payout version after a failed scheduled activation; new trades on this expiry stay blocked until repaired",
					"payoutId", closedID.Hex(), "err", rerr.Error())
			}
		}
		publishChange(ctx)
		return err
	}
	logx.Info("scheduled payout change activated", "payoutId", row.ID.Hex(), "version", row.Version,
		"scheduledFrom", row.EffectiveFrom.Format(time.RFC3339))
	return nil
}

var scheduler = leader.New(leader.Options{
	Name:     "payout-scheduler",
	Interval: time.Second,
	OnTick: func(ctx context.Context) error {
		_, err := ActivateDuePayouts(ctx, time.Now())
		return err
	},
})

// StartScheduler runs the payout scheduler (leader only).
func StartScheduler() { scheduler.Start() }

// StopScheduler stops it.
func StopScheduler(context.Context) { scheduler.Stop() }
