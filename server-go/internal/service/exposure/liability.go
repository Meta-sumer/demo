package exposure

// Liability model.
//
// Stake exposure says how much is at risk, not what the platform can lose.
// What it can lose depends on each trade's frozen profit (its payout rate) and
// on WHEN the trade settles: an UP trade and a DOWN trade only offset each
// other when they settle against the same price. So the book is split into
// expiry buckets (LiabilityBucketMs wide) and, per bucket b:
//
//	upWinNet_b   = upProfit_b - downStake_b    platform pays the UP profits,
//	                                           keeps the DOWN stakes
//	downWinNet_b = downProfit_b - upStake_b
//	L_b          = max(0, upWinNet_b, downWinNet_b)
//
// The asset's expiryLiability is the sum of L_b over its buckets (buckets
// settle on different prices, so each can go the bad way); the platform's is
// the sum over assets. maxPayoutLiability (upProfit + downProfit) is the
// no-netting bound - every trade wins - for stricter readings.
//
// Residual: within one bucket, trades entered at different prices can both
// win, so L_b nets more than strictly safe; maxPayoutLiability is the bound
// that does not.

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/redisx"
)

// BucketCounters are one expiry bucket's committed totals.
type BucketCounters struct {
	UpStake    int64
	DownStake  int64
	UpProfit   int64
	DownProfit int64
	OpenTrades int64
}

// BucketLiability is max(0, upProfit - downStake, downProfit - upStake).
func BucketLiability(b BucketCounters) int64 {
	return max(0, b.UpProfit-b.DownStake, b.DownProfit-b.UpStake)
}

// With returns the bucket with one more trade on side d.
func (b BucketCounters) With(d models.Direction, stake, profit int64) BucketCounters {
	if d == models.DirectionUp {
		b.UpStake += stake
		b.UpProfit += profit
	} else {
		b.DownStake += stake
		b.DownProfit += profit
	}
	b.OpenTrades++
	return b
}

// LiabilityTrade is the minimum a trade contributes to the liability model.
type LiabilityTrade struct {
	Direction       models.Direction
	Stake           int64
	Profit          int64
	ExpiryTimestamp int64
}

// ComputeLiability is the pure model over a list of trades (one asset):
// returns the per-bucket counters and the sum of bucket liabilities.
func ComputeLiability(trades []LiabilityTrade) (map[string]BucketCounters, int64) {
	buckets := map[string]BucketCounters{}
	for _, t := range trades {
		k := BucketOf(t.ExpiryTimestamp)
		buckets[k] = buckets[k].With(t.Direction, t.Stake, t.Profit)
	}
	var total int64
	for _, b := range buckets {
		total += BucketLiability(b)
	}
	return buckets, total
}

func parseBucket(raw string) BucketCounters {
	f := strings.Split(raw, "|")
	at := func(i int) int64 {
		if i >= len(f) {
			return 0
		}
		n, _ := strconv.ParseInt(f[i], 10, 64)
		return n
	}
	return BucketCounters{UpStake: at(0), DownStake: at(1), UpProfit: at(2), DownProfit: at(3), OpenTrades: at(4)}
}

// GetBucketCounters reads one bucket's committed counters.
func GetBucketCounters(ctx context.Context, assetID string, expiryTimestamp int64) (BucketCounters, error) {
	raw, err := redisx.R().HGet(ctx, BookNamespace()+":bkt:"+assetID, BucketOf(expiryTimestamp)).Result()
	if redisx.IsNil(err) {
		return BucketCounters{}, nil
	}
	if err != nil {
		return BucketCounters{}, err
	}
	return parseBucket(raw), nil
}

// LiabilityBucket is one expiry bucket of an asset's book, for the admin
// exposure view.
type LiabilityBucket struct {
	// Bucket window, epoch ms: expiries in [expiryFrom, expiryTo).
	ExpiryFrom int64 `json:"expiryFrom"`
	ExpiryTo   int64 `json:"expiryTo"`
	UpStake    int64 `json:"upStake"`
	DownStake  int64 `json:"downStake"`
	UpProfit   int64 `json:"upProfit"`
	DownProfit int64 `json:"downProfit"`
	UpWinNet   int64 `json:"upWinNet"`
	DownWinNet int64 `json:"downWinNet"`
	Liability  int64 `json:"liability"`
	OpenTrades int64 `json:"openTrades"`
}

// ToLiabilityBucket derives the view of one bucket.
func ToLiabilityBucket(start int64, b BucketCounters) LiabilityBucket {
	return LiabilityBucket{
		ExpiryFrom: start, ExpiryTo: start + LiabilityBucketMs,
		UpStake: b.UpStake, DownStake: b.DownStake, UpProfit: b.UpProfit, DownProfit: b.DownProfit,
		UpWinNet: b.UpProfit - b.DownStake, DownWinNet: b.DownProfit - b.UpStake,
		Liability: BucketLiability(b), OpenTrades: b.OpenTrades,
	}
}

// GetLiabilityBuckets lists an asset's non-empty expiry buckets, soonest first.
func GetLiabilityBuckets(ctx context.Context, assetID string) ([]LiabilityBucket, error) {
	raw, err := redisx.R().HGetAll(ctx, BookNamespace()+":bkt:"+assetID).Result()
	if err != nil {
		return nil, err
	}
	out := make([]LiabilityBucket, 0, len(raw))
	for k, v := range raw {
		start, err := strconv.ParseInt(k, 10, 64)
		if err != nil {
			continue
		}
		out = append(out, ToLiabilityBucket(start, parseBucket(v)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiryFrom < out[j].ExpiryFrom })
	return out, nil
}
