import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { FlatList, Platform, Pressable, SectionList, View, type TextStyle } from 'react-native';
import { router } from 'expo-router';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import Animated, { useAnimatedStyle, useSharedValue, withRepeat, withTiming } from 'react-native-reanimated';
import { ArrowRight, ChevronDown, ChevronUp } from 'lucide-react-native';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useMarketStore } from '../../stores/marketStore';
import { useTradeStore } from '../../stores/tradeStore';
import { useAccountStore } from '../../stores/accountStore';
import { useCountdown, useNow } from '../../hooks/useCountdown';
import { formatCountdown, formatMoney, formatPrice, formatSignedMoney } from '../../utils/format';
import { groupByDay, type DayGroup } from '../../utils/dayGroups';
import { durationOf } from '../../utils/tradeStatus';
import { cn } from '../../utils/cn';
import { announceResult } from './announceResult';
import { payoutOf, settlementOf } from './settlement';
import { accountOf, durationLabel } from './ticketModel';
import { amountTone, liveResult, liveTotal } from './liveResult';
import { useDrawPolicy } from './drawRule';
import { useLiveFeeds, useLivePrice } from './useLiveFeeds';
import {
  DirectionWord,
  LiveResultLine,
  StatusPill,
  TradeDemoTag,
  useClockFormatter,
  useTimeZone,
} from './tradeBits';
import { toast } from '../../hooks/useToast';
import { EmptyState, Icon, Text } from '../../components/ui';
import type { Trade } from '../../types/api';

/**
 * Active trades and recent results, for the account being traded.
 *
 * Each active row carries everything the brief asks to be prominent (UX-12):
 * asset, direction, investment, the clock time it expires at, the countdown to
 * it and its status in words - the expiry must never need searching for - and
 * what the trade would pay if it ended at the price now, in money (reference
 * record 15). A line above them adds those up for every running trade.
 *
 * Each also carries a live "Sell now" price fetched from the server - the
 * client never estimates what a position is worth, because that number decides
 * how much money changes hands. The "if it ends now" figure is not that: it is
 * one of the three outcomes fixed when the trade was accepted, picked by the
 * price on screen.
 *
 * History is grouped by the day each trade was placed, and a row opens in
 * place to show its full terms (record 17, Pocket Option) - the detail screen
 * is one link further for everything else.
 *
 * On a phone this is the body of the sheet the ticket's trade history button opens.
 * `onNavigate` runs before it leaves for another screen, so that sheet can
 * close first rather than stay over the screen it opened.
 */
