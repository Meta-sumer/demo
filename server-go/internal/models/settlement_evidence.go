package models

import "time"

// Settlement state machine and evidence (audit findings F-11, F-12, F-14).
//
// A trade leaves OPEN exactly once, through one conditional update that sets
// status SETTLING and records an immutable SettlementIntent in the same write.
// The intent fixes the whole financial outcome (result, wallet operation,
// amounts, evidence). Executing it - wallet move keyed trade:settle:<id>,
// settlement row unique on tradeId, trade finalisation conditional on
// SETTLING - is idempotent, so any process can finish an interrupted
// settlement and every path produces the same single outcome.

// SettlementKind is which path decided the outcome.
type SettlementKind string

const (
	SettlementKindExpiry     SettlementKind = "EXPIRY"
	SettlementKindEarlyClose SettlementKind = "EARLY_CLOSE"
)

// SettlementWalletOp is the single wallet movement an intent performs.
type SettlementWalletOp string

const (
	SettlementWalletWin        SettlementWalletOp = "WIN"         // release stake, credit profit
	SettlementWalletLoss       SettlementWalletOp = "LOSS"        // stake leaves the wallet
	SettlementWalletRefund     SettlementWalletOp = "REFUND"      // release stake untouched
	SettlementWalletEarlyClose SettlementWalletOp = "EARLY_CLOSE" // release stake, credit/debit value - stake
)

// Settlement rules: which rule produced the outcome. Stored as the trade's
// expiryPriceSource and as evidence.rule on the settlement row.
const (
	// The latest tick stamped at or before expiry, within the max tick age.
	SettlementRuleTickAtOrBeforeExpiry = "TICK_AT_OR_BEFORE_EXPIRY"
	// No tick within the max age after the defer window; the configured
	// no-price rule allowed the latest older tick at or before expiry.
	SettlementRuleStaleTickBeforeExpiry = "STALE_TICK_BEFORE_EXPIRY"
	// No usable tick after the defer window: the trade is voided and the
	// stake returned (status DRAW, wallet TRADE_REFUND).
	SettlementRuleNoPriceVoidRefund = "NO_PRICE_VOID_REFUND"
	// Customer "Sell now" at the server's fresh market price.
	SettlementRuleEarlyCloseAtMarket = "EARLY_CLOSE_AT_MARKET"
	// Recovered from a ledger row or settlement row written by an earlier
	// version that crashed before finishing (no intent was recorded).
	SettlementRuleLedgerRecovery = "LEDGER_RECOVERY"
)

// No-price rules (SettlementPolicy.NoPriceRule).
const (
	NoPriceRuleVoidRefund          = "VOID_REFUND"
	NoPriceRuleStaleTickThenVoid   = "STALE_TICK_THEN_VOID"
	DefaultSettlementNoPriceRule   = NoPriceRuleVoidRefund
	SettlementEvidenceSourceHist   = "md:hist"
	SettlementEvidenceSourceTick   = "md:tick"
	SettlementEvidenceSourceLedger = "wallet_ledger"
)

var NoPriceRuleValues = []string{NoPriceRuleVoidRefund, NoPriceRuleStaleTickThenVoid}

// EarlyCloseHardFloorMs: an early close is refused unless expiry is at least
// this far away, whatever the configuration says (settlement.PolicyFor and
// the risk-config validation both enforce it).
const EarlyCloseHardFloorMs = 1000

// SettlementPolicy is the effective settlement / early-close policy. The
// values come from the active risk configuration (defaults where a field is
// not configured) and a copy is stored with every outcome, so a decision can
// be replayed without the risk-config history.
type SettlementPolicy struct {
	// Expiry price: the tick used must be stamped at or before expiry and at
	// most this old relative to expiry.
	MaxTickAgeMs int64 `bson:"maxTickAgeMs" json:"maxTickAgeMs"`
	// Settlement waits for a tick stamped after expiry (so a tick still in
	// flight is not missed) for at most this long after expiry.
	ConfirmationWindowMs int64 `bson:"confirmationWindowMs" json:"confirmationWindowMs"`
	// With no qualifying tick, settlement is deferred until this long after
	// expiry, then NoPriceRule applies.
	DeferWindowMs int64 `bson:"deferWindowMs" json:"deferWindowMs"`
	// How often a deferred trade is retried.
	RetryDelayMs int64 `bson:"retryDelayMs" json:"retryDelayMs"`
	// A tick stamped more than this far ahead of the server's receipt time
	// has an untrustworthy clock and is ignored. 0 disables the check.
	MaxClockSkewMs int64  `bson:"maxClockSkewMs" json:"maxClockSkewMs"`
	NoPriceRule    string `bson:"noPriceRule" json:"noPriceRule"`

	// Early close: never at or after expiry (hard floor, ms before expiry).
	EarlyCloseFloorMs int64 `bson:"earlyCloseFloorMs" json:"earlyCloseFloorMs"`
	// The tick an early close is priced on must be at most this old.
	EarlyCloseMaxTickAgeMs int64 `bson:"earlyCloseMaxTickAgeMs" json:"earlyCloseMaxTickAgeMs"`
	// The customer must have been shown a quote within this window.
	EarlyCloseQuoteMaxAgeMs int64 `bson:"earlyCloseQuoteMaxAgeMs" json:"earlyCloseQuoteMaxAgeMs"`
	// The close may pay at most this much (percent of the shown quote) less
	// than the quote the customer saw; beyond it the close is refused.
	EarlyCloseQuoteTolerancePercent float64 `bson:"earlyCloseQuoteTolerancePercent" json:"earlyCloseQuoteTolerancePercent"`
}

