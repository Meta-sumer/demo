import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { FeedBadge, Panel, PanelHeader, Skeleton, Stat, TableSkeleton } from '../components/ui';

/**
 * Market data console (spec sections 23 and 28).
 *
 * Latency is broken down by stage so a slow feed can be told apart from a slow
 * server, and every percentile is shown rather than an average that hides the
 * tail.
 */
const METRIC_LABELS: Record<string, string> = {
  'feed.provider_to_server': 'Provider to server',
  'feed.processing': 'Tick processing',
  'feed.total': 'Feed total',
  'trade.acceptance': 'Trade acceptance',
  'trade.persistence': 'Trade persistence',
  'expiry.detection': 'Expiry detection',
  'settlement.duration': 'Settlement',
  'ws.delivery': 'Websocket delivery',
  'hedge.request': 'Hedge request',
  'hedge.fill': 'Hedge fill',
};

export function MarketDataPage() {
  const query = useQuery({
    queryKey: ['market-data'],
    queryFn: api.marketData.overview,
    refetchInterval: 2000,
  });

  const data = query.data;
  const healthy = (data?.feeds ?? []).filter((f) => f.status === 'HEALTHY').length;

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Provider"
          value={data?.provider?.provider ?? <Skeleton className="h-6 w-20" />}
          hint={data?.provider?.state ?? undefined}
          tone={data?.provider?.state === 'CONNECTED' ? 'up' : 'warn'}
        />
        <Stat
          label="Healthy feeds"
          value={data ? healthy + ' / ' + data.feeds.length : <Skeleton className="h-6 w-16" />}
          tone={data && healthy === data.feeds.length ? 'up' : 'warn'}
        />
        <Stat
          label="Feed p95"
          value={
            data ? (data.latency.find((m) => m.metric === 'feed.total')?.p95 ?? 0) + ' ms' : <Skeleton className="h-6 w-16" />
          }
        />
        <Stat
          label="Feed p99"
          value={
            data ? (data.latency.find((m) => m.metric === 'feed.total')?.p99 ?? 0) + ' ms' : <Skeleton className="h-6 w-16" />
          }
        />
      </div>

      <Panel className="overflow-hidden">
        <PanelHeader
          title="Feed status by asset"
          description="A stale or offline feed automatically stops new trades on that asset"
        />

        {query.isLoading ? (
          <TableSkeleton rows={5} columns={7} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[900px]">
              <thead>
                <tr>
                  <th>Asset</th>
                  <th className="text-right">Last price</th>
                  <th>Provider time</th>
                  <th>Server time</th>
                  <th className="text-right">Feed latency</th>
                  <th className="text-right">Processing</th>
                  <th className="text-right">Age</th>
                  <th>Status</th>
                </tr>
              </thead>
              <tbody>
                {(data?.feeds ?? []).map((feed) => (
                  <tr key={feed.assetId}>
                    <td className="font-medium text-content-100">{feed.symbol}</td>
                    <td className="text-right tnum text-content-200">{feed.lastPrice ?? '--'}</td>
                    <td className="font-mono text-[11px] text-content-400">
                      {feed.providerTimestamp ? new Date(feed.providerTimestamp).toISOString().slice(11, 23) : '--'}
                    </td>
                    <td className="font-mono text-[11px] text-content-400">
                      {feed.serverReceivedAt ? new Date(feed.serverReceivedAt).toISOString().slice(11, 23) : '--'}
                    </td>
                    <td
                      className={clsx(
                        'text-right tnum',
                        (feed.feedLatencyMs ?? 0) > 300 ? 'text-down' : (feed.feedLatencyMs ?? 0) > 100 ? 'text-warn' : 'text-up',
                      )}
                    >
                      {feed.feedLatencyMs !== null ? feed.feedLatencyMs + ' ms' : '--'}
                    </td>
                    <td className="text-right tnum text-content-300">
                      {feed.processingLatencyMs !== null ? feed.processingLatencyMs + ' ms' : '--'}
                    </td>
                    <td className="text-right tnum text-content-300">
                      {feed.ageMs !== null ? feed.ageMs + ' ms' : '--'}
                    </td>
                    <td>
                      <FeedBadge status={feed.status} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>

      <Panel className="overflow-hidden">
        <PanelHeader title="Latency percentiles" description="Rolling window of the most recent samples" />
        {query.isLoading ? (
          <TableSkeleton rows={8} columns={6} />
        ) : (
          <div className="overflow-x-auto">
            <table className="table min-w-[760px]">
              <thead>
                <tr>
                  <th>Stage</th>
                  <th className="text-right">Samples</th>
                  <th className="text-right">Avg</th>
                  <th className="text-right">P50</th>
                  <th className="text-right">P95</th>
                  <th className="text-right">P99</th>
                  <th className="text-right">Max</th>
                </tr>
              </thead>
              <tbody>
                {(data?.latency ?? []).map((row) => (
                  <tr key={row.metric}>
                    <td className="text-content-100">{METRIC_LABELS[row.metric] ?? row.metric}</td>
                    <td className="text-right tnum text-content-400">{row.count}</td>
                    <td className="text-right tnum text-content-300">{row.avg} ms</td>
                    <td className="text-right tnum text-content-200">{row.p50} ms</td>
                    <td className="text-right tnum text-content-200">{row.p95} ms</td>
                    <td className="text-right tnum text-warn">{row.p99} ms</td>
                    <td className="text-right tnum text-content-400">{row.max} ms</td>
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
