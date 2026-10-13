import { useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import { ArrowDownToLine, ArrowRightLeft, ArrowUpFromLine, Lock } from 'lucide-react-native';
import { Button, EmptyState, Icon, Panel, Segmented, Skeleton, Text, type IconGlyphProps } from '../../components/ui';
import { cn } from '../../utils/cn';
import { formatDateTime } from '../../utils/format';
import { formatUsdt } from '../../utils/usdt';
import type { Paginated, UsdtDepositView, UsdtNetworkView, UsdtTransaction, UsdtWithdrawalView } from '../../types/api';
import {
  depositStatusTone,
  depositStatusWord,
  isDepositOpen,
  isWithdrawalReturned,
  ledgerLabel,
  ledgerMovementWord,
  ledgerSign,
  networkLabel,
  usdtErrorMessage,
  withdrawalStatusTone,
  withdrawalStatusWord,
  type BadgeTone,
} from './model';
import { useUsdtDeposits, useUsdtTransactions, useUsdtWithdrawals } from './queries';
import { NetworkChip, StatusBadge } from './parts';

export type UsdtDetail =
  | { kind: 'deposit'; item: UsdtDepositView }
  | { kind: 'withdrawal'; item: UsdtWithdrawalView }
  | { kind: 'ledger'; item: UsdtTransaction };

type Tab = 'deposits' | 'withdrawals' | 'ledger';

const TABS: Array<{ value: Tab; label: string }> = [
  { value: 'deposits', label: 'Deposits' },
  { value: 'withdrawals', label: 'Withdrawals' },
  { value: 'ledger', label: 'All activity' },
];

/**
 * USDT history: deposit requests, withdrawal requests and the ledger, one
 * tab each. Requests have a life (awaiting payment, under review...) that
 * the ledger does not show until money actually moves, so they are listed as
 * themselves rather than folded into it. A row opens its full record.
 *
 * A page holds at most PAGE_SIZE rows behind Previous / Next, so the rows are
 * drawn in place inside the screen's scroll rather than in a list of their own.
 */
export function UsdtHistory({
  networks,
  onOpen,
}: {
  networks: UsdtNetworkView[];
  onOpen: (detail: UsdtDetail) => void;
}) {
  const [tab, setTab] = useState<Tab>('deposits');
  const [pages, setPages] = useState<Record<Tab, number>>({ deposits: 1, withdrawals: 1, ledger: 1 });
  const page = pages[tab];
  const setPage = (next: number) => setPages((current) => ({ ...current, [tab]: next }));

  const deposits = useUsdtDeposits(pages.deposits, tab === 'deposits');
  const withdrawals = useUsdtWithdrawals(pages.withdrawals, tab === 'withdrawals');
  const ledger = useUsdtTransactions(pages.ledger, tab === 'ledger');

  return (
    <Panel className="overflow-hidden">
      <View className="gap-2 border-b border-surface-800 px-4 py-2.5">
        <Text accessibilityRole="header" className="text-[11px] font-semibold uppercase tracking-wider text-content-400">
          History
        </Text>
        <Segmented options={TABS} value={tab} onChange={setTab} size="sm" accessibilityLabel="USDT history" />
      </View>

      <View>
        {tab === 'deposits' ? (
          <RowList
            query={deposits}
            empty="No USDT deposits yet"
            keyOf={(item) => item.id}
            render={(item) => (
              <DepositRow deposit={item} networks={networks} onOpen={() => onOpen({ kind: 'deposit', item })} />
            )}
          />
        ) : tab === 'withdrawals' ? (
          <RowList
            query={withdrawals}
            empty="No USDT withdrawals yet"
            keyOf={(item) => item.id}
            render={(item) => (
              <WithdrawalRow
                withdrawal={item}
                networks={networks}
                onOpen={() => onOpen({ kind: 'withdrawal', item })}
              />
            )}
          />
        ) : (
          <RowList
            query={ledger}
            empty="No USDT activity yet"
            keyOf={(item) => item._id}
            render={(item) => (
              <LedgerRow row={item} networks={networks} onOpen={() => onOpen({ kind: 'ledger', item })} />
            )}
          />
        )}
      </View>

      <Pager
        data={tab === 'deposits' ? deposits.data : tab === 'withdrawals' ? withdrawals.data : ledger.data}
        page={page}
        onPage={setPage}
      />
    </Panel>
  );
}

function RowList<T>({
  query,
  empty,
  keyOf,
  render,
}: {
  query: { data?: Paginated<T>; isLoading: boolean; isError: boolean; error: unknown; refetch: () => unknown };
  empty: string;
  keyOf: (item: T) => string;
  render: (item: T) => ReactNode;
}) {
  if (query.isLoading) {
    return (
      <View className="gap-2 p-3">
        {Array.from({ length: 5 }).map((_, index) => (
          <Skeleton key={index} className="h-12 w-full" />
        ))}
      </View>
    );
  }
  if (query.isError && !query.data) {
    return (
      <View className="items-center gap-2 px-6 py-10">
        <Text className="text-center text-sm text-content-300">{usdtErrorMessage(query.error)}</Text>
        <Button variant="ghost" size="sm" onPress={() => void query.refetch()}>
          Try again
        </Button>
      </View>
    );
  }
  const items = query.data?.items ?? [];
  if (items.length === 0) return <EmptyState title={empty} />;
  return (
    <View>
      {items.map((item, index) => (
        <View key={keyOf(item)} className={cn(index > 0 && 'border-t border-surface-800/70')}>
          {render(item)}
        </View>
      ))}
    </View>
  );
}

function Pager({
  data,
  page,
  onPage,
}: {
  data?: Pick<Paginated<unknown>, 'page' | 'totalPages'>;
  page: number;
  onPage: (page: number) => void;
}) {
  if (!data || data.totalPages <= 1) return null;
  return (
    <View className="flex-row items-center justify-center gap-2 border-t border-surface-800 px-4 py-3">
      <Button variant="ghost" disabled={page <= 1} onPress={() => onPage(page - 1)}>
        Previous
      </Button>
      <Text className="text-xs text-content-400 tnum">
        {data.page} / {data.totalPages}
      </Text>
      <Button variant="ghost" disabled={page >= data.totalPages} onPress={() => onPage(page + 1)}>
        Next
      </Button>
    </View>
  );
}

/** One history line: what, on which network, when - and the signed amount with its status in words. */
function Row({
  icon,
  iconTone,
  title,
  network,
  date,
  amount,
  amountTone,
  status,
  statusTone,
  onOpen,
}: {
  icon: ComponentType<IconGlyphProps>;
  iconTone: 'up' | 'down' | 'neutral';
  title: string;
  network: string | null;
  date: string;
  amount: string;
  amountTone: 'up' | 'down' | 'muted' | 'neutral';
  status: string;
  statusTone: BadgeTone;
  onOpen: () => void;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={[title, network, amount, status, formatDateTime(date)].filter(Boolean).join(', ')}
      onPress={onOpen}
      className="flex-row items-center gap-3 px-4 py-2.5 active:bg-surface-850"
    >
      <View
        className={cn(
          'h-8 w-8 shrink-0 items-center justify-center rounded-lg',
          iconTone === 'up' ? 'bg-up/15' : iconTone === 'down' ? 'bg-down/10' : 'bg-surface-800',
        )}
      >
        <Icon
          as={icon}
          size={16}
          className={iconTone === 'up' ? 'text-up' : iconTone === 'down' ? 'text-down' : 'text-content-300'}
        />
      </View>
      <View className="min-w-0 flex-1">
        <View className="flex-row items-center gap-1.5">
          <Text numberOfLines={1} className="shrink text-sm font-medium text-content-100">
            {title}
          </Text>
          {network ? <NetworkChip label={network} /> : null}
        </View>
        <Text numberOfLines={1} className="text-[11px] text-content-400">
          {formatDateTime(date)}
        </Text>
      </View>
      <View className="shrink-0 items-end gap-0.5">
        <Text
          className={cn(
            'text-sm font-semibold tnum',
            amountTone === 'up'
              ? 'text-up'
              : amountTone === 'down'
                ? 'text-down'
                : amountTone === 'muted'
                  ? 'text-content-400'
                  : 'text-content-200',
          )}
        >
          {amount}
        </Text>
        <StatusBadge tone={statusTone}>{status}</StatusBadge>
      </View>
    </Pressable>
  );
}

function DepositRow({
  deposit,
  networks,
  onOpen,
}: {
  deposit: UsdtDepositView;
  networks: UsdtNetworkView[];
  onOpen: () => void;
}) {
  // What actually landed once credited; until then, what was asked for.
  const amount = deposit.credited ? deposit.amountCredited : deposit.amountExpected;
  const settledWithout = !deposit.credited && !isDepositOpen(deposit.status);
  return (
    <Row
      icon={ArrowDownToLine}
      iconTone="up"
      title="Deposit"
      network={networkLabel(deposit.network, networks)}
      date={deposit.createdAt}
      amount={formatUsdt(amount, { trim: true, signed: true })}
      amountTone={deposit.credited ? 'up' : settledWithout ? 'muted' : 'neutral'}
      status={depositStatusWord(deposit.status)}
      statusTone={depositStatusTone(deposit.status)}
      onOpen={onOpen}
    />
  );
}

function WithdrawalRow({
  withdrawal,
  networks,
  onOpen,
}: {
  withdrawal: UsdtWithdrawalView;
  networks: UsdtNetworkView[];
  onOpen: () => void;
}) {
  const returned = isWithdrawalReturned(withdrawal.status);
  return (
    <Row
      icon={ArrowUpFromLine}
      iconTone="down"
      title="Withdrawal"
      network={networkLabel(withdrawal.network, networks)}
      date={withdrawal.createdAt}
      amount={formatUsdt(-withdrawal.amount, { trim: true })}
      amountTone={returned ? 'muted' : 'down'}
      status={withdrawalStatusWord(withdrawal.status)}
      statusTone={withdrawalStatusTone(withdrawal.status)}
      onOpen={onOpen}
    />
  );
}

function LedgerRow({
  row,
  networks,
  onOpen,
}: {
  row: UsdtTransaction;
  networks: UsdtNetworkView[];
  onOpen: () => void;
}) {
  const sign = ledgerSign(row);
  const transfer = /TRANSFER/.test(row.type);
  const icon = sign === 0 ? Lock : transfer ? ArrowRightLeft : sign > 0 ? ArrowDownToLine : ArrowUpFromLine;
  return (
    <Row
      icon={icon}
      iconTone={sign > 0 ? 'up' : sign < 0 && !transfer ? 'down' : 'neutral'}
      title={ledgerLabel(row.type)}
      network={row.network ? networkLabel(row.network, networks) : null}
      date={row.createdAt}
      amount={formatUsdt(sign < 0 ? -Math.abs(row.amount) : Math.abs(row.amount), { trim: true, signed: sign > 0 })}
      amountTone={sign > 0 ? 'up' : sign < 0 ? 'down' : 'neutral'}
      status={ledgerMovementWord(row.movement)}
      statusTone={sign > 0 ? 'up' : 'neutral'}
      onOpen={onOpen}
    />
  );
}
