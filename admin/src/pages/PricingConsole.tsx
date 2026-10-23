import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatExpiry } from '../utils/format';
import { Badge, Panel, PanelHeader, TableSkeleton } from '../components/ui';
import type { PricingSide } from '../types/api';

/**
 * The pricing engine, live (docs/audit/pricing/pricing-spec.md).
 *
 * What a customer would be offered right now on every payout row - per
 * direction, with the terms behind it - and the kill switch: halting a symbol
 * (or everything) stops new offers and acceptances; open trades still settle.
 * Halts are versioned risk-configuration changes and are audited.
 */
export function PricingConsole() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const board = useQuery({ queryKey: ['pricing'], queryFn: () => api.pricing.board(), refetchInterval: 2000 });
  const [symbol, setSymbol] = useState('');
  const [reason, setReason] = useState('');

  const switchMutation = useMutation({
    mutationFn: (input: { halt: boolean; symbol?: string; reason?: string }) =>
      input.halt
        ? api.pricing.halt({ symbol: input.symbol, reason: input.reason ?? '' })
        : api.pricing.resume({ symbol: input.symbol }),
    onSuccess: (_, input) => {
      void queryClient.invalidateQueries({ queryKey: ['pricing'] });
      setReason('');
      toast('success', (input.halt ? 'Halted ' : 'Resumed ') + (input.symbol || 'all trading'));
    },
    onError: (err) => toast('error', 'Kill switch failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const data = board.data;
  const symbols = Array.from(new Set((data?.rows ?? []).map((row) => row.symbol)));
  const writable = can('risk:write');

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Pricing engine"
        description={
          data
            ? data.mode +
              ' · rules ' +
              data.ruleLabel +
              ' · config v' +
              data.riskConfigVersion +
              ' · guard model ' +
              (data.model ? 'v' + data.model.version : 'none') +
              ' · hedge cost ' +
              data.hedgeEstimator.replace(/^.*\./, '')
            : 'Live offer terms per asset, expiry and direction'
        }
        actions={
          data?.tradingHalted ? (
            <Badge tone="critical">All trading halted: {data.tradingHaltReason || 'no reason given'}</Badge>
          ) : (
            <Badge tone="up">Trading live</Badge>
          )
        }
      />

      <div className="flex flex-wrap items-end gap-2 border-b border-surface-800 p-4 text-[11px]">
        {(data?.halts ?? []).map((halt) => (
          <span key={halt.symbol} className="inline-flex items-center gap-2">
            <Badge tone="warn">
              {halt.symbol} halted: {halt.reason}
            </Badge>
            {writable ? (
              <button
                type="button"
                className="btn-ghost px-2 py-1 text-[11px]"
                disabled={switchMutation.isPending}
                onClick={() => switchMutation.mutate({ halt: false, symbol: halt.symbol })}
              >
                Resume
              </button>
            ) : null}
          </span>
        ))}
        {writable ? (
          <form
            className="ml-auto flex flex-wrap items-end gap-2"
            onSubmit={(event) => {
              event.preventDefault();
              switchMutation.mutate({ halt: true, symbol: symbol || undefined, reason });
            }}
          >
            <select className="field w-36" value={symbol} onChange={(e) => setSymbol(e.target.value)}>
              <option value="">All symbols</option>
              {symbols.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
            <input
              className="field w-56"
              placeholder="Reason (required)"
              value={reason}
              minLength={3}
              required
              onChange={(e) => setReason(e.target.value)}
            />
            <button type="submit" className="btn-primary" disabled={switchMutation.isPending}>
              Halt
            </button>
            {data?.tradingHalted ? (
              <button
                type="button"
                className="btn-ghost"
                disabled={switchMutation.isPending}
                onClick={() => switchMutation.mutate({ halt: false })}
              >
                Resume all
              </button>
            ) : null}
          </form>
        ) : null}
      </div>

      {board.isLoading ? (
        <TableSkeleton rows={8} columns={10} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[1100px]">
            <thead>
              <tr>
                <th>Asset</th>
                <th>Expiry</th>
                <th className="text-right">Base</th>
                <th className="text-right">Min / max</th>
                <th className="text-right">Margin</th>
                <th className="text-right">UP</th>
                <th className="text-right">DOWN</th>
                <th className="text-right">Vol ratio</th>
                <th className="text-right">Spread</th>
                <th className="text-right">Deductions UP</th>
                <th className="text-right">Guard edge UP</th>
                <th>Rule version</th>
              </tr>
            </thead>
            <tbody>
              {(data?.rows ?? []).map((row) => {
                const c = row.up.components;
                return (
                  <tr key={row.payoutConfigId}>
                    <td className="font-medium text-content-100">
                      {row.symbol}
                      {!row.symbolEnabled ? (
                        <span className="ml-2 text-[10px] text-content-400">{row.disabledReason || 'not priced'}</span>
                      ) : null}
                    </td>
                    <td className="text-content-200">{formatExpiry(row.expirySeconds)}</td>
                    <td className="text-right tnum text-content-300">{row.basePayoutPercent}%</td>
                    <td className="text-right tnum text-content-300">
                      {row.minPayoutPercent}% / {row.maxPayoutPercent}%
                    </td>
                    <td className="text-right tnum text-content-300">{row.marginPercent}%</td>
                    <SideCell side={row.up} />
                    <SideCell side={row.down} />
                    <td className="text-right tnum text-content-300">{c.volRatio ? c.volRatio.toFixed(2) : '-'}</td>
                    <td className="text-right tnum text-content-300">{c.spreadBps ? c.spreadBps.toFixed(3) + ' bps' : '-'}</td>
                    <td
                      className="text-right tnum text-content-400"
                      title={
                        'vol ' + pts(c.volAdj) + ', spread ' + pts(c.spreadAdj) + ', hedge ' + pts(c.hedgeAdj) +
                        ', hedge outage ' + pts(c.hedgeUnavailableAdj) + ', exposure ' + pts(c.exposureAdj) +
                        ', margin ' + pts(c.marginAdj)
                      }
                    >
                      {pts(c.volAdj + c.spreadAdj + c.hedgeAdj + c.hedgeUnavailableAdj + c.exposureAdj + c.marginAdj)}
                    </td>
                    <td className="text-right tnum text-content-400" title={row.up.guard.reason ?? ''}>
                      {row.up.guard.enabled ? (row.up.guard.pointEdge * 100).toFixed(1) + '%' : '-'}
                    </td>
                    <td className="text-[10px] text-content-400">{row.ruleVersion || '-'}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}

/** Payout points of a fraction: 0.035 -> "3.50". */
function pts(fraction: number): string {
  return (fraction * 100).toFixed(2);
}

function SideCell({ side }: { side: PricingSide }) {
  return side.ok ? (
    <td className="text-right font-semibold tnum text-up">{side.payoutPercent}%</td>
  ) : (
    <td className="text-right text-[11px] text-down" title={side.reason ?? ''}>
      {side.code ?? 'blocked'}
    </td>
  );
}
