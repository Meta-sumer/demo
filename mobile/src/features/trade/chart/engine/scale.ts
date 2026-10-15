import { dayTimeText, clockText, hourMinuteText, localDay, offsetAt } from './format';
import type { SeriesData, Size, View, ZoneInfo } from './types';

/**
 * The viewport: which bars are on screen, where, and against what prices.
 *
 * All worklets. The engine recomputes a `View` on the UI thread whenever the
 * size, the spacing, the offset or the data moves, and every layer projects
 * through it.
 */

export interface Margins {
  /** Share of the plot height kept empty above the highest price on screen. */
  top: number;
  bottom: number;
}

/** A fixed price range, for the bounded oscillators; otherwise the visible data decides. */
export interface FixedRange {
  lo: number;
  hi: number;
}

export interface ViewInput {
  size: Size;
  axisW: number;
  axisH: number;
  spacing: number;
  /** Bars between the last bar and the plot's right edge. */
  rightOffset: number;
  count: number;
}

/**
 * The horizontal half of a view: everything but the price range.
 *
 * Index i sits at `pw - (count - 1 + rightOffset - i + 0.5) * spacing`, the
 * placement lightweight-charts uses - so a right offset of 6 leaves six empty
 * slots ahead of the newest bar, and a negative one means the trader has
 * dragged the newest bar off the right edge.
 */
export function horizontal(input: ViewInput): Omit<View, 'lo' | 'hi'> {
  'worklet';
  const { size, axisW, axisH, spacing, rightOffset, count } = input;
  const pw = Math.max(0, size.w - axisW);
  const ph = Math.max(0, size.h - axisH);
  const s = spacing > 0 ? spacing : 1;
  const x0 = pw - (count - 1 + rightOffset + 0.5) * s;
  const leftLogical = -x0 / s;
  const rightLogical = (pw - x0) / s;
  const first = Math.max(0, Math.ceil(leftLogical - 0.5));
  const last = Math.min(count - 1, Math.floor(rightLogical + 0.5));
  return {
    ok: count > 0 && pw > 0 && ph > 0,
    w: size.w,
    h: size.h,
    pw,
    ph,
    axisW,
    axisH,
    s,
    x0,
    count,
    first,
    last,
  };
}

/**
 * Fits the price range to what is on screen, the way lightweight-charts'
 * autoscale does: the highs and lows of the visible bars (or their closes,
 * for a line), every visible overlay value, then the margins on top.
 */
export function fitPrices(
  base: Omit<View, 'lo' | 'hi'>,
  series: SeriesData,
  extras: ReadonlyArray<ReadonlyArray<number>>,
  closeOnly: boolean,
  margins: Margins,
  minMove: number,
  fixed: FixedRange | null,
): View {
  'worklet';
  let lo = Infinity;
  let hi = -Infinity;
  if (fixed) {
    lo = fixed.lo;
    hi = fixed.hi;
  } else {
    for (let i = base.first; i <= base.last; i += 1) {
      if (closeOnly) {
        const close = series.c[i]!;
        if (close === close) {
          if (close < lo) lo = close;
          if (close > hi) hi = close;
        }
      } else {
        const low = series.l[i]!;
        const high = series.h[i]!;
        if (low === low && low < lo) lo = low;
        if (high === high && high > hi) hi = high;
      }
    }
    for (let k = 0; k < extras.length; k += 1) {
      const values = extras[k]!;
      for (let i = base.first; i <= base.last; i += 1) {
        const value = values[i];
        if (value !== undefined && value === value) {
          if (value < lo) lo = value;
          if (value > hi) hi = value;
        }
      }
    }
    if (!(hi >= lo)) {
      // Nothing on screen: hold the scale on the newest bar rather than
      // collapsing it, so a chart panned into empty space still has an axis.
      const n = series.c.length;
      let found = false;
      for (let i = n - 1; i >= 0 && !found; i -= 1) {
        const close = series.c[i]!;
        if (close === close) {
          lo = closeOnly ? close : series.l[i]!;
          hi = closeOnly ? close : series.h[i]!;
          found = true;
        }
      }
      if (!found) {
        lo = 0;
        hi = 1;
      }
    }
  }
  if (hi - lo <= 0) {
    const pad = Math.max(Math.abs(hi) * 0.0005, minMove * 5, 1e-9);
    lo -= pad;
    hi += pad;
  }
  const span = hi - lo;
  const inner = Math.max(0.05, 1 - margins.top - margins.bottom);
  return {
    ...base,
    lo: lo - (span * margins.bottom) / inner,
    hi: hi + (span * margins.top) / inner,
  };
}

/** A price to a y inside the plot. */
export function yOf(view: View, price: number): number {
  'worklet';
  const span = view.hi - view.lo;
  if (!(span > 0)) return view.ph / 2;
  return ((view.hi - price) / span) * view.ph;
}

/** A y inside the plot to a price. */
export function priceAt(view: View, y: number): number {
  'worklet';
  if (!(view.ph > 0)) return view.hi;
  return view.hi - (y / view.ph) * (view.hi - view.lo);
}

/** The centre of bar `i`, or any fractional index. */
export function xOf(view: { x0: number; s: number }, logical: number): number {
  'worklet';
  return view.x0 + logical * view.s;
}

