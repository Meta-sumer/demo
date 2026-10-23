import { useMemo, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { ArrowRight, ChevronLeft, ChevronRight, Download, History, Timer } from 'lucide-react';
import { api } from '../services/api';
import { toast } from '../hooks/useToast';
import { useAccountStore } from '../stores/accountStore';
import { useMarketStore } from '../stores/marketStore';
import { useCountdown, useNow } from '../hooks/useCountdown';
import { formatCountdown, formatDateTime, formatExpiry, formatMoney, formatPrice, formatSignedMoney } from '../utils/format';
import { downloadCsv, toCsv } from '../utils/csv';
import { groupByDay } from '../utils/dayGroups';
import { durationOf, tradeLabel } from '../utils/tradeStatus';
import { settlementOf } from '../features/trade/settlement';
import { accountOf } from '../features/trade/ticketModel';
import { amountTone, liveResult, liveTotal } from '../features/trade/liveResult';
import { useDrawPolicy } from '../features/trade/drawRule';
import { useLiveFeeds, useLivePrice } from '../features/trade/useLiveFeeds';
import {
  DirectionWord,
  LiveResultLine,
  StatusPill,
  TradeDemoTag,
  useClockFormatter,
  useTimeZone,
} from '../features/trade/tradeBits';
import { EmptyState, Panel, Skeleton } from '../components/ui';
import type { AccountType, Direction, Trade, TradeStatus, TradeSummary } from '../types/api';

/**
 * Trades (brief UX-14): Active | History.
 *
 * Two lists, because a trade that is still running and one that has finished
 * answer different questions: the first is "what am I exposed to", the second
 * "what happened". They are separate queries rather than a client-side filter,
 * so paging through history never has to load open trades to find them.
 *
 * Every row opens the same Trade Detail, and every row shows the terms the
 * trade was actually written under - the payout and both prices are the ones
 * frozen at the time, never recomputed from the current configuration. The
 * status is the shared wording and the result is the server's net figure
 * (`settlementOf`), so this page, the result card and the detail agree.
 *
 * Above the list, the totals for whatever the filters select, across every
 * page (reference record 17 - Deriv's profit/loss for the listed contracts),
 * from the server. On Active, what the running trades would pay if they ended
 * now (record 15). On a phone the cards are grouped by the day the trade was
 * placed, which is also the day the date filter selects on.
 */
const PAGE_SIZE = 20;
const EXPORT_PAGE_LIMIT = 100;
const EXPORT_MAX_ROWS = 5000;

type Tab = 'active' | 'history';

/** The result filter, in the trader's words, and the server status each one means. */
const RESULTS: Array<{ value: TradeStatus; label: string }> = [
  { value: 'WON', label: 'Won' },
  { value: 'LOST', label: 'Lost' },
  { value: 'DRAW', label: 'Draw / void' },
  { value: 'CLOSED_EARLY', label: 'Sold early' },
];

export function TradesPage() {
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get('tab') === 'history' ? 'history' : 'active';
  const activeAccount = useAccountStore((s) => s.active);

  const [page, setPage] = useState(1);
  const [range, setRange] = useState({ from: '', to: '' });
  const [assetId, setAssetId] = useState('');
  const [result, setResult] = useState<TradeStatus | ''>('');
  const [direction, setDirection] = useState<Direction | ''>('');
  // Defaults to the account being traded, so a demo session does not open on
  // a list of live trades - and follows it until the trader picks one here.
  const [accountChoice, setAccountChoice] = useState<AccountType | null>(null);
  const account = accountChoice ?? activeAccount;
  const [exporting, setExporting] = useState(false);

  const assetsQuery = useQuery({ queryKey: ['assets'], queryFn: api.assets.list, staleTime: 60_000 });
  const precisionOf = (id: string) => assetsQuery.data?.find((asset) => asset.id === id)?.pricePrecision ?? 2;

  const filters = useMemo(
    () => ({
      phase: tab === 'active' ? ('OPEN' as const) : ('SETTLED' as const),
      assetId: assetId || undefined,
      status: tab === 'history' && result ? result : undefined,
      direction: direction || undefined,
      accountType: account,
      // A date input gives a plain day; widen it to cover the whole day in UTC.
      from: range.from ? new Date(range.from + 'T00:00:00.000Z').toISOString() : undefined,
      to: range.to ? new Date(range.to + 'T23:59:59.999Z').toISOString() : undefined,
    }),
    [tab, assetId, result, direction, account, range],
  );

  const query = useQuery({
    queryKey: ['trades', filters, page],
    queryFn: () => api.trades.list({ ...filters, page, limit: PAGE_SIZE }),
    placeholderData: (previous) => previous,
    refetchInterval: tab === 'active' ? 5000 : false,
  });

  /*
   * Totals for the same filters, every page. The endpoint is newer than the
   * list: while a server without it is running this simply fails, and the
   * bar is left out rather than shown empty or wrong.
   */
  const summaryQuery = useQuery({
    queryKey: ['trades', 'summary', filters],
    queryFn: () => api.trades.summary(filters),
    placeholderData: (previous) => previous,
    // Kept current alongside the Active list - but not polled once the server
    // has said it does not have the endpoint.
    refetchInterval: (current) => (tab === 'active' && current.state.status !== 'error' ? 5000 : false),
    retry: false,
  });
  const summary = !summaryQuery.isError && isSummary(summaryQuery.data) ? summaryQuery.data : null;

  const data = query.data;
  // The server filters by account; this keeps the page right while an older
  // server that ignores the filter is still running. No account means live.
  const items = useMemo(
    () => (data?.items ?? []).filter((trade) => accountOf(trade) === account),
    [data, account],
  );
  const totalPages = data ? Math.max(1, Math.ceil(data.total / PAGE_SIZE)) : 1;
  const filtered = !!(range.from || range.to || assetId || result || direction);
  const walletCurrency = useAccountStore((s) => s.wallets[account]?.currency);
  const currency = walletCurrency ?? items[0]?.currency ?? 'INR';

  // Prices for every asset a listed trade is running on, for "if it ends now".
  const liveAssets = useMemo(
    () => (tab === 'active' ? items.map((trade) => trade.assetId) : []),
    [tab, items],
  );
  useLiveFeeds(liveAssets, { keepWorkspace: false });

  const zone = useTimeZone();
  const today = useNow(60_000);
  const days = useMemo(() => groupByDay(items, (trade) => trade.entryTimestamp, today, zone), [items, today, zone]);

  function changeTab(next: Tab) {
    setParams(next === 'history' ? { tab: 'history' } : {}, { replace: true });
    setPage(1);
  }

  function resetPage<T>(setter: (value: T) => void) {
    return (value: T) => {
      setter(value);
      setPage(1);
    };
  }

  /**
   * Exports what the current filter selects, not just the page on screen.
   *
   * Pages through the same query rather than asking for everything at once, and
   * stops at a cap so a large account cannot be turned into an unbounded
   * request. The user is told when that happens rather than being handed a
   * silently truncated file.
   */
  async function exportCsv() {
    setExporting(true);
    try {
      const rows: Trade[] = [];
      let current = 1;
      let total = 0;

      do {
        const response = await api.trades.list({ ...filters, page: current, limit: EXPORT_PAGE_LIMIT });
        rows.push(...response.items.filter((trade) => accountOf(trade) === account));
        total = response.total;
        current += 1;
        if (response.items.length === 0) break;
      } while (rows.length < Math.min(total, EXPORT_MAX_ROWS));

      if (rows.length === 0) {
        toast('info', 'Nothing to export', 'No trades match the current filter.');
        return;
      }

      downloadCsv(
        'trades-' + account.toLowerCase() + '-' + new Date().toISOString().slice(0, 10) + '.csv',
        toCsv(rows, [
          { header: 'Reference', value: (t) => t.reference },
          { header: 'Account', value: (t) => accountOf(t) },
          { header: 'Asset', value: (t) => t.symbol },
          { header: 'Direction', value: (t) => t.direction },
          { header: 'Duration', value: (t) => formatExpiry(durationOf(t)) },
          { header: 'Payout %', value: (t) => t.payoutPercent },
          { header: 'Investment', value: (t) => (t.stake / 100).toFixed(2) },
          { header: 'Currency', value: (t) => t.currency },
          { header: 'Entry price', value: (t) => t.entryPrice },
          { header: 'Placed at', value: (t) => new Date(t.entryTimestamp).toISOString() },
          { header: 'Expiry price', value: (t) => t.expiryPrice ?? '' },
          { header: 'Expires at', value: (t) => new Date(t.expiryTimestamp).toISOString() },
          { header: 'Status', value: (t) => tradeLabel(t) },
          {
            // Already net of the stake: +profit on a win, -stake on a loss.
            header: 'Net',
            value: (t) => {
              const net = settlementOf(t);
              return net === null ? '' : (net / 100).toFixed(2);
            },
          },
          { header: 'IP', value: (t) => t.ip ?? '' },
        ]),
      );

      if (total > rows.length) {
        toast('info', 'Export capped', 'The first ' + rows.length + ' of ' + total + ' trades were exported.');
      }
    } catch {
      toast('error', 'Export failed', 'Could not read the full history. Try a narrower date range.');
    } finally {
      setExporting(false);
    }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-end justify-between gap-3">
        <h1 className="text-xl font-bold text-content-100">Trades</h1>
        <div role="group" aria-label="Account" className="flex rounded-lg bg-surface-850 p-0.5 text-xs font-semibold">
          {(['LIVE', 'DEMO'] as AccountType[]).map((value) => (
            <button
              key={value}
              type="button"
              aria-pressed={account === value}
              onClick={() => resetPage(setAccountChoice)(value)}
              className={clsx(
                'rounded-md px-3 py-1.5 transition',
                account === value ? 'bg-surface-700 text-content-100' : 'text-content-400 hover:text-content-200',
              )}
            >
              {value === 'LIVE' ? 'Live' : 'Demo'}
            </button>
          ))}
        </div>
      </div>

      <div role="tablist" className="flex gap-6 border-b border-surface-800">
        {(
          [
            ['active', 'Active'],
            ['history', 'History'],
          ] as const
        ).map(([value, label]) => (
          <button
            key={value}
            type="button"
            role="tab"
            aria-selected={tab === value}
            onClick={() => changeTab(value)}
            className={clsx(
              '-mb-px border-b-2 px-1 pb-2.5 text-sm font-semibold transition',
              tab === value
                ? 'border-accent text-content-100'
                : 'border-transparent text-content-400 hover:text-content-200',
            )}
          >
            {label}
          </button>
        ))}
      </div>

      <div className="grid grid-cols-2 gap-3 sm:flex sm:flex-wrap sm:items-end">
        <Labelled label="Date range" className="col-span-2">
          <div className="flex items-center gap-1.5">
            <input
              type="date"
              aria-label="From date"
              className="field tnum min-w-0 sm:w-auto"
              value={range.from}
              max={range.to || undefined}
              onChange={(event) => resetPage(setRange)({ ...range, from: event.target.value })}
            />
            <span className="text-content-400">–</span>
            <input
              type="date"
              aria-label="To date"
              className="field tnum min-w-0 sm:w-auto"
              value={range.to}
              min={range.from || undefined}
              onChange={(event) => resetPage(setRange)({ ...range, to: event.target.value })}
            />
          </div>
        </Labelled>

        <Labelled label="Asset">
          <select className="field sm:w-auto" value={assetId} onChange={(event) => resetPage(setAssetId)(event.target.value)}>
            <option value="">All assets</option>
            {(assetsQuery.data ?? []).map((asset) => (
              <option key={asset.id} value={asset.id}>
                {asset.symbol}
              </option>
            ))}
          </select>
        </Labelled>

        <Labelled label="Direction">
          <select
            className="field sm:w-auto"
            value={direction}
            onChange={(event) => resetPage(setDirection)(event.target.value as Direction | '')}
          >
            <option value="">UP and DOWN</option>
            <option value="UP">UP</option>
            <option value="DOWN">DOWN</option>
          </select>
        </Labelled>

        {tab === 'history' ? (
          <Labelled label="Result">
            <select
              className="field sm:w-auto"
              value={result}
              onChange={(event) => resetPage(setResult)(event.target.value as TradeStatus | '')}
            >
              <option value="">All results</option>
              {RESULTS.map((entry) => (
                <option key={entry.value} value={entry.value}>
                  {entry.label}
                </option>
              ))}
            </select>
          </Labelled>
        ) : null}

        {filtered ? (
          <button
            type="button"
            onClick={() => {
              setRange({ from: '', to: '' });
              setAssetId('');
              setResult('');
              setDirection('');
              setPage(1);
            }}
            className="self-end pb-2 text-left text-xs font-semibold text-content-400 transition hover:text-content-200"
          >
            Clear filters
          </button>
        ) : null}

        <div className="col-span-2 flex items-end gap-2 sm:ml-auto">
          <button type="button" onClick={exportCsv} disabled={exporting} className="btn-ghost px-3 py-2">
            <Download className="h-4 w-4" />
            {exporting ? 'Exporting…' : 'Export CSV'}
          </button>

          <div className="ml-auto flex items-center gap-1 sm:ml-0">
            <button
              type="button"
              onClick={() => setPage((p) => Math.max(1, p - 1))}
              disabled={page <= 1}
              aria-label="Previous page"
              className="grid h-9 w-9 place-items-center rounded-lg border border-surface-700 bg-surface-850 text-content-300 transition enabled:hover:text-content-100 disabled:opacity-40"
            >
              <ChevronLeft className="h-4 w-4" />
            </button>
            <span className="tnum min-w-[54px] text-center text-xs font-semibold text-content-300">
              {page}/{totalPages}
            </span>
            <button
              type="button"
              onClick={() => setPage((p) => Math.min(totalPages, p + 1))}
              disabled={page >= totalPages}
              aria-label="Next page"
              className="grid h-9 w-9 place-items-center rounded-lg border border-surface-700 bg-surface-850 text-content-300 transition enabled:hover:text-content-100 disabled:opacity-40"
            >
              <ChevronRight className="h-4 w-4" />
            </button>
          </div>
        </div>
      </div>

      {tab === 'active' ? (
        items.length > 0 ? (
          <ActiveTotals trades={items} summary={summary} currency={currency} partial={totalPages > 1} />
        ) : null
      ) : summary && summary.count > 0 ? (
        <HistoryTotals summary={summary} currency={currency} />
      ) : null}

      <Panel className="overflow-hidden p-0">
        {query.isLoading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 6 }, (_, index) => (
              <Skeleton key={index} className="h-10 w-full" />
            ))}
          </div>
        ) : items.length === 0 ? (
          tab === 'history' && filtered ? (
            <EmptyState
              icon={History}
              title="No trades match"
              description="Widen the date range, or clear the filters to see everything."
            />
          ) : (
            <EmptyState
              icon={tab === 'active' ? Timer : History}
              title={tab === 'active' ? 'No active trades' : 'No settled trades yet'}
              description={
                tab === 'active'
                  ? 'Trades you place appear here with a live result and a countdown until they expire.'
                  : 'Every finished trade on this account is listed here with its entry, expiry and result.'
              }
              action={<StartTrading />}
            />
          )
        ) : (
          <>
            {/* Phone: one card per trade - no sideways scrolling at 390px -
                under the day it was placed. */}
            <div className="md:hidden">
              {days.map((day) => (
                <section key={day.key} aria-label={day.label}>
                  <h2 className="border-b border-surface-800 bg-surface-850/60 px-3 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-content-400">
                    {day.label}
                  </h2>
                  <ul className="divide-y divide-surface-800 border-b border-surface-800 last:border-b-0">
                    {day.items.map((trade) => (
                      <li key={trade.id}>
                        <TradeCard trade={trade} precision={precisionOf(trade.assetId)} />
                      </li>
                    ))}
                  </ul>
                </section>
              ))}
            </div>

            <table className="hidden w-full text-left text-sm md:table">
              <thead>
                <tr className="border-b border-surface-800 text-[11px] uppercase tracking-wider text-content-400">
                  <Th>Asset</Th>
                  <Th>Direction</Th>
                  <Th>Investment</Th>
                  <Th>Duration · expiry</Th>
                  <Th>Entry → expiry price</Th>
                  <Th>Status</Th>
                  <Th className="text-right">{tab === 'active' ? 'Time left · if it ends now' : 'Net result'}</Th>
                </tr>
              </thead>
              <tbody className="divide-y divide-surface-800">
                {items.map((trade) => (
                  <Row
                    key={trade.id}
                    trade={trade}
                    precision={precisionOf(trade.assetId)}
                    onOpen={() => navigate('/trades/' + trade.id)}
                  />
                ))}
              </tbody>
            </table>
          </>
        )}
      </Panel>
    </div>
  );
}

