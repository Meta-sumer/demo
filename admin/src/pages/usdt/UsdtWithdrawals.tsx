import { useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { AlertTriangle, RefreshCw, X } from 'lucide-react';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { toast } from '../../hooks/useToast';
import { useUsdtRefetchInterval } from '../../hooks/useUsdtLive';
import { EmptyState, Panel, PanelHeader, Skeleton, TableSkeleton } from '../../components/ui';
import { ConfirmDialog, Drawer } from '../../components/overlay';
import {
  CopyableText,
  CopyButton,
  DetailGrid,
  FilterField,
  LedgerTable,
  ListFooter,
  NetworkBadge,
  NetworkSelect,
  QueryError,
  STICKY_ACTION_TD,
  STICKY_ACTION_TH,
  Section,
  Usdt,
  UsdtFigure,
  UserCell,
  WebhookEventList,
  WithdrawalStatusBadge,
  dateOrDash,
  errorText,
  fullDate,
  useDebounced,
} from '../../components/usdt';
import {
  WITHDRAWAL_STATUS_ORDER,
  WITHDRAWAL_STATUSES,
  dayRangeToIso,
  estimateFee,
  explorerAddressUrl,
  explorerTxUrl,
  formatUsdt,
  networkLabel,
  withdrawalStatusMeta,
} from '../../utils/usdt';
import type { UsdtUserSummary, UsdtWithdrawalView } from '../../types/usdt';

/**
 * USDT withdrawals.
 *
 * Requests above the review threshold stop at "Under review" until an operator
 * approves (the payout goes to BeeX) or rejects (the reserved amount returns to
 * the customer's USDT wallet). A chain transfer cannot be recalled, so approval
 * always restates the amount, the address and the network first.
 */
const EMPTY_FILTERS = { status: '', network: '', q: '', reference: '', address: '', from: '', to: '' };

type Decision = { kind: 'approve' | 'reject'; withdrawal: UsdtWithdrawalView; user: UsdtUserSummary | null };

export function UsdtWithdrawalsPage() {
  const [params, setParams] = useSearchParams();
  const selectedId = params.get('id');
  const userId = params.get('userId') ?? '';
  const can = useAuthStore((s) => s.can);
  const canAct = can('withdrawals:approve');
  const interval = useUsdtRefetchInterval();
  const [decision, setDecision] = useState<Decision | null>(null);

  const [filters, setFilters] = useState({
    ...EMPTY_FILTERS,
    status: params.get('status') ?? '',
    network: params.get('network') ?? '',
  });
  const [page, setPage] = useState(1);
  const applied = useDebounced(filters);
  const range = dayRangeToIso(applied.from, applied.to);
  const badRange = !!range.from && !!range.to && range.from > range.to;

  const query = useQuery({
    queryKey: ['usdt', 'withdrawals', 'list', { ...applied, userId, page }],
    queryFn: () =>
      api.usdt.withdrawals({
        status: applied.status,
        network: applied.network,
        userId,
        q: applied.q.trim(),
        reference: applied.reference.trim(),
        address: applied.address.trim(),
        from: range.from,
        to: range.to,
        page,
        limit: 20,
      }),
    enabled: !badRange,
    refetchInterval: interval,
    placeholderData: (previous) => previous,
  });

  const set = (key: keyof typeof EMPTY_FILTERS) => (value: string) => {
    setFilters((current) => ({ ...current, [key]: value }));
    setPage(1);
  };

  const updateParams = (change: Record<string, string | null>) => {
    const next = new URLSearchParams(params);
    for (const [key, value] of Object.entries(change)) {
      if (value) next.set(key, value);
      else next.delete(key);
    }
    setParams(next, { replace: true });
  };

  const filtered = Object.values(filters).some(Boolean) || !!userId;
  const rows = query.data?.items ?? [];

  return (
    <>
      <Panel className="overflow-hidden">
        <PanelHeader
          title="USDT withdrawals"
          description="Approving sends the payout to BeeX. Rejecting returns the reserved amount to the user's USDT wallet."
          actions={
            <>
              {filters.status !== 'pending_review' ? (
                <button className="btn-ghost px-3 py-1.5 text-xs" onClick={() => set('status')('pending_review')}>
                  Show under review
                </button>
              ) : null}
              {filtered ? (
                <button
                  className="btn-ghost px-3 py-1.5 text-xs"
                  onClick={() => {
                    setFilters(EMPTY_FILTERS);
                    setPage(1);
                    updateParams({ userId: null, status: null, network: null });
                  }}
                >
                  Clear filters
                </button>
              ) : null}
            </>
          }
        />

        <div className="grid grid-cols-2 gap-3 border-b border-surface-800 px-4 py-3 md:grid-cols-4">
          <FilterField label="Status" htmlFor="wd-status">
            <select id="wd-status" className="field py-1.5" value={filters.status} onChange={(e) => set('status')(e.target.value)}>
              <option value="">All statuses</option>
              {WITHDRAWAL_STATUS_ORDER.map((status) => (
                <option key={status} value={status}>
                  {WITHDRAWAL_STATUSES[status].label}
                </option>
              ))}
            </select>
          </FilterField>
          <FilterField label="Network" htmlFor="wd-network">
            <NetworkSelect id="wd-network" value={filters.network} onChange={set('network')} />
          </FilterField>
          <FilterField label="User" htmlFor="wd-user">
            <input
              id="wd-user"
              className="field py-1.5"
              placeholder="Email or username"
              value={filters.q}
              onChange={(e) => set('q')(e.target.value)}
            />
          </FilterField>
          <FilterField label="Reference" htmlFor="wd-ref">
            <input
              id="wd-ref"
              className="field py-1.5 font-mono"
              placeholder="Ours or BeeX"
              value={filters.reference}
              onChange={(e) => set('reference')(e.target.value)}
            />
          </FilterField>
          <FilterField label="Address" htmlFor="wd-address">
            <input
              id="wd-address"
              className="field py-1.5 font-mono"
              value={filters.address}
              onChange={(e) => set('address')(e.target.value)}
            />
          </FilterField>
          <FilterField label="From" htmlFor="wd-from">
            <input
              id="wd-from"
              type="date"
              className="field py-1.5"
              value={filters.from}
              onChange={(e) => set('from')(e.target.value)}
            />
          </FilterField>
          <FilterField label="To" htmlFor="wd-to">
            <input id="wd-to" type="date" className="field py-1.5" value={filters.to} onChange={(e) => set('to')(e.target.value)} />
          </FilterField>
          {userId ? (
            <div className="col-span-full">
              <span className="inline-flex items-center gap-1 rounded-md bg-surface-800 px-2 py-1 text-[11px] text-content-200">
                One user only: <span className="font-mono">{userId}</span>
                <button
                  className="rounded p-0.5 hover:bg-surface-700"
                  aria-label="Show all users"
                  onClick={() => updateParams({ userId: null })}
                >
                  <X className="h-3 w-3" />
                </button>
              </span>
            </div>
          ) : null}
          {badRange ? <p className="col-span-full text-[11px] text-down">"From" is after "To".</p> : null}
        </div>

        {query.isLoading ? (
          <TableSkeleton rows={8} columns={8} />
        ) : query.isError && !query.data ? (
          <QueryError error={query.error} onRetry={() => void query.refetch()} what="withdrawals" />
        ) : rows.length === 0 ? (
          <EmptyState title="No withdrawals match" description={filtered ? 'Try clearing a filter.' : 'No USDT withdrawals yet.'} />
        ) : (
          <div className="relative overflow-x-auto">
            <table className="table min-w-[1300px]">
              <thead>
                <tr>
                  <th>User</th>
                  <th>Status</th>
                  <th className="text-right">Amount (USDT)</th>
                  <th className="text-right">Fee (USDT)</th>
                  <th className="text-right">Net amount (USDT)</th>
                  <th>Network</th>
                  <th>Destination address</th>
                  <th>BeeX reference</th>
                  <th>Our reference</th>
                  <th>TX hash</th>
                  <th>Date</th>
                  <th className={STICKY_ACTION_TH}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((withdrawal) => (
                  <tr key={withdrawal.id} className="group cursor-pointer" onClick={() => updateParams({ id: withdrawal.id })}>
                    <td className="max-w-[200px]">
                      <UserCell user={withdrawal.user} />
                    </td>
                    <td>
                      <WithdrawalStatusBadge status={withdrawal.status} />
                    </td>
                    <td className="text-right">
                      <UsdtFigure micro={withdrawal.amount} className="font-semibold text-content-100" />
                    </td>
                    <td className="text-right">
                      <UsdtFigure micro={withdrawal.feeAmount} className="text-content-300" />
                    </td>
                    <td className="text-right">
                      <UsdtFigure micro={withdrawal.netAmount} className="text-content-200" />
                    </td>
                    <td>
                      <NetworkBadge network={withdrawal.network} />
                    </td>
                    <td>
                      <CopyableText value={withdrawal.address} label="destination address" />
                    </td>
                    <td>
                      <CopyableText value={withdrawal.providerReference} label="BeeX reference" />
                    </td>
                    <td>
                      <CopyableText value={withdrawal.reference} label="our reference" />
                    </td>
                    <td>
                      <CopyableText
                        value={withdrawal.txHash}
                        label="transaction hash"
                        href={explorerTxUrl(withdrawal.network, withdrawal.txHash)}
                      />
                    </td>
                    <td className="whitespace-nowrap text-[11px] text-content-400">{dateOrDash(withdrawal.createdAt)}</td>
                    <td className={'text-right ' + STICKY_ACTION_TD} onClick={(event) => event.stopPropagation()}>
                      <div className="flex justify-end gap-1">
                        {withdrawal.status === 'pending_review' && canAct ? (
                          <>
                            <button
                              className="btn-primary whitespace-nowrap px-2 py-1 text-[11px]"
                              onClick={() => setDecision({ kind: 'approve', withdrawal, user: withdrawal.user ?? null })}
                            >
                              Approve
                            </button>
                            <button
                              className="btn-ghost whitespace-nowrap px-2 py-1 text-[11px]"
                              onClick={() => setDecision({ kind: 'reject', withdrawal, user: withdrawal.user ?? null })}
                            >
                              Reject
                            </button>
                          </>
                        ) : null}
                        <button
                          className="btn-ghost whitespace-nowrap px-2 py-1 text-[11px]"
                          onClick={() => updateParams({ id: withdrawal.id })}
                        >
                          View
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {query.data ? (
          <ListFooter
            total={query.data.total}
            noun="withdrawal"
            page={query.data.page}
            totalPages={query.data.totalPages}
            onPage={setPage}
            fetching={query.isFetching && !query.isLoading}
          />
        ) : null}
      </Panel>

      {selectedId ? (
        <WithdrawalDrawer
          id={selectedId}
          onClose={() => updateParams({ id: null })}
          onDecide={setDecision}
          onFilterUser={(user) => {
            setPage(1);
            updateParams({ id: null, userId: user.id });
          }}
        />
      ) : null}

      {decision ? <DecisionDialog decision={decision} onClose={() => setDecision(null)} /> : null}
    </>
  );
}

function WithdrawalDrawer({
  id,
  onClose,
  onDecide,
  onFilterUser,
}: {
  id: string;
  onClose: () => void;
  onDecide: (decision: Decision) => void;
  onFilterUser: (user: UsdtUserSummary) => void;
}) {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const canAct = can('withdrawals:approve');
  const interval = useUsdtRefetchInterval();
  const [refreshOutcome, setRefreshOutcome] = useState<{ changed: boolean; text: string } | null>(null);

  const detail = useQuery({
    queryKey: ['usdt', 'withdrawals', 'detail', id],
    queryFn: () => api.usdt.withdrawal(id),
    refetchInterval: interval,
  });

  const refresh = useMutation({
    mutationFn: () => api.usdt.refreshWithdrawal(id),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: ['usdt'] });
      const before = detail.data?.withdrawal.status;
      const after = result?.withdrawal?.status;
      const moved =
        before && after && before !== after
          ? withdrawalStatusMeta(before).label + ' → ' + withdrawalStatusMeta(after).label
          : '';
      const changed = typeof result?.changed === 'boolean' ? result.changed : !!moved;
      const text = [changed ? moved || 'The withdrawal was updated' : 'BeeX reports no change', result?.note]
        .filter(Boolean)
        .join('. ');
      setRefreshOutcome({ changed, text });
      toast(changed ? 'success' : 'info', changed ? 'Withdrawal updated from BeeX' : 'No change at BeeX', text);
    },
    onError: (err) => {
      setRefreshOutcome(null);
      toast('error', 'Refresh failed', errorText(err));
    },
  });

  const withdrawal = detail.data?.withdrawal;
  const user = detail.data?.user ?? null;
  const reviewable = withdrawal?.status === 'pending_review';
  // Nothing at BeeX to ask about until the payout has been submitted.
  const refreshable = !!withdrawal && withdrawal.status !== 'pending_review' && withdrawal.status !== 'rejected';

  return (
    <Drawer
      title={withdrawal ? 'Withdrawal ' + withdrawal.reference : 'Withdrawal'}
      subtitle={withdrawal ? <WithdrawalStatusBadge status={withdrawal.status} /> : undefined}
      onClose={onClose}
      footer={
        withdrawal && canAct ? (
          <>
            {refreshable ? (
              <button className="btn-ghost px-3 py-1.5 text-xs" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
                <RefreshCw className={'h-3.5 w-3.5' + (refresh.isPending ? ' animate-spin' : '')} />
                {refresh.isPending ? 'Asking BeeX…' : 'Refresh BeeX status'}
              </button>
            ) : null}
            {reviewable ? (
              <>
                <button className="btn-ghost px-3 py-1.5 text-xs" onClick={() => onDecide({ kind: 'reject', withdrawal, user })}>
                  Reject
                </button>
                <button className="btn-primary px-3 py-1.5 text-xs" onClick={() => onDecide({ kind: 'approve', withdrawal, user })}>
                  Approve
                </button>
              </>
            ) : null}
          </>
        ) : undefined
      }
    >
      {detail.isLoading ? (
        <div className="space-y-3 p-4">
          <Skeleton className="h-6 w-1/2" />
          <Skeleton className="h-40 w-full" />
          <Skeleton className="h-24 w-full" />
        </div>
      ) : detail.isError && !detail.data ? (
        <QueryError error={detail.error} onRetry={() => void detail.refetch()} what="this withdrawal" />
      ) : withdrawal ? (
        <>
          {refreshOutcome ? (
            <div
              className={
                'mx-4 mt-4 rounded-lg border px-3 py-2 text-xs ' +
                (refreshOutcome.changed ? 'border-up/40 bg-up/10 text-up' : 'border-surface-700 bg-surface-850 text-content-300')
              }
              role="status"
            >
              <span className="font-semibold">{refreshOutcome.changed ? 'Changed: ' : 'No change: '}</span>
              {refreshOutcome.text}
            </div>
          ) : null}

          {withdrawal.failureReason ? (
            <p className="mx-4 mt-4 rounded-lg border border-down/40 bg-down/10 px-3 py-2 text-xs text-down" role="alert">
              <span className="font-semibold">Failure reason: </span>
              {withdrawal.failureReason}
            </p>
          ) : null}

          <Section title="Amounts">
            <div className="grid grid-cols-1 gap-2 sm:grid-cols-3">
              {[
                { label: 'Amount', micro: withdrawal.amount },
                { label: 'Fee', micro: withdrawal.feeAmount },
                { label: 'Net amount', micro: withdrawal.netAmount },
              ].map((row) => (
                <div key={row.label} className="rounded-lg border border-surface-800 bg-surface-850 px-3 py-2">
                  <div className="text-[10px] font-medium uppercase tracking-wider text-content-400">{row.label}</div>
                  <Usdt micro={row.micro} className="mt-0.5 block text-sm font-semibold text-content-100" />
                </div>
              ))}
            </div>
          </Section>

          <Section title="Withdrawal">
            <DetailGrid
              items={[
                { label: 'Status', value: <WithdrawalStatusBadge status={withdrawal.status} /> },
                { label: 'Network', value: <NetworkBadge network={withdrawal.network} /> },
                {
                  label: 'Destination address',
                  wide: true,
                  value: (
                    <CopyableText
                      value={withdrawal.address}
                      label="destination address"
                      short={false}
                      href={explorerAddressUrl(withdrawal.network, withdrawal.address)}
                    />
                  ),
                },
                {
                  label: 'TX hash',
                  wide: true,
                  value: (
                    <CopyableText
                      value={withdrawal.txHash}
                      label="transaction hash"
                      short={false}
                      href={explorerTxUrl(withdrawal.network, withdrawal.txHash)}
                    />
                  ),
                },
                { label: 'Our reference', value: <CopyableText value={withdrawal.reference} label="our reference" short={false} /> },
                {
                  label: 'BeeX reference',
                  value: <CopyableText value={withdrawal.providerReference} label="BeeX reference" short={false} />,
                },
                { label: 'Withdrawal ID', value: <CopyableText value={withdrawal.id} label="withdrawal ID" short={false} /> },
                { label: 'Review note', value: withdrawal.reviewNote || '--' },
                { label: 'Requested', value: fullDate(withdrawal.createdAt) },
                { label: 'Updated', value: fullDate(withdrawal.updatedAt) },
                { label: 'Completed', value: fullDate(withdrawal.completedAt) },
              ]}
            />
          </Section>

          <Section
            title="User"
            actions={
              user ? (
                <button className="btn-ghost px-2 py-1 text-[11px]" onClick={() => onFilterUser(user)}>
                  All withdrawals by this user
                </button>
              ) : null
            }
          >
            {user ? (
              <div className="flex flex-wrap items-center gap-3">
                <UserCell user={user} />
                {can('users:read') ? (
                  <Link to={'/admin/users/' + user.id} className="text-[11px] text-accent hover:underline">
                    Open user profile
                  </Link>
                ) : null}
              </div>
            ) : (
              <p className="text-xs text-content-400">User details not returned.</p>
            )}
          </Section>

          <Section title="Webhook events" description="Every BeeX callback for this payout, newest first">
            <WebhookEventList events={detail.data?.events ?? []} />
          </Section>

          <Section title="Ledger" description="USDT wallet rows: the reservation, the debit or the release">
            <LedgerTable rows={detail.data?.ledger ?? []} />
          </Section>
        </>
      ) : null}
    </Drawer>
  );
}

/** Approve or reject, restating exactly what is about to happen. */
function DecisionDialog({ decision, onClose }: { decision: Decision; onClose: () => void }) {
  const queryClient = useQueryClient();
  const [error, setError] = useState<string | null>(null);
  const { withdrawal, user } = decision;
  const approving = decision.kind === 'approve';

  const limits = useQuery({
    queryKey: ['usdt', 'payout-limits'],
    queryFn: api.usdt.payoutLimits,
    enabled: approving,
    staleTime: 60_000,
  });

  const mutation = useMutation({
    mutationFn: (note: string) =>
      approving ? api.usdt.approveWithdrawal(withdrawal.id, note || undefined) : api.usdt.rejectWithdrawal(withdrawal.id, note),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['usdt'] });
      toast(
        'success',
        approving ? 'Withdrawal approved' : 'Withdrawal rejected',
        approving ? 'Submitted to BeeX for payout' : formatUsdt(withdrawal.amount) + ' returned to the user\'s USDT wallet',
      );
      onClose();
    },
    onError: (err) => {
      const message = errorText(
        err,
        approving ? 'The withdrawal was not approved. Try again.' : 'The withdrawal was not rejected. Try again.',
      );
      setError(message);
      toast('error', approving ? 'Approval failed' : 'Rejection failed', message);
    },
  });

  const schedule = limits.data;
  const scheduleFee = schedule ? estimateFee(withdrawal.amount, schedule.feePercent, schedule.feeFixed) : null;
  const fee = withdrawal.feeAmount > 0 || scheduleFee == null ? withdrawal.feeAmount : scheduleFee;
  const who = user?.username ?? 'the user';

  if (!approving) {
    return (
      <ConfirmDialog
        title="Reject withdrawal"
        tone="danger"
        confirmLabel="Reject and return funds"
        pending={mutation.isPending}
        error={error}
        note={{
          label: 'Reason (the user sees this)',
          required: true,
          placeholder: 'e.g. Destination address failed our checks',
          hint: 'Required, 3-500 characters. Saved to the audit log.',
        }}
        onCancel={onClose}
        onConfirm={(note) => mutation.mutate(note)}
      >
        <p>
          Reject <span className="font-mono text-content-200">{withdrawal.reference}</span> for{' '}
          <strong className="font-semibold text-content-100 tnum">{formatUsdt(withdrawal.amount)}</strong>.
        </p>
        <p>
          The reserved <span className="tnum">{formatUsdt(withdrawal.amount)}</span> returns to{' '}
          <strong className="font-semibold text-content-100">{who}</strong>'s USDT wallet. Nothing is sent on chain.
        </p>
      </ConfirmDialog>
    );
  }

  return (
    <ConfirmDialog
      title="Approve withdrawal"
      confirmLabel={'Approve ' + formatUsdt(withdrawal.amount)}
      pending={mutation.isPending}
      error={error}
      note={{ label: 'Note', required: false, placeholder: 'Anything the next reviewer should know', hint: 'Up to 500 characters.' }}
      onCancel={onClose}
      onConfirm={(note) => mutation.mutate(note)}
    >
      <dl className="grid grid-cols-[auto,1fr] gap-x-3 gap-y-1.5 rounded-lg border border-surface-700 bg-surface-850 p-3">
        <dt className="text-content-400">User</dt>
        <dd className="text-content-100">{who}</dd>
        <dt className="text-content-400">Amount</dt>
        <dd className="font-semibold text-content-100 tnum">{formatUsdt(withdrawal.amount)}</dd>
        <dt className="text-content-400">Fee estimate</dt>
        <dd className="text-content-100 tnum">
          {formatUsdt(fee)}
          {schedule ? (
            <span className="block text-[10px] text-content-400">
              Schedule {schedule.feePercent}% + {formatUsdt(schedule.feeFixed)}
              {scheduleFee != null && scheduleFee !== withdrawal.feeAmount ? ' = ' + formatUsdt(scheduleFee) : ''}
            </span>
          ) : null}
        </dd>
        <dt className="text-content-400">Net to address</dt>
        <dd className="text-content-100 tnum">{formatUsdt(withdrawal.netAmount)}</dd>
        <dt className="text-content-400">Network</dt>
        <dd className="font-semibold text-content-100">
          {networkLabel(withdrawal.network)} <span className="font-normal text-content-400">({withdrawal.network})</span>
        </dd>
        <dt className="text-content-400">Address</dt>
        <dd className="flex items-start gap-1">
          <span className="break-all font-mono text-[11px] text-content-100">{withdrawal.address}</span>
          <CopyButton value={withdrawal.address} label="destination address" />
        </dd>
      </dl>
      <p className="flex gap-1.5 text-warn">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
        <span>
          Approving submits the payout to BeeX. A {networkLabel(withdrawal.network)} transfer cannot be reversed - check the
          address and the network.
        </span>
      </p>
    </ConfirmDialog>
  );
}
