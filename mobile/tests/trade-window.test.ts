import { describe, expect, it } from 'vitest';
import {
  candlesForWindow,
  candleWindow,
  MAX_CANDLES,
  resampleTicks,
  tickRequestWindow,
  ticksReachEntry,
  ticksRetained,
  tickWindow,
  tradeMoments,
  TICK_RETENTION_MS,
} from '../src/features/trade/chart/tradeWindow';
import type { Trade } from '../src/types/api';

const MINUTE = 60_000;
const ENTRY = 1_790_000_000_384;

function trade(overrides: Partial<Trade> = {}): Trade {
  return {
    id: 't1',
    reference: 'R1',
    userId: 'u1',
    accountType: 'DEMO',
    assetId: 'a1',
    symbol: 'EUR/USD',
    direction: 'UP',
    stake: 50_000,
    currency: 'INR',
    payoutPercent: 84,
    profit: 42_000,
    potentialReturn: 92_000,
    entryPrice: 1.1,
    expiryPrice: 1.2,
    entryTimestamp: ENTRY,
    expiryTimestamp: ENTRY + 30_000,
    status: 'WON',
    result: 'WIN',
    settlementAmount: 42_000,
    ip: null,
    settledAt: new Date(ENTRY + 30_500).toISOString(),
    createdAt: new Date(ENTRY).toISOString(),
    ...overrides,
  };
}

describe('tradeMoments', () => {
  it('ends a settled trade at its expiry and expiry price', () => {
    const moments = tradeMoments(trade());

    expect(moments.exit).toBe(ENTRY + 30_000);
    expect(moments.exitPrice).toBe(1.2);
  });

  it('ends a trade sold early at the moment it was sold', () => {
    const soldAt = ENTRY + 12_345;
    const moments = tradeMoments(trade({ status: 'CLOSED_EARLY', settledAt: new Date(soldAt).toISOString() }));

    expect(moments.exit).toBe(soldAt);
    expect(moments.expiry).toBe(ENTRY + 30_000);
  });

  it('has no exit while running, while settling or when cancelled', () => {
    expect(tradeMoments(trade({ status: 'OPEN', expiryPrice: null })).exit).toBeNull();
    expect(tradeMoments(trade({ status: 'SETTLING', expiryPrice: null })).exit).toBeNull();
    expect(tradeMoments(trade({ status: 'CANCELLED' })).exit).toBeNull();
  });
});

describe('tickWindow', () => {
  it('pads a short trade by ten seconds and steps it by half a second', () => {
    const window = tickWindow(trade());

    expect(window.from).toBe(ENTRY - 10_000);
    expect(window.to).toBe(ENTRY + 40_000);
    expect(window.stepMs).toBe(500);
  });

  it('pads a five-minute trade by a quarter of its length, in whole seconds', () => {
    const window = tickWindow(trade({ expiryTimestamp: ENTRY + 5 * MINUTE }));

    expect(window.from).toBe(ENTRY - 75_000);
    expect(window.stepMs).toBe(1000);
  });
});

describe('tick retention', () => {
  it('uses ticks only while the whole window is still kept', () => {
    const now = ENTRY + 60_000;
    expect(ticksRetained(ENTRY - 10_000, now)).toBe(true);
    expect(ticksRetained(now - TICK_RETENTION_MS + 1_000, now)).toBe(false);
  });

  it('asks an older server for enough history, and never more than it keeps', () => {
    expect(tickRequestWindow(ENTRY - 10_000, ENTRY + 20_000)).toBe(35_000);
    expect(tickRequestWindow(ENTRY - 60 * MINUTE, ENTRY)).toBe(TICK_RETENTION_MS);
  });
});

