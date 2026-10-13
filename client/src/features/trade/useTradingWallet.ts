import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useAccountStore, useActiveWallet } from '../../stores/accountStore';
import { useWalletStore } from '../../stores/walletStore';
import type { Wallet } from '../../types/api';

/**
 * The wallet a new trade will be paid from: the active account's.
 *
 * The account store is the source (it is what the header and Home read, and
 * what the socket keeps current). Until it has loaded, the accounts list is
 * read directly, and failing that the single wallet the older endpoint
 * returns - but only when it belongs to the active account. A live balance
 * must never stand in for a demo one, or the ticket would validate an amount
 * against the wrong money.
 */
export function useTradingWallet(): Wallet | null {
  const active = useAccountStore((s) => s.active);
  const fromStore = useActiveWallet();
  const legacy = useWalletStore((s) => s.wallet);

  const accountsQuery = useQuery({
    queryKey: ['wallet', 'accounts'],
    queryFn: api.wallet.accounts,
    staleTime: 10_000,
    enabled: !fromStore,
  });

  if (fromStore) return fromStore;
  const listed = accountsQuery.data?.find((wallet) => (wallet.accountType ?? 'LIVE') === active);
  if (listed) return listed;
  if (legacy && (legacy.accountType ?? 'LIVE') === active) return legacy;
  return null;
}
