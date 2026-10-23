/** Admin API contracts. Money is always integer MINOR units. */

export type RiskLevel = 'LOW' | 'MEDIUM' | 'HIGH' | 'CRITICAL';
export type FeedStatus = 'HEALTHY' | 'DEGRADED' | 'STALE' | 'OFFLINE';
export type HedgeOrderStatus =
  | 'NOT_REQUIRED'
  | 'PENDING'
  | 'PARTIALLY_FILLED'
  | 'FILLED'
  | 'FAILED'
  | 'CANCELLED'
  | 'ACCEPTED';

export interface ApiSuccess<T> {
  success: true;
  data: T;
  meta?: Record<string, unknown>;
}

/** Live support chat. */
export type SupportSender = 'USER' | 'AGENT' | 'SYSTEM';

export interface SupportConversation {
  id: string;
  userId: string;
  user?: { id: string; email: string; username: string; fullName: string | null };
  status: 'OPEN' | 'RESOLVED';
  assignedTo: string | null;
  assignedName: string | null;
  lastMessageAt: string | null;
  lastMessagePreview: string | null;
  lastSender: SupportSender | null;
  unreadByUser: number;
  unreadByAgent: number;
  userReadAt: string | null;
  agentReadAt: string | null;
  createdAt: string;
}

export interface SupportMessage {
  id: string;
  conversationId: string;
  sender: SupportSender;
  senderName: string;
  body: string;
  createdAt: string;
}

export interface SupportThread {
  conversation: SupportConversation;
  items: SupportMessage[];
  hasMore: boolean;
}

export interface Paginated<T> {
  items: T[];
  page: number;
  limit: number;
  total: number;
  totalPages: number;
}

export interface AdminUser {
  id: string;
  email: string;
  username: string;
  fullName: string | null;
  phone: string | null;
  country: string | null;
  role: string;
  permissions: string[];
  status: 'ACTIVE' | 'SUSPENDED' | 'CLOSED';
  emailVerified: boolean;
  maxTradeAmount: number | null;
  maxUserExposure: number | null;
  lastLoginAt: string | null;
  createdAt: string;
}

export type AccountType = 'LIVE' | 'DEMO';

export interface AdminWallet {
  id: string;
  userId: string;
  accountType?: AccountType;
  currency: string;
  balance: number;
  lockedBalance: number;
  availableBalance: number;
  updatedAt: string;
}

export interface AuthResult {
  user: AdminUser;
  wallet: AdminWallet;
  tokens: { accessToken: string; refreshToken: string };
  sessionId: string;
}

/**
 * What /admin/login answers instead of a session when the account needs a
 * second factor. The mfaToken is single use and only authorises the
 * /admin/login/2fa steps.
 */
export interface MfaChallenge {
  mfaRequired: true;
  enrolmentRequired: boolean;
  mfaToken: string;
  expiresIn: number;
  methods: string[];
  user: { id: string; email: string; role: string };
}

/** A new authenticator secret, shown once while enrolling. */
export interface MfaEnrolment {
  secret: string;
  secretGrouped: string;
  otpauthUri: string;
  issuer: string;
  account: string;
  algorithm: string;
  digits: number;
  period: number;
  expiresIn: number;
}

export interface MfaStatus {
  enabled: boolean;
  required: boolean;
  enrolledAt: string | null;
  recoveryCodesRemaining: number;
}

export interface AdminMfaRow {
  id: string;
  email: string;
  username: string;
  fullName: string | null;
  role: string;
  status: string;
  enabled: boolean;
  enrolledAt: string | null;
  recoveryCodesRemaining: number;
  lastLoginAt: string | null;
}

export interface DashboardSummary {
  totalUsers: number;
  activeUsers: number;
  openTrades: number;
  todayTrades: number;
  todayVolume: number;
  todayPlatformResult: number;
  currentExposure: number;
  currentNetExposure: number;
  potentialLiability: number;
  hedgedExposure: number;
  unhedgedExposure: number;
  activeAssets: number;
  feedLatencyP50: number;
  feedLatencyP95: number;
  feedLatencyP99: number;
  tradeAcceptanceP95: number;
  totalCustomerBalance: number;
  totalLockedBalance: number;
  pendingDeposits: number;
  pendingWithdrawals: number;
  /** Hedge book (exposure:read): policy target, marks and reconciliation. */
  hedgeTargetNotional?: number;
  hedgeMarkedAt?: string | null;
  hedgeTargetAsOf?: string | null;
  hedgeReconcileStatus?: HedgeReconcileStatus;
  hedgeReconciledAt?: string | null;
  /** When the server computed the figures. */
  generatedAt?: string;
}

