import { useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { useAccountStore } from '../../stores/accountStore';
import { useTradeStore } from '../../stores/tradeStore';
import { accountOf } from './ticketModel';
import type { AccountType, Trade } from '../../types/api';

/**
 * Running trades, for the navigation's count (reference record 15, Deriv's
 * "Positions 1").
 *
 * The count is on every screen, so the open trades cannot wait for the trade
 * screen to load them: a trader who signs in on Home with a trade still
 * running must see it counted. This loads them once from anywhere, under the
 * same query key the trade screen uses - one request and one cache entry - and
 * hands them to the trade store. From then on the websocket keeps the store
 * current (created, settled); the slow refetch is only a safety net for an
 * event missed while the connection was down.
 */
const REFETCH_MS = 30_000;

function useOpenTradesSync(): void {
  const signedIn = useAuthStore((s) => s.status === 'authenticated');
  const query = useQuery({
    queryKey: ['trades', 'open'],
    queryFn: api.trades.open,
    enabled: signedIn,
    // Refetched on mount, and only an answer fetched since then is written:
    // after signing out and in again the cache can still hold the previous
    // session's list, and that must never reach the count.
    staleTime: 0,
    refetchInterval: REFETCH_MS,
  });
  const fresh = query.isFetchedAfterMount;

  useEffect(() => {
    if (fresh && query.data) useTradeStore.getState().setOpenTrades(query.data);
  }, [fresh, query.data]);
}

/** Running trades on one account. A number, so the selector is stable. */
export function countOpenFor(trades: readonly Trade[], account: AccountType): number {
  let count = 0;
  for (const trade of trades) if (accountOf(trade) === account) count += 1;
  return count;
}

/** How many trades are running on the account being traded. */
export function useActiveTradeCount(): number {
  useOpenTradesSync();
  const account = useAccountStore((s) => s.active);
  return useTradeStore((s) => countOpenFor(s.openTrades, account));
}
