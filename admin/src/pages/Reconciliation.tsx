import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime, formatMoney } from '../utils/format';
import { Badge, Pagination, Panel, PanelHeader, StatusBadge, TableSkeleton } from '../components/ui';
import type { ReconciliationRun } from '../types/api';

/**
 * Reconciliation runs (spec section 42).
 *
 * A mismatch raises an alert for a human to investigate; nothing here corrects
 * a balance automatically, because a silent correction would hide the defect
 * that caused the drift.
 */
export function ReconciliationPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [page, setPage] = useState(1);
  const [expanded, setExpanded] = useState<string | null>(null);

  const query = useQuery({
    queryKey: ['reconciliation', page],
    queryFn: () => api.reconciliation.list({ page, limit: 15 }),
    placeholderData: (previous) => previous,
  });

  const runMutation = useMutation({
    mutationFn: () => api.reconciliation.run(),
    onSuccess: (run) => {
      void queryClient.invalidateQueries({ queryKey: ['reconciliation'] });
      if (run.status === 'OK') toast('success', 'Reconciliation passed', 'All ledgers agree');
      else toast('error', 'Reconciliation alert', run.alerts[0] ?? 'See the run for details');
    },
    onError: (err) => toast('error', 'Run failed', err instanceof ApiRequestError ? err.message : undefined),
  });

  const resolveMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) => api.reconciliation.resolve(id, note),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['reconciliation'] });
      toast('success', 'Marked as investigated');
    },
    onError: (err) => toast('error', 'Could not resolve', err instanceof ApiRequestError ? err.message : undefined),
  });

  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Reconciliation"
        description="Wallet, trade, settlement and hedge ledgers compared against each other"
        actions={
          <button className="btn-primary" onClick={() => runMutation.mutate()} disabled={runMutation.isPending}>
            Run now
          </button>
        }
      />

      {query.isLoading ? (
        <TableSkeleton rows={6} columns={5} />
      ) : (
        <div className="overflow-x-auto">
          <table className="table min-w-[820px]">
            <thead>
              <tr>
                <th>Reference</th>
                <th>Status</th>
                <th>Checks</th>
                <th>Run at</th>
                <th className="text-right">Duration</th>
                <th>Resolved</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {(query.data?.items ?? []).flatMap((run) => {
                const rows = [
                  <tr key={run._id}>
                    <td className="font-mono text-[11px] text-content-300">{run.reference}</td>
                    <td>
                      <StatusBadge status={run.status} />
                    </td>
                    <td>
                      <div className="flex flex-wrap gap-1">
                        {run.checks.map((check) => (
                          <Badge key={check.name} tone={check.passed ? 'up' : 'down'}>
                            {check.name.replace(/_/g, ' ')}
                          </Badge>
                        ))}
                      </div>
                    </td>
                    <td className="text-[11px] text-content-400">{formatDateTime(run.createdAt)}</td>
                    <td className="text-right tnum text-content-400">{run.durationMs} ms</td>
                    <td className="text-[11px] text-content-400">
                      {run.resolvedAt ? formatDateTime(run.resolvedAt) : run.status === 'OK' ? '--' : 'open'}
                    </td>
                    <td className="text-right">
                      <div className="flex justify-end gap-1">
                        <button
                          className="btn-ghost px-2 py-1 text-[11px]"
                          onClick={() => setExpanded(expanded === run._id ? null : run._id)}
                        >
                          Details
                        </button>
                        {run.status !== 'OK' && !run.resolvedAt && can('reconciliation:resolve') ? (
                          <button
                            className="btn-primary px-2 py-1 text-[11px]"
                            onClick={() => {
                              const note = window.prompt('What did the investigation find?');
                              if (note && note.trim().length >= 3) {
                                resolveMutation.mutate({ id: run._id, note: note.trim() });
                              }
                            }}
                          >
                            Resolve
                          </button>
                        ) : null}
                      </div>
                    </td>
                  </tr>,
                ];

                if (expanded === run._id) rows.push(<RunDetail key={run._id + '-detail'} run={run} />);
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

/** Expanded row: the individual checks that made up one run. */
function RunDetail({ run }: { run: ReconciliationRun }) {
  return (
    <tr>
      <td colSpan={7} className="bg-surface-850/60 p-3">
        <table className="table">
          <thead>
            <tr>
              <th>Check</th>
              <th className="text-right">Expected</th>
              <th className="text-right">Actual</th>
              <th className="text-right">Difference</th>
              <th>Result</th>
            </tr>
          </thead>
          <tbody>
            {run.checks.map((check) => (
              <tr key={check.name}>
                <td className="text-content-200">{check.name.replace(/_/g, ' ')}</td>
                <td className="text-right tnum text-content-300">{formatMoney(check.expected)}</td>
                <td className="text-right tnum text-content-300">{formatMoney(check.actual)}</td>
                <td
                  className={clsx(
                    'text-right font-semibold tnum',
                    check.difference === 0 ? 'text-content-400' : 'text-down',
                  )}
                >
                  {formatMoney(check.difference)}
                </td>
                <td>
                  <Badge tone={check.passed ? 'up' : 'down'}>{check.passed ? 'PASS' : 'FAIL'}</Badge>
                </td>
              </tr>
            ))}
          </tbody>
        </table>

        {run.alerts.length > 0 ? (
          <ul className="mt-3 space-y-1 text-[11px] text-down">
            {run.alerts.map((alert) => (
              <li key={alert}>RECONCILIATION_ALERT: {alert}</li>
            ))}
          </ul>
        ) : null}

        {run.resolutionNote ? (
          <p className="mt-3 text-[11px] text-content-300">Resolution: {run.resolutionNote}</p>
        ) : null}
      </td>
    </tr>
  );
}
