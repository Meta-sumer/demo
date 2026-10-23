import { describe, expect, it } from 'vitest';
import {
  isPaused,
  isRequote,
  quoteBlockedReason,
  requoteFrom,
  shownPayout,
  shownTerms,
  sidePayout,
  unpricedReason,
} from '../src/features/trade/livePayout';
import type { EntryQuote } from '../src/types/api';

function quote(up: number | null, down: number | null): EntryQuote {
  const side = (direction: 'UP' | 'DOWN', payout: number | null) => ({
    offerId: payout === null ? null : 'offer-' + direction,
    direction,
    status: payout === null ? ('blocked' as const) : ('ok' as const),
    available: payout !== null,
    payoutPercent: payout,
    profit: payout === null ? null : payout * 100,
    potentialReturn: payout === null ? null : 10000 + payout * 100,
    referencePrice: payout === null ? null : 104250.01,
    code: payout === null ? 'EXPOSURE_LIMIT' : null,
    reason: payout === null ? 'exposure limit reached on UP side' : null,
  });
  const available = [up, down].filter((p): p is number => p !== null);
  return {
    assetId: 'a1',
    symbol: 'BTC/USD',
    expirySeconds: 60,
    amount: 10000,
    accountType: 'LIVE',
    mode: 'DYNAMIC',
    ruleVersion: 'rules-2026.10.1+rc3+p2',
    modelVersion: 1,
    riskConfigVersion: 3,
    payoutConfigVersion: 2,
    issuedAt: 1000,
    validUntil: 4000,
    validForMs: 3000,
    maxSlippageBps: 2,
    payoutPercent: available.length ? Math.min(...available) : null,
    up: side('UP', up),
    down: side('DOWN', down),
  };
}

describe('live payout', () => {
  it('uses the catalogue figure until the first offer lands', () => {
    expect(sidePayout(null, 'UP', 84)).toBe(84);
    expect(shownPayout(null, 84)).toBe(84);
    expect(shownTerms(null, 'UP', 84)).toEqual({ offerId: undefined, displayedPayoutPercent: 84 });
  });

  it('shows each side its own offer and one figure as the lower', () => {
    const q = quote(81, 85.5);
    expect(sidePayout(q, 'UP', 90)).toBe(81);
    expect(sidePayout(q, 'DOWN', 90)).toBe(85.5);
    expect(shownPayout(q, 90)).toBe(81);
    expect(shownTerms(q, 'DOWN', 90)).toEqual({ offerId: 'offer-DOWN', displayedPayoutPercent: 85.5 });
  });

  it('blocks a side that is not offered, and the ticket when neither is', () => {
    const one = quote(null, 85);
    expect(sidePayout(one, 'UP', 90)).toBeNull();
    expect(quoteBlockedReason(one)).toBeNull();
    expect(shownTerms(one, 'UP', 90)).toEqual({ offerId: undefined, displayedPayoutPercent: undefined });
    expect(quoteBlockedReason(quote(null, null))).toBe('exposure limit reached on UP side');
    expect(shownPayout(quote(null, null), 90)).toBeNull();
  });

  it('recognises a re-quote and reads the fresh offers from it', () => {
    expect(isRequote('OFFER_EXPIRED')).toBe(true);
    expect(isRequote('OFFER_INVALID')).toBe(true);
    expect(isRequote('PAYOUT_CHANGED')).toBe(true);
    expect(isRequote('PRICE_MOVED')).toBe(false);
    expect(isRequote(undefined)).toBe(false);
    const fresh = quote(80, 82);
    expect(requoteFrom({ quote: fresh, offerId: 'x' })).toBe(fresh);
    expect(requoteFrom({ serverPrice: 1 })).toBeNull();
    expect(requoteFrom(null)).toBeNull();
  });

  it('reads the fresh quote a halt or a stale feed refusal carries', () => {
    expect(isPaused('FEED_UNHEALTHY')).toBe(true);
    expect(isPaused('TRADING_HALTED')).toBe(true);
    expect(isPaused('NO_PRICE_AVAILABLE')).toBe(true);
    expect(isPaused('EXPOSURE_LIMIT_EXCEEDED')).toBe(true);
    expect(isPaused('OFFER_EXPIRED')).toBe(false);
    expect(isPaused('INSUFFICIENT_BALANCE')).toBe(false);
    expect(isPaused(null)).toBe(false);
    // The fresh quote says why nothing is offered; the ticket shows it.
    const paused = quote(null, null);
    const details = { reason: 'STALE_MARKET_DATA', offerId: 'offer-UP', quote: paused };
    expect(requoteFrom(details)).toBe(paused);
    expect(quoteBlockedReason(requoteFrom(details))).toBe('exposure limit reached on UP side');
    expect(requoteFrom({ reason: 'STALE_MARKET_DATA' })).toBeNull();
  });

  it('names the reason a duration is not offered', () => {
    const asset = {
      pricing: {
        mode: 'DYNAMIC' as const,
        ruleVersion: 'r',
        modelVersion: 1,
        computedAt: 0,
        halted: false,
        unavailable: [{ expirySeconds: 30, reason: 'NOT_PRICED', message: 'EUR/USD is not priced' }],
      },
    };
    expect(unpricedReason(asset, 30)).toBe('EUR/USD is not priced');
    expect(unpricedReason(asset, 60)).toBeNull();
    expect(unpricedReason({}, 30)).toBeNull();
  });
});
