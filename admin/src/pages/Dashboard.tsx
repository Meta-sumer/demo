import { useQuery } from '@tanstack/react-query';
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Legend,
  Line,
  LineChart,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { formatMoney, formatSignedMoney, fromMinor } from '../utils/format';
import { Panel, PanelHeader, Skeleton, Stat } from '../components/ui';
import { useChartPalette } from '../utils/theme-colors';

/**
 * Operations dashboard (spec section 20).
 *
 * Money is stored in minor units; the charts plot major units so the axis reads
 * in currency, and every tooltip formats through the same helper as the tables.
 */
/*
 * Categorical series colours.
 *
 * Chosen to stay distinguishable on both a near-black and a near-white ground,
 * and to survive the most common form of colour blindness - hue alone is never
 * the only difference between adjacent bars.
 */
const CHART_COLORS = ['#6366f1', '#0ea5e9', '#f59e0b', '#ec4899', '#14b8a6', '#a855f7'];

export function DashboardPage() {
  const summaryQuery = useQuery({
    queryKey: ['dashboard', 'summary'],
    queryFn: api.dashboard.summary,
    refetchInterval: 5000,
  });
  const seriesQuery = useQuery({
    queryKey: ['dashboard', 'series'],
    queryFn: () => api.dashboard.series(14),
    refetchInterval: 60_000,
  });
  const assetsQuery = useQuery({
    queryKey: ['dashboard', 'assets'],
    queryFn: () => api.dashboard.assets(7),
    refetchInterval: 60_000,
  });

  const palette = useChartPalette();

  // The server leaves out the figures a role may not read (a SUPPORT agent sees
  // activity, not the book), so their cards are not drawn at all.
  const can = useAuthStore((s) => s.can);
  const seeBook = can('exposure:read');
  const seePnl = can('reports:read');
  const seeDeposits = can('deposits:read');
  const seeWithdrawals = can('withdrawals:read');

  const axisStyle = { fontSize: 11, fill: palette.text };
  const tooltipStyle = {
    backgroundColor: palette.panel,
    border: '1px solid ' + palette.border,
    borderRadius: 8,
    fontSize: 12,
    color: palette.strongText,
    boxShadow: 'var(--shadow-raised)',
  };

  const summary = summaryQuery.data;
  const series = (seriesQuery.data ?? []).map((point) => ({
    ...point,
    volumeMajor: fromMinor(point.volume),
    resultMajor: fromMinor(point.platformResult),
    payoutMajor: fromMinor(point.customerPayout),
  }));

  const winLoss = series.reduce(
    (acc, point) => ({
      wins: acc.wins + point.wins,
      losses: acc.losses + point.losses,
      draws: acc.draws + point.draws,
    }),
    { wins: 0, losses: 0, draws: 0 },
  );

  const winLossData = [
    { name: 'Customer wins', value: winLoss.wins },
    { name: 'Customer losses', value: winLoss.losses },
    { name: 'Draws', value: winLoss.draws },
  ].filter((entry) => entry.value > 0);

  const hedgeRatio =
    summary && Math.abs(summary.currentNetExposure) > 0
      ? Math.min(100, Math.round((summary.hedgedExposure / Math.abs(summary.currentNetExposure)) * 100))
      : 0;

  return (
    <div className="space-y-4">
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <Stat
          label="Total users"
          value={summary ? summary.totalUsers.toLocaleString('en-IN') : <Skeleton className="h-6 w-16" />}
          hint={summary ? summary.activeUsers + ' active today' : undefined}
        />
        <Stat
          label="Open trades"
          value={summary ? summary.openTrades.toLocaleString('en-IN') : <Skeleton className="h-6 w-16" />}
          hint={summary ? summary.todayTrades + ' placed today' : undefined}
        />
        <Stat
          label="Today's volume"
          value={summary ? formatMoney(summary.todayVolume) : <Skeleton className="h-6 w-24" />}
        />
        {seePnl ? (
          <Stat
            label="Today's P&L"
            value={summary ? formatSignedMoney(summary.todayPlatformResult) : <Skeleton className="h-6 w-24" />}
            tone={summary ? (summary.todayPlatformResult >= 0 ? 'up' : 'down') : 'neutral'}
            hint="Platform gross result"
          />
        ) : null}

        {seeBook ? (
          <>
            <Stat
              label="Current exposure"
              value={summary ? formatMoney(summary.currentExposure) : <Skeleton className="h-6 w-24" />}
              hint={summary ? 'Net ' + formatSignedMoney(summary.currentNetExposure) : undefined}
            />
            <Stat
              label="Hedged exposure"
              value={summary ? formatMoney(summary.hedgedExposure) : <Skeleton className="h-6 w-24" />}
              tone="accent"
              hint={hedgeRatio + '% of net exposure'}
            />
            <Stat
              label="Unhedged exposure"
              value={summary ? formatMoney(summary.unhedgedExposure) : <Skeleton className="h-6 w-24" />}
              tone={summary && summary.unhedgedExposure > 0 ? 'warn' : 'neutral'}
              hint={summary ? 'Liability ' + formatMoney(summary.potentialLiability) : undefined}
            />
          </>
        ) : null}
        <Stat
          label="Feed latency"
          value={summary ? summary.feedLatencyP50 + ' ms' : <Skeleton className="h-6 w-16" />}
          tone={summary && summary.feedLatencyP95 > 300 ? 'warn' : 'up'}
          hint={summary ? 'p95 ' + summary.feedLatencyP95 + 'ms / p99 ' + summary.feedLatencyP99 + 'ms' : undefined}
        />
      </div>

      <div className="grid gap-3 xl:grid-cols-2">
        <Panel>
          <PanelHeader title="Trading volume" description="Daily stake volume, last 14 days" />
          <div className="h-64 p-3">
            {seriesQuery.isLoading ? (
              <Skeleton className="h-full w-full" />
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={series}>
                  <defs>
                    <linearGradient id="volumeFill" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor={palette.accent} stopOpacity={0.5} />
                      <stop offset="100%" stopColor={palette.accent} stopOpacity={0.02} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid strokeDasharray="3 3" stroke={palette.grid} />
                  <XAxis dataKey="date" tick={axisStyle} tickLine={false} axisLine={false} />
                  <YAxis tick={axisStyle} tickLine={false} axisLine={false} width={70} />
                  <Tooltip
                    contentStyle={tooltipStyle}
                    formatter={(value: number) => [formatMoney(Math.round(value * 100)), 'Volume']}
                  />
                  <Area type="monotone" dataKey="volumeMajor" stroke={palette.accent} strokeWidth={2} fill="url(#volumeFill)" />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
        </Panel>

        {seePnl ? (
          <Panel>
            <PanelHeader title="Platform P&L" description="Gross result against customer payouts" />
            <div className="h-64 p-3">
              {seriesQuery.isLoading ? (
                <Skeleton className="h-full w-full" />
              ) : (
                <ResponsiveContainer width="100%" height="100%">
                  <LineChart data={series}>
                    <CartesianGrid strokeDasharray="3 3" stroke={palette.grid} />
                    <XAxis dataKey="date" tick={axisStyle} tickLine={false} axisLine={false} />
                    <YAxis tick={axisStyle} tickLine={false} axisLine={false} width={70} />
                    <Tooltip
                      contentStyle={tooltipStyle}
                      formatter={(value: number, name) => [formatMoney(Math.round(value * 100)), name]}
                    />
                    <Legend wrapperStyle={{ fontSize: 11 }} />
                    <Line
                      type="monotone"
                      dataKey="resultMajor"
                      name="Platform result"
                      stroke={palette.up}
                      strokeWidth={2}
                      dot={false}
                    />
                    <Line
                      type="monotone"
                      dataKey="payoutMajor"
                      name="Customer payout"
                      stroke={palette.warn}
                      strokeWidth={2}
                      dot={false}
                    />
                  </LineChart>
                </ResponsiveContainer>
              )}
            </div>
          </Panel>
        ) : null}

        <Panel>
          <PanelHeader title="Volume by asset" description="Last 7 days" />
          <div className="h-64 p-3">
            {assetsQuery.isLoading ? (
              <Skeleton className="h-full w-full" />
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart data={(assetsQuery.data ?? []).map((row) => ({ ...row, volumeMajor: fromMinor(row.volume) }))}>
                  <CartesianGrid strokeDasharray="3 3" stroke={palette.grid} />
                  <XAxis dataKey="symbol" tick={axisStyle} tickLine={false} axisLine={false} />
                  <YAxis tick={axisStyle} tickLine={false} axisLine={false} width={70} />
                  <Tooltip
                    contentStyle={tooltipStyle}
                    formatter={(value: number) => [formatMoney(Math.round(value * 100)), 'Volume']}
                  />
                  <Bar dataKey="volumeMajor" radius={[4, 4, 0, 0]}>
                    {(assetsQuery.data ?? []).map((row, index) => (
                      <Cell key={row.assetId} fill={CHART_COLORS[index % CHART_COLORS.length]} />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            )}
          </div>
        </Panel>

        <Panel>
          <PanelHeader title="Trade outcomes" description="Settled trades, last 14 days" />
          <div className="h-64 p-3">
            {seriesQuery.isLoading ? (
              <Skeleton className="h-full w-full" />
            ) : winLossData.length === 0 ? (
              <div className="grid h-full place-items-center text-xs text-content-400">No settled trades yet</div>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie data={winLossData} dataKey="value" nameKey="name" innerRadius={55} outerRadius={85} paddingAngle={2}>
                    {winLossData.map((entry, index) => (
                      <Cell key={entry.name} fill={CHART_COLORS[index % CHART_COLORS.length]} />
                    ))}
                  </Pie>
                  <Tooltip contentStyle={tooltipStyle} />
                  <Legend wrapperStyle={{ fontSize: 11 }} />
                </PieChart>
              </ResponsiveContainer>
            )}
          </div>
        </Panel>
      </div>

      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        {seePnl ? (
          <Stat
            label="Customer balances"
            value={summary ? formatMoney(summary.totalCustomerBalance) : <Skeleton className="h-6 w-24" />}
            hint={summary ? formatMoney(summary.totalLockedBalance) + ' locked' : undefined}
          />
        ) : null}
        {seeDeposits ? (
          <Stat
            label="Pending deposits"
            value={summary ? summary.pendingDeposits : <Skeleton className="h-6 w-10" />}
            tone={summary && summary.pendingDeposits > 0 ? 'warn' : 'neutral'}
          />
        ) : null}
        {seeWithdrawals ? (
          <Stat
            label="Pending withdrawals"
            value={summary ? summary.pendingWithdrawals : <Skeleton className="h-6 w-10" />}
            tone={summary && summary.pendingWithdrawals > 0 ? 'warn' : 'neutral'}
          />
        ) : null}
        <Stat
          label="Trade acceptance p95"
          value={summary ? summary.tradeAcceptanceP95 + ' ms' : <Skeleton className="h-6 w-16" />}
          hint={summary ? summary.activeAssets + ' active assets' : undefined}
        />
      </div>
    </div>
  );
}
