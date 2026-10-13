import { Fragment, useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { toast } from '../../hooks/useToast';
import { useUsdtRefetchInterval } from '../../hooks/useUsdtLive';
import { EmptyState, Panel, PanelHeader, TableSkeleton } from '../../components/ui';
import { ConfirmDialog } from '../../components/overlay';
import {
  CopyableText,
  FilterField,
  JsonBlock,
  ListFooter,
  ModeBadge,
  ProcessingStatusBadge,
  QueryError,
  STICKY_ACTION_TD,
  STICKY_ACTION_TH,
  errorText,
  fullDate,
  useDebounced,
} from '../../components/usdt';
import { PROCESSING_STATUS_ORDER, PROCESSING_STATUSES } from '../../utils/usdt';
import type { WebhookEventView } from '../../types/usdt';

/**
 * Every BeeX callback as received, before and after processing.
 *
 * The payload is stored verbatim, so a failed event can be run through
 * processing again once whatever made it fail has been fixed.
 */
const EMPTY_FILTERS = { event: '', reference: '', processingStatus: '' };

export function UsdtWebhookEventsPage() {
  const queryClient = useQueryClient();
  const can = useAuthStore((s) => s.can);
  const canReprocess = can('deposits:approve');
  const interval = useUsdtRefetchInterval();
  const [filters, setFilters] = useState(EMPTY_FILTERS);
  const [page, setPage] = useState(1);
  const [expanded, setExpanded] = useState<string | null>(null);
  const [reprocessing, setReprocessing] = useState<WebhookEventView | null>(null);
  const [reprocessError, setReprocessError] = useState<string | null>(null);
  const applied = useDebounced(filters);

  const query = useQuery({
    queryKey: ['usdt', 'webhook-events', { ...applied, page }],
    queryFn: () =>
      api.usdt.webhookEvents({
        event: applied.event.trim(),
        reference: applied.reference.trim(),
        processingStatus: applied.processingStatus,
        page,
        limit: 25,
      }),
    refetchInterval: interval,
    placeholderData: (previous) => previous,
  });

  const reprocess = useMutation({
    mutationFn: (id: string) => api.usdt.reprocessWebhookEvent(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ['usdt'] });
      setReprocessing(null);
      setReprocessError(null);
      toast('success', 'Event reprocessed', 'Its processing status has been updated');
    },
    onError: (err) => {
      const message = errorText(err, 'The event was not reprocessed. Try again.');
      setReprocessError(message);
      toast('error', 'Reprocess failed', message);
    },
  });

  const set = (key: keyof typeof EMPTY_FILTERS) => (value: string) => {
    setFilters((current) => ({ ...current, [key]: value }));
    setPage(1);
  };

  const filtered = Object.values(filters).some(Boolean);
  const rows = query.data?.items ?? [];

  return (
    <>
      <Panel className="overflow-hidden">
        <PanelHeader
          title="BeeX webhook events"
          description="Callbacks exactly as received. Failed events can be reprocessed once the cause is fixed."
          actions={
            <>
              {filters.processingStatus !== 'failed' ? (
                <button className="btn-ghost px-3 py-1.5 text-xs" onClick={() => set('processingStatus')('failed')}>
                  Show failed
                </button>
              ) : null}
              {filtered ? (
                <button
                  className="btn-ghost px-3 py-1.5 text-xs"
                  onClick={() => {
                    setFilters(EMPTY_FILTERS);
                    setPage(1);
                  }}
                >
                  Clear filters
                </button>
              ) : null}
            </>
          }
        />

        <div className="grid grid-cols-1 gap-3 border-b border-surface-800 px-4 py-3 sm:grid-cols-3">
          <FilterField label="Event" htmlFor="wh-event">
            <input
              id="wh-event"
              className="field py-1.5 font-mono"
              placeholder="e.g. invoice.paid"
              value={filters.event}
              onChange={(e) => set('event')(e.target.value)}
            />
          </FilterField>
          <FilterField label="Reference" htmlFor="wh-ref">
            <input
              id="wh-ref"
              className="field py-1.5 font-mono"
              value={filters.reference}
              onChange={(e) => set('reference')(e.target.value)}
            />
          </FilterField>
          <FilterField label="Processing status" htmlFor="wh-status">
            <select
              id="wh-status"
              className="field py-1.5"
              value={filters.processingStatus}
              onChange={(e) => set('processingStatus')(e.target.value)}
            >
              <option value="">All</option>
              {PROCESSING_STATUS_ORDER.map((status) => (
                <option key={status} value={status}>
                  {PROCESSING_STATUSES[status].label}
                </option>
              ))}
            </select>
          </FilterField>
        </div>

        {query.isLoading ? (
          <TableSkeleton rows={8} columns={7} />
        ) : query.isError && !query.data ? (
          <QueryError error={query.error} onRetry={() => void query.refetch()} what="webhook events" />
        ) : rows.length === 0 ? (
          <EmptyState
            title="No webhook events match"
            description={filtered ? 'Try clearing a filter.' : 'BeeX has not called back yet.'}
          />
        ) : (
          <div className="relative overflow-x-auto">
            <table className="table min-w-[1180px]">
              <thead>
                <tr>
                  <th>Received</th>
                  <th>Event</th>
                  <th>Reference</th>
                  <th>Order ID</th>
                  <th>BeeX status</th>
                  <th>Mode</th>
                  <th>Processing</th>
                  <th className="text-right">Attempts</th>
                  <th>Error</th>
                  <th className={STICKY_ACTION_TH}>
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {rows.map((event) => (
                  <Fragment key={event.id}>
                    <tr className="group">
                      <td className="whitespace-nowrap text-[11px] text-content-400">{fullDate(event.receivedAt)}</td>
                      <td className="whitespace-nowrap font-mono text-[11px] text-content-100">{event.event}</td>
                      <td>
                        <CopyableText value={event.reference ?? event.clientReference} label="reference" />
                      </td>
                      <td>
                        <CopyableText value={event.orderId} label="order ID" />
                      </td>
                      <td className="text-[11px] text-content-200">{event.status ?? '--'}</td>
                      <td>
                        <ModeBadge mode={event.mode} />
                      </td>
                      <td>
                        <ProcessingStatusBadge status={event.processingStatus} />
                      </td>
                      <td className="text-right tnum text-content-300">{event.attempts}</td>
                      <td className="max-w-[240px]">
                        {event.error ? (
                          <span className="line-clamp-2 text-[11px] text-down" title={event.error}>
                            {event.error}
                          </span>
                        ) : (
                          <span className="text-[11px] text-content-400">--</span>
                        )}
                      </td>
                      <td className={'text-right ' + STICKY_ACTION_TD}>
                        <div className="flex justify-end gap-1">
                          <button
                            className="btn-ghost whitespace-nowrap px-2 py-1 text-[11px]"
                            aria-expanded={expanded === event.id}
                            onClick={() => setExpanded(expanded === event.id ? null : event.id)}
                          >
                            {expanded === event.id ? 'Hide payload' : 'Payload'}
                          </button>
                          {event.processingStatus === 'failed' && canReprocess ? (
                            <button
                              className="btn-primary whitespace-nowrap px-2 py-1 text-[11px]"
                              onClick={() => {
                                setReprocessError(null);
                                setReprocessing(event);
                              }}
                            >
                              Reprocess
                            </button>
                          ) : null}
                        </div>
                      </td>
                    </tr>
                    {expanded === event.id ? (
                      <tr>
                        <td colSpan={10} className="bg-surface-850/60 p-3">
                          <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-content-400">
                            <span className="font-mono">Event key {event.eventKey}</span>
                            {event.clientReference ? (
                              <span className="font-mono">Client reference {event.clientReference}</span>
                            ) : null}
                            <span>Signature time {formatSignatureTime(event.signatureTimestamp)}</span>
                            <span>Processed {fullDate(event.processedAt)}</span>
                          </div>
                          <JsonBlock value={event.payload} label="raw payload" />
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {query.data ? (
          <ListFooter
            total={query.data.total}
            noun="event"
            page={query.data.page}
            totalPages={query.data.totalPages}
            onPage={setPage}
            fetching={query.isFetching && !query.isLoading}
          />
        ) : null}
      </Panel>

      {reprocessing ? (
        <ConfirmDialog
          title="Reprocess webhook event"
          confirmLabel="Reprocess"
          pending={reprocess.isPending}
          error={reprocessError}
          onCancel={() => setReprocessing(null)}
          onConfirm={() => reprocess.mutate(reprocessing.id)}
        >
          <p>
            Run <span className="font-mono text-content-100">{reprocessing.event}</span> for{' '}
            <span className="font-mono text-content-100">
              {reprocessing.reference ?? reprocessing.orderId ?? reprocessing.id}
            </span>{' '}
            through processing again, using the payload stored on {fullDate(reprocessing.receivedAt)}.
          </p>
          {reprocessing.error ? <p className="text-down">Last error: {reprocessing.error}</p> : null}
          <p>
            Attempts so far: <span className="tnum">{reprocessing.attempts}</span>. The result replaces the current processing status.
          </p>
        </ConfirmDialog>
      ) : null}
    </>
  );
}

function formatSignatureTime(value: string | number | null): string {
  if (value == null || value === '') return '--';
  if (typeof value === 'number' || /^\d+$/.test(value)) {
    const n = Number(value);
    // BeeX-style signatures carry Unix seconds; tolerate milliseconds too.
    return fullDate(new Date(n < 1e12 ? n * 1000 : n).toISOString());
  }
  return fullDate(value);
}
