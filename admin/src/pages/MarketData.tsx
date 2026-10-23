import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { api } from '../services/api';
import { Badge, FeedBadge, Panel, PanelHeader, Skeleton, Stat, TableSkeleton } from '../components/ui';
import type { FeedQualityReport } from '../types/api';

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

      {data?.quality ? <FeedQualityPanel quality={data.quality} /> : null}

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

const STATE_TONE: Record<string, 'up' | 'down' | 'warn' | 'neutral'> = {
  LIVE: 'up',
  QUIET: 'neutral',
  GAP: 'down',
  DISCONNECTED: 'down',
  NO_DATA: 'warn',
  NOT_EVALUATED: 'warn',
};

const time = (ms: number) => (ms > 0 ? new Date(ms).toISOString().slice(11, 23) : '--');

/**
 * Feed data rules (docs/audit/operations/feed-rules.md): a gap (nothing at all
 * from the provider) is told apart from a quiet market (heartbeats, no price
 * change); dropped and rejected ticks are counted; the clock is checked
 * against the provider's.
 */
function FeedQualityPanel({ quality }: { quality: FeedQualityReport }) {
  const clock = quality.clock;
  return (
    <Panel className="overflow-hidden">
      <PanelHeader
        title="Data quality"
        description={
          'Clock check: ' +
          (clock ? clock.state + ' (offset ' + clock.offsetMs + ' ms, drift ' + clock.driftMs + ' ms, ' + clock.source + ')' : 'not run') +
          ' · ingestion buffer high-water ' +
          quality.queueHighWater +
          ' / ' +
          quality.queueCap +
          ' · dropped ' +
          quality.droppedTotal
        }
      />
      <div className="overflow-x-auto">
        <table className="table min-w-[860px]">
          <thead>
            <tr>
              <th>Asset</th>
              <th>State</th>
              <th className="text-right">Expected interval</th>
              <th className="text-right">Gap after</th>
              <th className="text-right">Updates / heartbeats</th>
              <th className="text-right">Dropped</th>
              <th className="text-right">Rejected</th>
              <th>Blocked</th>
            </tr>
          </thead>
          <tbody>
            {quality.assets.map((row) => (
              <tr key={row.assetId}>
                <td className="font-medium text-content-100">{row.symbol}</td>
                <td>
                  <Badge tone={STATE_TONE[row.state] ?? 'neutral'}>{row.state}</Badge>
                  {row.gapOpen ? <span className="ml-1 text-[11px] text-down">since {time(row.gapStartedAt)}</span> : null}
                </td>
                <td className="text-right tnum text-content-300">{Math.round(row.expectedIntervalMs)} ms</td>
                <td className="text-right tnum text-content-300">{row.gapThresholdMs} ms</td>
                <td className="text-right tnum text-content-300">
                  {row.updates} / {row.heartbeats}
                </td>
                <td className={clsx('text-right tnum', row.droppedQueueFull + row.droppedInvalid > 0 ? 'text-warn' : 'text-content-400')}>
                  {row.droppedQueueFull + row.droppedInvalid}
                </td>
                <td className="text-right tnum text-content-400">{row.rejected.total ?? 0}</td>
                <td className="text-[11px] text-content-400">{row.block ? row.block.code + ': ' + row.block.detail : '--'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {quality.gaps.length > 0 ? (
        <div className="overflow-x-auto border-t border-surface-800">
          <table className="table min-w-[760px]">
            <thead>
              <tr>
                <th>Gap</th>
                <th>Reason</th>
                <th>Last data before (UTC)</th>
                <th>First data after (UTC)</th>
                <th className="text-right">Duration</th>
              </tr>
            </thead>
            <tbody>
              {quality.gaps.slice(0, 20).map((gap) => (
                <tr key={gap.id}>
                  <td className="font-medium text-content-100">
                    {gap.symbol} {gap.open ? <Badge tone="down">open</Badge> : null}
                  </td>
                  <td className="text-content-300">{gap.reason}</td>
                  <td className="font-mono text-[11px] text-content-400">{new Date(gap.startedAt).toISOString().slice(0, 23)}</td>
                  <td className="font-mono text-[11px] text-content-400">
                    {gap.endedAt ? new Date(gap.endedAt).toISOString().slice(0, 23) : '--'}
                  </td>
                  <td className="text-right tnum text-content-300">{gap.durationMs !== null ? gap.durationMs + ' ms' : '--'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : null}
    </Panel>
  );
}
