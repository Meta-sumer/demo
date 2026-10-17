// Package settlement is the settlement engine (spec section 18) and the one
// state machine every trade outcome goes through - expiry settlement and the
// customer's early close alike.
//
// State machine (durable, on the trade document):
//
//	OPEN ──claim+intent──► SETTLING{intent} ──execute──► WON / LOST / DRAW / CLOSED_EARLY
//
// Leaving OPEN is ONE conditional update that also records the immutable
// SettlementIntent: the result, the single wallet operation, every amount and
// the evidence it was decided on. Executing the intent is idempotent, so the
// outcome is fixed the moment the trade leaves OPEN and a crash at any later
// point is finished by whoever looks at the trade next, with the same result.
//
// Invariants for concurrent operation (any number of instances, any number of
// queue copies of a trade, leader overlap in the expiry worker):
//
//  1. Exactly one claim. Both claims are conditional on status OPEN, and they
//     are also disjoint in time by the database clock: an expiry claim needs
//     expiryTimestamp <= $$NOW, an early-close claim needs expiryTimestamp >
//     $$NOW + floor. Nothing settles at expiry before expiry, and nothing
//     closes early at or after it.
//  2. The intent never changes after the claim, except to adopt the outcome
//     already booked on the wallet ledger by an earlier version (ledger wins),
//     conditional on the intent id it replaces.
//  3. One money movement: every outcome uses the wallet key
//     trade:settle:<id> (partial unique index), so a replay moves nothing.
//  4. One ledger row: settlements.tradeId is unique; the row is derived from
//     the intent, so a replay writes the same row or finds it.
//  5. One finalisation: SETTLING -> final is conditional on the intent id.
//     Only the executor whose update matched releases exposure, emits
//     trade.settled and notifies - so those happen once per trade too.
//  6. Queues are hints. Losing, duplicating or reordering queue entries can
//     delay an outcome but never change or repeat it; the database sweep
//     rebuilds them from trades (expiry.SweepMissedExpiries,
//     binary.RebuildExpiryQueue).
//  7. A SETTLING trade without an intent can only come from an earlier version.
//     Once stale it is finished from the wallet ledger / settlement row if
//     money already moved, otherwise returned to OPEN - never settled before
//     expiry.
package settlement

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/riskconfig"
)

// StaleSettlingMs: a SETTLING trade untouched this long is assumed to be
// abandoned by the process that claimed it.
const StaleSettlingMs = 30_000

// DefaultSettledBy is settleTrade's default settledBy.
const DefaultSettledBy = "settlement.worker"

// Outcome statuses.
const (
	StatusSettled          = "SETTLED"
	StatusAlreadySettled   = "ALREADY_SETTLED"
	StatusClaimedElsewhere = "CLAIMED_ELSEWHERE"
	// Deferred: no tick qualifies yet; retried at RetryAt (trade stays OPEN).
	StatusSkippedNoPrice = "SKIPPED_NO_PRICE"
	// Deferred: waiting for a tick stamped after expiry; retried at RetryAt.
	StatusAwaitingTick = "AWAITING_POST_EXPIRY_TICK"
	// Not expired yet: put back on the expiry queue for its expiry.
	StatusNotDue = "NOT_DUE"
	// Cancelled, unknown, or otherwise not something settlement acts on.
	StatusNotSettleable = "NOT_SETTLEABLE"
)

type Outcome struct {
	TradeID        string              `json:"tradeId"`
	Status         string              `json:"status"`
	Result         *models.TradeResult `json:"result,omitempty"`
	ExpiryPrice    *float64            `json:"expiryPrice,omitempty"`
	PayoutAmount   *int64              `json:"payoutAmount,omitempty"`
	PlatformResult *int64              `json:"platformResult,omitempty"`
	// The settlement rule that produced the result (models.SettlementRule*).
	Rule string `json:"rule,omitempty"`
	// For deferred outcomes: when the trade is due again, epoch ms.
	RetryAt *int64 `json:"retryAt,omitempty"`
}

// DetermineResult is the pure comparison rule (spec section 17).
func DetermineResult(direction models.Direction, entryPrice, expiryPrice float64) models.TradeResult {
	if expiryPrice == entryPrice {
		return models.TradeResultDraw
	}
	wentUp := expiryPrice > entryPrice
	if direction == models.DirectionUp {
		if wentUp {
			return models.TradeResultWin
		}
		return models.TradeResultLoss
	}
	if wentUp {
		return models.TradeResultLoss
	}
	return models.TradeResultWin
}

