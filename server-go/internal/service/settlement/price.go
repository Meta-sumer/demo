package settlement

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"

	"github.com/redis/go-redis/v9"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/redisx"
)

// Expiry price policy (F-11).
//
// The authoritative expiry price is the latest tick whose market timestamp is
// at or before the expiry instant and at most policy.MaxTickAgeMs older than
// it. A tick stamped after expiry is never used, under any rule.
//
//  1. Confirmation: settlement waits until a tick stamped after expiry has been
//     received (so in-order ticks stamped at or before expiry have all
//     arrived), or ConfirmationWindowMs has passed since expiry.
//  2. With no qualifying tick, settlement is deferred - the trade stays OPEN
//     and is retried every RetryDelayMs - until DeferWindowMs after expiry.
//  3. Then NoPriceRule decides, explicitly and on the record:
//     VOID_REFUND           - the trade is voided and the stake returned;
//     STALE_TICK_THEN_VOID  - the latest tick at or before expiry of any age,
//     and if there is none, void.
//
// Ties on the timestamp go to the higher sequence, then the later receipt,
// then the larger price, so the choice is deterministic for a given history.

// Decision kinds.
const (
	DecisionPriced            = "PRICED"
	DecisionAwaitConfirmation = "AWAIT_CONFIRMATION"
	DecisionDeferNoTick       = "DEFER_NO_QUALIFYING_TICK"
	DecisionVoid              = "VOID"
)

// ExpiryCandidates is the tick history the decision runs on.
type ExpiryCandidates struct {
	// Ticks stamped at or before expiry (any order; others are ignored).
	Ticks []models.NormalisedTick
	// A tick stamped after expiry had been received, and the first one's stamp.
	PostExpiryTickSeen      bool
	PostExpiryTickTimestamp *int64
}

// PriceDecision is the pure result of the expiry price policy.
type PriceDecision struct {
	Kind string
	// Set when Kind is DecisionPriced or DecisionVoid.
	Rule string
	Tick *models.NormalisedTick
	// When to try again (Kind AwaitConfirmation / DeferNoTick), epoch ms.
	RetryAt int64
	// How many ticks at or before expiry were examined.
	Candidates int
}

func usablePrice(p float64) bool { return p > 0 && !math.IsNaN(p) && !math.IsInf(p, 0) }

// clockTrusted rejects a tick stamped too far ahead of the server's receipt
// time: its provider clock cannot be used to place it relative to expiry.
func clockTrusted(t *models.NormalisedTick, p models.SettlementPolicy) bool {
	return p.MaxClockSkewMs <= 0 || t.Timestamp-t.ReceivedAt <= p.MaxClockSkewMs
}

func seq(t *models.NormalisedTick) int64 {
	if t.Sequence == nil {
		return math.MinInt64
	}
	return *t.Sequence
}

// newer orders ticks for selection: later market time first, then the tie
// breaks described above.
func newer(a, b *models.NormalisedTick) bool {
	if a.Timestamp != b.Timestamp {
		return a.Timestamp > b.Timestamp
	}
	if seq(a) != seq(b) {
		return seq(a) > seq(b)
	}
	if a.ReceivedAt != b.ReceivedAt {
		return a.ReceivedAt > b.ReceivedAt
	}
	return a.Price > b.Price
}

