import { describe, expect, it } from 'vitest';
import { STANDING_WORDS, amountTone, liveResult, liveTotal } from '../src/features/trade/liveResult';

/**
 * "If it ends now". The projection has to apply the server's settlement rule
 * exactly - UP wins above the entry, DOWN below, the same price settles by the
 * platform's draw policy (a refunded draw by default) - and use only the
 * amounts fixed on the trade. A projection that
 * disagreed with the settlement a second later would read as the platform
 * changing the result.
 */
const up = { direction: 'UP' as const, entryPrice: 100, stake: 100_00, profit: 82_00 };
const down = { ...up, direction: 'DOWN' as const };

describe('liveResult', () => {
  it('wins UP above the entry price with the profit fixed on the trade', () => {
    expect(liveResult(up, 100.01)).toEqual({ standing: 'winning', amount: 82_00 });
  });

  it('loses UP below the entry price, the whole stake', () => {
    expect(liveResult(up, 99.99)).toEqual({ standing: 'losing', amount: -100_00 });
  });

  it('mirrors for DOWN', () => {
    expect(liveResult(down, 99.99)).toEqual({ standing: 'winning', amount: 82_00 });
    expect(liveResult(down, 100.01)).toEqual({ standing: 'losing', amount: -100_00 });
  });

  it('calls the same price a draw that refunds, for either direction', () => {
    expect(liveResult(up, 100)).toEqual({ standing: 'draw', amount: 0 });
    expect(liveResult(down, 100)).toEqual({ standing: 'draw', amount: 0 });
  });

  it('follows the draw policy when an operator makes a tie a loss or a win', () => {
    expect(liveResult(up, 100, 'REFUND')).toEqual({ standing: 'draw', amount: 0 });
    expect(liveResult(up, 100, 'LOSS')).toEqual({ standing: 'losing', amount: -100_00 });
    expect(liveResult(down, 100, 'LOSS')).toEqual({ standing: 'losing', amount: -100_00 });
    expect(liveResult(up, 100, 'WIN')).toEqual({ standing: 'winning', amount: 82_00 });
    expect(liveResult(down, 100, 'WIN')).toEqual({ standing: 'winning', amount: 82_00 });
  });

  it('leaves prices off the entry to the direction, whatever the draw policy', () => {
    expect(liveResult(up, 100.01, 'LOSS')).toEqual({ standing: 'winning', amount: 82_00 });
    expect(liveResult(up, 99.99, 'WIN')).toEqual({ standing: 'losing', amount: -100_00 });
  });

  it('says nothing without a usable price', () => {
    expect(liveResult(up, null)).toBeNull();
    expect(liveResult(up, undefined)).toBeNull();
    expect(liveResult(up, Number.NaN)).toBeNull();
  });

  it('uses the profit written on the trade, not one recomputed from the payout', () => {
    // A trade whose server-rounded profit differs from stake x payout by a
    // paisa must show the server's figure.
    expect(liveResult({ ...up, profit: 41_51 }, 101)?.amount).toBe(41_51);
  });

  it('words each standing', () => {
    expect(STANDING_WORDS).toEqual({ winning: 'winning', losing: 'losing', draw: 'draw, refunded' });
  });
});

describe('liveTotal', () => {
  const now = 1_000_000;
  const trades = [
    { ...up, assetId: 'btc', expiryTimestamp: now + 30_000 },
    { ...down, assetId: 'eth', stake: 50_00, profit: 40_00, expiryTimestamp: now + 60_000 },
    { ...up, assetId: 'gold', expiryTimestamp: now + 5_000 },
  ];
  const prices: Record<string, number> = { btc: 101, eth: 101, gold: 100 };

  it('adds up every running trade that has a price', () => {
    // BTC UP above: +82; ETH DOWN above: -50; gold at entry: 0.
    expect(liveTotal(trades, (id) => prices[id], now)).toEqual({ amount: 32_00, running: 3, priced: 3 });
  });

  it('counts a trade without a price as running but leaves it out of the sum', () => {
    const total = liveTotal(trades, (id) => (id === 'eth' ? null : prices[id]), now);
    expect(total).toEqual({ amount: 82_00, running: 3, priced: 2 });
  });

  it('leaves out a trade past its expiry: its result is being settled on another price', () => {
    const total = liveTotal(trades, (id) => prices[id], now + 10_000);
    expect(total).toEqual({ amount: 32_00, running: 2, priced: 2 });
    expect(liveTotal(trades, (id) => prices[id], now + 60_000)).toEqual({ amount: 0, running: 0, priced: 0 });
  });

  it('applies the draw policy to trades sitting on their entry price', () => {
    // Gold is at its entry: -100 when a tie loses, +82 when it wins.
    expect(liveTotal(trades, (id) => prices[id], now, 'LOSS')).toEqual({ amount: -68_00, running: 3, priced: 3 });
    expect(liveTotal(trades, (id) => prices[id], now, 'WIN')).toEqual({ amount: 114_00, running: 3, priced: 3 });
  });

  it('is zero for no trades', () => {
    expect(liveTotal([], () => 1, now)).toEqual({ amount: 0, running: 0, priced: 0 });
  });
});

describe('amountTone', () => {
  it('colours gains, losses and nothing', () => {
    expect(amountTone(1)).toBe('up');
    expect(amountTone(-1)).toBe('down');
    expect(amountTone(0)).toBe('neutral');
  });
});
