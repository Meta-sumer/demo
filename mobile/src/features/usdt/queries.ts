import { useRef } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import type { UsdtDepositView, UsdtWithdrawalView } from '../../types/api';
import { depositNeedsPolling, isWithdrawalFinal } from './model';

/**
 * USDT queries.
 *
 * Everything lives under one key, so a websocket `usdt.updated` (or a balance
 * change) refreshes the wallet, the lists and any open record with a single
 * invalidation.
 */
export const USDT_KEY = ['usdt'] as const;

export const usdtKeys = {
  overview: ['usdt', 'overview'] as const,
  deposits: (page: number) => ['usdt', 'deposits', { page }] as const,
  deposit: (id: string) => ['usdt', 'deposit', id] as const,
  withdrawals: (page: number) => ['usdt', 'withdrawals', { page }] as const,
  withdrawal: (id: string) => ['usdt', 'withdrawal', id] as const,
  transactions: (page: number) => ['usdt', 'transactions', { page }] as const,
  quote: (network: string, amount: number) => ['usdt', 'quote', network, amount] as const,
};

/** How often a record that is still moving is asked about again. The websocket usually gets there first. */
const RECORD_POLL_MS = 5_000;
const LIST_POLL_MS = 12_000;
export const PAGE_SIZE = 15;

export function useUsdtOverview(options: { enabled?: boolean } = {}) {
  return useQuery({
    queryKey: usdtKeys.overview,
    queryFn: api.usdt.overview,
    enabled: options.enabled ?? true,
    staleTime: 15_000,
  });
}

/** One deposit, re-read while money may still arrive or the credit has not landed. */
export function useUsdtDeposit(id: string | null, initial?: UsdtDepositView) {
  return useQuery({
    queryKey: usdtKeys.deposit(id ?? ''),
    queryFn: () => api.usdt.deposit(id!),
    enabled: !!id,
    initialData: initial,
    // The row the list handed over is fresh enough to draw; fetch behind it.
    initialDataUpdatedAt: 0,
    refetchInterval: (query) => {
      const deposit = query.state.data;
      return deposit && depositNeedsPolling(deposit) ? RECORD_POLL_MS : false;
    },
  });
}

/** One withdrawal, re-read until it is completed, failed or rejected. */
export function useUsdtWithdrawal(id: string | null, initial?: UsdtWithdrawalView) {
  return useQuery({
    queryKey: usdtKeys.withdrawal(id ?? ''),
    queryFn: () => api.usdt.withdrawal(id!),
    enabled: !!id,
    initialData: initial,
    initialDataUpdatedAt: 0,
    refetchInterval: (query) => {
      const withdrawal = query.state.data;
      return withdrawal && !isWithdrawalFinal(withdrawal.status) ? RECORD_POLL_MS : false;
    },
  });
}

export function useUsdtDeposits(page: number, enabled = true) {
  return useQuery({
    queryKey: usdtKeys.deposits(page),
    queryFn: () => api.usdt.deposits({ page, limit: PAGE_SIZE }),
    enabled,
    placeholderData: (previous) => previous,
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some(depositNeedsPolling) ? LIST_POLL_MS : false,
  });
}

export function useUsdtWithdrawals(page: number, enabled = true) {
  return useQuery({
    queryKey: usdtKeys.withdrawals(page),
    queryFn: () => api.usdt.withdrawals({ page, limit: PAGE_SIZE }),
    enabled,
    placeholderData: (previous) => previous,
    refetchInterval: (query) =>
      (query.state.data?.items ?? []).some((w) => !isWithdrawalFinal(w.status)) ? LIST_POLL_MS : false,
  });
}

export function useUsdtTransactions(page: number, enabled = true) {
  return useQuery({
    queryKey: usdtKeys.transactions(page),
    queryFn: () => api.usdt.transactions({ page, limit: PAGE_SIZE }),
    enabled,
    placeholderData: (previous) => previous,
  });
}

/**
 * Lets a money-moving request go out once at a time.
 *
 * `isPending` reaches the button a tick late - the mutation cache notifies
 * asynchronously - so the second click of a double click can land before the
 * button is disabled. This closes that gap synchronously. The idempotency key
 * would make a second request harmless; this keeps it from being sent at all.
 */
export function useSendOnce<TBody>(mutate: (body: TBody, options: { onSettled: () => void }) => void) {
  const busy = useRef(false);
  return (body: TBody) => {
    if (busy.current) return;
    busy.current = true;
    mutate(body, {
      onSettled: () => {
        busy.current = false;
      },
    });
  };
}
