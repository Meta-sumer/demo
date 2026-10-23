import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../services/api';
import { ApiRequestError } from '../services/http';
import { useAuthStore } from '../stores/authStore';
import { toast } from '../hooks/useToast';
import { formatDateTime } from '../utils/format';
import { Badge, Pagination, Panel, PanelHeader, Stat, TableSkeleton } from '../components/ui';
import type { AlertSeverity, OpsAlert } from '../types/alerts';

const SEVERITY_TONE: Record<AlertSeverity, 'critical' | 'warn' | 'neutral'> = {
  critical: 'critical',
  warning: 'warn',
  info: 'neutral',
};

const SOURCE_LABEL: Record<string, string> = {
  usdt: 'USDT',
  hedging: 'Hedging',
  reconciliation: 'Reconciliation',
  exposure: 'Exposure book',
  feed: 'Market feed',
  settlement: 'Settlement',
  'rate-limiter': 'Rate limiter',
  system: 'System',
};

/** Where an alert mirrored from another page is worked on. */
const SOURCE_PAGE: Record<string, string> = {
  usdt: 'the USDT dashboard',
  hedging: 'the Hedging page',
  reconciliation: 'Reconciliation',
};

/**
 * Operational alerts (email alerting, docs/audit/operations/production-readiness.md section 10).
 *
 * One row per open problem; a repeat only raises its counter. Condition alerts (book, feed,
 * stuck settlements, rate limiter) close by themselves when the condition clears; money
 * alerts stay open until someone resolves them here with a note, which is audited.
 */
