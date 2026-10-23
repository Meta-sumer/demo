package payouts

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
)

func schedule(t *testing.T, a *models.Asset, percent float64, at time.Time) models.PayoutView {
	t.Helper()
	_, after, err := UpsertPayout(context.Background(), UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60,
		PayoutPercent: percent, EffectiveFrom: &at}, "")
	if err != nil {
		t.Fatalf("schedule %v%%: %v", percent, err)
	}
	return after
}

func load(t *testing.T, id string) models.PayoutConfig {
	t.Helper()
	var p models.PayoutConfig
	if err := mongox.C(models.CollPayoutConfigs).FindOne(context.Background(), bson.M{"_id": mustID(t, id)}).Decode(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// A change with a future effectiveFrom is invisible to trading until then,
// then replaces the version in force at one instant; existing trades keep
// their terms; one change at a time per slot.
func TestScheduledPayoutTakesEffectAtItsTime(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		a := seedAsset(t)
		v1 := upsert(t, a, 82)
		at := time.Now().Add(time.Hour)
		v2 := schedule(t, a, 85, at)
		if v2.Active || v2.Activation == nil || *v2.Activation != models.PayoutActivationScheduled || v2.Version != 2 ||
			v2.ScheduledFrom == nil {
			t.Fatalf("scheduled row %+v", v2)
		}
		// Still v1 for trading; exactly one active row.
		InvalidatePayoutCache()
		if p, err := RequireActivePayout(ctx, a.ID.Hex(), 60); err != nil || p.Version != 1 || p.PayoutPercent != 82 {
			t.Fatalf("active before the time %+v %v", p, err)
		}
		assertOneActive(t, a)

		// Another change while one is scheduled is refused.
		if _, _, err := UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 80}, ""); !apperr.HasCode(err, apperr.Conflict) {
			t.Fatalf("immediate change while scheduled: %v", err)
		}
		later := at.Add(time.Hour)
		if _, _, err := UpsertPayout(ctx, UpsertPayoutInput{AssetID: a.ID.Hex(), ExpirySeconds: 60, PayoutPercent: 80, EffectiveFrom: &later}, ""); !apperr.HasCode(err, apperr.Conflict) {
			t.Fatalf("second schedule: %v", err)
		}

		// Not due: nothing happens.
		if res, err := ActivateDuePayouts(ctx, time.Now()); err != nil || len(res.Activated) != 0 {
			t.Fatalf("early activation %+v %v", res, err)
		}
		// Due (the scheduler's clock passes effectiveFrom).
		res, err := ActivateDuePayouts(ctx, at.Add(time.Second))
		if err != nil || len(res.Activated) != 1 || res.Activated[0] != v2.ID {
			t.Fatalf("activation %+v %v", res, err)
		}
		active := assertOneActive(t, a)
		if active.ID.Hex() != v2.ID || active.PayoutPercent != 85 || active.Activation == nil ||
			*active.Activation != models.PayoutActivationActivated || active.ScheduledFrom == nil {
			t.Fatalf("active %+v", active)
		}
		old := load(t, v1.ID)
		if old.Active || old.EffectiveTo == nil || !old.EffectiveTo.Equal(active.EffectiveFrom) || old.SupersededBy == nil ||
			old.SupersededBy.Hex() != v2.ID || old.PayoutPercent != 82 {
			t.Fatalf("closed row %+v (new effectiveFrom %v)", old, active.EffectiveFrom)
		}
		InvalidatePayoutCache()
		if p, _ := RequireActivePayout(ctx, a.ID.Hex(), 60); p == nil || p.Version != 2 {
			t.Fatalf("active after %+v", p)
		}
		// A second pass finds nothing; the slot accepts changes again.
		if res, _ := ActivateDuePayouts(ctx, at.Add(2*time.Second)); len(res.Activated) != 0 {
			t.Fatalf("second pass %+v", res)
		}
		upsert(t, a, 84)
		assertOneActive(t, a)
	})
}

// A failed activation leaves the previous version in force and the change
// scheduled; the next tick activates it.
func TestScheduledActivationFailureIsRetried(t *testing.T) {
	for _, failAt := range []string{"deactivate", "activate"} {
		t.Run(failAt, func(t *testing.T) {
			forModes(t, func(t *testing.T, _ bool) {
				ctx := context.Background()
				a := seedAsset(t)
				v1 := upsert(t, a, 82)
				at := time.Now().Add(time.Minute)
				v2 := schedule(t, a, 86, at)
				testHookActivate = func(step string) error {
					if step == failAt {
						return errInjected
					}
					return nil
				}
				res, err := ActivateDuePayouts(ctx, at.Add(time.Second))
				testHookActivate = nil
				if err != nil || len(res.Retried) != 1 || len(res.Activated) != 0 {
					t.Fatalf("failed pass %+v %v", res, err)
				}
				if got := assertOneActive(t, a); got.ID.Hex() != v1.ID || got.EffectiveTo != nil {
					t.Fatalf("after a failed activation the active row is %+v", got)
				}
				if s := load(t, v2.ID); s.Activation == nil || *s.Activation != models.PayoutActivationScheduled || s.Active {
					t.Fatalf("scheduled row %+v", s)
				}
				res, err = ActivateDuePayouts(ctx, at.Add(2*time.Second))
				if err != nil || len(res.Activated) != 1 {
					t.Fatalf("retry %+v %v", res, err)
				}
				if got := assertOneActive(t, a); got.ID.Hex() != v2.ID {
					t.Fatalf("after the retry the active row is v%d", got.Version)
				}
			})
		})
	}
}

// A scheduled change can be cancelled; it then never takes effect.
func TestScheduledPayoutCanBeCancelled(t *testing.T) {
	forModes(t, func(t *testing.T, _ bool) {
		ctx := context.Background()
		a := seedAsset(t)
		v1 := upsert(t, a, 82)
		at := time.Now().Add(time.Hour)
		v2 := schedule(t, a, 70, at)
		cancelled, err := DeactivatePayout(ctx, v2.ID)
		if err != nil || cancelled.Activation == nil || *cancelled.Activation != models.PayoutActivationCancelled {
			t.Fatalf("cancel %+v %v", cancelled, err)
		}
		if res, _ := ActivateDuePayouts(ctx, at.Add(time.Second)); len(res.Activated) != 0 {
			t.Fatalf("cancelled change activated %+v", res)
		}
		if got := assertOneActive(t, a); got.ID.Hex() != v1.ID {
			t.Fatalf("active v%d", got.Version)
		}
		// The slot is free again.
		upsert(t, a, 83)
	})
}

func TestEffectiveFromValidation(t *testing.T) {
	now := time.Now()
	past, soon, far := now.Add(-time.Hour), now.Add(500*time.Millisecond), now.Add(91*24*time.Hour)
	if isScheduled(nil, now) || isScheduled(&past, now) || isScheduled(&soon, now) {
		t.Fatal("past or immediate effectiveFrom treated as scheduled")
	}
	if at := now.Add(time.Minute); !isScheduled(&at, now) {
		t.Fatal("future effectiveFrom not scheduled")
	}
	if err := validateEffectiveFrom(&far, now); !apperr.HasCode(err, apperr.ValidationError) {
		t.Fatalf("91 days ahead: %v", err)
	}
	if err := validateEffectiveFrom(&past, now); err != nil {
		t.Fatalf("past (= now): %v", err)
	}
}
