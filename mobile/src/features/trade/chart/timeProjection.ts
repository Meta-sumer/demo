/**
 * Instants to chart coordinates, and back.
 *
 * The chart engine (./engine) lays its time axis out by bar index, not by
 * time - the same way lightweight-charts does on the web: every data point
 * gets one slot whatever the gap to the next. Nearly everything drawn over the
 * chart lives between points or past them - an entry is a wall-clock moment
 * inside a bar, an expiry is usually in the future, a trend line ends wherever
 * the trader tapped.
 *
 * So everything that draws in chart space goes through here. An instant is
 * first turned into a fractional *logical index* against the bars on screen,
 * and the index into a pixel from the axis's own straight line. The orders,
 * the expiry guide and the drawings all project through the same function,
 * which is what keeps a trend line drawn to an order's expiry sitting exactly
 * on it.
 *
 * Every function here is a worklet as well as plain TypeScript: the layers
 * call them on the UI thread every frame, and the tap handlers call the very
 * same code on the JS thread, so a point placed by a tap lands where the frame
 * draws it.
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
  'worklet';
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
  'worklet';
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
  'worklet';
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

/*
 * The same three, over a bare array of open times.
 *
 * The UI thread holds the series as flat number arrays (see engine/types),
 * because copying a thousand small objects across threads on every tick is
 * what a flat array avoids. The rules are identical, line for line.
 */

export function lastAtOrBeforeTimes(times: ReadonlyArray<number>, ms: number): number {
  'worklet';
  let low = 0;
  let high = times.length - 1;
  let found = -1;
  while (low <= high) {
    const mid = (low + high) >> 1;
    if (times[mid]! <= ms) {
      found = mid;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }
  return found;
}

export function timeToLogicalTimes(ms: number, times: ReadonlyArray<number>, bucketMs: number): number | null {
  'worklet';
  const count = times.length;
  if (count === 0 || !(bucketMs > 0) || !Number.isFinite(ms)) return null;

  const first = times[0]!;
  const last = times[count - 1]!;
  if (ms >= last) return count - 1 + (ms - last) / bucketMs;
  if (ms < first) return (ms - first) / bucketMs;

  const index = lastAtOrBeforeTimes(times, ms);
  const open = times[index]!;
  return index + Math.min(1, (ms - open) / bucketMs);
}

export function logicalToTimeTimes(logical: number, times: ReadonlyArray<number>, bucketMs: number): number | null {
  'worklet';
  const count = times.length;
  if (count === 0 || !(bucketMs > 0) || !Number.isFinite(logical)) return null;

  if (logical >= count - 1) return times[count - 1]! + (logical - (count - 1)) * bucketMs;
  if (logical < 0) return times[0]! + logical * bucketMs;

  const index = Math.floor(logical);
  return times[index]! + (logical - index) * bucketMs;
}

/**
 * The time axis as a straight line: x = x0 + index * spacing.
 *
 * The engine places index i at `plotWidth - (lastIndex + rightOffset - i +
 * 0.5) * barSpacing` - the formula lightweight-charts uses, linear in i and
 * just as well defined past the last bar as inside the data, fractional
 * indices included.
 */
export interface LogicalAxis {
  x0: number;
  spacing: number;
}

/** The axis line of a chart view (engine/types `View`), or null before there is one. */
export function readLogicalAxis(view: { ok: boolean; x0: number; s: number }): LogicalAxis | null {
  'worklet';
  if (!view.ok || !(view.s > 0)) return null;
  return { x0: view.x0, spacing: view.s };
}

export function logicalToX(axis: LogicalAxis, logical: number): number {
  'worklet';
  return axis.x0 + logical * axis.spacing;
}

export function xToLogical(axis: LogicalAxis, x: number): number {
  'worklet';
  return (x - axis.x0) / axis.spacing;
}

/** Convenience: an instant straight to a pixel, or null when either step has no answer. */
export function timeToX(
  axis: LogicalAxis | null,
  ms: number,
  bars: ReadonlyArray<Timed>,
  bucketMs: number,
): number | null {
  'worklet';
  if (!axis) return null;
  const logical = timeToLogical(ms, bars, bucketMs);
  return logical === null ? null : logicalToX(axis, logical);
}

/** `timeToX` over a bare array of open times - the UI thread's form. */
export function timeToXTimes(
  axis: LogicalAxis | null,
  ms: number,
  times: ReadonlyArray<number>,
  bucketMs: number,
): number | null {
  'worklet';
  if (!axis) return null;
  const logical = timeToLogicalTimes(ms, times, bucketMs);
  return logical === null ? null : logicalToX(axis, logical);
}
