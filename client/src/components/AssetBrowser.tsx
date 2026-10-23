import { useMemo, useState, type ReactNode, type RefObject } from 'react';
import clsx from 'clsx';
import { Search, X } from 'lucide-react';
import { useFavouritesStore } from '../stores/favouritesStore';
import { AssetRow, AssetTableHeader } from './AssetRow';
import { MarketCardGrid, MarketCardSkeletons } from './MarketCard';
import { EmptyState, Skeleton } from './ui';
import {
  ASSET_SORTS,
  assetsInFilter,
  catalogueOrder,
  categoriesOf,
  isAssetSort,
  matchesSearch,
  openCount,
  sortAssets,
  type AssetFilter,
  type AssetSort,
} from './assetList';
import type { Asset } from '../types/api';

/**
 * Search, filters and the instrument list.
 *
 * The Markets page and the selector inside Trade are the same task - find an
 * instrument and pick it - so they share this body and only their frame
 * differs (brief UX-02/UX-03). Filtering is local and instant: the whole
 * catalogue is already in the `['assets']` cache.
 *
 * People look either for a name or for the best payout (reference record 03),
 * so the list sorts by either. Each chip says how many instruments it holds -
 * of those matching the search, when there is one, so the chips also say
 * where the matches are - and a line says how many of the listed ones take
 * trades right now (Binomo's "147 in total · 85 active").
 */
const SORT_KEY = 'bt.marketSort';

/** The sort is a per-device preference; a blocked storage just means Default. */
function readSort(): AssetSort {
  try {
    const stored = localStorage.getItem(SORT_KEY);
    return isAssetSort(stored) ? stored : 'default';
  } catch {
    return 'default';
  }
}

interface Props {
  assets: Asset[];
  loading?: boolean;
  onPick: (asset: Asset) => void;
  selectedId?: string | null;
  searchRef?: RefObject<HTMLInputElement>;
  /** Classes for the scrolling list, so a sheet can bound its height. */
  listClassName?: string;
  /** Classes for the search + chips block. */
  headerClassName?: string;
  className?: string;
  /**
   * The list has the page's width (Markets): from lg, search and filters share
   * one line and the rows become a table with column titles.
   */
  wide?: boolean;
  /**
   * Show the instruments as cards - price line, payout by duration, a Trade
   * button - instead of rows. For a page with only a few of them, where a
   * table would be two rows on an empty page. The search, chips and sort work
   * the same; the header and each card are then panels of their own, so the
   * host does not wrap the browser in one.
   */
  cards?: boolean;
}

