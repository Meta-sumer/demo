/**
 * Chart overlays.
 *
 * These are display aids computed from the candles the chart already holds -
 * they are never sent to the server and never take part in a trading decision,
 * so a wrong overlay can mislead the eye but cannot alter an order.
 *
 * Every function returns points only for the bars where the window is fully
 * populated. Padding the head with a partial average would draw a line that
 * looks like data but is not.
 */
export interface Point {
  time: number;
  value: number;
}

export interface Bar {
  time: number;
  close: number;
}

/** A bar with its range, for the oscillators that measure where a close sits inside it. */
export interface RangeBar extends Bar {
  high: number;
  low: number;
}

/** Simple moving average over `period` closes. */
export function sma(bars: Bar[], period: number): Point[] {
  if (period < 1 || bars.length < period) return [];

  const out: Point[] = [];
  let sum = 0;

  for (let i = 0; i < bars.length; i += 1) {
    sum += bars[i]!.close;
    if (i >= period) sum -= bars[i - period]!.close;
    if (i >= period - 1) out.push({ time: bars[i]!.time, value: sum / period });
  }
  return out;
}

/**
 * Exponential moving average.
 *
 * Seeded with the simple average of the first `period` closes, which is the
 * conventional seed and keeps the first plotted point comparable to the SMA.
 */
export function ema(bars: Bar[], period: number): Point[] {
  if (period < 1 || bars.length < period) return [];

  const multiplier = 2 / (period + 1);
  let seed = 0;
  for (let i = 0; i < period; i += 1) seed += bars[i]!.close;

  let value = seed / period;
  const out: Point[] = [{ time: bars[period - 1]!.time, value }];

  for (let i = period; i < bars.length; i += 1) {
    value = (bars[i]!.close - value) * multiplier + value;
    out.push({ time: bars[i]!.time, value });
  }
  return out;
}

export interface BollingerBands {
  upper: Point[];
  middle: Point[];
  lower: Point[];
}

/**
 * Bollinger bands: an SMA with a population standard deviation envelope.
 *
 * Population (divide by n), not sample (n-1) - the window is the whole
 * population being described, which is the standard definition for this band.
 */
export function bollinger(bars: Bar[], period = 20, deviations = 2): BollingerBands {
  const middle = sma(bars, period);
  if (middle.length === 0) return { upper: [], middle: [], lower: [] };

  const upper: Point[] = [];
  const lower: Point[] = [];

  for (let i = 0; i < middle.length; i += 1) {
    const end = i + period;
    let variance = 0;
    for (let j = i; j < end; j += 1) {
      const diff = bars[j]!.close - middle[i]!.value;
      variance += diff * diff;
    }
    const spread = Math.sqrt(variance / period) * deviations;
    upper.push({ time: middle[i]!.time, value: middle[i]!.value + spread });
    lower.push({ time: middle[i]!.time, value: middle[i]!.value - spread });
  }

  return { upper, middle, lower };
}

/* ------------------------------------------------------------ oscillators */

/**
 * Relative strength index, with Wilder's smoothing.
 *
 * The first value needs `period` price changes, so it lands on bar `period`
 * (the change into bar 0 does not exist). It is seeded with the plain average
 * gain and loss over those changes, then each later bar folds in with weight
 * 1/period - Wilder's original method, and the one every charting package
 * quotes, so a reading here matches a reading anywhere else.
 *
 * A window with no losses is 100 and one with no gains is 0. A window with
 * neither - a flat market - has no strength either way and reads 50 rather
 * than dividing zero by zero.
 */