export function PositionsPanel({
  precisions = {},
  onNavigate,
}: {
  precisions?: Record<string, number>;
  onNavigate?: () => void;
}) {
  const account = useAccountStore((s) => s.active);
  const allOpen = useTradeStore((s) => s.openTrades);
  const sessionSettled = useTradeStore((s) => s.recentlySettled);
  const [tab, setTab] = useState<'open' | 'history'>('open');
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const zone = useTimeZone();
  // Coarse: only the "Today" / "Yesterday" labels depend on it.
  const today = useNow(60_000);

  const openTrades = useMemo(() => allOpen.filter((trade) => accountOf(trade) === account), [allOpen, account]);
  const liveAssets = useMemo(
    () => (tab === 'open' ? openTrades.map((trade) => trade.assetId) : []),
    [tab, openTrades],
  );
  useLiveFeeds(liveAssets, { keepWorkspace: true });

  /*
   * History is the account's own recent record from the server, not only what
   * settled while the app was open - a trader coming back tomorrow still sees
   * yesterday's results here. Settlements from this session are merged in so a
   * result appears the moment it lands, before the refetch.
   */
  const historyQuery = useQuery({
    queryKey: ['trades', 'recent-settled', account],
    queryFn: () => api.trades.list({ phase: 'SETTLED', accountType: account, page: 1, limit: 20 }),
    enabled: tab === 'history',
    staleTime: 5_000,
  });

  // Newest placed first - the server's own order for history, and the one
  // the day groups are cut from.
  const settled = useMemo(() => {
    const byId = new Map<string, Trade>();
    for (const trade of [...sessionSettled, ...(historyQuery.data?.items ?? [])]) {
      if (accountOf(trade) !== account || byId.has(trade.id)) continue;
      byId.set(trade.id, trade);
    }
    return [...byId.values()].sort((a, b) => b.entryTimestamp - a.entryTimestamp).slice(0, 20);
  }, [sessionSettled, historyQuery.data, account]);

  const days = useMemo(
    () =>
      groupByDay(settled, (trade) => trade.entryTimestamp, today, zone).map((day) => ({ ...day, data: day.items })),
    [settled, today, zone],
  );

  const empty = tab === 'open' ? openTrades.length === 0 : settled.length === 0;

  function go(href: string) {
    onNavigate?.();
    router.push(href);
  }

  const viewAll =
    tab === 'history' ? (
      <View className="border-t border-surface-800/70 px-3 py-2.5">
        <Pressable
          onPress={() => {
            onNavigate?.();
            router.navigate({ pathname: '/trades', params: { tab: 'history' } });
          }}
          accessibilityRole="link"
          className="self-center py-1"
        >
          <Text className="text-xs font-semibold text-accent">View all trades</Text>
        </Pressable>
      </View>
    ) : null;

  return (
    <View className="min-h-0 flex-1">
      <View className="flex-row border-b border-surface-800">
        <TabButton
          active={tab === 'open'}
          onPress={() => setTab('open')}
          count={openTrades.length}
          live={openTrades.length > 0}
        >
          Active
        </TabButton>
        <TabButton active={tab === 'history'} onPress={() => setTab('history')}>
          History
        </TabButton>
      </View>

      {tab === 'history' && historyQuery.isLoading && settled.length === 0 ? (
        <View className="flex-1">
          <Text className="px-3 py-6 text-center text-xs text-content-400">Loading recent trades…</Text>
          {viewAll}
        </View>
      ) : empty ? (
        <View className="flex-1">
          <EmptyState
            title={tab === 'open' ? 'No active trades' : 'No settled trades yet'}
            description={
              tab === 'open'
                ? 'Place a trade and it will be tracked here in real time.'
                : 'Your settled trades on this account appear here.'
            }
          />
          {viewAll}
        </View>
      ) : tab === 'open' ? (
        <View className="flex-1">
          <OpenTotal trades={openTrades} />
          <FlatList
            data={openTrades}
            keyExtractor={(trade) => trade.id}
            renderItem={({ item }) => (
              <OpenPosition trade={item} pricePrecision={precisions[item.assetId] ?? 2} onOpen={go} />
            )}
            ItemSeparatorComponent={Separator}
            className="flex-1"
          />
        </View>
      ) : (
        <SectionList<Trade, DayGroup<Trade> & { data: Trade[] }>
          sections={days}
          keyExtractor={(trade) => trade.id}
          stickySectionHeadersEnabled
          renderSectionHeader={({ section }) => (
            <View className="border-b border-surface-800/70 bg-surface-900 px-2.5 py-1">
              <Text
                accessibilityRole="header"
                className="text-[10px] font-semibold uppercase tracking-wider text-content-400"
              >
                {section.label}
              </Text>
            </View>
          )}
          renderItem={({ item }) => (
            <SettledPosition
              trade={item}
              pricePrecision={precisions[item.assetId] ?? 2}
              expanded={expandedId === item.id}
              onToggle={() => setExpandedId((current) => (current === item.id ? null : item.id))}
              onOpen={go}
            />
          )}
          ItemSeparatorComponent={Separator}
          ListFooterComponent={viewAll}
          className="flex-1"
        />
      )}
    </View>
  );
}

function Separator() {
  return <View className="h-px bg-surface-800/70" />;
}

