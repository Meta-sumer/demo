import { useEffect, useRef, type MutableRefObject, type RefObject } from 'react';
import type { IChartApi, ISeriesApi } from 'lightweight-charts';
import { readLogicalAxis, timeToX } from '../chart/timeProjection';

/**
 * Projects order anchors onto chart coordinates, every frame.
 *
 * Why a frame loop rather than the chart's own events: an order marker has to
 * stay welded to a price and an instant, and both axes move for reasons the
 * library does not announce. Panning and zooming fire
 * `subscribeVisibleTimeRangeChange`, but the price scale autoscaling - which
 * happens on every tick, as the new high or low widens the range - fires
 * nothing at all. A marker driven by events alone slides off its price the
 * moment the market moves.
 *
 * Nothing here touches React state. The loop writes straight to the DOM nodes
 * the layer registers, and only when a coordinate has actually changed, so a
 * still chart with open orders costs a comparison per frame and no layout.
 */
export interface OrderAnchor {
  id: string;
  entryTimestamp: number;
  expiryTimestamp: number;
  /** The resolved entry price - live market price for a tracking preview. */
  price: number | null;
}

export interface OrderGeometry {
  /** Entry instant, in pixels from the plot's left edge. */
  entryX: number;
  /** Expiry instant. Clamped to the plot, which is why the flag exists. */
  expiryX: number;
  /** Entry price, in pixels from the plot's top edge. */
  y: number;
  entryClamped: boolean;
  expiryClamped: boolean;
  yClamped: boolean;
  /** Set when there is no usable coordinate at all; the order is hidden. */
  hidden: boolean;
  plotWidth: number;
  plotHeight: number;
}

interface Options {
  chartRef: MutableRefObject<IChartApi | null>;
  /** The series that owns the price scale the orders are measured against. */
  seriesRef: MutableRefObject<ISeriesApi<'Candlestick'> | null>;
  containerRef: MutableRefObject<HTMLDivElement | null>;
  /**
   * Bar times in milliseconds, used to extend the time axis past the data.
   *
   * Read-only, so a ref holding richer bars than this can be passed straight
   * in - a mutable ref would be invariant and would not accept one.
   */
  barsRef: RefObject<ReadonlyArray<{ time: number }>>;
  bucketMs: number;
  /** Reads the current anchors. Called every frame, so it must be cheap. */
  resolve: () => OrderAnchor[];
  /** Receives a changed geometry. Called only on a real change. */
  apply: (id: string, geometry: OrderGeometry) => void;
  enabled: boolean;
  /** Frame budget. Performance mode halves it. */
  maxFps?: number;
}

/** Sub-pixel movement is not worth a style write. */
const EPSILON = 0.5;

