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
	// Optional per-expiry stake bounds, minor units; null falls back to the asset bounds.
	MinStake      *int64         `bson:"minStake"`
	MaxStake      *int64         `bson:"maxStake"`
	Version       int64          `bson:"version"`
	Active        bool           `bson:"active"`
	EffectiveFrom time.Time      `bson:"effectiveFrom"`
	EffectiveTo   *time.Time     `bson:"effectiveTo"`
	CreatedBy     *bson.ObjectID `bson:"createdBy"`
	Note          *string        `bson:"note"`
	CreatedAt     time.Time      `bson:"createdAt"`
	UpdatedAt     time.Time      `bson:"updatedAt"`
	V             int64          `bson:"__v"`
}

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
	)
}

type PayoutView struct {
	ID            string    `json:"id"`
	AssetID       string    `json:"assetId"`
	ExpirySeconds int64     `json:"expirySeconds"`
	PayoutPercent float64   `json:"payoutPercent"`
	MinStake      *int64    `json:"minStake"`
	MaxStake      *int64    `json:"maxStake"`
	Version       int64     `json:"version"`
	Active        bool      `json:"active"`
	EffectiveFrom iso.Time  `json:"effectiveFrom"`
	EffectiveTo   *iso.Time `json:"effectiveTo"`
	Note          *string   `json:"note"`
}

func ToPayoutView(p *PayoutConfig) PayoutView {
	return PayoutView{
		ID:            p.ID.Hex(),
		AssetID:       p.AssetID.Hex(),
		ExpirySeconds: p.ExpirySeconds,
		PayoutPercent: p.PayoutPercent,
		MinStake:      p.MinStake,
		MaxStake:      p.MaxStake,
		Version:       p.Version,
		Active:        p.Active,
		EffectiveFrom: iso.From(p.EffectiveFrom),
		EffectiveTo:   iso.Ptr(p.EffectiveTo),
		Note:          p.Note,
	}
}