// SettlementClaim identifies who moved the trade out of OPEN.
type SettlementClaim struct {
	Token string         `bson:"token"`
	Kind  SettlementKind `bson:"kind"`
	By    string         `bson:"by"`
	At    time.Time      `bson:"at"`
}

// EarlyCloseEvidence is the pricing record of a customer close.
type EarlyCloseEvidence struct {
	// The quote the customer was shown (GET /quote), if any.
	QuotedValue *int64   `bson:"quotedValue"`
	QuotedAt    *int64   `bson:"quotedAt"`
	QuotedPrice *float64 `bson:"quotedPrice"`
	// The fresh re-price the close executed at.
	Value               int64      `bson:"value"`
	WinProbability      float64    `bson:"winProbability"`
	VolatilityPerSecond float64    `bson:"volatilityPerSecond"`
	SecondsRemaining    float64    `bson:"secondsRemaining"`
	FeePercent          float64    `bson:"feePercent"`
	PayoutPercent       float64    `bson:"payoutPercent"`
	FeedStatus          FeedStatus `bson:"feedStatus"`
	FeedAgeMs           int64      `bson:"feedAgeMs"`
}

// SettlementEvidence makes an outcome reproducible after the Redis tick
// history (15 minutes) is gone: the price, where and when it came from, the
// rule and the configuration that applied.
type SettlementEvidence struct {
	Rule string         `bson:"rule"`
	Kind SettlementKind `bson:"kind"`
	// Comparison before the draw policy (nil when no price was compared).
	RawResult  *TradeResult `bson:"rawResult"`
	DrawPolicy DrawPolicy   `bson:"drawPolicy,omitempty"`

	Price *float64 `bson:"price"`
	// The market timestamp the selection ran on (the md:hist score).
	TickTimestamp     *int64 `bson:"tickTimestamp"`
	ProviderTimestamp *int64 `bson:"providerTimestamp"`
	ReceivedAt        *int64 `bson:"receivedAt"`
	Sequence          *int64 `bson:"sequence"`
	Provider          string `bson:"provider"`
	Source            string `bson:"source"`
	// Reference instant minus the tick timestamp (expiry for an expiry
	// settlement, the decision time for an early close).
	TickAgeMs *int64 `bson:"tickAgeMs"`

	// Whether a tick stamped after expiry had been received when deciding,
	// and the first such tick's timestamp.
	PostExpiryTickSeen      bool   `bson:"postExpiryTickSeen"`
	PostExpiryTickTimestamp *int64 `bson:"postExpiryTickTimestamp"`
	CandidatesConsidered    int64  `bson:"candidatesConsidered"`

	ExpiryTimestamp   int64            `bson:"expiryTimestamp"`
	DecidedAt         int64            `bson:"decidedAt"`
	RiskConfigVersion int64            `bson:"riskConfigVersion"`
	Policy            SettlementPolicy `bson:"policy"`

	EarlyClose *EarlyCloseEvidence `bson:"earlyClose,omitempty"`
	Note       string              `bson:"note,omitempty"`
	// Why no price qualified (voids only): FEED_GAP (expiry inside a
	// recorded market data gap, FeedGapID), HISTORY_UNAVAILABLE (the tick
	// history no longer reaches back to expiry: retention or a Redis loss)
	// or NO_TICK_IN_WINDOW.
	NoPriceReason string `bson:"noPriceReason,omitempty"`
	FeedGapID     string `bson:"feedGapId,omitempty"`
}

// No-price reasons (SettlementEvidence.NoPriceReason).
const (
	NoPriceReasonFeedGap            = "FEED_GAP"
	NoPriceReasonHistoryUnavailable = "HISTORY_UNAVAILABLE"
	NoPriceReasonNoTick             = "NO_TICK_IN_WINDOW"
)

// SettlementIntent is the immutable financial outcome of a trade, recorded
// atomically with the claim and executed idempotently.
type SettlementIntent struct {
	ID          string             `bson:"id"`
	Kind        SettlementKind     `bson:"kind"`
	Result      TradeResult        `bson:"result"`
	FinalStatus TradeStatus        `bson:"finalStatus"`
	WalletOp    SettlementWalletOp `bson:"walletOp"`
	// Profit for WIN, close value for EARLY_CLOSE, stake otherwise.
	WalletAmount int64 `bson:"walletAmount"`
	// nil when no price was used (void).
	ExpiryPrice      *float64           `bson:"expiryPrice"`
	PayoutAmount     int64              `bson:"payoutAmount"`
	PlatformResult   int64              `bson:"platformResult"`
	SettlementAmount int64              `bson:"settlementAmount"`
	SettledBy        string             `bson:"settledBy"`
	Evidence         SettlementEvidence `bson:"evidence"`
	CreatedAt        time.Time          `bson:"createdAt"`
}
