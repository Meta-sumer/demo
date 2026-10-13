import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { formatDateTime } from '../utils/format';
import { Badge, Pagination, Panel, PanelHeader, TableSkeleton } from '../components/ui';
import type { AuditLog } from '../types/api';

/**
 * Audit trail (spec section 30).
 *
 * Append-only: this screen reads, and there is no endpoint that edits or
 * deletes a row. Expanding an entry shows the before/after snapshot the server
 * recorded for the change.
 */
export function AuditLogsPage() {
  const [page, setPage] = useState(1);
  const [action, setAction] = useState('');
  const [resource, setResource] = useState('');
  const [expanded, setExpanded] = useState<string | null>(null);

  const query = useQuery({
    queryKey: ['audit', { page, action, resource }],
    queryFn: () =>
      api.audit.list({
        page,
        limit: 25,
        action: action || undefined,
        resource: resource || undefined,
      }),
    placeholderData: (previous) => previous,
  });

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Audit log"
        description={query.data ? query.data.total + ' recorded actions' : undefined}
        actions={
          <div className="flex gap-2">
            <input
              className="field w-44 py-1.5"
              placeholder="Action, e.g. trade.created"
              value={action}
              onChange={(event) => {
                setAction(event.target.value);
                setPage(1);
              }}
            />
            <input
              className="field w-36 py-1.5"
              placeholder="Resource"
              value={resource}
              onChange={(event) => {
                setResource(event.target.value);
                setPage(1);
              }}
            />
          </div>
        }
      />

      {query.isLoading ? (
        <TableSkeleton rows={12} columns={6} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[900px]">
            <thead>
              <tr>
                <th>When</th>
                <th>Actor</th>
                <th>Action</th>
                <th>Resource</th>
                <th>Result</th>
                <th>IP</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(query.data?.items ?? []).flatMap((entry) => {
                const rows = [
                  <tr key={entry._id}>
                    <td className="whitespace-nowrap text-[11px] text-content-400">{formatDateTime(entry.createdAt)}</td>
                    <td>
                      <div className="text-sm text-content-200">{entry.actorEmail ?? entry.actorRole}</div>
                      <div className="text-[10px] text-content-400">{entry.actorRole}</div>
                    </td>
                    <td className="font-mono text-[11px] text-content-100">{entry.action}</td>
                    <td className="text-[11px] text-content-300">
                      {entry.resource}
                      {entry.resourceId ? (
                        <span className="ml-1 font-mono text-content-400">{entry.resourceId.slice(-8)}</span>
                      ) : null}
                    </td>
                    <td>
                      <Badge tone={entry.success ? 'up' : 'down'}>{entry.success ? 'OK' : 'FAILED'}</Badge>
                      {entry.reason ? <div className="text-[10px] text-content-400">{entry.reason}</div> : null}
                    </td>
                    <td className="font-mono text-[11px] text-content-400">{entry.ip ?? '--'}</td>
                    <td className="text-right">
                      {entry.before || entry.after ? (
                        <button
                          className="btn-ghost px-2 py-1 text-[11px]"
                          onClick={() => setExpanded(expanded === entry._id ? null : entry._id)}
                        >
                          {expanded === entry._id ? 'Hide' : 'Diff'}
                        </button>
                      ) : null}
                    </td>
                  </tr>,
                ];

                if (expanded === entry._id) rows.push(<AuditDiff key={entry._id + '-diff'} entry={entry} />);
                return rows;
              })}
            </tbody>
          </table>
        </div>
      )}

      <Pagination page={query.data?.page ?? 1} totalPages={query.data?.totalPages ?? 1} onChange={setPage} />
    </Panel>
  );
}

function AuditDiff({ entry }: { entry: AuditLog }) {
  return (
    <tr>
      <td colSpan={7} className="bg-surface-850/60">
        <div className="grid gap-3 p-3 lg:grid-cols-2">
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-content-400">Before</div>
            <pre className="max-h-56 overflow-auto rounded-lg bg-surface-950 p-3 text-[11px] text-content-300">
              {JSON.stringify(entry.before, null, 2)}
            </pre>
          </div>
          <div>
            <div className="mb-1 text-[10px] uppercase tracking-wider text-content-400">After</div>
            <pre className="max-h-56 overflow-auto rounded-lg bg-surface-950 p-3 text-[11px] text-content-300">
              {JSON.stringify(entry.after, null, 2)}
            </pre>
          </div>
        </div>
      </td>
    </tr>
  );
}