export function AlertsPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState<'open' | 'resolved' | ''>('open');
  const [severity, setSeverity] = useState<AlertSeverity | ''>('');
  const [expanded, setExpanded] = useState<string | null>(null);

  const query = useQuery({
    queryKey: ['alerts', status, severity, page],
    queryFn: () =>
      api.alerts.list({ status: status || undefined, severity: severity || undefined, page, limit: 20 }),
    placeholderData: (previous) => previous,
    refetchInterval: 30_000,
  });

  const statusQuery = useQuery({
    queryKey: ['alerts', 'status'],
    queryFn: () => api.alerts.status(),
    refetchInterval: 15_000,
  });

  const resolveMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) => api.alerts.resolve(id, note),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['alerts'] });
      toast('success', 'Alert resolved', 'The note is in the audit log');
    },
    onError: (err) => toast('error', 'Could not resolve', err instanceof ApiRequestError ? err.message : undefined),
  });

  const st = statusQuery.data;

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
        <Stat
          label="Email alerts"
          value={st ? (st.alerting.emailEnabled ? 'On' : 'Off') : '--'}
          tone={st ? (st.alerting.emailEnabled ? 'up' : 'warn') : 'neutral'}
          hint={
            st
              ? st.alerting.emailEnabled
                ? `${st.alerting.recipients} recipient(s), ${st.alerting.minSeverity} and above, ${st.alerting.sentLastHour}/${st.alerting.maxEmailsPerHour} this hour`
                : 'ALERT_EMAIL_ENABLED=false: stored and logged only'
              : undefined
          }
        />
        <Stat
          label="Open alerts"
          value={st ? st.alerting.open : '--'}
          tone={st && st.alerting.openCritical > 0 ? 'down' : 'neutral'}
          hint={
            st
              ? `${st.alerting.openCritical} critical, ${st.alerting.pendingNotifications} email(s) waiting` +
                (st.alerting.failedNotifications ? `, ${st.alerting.failedNotifications} failed` : '')
              : undefined
          }
        />
        <Stat
          label="Exposure book"
          value={st ? st.exposureBook.status : '--'}
          tone={st ? (st.exposureBook.status === 'READY' ? 'up' : 'down') : 'neutral'}
          hint={
            st
              ? st.exposureBook.status === 'READY'
                ? `${st.exposureBook.pendingReservations ?? 0} reservation(s) in flight`
                : st.exposureBook.error ?? st.exposureBook.reason ?? 'LIVE trades are refused until READY'
              : undefined
          }
        />
        <Stat
          label="Rate limiter"
          value={st ? (st.rateLimiter.mode === 'redis' ? 'Redis' : 'In memory') : '--'}
          tone={st ? (st.rateLimiter.mode === 'redis' ? 'up' : 'warn') : 'neutral'}
          hint={
            st
              ? st.rateLimiter.mode === 'redis'
                ? 'Limits shared by every instance'
                : `Fallback since ${formatDateTime(st.rateLimiter.degradedSince)}`
              : undefined
          }
        />
      </div>

      {st?.alerting.lastError ? (
        <div className="panel border-down/40 px-4 py-2 text-[11px] text-down">
          Last email error: {st.alerting.lastError}
        </div>
      ) : null}

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Alerts"
          description="Problems found by the monitors, reconciliation, USDT and hedging; emailed to the operators"
          actions={
            <>
              <select
                className="field w-auto py-1 text-xs"
                value={status}
                onChange={(e) => {
                  setStatus(e.target.value as typeof status);
                  setPage(1);
                }}
              >
                <option value="open">Open</option>
                <option value="resolved">Resolved</option>
                <option value="">All</option>
              </select>
              <select
                className="field w-auto py-1 text-xs"
                value={severity}
                onChange={(e) => {
                  setSeverity(e.target.value as typeof severity);
                  setPage(1);
                }}
              >
                <option value="">Any severity</option>
                <option value="critical">Critical</option>
                <option value="warning">Warning</option>
                <option value="info">Info</option>
              </select>
            </>
          }
        />

        {query.isLoading ? (
          <TableSkeleton rows={6} columns={6} />
        ) : (query.data?.items ?? []).length === 0 ? (
          <p className="px-4 py-8 text-center text-xs text-content-400">
            {status === 'open' ? 'No open alerts' : 'No alerts'}
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[860px]">
              <thead>
                <tr>
                  <th>Severity</th>
                  <th>Alert</th>
                  <th>Source</th>
                  <th className="text-right">Seen</th>
                  <th>First seen</th>
                  <th>Last seen</th>
                  <th>Status</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(query.data?.items ?? []).flatMap((alert) => {
                  const rows = [
                    <tr key={alert.id}>
                      <td>
                        <Badge tone={SEVERITY_TONE[alert.severity] ?? 'neutral'}>{alert.severity.toUpperCase()}</Badge>
                      </td>
                      <td>
                        <div className="text-content-100">{alert.title}</div>
                        <div className="font-mono text-[10px] text-content-400">{alert.code}</div>
                      </td>
                      <td className="text-[11px] text-content-300">{SOURCE_LABEL[alert.source] ?? alert.source}</td>
                      <td className="text-right tnum text-content-300">{alert.occurrences}</td>
                      <td className="text-[11px] text-content-400">{formatDateTime(alert.firstSeenAt)}</td>
                      <td className="text-[11px] text-content-400">{formatDateTime(alert.lastSeenAt)}</td>
                      <td>
                        {alert.status === 'open' ? (
                          <Badge tone="down">OPEN</Badge>
                        ) : (
                          <span className="text-[11px] text-content-400">
                            {alert.resolvedBySystem ? 'Cleared' : 'Resolved'}{' '}
                            {alert.resolvedAt ? formatDateTime(alert.resolvedAt) : ''}
                          </span>
                        )}
                      </td>
                      <td className="text-right">
                        <div className="flex justify-end gap-1">
                          <button
                            className="btn-ghost px-2 py-1 text-[11px]"
                            onClick={() => setExpanded(expanded === alert.id ? null : alert.id)}
                          >
                            Details
                          </button>
                          {alert.status === 'open' && can('reconciliation:resolve') ? (
                            <button
                              className="btn-primary px-2 py-1 text-[11px]"
                              disabled={resolveMutation.isPending}
                              onClick={() => {
                                const where = SOURCE_PAGE[alert.source];
                                const hint = where ? ` (the underlying item is worked on in ${where})` : '';
                                const note = window.prompt('What was found and done?' + hint);
                                if (note && note.trim().length >= 3) {
                                  resolveMutation.mutate({ id: alert.id, note: note.trim() });
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
                  if (expanded === alert.id) rows.push(<AlertDetail key={alert.id + '-detail'} alert={alert} />);
                  return rows;
                })}
              </tbody>
            </table>
          </div>
        )}

        <Pagination page={query.data?.page ?? 1} totalPages={query.data?.totalPages ?? 1} onChange={setPage} />
      </Panel>
    </div>
  );
}

/** Expanded row: the full detail, the identity and how it was closed. */
function AlertDetail({ alert }: { alert: OpsAlert }) {
  return (
    <tr>
      <td colSpan={8} className="bg-surface-850/60 p-3">
        <pre className="whitespace-pre-wrap break-words font-mono text-[11px] text-content-200">
          {alert.detail || 'No detail'}
        </pre>
        <div className="mt-2 flex flex-wrap gap-x-6 gap-y-1 text-[11px] text-content-400">
          <span>
            Key: <span className="font-mono">{alert.dedupKey}</span>
          </span>
          <span>Emailed: {alert.notified ? 'yes' : 'no'}</span>
          {alert.resolutionNote ? <span>Resolution: {alert.resolutionNote}</span> : null}
        </div>
      </td>
    </tr>
  );
}
