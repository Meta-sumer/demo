package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollReconciliationRuns: reconciliation runs and alerts (spec section 42).
//
// Each run compares the wallet, trade, settlement and hedge ledgers. Any
// mismatch is recorded for an operator to investigate - it is never
// auto-corrected. timestamps { createdAt: true, updatedAt: false }.
const CollReconciliationRuns = "reconciliation_runs"

type ReconciliationStatus string

const (
	ReconciliationOK     ReconciliationStatus = "OK"
	ReconciliationAlert  ReconciliationStatus = "ALERT"
	ReconciliationFailed ReconciliationStatus = "FAILED"
)

var ReconciliationStatusValues = []string{
	string(ReconciliationOK), string(ReconciliationAlert), string(ReconciliationFailed),
}

// ReconciliationCheck compares sums of minor units or row counts.
type ReconciliationCheck struct {
	Name       string `bson:"name"`
	Passed     bool   `bson:"passed"`
	Expected   int64  `bson:"expected"`
	Actual     int64  `bson:"actual"`
	Difference int64  `bson:"difference"`
	// Mixed, default {}.
	Details map[string]any `bson:"details"`
}

type ReconciliationRun struct {
	ID             bson.ObjectID         `bson:"_id,omitempty"`
	Reference      string                `bson:"reference"`
	WindowFrom     time.Time             `bson:"windowFrom"`
	WindowTo       time.Time             `bson:"windowTo"`
	Status         ReconciliationStatus  `bson:"status"`
	Checks         []ReconciliationCheck `bson:"checks"`
	Alerts         []string              `bson:"alerts"`
	DurationMs     int64                 `bson:"durationMs"`
	ResolvedBy     *bson.ObjectID        `bson:"resolvedBy"`
	ResolvedAt     *time.Time            `bson:"resolvedAt"`
	ResolutionNote *string               `bson:"resolutionNote"`
	CreatedAt      time.Time             `bson:"createdAt"`
	V              int64                 `bson:"__v"`
}

func init() {
	c := CollReconciliationRuns
	RegisterIndexes(
		mongox.Index{Collection: c, Keys: bson.D{{Key: "status", Value: 1}}},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "reference", Value: 1}}, Options: options.Index().SetUnique(true)},
		mongox.Index{Collection: c, Keys: bson.D{{Key: "createdAt", Value: -1}}},
	)
}

type ReconciliationCheckLean struct {
	Name       string `json:"name"`
	Passed     bool   `json:"passed"`
	Expected   int64  `json:"expected"`
	Actual     int64  `json:"actual"`
	Difference int64  `json:"difference"`
	Details    any    `json:"details,omitempty"`
}

// ReconciliationRunLean is the lean() document listReconciliationRuns returns.
type ReconciliationRunLean struct {
	ID             string                    `json:"_id"`
	Reference      string                    `json:"reference"`
	WindowFrom     iso.Time                  `json:"windowFrom"`
	WindowTo       iso.Time                  `json:"windowTo"`
	Status         ReconciliationStatus      `json:"status"`
	Checks         []ReconciliationCheckLean `json:"checks"`
	Alerts         []string                  `json:"alerts"`
	DurationMs     int64                     `json:"durationMs"`
	ResolvedBy     *string                   `json:"resolvedBy"`
	ResolvedAt     *iso.Time                 `json:"resolvedAt"`
	ResolutionNote *string                   `json:"resolutionNote"`
	CreatedAt      iso.Time                  `json:"createdAt"`
	V              int64                     `json:"__v"`
}

func ToReconciliationRunLean(r *ReconciliationRun) ReconciliationRunLean {
	checks := make([]ReconciliationCheckLean, len(r.Checks))
	for i, c := range r.Checks {
		checks[i] = ReconciliationCheckLean{
			Name: c.Name, Passed: c.Passed, Expected: c.Expected, Actual: c.Actual, Difference: c.Difference,
		}
		if c.Details != nil {
			checks[i].Details = mongox.JSONValue(c.Details)
		}
	}
	alerts := r.Alerts
	if alerts == nil {
		alerts = []string{}
	}
	return ReconciliationRunLean{
		ID:             r.ID.Hex(),
		Reference:      r.Reference,
		WindowFrom:     iso.From(r.WindowFrom),
		WindowTo:       iso.From(r.WindowTo),
		Status:         r.Status,
		Checks:         checks,
		Alerts:         alerts,
		DurationMs:     r.DurationMs,
		ResolvedBy:     hexPtr(r.ResolvedBy),
		ResolvedAt:     iso.Ptr(r.ResolvedAt),
		ResolutionNote: r.ResolutionNote,
		CreatedAt:      iso.From(r.CreatedAt),
		V:              r.V,
	}
}

// hexPtr maps a nullable ObjectID to a nullable hex string.
func hexPtr(id *bson.ObjectID) *string {
	if id == nil {
		return nil
	}
	s := id.Hex()
	return &s
}
