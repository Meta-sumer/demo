package usdt

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/micro"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/beex"
)

// BeeX payout limits, cached in memory and in Redis (shared by instances).
// Fresh for 10 minutes and refreshed lazily by whoever needs them next; a
// stale copy (kept a day) is used when BeeX cannot be reached. Webhooks never
// read limits, so a slow BeeX can never hold one up.

const (
	limitsFreshFor = 10 * time.Minute
	limitsKeepFor  = 24 * time.Hour
	limitsFetchMax = 10 * time.Second
)

// Limits are BeeX's payout limits in micro-USDT.
type Limits struct {
	Currencies         []string `json:"currencies"`
	MinWithdrawal      int64    `json:"minWithdrawal"`
	MaxWithdrawal      int64    `json:"maxWithdrawal"`
	DailyWithdrawalCap int64    `json:"dailyWithdrawalCap"`
	// BeeX holds payouts above this for its own approval.
	ReviewedAbove int64 `json:"reviewedAbove"`
	// Decimal percent string, e.g. "0.5" (= 0.5%).
	FeePercent string   `json:"feePercent"`
	FeeFixed   int64    `json:"feeFixed"`
	FetchedAt  iso.Time `json:"fetchedAt"`
	// True when BeeX could not be reached and an older copy is in use.
	Stale bool `json:"stale"`
}

// EstimateFee is BeeX's published fee: fixed + percent of the amount,
// floored to the micro. BeeX's own figure on the payout is authoritative.
func (l Limits) EstimateFee(amount int64) int64 {
	pct, err := micro.PercentFloor(amount, l.FeePercent)
	if err != nil {
		pct = 0
	}
	return l.FeeFixed + pct
}

// Supports reports whether BeeX lists the currency (an empty list = all).
func (l Limits) Supports(code string) bool {
	if len(l.Currencies) == 0 {
		return true
	}
	for _, c := range l.Currencies {
		if NormNetwork(c) == code {
			return true
		}
	}
	return false
}

var (
	limitsMu  sync.Mutex
	memLimits *Limits
	fetchMu   sync.Mutex
)

// ResetLimitsCache forgets the in-memory copy (tests).
func ResetLimitsCache() {
	limitsMu.Lock()
	memLimits = nil
	limitsMu.Unlock()
}

func cachedLimits() *Limits {
	limitsMu.Lock()
	defer limitsMu.Unlock()
	if memLimits == nil {
		return nil
	}
	c := *memLimits
	return &c
}

func storeLimits(l Limits) {
	limitsMu.Lock()
	memLimits = &l
	limitsMu.Unlock()
}

func fresh(l *Limits) bool { return l != nil && time.Since(l.FetchedAt.Time) < limitsFreshFor }

// GetPayoutLimits returns fresh limits, fetching them if needed. Only one
// fetch runs per process at a time.
func GetPayoutLimits(ctx context.Context) (Limits, error) {
	if l := cachedLimits(); fresh(l) {
		return *l, nil
	}
	fetchMu.Lock()
	defer fetchMu.Unlock()
	if l := cachedLimits(); fresh(l) {
		return *l, nil
	}
	stale := cachedLimits()
	if l, ok := readRedisLimits(ctx); ok {
		storeLimits(l)
		if fresh(&l) {
			return l, nil
		}
		stale = &l
	}
	l, err := fetchLimits(ctx)
	if err == nil {
		storeLimits(l)
		writeRedisLimits(ctx, l)
		return l, nil
	}
	if stale != nil {
		logx.Warn("BeeX payout limits unavailable - using the last known copy",
			"fetchedAt", stale.FetchedAt.UTC().Format(time.RFC3339), "err", err.Error())
		s := *stale
		s.Stale = true
		return s, nil
	}
	return Limits{}, beex.ToAppError(err)
}

// RefreshPayoutLimits fetches now, bypassing the cache (admin view).
func RefreshPayoutLimits(ctx context.Context) (Limits, error) {
	fetchMu.Lock()
	defer fetchMu.Unlock()
	l, err := fetchLimits(ctx)
	if err != nil {
		return Limits{}, beex.ToAppError(err)
	}
	storeLimits(l)
	writeRedisLimits(ctx, l)
	return l, nil
}

func fetchLimits(ctx context.Context) (Limits, error) {
	p, err := beex.Get()
	if err != nil {
		return Limits{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limitsFetchMax)
	defer cancel()
	raw, err := p.GetPayoutLimits(ctx)
	if err != nil {
		return Limits{}, err
	}
	return convertLimits(raw)
}

func convertLimits(raw *beex.PayoutLimits) (Limits, error) {
	l := Limits{Currencies: []string{}, FetchedAt: iso.From(time.Now().UTC().Truncate(time.Millisecond))}
	for _, c := range raw.Currencies {
		l.Currencies = append(l.Currencies, NormNetwork(c))
	}
	var err error
	conv := func(a beex.Amount) int64 {
		v, _, e := a.Micro()
		if e != nil && err == nil {
			err = e
		}
		return v
	}
	l.MinWithdrawal = conv(raw.MinWithdrawal)
	l.MaxWithdrawal = conv(raw.MaxWithdrawal)
	l.DailyWithdrawalCap = conv(raw.DailyWithdrawalCap)
	l.ReviewedAbove = conv(raw.ReviewedAbove)
	l.FeeFixed = conv(raw.Fee.Fixed)
	l.FeePercent = "0"
	if raw.Fee.Percent.Set {
		if _, perr := micro.PercentFloor(1, raw.Fee.Percent.Raw); perr != nil {
			err = perr
		} else {
			l.FeePercent = raw.Fee.Percent.Raw
		}
	}
	if err != nil {
		return Limits{}, &beex.Error{Code: "invalid_response", Message: "payout limits: " + err.Error(), Temporary: true}
	}
	return l, nil
}

func readRedisLimits(ctx context.Context) (Limits, bool) {
	if redisx.R() == nil {
		return Limits{}, false
	}
	raw, err := redisx.R().Get(ctx, config.RedisKeys.UsdtPayoutLimits).Bytes()
	if err != nil {
		return Limits{}, false
	}
	var l Limits
	if json.Unmarshal(raw, &l) != nil || l.FetchedAt.IsZero() {
		return Limits{}, false
	}
	l.Stale = false
	return l, true
}

func writeRedisLimits(ctx context.Context, l Limits) {
	if redisx.R() == nil {
		return
	}
	raw, _ := json.Marshal(l)
	if err := redisx.R().Set(ctx, config.RedisKeys.UsdtPayoutLimits, raw, limitsKeepFor).Err(); err != nil {
		logx.Warn("could not cache BeeX payout limits", "err", err.Error())
	}
}