/**
 * The net result, or while the trade is still running the countdown and what
 * it would pay if it ended now.
 */
function Outcome({ trade, live = true }: { trade: Trade; live?: boolean }) {
  const running = isRunning(trade);
  const remaining = useCountdown(running ? trade.expiryTimestamp : null, 1000);
  if (running) {
    const countdown = (
      <span className="font-mono text-[13px] font-bold tnum text-content-100">{formatCountdown(remaining)}</span>
    );
    if (!live) return countdown;
    return (
      <span className="inline-flex flex-col items-end gap-0.5">
        {countdown}
        {/* The column heading already says "if it ends now". */}
        <LiveOutcome trade={trade} settling={remaining === 0} variant="plain" label={false} />
      </span>
    );
  }
  // Already net of the stake - see settlementOf.
  const net = settlementOf(trade) ?? 0;
  return (
    <span
      className={clsx(
        'tnum text-[13px] font-bold',
        net > 0 ? 'text-up' : net < 0 ? 'text-down' : 'text-content-300',
      )}
    >
      {formatSignedMoney(net, trade.currency)}
    </span>
  );
}

function Row({ trade, precision, onOpen }: { trade: Trade; precision: number; onOpen: () => void }) {
  const clock = useClockFormatter();
  return (
    <tr onClick={onOpen} className="cursor-pointer align-middle transition hover:bg-surface-850/60">
      <Td>
        <span className="flex items-center gap-1.5">
          {/* The link is the row's keyboard and screen-reader route; the row
              click is the same destination for a pointer. */}
          <Link
            to={'/trades/' + trade.id}
            onClick={(event) => event.stopPropagation()}
            className="font-semibold text-content-100 hover:underline"
          >
            {trade.symbol}
          </Link>
          <TradeDemoTag trade={trade} />
        </span>
        <span className="block font-mono text-[11px] text-content-400">{trade.reference}</span>
      </Td>
      <Td>
        <DirectionWord direction={trade.direction} className="text-[13px]" />
      </Td>
      <Td>
        <span className="tnum text-[13px] font-semibold text-content-100">{formatMoney(trade.stake, trade.currency)}</span>
        <span className="block text-[11px] text-warn">{trade.payoutPercent}% payout</span>
      </Td>
      <Td>
        <span className="tnum block text-[13px] text-content-100">
          {formatExpiry(durationOf(trade))} · {clock(trade.expiryTimestamp)}
        </span>
        <span className="block text-[11px] text-content-400">{formatDateTime(trade.entryTimestamp)}</span>
      </Td>
      <Td>
        <span className="tnum text-[13px] text-content-100">
          {formatPrice(trade.entryPrice, precision)} →{' '}
          {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, precision) : '--'}
        </span>
      </Td>
      <Td>
        <StatusPill trade={trade} />
      </Td>
      <Td className="text-right">
        <Outcome trade={trade} />
      </Td>
    </tr>
  );
}

