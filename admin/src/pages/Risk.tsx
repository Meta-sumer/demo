import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { formatMoney, formatSignedMoney } from '../utils/format';
import { Panel, PanelHeader, RiskBadge, TableSkeleton } from '../components/ui';

/**
 * Risk engine output (spec section 13).
 *
 * Each row shows not just the level but the reasons that produced it, because a
 * risk decision that cannot be explained cannot be audited.
 */
export function RiskPage() {
  const query = useQuery({
    queryKey: ['risk', 'assessments'],
    queryFn: api.risk.assessments,
    refetchInterval: 5000,
  });

  const configQuery = useQuery({ queryKey: ['risk', 'config'], queryFn: api.risk.config });
  const config = configQuery.data?.config;

  return (
    <div className="space-y-4">
      {config ? (
        <Panel className="flex flex-wrap items-center gap-x-6 gap-y-2 px-4 py-3 text-xs">
          <span className="text-content-400">
            Config version <span className="font-semibold text-content-100">v{config.version}</span>
          </span>
          <span className="text-content-400">
            Hedge threshold <span className="font-semibold text-content-100">{config.hedgeThresholdPercent}%</span>
          </span>
          <span className="text-content-400">
            Critical at <span className="font-semibold text-content-100">{config.criticalExposurePercent}%</span>
          </span>
          <span className="text-content-400">
            Draw policy <span className="font-semibold text-content-100">{config.drawPolicy}</span>
          </span>
          {config.tradingHalted ? (
            <span className="rounded-md bg-critical/20 px-2 py-0.5 font-semibold text-critical">TRADING HALTED</span>
          ) : null}
        </Panel>
      ) : null}

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Risk assessment by asset"
          description="Deterministic function of exposure, feed health and volatility"
        />

        {query.isLoading ? (
          <TableSkeleton rows={5} columns={7} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[1000px]">
              <thead>
                <tr>
                  <th>Asset</th>
                  <th>Level</th>
                  <th className="text-right">Score</th>
                  <th className="text-right">Utilisation</th>
                  <th className="text-right">Net</th>
                  <th className="text-right">Gross</th>
                  <th className="text-right">Liability</th>
                  <th>Hedge decision</th>
                  <th className="text-right">Target hedge</th>
                  <th>Reasons</th>
                </tr>
              </thead>
              <tbody>
                {(query.data ?? []).map((row) => (
                  <tr key={row.assetId}>
                    <td className="font-medium text-content-100">{row.symbol}</td>
                    <td>
                      <RiskBadge level={row.level} />
                    </td>
                    <td className="text-right tnum text-content-200">{row.score.toFixed(1)}</td>
                    <td
                      className={clsx(
                        'text-right tnum',
                        row.utilisationPercent > 80
                          ? 'text-critical'
                          : row.utilisationPercent > 50
                            ? 'text-warn'
                            : 'text-content-300',
                      )}
                    >
                      {row.utilisationPercent.toFixed(1)}%
                    </td>
                    <td className="text-right tnum text-content-200">{formatSignedMoney(row.netExposure)}</td>
                    <td className="text-right tnum text-content-300">{formatMoney(row.grossExposure)}</td>
                    <td className="text-right tnum text-warn">{formatMoney(row.potentialLiability)}</td>
                    <td className="text-[11px] font-medium text-content-200">{row.hedgeDecision}</td>
                    <td className="text-right tnum text-accent">{formatMoney(row.targetHedgeNotional)}</td>
                    <td className="max-w-[280px] text-[11px] text-content-400">{row.reasons.join('; ')}</td>
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
