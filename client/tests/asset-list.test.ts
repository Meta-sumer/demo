import { describe, expect, it } from 'vitest';
import {
  assetAvailability,
  assetsInFilter,
  bestPayout,
  catalogueOrder,
  categoriesOf,
  formatNextOpen,
  isAssetSort,
  matchesSearch,
  openCount,
  sortAssets,
} from '../src/components/assetList';
import type { Asset, FeedStatus } from '../src/types/api';

/**
 * The market list: which instruments a chip holds, how the list sorts, how
 * many are open, and when a closed one reopens. The Markets page and the
 * selector on the trade screen share all of it.
 */
function asset(id: string, extra: Partial<Asset> & { payout?: number[]; status?: FeedStatus } = {}): Asset {
  const { payout = [80], status = 'HEALTHY', ...rest } = extra;
  return {
    id,
    symbol: id.toUpperCase() + '/USD',
    name: id + ' name',
    category: 'CRYPTO',
    active: true,
    pricePrecision: 2,
    minStake: 100,
    maxStake: 100_000,
    supportedExpiries: [60],
    marketOpen: true,
    displayOrder: 0,
    payouts: payout.map((payoutPercent, index) => ({
      id: id + index,
      assetId: id,
      expirySeconds: 60 * (index + 1),
      payoutPercent,
      minStake: null,
      maxStake: null,
      version: 1,
    })),
    feed: {
      assetId: id,
      symbol: id,
      status,
      lastPrice: 1,
      providerTimestamp: null,
      serverReceivedAt: null,
      feedLatencyMs: null,
      totalLatencyMs: null,
      ageMs: null,
      provider: 'sim',
      updatedAt: 0,
    },
    sentiment: null,
    change24hPercent: null,
    ...rest,
  };
}

const btc = asset('btc', { displayOrder: 0, payout: [82, 87] });
const eth = asset('eth', { displayOrder: 1, payout: [80, 86] });
const eur = asset('eur', { displayOrder: 2, category: 'FOREX', payout: [78, 84], status: 'STALE' });
const gbp = asset('gbp', { displayOrder: 3, category: 'FOREX', payout: [87], marketOpen: false });
const xau = asset('xau', { displayOrder: 4, category: 'COMMODITY', payout: [] });
const all = catalogueOrder([xau, gbp, eur, eth, btc]);

describe('availability and payout', () => {
  it('says open, paused or closed the way the server decides', () => {
    expect(assetAvailability(btc)).toBe('open');
    expect(assetAvailability(eur)).toBe('paused');
    expect(assetAvailability(gbp)).toBe('closed');
  });

  it('counts only markets that take trades now as open', () => {
    expect(openCount(all)).toBe(3); // btc, eth, xau - not paused eur, not closed gbp
    expect(openCount([])).toBe(0);
  });

  it('finds the best payout, or zero without one', () => {
    expect(bestPayout(btc)).toBe(87);
    expect(bestPayout(xau)).toBe(0);
  });
});

describe('filters and search', () => {
  const context = { favourites: ['eth', 'gbp'], recent: ['gbp', 'nope', 'btc'] };

  it('lists categories in catalogue order', () => {
    expect(categoriesOf(all)).toEqual(['CRYPTO', 'FOREX', 'COMMODITY']);
  });

  it('fills each chip', () => {
    const ids = (list: Asset[]) => list.map((a) => a.id);
    expect(ids(assetsInFilter('all', all, context))).toEqual(['btc', 'eth', 'eur', 'gbp', 'xau']);
    expect(ids(assetsInFilter('favourites', all, context))).toEqual(['eth', 'gbp']);
    // Most recent first, and an asset that no longer exists is skipped.
    expect(ids(assetsInFilter('recent', all, context))).toEqual(['gbp', 'btc']);
    expect(ids(assetsInFilter('category:FOREX', all, context))).toEqual(['eur', 'gbp']);
  });

  it('matches symbol or name, ignoring case; an empty search matches all', () => {
    expect(matchesSearch(btc, 'btc')).toBe(true);
    expect(matchesSearch(btc, 'NAME')).toBe(true);
    expect(matchesSearch(btc, 'eth')).toBe(false);
    expect(matchesSearch(btc, '   ')).toBe(true);
  });
});

describe('sortAssets', () => {
  it('keeps the chip order by default', () => {
    const recentOrder = [gbp, btc];
    expect(sortAssets(recentOrder, 'default').map((a) => a.id)).toEqual(['gbp', 'btc']);
  });

  it('puts the highest payout first, ties in catalogue order', () => {
    // btc 87 and gbp 87 tie: btc comes first in the catalogue.
    expect(sortAssets(all, 'payout').map((a) => a.id)).toEqual(['btc', 'gbp', 'eth', 'eur', 'xau']);
  });

  it('sorts by symbol', () => {
    expect(sortAssets([xau, eth, btc], 'name').map((a) => a.id)).toEqual(['btc', 'eth', 'xau']);
  });

  it('never reorders the list it was given', () => {
    const input = [eth, btc];
    sortAssets(input, 'name');
    expect(input.map((a) => a.id)).toEqual(['eth', 'btc']);
  });

  it('recognises only the offered sorts', () => {
    expect(isAssetSort('payout')).toBe(true);
    expect(isAssetSort('volume')).toBe(false);
    expect(isAssetSort(null)).toBe(false);
  });
});

describe('formatNextOpen', () => {
  // Wednesday 30 September 2026, 10:00 UTC.
  const now = Date.parse('2026-09-30T10:00:00Z');

  it('gives only the time for later today', () => {
    expect(formatNextOpen(Date.parse('2026-09-30T14:30:00Z'), now, 'UTC')).toBe('Opens 14:30');
  });

  it('gives the weekday within the coming week', () => {
    expect(formatNextOpen(Date.parse('2026-10-01T09:00:00Z'), now, 'UTC')).toBe('Opens Thu 09:00');
    expect(formatNextOpen(Date.parse('2026-10-05T00:00:00Z'), now, 'UTC')).toBe('Opens Mon 00:00');
  });

  it('gives the date beyond a week', () => {
    expect(formatNextOpen(Date.parse('2026-10-12T09:00:00Z'), now, 'UTC')).toMatch(/^Opens 12 Oct 09:00$/);
  });

  it('writes the time and the day in the chosen zone', () => {
    // 20:00 UTC is 01:30 the next day in Kolkata.
    expect(formatNextOpen(Date.parse('2026-09-30T20:00:00Z'), now, 'Asia/Kolkata')).toBe('Opens Thu 01:30');
  });

  it('says nothing when the time is unknown or already past', () => {
    expect(formatNextOpen(undefined, now, 'UTC')).toBeNull();
    expect(formatNextOpen(null, now, 'UTC')).toBeNull();
    expect(formatNextOpen(now - 1, now, 'UTC')).toBeNull();
    expect(formatNextOpen(Number.NaN, now, 'UTC')).toBeNull();
  });
});
