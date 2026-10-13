import { useState } from 'react';
import { View } from 'react-native';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { ArrowDownToLine, ArrowUpFromLine } from 'lucide-react-native';
import { api } from '../../services/api';
import { useAccountStore } from '../../stores/accountStore';
import { openFunds } from '../account/funds';
import { AccountMarker } from '../../components/AccountSwitch';
import { Screen, ScreenHeader } from '../../components/Screen';
import { UsdtWalletLink, monoStyle } from '../usdt/parts';
import type { AccountType } from '../../types/api';
import { cn } from '../../utils/cn';
import { formatDateTime, formatMoney } from '../../utils/format';
import { Badge, Button, EmptyState, Panel, Segmented, Skeleton, Stat, Text } from '../../components/ui';

const CREDIT_TYPES = new Set(['DEPOSIT', 'TRADE_WIN', 'ADJUSTMENT_CREDIT', 'TRADE_REFUND', 'TRADE_CANCEL']);

const ACCOUNT_OPTIONS: Array<{ value: AccountType; label: string }> = [
  { value: 'LIVE', label: 'Live' },
  { value: 'DEMO', label: 'Demo' },
];

/**
 * Transactions: balances, the ledger and deposit / withdrawal requests.
 *
 * Reached from Account. The ledger can be read for either account - it opens
 * on the one in use - while the transfer list is always Live, the only
 * account real money moves through.
 *
 * A ledger page is at most 20 rows behind Previous / Next and the request
 * list at most 10, so both are drawn in place in the screen's scroll.
 */