function TradeCard({ trade, precision }: { trade: Trade; precision: number }) {
  const clock = useClockFormatter();
  const running = isRunning(trade);
  return (
    <Link to={'/trades/' + trade.id} className="block px-3 py-2.5 transition hover:bg-surface-850/60">
      <div className="flex items-center gap-2">
        <DirectionWord direction={trade.direction} className="text-xs" />
        <span className="min-w-0 flex-1 truncate text-sm font-semibold text-content-100">{trade.symbol}</span>
        <TradeDemoTag trade={trade} />
        <StatusPill trade={trade} />
      </div>
      <div className="mt-1 flex items-baseline gap-2 text-[12px]">
        <span className="font-semibold tnum text-content-200">{formatMoney(trade.stake, trade.currency)}</span>
        <span className="min-w-0 truncate text-content-400">
          {formatExpiry(durationOf(trade))} · {clock(trade.expiryTimestamp)}
        </span>
        <span className="ml-auto shrink-0">
          <Outcome trade={trade} live={false} />
        </span>
      </div>
      {running ? <CardLiveOutcome trade={trade} /> : null}
      <div className="mt-0.5 truncate text-[11px] tnum text-content-400">
        {formatPrice(trade.entryPrice, precision)} →{' '}
        {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, precision) : '--'} ·{' '}
        {formatDateTime(trade.entryTimestamp)}
      </div>
    </Link>
  );
}

