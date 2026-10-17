package exposure

// The risk book: atomic reservation of exposure before a LIVE trade becomes
// irreversible, and exactly-once accounting of every trade id afterwards.
//
// Source of truth. MongoDB is the record: the open LIVE trades in
// binary_trades (status OPEN or SETTLING, accountType not DEMO/FUNDING) ARE
// the house book. Everything in Redis is a cache of it plus the in-flight
// reservations, which exist only in Redis because the trades they guard have
// not been written yet. The cache can always be rebuilt from MongoDB
// (RebuildBook) and is checked against it continuously (Reconcile).
//
// Lifecycle of one LIVE trade id in the book:
//
//	(none) --Reserve--> PENDING --Commit--> OPEN --Close--> CLOSED (tombstone)
//	          |            |  \--Release (refused / failed before insert)--> (none)
//	          |            \--deadline passed: the sweeper looks the trade up
//	          |               in MongoDB: live -> Commit, absent/finished -> Release
//	          \--refused / UNCERTAIN: nothing is held
//
// Reserve is refused (fail closed) unless the book is READY: missing state
// (Redis flushed or never built), DIRTY (an integrity check failed) or
// REBUILDING all mean exposure is not known exactly, and a LIVE trade must
// not be accepted against a guess.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/metrics"
)

// LiabilityBucketMs is the width of an expiry bucket. Trades whose expiry
// falls in the same window are taken to settle on the same price, so only
// they offset each other in the liability figure.
const LiabilityBucketMs = 1000

// Book states.
const (
	BookReady      = "READY"
	BookRebuilding = "REBUILDING"
	BookDirty      = "DIRTY"
	BookMissing    = "MISSING"
)

// Tunables, variables so tests can shorten them.
var (
	// How long a reservation holds headroom without being renewed. The
	// acceptance path renews it right before the trade insert.
	ReservationTTL = 30 * time.Second
	// A lapsed reservation with no trade in MongoDB is only released this much
	// after its deadline, so an insert that is merely slow is not raced.
	ReservationReleaseMargin = 30 * time.Second
	// A trade/record disagreement younger than this may still be in flight
	// (insert done, commit not yet; settled, release not yet) and is left alone.
	ReconcileGrace = 10 * time.Second
)

// BookNamespace is the Redis prefix of this database's book. It carries the
// database name so two environments sharing one Redis never mix their books.
func BookNamespace() string { return "exp:book:" + mongox.DB().Name() }

// BucketOf maps an expiry timestamp to the start of its bucket.
func BucketOf(expiryTimestamp int64) string {
	if expiryTimestamp <= 0 {
		return "0"
	}
	return strconv.FormatInt(expiryTimestamp-expiryTimestamp%LiabilityBucketMs, 10)
}

// cleanSymbol keeps the pipe-separated encodings parseable.
func cleanSymbol(s string) string { return strings.ReplaceAll(s, "|", "/") }

func nowArg() string { return strconv.FormatInt(util.NowMs(), 10) }

func dirArg(d models.Direction) string {
	if d == models.DirectionUp {
		return "UP"
	}
	return "DOWN"
}

// ErrBookUnavailable means the book could not be made READY in time.
var ErrBookUnavailable = errors.New("exposure book is not ready")

// ── reservation ─────────────────────────────────────────────────────────

// Reservation rules (the violated ones come back in ReserveOutcome.Rules).
const (
	RuleUserExposure      = "USER_EXPOSURE"
	RuleAssetGross        = "ASSET_GROSS"
	RuleAssetNet          = "ASSET_NET"
	RulePlatformGross     = "PLATFORM_GROSS"
	RuleConcentration     = "CONCENTRATION"
	RuleExpiryLiability   = "EXPIRY_LIABILITY"
	RuleAssetLiability    = "ASSET_LIABILITY"
	RulePlatformLiability = "PLATFORM_LIABILITY"
)

// ReserveLimits are the ceilings in force for one trade. Liability limits of
// 0 are off; the others always apply (as in risk.CheckTradeLimits).
type ReserveLimits struct {
	UserExposure         int64
	AssetGross           int64
	AssetNet             int64
	PlatformGross        int64
	ConcentrationPercent float64
	ExpiryLiability      int64
	AssetLiability       int64
	PlatformLiability    int64
}

type ReserveInput struct {
	TradeID         string
	AssetID         string
	Symbol          string
	UserID          string
	Direction       models.Direction
	Stake           int64
	Profit          int64
	ExpiryTimestamp int64
	Limits          ReserveLimits
}

// Reserve outcomes.
const (
	ReserveOK        = "OK"
	ReserveRefused   = "REFUSED"
	ReserveUncertain = "UNCERTAIN"
	ReserveConflict  = "CONFLICT"
)