export function rsi(bars: Bar[], period = 14): Point[] {
  if (period < 1 || bars.length <= period) return [];

  let gains = 0;
  let losses = 0;
  for (let i = 1; i <= period; i += 1) {
    const change = bars[i]!.close - bars[i - 1]!.close;
    if (change > 0) gains += change;
    else losses -= change;
  }

  let averageGain = gains / period;
  let averageLoss = losses / period;
  const out: Point[] = [{ time: bars[period]!.time, value: strength(averageGain, averageLoss) }];

  for (let i = period + 1; i < bars.length; i += 1) {
    const change = bars[i]!.close - bars[i - 1]!.close;
    averageGain = (averageGain * (period - 1) + Math.max(change, 0)) / period;
    averageLoss = (averageLoss * (period - 1) + Math.max(-change, 0)) / period;
    out.push({ time: bars[i]!.time, value: strength(averageGain, averageLoss) });
  }
  return out;
}

function strength(averageGain: number, averageLoss: number): number {
  if (averageLoss === 0) return averageGain === 0 ? 50 : 100;
  return 100 - 100 / (1 + averageGain / averageLoss);
}

export interface MacdLines {
  /** Fast EMA minus slow EMA. */
  macd: Point[];
  /** EMA of the MACD line. */
  signal: Point[];
  /** MACD minus signal: the bars drawn around the zero line. */
  histogram: Point[];
}

/**
 * Moving average convergence/divergence.
 *
 * The MACD line starts where the slow average does (bar `slow - 1`), because
 * before that there is nothing to subtract. The signal line is an EMA of the
 * MACD line itself, so it starts `signal - 1` bars later again, and the
 * histogram only exists where both do. Each line is returned only where it is
 * real - see the note at the top of this file.
 */
export function macd(bars: Bar[], fast = 12, slow = 26, signalPeriod = 9): MacdLines {
  const none: MacdLines = { macd: [], signal: [], histogram: [] };
  if (fast < 1 || slow <= fast || signalPeriod < 1) return none;

  const fastLine = ema(bars, fast);
  const slowLine = ema(bars, slow);
  if (slowLine.length === 0) return none;

  // fastLine[k] belongs to bar fast-1+k and slowLine[k] to bar slow-1+k, so
  // the fast value for the same bar sits (slow - fast) further along.
  const offset = slow - fast;
  const macdLine = slowLine.map((point, k) => ({ time: point.time, value: fastLine[k + offset]!.value - point.value }));

  const signal = ema(
    macdLine.map((point) => ({ time: point.time, close: point.value })),
    signalPeriod,
  );
  const lead = macdLine.length - signal.length;
  const histogram = signal.map((point, k) => ({ time: point.time, value: macdLine[k + lead]!.value - point.value }));

  return { macd: macdLine, signal, histogram };
}

export interface StochasticLines {
  /** %K, smoothed: where the close sits in the recent range, 0-100. */
  k: Point[];
  /** %D: a moving average of %K. */
  d: Point[];
}

/**
 * Slow stochastic oscillator, the (14, 3, 3) that charting packages default to.
 *
 * Raw %K is where the close sits between the lowest low and the highest high
 * of the last `period` bars. The slow form smooths that with a `kSmoothing`
 * average - the %K that is actually drawn - and %D averages %K again. A window
 * whose high equals its low has no range to sit in, and reads the midpoint
 * rather than dividing by zero.
 */
export function stochastic(bars: RangeBar[], period = 14, kSmoothing = 3, dSmoothing = 3): StochasticLines {
  if (period < 1 || kSmoothing < 1 || dSmoothing < 1 || bars.length < period) return { k: [], d: [] };

  const raw: Bar[] = [];
  for (let i = period - 1; i < bars.length; i += 1) {
    let high = -Infinity;
    let low = Infinity;
    for (let j = i - period + 1; j <= i; j += 1) {
      if (bars[j]!.high > high) high = bars[j]!.high;
      if (bars[j]!.low < low) low = bars[j]!.low;
    }
    const range = high - low;
    raw.push({ time: bars[i]!.time, close: range > 0 ? ((bars[i]!.close - low) / range) * 100 : 50 });
  }

  const k = sma(raw, kSmoothing);
  const d = sma(
    k.map((point) => ({ time: point.time, close: point.value })),
    dSmoothing,
  );
  return { k, d };
}
