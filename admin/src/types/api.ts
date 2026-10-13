/** Admin API contracts. Money is always integer MINOR units. */

export type RiskLevel = 'LOW' | 'MEDIUM' | 'HIGH' | 'CRITICAL';
export type FeedStatus = 'HEALTHY' | 'DEGRADED' | 'STALE' | 'OFFLINE';
export type HedgeOrderStatus =
  | 'NOT_REQUIRED'
  | 'PENDING'
  | 'PARTIALLY_FILLED'
  | 'FILLED'
  | 'FAILED'
  | 'CANCELLED';

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
  feedStaleMaxMs: number;
  feedTickMaxAgeMs: number;
  haltTradingOnStaleFeed: boolean;
  haltTradingOnDegradedFeed: boolean;
  tradingHalted: boolean;
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