/**
 * The "something is running" dot: a solid dot with a ring that keeps
 * spreading out from it, so it reads from arm's length.
 */
function LiveDot() {
  const pulse = useSharedValue(0);
  useEffect(() => {
    pulse.value = withRepeat(withTiming(1, { duration: 1000 }), -1, false);
  }, [pulse]);
  const ring = useAnimatedStyle(() => ({
    opacity: 0.7 * (1 - pulse.value),
    transform: [{ scale: 1 + pulse.value * 1.4 }],
  }));
  return (
    <View importantForAccessibility="no-hide-descendants" accessibilityElementsHidden className="h-1.5 w-1.5">
      <Animated.View className="absolute h-1.5 w-1.5 rounded-full bg-brand" style={ring} />
      <View className="h-1.5 w-1.5 rounded-full bg-brand" />
    </View>
  );
}

function TabButton({
  active,
  count,
  live,
  onPress,
  children,
}: {
  active: boolean;
  count?: number;
  /** Something is running right now, whether or not this tab is showing. */
  live?: boolean;
  onPress: () => void;
  children: string;
}) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="tab"
      accessibilityState={{ selected: active }}
      className={cn(
        '-mb-px flex-1 flex-row items-center justify-center gap-2 border-b-2 px-2 py-2.5',
        active ? 'border-accent' : 'border-transparent',
      )}
    >
      {/* The dot is the one part that reads from across the room: an order is
          live on the chart, even while the History tab is open. */}
      {live ? <LiveDot /> : null}
      <Text className={cn('text-[11px] font-semibold', active ? 'text-content-100' : 'text-content-400')}>
        {children}
      </Text>
      {count !== undefined ? (
        <View
          className={cn(
            'rounded px-1.5 py-0.5',
            live && !active ? 'bg-brand/20' : active ? 'bg-accent/20' : 'bg-surface-800',
          )}
        >
          <Text
            className={cn(
              'text-[10px] font-bold tnum',
              live && !active ? 'text-brand' : active ? 'text-accent' : 'text-content-400',
            )}
          >
            {count}
          </Text>
        </View>
      ) : null}
    </Pressable>
  );
}

const TONE_TEXT = { up: 'text-up', down: 'text-down', neutral: 'text-content-100' } as const;

/**
 * The platform's own monospace face, for the countdown and the reference. The
 * NativeWind preset's `font-mono` names a family Android does not have.
 */
const MONO: TextStyle = { fontFamily: Platform.select({ ios: 'Menlo', default: 'monospace' }) };

/**
 * "Open trades: +₹330.00 if they ended now" - every running trade on the
 * account, added up (Deriv's "Total profit/loss"). Trades still waiting for a
 * price are named rather than silently left out of the sum. Pinned above the
 * list, so it stays in view as the rows scroll.
 */
function OpenTotal({ trades }: { trades: Trade[] }) {
  // The whole map: the total moves with any of the assets. Selected as is -
  // a map rebuilt inside the selector would be a new object on every call.
  const ticks = useMarketStore((s) => s.ticks);
  const now = useNow(1000);
  const drawPolicy = useDrawPolicy();
  const total = liveTotal(trades, (assetId) => ticks[assetId]?.price, now, drawPolicy);
  const currency = trades[0]?.currency ?? 'INR';
  const missing = total.running - total.priced;

  return (
    <View className="flex-row flex-wrap items-baseline gap-x-1 border-b border-surface-800/70 bg-surface-900 px-2.5 py-1.5">
      <Text className="text-[11px] font-semibold text-content-200">Open trades:</Text>
      {total.running === 0 ? (
        <Text className="text-[11px] text-content-400">results are being settled…</Text>
      ) : total.priced === 0 ? (
        <Text className="text-[11px] text-content-400">waiting for prices</Text>
      ) : (
        <>
          <Text className={cn('text-[11px] font-bold tnum', TONE_TEXT[amountTone(total.amount)])}>
            {formatSignedMoney(total.amount, currency)}
          </Text>
          <Text className="text-[11px] text-content-400">if they ended now</Text>
          {missing > 0 ? <Text className="text-[11px] text-content-400">· {missing} waiting for a price</Text> : null}
        </>
      )}
    </View>
  );
}

