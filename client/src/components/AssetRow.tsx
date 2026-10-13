import clsx from 'clsx';
import { useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import { Star } from 'lucide-react';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { useMarketStore } from '../stores/marketStore';
import { useFavouritesStore } from '../stores/favouritesStore';
import { useSettingsStore } from '../stores/settingsStore';
import { formatPrice } from '../utils/format';
import { resolveZone } from '../utils/time';
import { assetAvailability, bestPayout, formatNextOpen, type AssetAvailability } from './assetList';
import type { Asset } from '../types/api';

// The list's rules live in assetList.ts, where they are tested without React;
// these names stay importable from here, where they have always been.
export { assetAvailability, bestPayout, type AssetAvailability };

/**
 * The one way an instrument is shown in a list.
 *
 * Home, Markets and the selector inside Trade all render this, so a symbol,
 * its price, whether it can be traded and whether it is a favourite look and
 * read the same wherever the customer meets them (brief section 28). The star
 * is its own control beside the row rather than inside it: pressing it must
 * never also open the market.
 */

/**
 * Opens an instrument on the trade screen from anywhere outside it.
 *
 * Makes it the workspace's active asset (a tab too, in advanced mode) and
 * records it as recent, so Home and the selector can offer it back.
 */
export function useOpenMarket(): (asset: Pick<Asset, 'id'>) => void {
  const navigate = useNavigate();
  const openAsset = useWorkspaceStore((s) => s.openAsset);
  const pushRecent = useFavouritesStore((s) => s.pushRecent);
  return useCallback(
    (asset) => {
      openAsset(asset.id);
      pushRecent(asset.id);
      navigate('/trade');
    },
    [navigate, openAsset, pushRecent],
  );
}

const AVAILABILITY_LABEL: Record<AssetAvailability, string> = {
  open: 'Open',
  paused: 'Paused',
  closed: 'Market closed',
};

/** Live tick when one has arrived, else the last price the list endpoint saw. */
export function useAssetPrice(asset: Asset): number | null {
  const tick = useMarketStore((s) => s.ticks[asset.id]?.price);
  return tick ?? asset.feed.lastPrice ?? null;
}

export function AvailabilityLabel({ asset, className }: { asset: Asset; className?: string }) {
  const state = assetAvailability(asset);
  return (
    <span
      className={clsx(
        'inline-flex items-center gap-1 text-[11px] font-medium',
        state === 'open' ? 'text-up' : state === 'paused' ? 'text-warn' : 'text-content-400',
        className,
      )}
    >
      <span
        className={clsx(
          'h-1.5 w-1.5 shrink-0 rounded-full',
          state === 'open' ? 'bg-up' : state === 'paused' ? 'bg-warn' : 'bg-surface-500',
        )}
      />
      {AVAILABILITY_LABEL[state]}
    </span>
  );
}

/** 24-hour movement with its sign. Nothing at all when there is no history. */
export function Movement({ percent, className }: { percent: number | null | undefined; className?: string }) {
  if (percent == null || !Number.isFinite(percent)) return null;
  const tone = percent > 0 ? 'text-up' : percent < 0 ? 'text-down' : 'text-content-400';
  const sign = percent > 0 ? '+' : '';
  return (
    <span className={clsx('text-[11px] font-semibold tnum', tone, className)} title="Change over 24 hours">
      {sign}
      {percent.toFixed(2)}%
    </span>
  );
}

export function FavouriteButton({ asset, className }: { asset: Asset; className?: string }) {
  const favourite = useFavouritesStore((s) => s.favourites.includes(asset.id));
  const toggle = useFavouritesStore((s) => s.toggle);
  return (
    <button
      type="button"
      onClick={(event) => {
        // Belt and braces: the star sits beside the row button, but a host
        // that wraps the row in its own clickable must not open the market.
        event.stopPropagation();
        toggle(asset.id);
      }}
      aria-pressed={favourite}
      aria-label={(favourite ? 'Remove ' : 'Add ') + asset.symbol + (favourite ? ' from favourites' : ' to favourites')}
      title={favourite ? 'Remove from favourites' : 'Add to favourites'}
      className={clsx(
        'grid w-11 shrink-0 place-items-center transition',
        favourite ? 'text-warn' : 'text-content-400 hover:text-content-200',
        className,
      )}
    >
      <Star className="h-[18px] w-[18px]" fill={favourite ? 'currentColor' : 'none'} />
    </button>
  );
}

interface AssetRowProps {
  asset: Asset;
  onSelect: (asset: Asset) => void;
  /** The instrument currently on the trade screen. */
  selected?: boolean;
  /**
   * From the lg breakpoint, lay the row out as a table row - market, price,
   * 24h, payout, status in columns under `AssetTableHeader` - for a list that
   * has the whole page width (Markets). Below lg it is the ordinary row.
   */
  wide?: boolean;
}

/**
 * The columns of a wide row, shared with its header so they line up. Every
 * cell is placed explicitly, so a cell with nothing to show (no 24h history)
 * leaves its column empty instead of shifting the next one into it.
 */
const WIDE_GRID = 'lg:grid lg:grid-cols-[minmax(0,1fr)_9rem_6rem_6rem_9rem] lg:gap-4';

/** Column titles over a list of wide rows; shown from lg, where the rows become a table. */
export function AssetTableHeader({ className }: { className?: string }) {
  return (
    <div className={clsx('hidden lg:flex', className)} aria-hidden>
      <div
        className={clsx(
          'flex-1 py-2 pl-4 text-[11px] font-semibold uppercase tracking-wider text-content-400',
          WIDE_GRID,
        )}
      >
        <span>Market</span>
        <span className="text-right">Price</span>
        <span className="text-right">24h</span>
        <span className="text-right">Payout</span>
        <span>Status</span>
      </div>
      {/* The star's column. */}
      <span className="w-11 shrink-0" />
    </div>
  );
}

/**
 * "Opens Mon 09:00" for a closed market whose schedule the server sent, in the
 * trader's chosen zone. Null otherwise - an unknown opening says nothing
 * rather than guessing.
 */
export function useReopening(asset: Pick<Asset, 'marketOpen' | 'nextOpenAt'>): string | null {
  const zone = resolveZone(useSettingsStore((s) => s.timezone));
  if (asset.marketOpen) return null;
  // The list refreshes every few seconds, which keeps "today" current enough.
  return formatNextOpen(asset.nextOpenAt, Date.now(), zone);
}

export function AssetRow({ asset, onSelect, selected, wide = false }: AssetRowProps) {
  const price = useAssetPrice(asset);
  const payout = bestPayout(asset);
  const reopens = useReopening(asset);

  return (
    <div className={clsx('flex items-stretch transition', selected ? 'bg-accent/10' : 'hover:bg-surface-850')}>
      <button
        type="button"
        onClick={() => onSelect(asset)}
        aria-current={selected ? 'true' : undefined}
        className={clsx('flex min-w-0 flex-1 items-center gap-3 py-2.5 pl-3 text-left sm:pl-4', wide && WIDE_GRID)}
      >
        <span className={clsx('min-w-0 flex-1', wide && 'lg:col-start-1 lg:row-start-1')}>
          <span className="flex items-baseline gap-2">
            <span className="truncate text-sm font-semibold text-content-100">{asset.symbol}</span>
            <AvailabilityLabel asset={asset} className={clsx('shrink-0', wide && 'lg:hidden')} />
          </span>
          <span className="flex min-w-0 items-baseline gap-1 text-[11px]">
            <span className="truncate text-content-400">{asset.name}</span>
            {reopens ? <span className="shrink-0 font-semibold text-content-300">· {reopens}</span> : null}
          </span>
        </span>

        {/* On a wide row these wrappers dissolve (display: contents) and each
            value takes its own column. */}
        <span className={clsx('shrink-0 text-right', wide && 'lg:contents')}>
          <span
            className={clsx(
              'block text-sm font-semibold tnum text-content-100',
              wide && 'lg:col-start-2 lg:row-start-1 lg:text-right',
            )}
          >
            {price !== null ? formatPrice(price, asset.pricePrecision) : '--'}
          </span>
          <span className={clsx('flex items-center justify-end gap-2', wide && 'lg:contents')}>
            <Movement
              percent={asset.change24hPercent}
              className={clsx(wide && 'lg:col-start-3 lg:row-start-1 lg:text-right lg:text-xs')}
            />
            <span
              className={clsx(
                'text-[11px] text-content-400',
                wide && 'lg:col-start-4 lg:row-start-1 lg:text-right lg:text-xs',
              )}
            >
              {payout > 0 ? (
                <>
                  <span className="font-semibold tnum text-content-200">{payout}%</span>
                  <span className={clsx(wide && 'lg:hidden')}> payout</span>
                </>
              ) : (
                'No payout'
              )}
            </span>
          </span>
        </span>

        {wide ? (
          <AvailabilityLabel asset={asset} className="hidden lg:col-start-5 lg:row-start-1 lg:inline-flex lg:text-xs" />
        ) : null}
      </button>

      <FavouriteButton asset={asset} />
    </div>
  );
}
