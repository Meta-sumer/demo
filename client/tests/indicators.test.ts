import { describe, expect, it } from 'vitest';
import { bollinger, ema, macd, rsi, sma, stochastic, type Bar, type RangeBar } from '../src/utils/indicators';

function bars(closes: number[]): Bar[] {
  return closes.map((close, index) => ({ time: index * 60, close }));
}

function ranged(rows: Array<[high: number, low: number, close: number]>): RangeBar[] {
  return rows.map(([high, low, close], index) => ({ time: index * 60, high, low, close }));
}

/** An EMA written the long way, as an independent check on the MACD plumbing. */
function naiveEma(values: number[], period: number): Array<number | null> {
  const out: Array<number | null> = values.map(() => null);
  if (values.length < period) return out;
  let value = values.slice(0, period).reduce((sum, v) => sum + v, 0) / period;
  out[period - 1] = value;
  for (let i = period; i < values.length; i += 1) {
    value = values[i]! * (2 / (period + 1)) + value * (1 - 2 / (period + 1));
    out[i] = value;
  }
  return out;
}

describe('sma', () => {
  it('averages a full window and skips the incomplete head', () => {
    const result = sma(bars([1, 2, 3, 4, 5]), 3);

    // Windows are [1,2,3], [2,3,4], [3,4,5] - the first two bars have no window.
    expect(result).toEqual([
      { time: 120, value: 2 },
      { time: 180, value: 3 },
      { time: 240, value: 4 },
    ]);
  });

  it('returns nothing when there are fewer bars than the period', () => {
    expect(sma(bars([1, 2]), 5)).toEqual([]);
  });

  it('does not drift over a long constant series', () => {
    const result = sma(bars(Array<number>(500).fill(42)), 20);

    // The rolling sum subtracts as it adds; any drift would show here.
    for (const point of result) expect(point.value).toBeCloseTo(42, 10);
  });
});

describe('ema', () => {
  it('seeds with the simple average of the first window', () => {
    const result = ema(bars([1, 2, 3, 4, 5]), 3);

    expect(result[0]).toEqual({ time: 120, value: 2 });
  });

  it('tracks a step change faster than the equivalent sma', () => {
    const series = bars([...Array<number>(20).fill(100), ...Array<number>(5).fill(200)]);
    const emaLast = ema(series, 10).at(-1)!.value;
    const smaLast = sma(series, 10).at(-1)!.value;

    expect(emaLast).toBeGreaterThan(smaLast);
  });

  it('converges to a constant series', () => {
    const result = ema(bars(Array<number>(200).fill(75)), 14);

    expect(result.at(-1)!.value).toBeCloseTo(75, 8);
  });
});

describe('bollinger', () => {
  it('collapses the bands onto the mean when the window is flat', () => {
    const { upper, middle, lower } = bollinger(bars(Array<number>(30).fill(10)), 20);

    expect(middle.at(-1)!.value).toBeCloseTo(10, 10);
    expect(upper.at(-1)!.value).toBeCloseTo(10, 10);
    expect(lower.at(-1)!.value).toBeCloseTo(10, 10);
  });

  it('places the bands symmetrically around the mean', () => {
    const { upper, middle, lower } = bollinger(bars([1, 5, 2, 8, 3, 9, 4, 7, 6, 2]), 5);

    for (let i = 0; i < middle.length; i += 1) {
      expect(upper[i]!.value - middle[i]!.value).toBeCloseTo(middle[i]!.value - lower[i]!.value, 10);
    }
  });

  it('uses the population deviation', () => {
    // closes 2,4,4,4,5,5,7,9 -> mean 5, population sd 2 exactly.
    const { upper, middle, lower } = bollinger(bars([2, 4, 4, 4, 5, 5, 7, 9]), 8, 2);

    expect(middle[0]!.value).toBeCloseTo(5, 10);
    expect(upper[0]!.value).toBeCloseTo(9, 10);
    expect(lower[0]!.value).toBeCloseTo(1, 10);
  });

  it('returns empty bands when the series is too short', () => {
    expect(bollinger(bars([1, 2, 3]), 20)).toEqual({ upper: [], middle: [], lower: [] });
  });
});