function OpenPosition({
  trade,
  pricePrecision,
  onOpen,
}: {
  trade: Trade;
  pricePrecision: number;
  onOpen: (href: string) => void;
}) {
  const queryClient = useQueryClient();
  const remaining = useCountdown(trade.expiryTimestamp);
  const current = useLivePrice(trade.assetId);
  const settleTrade = useTradeStore((s) => s.settleTrade);
  const clock = useClockFormatter();
  const drawPolicy = useDrawPolicy(trade.assetId);

  const running = remaining > 0;
  const result = running ? liveResult(trade, current, drawPolicy) : null;
  const priceTone = result ? TONE_TEXT[amountTone(result.amount)] : 'text-content-400';

  const totalMs = trade.expiryTimestamp - trade.entryTimestamp;
  const progress = totalMs > 0 ? Math.min(100, ((totalMs - remaining) / totalMs) * 100) : 100;

  /*
   * The sell price comes from the server and is refreshed while the position is
   * open. It is only ever displayed - the close request re-prices server-side,
   * so a stale quote on screen cannot be traded against.
   */
  const quoteQuery = useQuery({
    queryKey: ['trade', trade.id, 'quote'],
    queryFn: () => api.trades.quote(trade.id),
    enabled: running && trade.status === 'OPEN',
    refetchInterval: 2000,
    staleTime: 1000,
  });

  const closeMutation = useMutation({
    mutationFn: () => api.trades.closeEarly(trade.id),
    onSuccess: ({ trade: closed, quote }) => {
      settleTrade(closed);
      announceResult(closed);
      void queryClient.invalidateQueries({ queryKey: ['wallet'] });
      void queryClient.invalidateQueries({ queryKey: ['trades'] });
      toast(
        quote.customerResult >= 0 ? 'success' : 'info',
        'Sold early',
        formatMoney(quote.value, closed.currency) + ' returned (' + formatSignedMoney(quote.customerResult, closed.currency) + ')',
      );
    },
    onError: (error) =>
      toast('error', 'Could not sell', error instanceof ApiRequestError ? error.message : undefined),
  });

  const quote = quoteQuery.data;
  // At zero the server is settling it; the word says so rather than the row
  // sitting on "Active" with a clock that has run out.
  const shownStatus = !running && trade.status === 'OPEN' ? { ...trade, status: 'SETTLING' as const } : trade;

  return (
    <View className="px-2.5 py-2">
      <View className="flex-row items-center gap-1.5">
        <DirectionWord direction={trade.direction} className="text-[11px]" />
        <Pressable
          onPress={() => onOpen('/trades/' + trade.id)}
          accessibilityRole="link"
          accessibilityLabel={'View trade ' + trade.symbol}
          className="min-w-0 flex-1"
        >
          <Text numberOfLines={1} className="text-xs font-semibold text-content-100">
            {trade.symbol}
          </Text>
        </Pressable>
        <TradeDemoTag trade={trade} />
        <StatusPill trade={shownStatus} />
      </View>

      <View className="mt-1 flex-row items-baseline gap-2">
        <Text className="text-[11px] font-semibold tnum text-content-200">{formatMoney(trade.stake, trade.currency)}</Text>
        <Text numberOfLines={1} className="min-w-0 shrink text-[11px] text-content-400">
          {durationLabel(durationOf(trade))} · expires{' '}
          <Text className="text-[11px] tnum text-content-200">{clock(trade.expiryTimestamp)}</Text>
        </Text>
        <Text
          accessibilityLabel={'Time left ' + formatCountdown(remaining)}
          style={MONO}
          className={cn('ml-auto text-xs font-bold tnum', remaining <= 10_000 ? 'text-brand' : 'text-content-100')}
        >
          {formatCountdown(remaining)}
        </Text>
      </View>

      <View className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-surface-800">
        <View
          className={cn('h-full rounded-full', remaining > 5000 ? 'bg-accent' : 'bg-warn')}
          style={{ width: `${progress}%` }}
        />
      </View>

      <LiveResultLine result={result} settling={!running} currency={trade.currency} className="mt-1.5" />

      <View className="mt-1.5 flex-row items-center gap-2">
        <Text numberOfLines={1} className="min-w-0 shrink text-[11px] tnum text-content-400">
          {formatPrice(trade.entryPrice, pricePrecision)}
          {' → '}
          <Text className={cn('text-[11px] tnum', priceTone)}>
            {current !== null ? formatPrice(current, pricePrecision) : '--'}
          </Text>
        </Text>

        <View className="ml-auto shrink-0">
          {quote?.available && running ? (
            <Pressable
              disabled={closeMutation.isPending}
              onPress={() => closeMutation.mutate()}
              accessibilityRole="button"
              accessibilityState={{ disabled: closeMutation.isPending, busy: closeMutation.isPending }}
              accessibilityHint={
                'Selling now returns ' +
                formatMoney(quote.value, trade.currency) +
                ' instead of ' +
                formatMoney(trade.potentialReturn, trade.currency) +
                ' if it wins at expiry'
              }
              className={cn(
                'rounded-lg bg-accent px-2.5 py-1.5 active:opacity-85',
                closeMutation.isPending && 'opacity-50',
              )}
            >
              <Text className="text-[11px] font-bold tnum text-surface-950">
                Sell now {formatMoney(quote.value, trade.currency)}
              </Text>
            </Pressable>
          ) : (
            <Text className="text-[10px] text-content-400">
              {!running ? 'Settling…' : (quote?.unavailableReason ?? 'Pricing...')}
            </Text>
          )}
        </View>
      </View>
    </View>
  );
}