export interface DailySeriesPoint {
  date: string;
  trades: number;
  volume: number;
  platformResult: number;
  customerPayout: number;
  wins: number;
  losses: number;
  draws: number;
}

export interface AssetVolumeRow {
  assetId: string;
  symbol: string;
  trades: number;
  volume: number;
  platformResult: number;
}

export interface ExposureSnapshot {
  assetId: string;
  symbol: string;
  upStake: number;
  downStake: number;
  netExposure: number;
  grossExposure: number;
  upPotentialPayout: number;
  downPotentialPayout: number;
  potentialLiability: number;
  openTrades: number;
  updatedAt: number;
}

export interface RiskAssessment {
  assetId: string;
  symbol: string;
  level: RiskLevel;
  score: number;
  utilisationPercent: number;
  netExposure: number;
  grossExposure: number;
  potentialLiability: number;
  reasons: string[];
  hedgeDecision: string;
  targetHedgeRatio: number;
  targetHedgeNotional: number;
  evaluatedAt: number;
  /** Delta-based hedge target and its inputs (docs/audit/hedging). */
  hedgeTarget?: HedgeTargetDetail;
}

export type HedgeReconcileStatus = 'OK' | 'DRIFT' | 'UNKNOWN' | 'NOT_APPLICABLE' | 'NEVER';

export interface HedgeTargetDetail {
  bookDelta: number;
  rawTarget: number;
  targetSigned: number;
  cap: number;
  cappedBy: string;
  hedgeRatio: number;
  bandRatio: number;
  volatilityBoost: number;
  assetCeiling: number;
  sigma: number;
  sigmaSource: string;
  referencePrice: number;
  eligibleTrades: number;
  excludedTrades: number;
  gammaWindowSeconds: number;
  reason: string;
}

export interface ExposureResponse {
  totals: {
    grossExposure: number;
    netExposure: number;
    potentialLiability: number;
    openTrades: number;
  };
  assets: Array<ExposureSnapshot & { risk: RiskAssessment | null }>;
}

export interface HedgeMonitorRow {
  assetId: string;
  symbol: string;
  netExposure: number;
  targetHedge: number;
  currentHedge: number;
  remainingExposure: number;
  riskLevel: RiskLevel;
  hedgeDecision: string;
  provider: string;
  orderStatus: HedgeOrderStatus;
  fillPrice: number | null;
  hedgePnl: number;
  hedgeEnabled: boolean;
  hedgeable: boolean;
  venue: string;
  venueSymbol: string;
  bookDelta: number;
  targetSigned: number;
  hedgeRatio: number;
  cappedBy: string;
  targetReason: string;
  sigma: number;
  sigmaSource: string;
  positionQty: number;
  positionNotional: number;
  markPrice: number;
  markedAt: string | null;
  workingQty: number;
  workingNotional: number;
  residualNotional: number;
  realisedPnl: number;
  fees: number;
  funding: number;
  unrealisedPnl: number;
  reconcileStatus: HedgeReconcileStatus;
  reconciledAt: string | null;
  venueQty: number | null;
  driftQty: number;
  available: boolean;
  unavailableReason: string;
  expensive: boolean;
  roundTripBps: number;
  evaluatedAt: number;
}

export interface FxRate {
  inrPerUsdt: number;
  asOf: number;
  source: string;
  ageMs: number;
  maxAgeMs: number;
  missing: boolean;
  stale: boolean;
}

export interface HedgeVenueStatus {
  symbol: string;
  ok: boolean;
  error?: string;
  mid: number;
  mark: number;
  fundingRate: number;
  takerFeeBps: number;
  updatedAt: number;
  ageMs: number;
  stale: boolean;
}