describe('rsi', () => {
  // The worked example from StockCharts' RSI article (Wilder's method, 14).
  const closes = [
    44.3389, 44.0902, 44.1497, 43.6124, 44.3278, 44.8264, 45.0955, 45.4245, 45.8433, 46.0826, 45.8931, 46.0328,
    45.614, 46.282, 46.282, 46.0028, 46.0328, 46.4116, 46.2222, 45.6439, 46.2122, 46.2521, 45.7137, 46.4515, 45.7835,
    45.3548, 44.0288, 44.1783, 44.2181, 44.5672, 43.4205, 42.6628, 43.1314,
  ];

  it('matches the published worked example', () => {
    const result = rsi(bars(closes), 14);
    const published = [
      70.53, 66.32, 66.55, 69.41, 66.36, 57.97, 62.93, 63.26, 56.06, 62.38, 54.71, 50.42, 39.99, 41.46, 41.87, 45.46,
      37.3,
    ];

    for (let i = 0; i < published.length; i += 1) expect(result[i]!.value).toBeCloseTo(published[i]!, 2);
    // The article's spreadsheet rounds its running averages, so its last rows
    // drift by a few hundredths from the exact figure.
    expect(result.at(-2)!.value).toBeCloseTo(33.09, 1);
    expect(result.at(-1)!.value).toBeCloseTo(37.79, 1);
  });

  it('starts on the bar after the first full window of changes', () => {
    const result = rsi(bars(closes), 14);

    // 14 changes need 15 closes, so the first value belongs to bar 14.
    expect(result[0]!.time).toBe(14 * 60);
    expect(result).toHaveLength(closes.length - 14);
  });

  it('reads 100 when a window has only gains and 0 when it has only losses', () => {
    expect(rsi(bars(Array.from({ length: 20 }, (_, i) => 100 + i)), 14).at(-1)!.value).toBe(100);
    expect(rsi(bars(Array.from({ length: 20 }, (_, i) => 100 - i)), 14).at(-1)!.value).toBe(0);
  });

  it('reads the midpoint for a market that did not move, rather than NaN', () => {
    const result = rsi(bars(Array<number>(30).fill(5)), 14);

    for (const point of result) expect(point.value).toBe(50);
  });

  it('stays inside 0-100 on a noisy series', () => {
    const noisy = Array.from({ length: 300 }, (_, i) => 100 + Math.sin(i * 1.7) * 5 + Math.cos(i * 0.3) * 3);

    for (const point of rsi(bars(noisy), 14)) {
      expect(point.value).toBeGreaterThanOrEqual(0);
      expect(point.value).toBeLessThanOrEqual(100);
    }
  });

  it('returns nothing without a full window of changes', () => {
    expect(rsi(bars(Array<number>(14).fill(1)), 14)).toEqual([]);
  });
});

