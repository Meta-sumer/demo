import { formatHourMinute } from '../utils/time';
import { dayKey, daysBetween, weekdayOf } from '../utils/dayGroups';
import type { Asset } from '../types/api';

/**
 * The instrument list's rules, without React (brief UX-02/UX-03, reference
 * record 03): which instruments a chip holds, how a search matches, how the
 * list sorts, and what a closed market says about reopening. Kept pure so the
 * Markets page and the selector inside Trade - which share one list - can be
 * tested once.
 */

export type AssetAvailability = 'open' | 'paused' | 'closed';

/**
 * Whether the platform offers the instrument at all. An asset the pricing
 * engine cannot price (launch scope: only BTC/USD and ETH/USD are priced)
 * is not shown anywhere; a server that predates the flag offers everything.
 */
export function isOfferable(asset: Pick<Asset, 'pricing'>): boolean {
  return asset.pricing?.priced !== false;
}

export function offerableAssets<T extends Pick<Asset, 'pricing'>>(assets: T[]): T[] {
  return assets.filter(isOfferable);
}

/**
 * Whether the instrument takes new trades, in words.
 *
 * "Market closed" is the schedule; "Paused" is the platform refusing because
 * the price feed is stale or offline - the same rule the server applies before
 * it accepts an order.
 */
export function assetAvailability(asset: Pick<Asset, 'marketOpen' | 'feed'>): AssetAvailability {
  if (!asset.marketOpen) return 'closed';
  if (asset.feed.status === 'STALE' || asset.feed.status === 'OFFLINE') return 'paused';
  return 'open';
}

/** The best payout on offer across the instrument's durations, in percent. */
export function bestPayout(asset: Pick<Asset, 'payouts'>): number {
  return asset.payouts.reduce((best, p) => Math.max(best, p.payoutPercent), 0);
}

/**
 * The durations the engine can price right now, shortest first, each with its
 * indicative payout. A duration it cannot price is not in `payouts` at all;
 * a row without a positive figure is left out too, rather than shown as 0%.
 */
export function offeredPayouts(
  asset: Pick<Asset, 'payouts'>,
): Array<{ expirySeconds: number; payoutPercent: number }> {
  return asset.payouts
    .filter((p) => Number.isFinite(p.payoutPercent) && p.payoutPercent > 0)
    .map((p) => ({ expirySeconds: p.expirySeconds, payoutPercent: p.payoutPercent }))
    .sort((a, b) => a.expirySeconds - b.expirySeconds);
}

/** Lowest and highest payout on offer, in percent; null when nothing can be priced. */
export function payoutRange(asset: Pick<Asset, 'payouts'>): { min: number; max: number } | null {
  const offered = offeredPayouts(asset);
  if (offered.length === 0) return null;
  const figures = offered.map((p) => p.payoutPercent);
  return { min: Math.min(...figures), max: Math.max(...figures) };
}

/* ------------------------------------------------------------------ filters */

export type AssetFilter = 'all' | 'favourites' | 'recent' | `category:${string}`;

/** Categories in the order the catalogue first mentions them. */
export function categoriesOf(assets: ReadonlyArray<Pick<Asset, 'category'>>): string[] {
  const seen: string[] = [];
  for (const asset of assets) if (asset.category && !seen.includes(asset.category)) seen.push(asset.category);
  return seen;
}

/** Catalogue order: the order operations set for the list. */
export function catalogueOrder<T extends Pick<Asset, 'displayOrder'>>(assets: readonly T[]): T[] {
  return [...assets].sort((a, b) => a.displayOrder - b.displayOrder);
}

/**
 * The instruments a chip holds, in that chip's natural order.
 *
 * `catalogue` is the list already in catalogue order. Recent is the exception:
 * most recent first - the order they were used in, not catalogue order.
 */