export interface HedgeStatus {
  provider: string;
  venue: string;
  live: boolean;
  liveExecutionEnabled: boolean;
  canTrade: boolean;
  canTradeReason?: string;
  fx: FxRate;
  venues: HedgeVenueStatus[];
  reconcileStatus: HedgeReconcileStatus;
  lastReconciledAt: string | null;
  lastCheckedAt: string | null;
  openAlerts: number;
  costModelErrorBps: number | null;
  costModelSamples: number;
  hedgeIntervalMs: number;
  reconcileIntervalMs: number;
  venueMaxAgeMs: number;
  generatedAt: string;
}

export interface HedgeAlert {
  id: string;
  code: string;
  severity: string;
  message: string;
  details: Record<string, unknown>;
  assetId: string | null;
  status: 'open' | 'resolved';
  occurrences: number;
  firstSeenAt: string;
  lastSeenAt: string;
  resolvedAt: string | null;
  resolutionNote: string | null;
}

export interface HedgeOrder {
  id: string;
  reference: string;
  assetId: string;
  symbol: string;
  provider: string;
  side: 'UP' | 'DOWN';
  requestedNotional: number;
  filledNotional: number;
  remainingNotional: number;
  averageFillPrice: number | null;
  status: HedgeOrderStatus;
  providerOrderId: string | null;
  riskLevel: string;
  hedgeLevel: string;
  targetHedgeNotional: number;
  realisedPnl: number;
  unrealisedPnl: number;
  error: string | null;
  requestLatencyMs: number | null;
  createdAt: string;
  venue?: string;
  venueSymbol?: string;
  clientOrderId?: string;
  venueStatus?: string;
  targetQty?: number;
  filledQty?: number;
  remainingQty?: number;
  fillCount?: number;
  fee?: number;
  feeAsset?: string;
  feeInr?: number;
  working?: boolean;
  sendAttempts?: number;
  estimatedCostBps?: number;
  realisedCostBps?: number | null;
  inrPerUsdt?: number;
  reconciledAt?: string | null;
  finalAt?: string | null;
}

export interface FeedHealth {
  assetId: string;
  symbol: string;
  status: FeedStatus;
  lastPrice: number | null;
  providerTimestamp: number | null;
  serverReceivedAt: number | null;
  feedLatencyMs: number | null;
  processingLatencyMs: number | null;
  totalLatencyMs: number | null;
  ageMs: number | null;
  provider: string;
  updatedAt: number;
  /** Why a fresh feed is still treated as STALE (dropped ticks, clock drift). */
  blockReason?: string;
}

export interface LatencyReportEntry {
  metric: string;
  count: number;
  min: number;
  max: number;
  avg: number;
  p50: number;
  p95: number;
  p99: number;
}

export interface MarketDataOverview {
  provider: { provider: string; state: string; at: number } | null;
  feeds: FeedHealth[];
  latency: LatencyReportEntry[];
  /** Feed data rules: gaps, dropped ticks, clock check (absent from older servers). */
  quality?: FeedQualityReport;
}

/** One asset's feed quality (docs/audit/operations/feed-rules.md). */
export interface FeedQualityView {
  assetId: string;
  symbol: string;
  /** LIVE, QUIET, GAP, DISCONNECTED, NO_DATA or NOT_EVALUATED. */
  state: string;
  evaluatedAt: number;
  lastSeenAt: number;
  lastUpdateAt: number;
  expectedIntervalMs: number;
  updateIntervalMs: number;
  gapThresholdMs: number;
  seen: number;
  updates: number;
  heartbeats: number;
  seqRegressions: number;
  droppedQueueFull: number;
  droppedInvalid: number;
  lastDropAt: number;
  maxAheadMs: number;
  gapOpen: boolean;
  gapStartedAt: number;
  gapReason?: string;
  rejected: Record<string, number>;
  block: { code: string; detail: string } | null;
}

export interface ClockState {
  state: 'OK' | 'DRIFT' | 'ERROR' | 'NOT_SUPPORTED' | string;
  source: string;
  offsetMs: number;
  rttMs: number;
  driftMs: number;
  maxDriftMs: number;
  checkedAt: number;
  error?: string;
}

export interface MarketDataGap {
  id: string;
  assetId: string;
  symbol: string;
  provider: string;
  reason: string;
  startedAt: number;
  endedAt: number | null;
  durationMs: number | null;
  detectedAt: number;
  thresholdMs: number;
  expectedIntervalMs: number;
  open: boolean;
  createdAt: string;
}

