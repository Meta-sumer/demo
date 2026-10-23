import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { formatDateTime, formatMoney, formatPrice, formatSignedMoney } from '../utils/format';
import { isVoided, VOID_LABEL } from '../utils/tradeStatus';
import { AccountBadge, Badge, Pagination, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';

/** All customer trades (spec section 19). */
export function TradesPage() {
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [direction, setDirection] = useState('');

  const query = useQuery({
    queryKey: ['admin', 'trades', { page, status, direction }],
    queryFn: () =>
      api.trades.list({
        page,
        limit: 25,
        status: status || undefined,
        direction: direction || undefined,
      }),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
  });

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Binary trades"
        description={query.data ? query.data.total + ' trades' : undefined}
        actions={
          <div className="flex gap-2">
            <select
              className="field w-36 py-1.5"
              value={status}
              onChange={(event) => {
                setStatus(event.target.value);
                setPage(1);
              }}
            >
              <option value="">All statuses</option>
              <option value="OPEN">Open</option>
              <option value="SETTLING">Settling</option>
              <option value="WON">Won</option>
              <option value="LOST">Lost</option>
              <option value="DRAW">Draw / void</option>
              <option value="CANCELLED">Cancelled</option>
            </select>
            <select
              className="field w-32 py-1.5"
              value={direction}
              onChange={(event) => {
                setDirection(event.target.value);
                setPage(1);
              }}
            >
              <option value="">Both sides</option>
              <option value="UP">UP</option>
              <option value="DOWN">DOWN</option>
            </select>
          </div>
        }
      />

      {query.isLoading ? (
        <TableSkeleton rows={12} columns={9} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[1100px]">
            <thead>
              <tr>
                <th>Reference</th>
                <th>Asset</th>
                <th>Account</th>
                <th>Side</th>
                <th className="text-right">Stake</th>
                <th className="text-right">Payout</th>
                <th className="text-right">Entry</th>
                <th className="text-right">Expiry</th>
                <th>Status</th>
                <th className="text-right">Platform result</th>
                <th>Placed</th>
              </tr>
            </thead>
            <tbody>
              {(query.data?.items ?? []).map((trade) => {
                const platformResult =
                  trade.status === 'WON' ? -trade.profit : trade.status === 'LOST' ? trade.stake : 0;
                // Demo trades are practice money: never a platform result.
                const settled =
                  trade.accountType !== 'DEMO' &&
                  (trade.status === 'WON' || trade.status === 'LOST' || trade.status === 'DRAW');
                return (
                  <tr key={trade.id}>
                    <td>
                      <Link
                        to={'/admin/binary/trades/' + trade.id}
                        className="font-mono text-[11px] text-content-200 hover:text-accent"
                      >
                        {trade.reference}
                      </Link>
                    </td>
                    <td className="font-medium text-content-100">{trade.symbol}</td>
                    <td>
                      <AccountBadge accountType={trade.accountType} />
                    </td>
                    <td>
                      <Badge tone={trade.direction === 'UP' ? 'up' : 'down'}>{trade.direction}</Badge>
                    </td>
                    <td className="text-right tnum text-content-200">{formatMoney(trade.stake)}</td>
                    <td className="text-right tnum text-content-300">{trade.payoutPercent}%</td>
                    <td className="text-right tnum text-content-300">{formatPrice(trade.entryPrice, 2)}</td>
                    <td className="text-right tnum text-content-300">
                      {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, 2) : '--'}
                    </td>
                    <td>
                      {isVoided(trade) ? (
                        <span title={trade.voidReason ?? ''}>
                          <Badge tone="warn">{VOID_LABEL}</Badge>
                        </span>
                      ) : (
                        <StatusBadge status={trade.status} />
                      )}
                    </td>
                    <td
                      className={clsx(
                        'text-right font-semibold tnum',
                        platformResult > 0 ? 'text-up' : platformResult < 0 ? 'text-down' : 'text-content-300',
                      )}
                    >
                      {settled ? formatSignedMoney(platformResult) : '--'}
                    </td>
                    <td className="text-[11px] text-content-400">{formatDateTime(trade.createdAt)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      <Pagination page={query.data?.page ?? 1} totalPages={query.data?.totalPages ?? 1} onChange={setPage} />
    </Panel>
  );
}