func latest(ticks []models.NormalisedTick, keep func(*models.NormalisedTick) bool) *models.NormalisedTick {
	var best *models.NormalisedTick
	for i := range ticks {
		t := &ticks[i]
		if !keep(t) {
			continue
		}
		if best == nil || newer(t, best) {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	out := *best
	return &out
}

// DecideExpiryPrice applies the expiry price policy. Pure: the same inputs
// always give the same decision, which is what lets a settlement be replayed
// from its stored evidence.
func DecideExpiryPrice(expiry, now int64, c ExpiryCandidates, p models.SettlementPolicy) PriceDecision {
	p = NormalisePolicy(p)
	atOrBefore := func(t *models.NormalisedTick) bool {
		return t.Timestamp <= expiry && usablePrice(t.Price) && clockTrusted(t, p)
	}
	candidates := 0
	for i := range c.Ticks {
		if c.Ticks[i].Timestamp <= expiry {
			candidates++
		}
	}
	retry := func(limit int64) int64 {
		at := now + p.RetryDelayMs
		if limit > now && limit < at {
			at = limit
		}
		return at
	}

	if !c.PostExpiryTickSeen && now-expiry < p.ConfirmationWindowMs {
		return PriceDecision{Kind: DecisionAwaitConfirmation, RetryAt: retry(expiry + p.ConfirmationWindowMs), Candidates: candidates}
	}

	if t := latest(c.Ticks, func(t *models.NormalisedTick) bool {
		return atOrBefore(t) && expiry-t.Timestamp <= p.MaxTickAgeMs
	}); t != nil {
		return PriceDecision{Kind: DecisionPriced, Rule: models.SettlementRuleTickAtOrBeforeExpiry, Tick: t, Candidates: candidates}
	}

	if now-expiry < p.DeferWindowMs {
		return PriceDecision{Kind: DecisionDeferNoTick, RetryAt: retry(expiry + p.DeferWindowMs), Candidates: candidates}
	}

	if p.NoPriceRule == models.NoPriceRuleStaleTickThenVoid {
		if t := latest(c.Ticks, atOrBefore); t != nil {
			return PriceDecision{Kind: DecisionPriced, Rule: models.SettlementRuleStaleTickBeforeExpiry, Tick: t, Candidates: candidates}
		}
	}
	return PriceDecision{Kind: DecisionVoid, Rule: models.SettlementRuleNoPriceVoidRefund, Candidates: candidates}
}

// maxCandidates bounds the history read; ties beyond it are implausible.
const maxCandidates = 64

// loadExpiryCandidates reads the tick history the policy needs from Redis.
func loadExpiryCandidates(ctx context.Context, assetID string, expiry int64, p models.SettlementPolicy) (ExpiryCandidates, error) {
	key := config.RedisKeys.TickHistory(assetID)
	minScore := "-inf"
	if p.NoPriceRule != models.NoPriceRuleStaleTickThenVoid {
		minScore = strconv.FormatInt(expiry-p.MaxTickAgeMs, 10)
	}
	raw, err := redisx.R().ZRevRangeByScore(ctx, key, &redis.ZRangeBy{
		Max: strconv.FormatInt(expiry, 10), Min: minScore, Offset: 0, Count: maxCandidates,
	}).Result()
	if err != nil {
		return ExpiryCandidates{}, err
	}
	out := ExpiryCandidates{Ticks: make([]models.NormalisedTick, 0, len(raw))}
	for _, member := range raw {
		var t models.NormalisedTick
		if json.Unmarshal([]byte(member), &t) == nil {
			out.Ticks = append(out.Ticks, t)
		}
	}
	after, err := redisx.R().ZRangeByScoreWithScores(ctx, key, &redis.ZRangeBy{
		Min: "(" + strconv.FormatInt(expiry, 10), Max: "+inf", Offset: 0, Count: 1,
	}).Result()
	if err != nil {
		return ExpiryCandidates{}, err
	}
	if len(after) > 0 {
		ts := int64(after[0].Score)
		out.PostExpiryTickSeen, out.PostExpiryTickTimestamp = true, &ts
	}
	sort.SliceStable(out.Ticks, func(i, j int) bool { return newer(&out.Ticks[i], &out.Ticks[j]) })
	return out, nil
}

// ExpiryConfirmed reports whether a trade expiring at expiry may be handed to
// settlement: a tick stamped after expiry has arrived, or the confirmation
// window has passed. The expiry worker and settlement share this rule.
func ExpiryConfirmed(ctx context.Context, assetID string, expiry, now int64, p models.SettlementPolicy) (bool, error) {
	if now-expiry >= p.ConfirmationWindowMs {
		return true, nil
	}
	n, err := redisx.R().ZCount(ctx, config.RedisKeys.TickHistory(assetID), "("+strconv.FormatInt(expiry, 10), "+inf").Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// FeedProvider is the provider recorded with a price: the one that wrote the
// asset's last tick, else this instance's configured provider.
func FeedProvider(ctx context.Context, assetID string) string {
	if name, err := redisx.R().HGet(ctx, config.RedisKeys.LastTick(assetID), "provider").Result(); err == nil && name != "" {
		return name
	}
	if config.E != nil && config.E.MarketDataProvider != "" {
		return config.E.MarketDataProvider
	}
	return "unknown"
}

// EvidenceFromTick fills the tick part of the evidence.
func EvidenceFromTick(e *models.SettlementEvidence, t *models.NormalisedTick, reference int64, provider, source string) {
	if t == nil {
		return
	}
	price, ts, received := t.Price, t.Timestamp, t.ReceivedAt
	age := reference - ts
	e.Price, e.TickTimestamp, e.ReceivedAt, e.TickAgeMs = &price, &ts, &received, &age
	if t.ProviderTimestamp != nil {
		pt := *t.ProviderTimestamp
		e.ProviderTimestamp = &pt
	} else {
		e.ProviderTimestamp = &ts
	}
	if t.Sequence != nil {
		s := *t.Sequence
		e.Sequence = &s
	}
	e.Provider, e.Source = provider, source
}

// ReplayExpiryResult recomputes an expiry outcome from stored evidence: the
// comparison against the entry price, then the draw policy. nil when the
// evidence records no price (void).
func ReplayExpiryResult(direction models.Direction, entryPrice float64, e *models.SettlementEvidence) *models.TradeResult {
	if e == nil || e.Price == nil {
		return nil
	}
	r := ApplyDrawPolicy(DetermineResult(direction, entryPrice, *e.Price), e.DrawPolicy)
	return &r
}
