import { apiGet, apiPatch, apiPost, http } from './http';
import type {
  SupportConversation,
  SupportMessage,
  SupportThread,
  AdminAsset,
  AdminTrade,
  AdminUser,
  AdminWalletRow,
  AssetVolumeRow,
  AuditLog,
  AuthResult,
  DailySeriesPoint,
  DashboardSummary,
  ExposureResponse,
  HedgeMonitorRow,
  HedgeOrder,
  LatencyReportEntry,
  MarketDataOverview,
  Paginated,
  PaymentMethod,
  PayoutConfig,
  PlatformPnl,
  ReconciliationRun,
  RiskAssessment,
  RiskConfig,
  Transfer,
  UserDetail,
} from '../types/api';
import type {
  UsdtDepositDetail,
  UsdtDepositFilters,
  UsdtDepositRow,
  UsdtDepositView,
  UsdtNetworkPatch,
  UsdtNetworkView,
  UsdtPayoutLimits,
  UsdtRefreshResult,
  UsdtSummary,
  UsdtWithdrawalDetail,
  UsdtWithdrawalFilters,
  UsdtWithdrawalRow,
  UsdtWithdrawalView,
  WebhookEventFilters,
  WebhookEventView,
} from '../types/usdt';
import { toPaginated } from '../utils/paging';

async function apiDelete<T>(url: string): Promise<T> {
  const response = await http.delete<{ success: true; data: T }>(url);
  return response.data.data;
}

/** Drops empty filters so the query string carries only what the operator set. */
function compact(params: object): Record<string, unknown> {
  return Object.fromEntries(
    Object.entries(params).filter(([, value]) => value !== undefined && value !== null && value !== ''),
  );
}

/** A paginated list, whichever envelope style the route uses (see utils/paging). */
async function apiList<T>(url: string, params: { page?: number; limit?: number }): Promise<Paginated<T>> {
  const response = await http.get<{ data: unknown; meta?: Record<string, unknown> }>(url, { params: compact(params) });
  return toPaginated<T>(response.data.data, response.data.meta, params);
}

const usdtPath = (...parts: string[]) => '/admin/usdt/' + parts.map(encodeURIComponent).join('/');

