import { apiGet, apiPatch, apiPost, apiPut, postForm } from './http';
import type {
  AccountType,
  Asset,
  AuthResult,
  Candle,
  EarlyCloseQuote,
  FeedHealth,
  Notification,
  SupportConversation,
  SupportMessage,
  SupportThread,
  Paginated,
  PaymentMethod,
  PayoutDestination,
  Trade,
  TradeSummary,
  Tick,
  Transfer,
  User,
  UsdtDepositStatus,
  UsdtDepositView,
  UsdtNetworkView,
  UsdtOverview,
  UsdtTransaction,
  UsdtTransferResult,
  UsdtWalletView,
  UsdtWithdrawalQuote,
  UsdtWithdrawalStatus,
  UsdtWithdrawalView,
  Wallet,
  WalletTransaction,
} from '../types/api';

/** Every server endpoint the client uses, in one place. */
export const api = {
  auth: {
    login: (email: string, password: string) => apiPost<AuthResult>('/auth/login', { email, password }),
    register: (input: { email: string; username: string; password: string; fullName?: string }) =>
      apiPost<AuthResult>('/auth/register', input),
    me: () => apiGet<{ user: User; wallet: Wallet }>('/auth/me'),
    logout: (refreshToken?: string) => apiPost<{ loggedOut: boolean }>('/auth/logout', { refreshToken }),
    changePassword: (currentPassword: string, newPassword: string) =>
      apiPost<{ changed: boolean }>('/auth/change-password', { currentPassword, newPassword }),
  },

  profile: {
    get: () => apiGet<User>('/users/me'),
    update: (input: {
      fullName?: string;
      username?: string;
      phone?: string;
      country?: string;
      /** YYYY-MM-DD, or an empty string to clear it. */
      dateOfBirth?: string;
      address?: string;
    }) => apiPatch<User>('/users/me', input),
  },

  favourites: {
    get: () => apiGet<{ assetIds: string[] }>('/users/me/favourites'),
    set: (assetIds: string[]) => apiPut<{ assetIds: string[] }>('/users/me/favourites', { assetIds }),
  },

  assets: {
    list: () => apiGet<Asset[]>('/assets'),
    get: (id: string) => apiGet<Asset>('/assets/' + id),
  },

  market: {
    health: () => apiGet<FeedHealth[]>('/market-data/health'),
    /** Latest candles, or the ones up to `to` (epoch ms) for a past window. */
    candles: (assetId: string, limit = 300, interval = 60, to?: number) =>
      apiGet<Candle[]>('/market-data/' + assetId + '/candles', { limit, interval, to }),
    /**
     * Raw ticks. Only the last 15 minutes are kept, so a window further back
     * comes back empty - fall back to candles then.
     */
    ticks: (assetId: string, params: { from?: number; to?: number; windowMs?: number }) =>
      apiGet<Tick[]>('/market-data/' + assetId + '/ticks', params),
  },

  trades: {
    create: (
      input: {
        assetId: string;
        direction: 'UP' | 'DOWN';
        amount: number;
        expirySeconds: number;
        /**
         * The price on screen when the trader committed.
         *
         * A slippage guard only: the server fills at its own tick and uses
         * this solely to refuse a fill that moved against the trader.
         */
        quotedPrice?: number;
        /** Defaults to LIVE on the server. */
        accountType?: AccountType;
      },
      idempotencyKey: string,
    ) => apiPost<Trade>('/binary/trades', input, { 'idempotency-key': idempotencyKey }),
    list: (params: {
      status?: string;
      /** 'OPEN' for anything still running, 'SETTLED' for anything finished. */
      phase?: 'OPEN' | 'SETTLED';
      assetId?: string;
      direction?: 'UP' | 'DOWN';
      accountType?: AccountType;
      /** ISO instants. */
      from?: string;
      to?: string;
      page?: number;
      limit?: number;
    }) => apiGet<Paginated<Trade>>('/binary/trades', params),
    open: () => apiGet<Trade[]>('/binary/trades/open'),
    get: (id: string) => apiGet<Trade>('/binary/trades/' + id),

    /** Prices a position without closing it. */
    quote: (id: string) => apiGet<EarlyCloseQuote>('/binary/trades/' + id + '/quote'),

    closeEarly: (id: string) =>
      apiPost<{ trade: Trade; quote: EarlyCloseQuote }>('/binary/trades/' + id + '/close'),

    /** Totals for the same filters as list(), across all pages. */
    summary: (params: {
      status?: string;
      phase?: 'OPEN' | 'SETTLED';
      assetId?: string;
      direction?: 'UP' | 'DOWN';
      accountType?: 'LIVE' | 'DEMO';
      from?: string;
      to?: string;
    }) => apiGet<TradeSummary>('/binary/trades/summary', params),
  },

  wallet: {
    get: (accountType: AccountType = 'LIVE') => apiGet<Wallet>('/wallet', { accountType }),
    /** Both accounts, LIVE first. Creates the demo wallet on first use. */
    accounts: () => apiGet<Wallet[]>('/wallet/accounts'),
    /** Tops the demo balance back up to its starting amount. */
    resetDemo: () => apiPost<Wallet>('/wallet/demo/reset'),
    transactions: (params: { page?: number; limit?: number; type?: string; accountType?: AccountType }) =>
      apiGet<Paginated<WalletTransaction>>('/wallet/transactions', params),
  },

  transfers: {
    list: (params: { page?: number; limit?: number; kind?: string }) =>
      apiGet<Paginated<Transfer>>('/transfers', params),

    paymentMethods: () => apiGet<PaymentMethod[]>('/transfers/payment-methods'),

    /**
     * Submits a deposit claim.
     *
     * Sent as multipart when a screenshot is attached, so the reference and the
     * evidence arrive together and a half-submitted claim is impossible.
     */
    deposit: (input: {
      amount: number;
      paymentMethodKey?: string;
      utr?: string;
      customerNote?: string;
      proof?: File | null;
    }) => {
      if (!input.proof) {
        const { proof, ...json } = input;
        void proof;
        return apiPost<Transfer>('/transfers/deposits', json);
      }

      const form = new FormData();
      form.append('amount', String(input.amount));
      if (input.paymentMethodKey) form.append('paymentMethodKey', input.paymentMethodKey);
      if (input.utr) form.append('utr', input.utr);
      if (input.customerNote) form.append('customerNote', input.customerNote);
      form.append('proof', input.proof);
      return postForm<Transfer>('/transfers/deposits', form);
    },

    withdraw: (input: { amount: number; destination: PayoutDestination; customerNote?: string }) =>
      apiPost<Transfer>('/transfers/withdrawals', input),

    /** Authenticated URL for a stored screenshot. */
    proofUrl: (transferId: string) => '/transfers/' + transferId + '/proof',
  },

  /**
   * The USDT wallet. Amounts are integer micro-USDT both ways.
   *
   * Every call that moves money takes an idempotency key: the caller keeps
   * one per attempt (see `createAttemptKey`) so a retried or double-sent
   * request is acted on once.
   */
  usdt: {
    overview: () => apiGet<UsdtOverview>('/wallet/usdt'),
    balance: () => apiGet<UsdtWalletView>('/wallet/usdt/balance'),
    networks: () => apiGet<UsdtNetworkView[]>('/wallet/usdt/networks'),

    createDeposit: (input: { network: string; amount: number }, idempotencyKey: string) =>
      apiPost<UsdtDepositView>('/wallet/usdt/deposit', input, { 'Idempotency-Key': idempotencyKey }),
    deposits: (params: { status?: UsdtDepositStatus; network?: string; page?: number; limit?: number }) =>
      apiGet<Paginated<UsdtDepositView>>('/wallet/usdt/deposits', params),
    deposit: (id: string) => apiGet<UsdtDepositView>('/wallet/usdt/deposits/' + encodeURIComponent(id)),
    /** Asks the provider for the latest status instead of waiting for its callback. */
    refreshDeposit: (id: string) =>
      apiPost<UsdtDepositView>('/wallet/usdt/deposits/' + encodeURIComponent(id) + '/refresh'),

    withdrawalQuote: (params: { network: string; amount: number }) =>
      apiGet<UsdtWithdrawalQuote>('/wallet/usdt/withdrawal-quote', params),
    withdraw: (input: { network: string; address: string; amount: number }, idempotencyKey: string) =>
      apiPost<UsdtWithdrawalView>('/wallet/usdt/withdraw', input, { 'Idempotency-Key': idempotencyKey }),
    withdrawals: (params: { status?: UsdtWithdrawalStatus; network?: string; page?: number; limit?: number }) =>
      apiGet<Paginated<UsdtWithdrawalView>>('/wallet/usdt/withdrawals', params),
    withdrawal: (id: string) => apiGet<UsdtWithdrawalView>('/wallet/usdt/withdrawals/' + encodeURIComponent(id)),

    transferToTrading: (input: { amount: number }, idempotencyKey: string) =>
      apiPost<UsdtTransferResult>('/wallet/usdt/transfer-to-trading', input, { 'Idempotency-Key': idempotencyKey }),

    transactions: (params: { type?: string; page?: number; limit?: number }) =>
      apiGet<Paginated<UsdtTransaction>>('/wallet/usdt/transactions', params),
  },

  notifications: {
    list: (params: { page?: number; limit?: number }) => apiGet<Paginated<Notification>>('/notifications', params),
    markRead: (id: string) => apiPost<{ read: boolean }>('/notifications/' + id + '/read'),
    markAllRead: () => apiPost<{ updated: number }>('/notifications/read-all'),
  },

  support: {
    thread: (params?: { before?: string; limit?: number }) => apiGet<SupportThread>('/support/conversation', params),
    send: (body: string) =>
      apiPost<{ message: SupportMessage; conversation: SupportConversation }>('/support/messages', { body }),
    markRead: () => apiPost<SupportConversation>('/support/read'),
    typing: () => apiPost<{ sent: boolean }>('/support/typing'),
  },
};