function isRunning(trade: Pick<Trade, 'status'>): boolean {
  return trade.status === 'OPEN' || trade.status === 'SETTLING';
}

/** A running trade's "if it ends now", from the latest price of its asset. */
function LiveOutcome({
  trade,
  settling,
  variant,
  label,
}: {
  trade: Trade;
  settling: boolean;
  variant: 'strip' | 'plain';
  label?: boolean;
}) {
  const price = useLivePrice(trade.assetId);
  const drawPolicy = useDrawPolicy(trade.assetId);
  return (
    <LiveResultLine
      result={settling ? null : liveResult(trade, price, drawPolicy)}
      settling={settling}
      currency={trade.currency}
      variant={variant}
      label={label}
      className={variant === 'strip' ? 'mt-1.5' : undefined}
    />
  );
}

function CardLiveOutcome({ trade }: { trade: Trade }) {
  const remaining = useCountdown(trade.expiryTimestamp, 1000);
  return <LiveOutcome trade={trade} settling={remaining === 0} variant="strip" />;
}

const TONE_TEXT = { up: 'text-up', down: 'text-down', neutral: 'text-content-100' } as const;

/** A defensive read: an older server answers this path with something else entirely. */
function isSummary(value: unknown): value is TradeSummary {
  const summary = value as Partial<TradeSummary> | null | undefined;
  return (
    !!summary &&
    typeof summary.count === 'number' &&
    typeof summary.invested === 'number' &&
    typeof summary.netResult === 'number'
  );
}