// ApplyDrawPolicy: REFUND returns the stake, LOSS and WIN treat an exact match
// as the corresponding outcome. Whichever is configured is disclosed to users:
// GET /assets sends it as drawPolicy, and the client words ties by it.
func ApplyDrawPolicy(result models.TradeResult, policy models.DrawPolicy) models.TradeResult {
	if result != models.TradeResultDraw {
		return result
	}
	switch policy {
	case models.DrawPolicyLoss:
		return models.TradeResultLoss
	case models.DrawPolicyWin:
		return models.TradeResultWin
	}
	return models.TradeResultDraw
}

// Reschedule puts a trade back on the expiry queue so the expiry worker hands
// it to settlement again at `at` (epoch ms). Idempotent.
func Reschedule(ctx context.Context, tradeID string, at int64) error {
	return redisx.R().ZAdd(ctx, config.RedisKeys.ExpiryQueue, redisZ(at, tradeID)).Err()
}

func loadTrade(ctx context.Context, oid bson.ObjectID) (*models.BinaryTrade, error) {
	var t models.BinaryTrade
	if err := mongox.C(models.CollBinaryTrades).FindOne(ctx, bson.M{"_id": oid}).Decode(&t); err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &t, nil
}

// SettleTrade drives one trade as far through the state machine as it can
// go now; settledBy "" means DefaultSettledBy. Safe to call any number of
// times, concurrently, on any instance.
func SettleTrade(ctx context.Context, tradeID, settledBy string) (Outcome, error) {
	if settledBy == "" {
		settledBy = DefaultSettledBy
	}
	ctx = context.WithoutCancel(ctx)
	startedAt := time.Now()

	oid, err := bson.ObjectIDFromHex(tradeID)
	if err != nil {
		return Outcome{}, err
	}
	trade, err := loadTrade(ctx, oid)
	if err != nil {
		return Outcome{}, err
	}
	if trade == nil {
		_ = redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err()
		return Outcome{TradeID: tradeID, Status: StatusNotSettleable}, nil
	}

	var outcome Outcome
	switch trade.Status {
	case models.TradeStatusSettling:
		if trade.SettlementIntent != nil {
			outcome, err = Execute(ctx, trade)
		} else {
			outcome, err = recoverWithoutIntent(ctx, trade, settledBy)
		}
	case models.TradeStatusOpen:
		outcome, err = settleOpen(ctx, trade, settledBy)
	case models.TradeStatusCancelled:
		_ = redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err()
		outcome = Outcome{TradeID: tradeID, Status: StatusNotSettleable}
	default:
		_ = redisx.R().ZRem(ctx, config.RedisKeys.ExpiryQueue, tradeID).Err()
		outcome = Outcome{TradeID: tradeID, Status: StatusAlreadySettled, Result: trade.Result}
	}
	if err != nil {
		return Outcome{}, err
	}
	if outcome.Status == StatusSettled {
		metrics.Since(config.LatSettlement, startedAt)
	}
	return outcome, nil
}

