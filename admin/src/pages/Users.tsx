import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { formatDateTime, formatMoney } from '../utils/format';
import { Badge, Pagination, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';

export function UsersPage() {
  const [page, setPage] = useState(1);
  const [search, setSearch] = useState('');
  const [status, setStatus] = useState<string>('');

  const query = useQuery({
    queryKey: ['users', { page, search, status }],
    queryFn: () =>
      api.users.list({
        page,
        limit: 20,
        search: search || undefined,
        status: status || undefined,
      }),
    placeholderData: (previous) => previous,
  });

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Users"
        description={query.data ? query.data.total + ' accounts' : undefined}
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="field w-48 py-1.5"
              placeholder="Search email or username"
              value={search}
              onChange={(event) => {
                setSearch(event.target.value);
                setPage(1);
              }}
            />
            <select
              className="field w-36 py-1.5"
              value={status}
              onChange={(event) => {
                setStatus(event.target.value);
                setPage(1);
              }}
            >
              <option value="">All statuses</option>
              <option value="ACTIVE">Active</option>
              <option value="SUSPENDED">Suspended</option>
              <option value="CLOSED">Closed</option>
            </select>
          </div>
        }
      />

      {query.isLoading ? (
        <TableSkeleton rows={10} columns={6} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[860px]">
            <thead>
              <tr>
                <th>User</th>
                <th>Role</th>
                <th>Status</th>
                <th className="text-right">Trade cap</th>
                <th className="text-right">Exposure cap</th>
                <th>Last login</th>
                <th>Joined</th>
              </tr>
            </thead>
            <tbody>
              {(query.data?.items ?? []).map((user) => (
                <tr key={user.id}>
                  <td>
                    <Link to={'/admin/users/' + user.id} className="font-medium text-content-100 hover:text-accent">
                      {user.username}
                    </Link>
                    <div className="text-[11px] text-content-400">{user.email}</div>
                  </td>
                  <td>
                    <Badge tone={user.role === 'USER' ? 'neutral' : 'accent'}>{user.role}</Badge>
                  </td>
                  <td>
                    <StatusBadge status={user.status} />
                  </td>
                  <td className="text-right tnum text-content-300">
                    {user.maxTradeAmount !== null ? formatMoney(user.maxTradeAmount) : 'default'}
                  </td>
                  <td className="text-right tnum text-content-300">
                    {user.maxUserExposure !== null ? formatMoney(user.maxUserExposure) : 'default'}
                  </td>
                  <td className="text-[11px] text-content-400">
                    {user.lastLoginAt ? formatDateTime(user.lastLoginAt) : 'never'}
                  </td>
                  <td className="text-[11px] text-content-400">{formatDateTime(user.createdAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Pagination page={query.data?.page ?? 1} totalPages={query.data?.totalPages ?? 1} onChange={setPage} />
    </Panel>
  );
}
