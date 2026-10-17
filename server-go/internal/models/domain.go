package models

// Domain unions from server/src/types/domain.ts. Each union is a typed string
// with its values as constants, plus a []string of every value for
// validation (httpx V.Enum / Q.Enum take ...string).

type Direction string

const (
	DirectionUp   Direction = "UP"
	DirectionDown Direction = "DOWN"
)

var DirectionValues = []string{string(DirectionUp), string(DirectionDown)}

type TradeStatus string

const (
	TradeStatusOpen        TradeStatus = "OPEN"
	TradeStatusSettling    TradeStatus = "SETTLING"
	TradeStatusWon         TradeStatus = "WON"
	TradeStatusLost        TradeStatus = "LOST"
	TradeStatusDraw        TradeStatus = "DRAW"
	TradeStatusCancelled   TradeStatus = "CANCELLED"
	TradeStatusClosedEarly TradeStatus = "CLOSED_EARLY"
)

var TradeStatusValues = []string{
	string(TradeStatusOpen), string(TradeStatusSettling), string(TradeStatusWon), string(TradeStatusLost),
	string(TradeStatusDraw), string(TradeStatusCancelled), string(TradeStatusClosedEarly),
}

// TradeResult: EARLY_CLOSE is a settled outcome like the others, not a
// cancellation - money moved, so it belongs on the settlement ledger.
type TradeResult string

const (
	TradeResultWin        TradeResult = "WIN"
	TradeResultLoss       TradeResult = "LOSS"
	TradeResultDraw       TradeResult = "DRAW"
	TradeResultEarlyClose TradeResult = "EARLY_CLOSE"
)

var TradeResultValues = []string{
	string(TradeResultWin), string(TradeResultLoss), string(TradeResultDraw), string(TradeResultEarlyClose),
}

type UserStatus string

const (
	UserStatusActive    UserStatus = "ACTIVE"
	UserStatusSuspended UserStatus = "SUSPENDED"
	UserStatusClosed    UserStatus = "CLOSED"
)

var UserStatusValues = []string{string(UserStatusActive), string(UserStatusSuspended), string(UserStatusClosed)}

type FeedStatus string

const (
	FeedStatusHealthy  FeedStatus = "HEALTHY"
	FeedStatusDegraded FeedStatus = "DEGRADED"
	FeedStatusStale    FeedStatus = "STALE"
	FeedStatusOffline  FeedStatus = "OFFLINE"
)

var FeedStatusValues = []string{
	string(FeedStatusHealthy), string(FeedStatusDegraded), string(FeedStatusStale), string(FeedStatusOffline),
}

type RiskLevel string

const (
	RiskLevelLow      RiskLevel = "LOW"
	RiskLevelMedium   RiskLevel = "MEDIUM"
	RiskLevelHigh     RiskLevel = "HIGH"
	RiskLevelCritical RiskLevel = "CRITICAL"
)

var RiskLevelValues = []string{
	string(RiskLevelLow), string(RiskLevelMedium), string(RiskLevelHigh), string(RiskLevelCritical),
}

type HedgeDecision string

const (
	HedgeDecisionNoHedge         HedgeDecision = "NO_HEDGE"
	HedgeDecisionPartialHedge    HedgeDecision = "PARTIAL_HEDGE"
	HedgeDecisionHighHedge       HedgeDecision = "HIGH_HEDGE"
	HedgeDecisionMaxAllowedHedge HedgeDecision = "MAX_ALLOWED_HEDGE"
)

var HedgeDecisionValues = []string{
	string(HedgeDecisionNoHedge), string(HedgeDecisionPartialHedge),
	string(HedgeDecisionHighHedge), string(HedgeDecisionMaxAllowedHedge),
}

type HedgeOrderStatus string

const (
	HedgeOrderStatusNotRequired     HedgeOrderStatus = "NOT_REQUIRED"
	HedgeOrderStatusPending         HedgeOrderStatus = "PENDING"
	HedgeOrderStatusPartiallyFilled HedgeOrderStatus = "PARTIALLY_FILLED"
	HedgeOrderStatusFilled          HedgeOrderStatus = "FILLED"
	HedgeOrderStatusFailed          HedgeOrderStatus = "FAILED"
	HedgeOrderStatusCancelled       HedgeOrderStatus = "CANCELLED"
)

