import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { formatDateTime, formatMoney, formatSignedMoney } from '../utils/format';
import { toast } from '../hooks/useToast';
import { ApiRequestError } from '../services/http';
import {
  Badge,
  Pagination,
  Panel,
  PanelHeader,
  RiskBadge,
  StatusBadge,
  TableSkeleton,
} from '../components/ui';

/**
 * Live hedging screen (spec section 22).
 *
 * Shows, per asset, what the risk engine wants hedged, what is actually hedged
 * at the venue, and what is left exposed - plus the order log underneath.
 */
export function HedgingPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [page, setPage] = useState(1);

  const monitorQuery = useQuery({
    queryKey: ['hedging', 'monitor'],
    queryFn: api.hedging.monitor,
    refetchInterval: 5000,
  });

  const ordersQuery = useQuery({
    queryKey: ['hedging', 'orders', page],
    queryFn: () => api.hedging.orders({ page, limit: 20 }),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
  });

  const cancelMutation = useMutation({
    mutationFn: (id: string) => api.hedging.cancel(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['hedging'] });
      toast('success', 'Hedge order cancelled');
    },
    onError: (err) =>
      toast('error', 'Could not cancel', err instanceof ApiRequestError ? err.message : undefined),
  });

  return (
    <div className="space-y-4">
      <Panel className="overflow-hidden">
        <PanelHeader
          title="Hedge coverage"
          description="Hedging manages aggregate book exposure only - it never affects a customer result"
        />

        {monitorQuery.isLoading ? (
          <TableSkeleton rows={5} columns={8} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[1000px]">
              <thead>
                <tr>
                  <th>Asset</th>
                  <th className="text-right">Net exposure</th>
                  <th className="text-right">Target hedge</th>
                  <th className="text-right">Current hedge</th>
                  <th className="text-right">Remaining</th>
                  <th>Risk</th>
                  <th>Decision</th>
                  <th>Provider</th>
                  <th>Last order</th>
                  <th className="text-right">Fill price</th>
                  <th className="text-right">Hedge P and L</th>
                </tr>
              </thead>
              <tbody>
                {(monitorQuery.data ?? []).map((row) => {
                  const coverage =
                    row.targetHedge > 0 ? Math.min(100, Math.round((row.currentHedge / row.targetHedge) * 100)) : 0;
                  return (
                    <tr key={row.assetId}>
                      <td>
                        <div className="font-medium text-content-100">{row.symbol}</div>
                        {!row.hedgeEnabled ? (
                          <div className="text-[10px] text-content-400">hedging disabled</div>
                        ) : null}
                      </td>
                      <td
                        className={clsx(
                          'text-right font-semibold tnum',
                          row.netExposure > 0 ? 'text-up' : row.netExposure < 0 ? 'text-down' : 'text-content-300',
                        )}
                      >
                        {formatSignedMoney(row.netExposure)}
                      </td>
                      <td className="text-right tnum text-content-200">{formatMoney(row.targetHedge)}</td>
                      <td className="text-right tnum text-accent">
                        {formatMoney(row.currentHedge)}
                        {row.targetHedge > 0 ? (
                          <span className="ml-1 text-[10px] text-content-400">{coverage}%</span>
                        ) : null}
                      </td>
                      <td className="text-right tnum text-warn">{formatMoney(row.remainingExposure)}</td>
                      <td>
                        <RiskBadge level={row.riskLevel} />
                      </td>
                      <td>
                        <Badge tone={row.hedgeDecision === 'NO_HEDGE' ? 'neutral' : 'accent'}>
                          {row.hedgeDecision}
                        </Badge>
                      </td>
                      <td className="text-content-300">{row.provider}</td>
                      <td>
                        <StatusBadge status={row.orderStatus} />
                      </td>
                      <td className="text-right tnum text-content-300">
                        {row.fillPrice !== null ? row.fillPrice.toFixed(2) : '--'}
                      </td>
                      <td
                        className={clsx(
                          'text-right font-semibold tnum',
                          row.hedgePnl > 0 ? 'text-up' : row.hedgePnl < 0 ? 'text-down' : 'text-content-300',
                        )}
                      >
                        {formatSignedMoney(row.hedgePnl)}
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
        <PanelHeader title="Hedge orders" description="Every order carries the risk decision that produced it" />

        {ordersQuery.isLoading ? (
          <TableSkeleton rows={8} columns={8} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[1100px]">
              <thead>
                <tr>
                  <th>Reference</th>
                  <th>Asset</th>
                  <th>Side</th>
                  <th className="text-right">Requested</th>
                  <th className="text-right">Filled</th>
                  <th className="text-right">Avg price</th>
                  <th>Status</th>
                  <th>Level</th>
                  <th className="text-right">Latency</th>
                  <th>Created</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(ordersQuery.data?.items ?? []).map((order) => (
                  <tr key={order.id}>
                    <td className="font-mono text-[11px] text-content-300">{order.reference}</td>
                    <td className="font-medium text-content-100">{order.symbol}</td>
                    <td>
                      <Badge tone={order.side === 'UP' ? 'up' : 'down'}>
                        {order.side === 'UP' ? 'BUY' : 'SELL'}
                      </Badge>
                    </td>
                    <td className="text-right tnum text-content-200">{formatMoney(order.requestedNotional)}</td>
                    <td className="text-right tnum text-content-200">{formatMoney(order.filledNotional)}</td>
                    <td className="text-right tnum text-content-300">
                      {order.averageFillPrice !== null ? order.averageFillPrice.toFixed(2) : '--'}
                    </td>
                    <td>
                      <StatusBadge status={order.status} />
                      {order.error ? <div className="text-[10px] text-down">{order.error}</div> : null}
                    </td>
                    <td className="text-[11px] text-content-400">{order.hedgeLevel}</td>
                    <td className="text-right tnum text-content-400">
                      {order.requestLatencyMs !== null ? order.requestLatencyMs + 'ms' : '--'}
                    </td>
                    <td className="text-[11px] text-content-400">{formatDateTime(order.createdAt)}</td>
                    <td className="text-right">
                      {can('hedge:write') && (order.status === 'PENDING' || order.status === 'PARTIALLY_FILLED') ? (
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
