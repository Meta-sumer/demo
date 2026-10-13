import { useId, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import clsx from 'clsx';
import { ArrowRight, ChevronDown } from 'lucide-react';
import { api } from '../../services/api';
import { ApiRequestError } from '../../services/http';
import { useMarketStore } from '../../stores/marketStore';
import { useTradeStore } from '../../stores/tradeStore';
import { useAccountStore } from '../../stores/accountStore';
import { useCountdown, useNow } from '../../hooks/useCountdown';
import { formatCountdown, formatMoney, formatPrice, formatSignedMoney } from '../../utils/format';
import { groupByDay } from '../../utils/dayGroups';
import { durationOf } from '../../utils/tradeStatus';
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
import { EmptyState } from '../../components/ui';
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
 * place to show its full terms (record 17, Pocket Option) - the detail page is
 * one link further for everything else.
 */
export function PositionsPanel({ precisions = {} }: { precisions?: Record<string, number> }) {
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
   * settled while this tab was open - a trader coming back tomorrow still
   * sees yesterday's results here. Settlements from this session are merged
   * in so a result appears the moment it lands, before the refetch.
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
    () => groupByDay(settled, (trade) => trade.entryTimestamp, today, zone),
    [settled, today, zone],
  );

  const empty = tab === 'open' ? openTrades.length === 0 : settled.length === 0;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 border-b border-surface-800">
        <TabButton
          active={tab === 'open'}
          onClick={() => setTab('open')}
          count={openTrades.length}
          live={openTrades.length > 0}
        >
          Active
        </TabButton>
        <TabButton active={tab === 'history'} onClick={() => setTab('history')}>
          History
        </TabButton>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        {tab === 'history' && historyQuery.isLoading && settled.length === 0 ? (
          <p className="px-3 py-6 text-center text-xs text-content-400">Loading recent trades…</p>
        ) : empty ? (
          <EmptyState
            title={tab === 'open' ? 'No active trades' : 'No settled trades yet'}
            description={
              tab === 'open'
                ? 'Place a trade and it will be tracked here in real time.'
                : 'Your settled trades on this account appear here.'
            }
          />
        ) : tab === 'open' ? (
          <>
            <OpenTotal trades={openTrades} />
            <ul className="divide-y divide-surface-800/70">
              {openTrades.map((trade) => (
                <OpenPosition key={trade.id} trade={trade} pricePrecision={precisions[trade.assetId] ?? 2} />
              ))}
            </ul>
          </>
        ) : (
          days.map((day) => (
            <section key={day.key} aria-label={day.label}>
              <h3 className="sticky top-0 z-10 border-b border-surface-800/70 bg-surface-900 px-2.5 py-1 text-[10px] font-semibold uppercase tracking-wider text-content-400">
                {day.label}
              </h3>
              <ul className="divide-y divide-surface-800/70">
                {day.items.map((trade) => (
                  <SettledPosition
                    key={trade.id}
                    trade={trade}
                    pricePrecision={precisions[trade.assetId] ?? 2}
                    expanded={expandedId === trade.id}
                    onToggle={() => setExpandedId((current) => (current === trade.id ? null : trade.id))}
                  />
                ))}
              </ul>
            </section>
          ))
        )}

        {tab === 'history' ? (
          <div className="border-t border-surface-800/70 px-3 py-2 text-center">
            <Link to="/trades?tab=history" className="text-xs font-semibold text-accent hover:underline">
              View all trades
            </Link>
          </div>
        ) : null}
      </div>
    </div>
  );
}

function TabButton({
  active,
  count,
  live,
  onClick,
  children,
}: {
  active: boolean;
  count?: number;
  /** Something is running right now, whether or not this tab is showing. */
  live?: boolean;
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={clsx(
        'flex flex-1 items-center justify-center gap-2 border-b-2 px-2 py-2 text-[11px] font-semibold transition',
        active
          ? 'border-accent text-content-100'
          : 'border-transparent text-content-400 hover:text-content-200',
      )}
    >
      {/* The dot is the one part that reads from across the desk: an order is
          live on the chart, even while the History tab is open. */}
      {live ? (
        <span className="relative flex h-1.5 w-1.5 shrink-0" aria-hidden>
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-brand opacity-70" />
          <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-brand" />
        </span>
      ) : null}
      {children}
      {count !== undefined ? (
        <span
          className={clsx(
            'rounded px-1.5 py-0.5 text-[10px] font-bold tnum',
            live && !active
              ? 'bg-brand/20 text-brand'
              : active
                ? 'bg-accent/20 text-accent'
                : 'bg-surface-800 text-content-400',
          )}
        >
          {count}
        </span>
      ) : null}
    </button>
  );
}

