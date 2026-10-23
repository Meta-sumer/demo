import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { FlatList, Platform, Pressable, RefreshControl, View } from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { useQuery } from '@tanstack/react-query';
import { ChevronLeft, ChevronRight, Download } from 'lucide-react-native';
import { api } from '../../services/api';
import { toast } from '../../hooks/useToast';
import { useAccountStore } from '../../stores/accountStore';
import { useMarketStore } from '../../stores/marketStore';
import { useCountdown, useNow } from '../../hooks/useCountdown';
import { usePullRefresh } from '../../hooks/usePullRefresh';
import { useScreenFocused } from '../../hooks/useScreenFocused';
import { formatCountdown, formatDateTime, formatExpiry, formatMoney, formatPrice, formatSignedMoney } from '../../utils/format';
import { shareCsv, toCsv } from '../../utils/csv';
import { groupByDay } from '../../utils/dayGroups';
import { durationOf, tradeLabel } from '../../utils/tradeStatus';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { settlementOf } from '../trade/settlement';
import { accountOf } from '../trade/ticketModel';
import { amountTone, liveResult, liveTotal } from '../trade/liveResult';
import { useDrawPolicy } from '../trade/drawRule';
import { useLiveFeeds, useLivePrice } from '../trade/useLiveFeeds';
import {
  DirectionWord,
  LiveResultLine,
  StatusPill,
  TradeDemoTag,
  useClockFormatter,
  useTimeZone,
} from '../trade/tradeBits';
import {
  Button,
  EmptyState,
  Icon,
  Panel,
  Segmented,
  Select,
  Skeleton,
  Text,
  type SegmentOption,
  type SelectOption,
} from '../../components/ui';
import { DateRangeField, type DateRange } from './DateRangeField';
import type { AccountType, Direction, Trade, TradeStatus, TradeSummary } from '../../types/api';

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
 * now (record 15). The cards are grouped by the day the trade was placed,
 * which is also the day the date filter selects on.
 *
 * The tab is in the route (`/trades?tab=history`), so a link can open either
 * list. This screen stays mounted behind its tab, so its polls and price feeds
 * pause while another tab is in front, where the web would have unmounted it.
 */
const PAGE_SIZE = 20;
const EXPORT_PAGE_LIMIT = 100;
const EXPORT_MAX_ROWS = 5000;

const MONO = Platform.select({ ios: 'Menlo', default: 'monospace' });

type Tab = 'active' | 'history';

const TABS: ReadonlyArray<readonly [Tab, string]> = [
  ['active', 'Active'],
  ['history', 'History'],
];

const ACCOUNTS: ReadonlyArray<SegmentOption<AccountType>> = [
  { value: 'LIVE', label: 'Live' },
  { value: 'DEMO', label: 'Demo' },
];

/** The result filter, in the trader's words, and the server status each one means. */
const RESULTS: Array<{ value: TradeStatus; label: string }> = [
  { value: 'WON', label: 'Won' },
  { value: 'LOST', label: 'Lost' },
  { value: 'DRAW', label: 'Draw / void' },
  { value: 'CLOSED_EARLY', label: 'Sold early' },
];

const RESULT_OPTIONS: ReadonlyArray<SelectOption<TradeStatus | ''>> = [
  { value: '', label: 'All results' },
  ...RESULTS,
];

const DIRECTION_OPTIONS: ReadonlyArray<SelectOption<Direction | ''>> = [
  { value: '', label: 'UP and DOWN' },
  { value: 'UP', label: 'UP' },
  { value: 'DOWN', label: 'DOWN' },
];

/** A day heading or a trade card, flattened for the list. */
type Row =
  | { kind: 'day'; key: string; label: string }
  | { kind: 'trade'; key: string; trade: Trade; firstInDay: boolean; lastInDay: boolean };

