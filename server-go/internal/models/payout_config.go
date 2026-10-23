package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/mongox"
)

// CollPayoutConfigs: versioned payout configuration (spec section 24).
//
// Rows are never edited in place. Changing a payout supersedes the current row
// (active=false, effectiveTo set) and inserts a new version, so a historical
// trade can always be traced back to the exact terms it was accepted under.
const CollPayoutConfigs = "payout_configs"

type PayoutConfig struct {
	ID            bson.ObjectID `bson:"_id,omitempty"`
	AssetID       bson.ObjectID `bson:"assetId"`
	ExpirySeconds int64         `bson:"expirySeconds"`
	// Percentage of the stake paid as profit on a win, e.g. 82 => 82%.
	PayoutPercent float64 `bson:"payoutPercent"`
	// Optional per-expiry stake bounds, minor units; null means no per-expiry
	// bound. The asset bounds apply either way.
	MinStake *int64 `bson:"minStake"`
	MaxStake *int64 `bson:"maxStake"`
	// The pricing engine's terms for this (asset, expiry) (RiskConfig
	// PricingMode DYNAMIC, docs/audit/pricing/pricing-spec.md): PayoutPercent
	// is the base payout the formula deducts from; the offer is clamped to
	// [MinPayoutPercent, MaxPayoutPercent] (nil = the instrument rule's
	// defaults) and MarginPercent replaces the instrument's margin (nil =
	// the rule's). In FIXED mode PayoutPercent is paid as is.
	MinPayoutPercent *float64       `bson:"minPayoutPercent,omitempty"`
	MaxPayoutPercent *float64       `bson:"maxPayoutPercent,omitempty"`
	MarginPercent    *float64       `bson:"marginPercent,omitempty"`
	Version          int64          `bson:"version"`
	Active           bool           `bson:"active"`
	EffectiveFrom    time.Time      `bson:"effectiveFrom"`
	EffectiveTo      *time.Time     `bson:"effectiveTo"`
	CreatedBy        *bson.ObjectID `bson:"createdBy"`
	Note             *string        `bson:"note"`
	// Where the version is in the replacement protocol (PayoutActivation*).
	// Absent on rows written before the protocol existed.
	Activation *string `bson:"activation,omitempty"`
	// The row this version replaced, and on a replaced row the version that
	// replaced it: what a rollback uses to restore the previous terms.
	Supersedes   *bson.ObjectID `bson:"supersedes,omitempty"`
	SupersededBy *bson.ObjectID `bson:"supersededBy,omitempty"`
	// A scheduled change (activation SCHEDULED): the instant the operator
	// asked it to take effect. EffectiveFrom becomes the instant it actually
	// went live (at most one scheduler tick later), and the version it
	// replaced closes at that same instant.
	ScheduledFrom *time.Time `bson:"scheduledFrom,omitempty"`
	CreatedAt     time.Time  `bson:"createdAt"`
	UpdatedAt     time.Time  `bson:"updatedAt"`
	V             int64      `bson:"__v"`
}

// Replacement protocol states. A new version is inserted PENDING (never
// visible to trading, which reads active rows only), the current row is closed,
// and the new one is switched on as ACTIVATED. A replacement that fails is
// ABANDONED and the row it closed is restored.
const (
	PayoutActivationPending   = "PENDING"
	PayoutActivationActivated = "ACTIVATED"
	PayoutActivationAbandoned = "ABANDONED"
	// A change that takes effect at ScheduledFrom (never visible to trading
	// before), and one an operator withdrew before it did.
	PayoutActivationScheduled = "SCHEDULED"
	PayoutActivationCancelled = "CANCELLED"
)

func init() {
	c := CollPayoutConfigs
	RegisterIndexes(
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "assetId", Value: 1}, {Key: "expirySeconds", Value: 1}, {Key: "version", Value: -1}},
			Options:    options.Index().SetUnique(true),
		},
		// At most one active row per (asset, expiry).
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "assetId", Value: 1}, {Key: "expirySeconds", Value: 1}},
			Options:    options.Index().SetUnique(true).SetPartialFilterExpression(bson.D{{Key: "active", Value: true}}),
		},
		// At most one replacement in flight per (asset, expiry). Named, because
		// the generated name would clash with the active-row index above.
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "assetId", Value: 1}, {Key: "expirySeconds", Value: 1}},
			Options: options.Index().SetName("assetId_1_expirySeconds_1_pending").SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "activation", Value: PayoutActivationPending}}),
		},
		// At most one scheduled change waiting per (asset, expiry).
		mongox.Index{
			Collection: c,
			Keys:       bson.D{{Key: "assetId", Value: 1}, {Key: "expirySeconds", Value: 1}},
			Options: options.Index().SetName("assetId_1_expirySeconds_1_scheduled").SetUnique(true).
				SetPartialFilterExpression(bson.D{{Key: "activation", Value: PayoutActivationScheduled}}),
		},
	)
}

type PayoutView struct {
	ID            string  `json:"id"`
	AssetID       string  `json:"assetId"`
	ExpirySeconds int64   `json:"expirySeconds"`
	PayoutPercent float64 `json:"payoutPercent"`
	MinStake      *int64  `json:"minStake"`
	MaxStake      *int64  `json:"maxStake"`
	// Pricing-engine bounds (null = defaults, see PayoutConfig).
	MinPayoutPercent *float64  `json:"minPayoutPercent"`
	MaxPayoutPercent *float64  `json:"maxPayoutPercent"`
	MarginPercent    *float64  `json:"marginPercent"`
	Version          int64     `json:"version"`
	Active           bool      `json:"active"`
	EffectiveFrom    iso.Time  `json:"effectiveFrom"`
	EffectiveTo      *iso.Time `json:"effectiveTo"`
	Note             *string   `json:"note"`
	// PENDING / ACTIVATED / ABANDONED / SCHEDULED / CANCELLED; omitted on
	// rows that predate it.
	Activation *string `json:"activation,omitempty"`
	// When a scheduled change was asked to take effect.
	ScheduledFrom *iso.Time `json:"scheduledFrom,omitempty"`
}

func ToPayoutView(p *PayoutConfig) PayoutView {
	return PayoutView{
		ID:            p.ID.Hex(),
		AssetID:       p.AssetID.Hex(),
		ExpirySeconds: p.ExpirySeconds,
		PayoutPercent: p.PayoutPercent,
		MinStake:      p.MinStake,
		MaxStake:      p.MaxStake,
		// Pricing-engine bounds.
		MinPayoutPercent: p.MinPayoutPercent,
		MaxPayoutPercent: p.MaxPayoutPercent,
		MarginPercent:    p.MarginPercent,
		Version:          p.Version,
		Active:           p.Active,
		EffectiveFrom:    iso.From(p.EffectiveFrom),
		EffectiveTo:      iso.Ptr(p.EffectiveTo),
		Note:             p.Note,
		Activation:       p.Activation,
		ScheduledFrom:    iso.Ptr(p.ScheduledFrom),
	}
}
