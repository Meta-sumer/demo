package exposure

import (
	"context"
	"strconv"
	"strings"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/redisx"
)

// SideLiability is the pricing specification's worst-case loss per side
// (section 7.1) for one asset's LIVE book, minor units:
//
//	L(UP)   = sum of UP profits   - sum of DOWN stakes
//	L(DOWN) = sum of DOWN profits - sum of UP stakes
//
// over committed trades, with every live reservation counted in its worst
// case (its profit on its own side; its stake is not credited against the
// other side) - the rule the atomic reservation applies (SIDE_LIABILITY), so
// a quote and the acceptance that follows agree. Negative: that side winning
// still nets the platform money.
type SideLiability struct {
	Up   int64 `json:"up"`
	Down int64 `json:"down"`
	// The committed counters and reserved profits behind the figures.
	UpStake            int64 `json:"upStake"`
	DownStake          int64 `json:"downStake"`
	UpProfit           int64 `json:"upProfit"`
	DownProfit         int64 `json:"downProfit"`
	ReservedUpProfit   int64 `json:"reservedUpProfit"`
	ReservedDownProfit int64 `json:"reservedDownProfit"`
}

// GetSideLiability reads L(UP) and L(DOWN) for an asset in one round trip.
func GetSideLiability(ctx context.Context, assetID string) (SideLiability, error) {
	pipe := redisx.R().Pipeline()
	counters := pipe.HMGet(ctx, config.RedisKeys.Exposure(assetID), "upStake", "downStake", "upProfit", "downProfit")
	rsv := pipe.HGetAll(ctx, BookNamespace()+":rsv")
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		return SideLiability{}, err
	}
	num := func(v any) int64 {
		s, _ := v.(string)
		n, _ := strconv.ParseInt(s, 10, 64)
		return n
	}
	var out SideLiability
	if vals := counters.Val(); len(vals) == 4 {
		out.UpStake, out.DownStake, out.UpProfit, out.DownProfit = num(vals[0]), num(vals[1]), num(vals[2]), num(vals[3])
	}
	for _, raw := range rsv.Val() {
		f := strings.Split(raw, "|")
		if len(f) < 5 || f[0] != assetID {
			continue
		}
		p, _ := strconv.ParseInt(f[4], 10, 64)
		if f[2] == "UP" {
			out.ReservedUpProfit += p
		} else {
			out.ReservedDownProfit += p
		}
	}
	out.Up = out.UpProfit + out.ReservedUpProfit - out.DownStake
	out.Down = out.DownProfit + out.ReservedDownProfit - out.UpStake
	return out, nil
}
