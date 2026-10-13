import type { IChartApi, Logical } from 'lightweight-charts';

/**
 * Instants to chart coordinates, and back.
 *
 * lightweight-charts lays its time axis out by bar index, not by time: every
 * data point gets one slot whatever the gap to the next. Its own
 * `timeToCoordinate` only answers for instants that are themselves points,
 * and nearly everything drawn over the chart lives between points or past
 * them - an entry is a wall-clock moment inside a bar, an expiry is usually in
 * the future, a trend line ends wherever the trader clicked.
 *
 * So everything that draws in chart space goes through here. An instant is
 * first turned into a fractional *logical index* against the bars on screen,
 * and the index into a pixel from the axis's own two-point line. The orders,
 * the expiry guide and the drawings all project through the same function,
 * which is what keeps a trend line drawn to an order's expiry sitting exactly
 * on it.
 */

export interface Timed {
  /** Epoch milliseconds. */
  time: number;
}

/**
 * Where bar `i` opens, as a logical index: the bar's own slot.
 *
 * An instant part-way through a bar is that far along towards the next slot,
 * at the bucket's rate. Past the last bar and before the first, the bucket
 * width extrapolates - which is what gives an expiry in the future a place on
 * the axis. An instant inside a gap in the data (a closed market) is held at
 * the next bar's slot rather than stretching the bar before it.
 *
 * Returns null when there is nothing to measure against.
 */
export function timeToLogical(ms: number, bars: ReadonlyArray<Timed>, bucketMs: number): number | null {
  const count = bars.length;
  if (count === 0 || !(bucketMs > 0) || !Number.isFinite(ms)) return null;

  const first = bars[0]!.time;
  const last = bars[count - 1]!.time;
  if (ms >= last) return count - 1 + (ms - last) / bucketMs;
  if (ms < first) return (ms - first) / bucketMs;

  const index = lastAtOrBefore(bars, ms);
  const open = bars[index]!.time;
  return index + Math.min(1, (ms - open) / bucketMs);
}

/** The inverse of `timeToLogical`: the instant a fractional index stands for. */
export function logicalToTime(logical: number, bars: ReadonlyArray<Timed>, bucketMs: number): number | null {
  const count = bars.length;
  if (count === 0 || !(bucketMs > 0) || !Number.isFinite(logical)) return null;

  if (logical >= count - 1) return bars[count - 1]!.time + (logical - (count - 1)) * bucketMs;
  if (logical < 0) return bars[0]!.time + logical * bucketMs;

  const index = Math.floor(logical);
  return bars[index]!.time + (logical - index) * bucketMs;
}

/**
 * Index of the last bar that opened at or before `ms`, or -1 if none did.
 * Binary search: the bars are ascending and there can be a thousand of them.
 */
export function lastAtOrBefore(bars: ReadonlyArray<Timed>, ms: number): number {
  let low = 0;
  let high = bars.length - 1;
  let found = -1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    if (bars[mid]!.time <= ms) {
      found = mid;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return found;
}

/**
 * The time axis as a straight line: x = x0 + index * spacing.
 *
 * The library places index i at `width - (base + rightOffset - i + 0.5) *
 * barSpacing`, which is linear in i and just as well defined past the last bar
 * as inside the data. But it screens out non-integer indices and quietly
 * answers 0 for them, so asking it for a fractional index directly would pin
 * every mid-bar instant to the left edge. Two whole indices are enough to
 * recover the line, and every fractional index is then exact.
 */
export interface LogicalAxis {
  x0: number;
  spacing: number;
}

export function readLogicalAxis(chart: IChartApi): LogicalAxis | null {
  const scale = chart.timeScale();
  const x0 = scale.logicalToCoordinate(0 as Logical);
  const x1 = scale.logicalToCoordinate(1 as Logical);
  if (x0 === null || x1 === null) return null;
  const spacing = x1 - x0;
  if (!(spacing > 0)) return null;
  return { x0, spacing };
}

export function logicalToX(axis: LogicalAxis, logical: number): number {
  return axis.x0 + logical * axis.spacing;
}

export function xToLogical(axis: LogicalAxis, x: number): number {
  return (x - axis.x0) / axis.spacing;
}

/** Convenience: an instant straight to a pixel, or null when either step has no answer. */
export function timeToX(
  axis: LogicalAxis | null,
  ms: number,
  bars: ReadonlyArray<Timed>,
  bucketMs: number,
): number | null {
  if (!axis) return null;
  const logical = timeToLogical(ms, bars, bucketMs);
  return logical === null ? null : logicalToX(axis, logical);
}