var HedgeOrderStatusValues = []string{
	string(HedgeOrderStatusNotRequired), string(HedgeOrderStatusPending), string(HedgeOrderStatusPartiallyFilled),
	string(HedgeOrderStatusFilled), string(HedgeOrderStatusFailed), string(HedgeOrderStatusCancelled),
}

type WalletTransactionType string

const (
	WalletTxDeposit          WalletTransactionType = "DEPOSIT"
	WalletTxWithdrawal       WalletTransactionType = "WITHDRAWAL"
	WalletTxTradeLock        WalletTransactionType = "TRADE_LOCK"
	WalletTxTradeLoss        WalletTransactionType = "TRADE_LOSS"
	WalletTxTradeWin         WalletTransactionType = "TRADE_WIN"
	WalletTxTradeRefund      WalletTransactionType = "TRADE_REFUND"
	WalletTxTradeEarlyClose  WalletTransactionType = "TRADE_EARLY_CLOSE"
	WalletTxTradeCancel      WalletTransactionType = "TRADE_CANCEL"
	WalletTxAdjustmentCredit WalletTransactionType = "ADJUSTMENT_CREDIT"
	WalletTxAdjustmentDebit  WalletTransactionType = "ADJUSTMENT_DEBIT"
	WalletTxFee              WalletTransactionType = "FEE"
	// A movement between two of one customer's own accounts (USDT wallet ->
	// trading account). Each side has its own row; they share referenceId.
	WalletTxTransfer WalletTransactionType = "TRANSFER"
)

var WalletTransactionTypeValues = []string{
	string(WalletTxDeposit), string(WalletTxWithdrawal), string(WalletTxTradeLock), string(WalletTxTradeLoss),
	string(WalletTxTradeWin), string(WalletTxTradeRefund), string(WalletTxTradeEarlyClose),
	string(WalletTxTradeCancel), string(WalletTxAdjustmentCredit), string(WalletTxAdjustmentDebit),
	string(WalletTxFee), string(WalletTxTransfer),
}

type MoneyMovement string

const (
	MovementCredit MoneyMovement = "CREDIT"
	MovementDebit  MoneyMovement = "DEBIT"
	MovementLock   MoneyMovement = "LOCK"
	MovementUnlock MoneyMovement = "UNLOCK"
)

var MoneyMovementValues = []string{
	string(MovementCredit), string(MovementDebit), string(MovementLock), string(MovementUnlock),
}

type TransferStatus string

const (
	TransferStatusPending   TransferStatus = "PENDING"
	TransferStatusApproved  TransferStatus = "APPROVED"
	TransferStatusRejected  TransferStatus = "REJECTED"
	TransferStatusFailed    TransferStatus = "FAILED"
	TransferStatusCompleted TransferStatus = "COMPLETED"
)

var TransferStatusValues = []string{
	string(TransferStatusPending), string(TransferStatusApproved), string(TransferStatusRejected),
	string(TransferStatusFailed), string(TransferStatusCompleted),
}

// MarketTick is a provider tick. Timestamps are epoch ms.
type MarketTick struct {
	Asset string   `json:"asset"`
	Bid   *float64 `json:"bid,omitempty"`
	Ask   *float64 `json:"ask,omitempty"`
	Price float64  `json:"price"`
	// Provider-reported event time, epoch ms.
	Timestamp         int64  `json:"timestamp"`
	ProviderTimestamp *int64 `json:"providerTimestamp,omitempty"`
	// Server clock at the moment the tick entered the process, epoch ms.
	ReceivedAt int64  `json:"receivedAt"`
	Sequence   *int64 `json:"sequence,omitempty"`
}