/**
 * What the filters select on the Active tab: how many are running, what is in
 * them, and what they would pay together if they ended now. The first two come
 * from the server's totals when it offers them; the projection is added up
 * from the listed trades and the latest prices, and says so when the list runs
 * to more than one page.
 */
function ActiveTotals({
  trades,
  summary,
  currency,
  partial,
}: {
  trades: Trade[];
  summary: TradeSummary | null;
  currency: string;
  partial: boolean;
}) {
  const ticks = useMarketStore((s) => s.ticks);
  const now = useNow(1000);
  const drawPolicy = useDrawPolicy();
  const total = liveTotal(trades, (assetId) => ticks[assetId]?.price, now, drawPolicy);
  const missing = total.running - total.priced;

  return (
    <section
      aria-label="Totals for these filters"
      className="panel grid grid-cols-2 gap-x-4 gap-y-2 px-4 py-3 sm:flex sm:flex-wrap sm:items-end sm:gap-x-8"
    >
      <Figure label={'If they ended now' + (partial ? ' (this page)' : '')} className="col-span-2 sm:order-last sm:ml-auto sm:text-right">
        {total.priced > 0 ? (
          <span className={clsx('text-lg', TONE_TEXT[amountTone(total.amount)])}>
            {formatSignedMoney(total.amount, currency)}
          </span>
        ) : (
          <span className="text-sm font-medium text-content-400">
            {total.running === 0 ? 'Being settled…' : 'Waiting for prices'}
          </span>
        )}
        {missing > 0 && total.priced > 0 ? (
          <span className="block text-[11px] font-normal text-content-400">{missing} waiting for a price</span>
        ) : null}
      </Figure>
      {summary ? (
        <>
          <Figure label="Active">{summary.count}</Figure>
          <Figure label="Invested">{formatMoney(summary.invested, currency)}</Figure>
        </>
      ) : null}
    </section>
  );
}

