package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
)

// CollExposures is the persisted exposure snapshot per asset (spec section 11).
//
// Redis holds the live counters every trade increments; this collection is the
// durable record, refreshed by the risk worker and rebuilt from open trades on
// boot, so a Redis flush never loses accounting truth.
const CollExposures = "exposures"

// Exposure: all amounts are minor units.
type Exposure struct {
	ID                  bson.ObjectID `bson:"_id,omitempty"`
	AssetID             bson.ObjectID `bson:"assetId"`
	Symbol              string        `bson:"symbol"`
	UpStake             int64         `bson:"upStake"`
	DownStake           int64         `bson:"downStake"`
	NetExposure         int64         `bson:"netExposure"`
	GrossExposure       int64         `bson:"grossExposure"`
	UpPotentialPayout   int64         `bson:"upPotentialPayout"`
	DownPotentialPayout int64         `bson:"downPotentialPayout"`
	PotentialLiability  int64         `bson:"potentialLiability"`
	// Liability by expiry bucket and with no netting (see ExposureSnapshot).
	ExpiryLiability    int64     `bson:"expiryLiability"`
	MaxPayoutLiability int64     `bson:"maxPayoutLiability"`
	OpenTrades         int64     `bson:"openTrades"`
	HedgedNotional     int64     `bson:"hedgedNotional"`
	UpdatedAt          time.Time `bson:"updatedAt"`
	CreatedAt          time.Time `bson:"createdAt"`
	V                  int64     `bson:"__v"`
}

func init() {
	RegisterIndexes(
		mongox.Index{Collection: CollExposures, Keys: bson.D{{Key: "assetId", Value: 1}}, Options: options.Index().SetUnique(true)},
	)
}
