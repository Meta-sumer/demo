import { useEffect } from 'react';
import { Link, useParams } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { ArrowLeft, Flag, Timer } from 'lucide-react';
import { api } from '../services/api';
import { useCountdown } from '../hooks/useCountdown';
import { useMarketStore } from '../stores/marketStore';
import { useSettingsStore } from '../stores/settingsStore';
import { formatCountdown, formatDateTime, formatExpiry, formatMoney, formatPrice, formatSignedMoney } from '../utils/format';
import { resolveZone, zoneChip } from '../utils/time';
import { durationOf, isActive, statusLabel } from '../utils/tradeStatus';
import { Panel, Skeleton } from '../components/ui';
import { payoutOf, settlementOf } from '../features/trade/settlement';
import { accountOf } from '../features/trade/ticketModel';
import { DemoTag, DirectionWord, StatusPill, useClockFormatter } from '../features/trade/tradeBits';
import { TradeWindowChart } from '../features/trade/chart/TradeWindowChart';

/**
 * Trade detail (brief UX-15): one structure for every trade, whatever state it
 * is in and whichever screen it was opened from.
 *
 * Asset, status, direction, investment, entry price, duration, expiry time and
 * price, payout and return, result, when it was placed, its reference and its
 * account - in that order, so a trader comparing two trades finds each figure
 * in the same place. A running trade shows its countdown where the result will
 * be, and the current price where the expiry price will be.
 *
 * Beside the figures, the trade drawn against the price it was decided on
 * (Deriv's "Contract details"): the details on the left and the chart on the
 * right on a wide screen; on a phone the result first, then the chart, then
 * the figures. The chart's markers wear the same icons as the figures they
 * mark, so each one explains the other.
 */
