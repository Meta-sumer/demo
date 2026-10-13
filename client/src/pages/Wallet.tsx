import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { Link } from 'react-router-dom';
import { ArrowDownToLine, ArrowLeft, ArrowUpFromLine } from 'lucide-react';
import { api } from '../services/api';
import { useAccountStore } from '../stores/accountStore';
import { openFunds } from '../features/account/funds';
import { AccountMarker } from '../components/AccountSwitch';
import { UsdtWalletLink } from '../features/usdt/parts';
import type { AccountType } from '../types/api';
import { formatDateTime, formatMoney } from '../utils/format';
import { Badge, EmptyState, Panel, Skeleton, Stat } from '../components/ui';

const CREDIT_TYPES = new Set(['DEPOSIT', 'TRADE_WIN', 'ADJUSTMENT_CREDIT', 'TRADE_REFUND', 'TRADE_CANCEL']);

/**
 * Transactions: balances, the ledger and deposit / withdrawal requests.
 *
 * Reached from Account. The ledger can be read for either account - it opens
 * on the one in use - while the transfer list is always Live, the only
 * account real money moves through.
 */
export function WalletPage() {
  const [page, setPage] = useState(1);
  const activeAccount = useAccountStore((s) => s.active);
  const wallets = useAccountStore((s) => s.wallets);
  const [account, setAccount] = useState<AccountType>(activeAccount);

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

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <Link to="/account#funds" className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100">
          <ArrowLeft className="h-4 w-4" />
          Account
        </Link>
        <h1 className="text-lg font-bold text-content-100">Transactions</h1>
        <div role="radiogroup" aria-label="Account" className="ml-auto flex gap-1 rounded-lg bg-surface-850 p-1">
          {(['LIVE', 'DEMO'] as AccountType[]).map((type) => (
            <button
              key={type}
              type="button"
              role="radio"
              aria-checked={account === type}
              onClick={() => {
                setAccount(type);
                setPage(1);
              }}
              className={clsx(
                'rounded-md px-3 py-1 text-xs font-semibold transition',
                account === type ? 'bg-surface-700 text-content-100' : 'text-content-400 hover:text-content-100',
              )}
            >
              {type === 'LIVE' ? 'Live' : 'Demo'}
            </button>
          ))}
        </div>
      </div>

      <div className="grid gap-3 sm:grid-cols-3">
        <Stat
          label="Total balance"
          value={wallet ? formatMoney(wallet.balance, wallet.currency) : <Skeleton className="h-6 w-24" />}
        />
        <Stat
          label="Available"
          value={
            wallet ? formatMoney(wallet.availableBalance, wallet.currency) : <Skeleton className="h-6 w-24" />
          }
          tone="accent"
        />
        <Stat
          label="Locked"
          value={wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : <Skeleton className="h-6 w-24" />}
          hint="Reserved by open trades and pending withdrawals"
        />
      </div>

      <div className="flex flex-wrap gap-2">
        <button type="button" onClick={() => openFunds('DEPOSIT')} className="btn-primary">
          <ArrowDownToLine className="h-4 w-4" />
          Deposit
        </button>
        <button type="button" onClick={() => openFunds('WITHDRAWAL')} className="btn-ghost">
          <ArrowUpFromLine className="h-4 w-4" />
          Withdraw
        </button>
      </div>

      {/* USDT has a wallet and a history of its own; this page is INR. */}
      <UsdtWalletLink />

      <div className="grid gap-3 lg:grid-cols-[minmax(0,1fr)_340px]">
        <Panel className="overflow-hidden">
          <div className="border-b border-surface-800 px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-content-400">
            <span className="flex items-center gap-2">Ledger <AccountMarker account={account} /></span>
          </div>

          {ledgerQuery.isLoading ? (
            <div className="space-y-2 p-3">
              {Array.from({ length: 8 }).map((_, index) => (
                <Skeleton key={index} className="h-11 w-full" />
              ))}
            </div>
          ) : (ledgerQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title="No transactions yet" />
          ) : (
            <ul className="divide-y divide-surface-800/70">
              {ledgerQuery.data!.items.map((row) => {
                const credit = CREDIT_TYPES.has(row.type);
                const neutral = row.movement === 'LOCK' || row.movement === 'UNLOCK';
                return (
                  <li key={row._id} className="flex items-center gap-3 px-4 py-2.5">
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-medium text-content-100">{row.type.replace(/_/g, ' ')}</div>
                      <div className="truncate text-[11px] text-content-400">
                        {row.description ?? formatDateTime(row.createdAt)}
                      </div>
                    </div>
                    <div className="text-right">
                      <div
                        className={clsx(
                          'text-sm font-semibold tnum',
                          neutral ? 'text-content-300' : credit ? 'text-up' : 'text-down',
                        )}
                      >
                        {neutral ? '' : credit ? '+' : '-'}
                        {formatMoney(row.amount)}
                      </div>
                      <div className="text-[11px] text-content-400 tnum">bal {formatMoney(row.balanceAfter)}</div>
                    </div>
                  </li>
                );
              })}
            </ul>
          )}

          {ledgerQuery.data && ledgerQuery.data.totalPages > 1 ? (
            <div className="flex items-center justify-center gap-2 border-t border-surface-800 px-4 py-3">
              <button className="btn-ghost" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>
                Previous
              </button>
              <span className="text-xs text-content-400">
                {ledgerQuery.data.page} / {ledgerQuery.data.totalPages}
              </span>
              <button
                className="btn-ghost"
                disabled={page >= ledgerQuery.data.totalPages}
                onClick={() => setPage((p) => p + 1)}
              >
                Next
              </button>
            </div>
          ) : null}
        </Panel>

        <Panel className="overflow-hidden">
          <div className="border-b border-surface-800 px-4 py-2.5 text-[11px] font-semibold uppercase tracking-wider text-content-400">
            <span className="flex items-center gap-2">Deposits and withdrawals <AccountMarker account="LIVE" /></span>
          </div>
          {(transfersQuery.data?.items.length ?? 0) === 0 ? (
            <EmptyState title="No requests yet" />
          ) : (
            <ul className="divide-y divide-surface-800/70">
              {transfersQuery.data!.items.map((transfer) => (
                <li key={transfer.id} className="px-4 py-2.5">
                  <div className="flex items-center gap-3">
                    <Badge tone={transfer.kind === 'DEPOSIT' ? 'accent' : 'neutral'}>{transfer.kind}</Badge>
                    <div className="min-w-0 flex-1">
                      <div className="text-sm font-semibold text-content-100 tnum">
                        {formatMoney(transfer.amount)}
                      </div>
                      <div className="text-[11px] text-content-400">
                        {formatDateTime(transfer.createdAt)}
                        {transfer.method ? ' · ' + transfer.method : ''}
                      </div>
                    </div>
                    <Badge
                      tone={
                        transfer.status === 'COMPLETED' ? 'up' : transfer.status === 'REJECTED' ? 'down' : 'warn'
                      }
                    >
                      {transfer.status === 'PENDING' ? 'UNDER REVIEW' : transfer.status}
                    </Badge>
                  </div>

                  {transfer.utr ? (
                    <div className="mt-1 truncate font-mono text-[11px] text-content-400">
                      {transfer.kind === 'DEPOSIT' ? 'UTR ' : 'Paid, UTR '}
                      {transfer.utr}
                      {transfer.proof ? ' · screenshot attached' : ''}
                    </div>
                  ) : transfer.proof ? (
                    <div className="mt-1 text-[11px] text-content-400">Screenshot attached</div>
                  ) : null}

                  {/* A rejection is only useful if the customer is told why. */}
                  {transfer.status === 'REJECTED' && transfer.reviewNote ? (
                    <p className="mt-1.5 rounded border border-down/30 bg-down/10 px-2 py-1 text-[11px] text-down">
                      {transfer.reviewNote}
                    </p>
                  ) : null}
                </li>
              ))}
            </ul>
          )}
        </Panel>
      </div>
    </div>
  );
}
