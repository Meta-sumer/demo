import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { formatDateTime, formatExpiry } from '../utils/format';
import { Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';

/**
 * Payout table across every asset (spec section 24).
 *
 * Editing happens on the asset page, where a new version is published; this
 * screen is the platform-wide view of what is currently offered.
 */
export function PayoutsPage() {
  const query = useQuery({ queryKey: ['assets'], queryFn: api.assets.list });

  const rows = (query.data ?? []).flatMap((asset) =>
    asset.payouts
      .slice()
      .sort((a, b) => a.expirySeconds - b.expirySeconds)
      .map((payout) => ({ asset, payout })),
  );

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Payout configuration"
        description="Versioned per asset and expiry - a trade keeps the payout that was active when it was accepted"
      />

      {query.isLoading ? (
        <TableSkeleton rows={10} columns={6} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[800px]">
            <thead>
              <tr>
                <th>Asset</th>
                <th>Expiry</th>
                <th className="text-right">Payout</th>
                <th className="text-right">Version</th>
                <th>Effective from</th>
                <th>Asset status</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map(({ asset, payout }) => (
                <tr key={payout.id}>
                  <td className="font-medium text-content-100">{asset.symbol}</td>
                  <td className="text-content-200">{formatExpiry(payout.expirySeconds)}</td>
                  <td className="text-right font-semibold tnum text-up">{payout.payoutPercent}%</td>
                  <td className="text-right tnum text-content-400">v{payout.version}</td>
                  <td className="text-[11px] text-content-400">{formatDateTime(payout.effectiveFrom)}</td>
                  <td>
                    <StatusBadge status={asset.active ? 'ACTIVE' : 'DISABLED'} />
                  </td>
                  <td className="text-right">
                    <Link to={'/admin/assets/' + asset.id} className="btn-ghost px-2 py-1 text-[11px]">
                      Edit
                    </Link>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </Panel>
  );
}
