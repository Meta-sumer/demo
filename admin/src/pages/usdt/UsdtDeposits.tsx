import { useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { RefreshCw, X } from 'lucide-react';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { toast } from '../../hooks/useToast';
import { useUsdtRefetchInterval } from '../../hooks/useUsdtLive';
import { Panel, PanelHeader, Skeleton, TableSkeleton, EmptyState } from '../../components/ui';
import { ConfirmDialog, Drawer } from '../../components/overlay';
import {
  CopyableText,
  DepositStatusBadge,
  DetailGrid,
  FilterField,
  LedgerTable,
  ListFooter,
  ModeBadge,
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
  YesNo,
  dateOrDash,
  errorText,
  fullDate,
  useDebounced,
} from '../../components/usdt';
import {
  DEPOSIT_STATUS_ORDER,
  DEPOSIT_STATUSES,
  creditEligibility,
  dayRangeToIso,
  depositStatusMeta,
  explorerAddressUrl,
  formatUsdt,
  safeExternalUrl,
} from '../../utils/usdt';
import type { UsdtDepositView, UsdtUserSummary } from '../../types/usdt';

/**
 * USDT deposits (BeeX invoices).
 *
 * A deposit is credited by the BeeX webhook, not by an operator. This screen is
 * for watching and for the two exceptions: asking BeeX again when a webhook was
 * missed, and crediting what actually arrived on an invoice that closed short.
 */
const EMPTY_FILTERS = { status: '', network: '', q: '', reference: '', orderId: '', from: '', to: '' };

const SHORT_STATUSES: string[] = ['partially_paid', 'underpaid'];

/** Something arrived, but less than the invoice asked for. */
const isShort = (deposit: UsdtDepositView) => deposit.amountReceived > 0 && deposit.amountReceived < deposit.amountExpected;

export function UsdtDepositsPage() {
  const [params, setParams] = useSearchParams();
  const selectedId = params.get('id');
  const userId = params.get('userId') ?? '';
  const interval = useUsdtRefetchInterval();

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
    queryKey: ['usdt', 'deposits', 'list', { ...applied, userId, page }],
    queryFn: () =>
      api.usdt.deposits({
        status: applied.status,
        network: applied.network,
        userId,
        q: applied.q.trim(),
        reference: applied.reference.trim(),
        orderId: applied.orderId.trim(),
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
          title="USDT deposits"
          description="BeeX invoices. Paid invoices are credited automatically by the webhook."
          actions={
            filtered ? (
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
            ) : null
          }
        />

        <div className="grid grid-cols-2 gap-3 border-b border-surface-800 px-4 py-3 md:grid-cols-4">
          <FilterField label="Status" htmlFor="dep-status">
            <select id="dep-status" className="field py-1.5" value={filters.status} onChange={(e) => set('status')(e.target.value)}>
              <option value="">All statuses</option>
              {DEPOSIT_STATUS_ORDER.map((status) => (
                <option key={status} value={status}>
                  {DEPOSIT_STATUSES[status].label}
                </option>
              ))}
            </select>
          </FilterField>
          <FilterField label="Network" htmlFor="dep-network">
            <NetworkSelect id="dep-network" value={filters.network} onChange={set('network')} />
          </FilterField>
          <FilterField label="User" htmlFor="dep-user">
            <input
              id="dep-user"
              className="field py-1.5"
              placeholder="Email or username"
              value={filters.q}
              onChange={(e) => set('q')(e.target.value)}
            />
          </FilterField>
          <FilterField label="BeeX reference" htmlFor="dep-ref">
            <input
              id="dep-ref"
              className="field py-1.5 font-mono"
              value={filters.reference}
              onChange={(e) => set('reference')(e.target.value)}
            />
          </FilterField>
          <FilterField label="Order ID" htmlFor="dep-order">
            <input
              id="dep-order"
              className="field py-1.5 font-mono"
              value={filters.orderId}
              onChange={(e) => set('orderId')(e.target.value)}
            />
          </FilterField>
          <FilterField label="From" htmlFor="dep-from">
            <input
              id="dep-from"
              type="date"
              className="field py-1.5"
              value={filters.from}
              onChange={(e) => set('from')(e.target.value)}
            />
          </FilterField>
          <FilterField label="To" htmlFor="dep-to">
            <input id="dep-to" type="date" className="field py-1.5" value={filters.to} onChange={(e) => set('to')(e.target.value)} />
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
          <QueryError error={query.error} onRetry={() => void query.refetch()} what="deposits" />
        ) : rows.length === 0 ? (
          <EmptyState title="No deposits match" description={filtered ? 'Try clearing a filter.' : 'No USDT deposits yet.'} />
        ) : (
          <div className="relative overflow-x-auto">
            <table className="table min-w-[1180px]">
              <thead>
                <tr>
                  <th>User</th>
                  <th>Status</th>
                  <th className="text-right">Expected (USDT)</th>
                  <th className="text-right">Received (USDT)</th>
                  <th className="text-right">Credited (USDT)</th>
                  <th className="text-right">Fee (USDT)</th>
                  <th>Network</th>
                  <th>BeeX reference</th>
                  <th>Order ID</th>
                  <th>Date</th>
                  <th className={STICKY_ACTION_TH}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((deposit) => (
                  <tr key={deposit.id} className="group cursor-pointer" onClick={() => updateParams({ id: deposit.id })}>
                    <td className="max-w-[200px]">
                      <UserCell user={deposit.user} />
                    </td>
                    <td>
                      <DepositStatusBadge status={deposit.status} />
                    </td>
                    <td className="text-right">
                      <UsdtFigure micro={deposit.amountExpected} className="text-content-200" />
                    </td>
                    <td className="text-right">
                      <UsdtFigure
                        micro={deposit.amountReceived}
                        className={isShort(deposit) ? 'text-warn' : 'text-content-100'}
                      />
                    </td>
                    <td className="text-right">
                      {deposit.credited ? (
                        <UsdtFigure micro={deposit.amountCredited} className="font-semibold text-up" />
                      ) : (
                        <span className="text-[11px] text-content-400">Not credited</span>
                      )}
                    </td>
                    <td className="text-right">
                      <UsdtFigure micro={deposit.feeAmount} className="text-content-300" />
                    </td>
                    <td>
                      <NetworkBadge network={deposit.network} />
                    </td>
                    <td>
                      <CopyableText value={deposit.reference} label="BeeX reference" />
                    </td>
                    <td>
                      <CopyableText value={deposit.orderId} label="order ID" />
                    </td>
                    <td className="whitespace-nowrap text-[11px] text-content-400">{dateOrDash(deposit.createdAt)}</td>
                    <td className={'text-right ' + STICKY_ACTION_TD}>
                      <button
                        className="btn-ghost whitespace-nowrap px-2 py-1 text-[11px]"
                        onClick={(event) => {
                          event.stopPropagation();
                          updateParams({ id: deposit.id });
                        }}
                      >
                        View
                      </button>
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
            noun="deposit"
            page={query.data.page}
            totalPages={query.data.totalPages}
            onPage={setPage}
            fetching={query.isFetching && !query.isLoading}
          />
        ) : null}
      </Panel>

      {selectedId ? (
        <DepositDrawer
          id={selectedId}
          onClose={() => updateParams({ id: null })}
          onFilterUser={(user) => {
            setPage(1);
            updateParams({ id: null, userId: user.id });
          }}
        />
      ) : null}
    </>
  );
}

function DepositDrawer({
  id,
  onClose,
  onFilterUser,
}: {
  id: string;
  onClose: () => void;
  onFilterUser: (user: UsdtUserSummary) => void;
}) {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const interval = useUsdtRefetchInterval();
  const canAct = can('deposits:approve');
  const [confirmCredit, setConfirmCredit] = useState(false);
  const [creditError, setCreditError] = useState<string | null>(null);
  const [refreshOutcome, setRefreshOutcome] = useState<{ changed: boolean; text: string } | null>(null);

  const detail = useQuery({
    queryKey: ['usdt', 'deposits', 'detail', id],
    queryFn: () => api.usdt.deposit(id),
    refetchInterval: interval,
  });

  const refresh = useMutation({
    mutationFn: () => api.usdt.refreshDeposit(id),
    onSuccess: (result) => {
      void queryClient.invalidateQueries({ queryKey: ['usdt'] });
      const before = detail.data?.deposit.status;
      const after = result?.deposit?.status;
      const moved =
        before && after && before !== after
          ? depositStatusMeta(before).label + ' → ' + depositStatusMeta(after).label
          : '';
      const text = [result?.changed ? moved || 'The deposit was updated' : 'BeeX reports no change', result?.note]
        .filter(Boolean)
        .join('. ');
      setRefreshOutcome({ changed: !!result?.changed, text });
      toast(result?.changed ? 'success' : 'info', result?.changed ? 'Deposit updated from BeeX' : 'No change at BeeX', text);
    },
    onError: (err) => {
      setRefreshOutcome(null);
      toast('error', 'Refresh failed', errorText(err));
    },
  });

  const credit = useMutation({
    mutationFn: (note: string) => api.usdt.creditDeposit(id, note),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['usdt'] });
      setConfirmCredit(false);
      setCreditError(null);
      toast('success', 'Received amount credited', 'Recorded in the audit log');
    },
    onError: (err) => {
      const message = errorText(err, 'The credit was not applied. Try again.');
      setCreditError(message);
      toast('error', 'Credit failed', message);
    },
  });

  const deposit = detail.data?.deposit;
  const user = detail.data?.user ?? null;
  const eligibility = deposit ? creditEligibility(deposit) : null;

  return (
    <>
      <Drawer
        title={deposit ? 'Deposit ' + (deposit.orderId || deposit.id) : 'Deposit'}
        subtitle={deposit ? <DepositStatusBadge status={deposit.status} /> : undefined}
        onClose={onClose}
        footer={
          deposit && canAct ? (
            <>
              <button className="btn-ghost px-3 py-1.5 text-xs" disabled={refresh.isPending} onClick={() => refresh.mutate()}>
                <RefreshCw className={'h-3.5 w-3.5' + (refresh.isPending ? ' animate-spin' : '')} />
                {refresh.isPending ? 'Asking BeeX…' : 'Refresh BeeX status'}
              </button>
              {eligibility?.eligible ? (
                <button
                  className="btn-primary px-3 py-1.5 text-xs"
                  onClick={() => {
                    setCreditError(null);
                    setConfirmCredit(true);
                  }}
                >
                  Credit received amount
                </button>
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
          <QueryError error={detail.error} onRetry={() => void detail.refetch()} what="this deposit" />
        ) : deposit ? (
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

            {canAct && eligibility && !eligibility.eligible && SHORT_STATUSES.includes(deposit.status) ? (
              <p className="mx-4 mt-4 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2 text-[11px] text-content-300">
                Manual credit not available: {eligibility.reason}
              </p>
            ) : null}

            <Section title="Amounts">
              <DepositAmounts deposit={deposit} />
            </Section>

            <Section title="Deposit">
              <DetailGrid
                items={[
                  { label: 'Status', value: <DepositStatusBadge status={deposit.status} /> },
                  { label: 'Mode', value: <ModeBadge mode={deposit.mode} /> },
                  { label: 'Network', value: <NetworkBadge network={deposit.network} /> },
                  {
                    label: 'Credited',
                    value: (
                      <span className="inline-flex items-center gap-2">
                        <YesNo value={deposit.credited} />
                        {deposit.creditedAt ? (
                          <span className="text-[11px] text-content-400">{fullDate(deposit.creditedAt)}</span>
                        ) : null}
                      </span>
                    ),
                  },
                  { label: 'Order ID', value: <CopyableText value={deposit.orderId} label="order ID" short={false} /> },
                  { label: 'BeeX reference', value: <CopyableText value={deposit.reference} label="BeeX reference" short={false} /> },
                  { label: 'Invoice ID', value: <CopyableText value={deposit.invoiceId} label="invoice ID" short={false} /> },
                  { label: 'Deposit ID', value: <CopyableText value={deposit.id} label="deposit ID" short={false} /> },
                  {
                    label: 'Pay address',
                    wide: true,
                    value: (
                      <CopyableText
                        value={deposit.payAddress}
                        label="pay address"
                        short={false}
                        href={explorerAddressUrl(deposit.network, deposit.payAddress)}
                      />
                    ),
                  },
                  {
                    label: 'Payment URL',
                    wide: true,
                    value: <PaymentLink url={deposit.paymentUrl} />,
                  },
                  { label: 'Expires', value: fullDate(deposit.expiresAt) },
                  { label: 'Created', value: fullDate(deposit.createdAt) },
                  { label: 'Updated', value: fullDate(deposit.updatedAt) },
                  { label: 'Credited at', value: fullDate(deposit.creditedAt) },
                ]}
              />
            </Section>

            <Section
              title="User"
              actions={
                user ? (
                  <button className="btn-ghost px-2 py-1 text-[11px]" onClick={() => onFilterUser(user)}>
                    All deposits by this user
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

            <Section title="Webhook events" description="Every BeeX callback for this invoice, newest first">
              <WebhookEventList events={detail.data?.events ?? []} />
            </Section>

            <Section title="Ledger" description="USDT wallet rows written for this deposit">
              <LedgerTable rows={detail.data?.ledger ?? []} />
            </Section>
          </>
        ) : null}
      </Drawer>

      {confirmCredit && deposit ? (
        <ConfirmDialog
          title="Credit received amount"
          confirmLabel={'Credit ' + formatUsdt(deposit.amountReceived)}
          pending={credit.isPending}
          error={creditError}
          note={{
            label: 'Reason for the manual credit',
            required: true,
            placeholder: 'e.g. Customer confirmed the short payment; crediting what arrived',
            hint: 'Required, 3-500 characters. Saved to the audit log.',
          }}
          onCancel={() => setConfirmCredit(false)}
          onConfirm={(note) => credit.mutate(note)}
        >
          <p>
            Credit exactly{' '}
            <strong className="font-semibold text-content-100 tnum">{formatUsdt(deposit.amountReceived)}</strong> to{' '}
            <strong className="font-semibold text-content-100">{user?.username ?? 'the customer'}</strong>'s USDT wallet.
          </p>
          <p className="tnum">
            Invoice expected {formatUsdt(deposit.amountExpected)}; received {formatUsdt(deposit.amountReceived)} on{' '}
            {deposit.network}.
          </p>
          <p>This is recorded in the audit log and cannot be undone from this screen.</p>
        </ConfirmDialog>
      ) : null}
    </>
  );
}

function DepositAmounts({ deposit }: { deposit: UsdtDepositView }) {
  const shortfall = deposit.amountExpected - deposit.amountReceived;
  const rows: Array<{ label: string; micro: number; tone?: string }> = [
    { label: 'Expected', micro: deposit.amountExpected },
    { label: 'Received', micro: deposit.amountReceived, tone: shortfall > 0 && deposit.amountReceived > 0 ? 'text-warn' : undefined },
    { label: 'Fee', micro: deposit.feeAmount },
    { label: 'Net', micro: deposit.netAmount },
    { label: 'Credited', micro: deposit.amountCredited, tone: deposit.credited ? 'text-up' : 'text-content-400' },
  ];
  return (
    <div>
      <div className="grid grid-cols-2 gap-2 sm:grid-cols-5">
        {rows.map((row) => (
          <div key={row.label} className="rounded-lg border border-surface-800 bg-surface-850 px-3 py-2">
            <div className="text-[10px] font-medium uppercase tracking-wider text-content-400">{row.label}</div>
            <Usdt micro={row.micro} className={'mt-0.5 block text-xs font-semibold ' + (row.tone ?? 'text-content-100')} />
          </div>
        ))}
      </div>
      {shortfall > 0 && deposit.amountReceived > 0 ? (
        <p className="mt-2 text-[11px] text-warn">Short by {formatUsdt(shortfall)}</p>
      ) : shortfall < 0 ? (
        <p className="mt-2 text-[11px] text-content-300">Over by {formatUsdt(-shortfall)}</p>
      ) : null}
    </div>
  );
}

function PaymentLink({ url }: { url: string | null }) {
  const safe = safeExternalUrl(url);
  if (!url) return <span className="text-content-400">--</span>;
  if (!safe) return <span className="break-all font-mono text-[11px] text-content-300">{url}</span>;
  return (
    <a href={safe} target="_blank" rel="noopener noreferrer" className="break-all font-mono text-[11px] text-accent hover:underline">
      {safe}
    </a>
  );
}