function SettledPosition({
  trade,
  pricePrecision,
  expanded,
  onToggle,
  onOpen,
}: {
  trade: Trade;
  pricePrecision: number;
  expanded: boolean;
  onToggle: () => void;
  onOpen: (href: string) => void;
}) {
  // Already net of the stake - see settlementOf.
  const amount = settlementOf(trade) ?? 0;
  const clock = useClockFormatter();

  return (
    <View className={cn(expanded && 'bg-surface-850/60')}>
      <Pressable
        onPress={onToggle}
        accessibilityRole="button"
        accessibilityState={{ expanded }}
        className="px-2.5 py-2 active:bg-surface-850/60"
      >
        <View className="flex-row items-center gap-1.5">
          <DirectionWord direction={trade.direction} className="text-[11px]" />
          <Text numberOfLines={1} className="min-w-0 flex-1 text-xs font-medium text-content-200">
            {trade.symbol}
          </Text>
          <TradeDemoTag trade={trade} />
          <StatusPill trade={trade} />
          <Text className={cn('w-[76px] text-right text-xs font-bold tnum', TONE_TEXT[amountTone(amount)])}>
            {formatSignedMoney(amount, trade.currency)}
          </Text>
          <Icon as={expanded ? ChevronUp : ChevronDown} size={14} className="text-content-400" />
        </View>
        <View className="mt-1 flex-row gap-2">
          <Text className="text-[11px] tnum text-content-400">{formatMoney(trade.stake, trade.currency)}</Text>
          <Text className="text-[11px] tnum text-content-400">{clock(trade.entryTimestamp)}</Text>
          <Text numberOfLines={1} className="min-w-0 shrink text-[11px] tnum text-content-400">
            {formatPrice(trade.entryPrice, pricePrecision)}
            {' → '}
            {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, pricePrecision) : '--'}
          </Text>
        </View>
      </Pressable>

      {expanded ? <TradeFacts trade={trade} pricePrecision={pricePrecision} onOpen={onOpen} /> : null}
    </View>
  );
}