export function useOrderProjection({
  chartRef,
  seriesRef,
  containerRef,
  barsRef,
  bucketMs,
  resolve,
  apply,
  enabled,
  maxFps = 60,
}: Options): void {
  // The callbacks change on every render; holding them in refs keeps the loop
  // from being torn down and restarted sixty times a second.
  const resolveRef = useRef(resolve);
  const applyRef = useRef(apply);
  const bucketRef = useRef(bucketMs);
  resolveRef.current = resolve;
  applyRef.current = apply;
  bucketRef.current = bucketMs;

  const cacheRef = useRef(new Map<string, OrderGeometry>());

  useEffect(() => {
    if (!enabled) {
      cacheRef.current.clear();
      return;
    }

    let frame = 0;
    let lastRun = 0;
    // A frame's timestamp jitters around 1000/60, so comparing strictly with
    // the budget dropped every other frame and ran the loop at half the rate
    // it was given. Two milliseconds of slack keeps the budget honest.
    const minInterval = 1000 / Math.max(1, maxFps) - 2;

    const run = (timestamp: number) => {
      frame = window.requestAnimationFrame(run);
      if (timestamp - lastRun < minInterval) return;
      lastRun = timestamp;

      const chart = chartRef.current;
      const series = seriesRef.current;
      const container = containerRef.current;
      if (!chart || !series || !container) return;

      const anchors = resolveRef.current();
      const cache = cacheRef.current;

      if (anchors.length === 0) {
        if (cache.size > 0) cache.clear();
        return;
      }

      const scale = chart.timeScale();
      const plotWidth = scale.width();
      const plotHeight = Math.max(0, container.clientHeight - scale.height());
      if (plotWidth <= 0 || plotHeight <= 0) return;

      const bars = barsRef.current ?? [];
      const bucket = bucketRef.current;

      /**
       * Milliseconds to a pixel on the time axis.
       *
       * `timeToCoordinate` is no use here. It only answers for instants that
       * are themselves bars, and an order's entry is a wall-clock moment in
       * the middle of a bar while its expiry is usually in the future, past
       * the data entirely.
       *
       * Logical indices have no such limit - the axis places index i at
       * `width - (base + rightOffset - i + 0.5) * barSpacing`, which is
       * defined just as well beyond the last bar as within it. But the library
       * screens out non-integer indices and quietly answers 0 for them, so
       * asking for a fractional index directly would pin every mid-bar entry
       * to the left edge. The mapping is linear in i, so the line through two
       * whole indices gives the exact coordinate for any fractional one.
       *
       * The instant-to-index step is shared with the expiry guide and the
       * drawings (`chart/timeProjection`), so a line drawn to an order's
       * expiry lands exactly on it.
       */
      const axis = readLogicalAxis(chart);
      const toX = (ms: number): number | null => timeToX(axis, ms, bars, bucket);

      const seen = new Set<string>();

      for (const anchor of anchors) {
        seen.add(anchor.id);

        const rawEntryX = toX(anchor.entryTimestamp);
        const rawExpiryX = toX(anchor.expiryTimestamp);
        const rawY = anchor.price === null ? null : series.priceToCoordinate(anchor.price);

        const hidden = rawEntryX === null || rawExpiryX === null || rawY === null;

        const entryX = clamp(rawEntryX ?? 0, 0, plotWidth);
        const expiryX = clamp(rawExpiryX ?? 0, 0, plotWidth);
        const y = clamp(rawY ?? 0, 0, plotHeight);

        const next: OrderGeometry = {
          entryX,
          expiryX,
          y,
          entryClamped: rawEntryX !== null && Math.abs(rawEntryX - entryX) > EPSILON,
          expiryClamped: rawExpiryX !== null && Math.abs(rawExpiryX - expiryX) > EPSILON,
          yClamped: rawY !== null && Math.abs(rawY - y) > EPSILON,
          hidden,
          plotWidth,
          plotHeight,
        };

        const previous = cache.get(anchor.id);
        if (previous && same(previous, next)) continue;
        cache.set(anchor.id, next);
        applyRef.current(anchor.id, next);
      }

      for (const id of cache.keys()) if (!seen.has(id)) cache.delete(id);
    };

    frame = window.requestAnimationFrame(run);
    return () => window.cancelAnimationFrame(frame);
  }, [enabled, maxFps, chartRef, seriesRef, containerRef, barsRef]);
}

function clamp(value: number, min: number, max: number): number {
  return value < min ? min : value > max ? max : value;
}

function same(a: OrderGeometry, b: OrderGeometry): boolean {
  return (
    Math.abs(a.entryX - b.entryX) < EPSILON &&
    Math.abs(a.expiryX - b.expiryX) < EPSILON &&
    Math.abs(a.y - b.y) < EPSILON &&
    Math.abs(a.plotWidth - b.plotWidth) < EPSILON &&
    Math.abs(a.plotHeight - b.plotHeight) < EPSILON &&
    a.entryClamped === b.entryClamped &&
    a.expiryClamped === b.expiryClamped &&
    a.yClamped === b.yClamped &&
    a.hidden === b.hidden
  );
}