/**
 * The filters' totals on History: how many trades, how they ended, what went
 * into them and the net result - the sum of every settled result, as the
 * server adds it up with the same rule `settlementOf` reads.
 */
function HistoryTotals({ summary, currency }: { summary: TradeSummary; currency: string }) {
  const outcomes = [
    [summary.won, 'won'],
    [summary.lost, 'lost'],
    [summary.draw, summary.draw === 1 ? 'draw' : 'draws'],
    [summary.soldEarly, 'sold early'],
    [summary.cancelled, 'cancelled'],
  ] as const;

  return (
    <section
      aria-label="Totals for these filters"
      className="panel grid grid-cols-2 gap-x-4 gap-y-2 px-4 py-3 sm:flex sm:flex-wrap sm:items-end sm:gap-x-8"
    >
      <Figure label="Net result" className="col-span-2 sm:order-last sm:ml-auto sm:text-right">
        <span className={clsx('text-lg', TONE_TEXT[amountTone(summary.netResult)])}>
          {formatSignedMoney(summary.netResult, currency)}
        </span>
      </Figure>
      <Figure label="Trades">{summary.count}</Figure>
      <Figure label="Invested">{formatMoney(summary.invested, currency)}</Figure>
      <Figure label="Results" className="col-span-2 sm:col-span-1">
        <span className="text-[13px] font-semibold">
          {outcomes
            .filter(([count, word]) => count > 0 || word === 'won' || word === 'lost')
            .map(([count, word], index) => (
              <span key={word}>
                {index > 0 ? <span className="text-content-400"> · </span> : null}
                <span
                  className={word === 'won' ? 'text-up' : word === 'lost' ? 'text-down' : 'text-content-100'}
                >
                  {count} {word}
                </span>
              </span>
            ))}
        </span>
      </Figure>
    </section>
  );
}

function Figure({ label, className, children }: { label: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={clsx('min-w-0', className)}>
      <div className="text-[10px] font-medium uppercase tracking-wider text-content-400">{label}</div>
      <div className="mt-0.5 font-bold tnum text-content-100">{children}</div>
    </div>
  );
}

function Th({ children, className }: { children: React.ReactNode; className?: string }) {
  return <th className={clsx('px-4 py-2.5 font-semibold', className)}>{children}</th>;
}

function Td({ children, className }: { children: React.ReactNode; className?: string }) {
  return <td className={clsx('px-4 py-3', className)}>{children}</td>;
}

function Labelled({ label, children, className }: { label: string; children: React.ReactNode; className?: string }) {
  return (
    <div className={clsx('min-w-0', className)}>
      <span className="mb-1 block text-[10px] font-medium uppercase tracking-wider text-content-400">{label}</span>
      {children}
    </div>
  );
}

/** The step that fills an empty list: the trade screen. */
function StartTrading() {
  return (
    <Link to="/trade" className="btn-primary">
      Start trading
      <ArrowRight className="h-4 w-4" />
    </Link>
  );
}
