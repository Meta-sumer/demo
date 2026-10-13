/**
 * USDT (BeeX) admin contracts.
 *
 * Every money field is an integer number of micro-USDT (1 USDT = 1,000,000).
 * Nothing here is ever a decimal: conversion to text happens only in
 * utils/usdt.ts, so a display value can never leak back into a request.
 */

export type UsdtNetworkCode = 'USDTTRC20' | 'USDTBSC';
export type UsdtNetworkLabel = 'TRC20' | 'BSC';
export type UsdtMode = 'live' | 'test';

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

export type UsdtWithdrawalStatus =
  | 'pending_review'
  | 'requested'
  | 'awaiting_approval'
  | 'sending'
  | 'completed'
  | 'failed'
  | 'rejected';

export type WebhookProcessingStatus = 'processed' | 'duplicate' | 'ignored' | 'failed' | 'pending';

export interface UsdtNetworkView {
  code: UsdtNetworkCode;
  label: UsdtNetworkLabel;
  name: string;
  chain: string;
  decimals: number;
  depositEnabled: boolean;
  withdrawEnabled: boolean;
  /** micro-USDT */
  minDeposit: number;
  addressExample: string;
}

export interface UsdtUserSummary {
  id: string;
  email: string;
  username: string;
}

export interface UsdtDepositView {
  id: string;
  orderId: string;
  invoiceId: string | null;
  reference: string | null;
  network: string;
  mode: UsdtMode | string;
  status: UsdtDepositStatus | string;
  amountExpected: number;
  amountReceived: number;
  amountCredited: number;
  feeAmount: number;
  netAmount: number;
  payAddress: string | null;
  paymentUrl: string | null;
  expiresAt: string | null;
  credited: boolean;
  creditedAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface UsdtWithdrawalView {
  id: string;
  reference: string;
  providerReference: string | null;
  network: string;
  address: string;
  amount: number;
  feeAmount: number;
  netAmount: number;
  status: UsdtWithdrawalStatus | string;
  txHash: string | null;
  failureReason: string | null;
  reviewNote: string | null;
  createdAt: string;
  updatedAt: string;
  completedAt: string | null;
}

export interface WebhookEventView {
  id: string;
  eventKey: string;
  event: string;
  reference: string | null;
  orderId: string | null;
  clientReference: string | null;
  /** The provider's status carried by the event, e.g. "paid". */
  status: string | null;
  mode: UsdtMode | string | null;
  signatureTimestamp: string | number | null;
  receivedAt: string;
  processedAt: string | null;
  processingStatus: WebhookProcessingStatus | string;
  error: string | null;
  attempts: number;
  payload: unknown;
}

/** A wallet ledger row (the shared wallet transaction shape) with the USDT extras. */
export interface UsdtLedgerRow {
  _id?: string;
  id?: string;
  walletId?: string;
  userId?: string;
  accountType?: string;
  type: string;
  movement: string;
  amount: number;
  currency: string;
  balanceBefore?: number;
  balanceAfter?: number;
  lockedBefore?: number;
  lockedAfter?: number;
  referenceType?: string | null;
  referenceId?: string | null;
  description?: string | null;
  createdAt: string;
  network?: string | null;
  fee?: number | null;
}

export type UsdtDepositRow = UsdtDepositView & { user?: UsdtUserSummary | null };
export type UsdtWithdrawalRow = UsdtWithdrawalView & { user?: UsdtUserSummary | null };

export interface UsdtDepositDetail {
  deposit: UsdtDepositView;
  user: UsdtUserSummary | null;
  events: WebhookEventView[];
  ledger: UsdtLedgerRow[];
}

export interface UsdtWithdrawalDetail {
  withdrawal: UsdtWithdrawalView;
  user: UsdtUserSummary | null;
  events: WebhookEventView[];
  ledger: UsdtLedgerRow[];
}

export interface UsdtRefreshResult<T> {
  changed: boolean;
  note?: string | null;
  deposit?: T;
  withdrawal?: T;
}

export interface UsdtSummary {
  deposits: { count: number; pending: number; creditedAmount: number; failed: number };
  withdrawals: { count: number; pending: number; completedAmount: number; failed: number };
  byNetwork: Array<{
    network: string;
    depositAmount: number;
    withdrawalAmount: number;
    deposits: number;
    withdrawals: number;
  }>;
  walletTotals: { balance: number; locked: number };
  provider: {
    name: string;
    mode: UsdtMode | string;
    configured: boolean;
    /** Shape is the provider's; rendered generically (map or list of currency/amount). */
    balances?: unknown;
  };
}

export interface UsdtPayoutLimits {
  minWithdrawal: number;
  maxWithdrawal: number;
  dailyWithdrawalCap: number;
  reviewedAbove: number;
  /** A percentage, e.g. 1.5 means 1.5 %. */
  feePercent: number;
  feeFixed: number;
  currencies: string[];
  fetchedAt: string | null;
}

export interface UsdtNetworkPatch {
  depositEnabled?: boolean;
  withdrawEnabled?: boolean;
  minDeposit?: number;
}

export interface UsdtDepositFilters {
  status?: string;
  network?: string;
  userId?: string;
  q?: string;
  reference?: string;
  orderId?: string;
  from?: string;
  to?: string;
  page?: number;
  limit?: number;
}

export interface UsdtWithdrawalFilters {
  status?: string;
  network?: string;
  userId?: string;
  q?: string;
  reference?: string;
  address?: string;
  from?: string;
  to?: string;
  page?: number;
  limit?: number;
}

export interface WebhookEventFilters {
  event?: string;
  reference?: string;
  processingStatus?: string;
  page?: number;
  limit?: number;
}