// NormalisedTick is a MarketTick after ingestion. JSON field order differs
// from the TS object literal, which is irrelevant to consumers.
type NormalisedTick struct {
	MarketTick
	AssetID string `json:"assetId"`
	Symbol  string `json:"symbol"`
	// Server clock once normalisation finished.
	ProcessedAt         int64 `json:"processedAt"`
	FeedLatencyMs       int64 `json:"feedLatencyMs"`
	ProcessingLatencyMs int64 `json:"processingLatencyMs"`
	TotalLatencyMs      int64 `json:"totalLatencyMs"`
}

type FeedHealthSnapshot struct {
	AssetID             string     `json:"assetId"`
	Symbol              string     `json:"symbol"`
	Status              FeedStatus `json:"status"`
	LastPrice           *float64   `json:"lastPrice"`
	ProviderTimestamp   *int64     `json:"providerTimestamp"`
	ServerReceivedAt    *int64     `json:"serverReceivedAt"`
	ProcessedAt         *int64     `json:"processedAt"`
	FeedLatencyMs       *int64     `json:"feedLatencyMs"`
	ProcessingLatencyMs *int64     `json:"processingLatencyMs"`
	TotalLatencyMs      *int64     `json:"totalLatencyMs"`
	AgeMs               *int64     `json:"ageMs"`
	Provider            string     `json:"provider"`
	UpdatedAt           int64      `json:"updatedAt"`
}

// ExposureSnapshot: all amounts in minor units.
type ExposureSnapshot struct {
	AssetID string `json:"assetId"`
	Symbol  string `json:"symbol"`
	// Stake volume.
	UpStake   int64 `json:"upStake"`
	DownStake int64 `json:"downStake"`
	// upStake - downStake (sign shows the side the book leans to).
	NetExposure int64 `json:"netExposure"`
	// upStake + downStake.
	GrossExposure int64 `json:"grossExposure"`
	// Worst-case payout if every UP trade wins.
	UpPotentialPayout   int64 `json:"upPotentialPayout"`
	DownPotentialPayout int64 `json:"downPotentialPayout"`
	// Worst case of the two sides minus the stakes already collected.
	PotentialLiability int64 `json:"potentialLiability"`
	OpenTrades         int64 `json:"openTrades"`
	UpdatedAt          int64 `json:"updatedAt"`

	// Liability model (added fields; the ones above keep their meaning).
	// Platform net result if every open UP trade wins: upProfit - downStake
	// (positive = the platform pays). DownWinNet is the mirror image.
	UpWinNet   int64 `json:"upWinNet"`
	DownWinNet int64 `json:"downWinNet"`
	// Worst-case net liability with each expiry bucket settling on its own
	// price: sum over buckets of max(0, upWinNet_b, downWinNet_b). UP and DOWN
	// only offset each other inside a bucket, so this is >= potentialLiability.
	ExpiryLiability int64 `json:"expiryLiability"`
	// No netting at all - every open trade wins: upProfit + downProfit.
	MaxPayoutLiability int64 `json:"maxPayoutLiability"`
	// Non-empty expiry buckets.
	ExpiryBuckets int64 `json:"expiryBuckets"`
	// In-flight reservations: passed the atomic risk check, trade not yet
	// written. Counted against every limit until committed or released.
	ReservedStake  int64 `json:"reservedStake"`
	ReservedTrades int64 `json:"reservedTrades"`
}

type RiskAssessment struct {
	AssetID             string        `json:"assetId"`
	Symbol              string        `json:"symbol"`
	Level               RiskLevel     `json:"level"`
	Score               float64       `json:"score"`
	UtilisationPercent  float64       `json:"utilisationPercent"`
	NetExposure         int64         `json:"netExposure"`
	GrossExposure       int64         `json:"grossExposure"`
	PotentialLiability  int64         `json:"potentialLiability"`
	ExpiryLiability     int64         `json:"expiryLiability"`
	Reasons             []string      `json:"reasons"`
	HedgeDecision       HedgeDecision `json:"hedgeDecision"`
	TargetHedgeRatio    float64       `json:"targetHedgeRatio"`
	TargetHedgeNotional int64         `json:"targetHedgeNotional"`
	EvaluatedAt         int64         `json:"evaluatedAt"`
}