/**
 * A settled trade's terms, opened in place: what was chosen, what it was
 * written at, what happened and when. Every figure is the one frozen on the
 * trade; the result is `settlementOf`, the same number the result card and the
 * detail screen show.
 */
function TradeFacts({
  trade,
  pricePrecision,
  onOpen,
}: {
  trade: Trade;
  pricePrecision: number;
  onOpen: (href: string) => void;
}) {
  const clock = useClockFormatter();
  const net = settlementOf(trade);
  const move = trade.expiryPrice !== null ? trade.expiryPrice - trade.entryPrice : null;
  // A sale ends the trade when it happens; everything else ends at expiry.
  const closedAt =
    trade.status === 'CLOSED_EARLY' && trade.settledAt ? Date.parse(trade.settledAt) : trade.expiryTimestamp;

  return (
    <View className="px-2.5 pb-2.5">
      <View className="flex-row flex-wrap gap-y-1.5 rounded-lg border border-surface-700/70 bg-surface-900 px-2.5 py-2">
        <Fact label="Direction">
          <DirectionWord direction={trade.direction} className="text-[11px]" />
        </Fact>
        <Fact label="Investment">{formatMoney(trade.stake, trade.currency)}</Fact>
        <Fact label="Duration">{durationLabel(durationOf(trade))}</Fact>
        <Fact label="Payout">{trade.payoutPercent}%</Fact>
        <Fact label="Entry → exit price" wide>
          {formatPrice(trade.entryPrice, pricePrecision)} →{' '}
          {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, pricePrecision) : '--'}
          {move !== null ? (
            <Text className="text-[11px] font-normal text-content-400">
              {' '}
              ({move > 0 ? '+' : ''}
              {formatPrice(move, pricePrecision)})
            </Text>
          ) : null}
        </Fact>
        <Fact label="Result">
          <Text className={cn('text-[11px] font-semibold tnum', net !== null ? TONE_TEXT[amountTone(net)] : 'text-content-100')}>
            {net !== null ? formatSignedMoney(net, trade.currency) : '--'}
          </Text>
        </Fact>
        <Fact label="Returned">{formatMoney(payoutOf(trade), trade.currency)}</Fact>
        <Fact label="Opened">{clock(trade.entryTimestamp)}</Fact>
        <Fact label={trade.status === 'CLOSED_EARLY' ? 'Sold' : 'Closed'}>{clock(closedAt)}</Fact>
        <Fact label="Reference" wide>
          <Text style={MONO} className="text-[11px] font-medium text-content-100">
            {trade.reference}
          </Text>
        </Fact>
      </View>
      <Pressable
        onPress={() => onOpen('/trades/' + trade.id)}
        accessibilityRole="link"
        className="mt-2 flex-row items-center gap-1 self-start py-1"
      >
        <Text className="text-xs font-semibold text-accent">View trade</Text>
        <Icon as={ArrowRight} size={14} className="text-accent" />
      </Pressable>
    </View>
  );
}

function Fact({ label, wide, children }: { label: string; wide?: boolean; children: ReactNode }) {
  return (
    <View className={cn('min-w-0 pr-3', wide ? 'w-full' : 'w-1/2')}>
      <Text className="text-[11px] text-content-400">{label}</Text>
      {typeof children === 'string' || typeof children === 'number' ? (
        <Text numberOfLines={1} className="text-[11px] font-semibold tnum text-content-100">
          {children}
        </Text>
      ) : Array.isArray(children) ? (
        <Text numberOfLines={1} className="text-[11px] font-semibold tnum text-content-100">
          {children}
        </Text>
      ) : (
        <View className="flex-row">{children}</View>
      )}
    </View>
  );
}
