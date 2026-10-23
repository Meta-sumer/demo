import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { formatDateTime, formatMoney, formatSignedMoney } from '../utils/format';
import { queryMaxAge } from '../utils/freshness';
import { toast } from '../hooks/useToast';
import { ApiRequestError } from '../services/http';
import { StaleBadge, Unavailable } from '../components/freshness';
import {
  Badge,
  Pagination,
  Panel,
  PanelHeader,
  RiskBadge,
  Stat,
  StatusBadge,
  TableSkeleton,
} from '../components/ui';

/**
 * Live hedging screen (docs/audit/hedging, metric dictionary).
 *
 * Per asset: the book's delta-equivalent, the capped target, the protection
 * actually held (filled position only), what is still working at the venue
 * (contingent, not protection) and the residual - plus the venue, INR rate and
 * reconciliation state the figures rest on, open alerts and the order log.
 */
const MONITOR_REFRESH_MS = 5000;
const STATUS_REFRESH_MS = 5000;

function qty(value: number | undefined | null): string {
  if (value === undefined || value === null) return '--';
  return value.toLocaleString('en-US', { maximumFractionDigits: 8 });
}

function errorMessage(err: unknown): string | undefined {
  return err instanceof ApiRequestError ? err.message : undefined;
}

export function HedgingPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [page, setPage] = useState(1);
  const [rate, setRate] = useState('');

  const statusQuery = useQuery({
    queryKey: ['hedging', 'status'],
    queryFn: api.hedging.status,
    refetchInterval: STATUS_REFRESH_MS,
  });

  const monitorQuery = useQuery({
    queryKey: ['hedging', 'monitor'],
    queryFn: api.hedging.monitor,
    refetchInterval: MONITOR_REFRESH_MS,
  });

  const alertsQuery = useQuery({
    queryKey: ['hedging', 'alerts'],
    queryFn: () => api.hedging.alerts('open'),
    refetchInterval: 15_000,
  });

  const ordersQuery = useQuery({
    queryKey: ['hedging', 'orders', page],
    queryFn: () => api.hedging.orders({ page, limit: 20 }),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
  });

  const invalidate = () => void queryClient.invalidateQueries({ queryKey: ['hedging'] });

  const cancelMutation = useMutation({
    mutationFn: (id: string) => api.hedging.cancel(id),
    onSuccess: (order) => {
      invalidate();
      toast('success', order.status === 'CANCELLED' ? 'Hedge order cancelled' : 'Order was already ' + order.status);
    },
    onError: (err) => toast('error', 'Could not cancel', errorMessage(err)),
  });

  const rateMutation = useMutation({
    mutationFn: (value: number) => api.hedging.setFxRate({ inrPerUsdt: value, source: 'admin console' }),
    onSuccess: () => {
      setRate('');
      invalidate();
      toast('success', 'INR rate recorded');
    },
    onError: (err) => toast('error', 'Could not set the rate', errorMessage(err)),
  });

  const reconcileMutation = useMutation({
    mutationFn: () => api.hedging.reconcile(),
    onSuccess: () => {
      invalidate();
      toast('success', 'Reconciliation finished');
    },
    onError: (err) => toast('error', 'Reconciliation failed', errorMessage(err)),
  });

  const resolveMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) => api.hedging.resolveAlert(id, note),
    onSuccess: () => {
      invalidate();
      toast('success', 'Alert resolved');
    },
    onError: (err) => toast('error', 'Could not resolve', errorMessage(err)),
  });

  const status = statusQuery.data;
  const reconcileMaxAge = status ? status.reconcileIntervalMs * 2 + 60_000 : 180_000;
  const monitorAsOf = monitorQuery.dataUpdatedAt || null;

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Hedge venue"
          value={
            status ? (
              <span className="flex items-center gap-2">
                {status.venue}
                <Badge tone={status.live ? 'critical' : 'neutral'}>{status.live ? 'LIVE' : status.provider === 'paper' ? 'PAPER' : 'TESTNET'}</Badge>
              </span>
            ) : statusQuery.isError ? (
              <Unavailable />
            ) : (
              '--'
            )
          }
          tone={status && !status.canTrade ? 'warn' : 'neutral'}
          hint={
            <>
              {status ? (status.canTrade ? 'Orders allowed' : status.canTradeReason) : null}
              <StaleBadge asOf={statusQuery.dataUpdatedAt || null} maxAgeMs={queryMaxAge(STATUS_REFRESH_MS)} />
            </>
          }
        />
        <Stat
          label="INR per USDT"
          value={status && !status.fx.missing ? status.fx.inrPerUsdt.toFixed(4) : status ? <Unavailable /> : '--'}
          tone={status && (status.fx.missing || status.fx.stale) ? 'warn' : 'neutral'}
          hint={
            status && !status.fx.missing ? (
              <>
                {status.fx.source} - as of {formatDateTime(status.fx.asOf)}
                <StaleBadge asOf={status.fx.asOf} maxAgeMs={status.fx.maxAgeMs} label="rate" />
              </>
            ) : (
              'No rate: hedging is unavailable'
            )
          }
        />
        <Stat
          label="Reconciliation"
          value={status ? <StatusBadge status={status.reconcileStatus} /> : statusQuery.isError ? <Unavailable /> : '--'}
          tone={status && status.reconcileStatus === 'DRIFT' ? 'down' : 'neutral'}
          hint={
            status ? (
              <>
                {status.lastReconciledAt
                  ? 'Last reconciled ' + formatDateTime(status.lastReconciledAt)
                  : 'Never reconciled with the venue'}
                {status.lastReconciledAt ? (
                  <StaleBadge asOf={status.lastReconciledAt} maxAgeMs={reconcileMaxAge} />
                ) : null}
              </>
            ) : undefined
          }
        />
        <Stat
          label="Open hedge alerts"
          value={status ? status.openAlerts : '--'}
          tone={status && status.openAlerts > 0 ? 'warn' : 'neutral'}
          hint={
            status && status.costModelErrorBps !== null
              ? 'Cost model error ' + status.costModelErrorBps.toFixed(2) + ' bps/side over ' + status.costModelSamples + ' orders'
              : 'No filled orders to measure the cost model yet'
          }
        />
      </div>

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Venue"
          description="Book, mark, funding and fee the cost estimates read; older than the venue limit means hedging is unavailable"
          actions={
            can('hedge:write') ? (
              <div className="flex items-center gap-2">
                <input
                  className="field w-28"
                  placeholder="INR/USDT"
                  value={rate}
                  onChange={(event) => setRate(event.target.value)}
                />
                <button
                  className="btn-ghost px-2 py-1 text-[11px]"
                  disabled={rateMutation.isPending || !(Number(rate) > 0)}
                  onClick={() => rateMutation.mutate(Number(rate))}
                >
                  Set rate
                </button>
                <button
                  className="btn-ghost px-2 py-1 text-[11px]"
                  disabled={reconcileMutation.isPending}
                  onClick={() => reconcileMutation.mutate()}
                >
                  Reconcile now
                </button>
              </div>
            ) : null
          }
        />
        <div className="overflow-x-auto">
          <table className="table min-w-[800px]">
            <thead>
              <tr>
                <th>Symbol</th>
                <th>State</th>
                <th className="text-right">Mid</th>
                <th className="text-right">Mark</th>
                <th className="text-right">Funding rate</th>
                <th className="text-right">Taker fee</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {(status?.venues ?? []).map((v) => (
                <tr key={v.symbol}>
                  <td className="font-medium text-content-100">{v.symbol}</td>
                  <td>
                    <StatusBadge status={v.ok && !v.stale ? 'OK' : 'FAILED'} />
                    {v.error ? <div className="text-[10px] text-down">{v.error}</div> : null}
                  </td>
                  <td className="text-right tnum">{v.ok ? v.mid.toFixed(2) : <Unavailable />}</td>
                  <td className="text-right tnum">{v.ok ? v.mark.toFixed(2) : '--'}</td>
                  <td className="text-right tnum">{v.ok ? (v.fundingRate * 100).toFixed(4) + '%' : '--'}</td>
                  <td className="text-right tnum">{v.takerFeeBps > 0 ? v.takerFeeBps.toFixed(2) + ' bps' : 'config'}</td>
                  <td className="text-[11px] text-content-400">
                    {v.updatedAt ? formatDateTime(v.updatedAt) : '--'}
                    <StaleBadge asOf={v.updatedAt || null} maxAgeMs={status?.venueMaxAgeMs ?? 15_000} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Panel>

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Hedge coverage"
          description="Delta-equivalent INR notionals. Held = filled position at mark (protection); working orders are contingent and shown apart. Hedging never affects a customer result."
          actions={<StaleBadge asOf={monitorAsOf} maxAgeMs={queryMaxAge(MONITOR_REFRESH_MS)} label="table" />}
        />

        {monitorQuery.isLoading ? (
          <TableSkeleton rows={5} columns={10} />
        ) : monitorQuery.isError && !monitorQuery.data ? (
          <div className="p-4">
            <Unavailable />
          </div>
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[1300px]">
              <thead>
                <tr>
                  <th>Asset</th>
                  <th className="text-right">Book delta</th>
                  <th className="text-right">Target</th>
                  <th className="text-right">Held (filled)</th>
                  <th className="text-right">Working</th>
                  <th className="text-right">Residual</th>
                  <th>Risk</th>
                  <th>Reconciliation</th>
                  <th>Cost / availability</th>
                  <th className="text-right">Realised (net)</th>
                  <th className="text-right">Unrealised</th>
                  <th>Last order</th>
                </tr>
              </thead>
              <tbody>
                {(monitorQuery.data ?? []).map((row) => {
                  const coverage =
                    row.targetHedge > 0 ? Math.min(100, Math.round((row.currentHedge / row.targetHedge) * 100)) : 0;
                  const realisedNet = row.realisedPnl - row.fees + row.funding;
                  return (
                    <tr key={row.assetId}>
                      <td>
                        <div className="font-medium text-content-100">{row.symbol}</div>
                        <div className="text-[10px] text-content-400">
                          {!row.hedgeable ? 'not hedgeable' : !row.hedgeEnabled ? 'hedging disabled' : row.venueSymbol}
                        </div>
                      </td>
                      <td className="text-right tnum text-content-300">{row.hedgeable ? formatSignedMoney(row.bookDelta) : '--'}</td>
                      <td className="text-right tnum text-content-200">
                        {formatSignedMoney(row.targetSigned)}
                        <div className="text-[10px] text-content-400">
                          {row.targetReason
                            ? row.targetReason
                            : 'h ' + row.hedgeRatio.toFixed(2) + (row.cappedBy ? ', capped: ' + row.cappedBy : '')}
                        </div>
                      </td>
                      <td className="text-right tnum text-accent">
                        {formatSignedMoney(row.positionNotional)}
                        <div className="text-[10px] text-content-400">
                          {qty(row.positionQty)}
                          {row.targetHedge > 0 ? ' - ' + coverage + '% of target' : ''}
                        </div>
                        {row.positionQty !== 0 ? <StaleBadge asOf={row.markedAt} maxAgeMs={60_000} label="mark" /> : null}
                      </td>
                      <td className="text-right tnum text-content-400">
                        {row.workingQty !== 0 ? formatSignedMoney(row.workingNotional) : '--'}
                        {row.workingQty !== 0 ? <div className="text-[10px]">{qty(row.workingQty)} contingent</div> : null}
                      </td>
                      <td className="text-right tnum text-warn">{formatSignedMoney(row.residualNotional)}</td>
                      <td>
                        <RiskBadge level={row.riskLevel} />
                      </td>
                      <td>
                        <StatusBadge status={row.reconcileStatus} />
                        <div className="text-[10px] text-content-400">
                          {row.reconciledAt ? formatDateTime(row.reconciledAt) : 'never'}
                          {row.reconcileStatus === 'DRIFT' ? ' - venue ' + qty(row.venueQty) + ', drift ' + qty(row.driftQty) : ''}
                        </div>
                      </td>
                      <td>
                        {row.available ? (
                          <Badge tone={row.expensive ? 'warn' : 'up'}>
                            {row.expensive ? 'expensive' : 'available'} {row.roundTripBps.toFixed(1)} bps
                          </Badge>
                        ) : (
                          <Badge tone="down">{row.unavailableReason || 'unavailable'}</Badge>
                        )}
                      </td>
                      <td
                        className={clsx(
                          'text-right font-semibold tnum',
                          realisedNet > 0 ? 'text-up' : realisedNet < 0 ? 'text-down' : 'text-content-300',
                        )}
                      >
                        {formatSignedMoney(realisedNet)}
                        <div className="text-[10px] font-normal text-content-400">
                          fees {formatMoney(row.fees)}, funding {formatSignedMoney(row.funding)}
                        </div>
                      </td>
                      <td className="text-right tnum text-content-300">{formatSignedMoney(row.unrealisedPnl)}</td>
                      <td>
                        <StatusBadge status={row.orderStatus} />
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel className="overflow-hidden">
        <PanelHeader title="Open hedge alerts" description="Venue disagreements are never corrected silently" />
        {(alertsQuery.data ?? []).length === 0 ? (
          <p className="px-4 py-3 text-xs text-content-400">
            {alertsQuery.isError ? <Unavailable /> : 'No open alerts'}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[900px]">
              <thead>
                <tr>
                  <th>Code</th>
                  <th>Message</th>
                  <th className="text-right">Seen</th>
                  <th>Last seen</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(alertsQuery.data ?? []).map((alert) => (
                  <tr key={alert.id}>
                    <td>
                      <Badge tone={alert.severity === 'critical' ? 'critical' : 'warn'}>{alert.code}</Badge>
                    </td>
                    <td className="text-content-200">{alert.message}</td>
                    <td className="text-right tnum text-content-300">{alert.occurrences}</td>
                    <td className="text-[11px] text-content-400">{formatDateTime(alert.lastSeenAt)}</td>
                    <td className="text-right">
                      {can('hedge:write') ? (
                        <button
                          className="btn-ghost px-2 py-1 text-[11px]"
                          disabled={resolveMutation.isPending}
                          onClick={() => {
                            const note = window.prompt('What did the investigation find?');
                            if (note && note.trim().length >= 3) resolveMutation.mutate({ id: alert.id, note: note.trim() });
                          }}
                        >
                          Resolve
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel className="overflow-hidden">
        <PanelHeader title="Hedge orders" description="Every order carries the decision that produced it; only fills count as protection" />

        {ordersQuery.isLoading ? (
          <TableSkeleton rows={8} columns={10} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[1300px]">
              <thead>
                <tr>
                  <th>Reference</th>
                  <th>Asset</th>
                  <th>Side</th>
                  <th className="text-right">Target qty</th>
                  <th className="text-right">Filled qty</th>
                  <th className="text-right">Remaining</th>
                  <th className="text-right">Avg price</th>
                  <th className="text-right">Fee</th>
                  <th className="text-right">Cost bps/side est. / real</th>
                  <th>Status</th>
                  <th>Created</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(ordersQuery.data?.items ?? []).map((order) => (
                  <tr key={order.id}>
                    <td>
                      <div className="font-mono text-[11px] text-content-300">{order.reference}</div>
                      {order.clientOrderId ? (
                        <div className="font-mono text-[10px] text-content-400">{order.clientOrderId}</div>
                      ) : null}
                    </td>
                    <td className="font-medium text-content-100">{order.symbol}</td>
                    <td>
                      <Badge tone={order.side === 'UP' ? 'up' : 'down'}>
                        {order.side === 'UP' ? 'BUY' : 'SELL'}
                      </Badge>
                    </td>
                    <td className="text-right tnum text-content-200">
                      {order.targetQty !== undefined ? qty(order.targetQty) : formatMoney(order.requestedNotional)}
                    </td>
                    <td className="text-right tnum text-content-200">
                      {order.filledQty !== undefined ? qty(order.filledQty) : formatMoney(order.filledNotional)}
                      {order.fillCount ? <div className="text-[10px] text-content-400">{order.fillCount} fills</div> : null}
                    </td>
                    <td className="text-right tnum text-content-400">{qty(order.remainingQty)}</td>
                    <td className="text-right tnum text-content-300">
                      {order.averageFillPrice !== null ? order.averageFillPrice.toFixed(2) : '--'}
                    </td>
                    <td className="text-right tnum text-content-300">{order.feeInr ? formatMoney(order.feeInr) : '--'}</td>
                    <td className="text-right tnum text-content-400">
                      {order.estimatedCostBps !== undefined ? (order.estimatedCostBps / 2).toFixed(2) : '--'}
                      {' / '}
                      {order.realisedCostBps !== undefined && order.realisedCostBps !== null ? order.realisedCostBps.toFixed(2) : '--'}
                    </td>
                    <td>
                      <StatusBadge status={order.status} />
                      {order.working ? <div className="text-[10px] text-warn">working - contingent</div> : null}
                      {order.error ? <div className="text-[10px] text-down">{order.error}</div> : null}
                    </td>
                    <td className="text-[11px] text-content-400">{formatDateTime(order.createdAt)}</td>
                    <td className="text-right">
                      {can('hedge:write') && (order.working ?? (order.status === 'PENDING' || order.status === 'PARTIALLY_FILLED')) ? (
                        <button
                          className="btn-ghost px-2 py-1 text-[11px]"
                          disabled={cancelMutation.isPending}
                          onClick={() => cancelMutation.mutate(order.id)}
                        >
                          Cancel
                        </button>
                      ) : null}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        <Pagination
          page={ordersQuery.data?.page ?? 1}
          totalPages={ordersQuery.data?.totalPages ?? 1}
          onChange={setPage}
        />
      </Panel>
    </div>
  );
}
