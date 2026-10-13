package exposure

import (
	"context"

	"binarytrading/internal/platform/util"
)

// AssetSentiment is trader sentiment: how the open book is split between UP
// and DOWN.
//
// Only the RATIO is exposed to customers, never the amounts: the size of the
// book is commercially sensitive and would tell a large trader exactly how
// much room they have before hitting an exposure limit.
//
// When nothing is open there is no sentiment to report: the percentages are
// null rather than an invented 50/50, because a fabricated crowd signal is the
// kind of thing a customer would reasonably feel misled by.
type AssetSentiment struct {
	AssetID     string `json:"assetId"`
	Symbol      string `json:"symbol"`
	UpPercent   *int64 `json:"upPercent"`
	DownPercent *int64 `json:"downPercent"`
	OpenTrades  int64  `json:"openTrades"`
}

type SentimentSplit struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
}

// DeriveSentiment returns nil for an empty book.
func DeriveSentiment(upStake, downStake int64) *SentimentSplit {
	total := upStake + downStake
	if total <= 0 {
		return nil
	}
	up := int64(util.RoundHalfUp(float64(upStake) / float64(total) * 100))
	return &SentimentSplit{Up: up, Down: 100 - up}
}

func GetSentiment(ctx context.Context) ([]AssetSentiment, error) {
	exposures, err := GetAllExposures(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AssetSentiment, 0, len(exposures))
	for _, e := range exposures {
		s := AssetSentiment{AssetID: e.AssetID, Symbol: e.Symbol, OpenTrades: e.OpenTrades}
		if split := DeriveSentiment(e.UpStake, e.DownStake); split != nil {
			up, down := split.Up, split.Down
			s.UpPercent, s.DownPercent = &up, &down
		}
		out = append(out, s)
	}
	return out, nil
}