describe('resampleTicks', () => {
  const window = { from: ENTRY - 2_000, to: ENTRY + 3_000, stepMs: 1_000 };
  const ticks = [
    { timestamp: ENTRY - 2_500, price: 1 },
    { timestamp: ENTRY - 400, price: 2 },
    { timestamp: ENTRY + 1_200, price: 3 },
    { timestamp: ENTRY + 1_900, price: 4 },
  ];

  it('lays a regular grid anchored on the entry, each point taking the last tick at or before it', () => {
    const points = resampleTicks(ticks, window, ENTRY, ENTRY + 10_000);

    expect(points.map((point) => point.time - ENTRY)).toEqual([-2_000, -1_000, 0, 1_000, 2_000, 3_000]);
    expect(points.map((point) => point.value)).toEqual([1, 1, 2, 2, 4, 4]);
  });

  it('leaves the future as whitespace, so a running trade still has room to its expiry', () => {
    const points = resampleTicks(ticks, window, ENTRY, ENTRY + 1_500);

    // The tick at +1.2s is after the +1s point, so that point still reads the entry's price.
    expect(points.map((point) => point.value)).toEqual([1, 1, 2, 2, undefined, undefined]);
  });

  it('leaves points before the first tick as whitespace rather than inventing a price', () => {
    const points = resampleTicks(ticks.slice(2), window, ENTRY, ENTRY + 10_000);

    expect(points.slice(0, 4).every((point) => point.value === undefined)).toBe(true);
    expect(points[4]!.value).toBe(4);
  });

  it('pins the recorded entry and exit prices onto their grid points', () => {
    const points = resampleTicks(ticks, window, ENTRY, ENTRY + 10_000, [
      { time: ENTRY, price: 9 },
      { time: ENTRY + 2_000, price: 8 },
      // Between two points: left alone.
      { time: ENTRY + 2_500, price: 7 },
    ]);

    expect(points.map((point) => point.value)).toEqual([1, 1, 9, 2, 8, 4]);
  });

  it('copes with ticks that arrive out of order', () => {
    const points = resampleTicks([...ticks].reverse(), window, ENTRY, ENTRY + 10_000);

    expect(points.map((point) => point.value)).toEqual([1, 1, 2, 2, 4, 4]);
  });

  it('knows whether the ticks reach back to the entry', () => {
    expect(ticksReachEntry(ticks, ENTRY)).toBe(true);
    expect(ticksReachEntry(ticks.slice(2), ENTRY)).toBe(false);
  });
});

describe('candles for an older trade', () => {
  const now = ENTRY + 3 * 60 * MINUTE;

  it('asks for twenty minutes of lead-in and five after the expiry', () => {
    const window = candleWindow(trade(), now);

    expect(window.from).toBe(Math.floor((ENTRY - 20 * MINUTE) / MINUTE) * MINUTE);
    expect(window.to).toBe(ENTRY + 30_000 + 5 * MINUTE);
    expect(window.limit).toBe(Math.ceil((window.to - window.from) / MINUTE) + 2);
  });

  it('never asks past now for a trade that has only just finished', () => {
    expect(candleWindow(trade(), ENTRY + 40_000).to).toBe(ENTRY + 40_000);
  });

  it('keeps the candles inside the window when they contain the trade', () => {
    const window = candleWindow(trade(), now);
    const start = Math.floor(ENTRY / MINUTE) * MINUTE;
    const candles = Array.from({ length: 60 }, (_, i) => ({
      time: start - 30 * MINUTE + i * MINUTE,
      open: 1,
      high: 1,
      low: 1,
      close: 1,
    }));

    const kept = candlesForWindow(candles, window, ENTRY)!;
    expect(kept[0]!.time).toBe(window.from);
    expect(kept.at(-1)!.time).toBeLessThanOrEqual(window.to);
  });

  it('refuses an answer that does not contain the trade - a server that ignored `to`', () => {
    const window = candleWindow(trade(), now);
    const latest = Array.from({ length: 30 }, (_, i) => ({
      time: now - 30 * MINUTE + i * MINUTE,
      open: 1,
      high: 1,
      low: 1,
      close: 1,
    }));

    expect(candlesForWindow(latest, window, ENTRY)).toBeNull();
  });

  it('never asks for more candles than the server sends in one answer', () => {
    const long = trade({ entryTimestamp: now - 3000 * MINUTE, expiryTimestamp: now - 2990 * MINUTE });

    expect(candleWindow(long, now).limit).toBeLessThanOrEqual(MAX_CANDLES);
  });
});
