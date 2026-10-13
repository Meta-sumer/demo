import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { useQuery, type UseQueryResult } from '@tanstack/react-query';
import clsx from 'clsx';
import { ArrowRight } from 'lucide-react';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { useUsdtRefetchInterval } from '../../hooks/useUsdtLive';
import { EmptyState, Panel, PanelHeader, Skeleton } from '../../components/ui';
import { ModeBadge, NetworkBadge, QueryError, Usdt, YesNo, fullDate } from '../../components/usdt';
import { formatUsdt } from '../../utils/usdt';
import type { UsdtPayoutLimits, UsdtSummary } from '../../types/usdt';

/**
 * USDT overview: what has come in, what has gone out, what customers hold,
 * and whether the BeeX connection is configured and in which mode.
 */
export function UsdtDashboardPage() {
  const can = useAuthStore((s) => s.can);
  const interval = useUsdtRefetchInterval();
  const canWithdrawals = can('withdrawals:read');

  const summary = useQuery({ queryKey: ['usdt', 'summary'], queryFn: api.usdt.summary, refetchInterval: interval });
  const limits = useQuery({ queryKey: ['usdt', 'payout-limits'], queryFn: api.usdt.payoutLimits, staleTime: 60_000 });
  const underReview = useQuery({
    queryKey: ['usdt', 'badge', 'pending-review'],
    queryFn: () => api.usdt.withdrawals({ status: 'pending_review', limit: 1 }),
    enabled: canWithdrawals,
    refetchInterval: interval,
  }).data?.total;

  if (summary.isError && !summary.data) {
    return (
      <Panel>
        <QueryError error={summary.error} onRetry={() => void summary.refetch()} what="the USDT summary" />
      </Panel>
    );
  }

  const data = summary.data;

  return (
    <div className="space-y-4">
      {canWithdrawals && underReview ? (
        <Link
          to="/admin/usdt/withdrawals?status=pending_review"
          className="panel flex items-center gap-3 border-warn/40 px-4 py-3 text-sm hover:border-warn"
        >
          <span className="rounded-md bg-warn/15 px-2 py-0.5 text-xs font-bold text-warn ring-1 ring-warn/30 tnum">{underReview}</span>
          <span className="font-medium text-content-100">
            {underReview === 1 ? 'withdrawal is' : 'withdrawals are'} waiting for review
          </span>
          <ArrowRight className="ml-auto h-4 w-4 text-content-400" />
        </Link>
      ) : null}

      <div className="grid gap-3 xl:grid-cols-2">
        <Panel>
          <PanelHeader
            title="Deposits"
            description="BeeX invoices, all time"
            actions={<OpenLink to="/admin/usdt/deposits">Open</OpenLink>}
          />
          <div className="grid grid-cols-2 gap-px bg-surface-800 sm:grid-cols-4 xl:grid-cols-2">
            <Tile label="Count" value={data ? data.deposits.count.toLocaleString('en-US') : null} />
            <Tile label="Credited" value={data ? <Usdt micro={data.deposits.creditedAmount} wrap /> : null} tone="up" />
            <Tile
              label="Pending"
              value={data ? data.deposits.pending.toLocaleString('en-US') : null}
              tone={data && data.deposits.pending > 0 ? 'warn' : undefined}
            />
            <Tile
              label="Failed"
              value={data ? data.deposits.failed.toLocaleString('en-US') : null}
              tone={data && data.deposits.failed > 0 ? 'down' : undefined}
              to={data && data.deposits.failed > 0 ? '/admin/usdt/deposits?status=failed' : undefined}
            />
          </div>
        </Panel>

        <Panel>
          <PanelHeader
            title="Withdrawals"
            description="Payouts through BeeX, all time"
            actions={canWithdrawals ? <OpenLink to="/admin/usdt/withdrawals">Open</OpenLink> : null}
          />
          <div className="grid grid-cols-2 gap-px bg-surface-800 sm:grid-cols-4 xl:grid-cols-2">
            <Tile label="Count" value={data ? data.withdrawals.count.toLocaleString('en-US') : null} />
            <Tile label="Completed" value={data ? <Usdt micro={data.withdrawals.completedAmount} wrap /> : null} tone="up" />
            <Tile
              label="Pending"
              value={data ? data.withdrawals.pending.toLocaleString('en-US') : null}
              tone={data && data.withdrawals.pending > 0 ? 'warn' : undefined}
            />
            <Tile
              label="Failed"
              value={data ? data.withdrawals.failed.toLocaleString('en-US') : null}
              tone={data && data.withdrawals.failed > 0 ? 'down' : undefined}
              to={canWithdrawals && data && data.withdrawals.failed > 0 ? '/admin/usdt/withdrawals?status=failed' : undefined}
            />
          </div>
        </Panel>
      </div>

      <Panel>
        <PanelHeader title="Customer USDT wallets" description="Held on behalf of customers; locked backs withdrawals in progress" />
        <div className="grid grid-cols-1 gap-px bg-surface-800 sm:grid-cols-3">
          <Tile label="Balance" value={data ? <Usdt micro={data.walletTotals.balance} wrap /> : null} />
          <Tile label="Locked" value={data ? <Usdt micro={data.walletTotals.locked} wrap /> : null} tone="warn" />
          <Tile
            label="Available"
            value={data ? <Usdt micro={data.walletTotals.balance - data.walletTotals.locked} wrap /> : null}
          />
        </div>
      </Panel>

      <div className="grid gap-3 xl:grid-cols-3">
        <Panel className="overflow-hidden xl:col-span-2">
          <PanelHeader title="By network" description="Deposited and withdrawn per chain" />
          {!data ? (
            <div className="p-4">
              <Skeleton className="h-20 w-full" />
            </div>
          ) : data.byNetwork.length === 0 ? (
            <EmptyState title="No activity yet" />
          ) : (
            <div className="relative overflow-x-auto">
              <table className="table min-w-[620px]">
                <thead>
                  <tr>
                    <th>Network</th>
                    <th className="text-right">Deposits</th>
                    <th className="text-right">Deposited</th>
                    <th className="text-right">Withdrawals</th>
                    <th className="text-right">Withdrawn</th>
                  </tr>
                </thead>
                <tbody>
                  {data.byNetwork.map((row) => (
                    <tr key={row.network}>
                      <td>
                        <NetworkBadge network={row.network} />
                      </td>
                      <td className="text-right tnum text-content-300">{row.deposits.toLocaleString('en-US')}</td>
                      <td className="text-right">
                        <Usdt micro={row.depositAmount} className="text-content-100" />
                      </td>
                      <td className="text-right tnum text-content-300">{row.withdrawals.toLocaleString('en-US')}</td>
                      <td className="text-right">
                        <Usdt micro={row.withdrawalAmount} className="text-content-100" />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </Panel>

        <ProviderCard provider={data?.provider} />
      </div>

      <PayoutLimitsCard query={limits} />
    </div>
  );
}

function OpenLink({ to, children }: { to: string; children: ReactNode }) {
  return (
    <Link to={to} className="inline-flex items-center gap-1 text-xs font-medium text-accent hover:underline">
      {children}
      <ArrowRight className="h-3.5 w-3.5" />
    </Link>
  );
}

function Tile({
  label,
  value,
  tone,
  to,
}: {
  label: string;
  value: ReactNode | null;
  tone?: 'up' | 'down' | 'warn';
  to?: string;
}) {
  const body = (
    <>
      <div className="text-[10px] font-medium uppercase tracking-wider text-content-400">{label}</div>
      <div
        className={clsx(
          'mt-1 text-sm font-semibold tnum sm:text-base',
          tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : tone === 'warn' ? 'text-warn' : 'text-content-100',
        )}
      >
        {value ?? <Skeleton className="h-5 w-20" />}
      </div>
    </>
  );
  return to ? (
    <Link to={to} className="block min-w-0 bg-surface-900 px-4 py-3 hover:bg-surface-850">
      {body}
    </Link>
  ) : (
    <div className="min-w-0 bg-surface-900 px-4 py-3">{body}</div>
  );
}

function ProviderCard({ provider }: { provider: UsdtSummary['provider'] | undefined }) {
  const balances = provider ? normaliseBalances(provider.balances) : [];
  return (
    <Panel>
      <PanelHeader title="Provider" description="The payment gateway connection" />
      {!provider ? (
        <div className="p-4">
          <Skeleton className="h-24 w-full" />
        </div>
      ) : (
        <div className="space-y-3 p-4">
          <dl className="grid grid-cols-[auto,1fr] items-center gap-x-4 gap-y-2 text-sm">
            <dt className="text-[11px] uppercase tracking-wider text-content-400">Name</dt>
            <dd className="font-semibold text-content-100">{provider.name}</dd>
            <dt className="text-[11px] uppercase tracking-wider text-content-400">Mode</dt>
            <dd>
              <ModeBadge mode={provider.mode} />
            </dd>
            <dt className="text-[11px] uppercase tracking-wider text-content-400">Configured</dt>
            <dd>
              <YesNo value={provider.configured} yes="Yes - keys set" no="No - keys missing" />
            </dd>
          </dl>
          {!provider.configured ? (
            <p className="rounded-lg border border-down/40 bg-down/10 px-3 py-2 text-[11px] text-down">
              BeeX is not configured: new deposits and payouts cannot be created.
            </p>
          ) : null}
          {balances.length > 0 ? (
            <div>
              <div className="mb-1 text-[11px] font-medium uppercase tracking-wider text-content-400">BeeX balances</div>
              <ul className="divide-y divide-surface-800 rounded-lg border border-surface-800">
                {balances.map((balance) => (
                  <li key={balance.currency} className="flex flex-wrap items-baseline gap-x-3 px-3 py-2 text-xs">
                    <span className="font-mono text-content-200">{balance.currency}</span>
                    <span className="ml-auto flex flex-wrap justify-end gap-x-3">
                      {balance.figures.map((figure) => (
                        <span key={figure.label} className="text-content-100">
                          {figure.label ? <span className="mr-1 text-content-400">{figure.label}</span> : null}
                          <span className="tnum">{figure.text}</span>
                        </span>
                      ))}
                    </span>
                  </li>
                ))}
              </ul>
            </div>
          ) : null}
        </div>
      )}
    </Panel>
  );
}

/**
 * BeeX balances arrive in the provider's own shape: a map of currency to
 * amount, a map of currency to {available, frozen...}, or a list of rows.
 * Integers are micro-USDT; anything else is shown exactly as reported.
 */
const LABEL_KEYS = /^(currency|code|asset|coin|symbol|network)$/i;

function normaliseBalances(raw: unknown): Array<{ currency: string; figures: Array<{ label: string; text: string }> }> {
  const show = (value: unknown) =>
    typeof value === 'number' && Number.isSafeInteger(value) ? formatUsdt(value) : String(value);
  const figuresOf = (value: unknown) => {
    if (value && typeof value === 'object' && !Array.isArray(value)) {
      return Object.entries(value as Record<string, unknown>)
        .filter(([key, v]) => !LABEL_KEYS.test(key) && (typeof v === 'number' || typeof v === 'string'))
        .map(([key, v]) => ({ label: key, text: show(v) }));
    }
    return [{ label: '', text: show(value) }];
  };

  if (Array.isArray(raw)) {
    return raw
      .filter((row): row is Record<string, unknown> => !!row && typeof row === 'object')
      .map((row, index) => ({
        currency: String(row.currency ?? row.code ?? row.asset ?? row.coin ?? row.symbol ?? 'Balance ' + (index + 1)),
        figures: figuresOf(row),
      }));
  }
  if (raw && typeof raw === 'object') {
    return Object.entries(raw as Record<string, unknown>).map(([currency, value]) => ({ currency, figures: figuresOf(value) }));
  }
  return [];
}

function PayoutLimitsCard({ query }: { query: UseQueryResult<UsdtPayoutLimits> }) {
  const limits = query.data;
  return (
    <Panel>
      <PanelHeader
        title="Payout limits"
        description={limits?.fetchedAt ? 'From BeeX, fetched ' + fullDate(limits.fetchedAt) : 'From BeeX'}
      />
      {query.isLoading ? (
        <div className="p-4">
          <Skeleton className="h-16 w-full" />
        </div>
      ) : query.isError && !limits ? (
        <QueryError error={query.error} onRetry={() => void query.refetch()} what="payout limits" />
      ) : limits ? (
        <div className="grid grid-cols-2 gap-px bg-surface-800 sm:grid-cols-3 xl:grid-cols-6">
          <Tile label="Minimum" value={<Usdt micro={limits.minWithdrawal} wrap />} />
          <Tile label="Maximum" value={<Usdt micro={limits.maxWithdrawal} wrap />} />
          <Tile label="Daily cap" value={<Usdt micro={limits.dailyWithdrawalCap} wrap />} />
          <Tile label="Reviewed above" value={<Usdt micro={limits.reviewedAbove} wrap />} tone="warn" />
          <Tile
            label="Fee"
            value={
              <span className="text-sm">
                {limits.feePercent}% + <Usdt micro={limits.feeFixed} />
              </span>
            }
          />
          <Tile
            label="Currencies"
            value={<span className="font-mono text-sm">{limits.currencies?.length ? limits.currencies.join(', ') : '--'}</span>}
          />
        </div>
      ) : null}
    </Panel>
  );
}
