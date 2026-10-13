import clsx from 'clsx';
import { ChevronDown, Star } from 'lucide-react';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { useFavouritesStore } from '../../stores/favouritesStore';
import { useSettingsStore, type TradeMode } from '../../stores/settingsStore';
import { formatPrice } from '../../utils/format';
import { AvailabilityLabel, Movement, useAssetPrice } from '../../components/AssetRow';
import type { Asset } from '../../types/api';

/**
 * The trade header (brief UX-05): what is being traded, and whether it can be.
 *
 * Symbol and name, the live price, the market status in words, the favourite
 * star, the switch to another asset, and the Beginner / Advanced toggle. The
 * asset part is the switch - tapping it opens the same picker the "+" tab
 * does - while the star and the toggle are their own buttons beside it, so a
 * tap meant for one can never land on the other.
 */
export function TradeAssetHeader({ asset }: { asset: Asset | null }) {
  const setPickerOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);
  const favourite = useFavouritesStore((s) => (asset ? s.favourites.includes(asset.id) : false));
  const toggleFavourite = useFavouritesStore((s) => s.toggle);

  return (
    <div className="flex w-full shrink-0 items-center gap-2 border-b border-surface-800 bg-surface-900 px-3 py-2">
      <button
        type="button"
        onClick={() => setPickerOpen(true)}
        aria-label={asset ? 'Change asset, now ' + asset.symbol : 'Choose an asset'}
        className="flex min-w-0 flex-1 items-center gap-3 rounded-lg py-0.5 text-left transition hover:bg-surface-850/60"
      >
        <span className="min-w-0 flex-1">
          <span className="flex items-center gap-1.5">
            <span className="whitespace-nowrap text-sm font-bold text-content-100">{asset?.symbol ?? 'Choose an asset'}</span>
            <ChevronDown className="h-4 w-4 shrink-0 text-content-400" />
          </span>
          {asset ? <span className="block truncate text-[11px] text-content-400">{asset.name}</span> : null}
        </span>

        {asset ? <HeaderPrice asset={asset} /> : null}
      </button>

      {asset ? (
        <button
          type="button"
          onClick={() => toggleFavourite(asset.id)}
          aria-pressed={favourite}
          aria-label={favourite ? 'Remove ' + asset.symbol + ' from favourites' : 'Add ' + asset.symbol + ' to favourites'}
          title={favourite ? 'Remove from favourites' : 'Add to favourites'}
          className="grid h-9 w-9 shrink-0 place-items-center rounded-lg text-content-400 transition hover:bg-surface-850 hover:text-content-100"
        >
          <Star className={clsx('h-4 w-4', favourite && 'fill-accent text-accent')} />
        </button>
      ) : null}

      <ModeToggle />
    </div>
  );
}

/**
 * Live price, 24h movement and the market status in words - the same words
 * and helpers the asset rows on Home and Markets use, so "Paused" means one
 * thing everywhere.
 */
function HeaderPrice({ asset }: { asset: Asset }) {
  const price = useAssetPrice(asset);
  return (
    <span className="shrink-0 text-right">
      <span className="flex items-baseline justify-end gap-1.5">
        <Movement percent={asset.change24hPercent} className="hidden sm:inline" />
        <span className="text-sm font-bold tnum text-content-100">
          {price !== null ? formatPrice(price, asset.pricePrecision) : '--'}
        </span>
      </span>
      <AvailabilityLabel asset={asset} className="justify-end font-semibold" />
    </span>
  );
}

/**
 * Beginner / Advanced.
 *
 * Two labelled segments rather than a switch: a switch says "on" or "off",
 * and neither mode is the other one switched off.
 */
export function ModeToggle({ className }: { className?: string }) {
  const mode = useSettingsStore((s) => s.tradeMode);
  const setMode = useSettingsStore((s) => s.setTradeMode);

  return (
    <div
      role="group"
      aria-label="Trading mode"
      className={clsx('flex shrink-0 rounded-lg bg-surface-850 p-0.5 text-[11px] font-semibold', className)}
    >
      {(['beginner', 'advanced'] as TradeMode[]).map((value) => (
        <button
          key={value}
          type="button"
          onClick={() => setMode(value)}
          aria-pressed={mode === value}
          className={clsx(
            'rounded-md px-1.5 py-1 capitalize transition sm:px-2',
            mode === value ? 'bg-surface-700 text-content-100' : 'text-content-400 hover:text-content-200',
          )}
        >
          {value}
        </button>
      ))}
    </div>
  );
}
