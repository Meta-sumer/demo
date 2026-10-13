package models

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/platform/mongox"
)

// CollCandles holds persisted OHLC candles. Ticks live in Redis (hot, short
// retention); candles are the durable series the chart loads on page open and
// the volatility estimate reads from. No __v (versionKey: false), no timestamps.
const CollCandles = "candles"

type Candle struct {
	ID      bson.ObjectID `bson:"_id,omitempty"`
	AssetID bson.ObjectID `bson:"assetId"`
	Symbol  string        `bson:"symbol"`
	// Bucket size in seconds, e.g. 1 or 60.
	Interval int64 `bson:"interval"`
	// Bucket start, epoch ms.
	OpenTime int64   `bson:"openTime"`
	Open     float64 `bson:"open"`
	High     float64 `bson:"high"`
	Low      float64 `bson:"low"`
	Close    float64 `bson:"close"`
	Ticks    int64   `bson:"ticks"`
}

func init() {
	RegisterIndexes(
		mongox.Index{
			Collection: CollCandles,
			Keys:       bson.D{{Key: "assetId", Value: 1}, {Key: "interval", Value: 1}, {Key: "openTime", Value: -1}},
			Options:    options.Index().SetUnique(true),
		},
	)
}

// CandleView is the chart point market-data returns ({ time, open, high, low, close }).
type CandleView struct {
	Time  int64   `json:"time"`
	Open  float64 `json:"open"`
	High  float64 `json:"high"`
	Low   float64 `json:"low"`
	Close float64 `json:"close"`
}

func ToCandleView(c *Candle) CandleView {
	return CandleView{Time: c.OpenTime, Open: c.Open, High: c.High, Low: c.Low, Close: c.Close}
}