export interface FeedQualityReport {
  assets: FeedQualityView[];
  clock: ClockState | null;
  queueHighWater: number;
  queueCap: number;
  droppedTotal: number;
  gaps: MarketDataGap[];
}

export interface PayoutConfig {
  id: string;
  assetId: string;
  expirySeconds: number;
  payoutPercent: number;
  minStake: number | null;
  maxStake: number | null;
  version: number;
  active: boolean;
  effectiveFrom: string;
  effectiveTo: string | null;
  note: string | null;
  /** Pricing-engine terms (payoutPercent is the base payout); null = the instrument rule's default. */
  minPayoutPercent?: number | null;
  maxPayoutPercent?: number | null;
  marginPercent?: number | null;
  /** PENDING, ACTIVATED, ABANDONED, SCHEDULED or CANCELLED; absent on old rows. */
  activation?: string;
  /** A scheduled change: when it was asked to take effect. */
  scheduledFrom?: string | null;
}

export interface AdminAsset {
  id: string;
  symbol: string;
  name: string;
  category: string;
  active: boolean;
  providerSymbol: string;
  provider: string;
  pricePrecision: number;
  minStake: number;
  maxStake: number;
  supportedExpiries: number[];
  exposureLimit: number;
  netExposureLimit: number;
  hedgeEnabled: boolean;
  targetHedgeRatio: number;
  hedgeThresholdPercent: number | null;
  minHedgeNotional: number;
  maxHedgeNotional: number;
  displayOrder: number;
  marketOpen: boolean;
  payouts: PayoutConfig[];
  payoutHistory?: PayoutConfig[];
  /** False when the pricing engine cannot price the instrument (launch scope). */
  priced?: boolean;
  notPricedReason?: string;
}

export interface AdminTrade {
  id: string;
  reference: string;
  userId: string;
  assetId: string;
  symbol: string;
  direction: 'UP' | 'DOWN';
  accountType?: AccountType;
  stake: number;
  payoutPercent: number;
  profit: number;
  potentialReturn: number;
  entryPrice: number;
  expiryPrice: number | null;
  entryTimestamp: number;
  expiryTimestamp: number;
  status: string;
  result: string | null;
  settlementAmount: number | null;
  settledAt: string | null;
  createdAt: string;
  /** The offer the trade was accepted on, and its frozen pricing record (trade detail only). */
  offerId?: string;
  pricing?: TradePricing | null;
  /** The settlement rule behind expiryPrice, e.g. NO_PRICE_VOID_REFUND. */
  expiryPriceSource?: string;
  /** Voided for want of a valid expiry price; status stays DRAW. */
  voided?: boolean;
  voidReason?: string;
  /** The tie rule frozen on the trade at acceptance. */
  drawPolicy?: 'REFUND' | 'LOSS' | 'WIN';
}

export interface HedgeLevel {
  name: string;
  fromPercent: number;
  toPercent: number;
  hedgeRatio: number;
}