export function WalletScreen() {
  const queryClient = useQueryClient();
  const [page, setPage] = useState(1);
  const activeAccount = useAccountStore((s) => s.active);
  const wallets = useAccountStore((s) => s.wallets);
  const [account, setAccount] = useState<AccountType>(activeAccount);
  const [refreshing, setRefreshing] = useState(false);

  const ledgerQuery = useQuery({
    queryKey: ['wallet', 'transactions', { accountType: account, page }],
    queryFn: () => api.wallet.transactions({ page, limit: 20, accountType: account }),
    placeholderData: (previous) => previous,
  });
  const transfersQuery = useQuery({
    queryKey: ['transfers'],
    queryFn: () => api.transfers.list({ limit: 10 }),
  });

  const wallet = wallets[account] ?? null;

  async function onRefresh() {
    setRefreshing(true);
    try {
      // `['wallet']` covers both balances and the ledger.
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ['wallet'] }),
        queryClient.invalidateQueries({ queryKey: ['transfers'] }),
      ]);
    } finally {
      setRefreshing(false);
    }
  }

  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader
        title="Transactions"
        fallbackHref="/account?section=funds"
        right={
          <Segmented
            options={ACCOUNT_OPTIONS}
            value={account}
            onChange={(type) => {
              setAccount(type);
              setPage(1);
            }}
            size="sm"
            stretch={false}
            accessibilityLabel="Account"
          />
        }
      />
      <Screen bottomInset onRefresh={onRefresh} refreshing={refreshing} contentClassName="gap-4">
        <View className="gap-3">
          <Stat
            label="Total balance"
            value={wallet ? formatMoney(wallet.balance, wallet.currency) : <Skeleton className="h-6 w-24" />}
          />
          <Stat
            label="Available"
            value={wallet ? formatMoney(wallet.availableBalance, wallet.currency) : <Skeleton className="h-6 w-24" />}
            tone="accent"
          />
          <Stat
            label="Locked"
            value={wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : <Skeleton className="h-6 w-24" />}
            hint="Reserved by open trades and pending withdrawals"
          />
        </View>

        <View className="flex-row flex-wrap gap-2">
          <Button icon={ArrowDownToLine} onPress={() => openFunds('DEPOSIT')}>
            Deposit
          </Button>
          <Button variant="ghost" icon={ArrowUpFromLine} onPress={() => openFunds('WITHDRAWAL')}>
            Withdraw
          </Button>
        </View>

        {/* USDT has a wallet and a history of its own; this screen is INR. */}
        <UsdtWalletLink />

        <Panel className="overflow-hidden">
          <View className="flex-row items-center gap-2 border-b border-surface-800 px-4 py-2.5">
            <Text className="text-[11px] font-semibold uppercase tracking-wider text-content-400">Ledger</Text>
            <AccountMarker account={account} />
          </View>

          {ledgerQuery.isLoading ? (
            <View className="gap-2 p-3">
              {Array.from({ length: 8 }).map((_, index) => (
                <Skeleton key={index} className="h-11 w-full" />
              ))}
            </View>
          ) : (ledgerQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title="No transactions yet" />
          ) : (
            <View>
              {ledgerQuery.data!.items.map((row, index) => {
                const credit = CREDIT_TYPES.has(row.type);
                const neutral = row.movement === 'LOCK' || row.movement === 'UNLOCK';
                return (
                  <View
                    key={row._id}
                    className={cn(
                      'flex-row items-center gap-3 px-4 py-2.5',
                      index > 0 && 'border-t border-surface-800/70',
                    )}
                  >
                    <View className="min-w-0 flex-1">
                      <Text className="text-sm font-medium text-content-100">{row.type.replace(/_/g, ' ')}</Text>
                      <Text numberOfLines={1} className="text-[11px] text-content-400">
                        {row.description ?? formatDateTime(row.createdAt)}
                      </Text>
                    </View>
                    <View className="items-end">
                      <Text
                        className={cn(
                          'text-sm font-semibold tnum',
                          neutral ? 'text-content-300' : credit ? 'text-up' : 'text-down',
                        )}
                      >
                        {neutral ? '' : credit ? '+' : '-'}
                        {formatMoney(row.amount)}
                      </Text>
                      <Text className="text-[11px] text-content-400 tnum">bal {formatMoney(row.balanceAfter)}</Text>
                    </View>
                  </View>
                );
              })}
            </View>
          )}

          {ledgerQuery.data && ledgerQuery.data.totalPages > 1 ? (
            <View className="flex-row items-center justify-center gap-2 border-t border-surface-800 px-4 py-3">
              <Button variant="ghost" disabled={page <= 1} onPress={() => setPage((p) => p - 1)}>
                Previous
              </Button>
              <Text className="text-xs text-content-400 tnum">
                {ledgerQuery.data.page} / {ledgerQuery.data.totalPages}
              </Text>
              <Button
                variant="ghost"
                disabled={page >= ledgerQuery.data.totalPages}
                onPress={() => setPage((p) => p + 1)}
              >
                Next
              </Button>
            </View>
          ) : null}
        </Panel>

        <Panel className="overflow-hidden">
          <View className="flex-row items-center gap-2 border-b border-surface-800 px-4 py-2.5">
            <Text className="text-[11px] font-semibold uppercase tracking-wider text-content-400">
              Deposits and withdrawals
            </Text>
            <AccountMarker account="LIVE" />
          </View>
          {(transfersQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title="No requests yet" />
          ) : (
            <View>
              {transfersQuery.data!.items.map((transfer, index) => (
                <View key={transfer.id} className={cn('px-4 py-2.5', index > 0 && 'border-t border-surface-800/70')}>
                  <View className="flex-row items-center gap-3">
                    <Badge tone={transfer.kind === 'DEPOSIT' ? 'accent' : 'neutral'}>{transfer.kind}</Badge>
                    <View className="min-w-0 flex-1">
                      <Text className="text-sm font-semibold text-content-100 tnum">
                        {formatMoney(transfer.amount)}
                      </Text>
                      <Text className="text-[11px] text-content-400">
                        {formatDateTime(transfer.createdAt)}
                        {transfer.method ? ' · ' + transfer.method : ''}
                      </Text>
                    </View>
                    <Badge
                      tone={
                        transfer.status === 'COMPLETED' ? 'up' : transfer.status === 'REJECTED' ? 'down' : 'warn'
                      }
                    >
                      {transfer.status === 'PENDING' ? 'UNDER REVIEW' : transfer.status}
                    </Badge>
                  </View>

                  {transfer.utr ? (
                    <Text numberOfLines={1} className="mt-1 text-[11px] text-content-400" style={monoStyle}>
                      {transfer.kind === 'DEPOSIT' ? 'UTR ' : 'Paid, UTR '}
                      {transfer.utr}
                      {transfer.proof ? ' · screenshot attached' : ''}
                    </Text>
                  ) : transfer.proof ? (
                    <Text className="mt-1 text-[11px] text-content-400">Screenshot attached</Text>
                  ) : null}

                  {/* A rejection is only useful if the customer is told why. */}
                  {transfer.status === 'REJECTED' && transfer.reviewNote ? (
                    <View className="mt-1.5 rounded border border-down/30 bg-down/10 px-2 py-1">
                      <Text className="text-[11px] text-down">{transfer.reviewNote}</Text>
                    </View>
                  ) : null}
                </View>
              ))}
            </View>
          )}
        </Panel>
      </Screen>
    </View>
  );
}