// settleOpen settles an OPEN trade at expiry: decide the price, then claim the
// trade together with the outcome.
func settleOpen(ctx context.Context, trade *models.BinaryTrade, settledBy string) (Outcome, error) {
	tradeID := trade.ID.Hex()

	// A settlement row on an OPEN trade predates the state machine (the row
	// was written but the trade never updated): finish it from the books.
	if intent, err := legacyIntent(ctx, trade, settledBy); err != nil {
		return Outcome{}, err
	} else if intent != nil {
		claimed, err := attachIntent(ctx, trade, intent, models.TradeStatusOpen, settledBy)
		if err != nil {
			return Outcome{}, err
		}
		if claimed == nil {
			return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
		}
		return Execute(ctx, claimed)
	}

	now := util.NowMs()
	if now < trade.ExpiryTimestamp {
		// Never settle early - whoever queued it (a duplicate entry, the
		// sweeper, an operator) gets it back on the queue for its expiry.
		if err := Reschedule(ctx, tradeID, trade.ExpiryTimestamp); err != nil {
			return Outcome{}, err
		}
		at := trade.ExpiryTimestamp
		return Outcome{TradeID: tradeID, Status: StatusNotDue, RetryAt: &at}, nil
	}

	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return Outcome{}, err
	}
	policy, err := PolicyFor(ctx, cfg)
	if err != nil {
		return Outcome{}, err
	}
	assetHex := trade.AssetID.Hex()
	candidates, err := loadExpiryCandidates(ctx, assetHex, trade.ExpiryTimestamp, policy)
	if err != nil {
		return Outcome{}, err
	}
	decision := DecideExpiryPrice(trade.ExpiryTimestamp, now, candidates, policy)

	switch decision.Kind {
	case DecisionAwaitConfirmation, DecisionDeferNoTick:
		if err := Reschedule(ctx, tradeID, decision.RetryAt); err != nil {
			return Outcome{}, err
		}
		status := StatusAwaitingTick
		if decision.Kind == DecisionDeferNoTick {
			status = StatusSkippedNoPrice
			logx.Warn("no qualifying expiry price yet - settlement deferred",
				"tradeId", tradeID, "symbol", trade.Symbol, "expiry", trade.ExpiryTimestamp,
				"maxTickAgeMs", policy.MaxTickAgeMs, "deferUntil", trade.ExpiryTimestamp+policy.DeferWindowMs)
		}
		at := decision.RetryAt
		return Outcome{TradeID: tradeID, Status: status, RetryAt: &at}, nil
	}

	evidence := models.SettlementEvidence{
		Rule:                    decision.Rule,
		Kind:                    models.SettlementKindExpiry,
		DrawPolicy:              cfg.DrawPolicy,
		PostExpiryTickSeen:      candidates.PostExpiryTickSeen,
		PostExpiryTickTimestamp: candidates.PostExpiryTickTimestamp,
		CandidatesConsidered:    int64(decision.Candidates),
		ExpiryTimestamp:         trade.ExpiryTimestamp,
		DecidedAt:               now,
		RiskConfigVersion:       cfg.Version,
		Policy:                  policy,
	}
	var intent models.SettlementIntent
	if decision.Kind == DecisionPriced {
		EvidenceFromTick(&evidence, decision.Tick, trade.ExpiryTimestamp, FeedProvider(ctx, assetHex), models.SettlementEvidenceSourceHist)
		raw := DetermineResult(trade.Direction, trade.EntryPrice, decision.Tick.Price)
		evidence.RawResult = &raw
		price := decision.Tick.Price
		intent = expiryIntent(trade, ApplyDrawPolicy(raw, cfg.DrawPolicy), &price, evidence, settledBy)
	} else {
		// Void: no price is compared, so the draw policy does not apply.
		evidence.DrawPolicy = ""
		evidence.Note = "no tick at or before expiry within the policy; voided and stake returned"
		intent = expiryIntent(trade, models.TradeResultDraw, nil, evidence, settledBy)
		logx.Error("no usable expiry price - trade voided under the no-price rule",
			"tradeId", tradeID, "symbol", trade.Symbol, "expiry", trade.ExpiryTimestamp, "rule", policy.NoPriceRule)
	}

	claimed, err := claimExpiry(ctx, trade.ID, &intent, settledBy)
	if err != nil {
		return Outcome{}, err
	}
	if claimed == nil {
		current, err := loadTrade(ctx, trade.ID)
		if err != nil {
			return Outcome{}, err
		}
		if current != nil && current.Status == models.TradeStatusOpen {
			// Still OPEN: the database clock has not reached expiry yet.
			at := util.NowMs() + policy.RetryDelayMs
			if err := Reschedule(ctx, tradeID, at); err != nil {
				return Outcome{}, err
			}
			return Outcome{TradeID: tradeID, Status: StatusNotDue, RetryAt: &at}, nil
		}
		return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
	}
	return Execute(ctx, claimed)
}

// recoverWithoutIntent handles a SETTLING trade an earlier version claimed
// but never finished (invariant 7).
func recoverWithoutIntent(ctx context.Context, trade *models.BinaryTrade, settledBy string) (Outcome, error) {
	tradeID := trade.ID.Hex()
	if trade.UpdatedAt.After(staleBefore()) {
		// Possibly still being worked on; the sweeper brings it back once stale.
		return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
	}

	intent, err := legacyIntent(ctx, trade, settledBy)
	if err != nil {
		return Outcome{}, err
	}
	if intent != nil {
		claimed, err := attachIntent(ctx, trade, intent, models.TradeStatusSettling, settledBy)
		if err != nil {
			return Outcome{}, err
		}
		if claimed == nil {
			return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
		}
		return Execute(ctx, claimed)
	}

	// No money moved and no row: the claim was abandoned before anything
	// happened. Give the trade back to OPEN; it settles normally at expiry
	// (or now, if expiry has passed) - never on a price taken before expiry.
	reverted, err := revertToOpen(ctx, trade)
	if err != nil {
		return Outcome{}, err
	}
	if !reverted {
		return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
	}
	current, err := loadTrade(ctx, trade.ID)
	if err != nil || current == nil {
		return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, err
	}
	if current.Status != models.TradeStatusOpen {
		return Outcome{TradeID: tradeID, Status: StatusClaimedElsewhere}, nil
	}
	return settleOpen(ctx, current, settledBy)
}

func staleBefore() time.Time {
	return time.UnixMilli(util.NowMs() - StaleSettlingMs).UTC()
}
