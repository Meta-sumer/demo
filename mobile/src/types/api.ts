/**
 * API contracts shared with the server.
 *
 * Every monetary field is an INTEGER number of MINOR units (paise/cents) -
 * exactly what the server stores. `formatMoney` divides for display and
 * `toMinor` multiplies on input; nothing else does arithmetic on money.
 */

export type Direction = 'UP' | 'DOWN';
export type TradeStatus = 'OPEN' | 'SETTLING' | 'WON' | 'LOST' | 'DRAW' | 'CANCELLED' | 'CLOSED_EARLY';
export type TradeResult = 'WIN' | 'LOSS' | 'DRAW' | 'EARLY_CLOSE';
export type FeedStatus = 'HEALTHY' | 'DEGRADED' | 'STALE' | 'OFFLINE';

/**
 * Which balance a wallet or trade belongs to. DEMO is virtual money for
 * practice; LIVE holds real deposits. Deposits and withdrawals are LIVE only.
 */
export type AccountType = 'LIVE' | 'DEMO';

export interface ApiSuccess<T> {
  success: true;
  data: T;
  meta?: Record<string, unknown>;
}

export interface ApiError {
  success: false;
  error: { code: string; message: string; details?: unknown };
}

export interface User {
  id: string;
  email: string;
  username: string;
  fullName: string | null;
  phone: string | null;
  country: string | null;
  /** ISO date only, YYYY-MM-DD. */
  dateOfBirth: string | null;
  address: string | null;
  role: string;
  permissions: string[];
  status: 'ACTIVE' | 'SUSPENDED' | 'CLOSED';
  emailVerified: boolean;
  createdAt: string;
}

export interface Wallet {
  id: string;
  userId: string;
  accountType: AccountType;
  currency: string;
  balance: number;
  lockedBalance: number;
  availableBalance: number;
  updatedAt: string;
}

export interface AuthTokens {
  accessToken: string;
  refreshToken: string;
  accessTokenExpiresIn: number;
  refreshTokenExpiresIn: number;
  tokenType: 'Bearer';
}

export interface AuthResult {
  user: User;
  wallet: Wallet;
  tokens: AuthTokens;
  sessionId: string;
}

export interface PayoutConfig {
  id: string;
  assetId: string;
  expirySeconds: number;
  payoutPercent: number;
  minStake: number | null;
  maxStake: number | null;
  version: number;
}

export interface FeedHealth {
  assetId: string;
  symbol: string;
  status: FeedStatus;
  lastPrice: number | null;
  providerTimestamp: number | null;
  serverReceivedAt: number | null;
  feedLatencyMs: number | null;
  totalLatencyMs: number | null;
  ageMs: number | null;
  provider: string;
  updatedAt: number;
}

export interface Asset {
  id: string;
  symbol: string;
  name: string;
  category: string;
  active: boolean;
  pricePrecision: number;
  minStake: number;
  maxStake: number;
  supportedExpiries: number[];
  marketOpen: boolean;
  displayOrder: number;
  payouts: PayoutConfig[];
  feed: FeedHealth;
  sentiment: AssetSentiment | null;
  /** Last price against the price 24 hours ago, in percent. Null without history. */
  change24hPercent: number | null;
  /**
   * When a closed market next opens, epoch ms. Null while it is open, when it
   * has no schedule, or from a server that does not send it yet.
   */
  nextOpenAt?: number | null;
  /**
   * What an exact match of entry and expiry price settles as. Set in the
   * platform's risk configuration; absent from a server that predates it,
   * which only ever refunds.
   */
  drawPolicy?: DrawPolicy;
}

/** A tie refunds the investment (the default), or counts as a loss or a win. */
export type DrawPolicy = 'REFUND' | 'LOSS' | 'WIN';

/** How the open book splits between UP and DOWN. Null when nothing is open. */
export interface AssetSentiment {
  assetId: string;
  symbol: string;
  upPercent: number | null;
  downPercent: number | null;
  openTrades: number;
}

/** Live "Sell now" price for an open position. */
export interface EarlyCloseQuote {
  tradeId: string;
  value: number;
  customerResult: number;
  winProbability: number;
  inTheMoney: boolean;
  currentPrice: number;
  secondsRemaining: number;
  available: boolean;
  unavailableReason: string | null;
}

export interface Trade {
  id: string;
  reference: string;
  userId: string;
  accountType: AccountType;
  assetId: string;
  symbol: string;
  direction: Direction;
  stake: number;
  currency: string;
  payoutPercent: number;
  profit: number;
  potentialReturn: number;
  entryPrice: number;
  expiryPrice: number | null;
  entryTimestamp: number;
  expiryTimestamp: number;
  status: TradeStatus;
  result: TradeResult | null;
  settlementAmount: number | null;
  /** Where the order was placed from. */
  ip: string | null;
  settledAt: string | null;
  createdAt: string;
}

export interface Tick {
  assetId: string;
  symbol: string;
  price: number;
  bid?: number;
  ask?: number;
  timestamp: number;
  receivedAt: number;
  totalLatencyMs: number;
}

/**
 * Totals for the trades a filter selects, across every page - the list itself
 * is paginated, so a total computed from the page on screen would be wrong.
 * Money in minor units; netResult is the sum of settled results.
 */
export interface TradeSummary {
  count: number;
  open: number;
  won: number;
  lost: number;
  draw: number;
  soldEarly: number;
  cancelled: number;
  invested: number;
  netResult: number;
}

