import clsx from 'clsx';
import { useId, useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { ArrowRight, CandlestickChart } from 'lucide-react';
import { api } from '../services/api';
import { useMarketStore } from '../stores/marketStore';
import { useLiveFeeds } from '../features/trade/useLiveFeeds';
import { payoutText } from '../features/landing/markets';
import { drawPolicyOf } from '../features/trade/drawRule';
import { formatExpiry, formatMoneyCompact, formatPrice } from '../utils/format';
import { AvailabilityLabel, FavouriteButton, Movement, useAssetPrice, useReopening } from './AssetRow';
import { assetAvailability, offeredPayouts, payoutRange } from './assetList';
import {
  SPARKLINE_CANDLES,
  SPARKLINE_INTERVAL_SECONDS,
  sparklinePaths,
  sparklineSeries,
  sparklineSpanLabel,
  sparklineTrend,
} from './sparkline';
import { Skeleton } from './ui';
import type { Asset } from '../types/api';

/** What a tie settles as, under the asset's rule (drawRule.ts). */
const TIE_RESULT = { REFUND: 'Investment refunded', LOSS: 'Counts as a loss', WIN: 'Counts as a win' } as const;

/**
 * An instrument as a card: what it costs now, how it has moved, what it pays
 * and one button to trade it.
 *
 * Home and Markets show these while there are only a few instruments, where a
 * table of two rows would leave a page of empty space. Every figure is the
 * server's: the price is the latest tick (else the catalogue's last price),
 * the 24h change and the payouts come from the catalogue, and the line is the
 * last hour of the server's one-minute candles. The payouts are the engine's
 * indicative figures - the lower of UP and DOWN - rounded down, so a card
 * never shows more than the ticket will.
 *
 * The star is its own control, as on a list row: pressing it never opens the
 * market.
 *
 * `detailed` (Markets) draws the line full width and taller, and adds the
 * asset's stake range and tie rule - both from the catalogue row - so the
 * card answers "what can I trade here" without opening the ticket.
 */
export function MarketCard({
  asset,
  onTrade,
  detailed = false,
  className,
}: {
  asset: Asset;
  onTrade: (asset: Asset) => void;
  detailed?: boolean;
  className?: string;
}) {
  const price = useAssetPrice(asset);
  const reopens = useReopening(asset);
  const availability = assetAvailability(asset);
  const offered = offeredPayouts(asset);
  const range = payoutRange(asset);
  const titleId = useId();

  return (
    <article
      aria-labelledby={titleId}
      className={clsx('panel flex min-w-0 flex-col p-4 transition hover:border-surface-600', className)}
    >
      <header className="flex items-start gap-3">
        <span
          aria-hidden
          className="grid h-10 w-10 shrink-0 place-items-center rounded-xl bg-accent/10 text-[11px] font-black tracking-tight text-accent ring-1 ring-inset ring-accent/25"
        >
          {asset.symbol.split('/')[0]!.slice(0, 4)}
        </span>
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-2">
            <h3 id={titleId} className="truncate text-sm font-bold text-content-100">
              {asset.symbol}
            </h3>
            <AvailabilityLabel asset={asset} className="shrink-0" />
          </span>
          <span className="block truncate text-[11px] text-content-400">
            {asset.name}
            {reopens ? <span className="font-semibold text-content-300"> · {reopens}</span> : null}
          </span>
        </span>
        <FavouriteButton asset={asset} className="-mr-2 -mt-1 h-9" />
      </header>

      <div className="mt-4 flex items-end gap-4">
        <div className="min-w-0 shrink-0">
          <div className="text-2xl font-bold leading-none tnum text-content-100">
            {price !== null ? formatPrice(price, asset.pricePrecision) : '--'}
          </div>
          <div className="mt-1.5 flex items-center gap-1.5 text-[11px] text-content-400">
            {asset.change24hPercent != null ? (
              <>
                <Movement percent={asset.change24hPercent} className="text-xs" />
                <span>24h</span>
              </>
            ) : (
              <span>No 24h history yet</span>
            )}
          </div>
        </div>
        {detailed ? null : <MarketSparkline asset={asset} className="min-w-0 flex-1" />}
      </div>

      {detailed ? <MarketSparkline asset={asset} tall className="mt-3" /> : null}

      {offered.length > 0 ? (
        <ul className="mt-4 flex flex-wrap gap-1.5" aria-label={'Payout by duration on ' + asset.symbol}>
          {offered.map((row) => (
            <li
              key={row.expirySeconds}
              className="inline-flex items-baseline gap-1.5 rounded-md border border-surface-700 bg-surface-850 px-2 py-1 text-[11px]"
            >
              <span className="font-medium text-content-400">{formatExpiry(row.expirySeconds)}</span>
              <span className="font-semibold tnum text-content-100">{payoutText(row.payoutPercent)}</span>
            </li>
          ))}
        </ul>
      ) : null}

      {detailed ? (
        <dl className="mt-3 grid grid-cols-2 gap-3 text-[11px]">
          <div className="min-w-0">
            <dt className="text-content-400">Investment</dt>
            <dd className="truncate font-semibold tnum text-content-200">
              {formatMoneyCompact(asset.minStake)} – {formatMoneyCompact(asset.maxStake)}
            </dd>
          </div>
          <div className="min-w-0">
            <dt className="text-content-400">Same price at expiry</dt>
            <dd className="truncate font-semibold text-content-200">{TIE_RESULT[drawPolicyOf(asset)]}</dd>
          </div>
        </dl>
      ) : null}

      <footer className="mt-4 flex items-center justify-between gap-3 border-t border-surface-800 pt-3">
        <div className="min-w-0">
          <div className="text-[10px] font-semibold uppercase tracking-wider text-content-400">Payout</div>
          {range ? (
            <div className="text-sm font-bold tnum text-up">
              {range.min === range.max ? (
                payoutText(range.max)
              ) : (
                <>
                  {payoutText(range.min).replace('%', '')}–{payoutText(range.max)}
                </>
              )}
            </div>
          ) : (
            <div className="text-xs font-semibold text-content-400">Not available right now</div>
          )}
        </div>
        <button
          type="button"
          onClick={() => onTrade(asset)}
          className={clsx('shrink-0 px-5', availability === 'open' ? 'btn-primary' : 'btn-ghost')}
          aria-label={(availability === 'open' ? 'Trade ' : 'Open the chart of ') + asset.symbol}
        >
          {availability === 'open' ? (
            <>
              Trade
              <ArrowRight className="h-4 w-4" />
            </>
          ) : (
            <>
              <CandlestickChart className="h-4 w-4" />
              View chart
            </>
          )}
        </button>
      </footer>
    </article>
  );
}

/** The last hour of closes as a line, the live price on its end. */
function MarketSparkline({ asset, tall = false, className }: { asset: Asset; tall?: boolean; className?: string }) {
  const tick = useMarketStore((s) => s.ticks[asset.id]);
  const query = useQuery({
    queryKey: ['sparkline', asset.id],
    queryFn: () => api.market.candles(asset.id, SPARKLINE_CANDLES, SPARKLINE_INTERVAL_SECONDS),
    refetchInterval: 60_000,
    staleTime: 30_000,
  });
  const series = useMemo(
    () => sparklineSeries(query.data ?? [], tick ? { price: tick.price, timestamp: tick.timestamp } : null),
    [query.data, tick],
  );
  const gradientId = 'spark-' + useId().replace(/[^a-zA-Z0-9_-]/g, '');

  const width = tall ? 480 : 240;
  const height = tall ? 112 : 56;
  const box = tall ? 'h-20 sm:h-28' : 'h-14';
  const paths = sparklinePaths(series.values, width, height);
  const trend = sparklineTrend(series.values);
  const span = sparklineSpanLabel(series);

  if (query.isLoading) return <Skeleton className={clsx(box, className)} />;

  return (
    <div className={clsx('flex flex-col items-stretch', className)}>
      {paths ? (
        <svg
          viewBox={'0 0 ' + width + ' ' + height}
          preserveAspectRatio="none"
          className={clsx(box, 'w-full', trend === 'down' ? 'text-down' : trend === 'up' ? 'text-up' : 'text-content-400')}
          role="img"
          aria-label={asset.symbol + ' price, ' + (span ?? 'recent').toLowerCase()}
        >
          <defs>
            <linearGradient id={gradientId} x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="currentColor" stopOpacity="0.22" />
              <stop offset="100%" stopColor="currentColor" stopOpacity="0" />
            </linearGradient>
          </defs>
          <path d={paths.area} fill={'url(#' + gradientId + ')'} stroke="none" />
          <path
            d={paths.line}
            fill="none"
            stroke="currentColor"
            strokeWidth="1.75"
            strokeLinejoin="round"
            strokeLinecap="round"
            vectorEffect="non-scaling-stroke"
          />
        </svg>
      ) : (
        <div
          className={clsx(
            box,
            'grid place-items-center rounded-md border border-dashed border-surface-700 text-[10px] text-content-400',
          )}
        >
          No recent prices yet
        </div>
      )}
      {span ? <span className="mt-0.5 text-right text-[10px] text-content-400">{span}</span> : null}
    </div>
  );
}

/**
 * Cards for a few instruments, with their prices streaming.
 *
 * Off the trade screen nothing else subscribes to an instrument's ticks, so
 * the grid holds the channels for the cards it shows and lets them go when it
 * leaves (the trade screen subscribes its own on arrival).
 */
export function MarketCardGrid({
  assets,
  onTrade,
  detailed = false,
  className,
}: {
  assets: Asset[];
  onTrade: (asset: Asset) => void;
  detailed?: boolean;
  className?: string;
}) {
  const ids = useMemo(() => assets.map((asset) => asset.id), [assets]);
  useLiveFeeds(ids, { keepWorkspace: false });

  return (
    <ul className={clsx('grid gap-3', className)}>
      {assets.map((asset) => (
        <li key={asset.id} className="flex min-w-0">
          <MarketCard asset={asset} onTrade={onTrade} detailed={detailed} className="w-full" />
        </li>
      ))}
    </ul>
  );
}

/** Placeholders the size of a card while the catalogue loads. */
export function MarketCardSkeletons({ count = 2, className }: { count?: number; className?: string }) {
  return (
    <div className={clsx('grid gap-3', className)}>
      {Array.from({ length: count }).map((_, index) => (
        <div key={index} className="panel space-y-4 p-4">
          <div className="flex items-center gap-3">
            <Skeleton className="h-10 w-10 rounded-xl" />
            <div className="flex-1 space-y-1.5">
              <Skeleton className="h-3.5 w-24" />
              <Skeleton className="h-3 w-36" />
            </div>
          </div>
          <Skeleton className="h-14 w-full" />
          <Skeleton className="h-9 w-full" />
        </div>
      ))}
    </div>
  );
}
