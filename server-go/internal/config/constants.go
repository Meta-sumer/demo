package config

// Redis key namespace. Identical to the Node server so both can share one
// Redis during a migration.
var RedisKeys = struct {
	LastTick         func(assetID string) string
	TickHistory      func(assetID string) string
	FeedHealth       func(assetID string) string
	SubscribedAssets string
	ProviderStatus   string

	Exposure       func(assetID string) string
	ExposureAssets string
	UserExposure   func(userID string) string
	RiskState      func(assetID string) string

	ExpiryQueue     string
	SettlementQueue string
	ExpiryClaim     func(tradeID string) string

	HedgeQueue string

	Lock            func(name string) string
	RateLimit       func(bucket, identity string) string
	Idempotency     func(userID, key string) string
	Latency         func(metric string) string
	Counters        func(day string) string
	RiskConfigCache string
	WSConnections   string

	// BeeX payout limits (JSON {limits, fetchedAt}).
	UsdtPayoutLimits string
}{
	LastTick:         func(id string) string { return "md:tick:" + id },
	TickHistory:      func(id string) string { return "md:hist:" + id },
	FeedHealth:       func(id string) string { return "md:health:" + id },
	SubscribedAssets: "md:subscribed",
	ProviderStatus:   "md:provider:status",

	Exposure:       func(id string) string { return "exp:asset:" + id },
	ExposureAssets: "exp:assets",
	UserExposure:   func(id string) string { return "exp:user:" + id },
	RiskState:      func(id string) string { return "risk:asset:" + id },

	ExpiryQueue:     "expiry:queue",
	SettlementQueue: "settlement:queue",
	ExpiryClaim:     func(id string) string { return "expiry:claim:" + id },

	HedgeQueue: "hedge:queue",

	Lock:            func(name string) string { return "lock:" + name },
	RateLimit:       func(bucket, identity string) string { return "rl:" + bucket + ":" + identity },
	Idempotency:     func(userID, key string) string { return "idem:" + userID + ":" + key },
	Latency:         func(metric string) string { return "metrics:latency:" + metric },
	Counters:        func(day string) string { return "metrics:counters:" + day },
	RiskConfigCache: "cfg:risk",
	WSConnections:   "ws:connections",

	UsdtPayoutLimits: "usdt:payout-limits",
}

// PubSubBroadcast carries websocket events between server instances.
const PubSubBroadcast = "pubsub:broadcast"

// Websocket event names (spec section 26).
const (
	EvMarketTick   = "market.tick"
	EvMarketStatus = "market.status"

	EvTradeCreated  = "trade.created"
	EvTradeUpdated  = "trade.updated"
	EvTradeExpiring = "trade.expiring"
	EvTradeSettled  = "trade.settled"

	EvExposureUpdated = "exposure.updated"
	EvRiskUpdated     = "risk.updated"

	EvHedgeCreated = "hedge.created"
	EvHedgeUpdated = "hedge.updated"

	EvWalletUpdated       = "wallet.updated"
	EvNotificationCreated = "notification.created"
	// A USDT deposit, withdrawal or transfer changed: {kind, id, status}.
	EvUsdtUpdated = "usdt.updated"

	EvSupportMessage      = "support.message"
	EvSupportConversation = "support.conversation"
	EvSupportTyping       = "support.typing"

	EvConnected  = "connection.ready"
	EvError      = "connection.error"
	EvPong       = "connection.pong"
	EvSubscribed = "subscription.updated"
)

// Websocket channels.
func ChMarket(assetID string) string { return "market:" + assetID }
func ChUser(userID string) string    { return "user:" + userID }

const (
	ChAdminRisk    = "admin:risk"
	ChAdminOps     = "admin:ops"
	ChAdminMarket  = "admin:market"
	ChAdminSupport = "admin:support"
)

// Latency metric names.
const (
	LatFeedProviderToServer = "feed.provider_to_server"
	LatFeedProcessing       = "feed.processing"
	LatFeedTotal            = "feed.total"
	LatTradeAcceptance      = "trade.acceptance"
	LatTradePersistence     = "trade.persistence"
	LatExpiryDetection      = "expiry.detection"
	LatSettlement           = "settlement.duration"
	LatWSDelivery           = "ws.delivery"
	LatHedgeRequest         = "hedge.request"
	LatHedgeFill            = "hedge.fill"
)

// LatencyMetrics lists every metric in report order.
var LatencyMetrics = []string{
	LatFeedProviderToServer, LatFeedProcessing, LatFeedTotal,
	LatTradeAcceptance, LatTradePersistence, LatExpiryDetection,
	LatSettlement, LatWSDelivery, LatHedgeRequest, LatHedgeFill,
}

// TickHistoryRetentionMs is how long per-asset tick history is kept in Redis.
const TickHistoryRetentionMs = 15 * 60 * 1000

// LatencySampleLimit caps the samples kept per metric.
const LatencySampleLimit = 2000
