import type { Candle } from '../types/api';

/**
 * The market cards' price line, without React.
 *
 * Drawn from the server's own candles - the last hour of one-minute closes -
 * with the live price folded onto its end, so the line moves with the price
 * printed above it. Nothing here is invented: a market with no history yet
 * gets no line, and a short history is labelled as the time it covers.
 */

/** One-minute candles, the last hour of them. */
export const SPARKLINE_INTERVAL_SECONDS = 60;
export const SPARKLINE_CANDLES = 60;

export interface SparklineSeries {
  /** Prices, oldest first. */
  values: number[];
  /** Epoch ms the series starts at and reaches; null when it is empty. */
  from: number | null;
  to: number | null;
}

/**
 * The closes, oldest first, with the latest tick folded in.
 *
 * A tick inside the last candle's interval is that candle still being built,
 * so it replaces the close; a later one is the next point; an older one (a
 * late frame) changes nothing. Bad figures are dropped rather than drawn.
 */
export function sparklineSeries(
  candles: readonly Candle[],
  live?: { price: number; timestamp: number } | null,
  intervalMs = SPARKLINE_INTERVAL_SECONDS * 1000,
): SparklineSeries {
  const usable = candles
    .filter((c) => Number.isFinite(c.close) && Number.isFinite(c.time))
    .sort((a, b) => a.time - b.time);
  const values = usable.map((c) => c.close);
  let from = usable[0]?.time ?? null;
  let to = usable.length > 0 ? usable[usable.length - 1]!.time + intervalMs : null;

  if (live && Number.isFinite(live.price) && Number.isFinite(live.timestamp)) {
    const last = usable[usable.length - 1];
    if (!last) {
      values.push(live.price);
      from = live.timestamp;
      to = live.timestamp;
    } else if (live.timestamp >= last.time + intervalMs) {
      values.push(live.price);
      to = live.timestamp;
    } else if (live.timestamp >= last.time) {
      values[values.length - 1] = live.price;
    }
  }
  return { values, from, to };
}

/** "Last hour", or "Last 14 min" while there is less history than that. */
export function sparklineSpanLabel(series: Pick<SparklineSeries, 'from' | 'to'>): string | null {
  if (series.from === null || series.to === null || series.to <= series.from) return null;
  const minutes = Math.max(1, Math.round((series.to - series.from) / 60_000));
  return minutes >= SPARKLINE_CANDLES - 5 ? 'Last hour' : 'Last ' + minutes + ' min';
}

/**
 * SVG paths for the values in a `width` x `height` box: the line, and the
 * same line closed down to the bottom edge for the fill under it. Null with
 * fewer than two points - one price is not a line. A flat series is drawn
 * through the middle.
 */
export function sparklinePaths(
  values: readonly number[],
  width: number,
  height: number,
  inset = 2,
): { line: string; area: string } | null {
  if (values.length < 2) return null;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min;
  const step = width / (values.length - 1);
  const usable = height - inset * 2;
  const round = (n: number) => Math.round(n * 100) / 100;
  const points = values.map((value, index) => {
    const x = round(index * step);
    const y = round(span === 0 ? height / 2 : inset + (1 - (value - min) / span) * usable);
    return x + ',' + y;
  });
  const line = 'M' + points.join(' L');
  const area = line + ' L' + round(width) + ',' + height + ' L0,' + height + ' Z';
  return { line, area };
}

/** Whether the line ended above, below or level with where it started. */
export function sparklineTrend(values: readonly number[]): 'up' | 'down' | 'flat' {
  if (values.length < 2) return 'flat';
  const first = values[0]!;
  const last = values[values.length - 1]!;
  return last > first ? 'up' : last < first ? 'down' : 'flat';
}
