import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { formatDateTime, formatMoney } from '../utils/format';
import { AccountBadge, Pagination, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';

/** Customer wallet overview. */
export function WalletsPage() {
  const [page, setPage] = useState(1);
  const query = useQuery({
    queryKey: ['wallets', page],
    queryFn: () => api.finance.wallets({ page, limit: 25 }),
    placeholderData: (previous) => previous,
  });

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Customer wallets"
        description="Balances held on behalf of customers; locked funds back open trades and pending withdrawals"
      />

      {query.isLoading ? (
        <TableSkeleton rows={10} columns={5} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[820px]">
            <thead>
              <tr>
                <th>User</th>
                <th>Account</th>
                <th>Status</th>
                <th className="text-right">Balance</th>
                <th className="text-right">Locked</th>
                <th className="text-right">Available</th>
                <th>Updated</th>
              </tr>
            </thead>
            <tbody>
              {(query.data?.items ?? []).map((wallet) => {
                const user = typeof wallet.userId === 'string' ? null : wallet.userId;
                const available = wallet.balance - wallet.lockedBalance;
                return (
                  <tr key={wallet._id}>
                    <td>
                      {user ? (
                        <Link to={'/admin/users/' + user._id} className="font-medium text-content-100 hover:text-accent">
                          {user.username}
                        </Link>
                      ) : (
                        <span className="font-mono text-[11px] text-content-400">{String(wallet.userId)}</span>
                      )}
                      {user ? <div className="text-[11px] text-content-400">{user.email}</div> : null}
                    </td>
                    <td>
                      <AccountBadge accountType={wallet.accountType} />
                    </td>
                    <td>{user ? <StatusBadge status={user.status} /> : null}</td>
                    <td className="text-right font-semibold tnum text-content-100">
                      {formatMoney(wallet.balance, wallet.currency)}
                    </td>
                    <td className="text-right tnum text-warn">{formatMoney(wallet.lockedBalance, wallet.currency)}</td>
                    <td className="text-right tnum text-content-200">{formatMoney(available, wallet.currency)}</td>
                    <td className="text-[11px] text-content-400">{formatDateTime(wallet.updatedAt)}</td>
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