export interface RiskConfig {
  _id: string;
  version: number;
  active: boolean;
  maxAssetExposure: number;
  maxUserExposure: number;
  maxGrossExposure: number;
  minTradeAmount: number;
  maxTradeAmount: number;
  maxOpenTradesPerUser: number;
  maxUserAssetConcentrationPercent: number;
  /** Worst-case payout ceilings, minor units; 0 = no limit. */
  maxExpiryLiability?: number;
  maxAssetLiability?: number;
  maxPlatformLiability?: number;
  hedgeThresholdPercent: number;
  criticalExposurePercent: number;
  hedgeLevels: HedgeLevel[];
  minSecondsToExpiryForHedge: number;
  volatilityHedgeBoost: number;
  slippageTolerancePercent: number;
  earlyCloseEnabled: boolean;
  earlyCloseFeePercent: number;
  minSecondsRemainingForEarlyClose: number;
  drawPolicy: 'REFUND' | 'LOSS' | 'WIN';
  feedHealthyMaxMs: number;
  feedDegradedMaxMs: number;
  /** Retired: never read by the server, no longer editable. */
  feedStaleMaxMs?: number;
  feedTickMaxAgeMs: number;
  haltTradingOnStaleFeed: boolean;
  haltTradingOnDegradedFeed: boolean;
  tradingHalted: boolean;
  /** Feed data rules (docs/audit/operations/feed-rules.md); absent from older servers. */
  feedTickHistoryRetentionMs?: number;
  feedGapMinMs?: number;
  feedGapIntervalMultiple?: number;
  feedSaturationHoldMs?: number;
  feedMaxClockDriftMs?: number;
  feedClockCheckIntervalMs?: number;
  /** Settlement and Sell-now policy (docs/audit/customer/disclosures.md). */
  settlementMaxTickAgeMs?: number;
  settlementConfirmationWindowMs?: number;
  settlementDeferWindowMs?: number;
  settlementRetryDelayMs?: number;
  settlementMaxClockSkewMs?: number;
  settlementNoPriceRule?: 'VOID_REFUND' | 'STALE_TICK_THEN_VOID';
  earlyCloseFloorMs?: number;
  earlyCloseMaxTickAgeMs?: number;
  earlyCloseQuoteMaxAgeMs?: number;
  earlyCloseQuoteTolerancePercent?: number;
  note: string | null;
  createdAt: string;
}

export interface PlatformPnl {
  from: string;
  to: string;
  customerLiability: number;
  customerPayout: number;
  customerStakesCollected: number;
  platformGrossResult: number;
  hedgeRealisedPnl: number;
  hedgeUnrealisedPnl: number;
  fees: number;
  adjustments: number;
  netResult: number;
  /** hedgeRealisedPnl = trading - fees + funding + adjustments (fills ledger). */
  hedgeTradingPnl?: number;
  hedgeFees?: number;
  hedgeFunding?: number;
  hedgeAdjustments?: number;
  hedgeFills?: number;
  hedgeReconcileStatus?: HedgeReconcileStatus;
  hedgeReconciledAt?: string | null;
  hedgeMarkedAt?: string | null;
  generatedAt?: string;
}

export interface AuditLog {
  _id: string;
  actorId: string | null;
  actorRole: string;
  actorEmail: string | null;
  action: string;
  resource: string;
  resourceId: string | null;
  before: unknown;
  after: unknown;
  ip: string | null;
  success: boolean;
  reason: string | null;
  createdAt: string;
}

export interface PayoutDestination {
  type: 'UPI' | 'BANK';
  upiId?: string | null;
  accountName?: string | null;
  accountNumber?: string | null;
  ifsc?: string | null;
  bankName?: string | null;
}

export interface TransferProof {
  fileId: string;
  originalName: string;
  mimeType: string;
  size: number;
  uploadedAt: string;
}

export interface Transfer {
  id: string;
  reference: string;
  kind: 'DEPOSIT' | 'WITHDRAWAL';
  userId: string;
  amount: number;
  currency: string;
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'FAILED' | 'COMPLETED';
  method: string | null;
  /** Customer-supplied on a deposit; the operator's payout reference on a withdrawal. */
  utr: string | null;
  proof: TransferProof | null;
  customerNote: string | null;
  destination: PayoutDestination | null;
  reviewNote: string | null;
  reviewedAt: string | null;
  createdAt: string;
}

export interface PaymentMethod {
  id: string;
  key: string;
  label: string;
  type: 'UPI' | 'BANK';
  upiId: string | null;
  accountName: string | null;
  accountNumber: string | null;
  ifsc: string | null;
  bankName: string | null;
  instructions: string | null;
  minAmount: number;
  maxAmount: number;
  active: boolean;
  displayOrder: number;
}

export interface AdminWalletRow {
  _id: string;
  userId: { _id: string; email: string; username: string; status: string } | string;
  accountType?: AccountType;
  currency: string;
  balance: number;
  lockedBalance: number;
  updatedAt: string;
}

export interface ReconciliationCheck {
  name: string;
  passed: boolean;
  expected: number;
  actual: number;
  difference: number;
  details?: Record<string, unknown>;
}

export interface ReconciliationRun {
  _id: string;
  reference: string;
  windowFrom: string;
  windowTo: string;
  status: 'OK' | 'ALERT' | 'FAILED';
  checks: ReconciliationCheck[];
  alerts: string[];
  durationMs: number;
  resolvedAt: string | null;
  resolutionNote: string | null;
  createdAt: string;
}

