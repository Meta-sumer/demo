import { useState, type ReactNode } from 'react';
import clsx from 'clsx';
import { ArrowDownToLine, ArrowRightLeft, ArrowUpFromLine, Lock, type LucideIcon } from 'lucide-react';
import { EmptyState, Panel, Skeleton } from '../../components/ui';
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

const TABS: Array<{ id: Tab; label: string }> = [
  { id: 'deposits', label: 'Deposits' },
  { id: 'withdrawals', label: 'Withdrawals' },
  { id: 'ledger', label: 'All activity' },
];

/**
 * USDT history: deposit requests, withdrawal requests and the ledger, one
 * tab each. Requests have a life (awaiting payment, under review...) that
 * the ledger does not show until money actually moves, so they are listed as
 * themselves rather than folded into it. A row opens its full record.
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
      <div className="flex items-center gap-3 border-b border-surface-800 px-4 py-2">
        <h2 className="text-[11px] font-semibold uppercase tracking-wider text-content-400">History</h2>
        <div role="tablist" aria-label="USDT history" className="ml-auto flex gap-1 rounded-lg bg-surface-850 p-1">
          {TABS.map((item) => (
            <button
              key={item.id}
              type="button"
              role="tab"
              id={'usdt-tab-' + item.id}
              aria-selected={tab === item.id}
              aria-controls="usdt-history-panel"
              onClick={() => setTab(item.id)}
              className={clsx(
                'rounded-md px-2.5 py-1 text-xs font-semibold transition',
                tab === item.id ? 'bg-surface-700 text-content-100' : 'text-content-400 hover:text-content-100',
              )}
            >
              {item.label}
            </button>
          ))}
        </div>
      </div>

      <div id="usdt-history-panel" role="tabpanel" aria-labelledby={'usdt-tab-' + tab}>
        {tab === 'deposits' ? (
          <RowList
            query={deposits}
            empty="No USDT deposits yet"
            render={(item) => (
              <DepositRow
                key={item.id}
                deposit={item}
                networks={networks}
                onOpen={() => onOpen({ kind: 'deposit', item })}
              />
            )}
          />
        ) : tab === 'withdrawals' ? (
          <RowList
            query={withdrawals}
            empty="No USDT withdrawals yet"
            render={(item) => (
              <WithdrawalRow
                key={item.id}
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
            render={(item) => (
              <LedgerRow
                key={item._id}
                row={item}
                networks={networks}
                onOpen={() => onOpen({ kind: 'ledger', item })}
              />
            )}
          />
        )}
      </div>

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
  render,
}: {
  query: { data?: Paginated<T>; isLoading: boolean; isError: boolean; error: unknown; refetch: () => unknown };
  empty: string;
  render: (item: T) => ReactNode;
}) {
  if (query.isLoading) {
    return (
      <div className="space-y-2 p-3">
        {Array.from({ length: 5 }).map((_, index) => (
          <Skeleton key={index} className="h-12 w-full" />
        ))}
      </div>
    );
  }
  if (query.isError && !query.data) {
    return (
      <div className="flex flex-col items-center gap-2 px-6 py-10 text-center">
        <p className="text-sm text-content-300">{usdtErrorMessage(query.error)}</p>
        <button type="button" className="btn-ghost px-3 py-1.5 text-xs" onClick={() => void query.refetch()}>
          Try again
        </button>
      </div>
    );
  }
  const items = query.data?.items ?? [];
  if (items.length === 0) return <EmptyState title={empty} />;
  return <ul className="divide-y divide-surface-800/70">{items.map(render)}</ul>;
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
    <div className="flex items-center justify-center gap-2 border-t border-surface-800 px-4 py-3">
      <button type="button" className="btn-ghost" disabled={page <= 1} onClick={() => onPage(page - 1)}>
        Previous
      </button>
      <span className="text-xs text-content-400 tnum">
        {data.page} / {data.totalPages}
      </span>
      <button type="button" className="btn-ghost" disabled={page >= data.totalPages} onClick={() => onPage(page + 1)}>
        Next
      </button>
    </div>
  );
}

/** One history line: what, on which network, when - and the signed amount with its status in words. */
function Row({
  icon: Icon,
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
  icon: LucideIcon;
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
    <li>
      <button
        type="button"
        onClick={onOpen}
        className="flex w-full items-center gap-3 px-4 py-2.5 text-left transition hover:bg-surface-850 focus-visible:bg-surface-850"
      >
        <span
          className={clsx(
            'grid h-8 w-8 shrink-0 place-items-center rounded-lg',
            iconTone === 'up'
              ? 'bg-up/15 text-up'
              : iconTone === 'down'
                ? 'bg-down/10 text-down'
                : 'bg-surface-800 text-content-300',
          )}
        >
          <Icon className="h-4 w-4" aria-hidden />
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="truncate text-sm font-medium text-content-100">{title}</span>
            {network ? <NetworkChip label={network} /> : null}
          </span>
          <span className="block truncate text-[11px] text-content-400">{formatDateTime(date)}</span>
        </span>
        <span className="flex shrink-0 flex-col items-end gap-0.5">
          <span
            className={clsx(
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
          </span>
          <StatusBadge tone={statusTone}>{status}</StatusBadge>
        </span>
      </button>
    </li>
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
