/**
 * The chart engine's data shapes.
 *
 * The engine replaces lightweight-charts. It keeps that library's model - a
 * time axis laid out by bar index, a right offset counted in bars, a price
 * scale fitted to the bars on screen - so everything the web client projects
 * onto the chart projects the same way here.
 *
 * Two threads share it. The JS thread owns the data (history, ticks,
 * indicators, orders) and writes it into Reanimated shared values; the UI
 * thread owns the viewport (bar spacing, right offset, the fitted price range)
 * and repaints from both inside worklets. A pan or a pinch therefore never
 * costs a React render.
 */

/**
 * The series as the UI thread holds it: one flat array per field.
 *
 * Copying a thousand `{ time, open, ... }` objects across threads on every
 * tick is what flat arrays avoid; a tick updates the last element in place.
 * `NaN` in `c` is whitespace - a slot on the axis with nothing drawn - which
 * the trade window uses for the part of a running trade still in the future.
 */
export interface SeriesData {
  /** Bar open times, epoch ms, ascending. */
  t: number[];
  o: number[];
  h: number[];
  l: number[];
  c: number[];
  /** Bucket width in ms. */
  bucket: number;
}

export const EMPTY_SERIES: SeriesData = { t: [], o: [], h: [], l: [], c: [], bucket: 60_000 };

export interface Size {
  w: number;
  h: number;
}

/**
 * Everything a frame needs to turn an index or a price into a pixel.
 *
 * Recomputed on the UI thread whenever the size, the spacing, the offset or
 * the data changes - the price range included, which is fitted to whatever is
 * visible, so the scale follows a pan the way lightweight-charts' autoscale
 * does.
 */
export interface View {
  /** There are bars and room to draw them. */
  ok: boolean;
  /** The whole canvas. */
  w: number;
  h: number;
  /** The plot: the canvas less the price axis on the right and the time axis below. */
  pw: number;
  ph: number;
  axisW: number;
  axisH: number;
  /** Bar spacing, px. */
  s: number;
  /** x of logical index 0 - the centre of the first bar. */
  x0: number;
  count: number;
  /** First and last bar with any part on screen, inclusive. `last < first` when none is. */
  first: number;
  last: number;
  /** Prices at the bottom and the top of the plot, margins included. */
  lo: number;
  hi: number;
}

export interface Crosshair {
  visible: boolean;
  /** The bar under it. */
  index: number;
  /** Where the finger is, for a chart whose crosshair does not snap to the close. */
  y: number;
}

export const HIDDEN_CROSSHAIR: Crosshair = { visible: false, index: -1, y: 0 };

/**
 * A line drawn over the price, aligned with the bars: `v[i]` belongs to bar
 * `i`, and `NaN` where the line has no value yet.
 */
export interface OverlayLine {
  v: number[];
  color: string;
  dashed: boolean;
}

/**
 * Wall-clock offset from UTC, piecewise.
 *
 * Intl is far too slow to call per label per frame on the UI thread, so the
 * offsets for the span on screen are worked out once on the JS thread - with
 * every daylight-saving switch inside it - and the axis formats from these.
 * `off[i]` (minutes) applies before `at[i]`; the last entry applies after the
 * last switch.
 */
export interface ZoneInfo {
  at: number[];
  off: number[];
}

export const UTC_ZONE: ZoneInfo = { at: [], off: [0] };