const TONE_TEXT = { up: 'text-up', down: 'text-down', neutral: 'text-content-100' } as const;

/**
 * "Open trades: +₹330.00 if they ended now" - every running trade on the
 * account, added up (Deriv's "Total profit/loss"). Trades still waiting for a
 * price are named rather than silently left out of the sum.
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
    <p className="sticky top-0 z-10 flex flex-wrap items-baseline gap-x-1 border-b border-surface-800/70 bg-surface-900 px-2.5 py-1.5 text-[11px] text-content-400">
      <span className="font-semibold text-content-200">Open trades:</span>
      {total.running === 0 ? (
        <span>results are being settled…</span>
      ) : total.priced === 0 ? (
        <span>waiting for prices</span>
      ) : (
        <>
          <span className={clsx('font-bold tnum', TONE_TEXT[amountTone(total.amount)])}>
            {formatSignedMoney(total.amount, currency)}
          </span>
          <span>if they ended now</span>
          {missing > 0 ? <span>· {missing} waiting for a price</span> : null}
        </>
      )}
    </p>
  );
}

function OpenPosition({ trade, pricePrecision }: { trade: Trade; pricePrecision: number }) {
  const queryClient = useQueryClient();
  const remaining = useCountdown(trade.expiryTimestamp);
  const current = useLivePrice(trade.assetId);
  const settleTrade = useTradeStore((s) => s.settleTrade);
  const clock = useClockFormatter();
  const drawPolicy = useDrawPolicy(trade.assetId);

  const running = remaining > 0;
  const result = running ? liveResult(trade, current, drawPolicy) : null;
  const priceTone = result ? TONE_TEXT[amountTone(result.amount)] : '';

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
    <li className="px-2.5 py-2">
      <div className="flex items-center gap-1.5">
        <DirectionWord direction={trade.direction} className="text-[11px]" />
        <Link
          to={'/trades/' + trade.id}
          className="min-w-0 flex-1 truncate text-xs font-semibold text-content-100 hover:underline"
        >
          {trade.symbol}
        </Link>
        <TradeDemoTag trade={trade} />
        <StatusPill trade={shownStatus} />
      </div>

      <div className="mt-1 flex items-baseline gap-2 text-[11px]">
        <span className="font-semibold tnum text-content-200">{formatMoney(trade.stake, trade.currency)}</span>
        <span className="min-w-0 truncate text-content-400">
          {durationLabel(durationOf(trade))} · expires{' '}
          <span className="tnum text-content-200">{clock(trade.expiryTimestamp)}</span>
        </span>
        <span
          className={clsx(
            'ml-auto font-mono text-xs font-bold tnum',
            remaining <= 10_000 ? 'text-brand' : 'text-content-100',
          )}
          aria-label="Time left"
        >
          {formatCountdown(remaining)}
        </span>
      </div>

      <div className="mt-1.5 h-1 w-full overflow-hidden rounded-full bg-surface-800">
        <div
          className={clsx('h-full rounded-full transition-[width] duration-300', remaining > 5000 ? 'bg-accent' : 'bg-warn')}
          style={{ width: progress + '%' }}
        />
      </div>

      <LiveResultLine result={result} settling={!running} currency={trade.currency} className="mt-1.5" />

      <div className="mt-1.5 flex items-center gap-2 text-[11px]">
        <span className="min-w-0 truncate tnum text-content-400">
          {formatPrice(trade.entryPrice, pricePrecision)}
          {' → '}
          <span className={priceTone}>{current !== null ? formatPrice(current, pricePrecision) : '--'}</span>
        </span>

        <span className="ml-auto shrink-0">
          {quote?.available && running ? (
            <button
              type="button"
              disabled={closeMutation.isPending}
              onClick={() => closeMutation.mutate()}
              className="rounded-lg bg-accent px-2.5 py-1 text-[11px] font-bold text-surface-950 transition hover:brightness-110 disabled:opacity-50"
              title={
                'Selling now returns ' + formatMoney(quote.value, trade.currency) +
                ' instead of ' + formatMoney(trade.potentialReturn, trade.currency) + ' if it wins at expiry'
              }
            >
              Sell now {formatMoney(quote.value, trade.currency)}
            </button>
          ) : (
            <span className="text-[10px] text-content-400">
              {!running ? 'Settling…' : (quote?.unavailableReason ?? 'Pricing...')}
            </span>
          )}
        </span>
      </div>
    </li>
  );
}

function SettledPosition({
  trade,
  pricePrecision,
  expanded,
  onToggle,
}: {
  trade: Trade;
  pricePrecision: number;
  expanded: boolean;
  onToggle: () => void;
}) {
  // Already net of the stake - see settlementOf.
  const amount = settlementOf(trade) ?? 0;
  const clock = useClockFormatter();
  const detailsId = useId();

  return (
    <li className={clsx(expanded && 'bg-surface-850/60')}>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={expanded}
        aria-controls={detailsId}
        className="block w-full px-2.5 py-2 text-left transition hover:bg-surface-850/60"
      >
        <span className="flex items-center gap-1.5">
          <DirectionWord direction={trade.direction} className="text-[11px]" />
          <span className="min-w-0 flex-1 truncate text-xs font-medium text-content-200">{trade.symbol}</span>
          <TradeDemoTag trade={trade} />
          <StatusPill trade={trade} />
          <span className={clsx('w-[76px] text-right text-xs font-bold tnum', TONE_TEXT[amountTone(amount)])}>
            {formatSignedMoney(amount, trade.currency)}
          </span>
          <ChevronDown
            className={clsx('h-3.5 w-3.5 shrink-0 text-content-400 transition-transform', expanded && 'rotate-180')}
            aria-hidden
          />
        </span>
        <span className="mt-1 flex gap-2 text-[11px] tnum text-content-400">
          <span>{formatMoney(trade.stake, trade.currency)}</span>
          <span>{clock(trade.entryTimestamp)}</span>
          <span className="min-w-0 truncate">
            {formatPrice(trade.entryPrice, pricePrecision)}
            {' → '}
            {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, pricePrecision) : '--'}
          </span>
        </span>
      </button>

      <div id={detailsId} hidden={!expanded}>
        {expanded ? <TradeFacts trade={trade} pricePrecision={pricePrecision} /> : null}
      </div>
    </li>
  );
}

/**
 * A settled trade's terms, opened in place: what was chosen, what it was
 * written at, what happened and when. Every figure is the one frozen on the
 * trade; the result is `settlementOf`, the same number the result card and the
 * detail page show.
 */