export function TradesScreen() {
  const params = useLocalSearchParams<{ tab?: string }>();
  const tab: Tab = params.tab === 'history' ? 'history' : 'active';
  const activeAccount = useAccountStore((s) => s.active);
  const focused = useScreenFocused();
  const tint = useTokenColor('accent');

  // The page belongs to the tab it was turned on: switching tab - here, or by
  // a link to /trades?tab=history - starts again from the first page.
  const [paging, setPaging] = useState<{ tab: Tab; page: number }>({ tab, page: 1 });
  const page = paging.tab === tab ? paging.page : 1;
  const setPage = (next: number) => setPaging({ tab, page: next });

  const [range, setRange] = useState<DateRange>({ from: '', to: '' });
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
  const assetOptions = useMemo<Array<SelectOption<string>>>(
    () => [
      { value: '', label: 'All assets' },
      ...(assetsQuery.data ?? []).map((asset) => ({ value: asset.id, label: asset.symbol })),
    ],
    [assetsQuery.data],
  );

  const filters = useMemo(
    () => ({
      phase: tab === 'active' ? ('OPEN' as const) : ('SETTLED' as const),
      assetId: assetId || undefined,
      status: tab === 'history' && result ? result : undefined,
      direction: direction || undefined,
      accountType: account,
      // A calendar day; widen it to cover the whole day in UTC.
      from: range.from ? new Date(range.from + 'T00:00:00.000Z').toISOString() : undefined,
      to: range.to ? new Date(range.to + 'T23:59:59.999Z').toISOString() : undefined,
    }),
    [tab, assetId, result, direction, account, range],
  );

  const query = useQuery({
    queryKey: ['trades', filters, page],
    queryFn: () => api.trades.list({ ...filters, page, limit: PAGE_SIZE }),
    placeholderData: (previous) => previous,
    refetchInterval: tab === 'active' && focused ? 5000 : false,
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
    refetchInterval: (current) =>
      tab === 'active' && focused && current.state.status !== 'error' ? 5000 : false,
    retry: false,
  });
  const summary = !summaryQuery.isError && isSummary(summaryQuery.data) ? summaryQuery.data : null;

  // Coming back to the tab is what opening the page was on the web: read the
  // lists again rather than show what they said when the trader left.
  const wasFocused = useRef(focused);
  useEffect(() => {
    if (focused && !wasFocused.current) {
      void query.refetch();
      void summaryQuery.refetch();
    }
    wasFocused.current = focused;
  }, [focused]); // eslint-disable-line react-hooks/exhaustive-deps

  const { refreshing, onRefresh } = usePullRefresh(() => Promise.all([query.refetch(), summaryQuery.refetch()]));

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
  // The trade screen stays mounted behind its own tab, so a channel its chart
  // may be drawing is never released from here (`keepWorkspace`).
  const liveAssets = useMemo(
    () => (tab === 'active' && focused ? items.map((trade) => trade.assetId) : []),
    [tab, focused, items],
  );
  useLiveFeeds(liveAssets, { keepWorkspace: true });

  const zone = useTimeZone();
  const today = useNow(60_000);
  const days = useMemo(() => groupByDay(items, (trade) => trade.entryTimestamp, today, zone), [items, today, zone]);
  const rows = useMemo(() => {
    const list: Row[] = [];
    for (const day of days) {
      list.push({ kind: 'day', key: 'day:' + day.key, label: day.label });
      day.items.forEach((trade, index) =>
        list.push({
          kind: 'trade',
          key: trade.id,
          trade,
          firstInDay: index === 0,
          lastInDay: index === day.items.length - 1,
        }),
      );
    }
    return list;
  }, [days]);

  const listRef = useRef<FlatList<Row>>(null);
  const headerHeight = useRef(0);

  function changeTab(next: Tab) {
    router.setParams({ tab: next === 'history' ? 'history' : undefined });
  }

  function resetPage<T>(setter: (value: T) => void) {
    return (value: T) => {
      setter(value);
      setPage(1);
    };
  }

  /** Turning the page from under the list brings the new page's first card into view. */
  function turnPage(next: number, fromFooter: boolean) {
    setPage(next);
    if (fromFooter) listRef.current?.scrollToOffset({ offset: headerHeight.current, animated: false });
  }

  /**
   * Exports what the current filter selects, not just the page on screen.
   *
   * Pages through the same query rather than asking for everything at once, and
   * stops at a cap so a large account cannot be turned into an unbounded
   * request. The user is told when that happens rather than being handed a
   * silently truncated file. The file goes to the share sheet, to be saved or
   * sent on.
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

      await shareCsv(
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

  const header = (
    <View
      className="gap-4 pb-4"
      onLayout={(event) => {
        headerHeight.current = event.nativeEvent.layout.height;
      }}
    >
      <View className="flex-row items-end justify-between gap-3">
        <Text accessibilityRole="header" className="text-xl font-bold text-content-100">
          Trades
        </Text>
        <Segmented
          options={ACCOUNTS}
          value={account}
          onChange={resetPage(setAccountChoice)}
          stretch={false}
          accessibilityLabel="Account"
        />
      </View>

      <View accessibilityRole="tablist" className="flex-row gap-6 border-b border-surface-800">
        {TABS.map(([value, label]) => (
          <Pressable
            key={value}
            accessibilityRole="tab"
            accessibilityState={{ selected: tab === value }}
            onPress={() => changeTab(value)}
            className={cn('-mb-px border-b-2 px-1 pb-2.5', tab === value ? 'border-accent' : 'border-transparent')}
          >
            <Text className={cn('text-sm font-semibold', tab === value ? 'text-content-100' : 'text-content-400')}>
              {label}
            </Text>
          </Pressable>
        ))}
      </View>

      <View className="gap-3">
        <DateRangeField value={range} onChange={resetPage(setRange)} />

        <View className="flex-row gap-3">
          <Select
            className="min-w-0 flex-1"
            label="Asset"
            title="Asset"
            value={assetId}
            options={assetOptions}
            onChange={resetPage(setAssetId)}
          />
          <Select
            className="min-w-0 flex-1"
            label="Direction"
            title="Direction"
            value={direction}
            options={DIRECTION_OPTIONS}
            onChange={resetPage(setDirection)}
          />
        </View>

        {tab === 'history' || filtered ? (
          <View className="flex-row items-end gap-3">
            {tab === 'history' ? (
              <Select
                className="min-w-0 flex-1"
                label="Result"
                title="Result"
                value={result}
                options={RESULT_OPTIONS}
                onChange={resetPage(setResult)}
              />
            ) : null}
            <View className={cn(tab === 'history' && 'flex-1')}>
              {filtered ? (
                <Pressable
                  onPress={() => {
                    setRange({ from: '', to: '' });
                    setAssetId('');
                    setResult('');
                    setDirection('');
                    setPage(1);
                  }}
                  accessibilityRole="button"
                  hitSlop={6}
                  className="self-start py-3 active:opacity-70"
                >
                  <Text className="text-xs font-semibold text-content-400">Clear filters</Text>
                </Pressable>
              ) : null}
            </View>
          </View>
        ) : null}

        <View className="flex-row items-end gap-2">
          <Button variant="ghost" icon={Download} onPress={exportCsv} disabled={exporting} className="px-3">
            {exporting ? 'Exporting…' : 'Export CSV'}
          </Button>
          <View className="ml-auto">
            <Pager page={page} totalPages={totalPages} onChange={(next) => turnPage(next, false)} />
          </View>
        </View>
      </View>

      {tab === 'active' ? (
        items.length > 0 ? (
          <ActiveTotals trades={items} summary={summary} currency={currency} partial={totalPages > 1} />
        ) : null
      ) : summary && summary.count > 0 ? (
        <HistoryTotals summary={summary} currency={currency} />
      ) : null}
    </View>
  );

  const empty = (
    <Panel className="overflow-hidden">
      {query.isLoading ? (
        <View className="gap-2 p-3">
          {Array.from({ length: 6 }, (_, index) => (
            <Skeleton key={index} className="h-10 w-full" />
          ))}
        </View>
      ) : (
        <EmptyState
          title={tab === 'active' ? 'No active trades' : 'No trades match'}
          description={
            tab === 'active'
              ? 'Trades you place appear here until they expire.'
              : filtered
                ? 'Widen the date range, or clear the filters to see everything.'
                : 'Settled trades on this account appear here.'
          }
        />
      )}
    </Panel>
  );

  return (
    <View className="flex-1 bg-surface-950">
      <FlatList
        ref={listRef}
        data={query.isLoading ? [] : rows}
        // Prices are written to each asset's precision, which arrives with the assets.
        extraData={assetsQuery.data}
        keyExtractor={(row) => row.key}
        renderItem={({ item, index }) => (
          // One card per trade under the day it was placed, framed together as
          // the web's panel: the first row carries its top, the last its bottom.
          <View
            className={cn(
              'border-x border-surface-700 bg-surface-900',
              index === 0 && 'overflow-hidden rounded-t-xl border-t',
              index === rows.length - 1 && 'overflow-hidden rounded-b-xl border-b',
            )}
          >
            {item.kind === 'day' ? (
              <View accessibilityRole="header" className="border-b border-surface-800 bg-surface-850/60 px-3 py-1.5">
                <Text className="text-[11px] font-semibold uppercase tracking-wider text-content-400">{item.label}</Text>
              </View>
            ) : (
              <View
                className={cn(
                  !item.firstInDay && 'border-t border-surface-800',
                  item.lastInDay && index !== rows.length - 1 && 'border-b border-surface-800',
                )}
              >
                <TradeCard trade={item.trade} precision={precisionOf(item.trade.assetId)} />
              </View>
            )}
          </View>
        )}
        ListHeaderComponent={header}
        ListEmptyComponent={empty}
        ListFooterComponent={
          !query.isLoading && items.length > 0 && totalPages > 1 ? (
            <View className="items-center pt-4">
              <Pager page={page} totalPages={totalPages} onChange={(next) => turnPage(next, true)} />
            </View>
          ) : null
        }
        contentContainerClassName="px-3 pt-4"
        contentContainerStyle={{ paddingBottom: 24 }}
        keyboardShouldPersistTaps="handled"
        initialNumToRender={12}
        refreshControl={<RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={tint} colors={[tint]} />}
      />
    </View>
  );
}

function Pager({ page, totalPages, onChange }: { page: number; totalPages: number; onChange: (page: number) => void }) {
  return (
    <View className="flex-row items-center gap-1">
      <PageButton
        icon="previous"
        disabled={page <= 1}
        onPress={() => onChange(Math.max(1, page - 1))}
      />
      <Text className="min-w-[54px] text-center text-xs font-semibold tnum text-content-300">
        {page}/{totalPages}
      </Text>
      <PageButton
        icon="next"
        disabled={page >= totalPages}
        onPress={() => onChange(Math.min(totalPages, page + 1))}
      />
    </View>
  );
}

function PageButton({ icon, disabled, onPress }: { icon: 'previous' | 'next'; disabled: boolean; onPress: () => void }) {
  return (
    <Pressable
      onPress={onPress}
      disabled={disabled}
      accessibilityRole="button"
      accessibilityLabel={icon === 'previous' ? 'Previous page' : 'Next page'}
      accessibilityState={{ disabled }}
      className={cn(
        'h-9 w-9 items-center justify-center rounded-lg border border-surface-700 bg-surface-850 active:bg-surface-800',
        disabled && 'opacity-40',
      )}
    >
      <Icon as={icon === 'previous' ? ChevronLeft : ChevronRight} size={16} className="text-content-300" />
    </Pressable>
  );
}

/**
 * The net result, or while the trade is still running its countdown. The
 * card shows what a running trade would pay if it ended now on a line of its
 * own, under this.
 */
function Outcome({ trade }: { trade: Trade }) {
  const running = isRunning(trade);
  const remaining = useCountdown(running ? trade.expiryTimestamp : null, 1000);
  if (running) {
    return (
      <Text className="text-[13px] font-bold tnum text-content-100" style={{ fontFamily: MONO }}>
        {formatCountdown(remaining)}
      </Text>
    );
  }
  // Already net of the stake - see settlementOf.
  const net = settlementOf(trade) ?? 0;
  return (
    <Text className={cn('text-[13px] font-bold tnum', net > 0 ? 'text-up' : net < 0 ? 'text-down' : 'text-content-300')}>
      {formatSignedMoney(net, trade.currency)}
    </Text>
  );
}

function TradeCard({ trade, precision }: { trade: Trade; precision: number }) {
  const clock = useClockFormatter();
  const running = isRunning(trade);
  return (
    <Pressable
      onPress={() => router.push('/trades/' + trade.id)}
      accessibilityRole="link"
      className="px-3 py-2.5 active:bg-surface-850/60"
    >
      <View className="flex-row items-center gap-2">
        <DirectionWord direction={trade.direction} className="text-xs" />
        <Text numberOfLines={1} className="min-w-0 flex-1 text-sm font-semibold text-content-100">
          {trade.symbol}
        </Text>
        <TradeDemoTag trade={trade} />
        <StatusPill trade={trade} />
      </View>
      <View className="mt-1 flex-row items-center gap-2">
        <Text className="text-[12px] font-semibold tnum text-content-200">{formatMoney(trade.stake, trade.currency)}</Text>
        <Text numberOfLines={1} className="min-w-0 shrink text-[12px] text-content-400">
          {formatExpiry(durationOf(trade))} · {clock(trade.expiryTimestamp)}
        </Text>
        <View className="ml-auto shrink-0">
          <Outcome trade={trade} />
        </View>
      </View>
      {running ? <CardLiveOutcome trade={trade} /> : null}
      <Text numberOfLines={1} className="mt-0.5 text-[11px] tnum text-content-400">
        {formatPrice(trade.entryPrice, precision)} →{' '}
        {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, precision) : '--'} ·{' '}
        {formatDateTime(trade.entryTimestamp)}
      </Text>
    </Pressable>
  );
}