describe('macd', () => {
  const series = Array.from({ length: 80 }, (_, i) => 50 + Math.sin(i / 4) * 6 + i * 0.1);

  it('is the fast EMA minus the slow EMA, from the bar the slow one starts on', () => {
    const { macd: line } = macd(bars(series), 12, 26, 9);
    const fast = naiveEma(series, 12);
    const slow = naiveEma(series, 26);

    expect(line[0]!.time).toBe(25 * 60);
    expect(line).toHaveLength(series.length - 25);
    for (const point of line) {
      const index = point.time / 60;
      expect(point.value).toBeCloseTo(fast[index]! - slow[index]!, 10);
    }
  });

  it('builds the signal from the MACD line and the histogram from both', () => {
    const { macd: line, signal, histogram } = macd(bars(series), 12, 26, 9);
    const expected = naiveEma(
      line.map((point) => point.value),
      9,
    );

    // The signal needs nine MACD values of its own: bar 25 + 8.
    expect(signal[0]!.time).toBe(33 * 60);
    expect(histogram).toHaveLength(signal.length);
    for (let k = 0; k < signal.length; k += 1) {
      expect(signal[k]!.value).toBeCloseTo(expected[k + 8]!, 10);
      expect(histogram[k]!.value).toBeCloseTo(line[k + 8]!.value - signal[k]!.value, 10);
      expect(histogram[k]!.time).toBe(signal[k]!.time);
    }
  });

  it('is flat at zero for a market that did not move', () => {
    const { macd: line, signal, histogram } = macd(bars(Array<number>(60).fill(7)), 12, 26, 9);

    for (const point of [...line, ...signal, ...histogram]) expect(point.value).toBeCloseTo(0, 12);
  });

  it('turns positive in a steady rise', () => {
    const { macd: line } = macd(bars(Array.from({ length: 60 }, (_, i) => 100 + i)), 12, 26, 9);

    expect(line.at(-1)!.value).toBeGreaterThan(0);
  });

  it('returns nothing for too little data or an inverted pair of periods', () => {
    expect(macd(bars(Array<number>(25).fill(1)))).toEqual({ macd: [], signal: [], histogram: [] });
    expect(macd(bars(series), 26, 12, 9)).toEqual({ macd: [], signal: [], histogram: [] });
  });
});

describe('stochastic', () => {
  it('places the close inside the recent range', () => {
    // Range 10-20 throughout; closes at the top, the bottom and the middle.
    const rows: Array<[number, number, number]> = [
      [20, 10, 20],
      [20, 10, 20],
      [20, 10, 20],
      [20, 10, 10],
      [20, 10, 10],
      [20, 10, 10],
      [20, 10, 15],
      [20, 10, 15],
      [20, 10, 15],
    ];
    const { k, d } = stochastic(ranged(rows), 3, 1, 1);

    expect(k.map((point) => point.value)).toEqual([100, 0, 0, 0, 50, 50, 50]);
    expect(d.map((point) => point.value)).toEqual([100, 0, 0, 0, 50, 50, 50]);
  });

  it('smooths %K and then %D, each starting where its window fills', () => {
    const rows = Array.from({ length: 40 }, (_, i): [number, number, number] => [
      110 + Math.sin(i) * 4,
      90 + Math.cos(i) * 4,
      100 + Math.sin(i * 0.7) * 8,
    ]);
    const bars = ranged(rows);
    const { k, d } = stochastic(bars, 14, 3, 3);
    const raw = stochastic(bars, 14, 1, 1).k;

    // Raw %K starts on bar 13, smoothed %K two bars later, %D two after that.
    expect(raw[0]!.time).toBe(13 * 60);
    expect(k[0]!.time).toBe(15 * 60);
    expect(d[0]!.time).toBe(17 * 60);
    expect(k[0]!.value).toBeCloseTo((raw[0]!.value + raw[1]!.value + raw[2]!.value) / 3, 10);
    expect(d[0]!.value).toBeCloseTo((k[0]!.value + k[1]!.value + k[2]!.value) / 3, 10);
  });

  it('stays inside 0-100', () => {
    const rows = Array.from({ length: 120 }, (_, i): [number, number, number] => {
      const mid = 100 + Math.sin(i / 5) * 10;
      return [mid + 2, mid - 2, mid + Math.sin(i * 3.1) * 2];
    });

    for (const line of Object.values(stochastic(ranged(rows)))) {
      for (const point of line) {
        expect(point.value).toBeGreaterThanOrEqual(0);
        expect(point.value).toBeLessThanOrEqual(100);
      }
    }
  });

  it('reads the midpoint when the range has collapsed, rather than NaN', () => {
    const { k } = stochastic(ranged(Array.from({ length: 20 }, (): [number, number, number] => [5, 5, 5])));

    for (const point of k) expect(point.value).toBe(50);
  });

  it('returns nothing without a full window', () => {
    expect(stochastic(ranged([[2, 1, 1.5]]), 14)).toEqual({ k: [], d: [] });
  });
});