export function TradeDetailPage() {
  const { id = '' } = useParams();
  const query = useQuery({
    queryKey: ['trade', id],
    queryFn: () => api.trades.get(id),
    refetchInterval: (result) => {
      const status = result.state.data?.status;
      return status === 'OPEN' || status === 'SETTLING' ? 2000 : false;
    },
  });

  const trade = query.data;

  // The shell scrolls its outlet, not the window, and keeps the offset across
  // routes - arriving from a result card would otherwise land mid-page with
  // "Back to history" scrolled out of sight.
  useEffect(() => {
    document.querySelector('main')?.scrollTo({ top: 0 });
  }, [id]);
  const remaining = useCountdown(trade && trade.status === 'OPEN' ? trade.expiryTimestamp : null);
  const tick = useMarketStore((s) => (trade ? s.ticks[trade.assetId] : undefined));
  const clock = useClockFormatter();
  const zone = resolveZone(useSettingsStore((s) => s.timezone));
  // Same cache as the Trade screen, so this is normally already loaded.
  const assetsQuery = useQuery({ queryKey: ['assets'], queryFn: api.assets.list });

  if (query.isError) {
    return (
      <div className="mx-auto max-w-3xl space-y-3">
        <BackLink />
        <Panel className="px-5 py-8 text-center text-sm text-content-300">This trade could not be loaded.</Panel>
      </div>
    );
  }

  if (query.isLoading || !trade) {
    return (
      <div className="mx-auto max-w-6xl space-y-3">
        <Skeleton className="h-8 w-40" />
        <div className="grid gap-3 lg:grid-cols-[minmax(320px,380px)_minmax(0,1fr)]">
          <Skeleton className="h-64 w-full" />
          <Skeleton className="h-64 w-full lg:h-[460px]" />
        </div>
      </div>
    );
  }

  const open = isActive(trade.status);
  const net = settlementOf(trade);
  const account = accountOf(trade);
  // The asset's own precision, as the chart and ticket use - not a fixed five places.
  const asset = assetsQuery.data?.find((a) => a.id === trade.assetId);
  const precision = asset?.pricePrecision ?? 2;
  const currentPrice = tick?.price ?? null;
  const money = (minor: number) => formatMoney(minor, trade.currency);

  return (
    <div className="mx-auto max-w-6xl space-y-3">
      <BackLink />

      <div
        className={clsx(
          'grid gap-3',
          "[grid-template-areas:'summary'_'chart'_'fields']",
          "lg:grid-cols-[minmax(320px,380px)_minmax(0,1fr)] lg:grid-rows-[auto_1fr] lg:[grid-template-areas:'summary_chart'_'fields_chart']",
        )}
      >
        <Panel className="overflow-hidden [grid-area:summary]">
          <div className="flex flex-wrap items-center gap-3 px-5 py-4">
            <div className="min-w-0">
              <h1 className="flex items-center gap-2 text-lg font-semibold text-content-100">
                {trade.symbol}
                <DemoTag account={account} />
              </h1>
              {asset ? <p className="text-xs text-content-400">{asset.name}</p> : null}
            </div>

            <div className="ml-auto text-right">
              <StatusPill trade={trade} className="text-xs" />
              <div
                className={clsx(
                  'mt-1 text-xl font-bold tnum',
                  open
                    ? 'text-content-100'
                    : (net ?? 0) > 0
                      ? 'text-up'
                      : (net ?? 0) < 0
                        ? 'text-down'
                        : 'text-content-200',
                )}
              >
                {open ? formatCountdown(remaining) : net !== null ? formatSignedMoney(net, trade.currency) : '--'}
              </div>
            </div>
          </div>
        </Panel>

        <Panel className="flex min-w-0 flex-col overflow-hidden [grid-area:chart] lg:min-h-[480px]">
          <div className="flex items-center gap-2 border-b border-surface-800 px-4 py-2.5">
            <h2 className="text-sm font-semibold text-content-100">Price during this trade</h2>
            <DirectionWord direction={trade.direction} className="text-xs" />
            <span className="ml-auto text-[11px] text-content-400">Times in {zoneChip(zone)}</span>
          </div>
          <div className="min-h-0 flex-1">
            <TradeWindowChart trade={trade} precision={precision} zone={zone} />
          </div>
        </Panel>

        <Panel className="overflow-hidden [grid-area:fields]">
          <dl className="grid grid-cols-2 gap-px bg-surface-800/60 sm:grid-cols-3 lg:grid-cols-2">
            <Field label="Asset" value={trade.symbol} />
            <Field label="Status" value={statusLabel(trade.status)} />
            <Field label="Direction" value={<DirectionWord direction={trade.direction} />} />
            <Field label="Investment" value={money(trade.stake)} />
            <Field label="Entry price" icon={<EntrySpot />} value={formatPrice(trade.entryPrice, precision)} mono />
            <Field label="Duration" value={formatExpiry(durationOf(trade))} />
            <Field
              label="Expiry time"
              icon={<Flag className="h-3 w-3" />}
              value={
                clock(trade.expiryTimestamp) +
                ', ' +
                new Date(trade.expiryTimestamp).toLocaleDateString('en-GB', { day: 'numeric', month: 'short' })
              }
            />
            <Field
              label={open ? 'Current price' : 'Expiry price'}
              icon={open ? undefined : <ExitSpot />}
              value={
                open
                  ? currentPrice !== null
                    ? formatPrice(currentPrice, precision)
                    : '--'
                  : trade.expiryPrice !== null
                    ? formatPrice(trade.expiryPrice, precision)
                    : '--'
              }
              mono
            />
            <Field label="Payout" value={trade.payoutPercent + '%'} />
            <Field
              label={open ? 'Return if it wins' : 'Returned'}
              value={open ? money(trade.potentialReturn) : money(payoutOf(trade))}
            />
            <Field
              label="Result"
              value={open ? 'Pending' : net !== null ? formatSignedMoney(net, trade.currency) : '--'}
              tone={open || net === null ? undefined : net > 0 ? 'up' : net < 0 ? 'down' : undefined}
            />
            <Field label="Maximum loss" value={money(trade.stake)} />
            <Field label="Placed at" icon={<Timer className="h-3 w-3" />} value={formatDateTime(trade.entryTimestamp)} />
            <Field label="Settled at" value={trade.settledAt ? formatDateTime(trade.settledAt) : '--'} />
            <Field label="Account" value={account === 'DEMO' ? 'Demo' : 'Live'} />
            <Field label="Trade ID" value={trade.reference} mono />
          </dl>

          <p className="border-t border-surface-800 px-5 py-3 text-[11px] leading-relaxed text-content-400">
            Entry price, timestamps and payout were fixed by the platform when this trade was accepted and are never
            changed afterwards. The expiry price is the last quoted price at or before the expiry instant.
          </p>
        </Panel>
      </div>
    </div>
  );
}

function BackLink() {
  return (
    <Link to="/trades?tab=history" className="inline-flex items-center gap-1.5 text-sm text-content-400 hover:text-content-100">
      <ArrowLeft className="h-4 w-4" />
      Back to history
    </Link>
  );
}

/** The chart's entry marker, as a legend beside the figure it marks. */
function EntrySpot() {
  return <span className="inline-block h-2.5 w-2.5 rounded-full border-[1.5px] border-current" aria-hidden />;
}

function ExitSpot() {
  return <span className="inline-block h-2.5 w-2.5 rounded-full bg-current" aria-hidden />;
}

function Field({
  label,
  value,
  mono,
  tone,
  icon,
}: {
  label: string;
  value: React.ReactNode;
  mono?: boolean;
  tone?: 'up' | 'down';
  /** The chart marker this figure corresponds to. */
  icon?: React.ReactNode;
}) {
  return (
    <div className="min-w-0 bg-surface-900 px-5 py-3">
      <dt className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-content-400">
        {icon}
        {label}
      </dt>
      <dd
        className={clsx(
          'mt-0.5 break-words text-sm font-medium tnum',
          tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : 'text-content-100',
          mono && 'font-mono',
        )}
      >
        {value}
      </dd>
    </div>
  );
}