type ReserveOutcome struct {
	Status string
	// Violated rules, in check order (REFUSED).
	Rules []string
	// Why the book is not trusted (UNCERTAIN) or what the id already is
	// (CONFLICT).
	Detail string
	// The same trade id was already reserved: nothing new was held.
	Duplicate bool
}

// Reservation is headroom held for one trade id.
type Reservation struct {
	in     ReserveInput
	bucket string
}

func (r *Reservation) TradeID() string { return r.in.TradeID }

// Reserve checks every exposure limit against committed counters plus live
// reservations and, if all pass, holds the headroom under the trade id - in
// one atomic step. Idempotent per trade id. The Reservation is non-nil only
// for ReserveOK.
func Reserve(ctx context.Context, in ReserveInput) (*Reservation, ReserveOutcome, error) {
	in.Symbol = cleanSymbol(in.Symbol)
	bucket := BucketOf(in.ExpiryTimestamp)
	deadline := util.NowMs() + ReservationTTL.Milliseconds()
	l := in.Limits
	res, err := reserveScript.Run(ctx, redisx.R(), nil,
		BookNamespace(), in.TradeID, in.AssetID, in.UserID, dirArg(in.Direction), in.Stake, in.Profit,
		bucket, in.Symbol, deadline, nowArg(),
		l.UserExposure, l.AssetGross, l.AssetNet, l.PlatformGross, l.ConcentrationPercent,
		l.ExpiryLiability, l.AssetLiability, l.PlatformLiability,
	).Slice()
	if err != nil {
		return nil, ReserveOutcome{}, err
	}
	parts := strs(res)
	if len(parts) == 0 {
		return nil, ReserveOutcome{}, fmt.Errorf("exposure: empty reserve reply")
	}
	out := ReserveOutcome{Status: parts[0]}
	switch parts[0] {
	case ReserveOK:
		out.Duplicate = len(parts) > 1 && parts[1] == "dup"
		return &Reservation{in: in, bucket: bucket}, out, nil
	case ReserveRefused:
		out.Rules = append([]string{}, parts[1:]...)
	case ReserveUncertain, ReserveConflict:
		out.Detail = strings.Join(parts[1:], ":")
	default:
		return nil, out, fmt.Errorf("exposure: unexpected reserve reply %v", parts)
	}
	return nil, out, nil
}

// Renew extends the reservation's deadline. false means it no longer exists
// (it lapsed and was swept, or Redis lost it): the trade must not proceed.
func (r *Reservation) Renew(ctx context.Context) (bool, error) {
	deadline := util.NowMs() + ReservationTTL.Milliseconds()
	n, err := renewScript.Run(ctx, redisx.R(), nil, BookNamespace(), r.in.TradeID, deadline).Int()
	return n == 1, err
}

// Release drops the reservation (the trade was refused or failed before it
// was written). Idempotent.
func (r *Reservation) Release(ctx context.Context) error {
	return ReleaseReservation(ctx, r.in.TradeID)
}

// Commit turns the reservation into committed exposure once the trade is
// written. Idempotent. Works even when the reservation is gone (a "late"
// commit): the trade exists, so its exposure must be counted regardless.
func (r *Reservation) Commit(ctx context.Context) (CommitResult, error) {
	return commit(ctx, OpenTrade{
		TradeID: r.in.TradeID, AssetID: r.in.AssetID, Symbol: r.in.Symbol, UserID: r.in.UserID,
		Direction: r.in.Direction, Stake: r.in.Stake, Profit: r.in.Profit, ExpiryTimestamp: r.in.ExpiryTimestamp,
	}, false)
}

// ReleaseReservation drops a reservation by trade id. Idempotent.
func ReleaseReservation(ctx context.Context, tradeID string) error {
	return releaseScript.Run(ctx, redisx.R(), nil, BookNamespace(), tradeID).Err()
}

// ── committed trades ────────────────────────────────────────────────────

// OpenTrade is one committed LIVE trade's contribution to the book.
type OpenTrade struct {
	TradeID         string
	AssetID         string
	Symbol          string
	UserID          string
	Direction       models.Direction
	Stake           int64
	Profit          int64
	ExpiryTimestamp int64
}

// Commit results.
const (
	CommitOpened = "opened" // counted now
	CommitDup    = "dup"    // already counted
	CommitClosed = "closed" // already released (settled before the commit landed)
)

type CommitResult struct {
	Result string
	// No reservation was held when the commit landed.
	Late bool
}

// CommitTrade counts a written LIVE trade, exactly once per trade id.
func CommitTrade(ctx context.Context, t OpenTrade) (CommitResult, error) {
	return commit(ctx, t, false)
}