export function AssetBrowser({
  assets,
  loading,
  onPick,
  selectedId,
  searchRef,
  listClassName,
  headerClassName,
  className,
  wide = false,
  cards = false,
}: Props) {
  const favourites = useFavouritesStore((s) => s.favourites);
  const recent = useFavouritesStore((s) => s.recent);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<AssetFilter>('all');
  const [sort, setSortState] = useState<AssetSort>(readSort);

  function setSort(next: AssetSort) {
    setSortState(next);
    try {
      localStorage.setItem(SORT_KEY, next);
    } catch {
      /* private browsing - the choice lasts until the list closes */
    }
  }

  const catalogue = useMemo(() => catalogueOrder(assets), [assets]);
  const categories = useMemo(() => categoriesOf(assets), [assets]);
  const context = useMemo(() => ({ favourites, recent }), [favourites, recent]);
  const term = search.trim();

  const chips = useMemo(() => {
    const list: Array<{ value: AssetFilter; label: string }> = [
      { value: 'all', label: 'All' },
      { value: 'favourites', label: 'Favourites' },
      { value: 'recent', label: 'Recent' },
      ...categories.map((category) => ({
        value: `category:${category}` as AssetFilter,
        label: categoryLabel(category),
      })),
    ];
    return list.map((chip) => ({
      ...chip,
      count: assetsInFilter(chip.value, catalogue, context).filter((asset) => matchesSearch(asset, term)).length,
    }));
  }, [categories, catalogue, context, term]);

  const visible = useMemo(
    () =>
      sortAssets(
        assetsInFilter(filter, catalogue, context).filter((asset) => matchesSearch(asset, term)),
        sort,
      ),
    [filter, catalogue, context, term, sort],
  );
  // A search that finds nothing in a narrow filter may still find it overall;
  // offer that instead of a dead end.
  const elsewhere = term && visible.length === 0 && filter !== 'all' ? chips[0]!.count : 0;
  const openNow = openCount(visible);
  // Cards sit on the page, so a message in their place needs a panel of its own.
  const framed = (node: ReactNode) => (cards ? <div className="panel">{node}</div> : node);
  const cardColumns = catalogue.length > 2 ? 'sm:grid-cols-2 xl:grid-cols-3' : 'sm:grid-cols-2';

  return (
    <div className={clsx('flex min-h-0 flex-col', className)}>
      <div className={clsx('space-y-2.5', headerClassName)}>
        <div className={clsx('space-y-2.5', wide && 'lg:flex lg:items-center lg:gap-3 lg:space-y-0')}>
          <div className={clsx('relative', wide && 'lg:w-80 lg:shrink-0')}>
            <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-content-400" />
            <input
              ref={searchRef}
              type="text"
              inputMode="search"
              enterKeyHint="search"
              autoComplete="off"
              className="field pl-9 pr-9"
              placeholder="Search markets"
              aria-label="Search markets by symbol or name"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
            {search ? (
              <button
                type="button"
                onClick={() => setSearch('')}
                className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded-md p-1.5 text-content-400 hover:text-content-100"
                aria-label="Clear search"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            ) : null}
          </div>

          <div
            className={clsx(
              '-mx-1 flex gap-1.5 overflow-x-auto px-1 pb-0.5',
              wide && 'lg:mx-0 lg:min-w-0 lg:flex-1 lg:pb-0',
            )}
            role="tablist"
            aria-label="Filter markets"
          >
            {chips.map((chip) => (
              <button
                key={chip.value}
                type="button"
                role="tab"
                aria-selected={filter === chip.value}
                onClick={() => setFilter(chip.value)}
                className={clsx(
                  'shrink-0 rounded-full border px-3 py-1 text-xs font-semibold transition',
                  filter === chip.value
                    ? 'border-transparent bg-accent text-surface-950'
                    : 'border-surface-700 bg-surface-850 text-content-300 hover:text-content-100',
                )}
              >
                {chip.label}
                <span className="ml-1 tnum opacity-70">{chip.count}</span>
              </button>
            ))}
          </div>
        </div>

        {!loading && assets.length > 0 ? (
          <div className="flex items-center justify-between gap-3 text-[11px] text-content-400">
            <p className="min-w-0 truncate">
              {visible.length > 0 ? (
                <>
                  <span className="font-semibold tnum text-content-200">{openNow}</span> of{' '}
                  <span className="tnum">{visible.length}</span> open now
                </>
              ) : null}
            </p>
            <label className="flex shrink-0 items-center gap-1.5">
              Sort
              <select
                value={sort}
                onChange={(event) => setSort(isAssetSort(event.target.value) ? event.target.value : 'default')}
                className="rounded-md border border-surface-700 bg-surface-850 py-1 pl-2 pr-1 text-[11px] font-semibold text-content-100 outline-none focus:border-accent"
              >
                {ASSET_SORTS.map((option) => (
                  <option key={option.value} value={option.value}>
                    {option.label}
                  </option>
                ))}
              </select>
            </label>
          </div>
        ) : null}
      </div>

      <div className={clsx('min-h-0', listClassName)}>
        {loading && cards ? (
          <MarketCardSkeletons className={cardColumns} />
        ) : loading ? (
          <div className="space-y-2 p-3">
            {Array.from({ length: 6 }).map((_, index) => (
              <Skeleton key={index} className="h-12 w-full" />
            ))}
          </div>
        ) : visible.length > 0 && cards ? (
          <MarketCardGrid assets={visible} onTrade={onPick} detailed className={cardColumns} />
        ) : visible.length > 0 ? (
          <>
            {wide ? <AssetTableHeader className="border-b border-surface-800" /> : null}
            <ul className="divide-y divide-surface-800/70">
              {visible.map((asset) => (
                <li key={asset.id}>
                  <AssetRow asset={asset} onSelect={onPick} selected={asset.id === selectedId} wide={wide} />
                </li>
              ))}
            </ul>
          </>
        ) : elsewhere > 0 ? (
          framed(
            <div className="px-6 py-10 text-center">
              <p className="text-sm text-content-200">Nothing here matches “{search.trim()}”.</p>
              <button type="button" className="btn-ghost mt-3" onClick={() => setFilter('all')}>
                Show {elsewhere} {elsewhere === 1 ? 'match' : 'matches'} in all markets
              </button>
            </div>,
          )
        ) : term ? (
          framed(
            <EmptyState
              title="No market matches that search"
              description="Try the symbol, e.g. BTC, or part of the name."
            />,
          )
        ) : filter === 'favourites' ? (
          framed(
            <EmptyState
              title="No favourites yet"
              description="Tap the star on any market to keep it here, on Home and in the trade screen."
            />,
          )
        ) : filter === 'recent' ? (
          framed(<EmptyState title="Nothing recent" description="Markets you open to trade appear here." />)
        ) : (
          framed(<EmptyState title="No markets available" description="Please check again shortly." />)
        )}
      </div>
    </div>
  );
}

/** "crypto" -> "Crypto", "FOREX" -> "Forex". Categories come from the server as codes. */
export function categoryLabel(category: string): string {
  const lower = category.toLowerCase().replace(/_/g, ' ');
  return lower.charAt(0).toUpperCase() + lower.slice(1);
}