export interface Candle {
  time: number;
  open: number;
  high: number;
  low: number;
  close: number;
}

export interface WalletTransaction {
  _id: string;
  type: string;
  movement: 'CREDIT' | 'DEBIT' | 'LOCK' | 'UNLOCK';
  amount: number;
  balanceBefore: number;
  balanceAfter: number;
  description: string | null;
  referenceId: string | null;
  createdAt: string;
}

/* ------------------------------------------------------------------ USDT */

/*
 * The USDT wallet, kept apart from the INR Live and Demo balances.
 *
 * Every amount below is an INTEGER number of micro-USDT (1 USDT = 1_000_000).
 * `parseUsdt` / `formatUsdt` in utils/usdt.ts are the only conversions.
 */

export type UsdtNetworkCode = 'USDTTRC20' | 'USDTBSC';

export interface UsdtNetworkView {
  code: UsdtNetworkCode;
  label: 'TRC20' | 'BSC';
  name: string;
  chain: string;
  decimals: number;
  depositEnabled: boolean;
  withdrawEnabled: boolean;
  /** 0 when there is no minimum. */
  minDeposit: number;
  addressExample: string;
}

export interface UsdtWalletView {
  id: string;
  currency: 'USDT';
  decimals: 6;
  balance: number;
  /** Reserved by withdrawals in progress. */
  lockedBalance: number;
  availableBalance: number;
  /** Deposits paid or confirming but not yet credited; not part of `balance`. */
  pendingDeposits: number;
  updatedAt: string;
}

export type UsdtDepositStatus =
  | 'created'
  | 'awaiting_payment'
  | 'partially_paid'
  | 'confirming'
  | 'paid'
  | 'overpaid'
  | 'underpaid'
  | 'expired'
  | 'cancelled'
  | 'failed';

export interface UsdtDepositView {
  id: string;
  orderId: string;
  invoiceId: string | null;
  reference: string;
  /** Network code, e.g. "USDTTRC20". */
  network: string;
  mode: string;
  status: UsdtDepositStatus;
  amountExpected: number;
  amountReceived: number;
  amountCredited: number;
  feeAmount: number;
  netAmount: number;
  payAddress: string | null;
  /** The provider's hosted checkout. */
  paymentUrl: string | null;
  expiresAt: string | null;
  /** True only once the wallet has actually been credited. */
  credited: boolean;
  creditedAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export type UsdtWithdrawalStatus =
  | 'pending_review'
  | 'requested'
  | 'awaiting_approval'
  | 'sending'
  | 'completed'
  | 'failed'
  | 'rejected';

export interface UsdtWithdrawalView {
  id: string;
  reference: string;
  providerReference: string | null;
  network: string;
  address: string;
  amount: number;
  feeAmount: number;
  netAmount: number;
  status: UsdtWithdrawalStatus;
  txHash: string | null;
  failureReason: string | null;
  reviewNote: string | null;
  createdAt: string;
  updatedAt: string;
  completedAt: string | null;
}

export interface UsdtWithdrawalQuote {
  network: string;
  amount: number;
  feeAmount: number;
  netAmount: number;
  feeIsEstimate: boolean;
  minWithdrawal: number;
  /** 0 when there is no maximum. */
  maxWithdrawal: number;
  /** 0 when there is no daily cap. */
  dailyCap: number;
  dailyUsed: number;
  /** Withdrawals above this are checked by the team first; 0 when none are. */
  reviewedAbove: number;
  needsReview: boolean;
  feePercent: number;
  feeFixed: number;
}

export interface UsdtTransferView {
  id: string;
  amount: number;
  from: 'USDT_WALLET';
  to: 'TRADING';
  createdAt: string;
}

/** GET /wallet/usdt. `trading` is the USDT balance on the trading side, when there is one. */
export interface UsdtOverview {
  wallet: UsdtWalletView | null;
  trading: Wallet | null;
  networks: UsdtNetworkView[];
  enabled: boolean;
  transferToTradingEnabled: boolean;
}

export interface UsdtTransferResult {
  transfer: UsdtTransferView;
  wallet: UsdtWalletView;
  trading: Wallet | null;
}

/** A USDT ledger row: the ordinary ledger row plus where it happened and any fee. */
export interface UsdtTransaction extends WalletTransaction {
  network?: string | null;
  fee?: number | null;
}

/** `usdt.updated` on the user channel. */
export interface UsdtUpdatedEvent {
  kind: 'deposit' | 'withdrawal' | 'transfer';
  id: string;
  status: string;
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
  amount: number;
  currency: string;
  status: 'PENDING' | 'APPROVED' | 'REJECTED' | 'FAILED' | 'COMPLETED';
  method: string | null;
  /** Bank reference: supplied by the customer on a deposit, by the operator on a payout. */
  utr: string | null;
  proof: TransferProof | null;
  customerNote: string | null;
  destination: PayoutDestination | null;
  reviewNote: string | null;
  reviewedAt: string | null;
  createdAt: string;
}

/** Where to send money for a manual deposit. */
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
}

export interface Notification {
  id: string;
  type: string;
  title: string;
  body: string;
  data: Record<string, unknown>;
  read: boolean;
  createdAt: string;
}

/** Live support chat - one conversation per customer. */
export type SupportSender = 'USER' | 'AGENT' | 'SYSTEM';

export interface SupportConversation {
  id: string;
  userId: string;
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