// forceOpen re-counts a trade MongoDB says is open even if it was released
// before (a release matched to the wrong one of two identical trades).
func forceOpen(ctx context.Context, t OpenTrade) (CommitResult, error) {
	return commit(ctx, t, true)
}

func commit(ctx context.Context, t OpenTrade, force bool) (CommitResult, error) {
	f := "0"
	if force {
		f = "1"
	}
	res, err := commitScript.Run(ctx, redisx.R(), nil,
		BookNamespace(), t.TradeID, t.AssetID, t.UserID, dirArg(t.Direction), t.Stake, t.Profit,
		BucketOf(t.ExpiryTimestamp), cleanSymbol(t.Symbol), nowArg(), f,
	).Slice()
	if err != nil {
		return CommitResult{}, err
	}
	parts := strs(res)
	if len(parts) < 2 {
		return CommitResult{}, fmt.Errorf("exposure: unexpected commit reply %v", parts)
	}
	out := CommitResult{Result: parts[0], Late: parts[1] == "late"}
	if out.Late && out.Result == CommitOpened && !force {
		// The trade was written without a live reservation (it lapsed, or Redis
		// lost it). Counted now, but it was not covered by the atomic check.
		metrics.IncrementCounter("exposure.late_commit", 1, 0)
		logx.Warn("exposure committed without a live reservation", "tradeId", t.TradeID)
	}
	return out, nil
}

// Close results.
const (
	CloseClosed  = "closed"
	CloseMissing = "missing"
	CloseNoMatch = "nomatch"
)

// CloseTrade releases a trade's committed exposure, exactly once per trade
// id. A close that arrives before the commit leaves a tombstone, so the late
// commit is ignored.
func CloseTrade(ctx context.Context, tradeID, userID string) (string, error) {
	return closeScript.Run(ctx, redisx.R(), nil, BookNamespace(), tradeID, userID, nowArg()).Text()
}

// closeByMatch serves callers that release without a trade id.
func closeByMatch(ctx context.Context, d ExposureDelta) (string, error) {
	res, err := closeMatchScript.Run(ctx, redisx.R(), nil,
		BookNamespace(), d.AssetID, d.UserID, dirArg(d.Direction), d.Stake, d.Profit, nowArg()).Slice()
	if err != nil {
		return "", err
	}
	parts := strs(res)
	if len(parts) == 0 {
		return "", fmt.Errorf("exposure: empty close reply")
	}
	if parts[0] == CloseNoMatch {
		logx.Debug("exposure release matched no open trade", "assetId", d.AssetID, "userId", d.UserID)
	}
	return parts[0], nil
}

func anonTradeID() string { return "anon-" + uuid.NewString() }

// ── state ───────────────────────────────────────────────────────────────

// BookStatus is the book's health, for admin views and safe-mode decisions.
type BookStatus struct {
	Status              string `json:"status"`
	Generation          string `json:"generation"`
	Reason              string `json:"reason,omitempty"`
	BuiltAt             *int64 `json:"builtAt"`
	LastReconciledAt    *int64 `json:"lastReconciledAt"`
	PendingReservations int64  `json:"pendingReservations"`
}

func optMs(raw map[string]string, field string) *int64 {
	if _, ok := raw[field]; !ok {
		return nil
	}
	v := num(raw, field)
	return &v
}

// GetBookStatus reads the book state.
func GetBookStatus(ctx context.Context) (BookStatus, error) {
	ns := BookNamespace()
	pipe := redisx.R().Pipeline()
	stCmd := pipe.HGetAll(ctx, ns+":state")
	nCmd := pipe.ZCard(ctx, ns+":rsvdl")
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		return BookStatus{}, err
	}
	raw := stCmd.Val()
	status := raw["status"]
	if status == "" {
		status = BookMissing
	}
	return BookStatus{
		Status:              status,
		Generation:          raw["gen"],
		Reason:              raw["reason"],
		BuiltAt:             optMs(raw, "builtAt"),
		LastReconciledAt:    optMs(raw, "lastReconciledAt"),
		PendingReservations: nCmd.Val(),
	}, nil
}

// MarkBookDirty puts a READY book into safe mode until it is rebuilt.
func MarkBookDirty(ctx context.Context, reason string) error {
	return markDirtyScript.Run(ctx, redisx.R(), nil, BookNamespace(), reason, nowArg()).Err()
}

// strs flattens a script reply of strings and integers.
func strs(v []any) []string {
	out := make([]string, 0, len(v))
	for _, x := range v {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case int64:
			out = append(out, strconv.FormatInt(t, 10))
		default:
			out = append(out, fmt.Sprint(t))
		}
	}
	return out
}