function TradeFacts({ trade, pricePrecision }: { trade: Trade; pricePrecision: number }) {
  const clock = useClockFormatter();
  const net = settlementOf(trade);
  const move = trade.expiryPrice !== null ? trade.expiryPrice - trade.entryPrice : null;
  // A sale ends the trade when it happens; everything else ends at expiry.
  const closedAt =
    trade.status === 'CLOSED_EARLY' && trade.settledAt ? Date.parse(trade.settledAt) : trade.expiryTimestamp;

  return (
    <div className="px-2.5 pb-2.5">
      <dl className="grid grid-cols-2 gap-x-3 gap-y-1.5 rounded-lg border border-surface-700/70 bg-surface-900 px-2.5 py-2 text-[11px]">
        <Fact label="Direction">
          <DirectionWord direction={trade.direction} />
        </Fact>
        <Fact label="Investment">{formatMoney(trade.stake, trade.currency)}</Fact>
        <Fact label="Duration">{durationLabel(durationOf(trade))}</Fact>
        <Fact label="Payout">{trade.payoutPercent}%</Fact>
        <Fact label="Entry → exit price" wide>
          {formatPrice(trade.entryPrice, pricePrecision)} →{' '}
          {trade.expiryPrice !== null ? formatPrice(trade.expiryPrice, pricePrecision) : '--'}
          {move !== null ? (
            <span className="font-normal text-content-400">
              {' '}
              ({move > 0 ? '+' : ''}
              {formatPrice(move, pricePrecision)})
            </span>
          ) : null}
        </Fact>
        <Fact label="Result">
          <span className={net !== null ? TONE_TEXT[amountTone(net)] : undefined}>
            {net !== null ? formatSignedMoney(net, trade.currency) : '--'}
          </span>
        </Fact>
        <Fact label="Returned">{formatMoney(payoutOf(trade), trade.currency)}</Fact>
        <Fact label="Opened">{clock(trade.entryTimestamp)}</Fact>
        <Fact label={trade.status === 'CLOSED_EARLY' ? 'Sold' : 'Closed'}>{clock(closedAt)}</Fact>
        <Fact label="Reference" wide>
          <span className="font-mono font-medium">{trade.reference}</span>
        </Fact>
      </dl>
      <Link
        to={'/trades/' + trade.id}
        className="mt-2 inline-flex items-center gap-1 text-xs font-semibold text-accent hover:underline"
      >
        View trade
        <ArrowRight className="h-3.5 w-3.5" />
      </Link>
    </div>
  );
}

function Fact({ label, wide, children }: { label: string; wide?: boolean; children: React.ReactNode }) {
  return (
    <div className={clsx('min-w-0', wide && 'col-span-2')}>
      <dt className="text-content-400">{label}</dt>
      <dd className="truncate font-semibold tnum text-content-100">{children}</dd>
    </div>
  );
}