export function logicalAt(view: { x0: number; s: number }, x: number): number {
  'worklet';
  return (x - view.x0) / view.s;
}

/** The bar under an x, clamped to the data. */
export function indexAt(view: View, x: number): number {
  'worklet';
  if (view.count === 0) return -1;
  const index = Math.round(logicalAt(view, x));
  return index < 0 ? 0 : index > view.count - 1 ? view.count - 1 : index;
}

/**
 * The right offset that keeps the logical index at `x` under `x` when the
 * spacing becomes `spacing` - zooming about a point.
 */
export function offsetKeeping(
  pw: number,
  count: number,
  logical: number,
  x: number,
  spacing: number,
): number {
  'worklet';
  const x0 = x - logical * spacing;
  return (pw - x0) / spacing - (count - 1) - 0.5;
}

/**
 * How far the view may scroll: at least a couple of bars always stay on
 * screen, whichever way the trader drags.
 */
export function clampOffset(offset: number, count: number, pw: number, spacing: number): number {
  'worklet';
  const visible = spacing > 0 ? pw / spacing : 0;
  const max = Math.max(0, visible - 2);
  const min = -Math.max(0, count - 2);
  return offset < min ? min : offset > max ? max : offset;
}

/* ------------------------------------------------------------- tick marks */

export interface PriceMark {
  y: number;
  value: number;
  /** A rounder number than its neighbours - TradingView writes these brighter. */
  major: boolean;
}

const NICE = [1, 2, 2.5, 5, 10];

/**
 * Price labels at round numbers, about `gap` pixels apart, never finer than
 * the instrument's own precision.
 */
export function priceMarks(view: View, digits: number, gap: number): PriceMark[] {
  'worklet';
  const out: PriceMark[] = [];
  const span = view.hi - view.lo;
  if (!(span > 0) || !(view.ph > 0)) return out;
  const rough = (span * gap) / view.ph;
  const minStep = Math.pow(10, -Math.max(0, digits));
  const magnitude = Math.pow(10, Math.floor(Math.log10(rough)));
  let step = magnitude * 10;
  let mantissa = 10;
  for (let i = 0; i < NICE.length; i += 1) {
    const candidate = NICE[i]! * magnitude;
    // 2.5 of the smallest unit cannot be written at this precision.
    if (NICE[i] === 2.5 && candidate < minStep * 10) continue;
    if (candidate >= rough) {
      step = candidate;
      mantissa = NICE[i]!;
      break;
    }
  }
  if (step < minStep) {
    step = minStep;
    mantissa = 1;
  }
  // Every label that lands on the next round number up: 5 steps of 1 or 2,
  // 4 of 2.5, 2 of 5.
  const every = mantissa === 2.5 ? 4 : mantissa === 5 ? 2 : 5;
  const start = Math.ceil(view.lo / step);
  const end = Math.floor(view.hi / step);
  for (let k = start; k <= end && out.length < 64; k += 1) {
    const value = k * step;
    out.push({ y: yOf(view, value), value, major: ((k % every) + every) % every === 0 });
  }
  return out;
}

export interface TimeMark {
  x: number;
  text: string;
}

const STEPS = [
  1_000, 2_000, 5_000, 10_000, 15_000, 30_000, 60_000, 120_000, 300_000, 600_000, 900_000, 1_800_000, 3_600_000,
  7_200_000, 10_800_000, 14_400_000, 21_600_000, 43_200_000, 86_400_000, 172_800_000, 604_800_000, 2_592_000_000,
];

/**
 * Time labels on the bars where the clock crosses a round step.
 *
 * The step is the smallest that keeps labels about `gap` pixels apart at this
 * spacing. Like lightweight-charts, labels sit on bars (the axis is by index),
 * a label opening a new day carries the date, and frames under a minute show
 * seconds. Labels that would still collide - a gap in the data bunches bars
 * from far-apart times - are thinned by the painter, which can measure them.
 */
export function timeMarks(view: View, times: ReadonlyArray<number>, bucket: number, zone: ZoneInfo, gap: number): TimeMark[] {
  'worklet';
  const out: TimeMark[] = [];
  if (!view.ok || view.last < view.first) return out;
  let step = STEPS[STEPS.length - 1]!;
  for (let i = 0; i < STEPS.length; i += 1) {
    const candidate = STEPS[i]!;
    if (candidate >= bucket && (candidate / bucket) * view.s >= gap) {
      step = candidate;
      break;
    }
  }
  for (let i = Math.max(1, view.first); i <= view.last; i += 1) {
    const time = times[i]!;
    const before = times[i - 1]!;
    const shift = offsetAt(zone, time) * 60_000;
    const shiftBefore = offsetAt(zone, before) * 60_000;
    if (Math.floor((time + shift) / step) === Math.floor((before + shiftBefore) / step)) continue;
    const x = xOf(view, i);
    if (x < 0 || x > view.pw) continue;
    const newDay = localDay(time, zone) !== localDay(before, zone);
    const text = newDay ? dayTimeText(time, zone) : step < 60_000 ? clockText(time, zone) : hourMinuteText(time, zone);
    out.push({ x, text });
  }
  return out;
}
