import { describe, expect, it } from 'vitest';
import { isVoided, statusLabel, tradeLabel, VOID_LABEL, voidReason } from '../src/utils/tradeStatus';
import { isOfferable, offerableAssets } from '../src/components/assetList';
import type { Asset, Trade } from '../src/types/api';

/**
 * A trade voided for want of a price reads as a void, not as a tie, and old
 * trades keep reading as before; the apps never offer an instrument the
 * platform cannot price.
 */
describe('void display', () => {
  const base = { status: 'DRAW' } as Pick<Trade, 'status' | 'voided' | 'expiryPriceSource' | 'voidReason'>;

  it('shows a voided trade as a void, with the reason', () => {
    const t = { ...base, voided: true, voidReason: 'No valid market price at expiry; stake returned' };
    expect(isVoided(t)).toBe(true);
    expect(tradeLabel(t)).toBe(VOID_LABEL);
    expect(voidReason(t)).toBe('No valid market price at expiry; stake returned');
  });

  it('recognises a void from the settlement rule alone', () => {
    const t = { ...base, expiryPriceSource: 'NO_PRICE_VOID_REFUND' };
    expect(tradeLabel(t)).toBe(VOID_LABEL);
    expect(voidReason(t)).toMatch(/stake was returned/);
  });

  it('keeps a real tie and older trades as a draw', () => {
    expect(tradeLabel({ ...base, expiryPriceSource: 'TICK_AT_OR_BEFORE_EXPIRY' })).toBe(statusLabel('DRAW'));
    expect(tradeLabel(base)).toBe(statusLabel('DRAW'));
    expect(voidReason(base)).toBeNull();
    // Only a DRAW can be a void.
    expect(isVoided({ status: 'WON', voided: true })).toBe(false);
  });
});

describe('launch scope', () => {
  const priced = { pricing: { priced: true } } as unknown as Pick<Asset, 'pricing'>;
  const unpriced = { pricing: { priced: false, notPricedReason: 'not priced' } } as unknown as Pick<Asset, 'pricing'>;
  const oldServer = {} as unknown as Pick<Asset, 'pricing'>;

  it('offers only priced assets', () => {
    expect(isOfferable(priced)).toBe(true);
    expect(isOfferable(unpriced)).toBe(false);
    expect(isOfferable(oldServer)).toBe(true);
    expect(offerableAssets([priced, unpriced, oldServer])).toEqual([priced, oldServer]);
  });
});

describe('tie rule frozen on the trade', () => {
  const trade = { direction: 'UP' as const, entryPrice: 100, stake: 100_00, profit: 82_00 };

  it('projects a tie with the rule frozen on the trade, the current rule only for older trades', async () => {
    const { liveResult } = await import('../src/features/trade/liveResult');
    expect(liveResult({ ...trade, drawPolicy: 'REFUND' }, 100, 'LOSS')).toEqual({ standing: 'draw', amount: 0 });
    expect(liveResult({ ...trade, drawPolicy: 'LOSS' }, 100, 'REFUND')).toEqual({ standing: 'losing', amount: -100_00 });
    expect(liveResult(trade, 100, 'WIN')).toEqual({ standing: 'winning', amount: 82_00 });
  });
});
