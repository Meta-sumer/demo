import { useEffect } from 'react';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { useRiskStore } from '../stores/riskStore';
import { formatMoney, formatSignedMoney } from '../utils/format';
import { Panel, PanelHeader, RiskBadge, Stat, TableSkeleton } from '../components/ui';
import { StaleBadge, Unavailable } from '../components/freshness';
import { queryMaxAge } from '../utils/freshness';

/**
 * Live exposure book (spec section 21).
 *
 * The table is fed by the websocket: `exposure.updated` arrives on every trade
 * and settlement, so no refresh is needed. The query below is only the initial
 * load and a slow safety-net refetch.
 */
const EXPOSURE_REFRESH_MS = 15_000;

export function ExposurePage() {
  const exposure = useRiskStore((s) => s.exposure);
  const setExposure = useRiskStore((s) => s.setExposure);
  const lastUpdatedAt = useRiskStore((s) => s.lastUpdatedAt);

  const query = useQuery({
    queryKey: ['exposure'],
    queryFn: api.risk.exposure,
    refetchInterval: EXPOSURE_REFRESH_MS,
  });

  useEffect(() => {
    if (query.data) setExposure(query.data);
  }, [query.data, setExposure]);

  const data = exposure ?? query.data ?? null;
  // Websocket push or the safety-net refetch, whichever is newer; a figure
  // that could not be loaded reads "unavailable", never 0.
  const asOf = Math.max(lastUpdatedAt ?? 0, query.dataUpdatedAt || 0) || null;
  const missing = query.isError && !data ? <Unavailable /> : '--';

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Gross exposure"
          value={data ? formatMoney(data.totals.grossExposure) : missing}
          hint={<StaleBadge asOf={asOf} maxAgeMs={queryMaxAge(EXPOSURE_REFRESH_MS)} />}
        />
        <Stat
          label="Net exposure"
          value={data ? formatSignedMoney(data.totals.netExposure) : missing}
          tone={data && data.totals.netExposure !== 0 ? (data.totals.netExposure > 0 ? 'up' : 'down') : 'neutral'}
          hint="Positive means the book leans UP"
        />
        <Stat
          label="Potential liability"
          value={data ? formatMoney(data.totals.potentialLiability) : missing}
          tone="warn"
          hint="Worst-case net payout across both outcomes"
        />
        <Stat label="Open trades" value={data ? data.totals.openTrades : missing} />
      </div>

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Exposure by asset"
          description="Streamed over websocket - no refresh required"
          actions={
            asOf ? (
              <span className="text-[11px] text-content-400">
                updated {new Date(asOf).toLocaleTimeString('en-GB', { hour12: false })}
                <StaleBadge asOf={asOf} maxAgeMs={queryMaxAge(EXPOSURE_REFRESH_MS)} />
              </span>
            ) : query.isError ? (
              <Unavailable />
            ) : null
          }
        />

        {query.isLoading && !data ? (
          <TableSkeleton rows={6} columns={8} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[900px]">
              <thead>
                <tr>
                  <th>Asset</th>
                  <th className="text-right">UP</th>
                  <th className="text-right">DOWN</th>
                  <th className="text-right">NET</th>
                  <th className="text-right">Gross</th>
                  <th className="text-right">Liability</th>
                  <th className="text-right">Open</th>
                  <th className="text-right">Utilisation</th>
                  <th className="text-right">Hedge target</th>
                  <th>Risk</th>
                </tr>
              </thead>
              <tbody>
                {(data?.assets ?? []).map((asset) => (
                  <tr key={asset.assetId}>
                    <td className="font-medium text-content-100">{asset.symbol}</td>
                    <td className="text-right tnum text-up">{formatMoney(asset.upStake)}</td>
                    <td className="text-right tnum text-down">{formatMoney(asset.downStake)}</td>
                    <td
                      className={clsx(
                        'text-right font-semibold tnum',
                        asset.netExposure > 0 ? 'text-up' : asset.netExposure < 0 ? 'text-down' : 'text-content-300',
                      )}
                    >
                      {formatSignedMoney(asset.netExposure)}
                    </td>
                    <td className="text-right tnum text-content-200">{formatMoney(asset.grossExposure)}</td>
                    <td className="text-right tnum text-warn">{formatMoney(asset.potentialLiability)}</td>
                    <td className="text-right tnum text-content-300">{asset.openTrades}</td>
                    <td className="text-right">
                      {asset.risk ? (
                        <div className="flex items-center justify-end gap-2">
                          <div className="h-1.5 w-20 overflow-hidden rounded-full bg-surface-800">
                            <div
                              className={clsx(
                                'h-full rounded-full',
                                asset.risk.utilisationPercent > 80
                                  ? 'bg-critical'
                                  : asset.risk.utilisationPercent > 50
                                    ? 'bg-warn'
                                    : 'bg-up',
                              )}
                              style={{ width: Math.min(100, asset.risk.utilisationPercent) + '%' }}
                            />
                          </div>
                          <span className="w-12 text-right tnum text-content-300">
                            {asset.risk.utilisationPercent.toFixed(1)}%
                          </span>
                        </div>
                      ) : (
                        <span className="text-content-400">--</span>
                      )}
                    </td>
                    <td className="text-right tnum text-content-300">
                      {asset.risk?.hedgeTarget ? formatSignedMoney(asset.risk.hedgeTarget.targetSigned) : '--'}
                      {asset.risk?.hedgeTarget?.cappedBy ? (
                        <div className="text-[10px] text-content-400">capped: {asset.risk.hedgeTarget.cappedBy}</div>
                      ) : null}
                    </td>
                    <td>{asset.risk ? <RiskBadge level={asset.risk.level} /> : null}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </div>
  );
}
