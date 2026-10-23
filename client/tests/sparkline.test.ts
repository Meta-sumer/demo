import { describe, expect, it } from 'vitest';
import {
  sparklinePaths,
  sparklineSeries,
  sparklineSpanLabel,
  sparklineTrend,
} from '../src/components/sparkline';
import type { Candle } from '../src/types/api';

/**
 * The market cards' price line: the server's one-minute closes with the live
 * price folded onto the end, and nothing drawn that the server did not send.
 */
const MIN = 60_000;
const T0 = Date.UTC(2026, 9, 23, 10, 0);

function candle(minute: number, close: number): Candle {
  return { time: T0 + minute * MIN, open: close, high: close, low: close, close };
}

const candles = [candle(0, 100), candle(1, 101), candle(2, 99)];

describe('sparklineSeries', () => {
  it('is the closes, oldest first, whatever order they arrive in', () => {
    const series = sparklineSeries([candles[2]!, candles[0]!, candles[1]!]);
    expect(series.values).toEqual([100, 101, 99]);
    expect(series.from).toBe(T0);
    expect(series.to).toBe(T0 + 3 * MIN); // the end of the last candle
  });

  it('lets a tick inside the last candle replace its close', () => {
    expect(sparklineSeries(candles, { price: 98.5, timestamp: T0 + 2 * MIN + 30_000 }).values).toEqual([100, 101, 98.5]);
  });

  it('adds a later tick as the next point', () => {
    const series = sparklineSeries(candles, { price: 102, timestamp: T0 + 3 * MIN + 5_000 });
    expect(series.values).toEqual([100, 101, 99, 102]);
    expect(series.to).toBe(T0 + 3 * MIN + 5_000);
  });

  it('ignores a tick older than the last candle', () => {
    expect(sparklineSeries(candles, { price: 1, timestamp: T0 + MIN }).values).toEqual([100, 101, 99]);
  });

  it('drops figures that are not numbers rather than drawing them', () => {
    expect(sparklineSeries([candle(0, 100), candle(1, Number.NaN), candle(2, 99)]).values).toEqual([100, 99]);
    expect(sparklineSeries(candles, { price: Number.NaN, timestamp: T0 + 9 * MIN }).values).toEqual([100, 101, 99]);
  });

  it('is empty without history, or the one live price', () => {
    expect(sparklineSeries([])).toEqual({ values: [], from: null, to: null });
    expect(sparklineSeries([], { price: 5, timestamp: T0 }).values).toEqual([5]);
  });
});

describe('sparklineSpanLabel', () => {
  it('names the time the line covers', () => {
    expect(sparklineSpanLabel({ from: T0, to: T0 + 14 * MIN })).toBe('Last 14 min');
    expect(sparklineSpanLabel({ from: T0, to: T0 + 60 * MIN })).toBe('Last hour');
  });

  it('says nothing for an empty or single-point line', () => {
    expect(sparklineSpanLabel({ from: null, to: null })).toBeNull();
    expect(sparklineSpanLabel({ from: T0, to: T0 })).toBeNull();
  });
});

describe('sparklinePaths', () => {
  it('needs two points to draw a line', () => {
    expect(sparklinePaths([], 100, 40)).toBeNull();
    expect(sparklinePaths([5], 100, 40)).toBeNull();
  });

  it('spans the box: highest price at the top inset, lowest at the bottom inset', () => {
    const paths = sparklinePaths([10, 20, 15], 100, 40, 2)!;
    expect(paths.line).toBe('M0,38 L50,2 L100,20');
    expect(paths.area).toBe('M0,38 L50,2 L100,20 L100,40 L0,40 Z');
  });

  it('draws a flat series through the middle', () => {
    expect(sparklinePaths([7, 7], 100, 40)!.line).toBe('M0,20 L100,20');
  });
});

describe('sparklineTrend', () => {
  it('compares the end of the line with its start', () => {
    expect(sparklineTrend([1, 3, 2])).toBe('up');
    expect(sparklineTrend([3, 1, 2])).toBe('down');
    expect(sparklineTrend([2, 9, 2])).toBe('flat');
    expect(sparklineTrend([2])).toBe('flat');
  });
});