export interface UserDetail {
  user: AdminUser;
  /** The LIVE wallet. */
  wallet: AdminWallet;
  /** [LIVE, DEMO]. */
  wallets?: AdminWallet[];
  /** The USDT FUNDING wallet (micro-USDT); null when the customer has none. */
  fundingWallet?: AdminWallet | null;
  openExposure: number;
  stats: {
    totalTrades: number;
    openTrades: number;
    wonTrades: number;
    lostTrades: number;
    totalStaked: number;
    netCustomerResult: number;
  };
}

export interface ApiError {
  success: false;
  error: { code: string; message: string; details?: unknown };
}

export interface AuthTokens {
  accessToken: string;
  refreshToken: string;
  accessTokenExpiresIn?: number;
  refreshTokenExpiresIn?: number;
  tokenType?: 'Bearer';
}

/* ------------------------------------------------------------- pricing */

/** Every term of one payout calculation (docs/audit/pricing/pricing-spec.md). Fractions: 0.02 = 2 points. */
export interface PricingComponents {
  base: number;
  min: number;
  max: number;
  step: number;
  sigma: number;
  baselineVol: number;
  volRatio: number;
  spreadBps: number;
  expectedMoveBps: number;
  expiryFactor: number;
  volAdj: number;
  spreadAdj: number;
  hedgeCost: number;
  hedgeAdj: number;
  exposureAdj: number;
  marginAdj: number;
  hedgeUnavailableAdj: number;
  hedgeAvailable: boolean;
  hedgeBasis: string;
  hedgeVenueCost: number;
  /** L(D) before the trade and the limit, minor units. */
  liability: number;
  maxExposure: number;
  utilEstimate: number;
  utilFinal: number;
  raw: number;
  clamped: number;
  payout: number;
}

/** The calibrated safety guard's check of one offer. */
export interface PricingGuard {
  enabled: boolean;
  ok: boolean;
  reason?: string;
  modelVersion: number;
  regime: string;
  probWin: number;
  probDraw: number;
  uncertainty: number;
  breakEvenPayout: number;
  pointEdge: number;
  conservativeEdge: number;
  minEdge: number;
  conservative: boolean;
}

export interface PricingSide {
  ok: boolean;
  payoutPercent: number | null;
  code?: string;
  reason?: string;
  components: PricingComponents;
  guard: PricingGuard;
}

/** One (asset, expiry) of the live pricing board. */
export interface PricingRow {
  assetId: string;
  symbol: string;
  expirySeconds: number;
  mode: 'DYNAMIC' | 'FIXED';
  ruleVersion: string;
  payoutConfigId: string;
  payoutConfigVersion: number;
  basePayoutPercent: number;
  minPayoutPercent: number;
  maxPayoutPercent: number;
  marginPercent: number;
  minStake: number;
  maxStake: number;
  referenceStake: number;
  symbolEnabled: boolean;
  disabledReason?: string;
  feedProvider: string;
  referencePrice: number;
  riskConfigVersion: number;
  modelVersion: number;
  up: PricingSide;
  down: PricingSide;
}

export interface PricingHalt {
  symbol: string;
  reason: string;
  at: string;
  by: string;
}

export interface PricingBoard {
  mode: 'DYNAMIC' | 'FIXED';
  ruleLabel: string;
  riskConfigVersion: number;
  tradingHalted: boolean;
  tradingHaltReason: string;
  halts: PricingHalt[];
  model: { version: number; name: string; hash: string; generatedAt: string } | null;
  hedgeEstimator: string;
  rows: PricingRow[];
}

/** The pricing record frozen on a trade: locked payout, offer and every component. */
export interface TradePricing {
  mode: string;
  ruleVersion: string;
  riskConfigVersion: number;
  payoutConfigVersion: number;
  payoutPercent: number;
  offerId: string;
  inline: boolean;
  offerCreatedAt: number;
  offerValidUntil: number;
  referencePrice: number;
  acceptMid: number;
  slippageBps: number;
  displayedPayoutPercent: number | null;
  feedProvider: string;
  components: PricingComponents;
  guard: PricingGuard;
}