export function assetsInFilter<T extends Pick<Asset, 'id' | 'category'>>(
  filter: AssetFilter,
  catalogue: readonly T[],
  context: { favourites: readonly string[]; recent: readonly string[] },
): T[] {
  if (filter === 'favourites') return catalogue.filter((a) => context.favourites.includes(a.id));
  if (filter === 'recent') {
    const byId = new Map(catalogue.map((a) => [a.id, a]));
    return context.recent.map((id) => byId.get(id)).filter((a): a is T => !!a);
  }
  if (filter.startsWith('category:')) {
    const category = filter.slice('category:'.length);
    return catalogue.filter((a) => a.category === category);
  }
  return [...catalogue];
}

/** Symbol or name contains the term, ignoring case. An empty term matches everything. */
export function matchesSearch(asset: Pick<Asset, 'symbol' | 'name'>, term: string): boolean {
  const needle = term.trim().toLowerCase();
  if (!needle) return true;
  return asset.symbol.toLowerCase().includes(needle) || asset.name.toLowerCase().includes(needle);
}

/** How many of `assets` take trades right now. Paused does not count: the platform refuses those. */
export function openCount(assets: ReadonlyArray<Pick<Asset, 'marketOpen' | 'feed'>>): number {
  return assets.reduce((count, asset) => count + (assetAvailability(asset) === 'open' ? 1 : 0), 0);
}

/* ------------------------------------------------------------------ sorting */

export type AssetSort = 'default' | 'payout' | 'name';

export const ASSET_SORTS: ReadonlyArray<{ value: AssetSort; label: string }> = [
  { value: 'default', label: 'Default' },
  { value: 'payout', label: 'Highest payout' },
  { value: 'name', label: 'Name' },
];

export function isAssetSort(value: unknown): value is AssetSort {
  return value === 'default' || value === 'payout' || value === 'name';
}

/**
 * A chip's list, sorted.
 *
 * Default keeps the chip's own order (catalogue, or recency for Recent).
 * Ties fall back to catalogue order so equal payouts do not reshuffle from one
 * refresh to the next - a row that jumps while the finger is on its way to it
 * is a mis-tap.
 */
export function sortAssets<T extends Pick<Asset, 'symbol' | 'displayOrder' | 'payouts'>>(
  assets: readonly T[],
  sort: AssetSort,
): T[] {
  const list = [...assets];
  if (sort === 'payout') return list.sort((a, b) => bestPayout(b) - bestPayout(a) || a.displayOrder - b.displayOrder);
  if (sort === 'name') {
    return list.sort(
      (a, b) =>
        a.symbol.localeCompare(b.symbol, 'en', { numeric: true, sensitivity: 'base' }) ||
        a.displayOrder - b.displayOrder,
    );
  }
  return list;
}

/* ---------------------------------------------------------------- reopening */

/**
 * When a closed market opens again, for its row.
 *
 * "Opens 14:30" today, "Opens Mon 09:00" within the coming week and
 * "Opens 12 Oct 09:00" beyond it, written in the trader's chosen zone like
 * every other clock time. Null when the time is unknown - an older server does
 * not send it - or already past: a stale schedule must not promise an opening
 * that has come and gone.
 */
export function formatNextOpen(nextOpenAt: number | null | undefined, now: number, timeZone: string): string | null {
  if (nextOpenAt === null || nextOpenAt === undefined || !Number.isFinite(nextOpenAt) || nextOpenAt <= now) {
    return null;
  }
  const time = formatHourMinute(nextOpenAt, timeZone);
  const target = dayKey(nextOpenAt, timeZone);
  const days = daysBetween(dayKey(now, timeZone), target);
  if (days <= 0) return 'Opens ' + time;
  if (days < 7) return 'Opens ' + weekdayOf(target) + ' ' + time;
  const date = new Intl.DateTimeFormat('en-GB', { timeZone, day: 'numeric', month: 'short' }).format(
    new Date(nextOpenAt),
  );
  return 'Opens ' + date + ' ' + time;
}