function isRunning(trade: Pick<Trade, 'status'>): boolean {
  return trade.status === 'OPEN' || trade.status === 'SETTLING';
}

/** A running trade's "if it ends now", from the latest price of its asset. */
function LiveOutcome({ trade, settling }: { trade: Trade; settling: boolean }) {
  const price = useLivePrice(trade.assetId);
  const drawPolicy = useDrawPolicy(trade.assetId);
  return (
    <LiveResultLine
      result={settling ? null : liveResult(trade, price, drawPolicy)}
      settling={settling}
      currency={trade.currency}
      variant="strip"
      className="mt-1.5"
    />
  );
}

function CardLiveOutcome({ trade }: { trade: Trade }) {
  const remaining = useCountdown(trade.expiryTimestamp, 1000);
  return <LiveOutcome trade={trade} settling={remaining === 0} />;
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
    <Panel accessibilityLabel="Totals for these filters" className="gap-2 px-4 py-3">
      <Figure label={'If they ended now' + (partial ? ' (this page)' : '')}>
        {total.priced > 0 ? (
          <Text className={cn('text-lg font-bold tnum', TONE_TEXT[amountTone(total.amount)])}>
            {formatSignedMoney(total.amount, currency)}
          </Text>
        ) : (
          <Text className="text-sm font-medium text-content-400">
            {total.running === 0 ? 'Being settled…' : 'Waiting for prices'}
          </Text>
        )}
        {missing > 0 && total.priced > 0 ? (
          <Text className="text-[11px] text-content-400">{missing} waiting for a price</Text>
        ) : null}
      </Figure>
      {summary ? (
        <View className="flex-row gap-4">
          <Figure label="Active" className="flex-1">
            {summary.count}
          </Figure>
          <Figure label="Invested" className="flex-1">
            {formatMoney(summary.invested, currency)}
          </Figure>
        </View>
      ) : null}
    </Panel>
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
    <Panel accessibilityLabel="Totals for these filters" className="gap-2 px-4 py-3">
      <Figure label="Net result">
        <Text className={cn('text-lg font-bold tnum', TONE_TEXT[amountTone(summary.netResult)])}>
          {formatSignedMoney(summary.netResult, currency)}
        </Text>
      </Figure>
      <View className="flex-row gap-4">
        <Figure label="Trades" className="flex-1">
          {summary.count}
        </Figure>
        <Figure label="Invested" className="flex-1">
          {formatMoney(summary.invested, currency)}
        </Figure>
      </View>
      <Figure label="Results">
        <Text className="text-[13px] font-semibold tnum">
          {outcomes
            .filter(([count, word]) => count > 0 || word === 'won' || word === 'lost')
            .map(([count, word], index) => (
              <Text key={word} className="text-[13px] font-semibold tnum">
                {index > 0 ? <Text className="text-[13px] font-semibold text-content-400"> · </Text> : null}
                <Text
                  className={cn(
                    'text-[13px] font-semibold tnum',
                    word === 'won' ? 'text-up' : word === 'lost' ? 'text-down' : 'text-content-100',
                  )}
                >
                  {count} {word}
                </Text>
              </Text>
            ))}
        </Text>
      </Figure>
    </Panel>
  );
}

function Figure({ label, className, children }: { label: string; className?: string; children: ReactNode }) {
  return (
    <View className={cn('min-w-0', className)}>
      <Text className="text-[10px] font-medium uppercase tracking-wider text-content-400">{label}</Text>
      {typeof children === 'string' || typeof children === 'number' ? (
        <Text className="mt-0.5 text-base font-bold tnum text-content-100">{children}</Text>
      ) : (
        <View className="mt-0.5">{children}</View>
      )}
    </View>
  );
}