/** Every admin endpoint, grouped by the screen that uses it. */
export const api = {
  auth: {
    login: (email: string, password: string) => apiPost<AuthResult>('/admin/login', { email, password }),
    me: () => apiGet<{ user: AdminUser }>('/auth/me'),
    logout: (refreshToken?: string) => apiPost('/auth/logout', { refreshToken }),
  },

  dashboard: {
    summary: () => apiGet<DashboardSummary>('/admin/dashboard'),
    series: (days = 14) => apiGet<DailySeriesPoint[]>('/admin/dashboard/series', { days }),
    assets: (days = 7) => apiGet<AssetVolumeRow[]>('/admin/dashboard/assets', { days }),
  },

  users: {
    list: (params: { page?: number; limit?: number; search?: string; role?: string; status?: string }) =>
      apiGet<Paginated<AdminUser>>('/admin/users', params),
    detail: (id: string) => apiGet<UserDetail>('/admin/users/' + id),
    update: (id: string, input: Record<string, unknown>) => apiPatch<AdminUser>('/admin/users/' + id, input),
    adjustWallet: (id: string, input: { amount: number; direction: 'CREDIT' | 'DEBIT'; reason: string }) =>
      apiPost('/admin/users/' + id + '/wallet/adjust', input),
    transactions: (id: string, params: { page?: number; limit?: number }) =>
      apiGet<Paginated<Record<string, unknown>>>('/admin/users/' + id + '/transactions', params),
  },

  assets: {
    list: () => apiGet<AdminAsset[]>('/admin/assets'),
    detail: (id: string) => apiGet<AdminAsset>('/admin/assets/' + id),
    create: (input: Record<string, unknown>) => apiPost<AdminAsset>('/admin/assets', input),
    update: (id: string, input: Record<string, unknown>) => apiPatch<AdminAsset>('/admin/assets/' + id, input),
    payouts: (id: string, history = false) =>
      apiGet<PayoutConfig[]>('/admin/assets/' + id + '/payouts', { history }),
    setPayout: (
      id: string,
      input: { expirySeconds: number; payoutPercent: number; minStake?: number | null; maxStake?: number | null; note?: string },
    ) => apiPost<PayoutConfig>('/admin/assets/' + id + '/payouts', input),
    deactivatePayout: (payoutId: string) => apiDelete<PayoutConfig>('/admin/assets/payouts/' + payoutId),
  },

  trades: {
    list: (params: Record<string, unknown>) => apiGet<Paginated<AdminTrade>>('/admin/binary/trades', params),
    detail: (id: string) => apiGet<AdminTrade>('/admin/binary/trades/' + id),
    cancel: (id: string, reason: string) => apiPost<AdminTrade>('/admin/binary/trades/' + id + '/cancel', { reason }),
  },

  risk: {
    exposure: () => apiGet<ExposureResponse>('/admin/exposure'),
    assessments: () => apiGet<RiskAssessment[]>('/admin/risk'),
    config: () => apiGet<{ config: RiskConfig; versions: RiskConfig[] }>('/admin/settings/risk'),
    updateConfig: (input: Record<string, unknown>) => http.put('/admin/settings/risk', input).then((r) => r.data.data as RiskConfig),
  },

  hedging: {
    monitor: () => apiGet<HedgeMonitorRow[]>('/admin/hedging'),
    orders: (params: Record<string, unknown>) => apiGet<Paginated<HedgeOrder>>('/admin/hedging/orders', params),
    cancel: (id: string) => apiPost<HedgeOrder>('/admin/hedging/orders/' + id + '/cancel'),
  },

  marketData: {
    overview: () => apiGet<MarketDataOverview>('/admin/market-data'),
    latency: () => apiGet<LatencyReportEntry[]>('/admin/metrics/latency'),
  },

  finance: {
    wallets: (params: { page?: number; limit?: number }) => apiGet<Paginated<AdminWalletRow>>('/admin/wallets', params),
    deposits: (params: Record<string, unknown>) => apiGet<Paginated<Transfer>>('/admin/deposits', params),
    withdrawals: (params: Record<string, unknown>) => apiGet<Paginated<Transfer>>('/admin/withdrawals', params),
    approveDeposit: (id: string, note?: string) => apiPost<Transfer>('/admin/deposits/' + id + '/approve', { note }),
    /** The payout UTR is required: it is the record that the money was sent. */
    approveWithdrawal: (id: string, input: { utr: string; note?: string }) =>
      apiPost<Transfer>('/admin/withdrawals/' + id + '/approve', input),
    reject: (id: string, note: string) => apiPost<Transfer>('/admin/transfers/' + id + '/reject', { note }),

    /** Authenticated URL of a customer's payment screenshot. */
    proofUrl: (transferId: string) => '/transfers/' + transferId + '/proof',

    paymentMethods: () => apiGet<PaymentMethod[]>('/admin/payment-methods'),
    savePaymentMethod: (input: Record<string, unknown>) =>
      http.put('/admin/payment-methods', input).then((r) => r.data.data as PaymentMethod),
    setPaymentMethodActive: (key: string, active: boolean) =>
      apiPost<PaymentMethod>('/admin/payment-methods/' + key + '/active', { active }),
  },

  reports: {
    pnl: (params: { from?: string; to?: string }) => apiGet<PlatformPnl>('/admin/reports/pnl', params),
    counters: () => apiGet<Record<string, number>>('/admin/reports/counters'),
  },

  audit: {
    list: (params: Record<string, unknown>) => apiGet<Paginated<AuditLog>>('/admin/audit-logs', params),
  },

  reconciliation: {
    list: (params: { page?: number; limit?: number }) => apiGet<Paginated<ReconciliationRun>>('/admin/reconciliation', params),
    run: () => apiPost<ReconciliationRun>('/admin/reconciliation/run'),
    resolve: (id: string, note: string) => apiPost('/admin/reconciliation/' + id + '/resolve', { note }),
  },

  /** USDT via BeeX. Every amount is integer micro-USDT. */
  usdt: {
    summary: () => apiGet<UsdtSummary>('/admin/usdt/summary'),
    payoutLimits: () => apiGet<UsdtPayoutLimits>('/admin/usdt/payout-limits'),

    deposits: (filters: UsdtDepositFilters) => apiList<UsdtDepositRow>('/admin/usdt/deposits', filters),
    deposit: (id: string) => apiGet<UsdtDepositDetail>(usdtPath('deposits', id)),
    /** Asks BeeX for the invoice's current state; `changed` says whether anything moved. */
    refreshDeposit: (id: string) => apiPost<UsdtRefreshResult<UsdtDepositView>>(usdtPath('deposits', id, 'refresh')),
    /** Credits what actually arrived on a closed underpaid invoice. Audited server side. */
    creditDeposit: (id: string, note: string) => apiPost<unknown>(usdtPath('deposits', id, 'credit'), { note }),

    withdrawals: (filters: UsdtWithdrawalFilters) => apiList<UsdtWithdrawalRow>('/admin/usdt/withdrawals', filters),
    withdrawal: (id: string) => apiGet<UsdtWithdrawalDetail>(usdtPath('withdrawals', id)),
    approveWithdrawal: (id: string, note?: string) =>
      apiPost<unknown>(usdtPath('withdrawals', id, 'approve'), note ? { note } : {}),
    /** The reserved amount goes back to the customer's USDT wallet. */
    rejectWithdrawal: (id: string, note: string) => apiPost<unknown>(usdtPath('withdrawals', id, 'reject'), { note }),
    refreshWithdrawal: (id: string) =>
      apiPost<UsdtRefreshResult<UsdtWithdrawalView>>(usdtPath('withdrawals', id, 'refresh')),

    webhookEvents: (filters: WebhookEventFilters) => apiList<WebhookEventView>('/admin/usdt/webhook-events', filters),
    reprocessWebhookEvent: (id: string) => apiPost<unknown>(usdtPath('webhook-events', id, 'reprocess')),

    networks: () => apiGet<UsdtNetworkView[]>('/admin/usdt/networks'),
    updateNetwork: (code: string, patch: UsdtNetworkPatch) => apiPatch<UsdtNetworkView>(usdtPath('networks', code), patch),
  },

  support: {
    /** The inbox, plus the desk-wide unread count carried in the response meta. */
    list: async (params: { page?: number; limit?: number; status?: string; search?: string; mine?: boolean }) => {
      const response = await http.get<{
        data: Paginated<SupportConversation>;
        meta?: { unreadTotal?: number };
      }>('/admin/support/conversations', { params });
      return { ...response.data.data, unreadTotal: response.data.meta?.unreadTotal ?? 0 };
    },
    thread: (id: string, params?: { before?: string; limit?: number }) =>
      apiGet<SupportThread>('/admin/support/conversations/' + id, params),
    send: (id: string, body: string) =>
      apiPost<{ message: SupportMessage; conversation: SupportConversation }>(
        '/admin/support/conversations/' + id + '/messages',
        { body },
      ),
    markRead: (id: string) => apiPost<SupportConversation>('/admin/support/conversations/' + id + '/read'),
    typing: (id: string) => apiPost<{ sent: boolean }>('/admin/support/conversations/' + id + '/typing'),
    update: (id: string, change: { status?: 'OPEN' | 'RESOLVED'; assignToMe?: boolean }) =>
      apiPatch<SupportConversation>('/admin/support/conversations/' + id, change),
    openForUser: (userId: string) =>
      apiPost<SupportConversation>('/admin/support/users/' + userId + '/conversation'),
  },
};
