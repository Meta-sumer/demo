import { useDerivedValue, type DerivedValue, type SharedValue } from 'react-native-reanimated';
import type { ChartEngine } from '../chart/engine/useChartEngine';
import { yOf } from '../chart/engine/scale';
import { timeToLogicalTimes } from '../chart/timeProjection';

/**
 * Projects order anchors onto chart coordinates, every frame.
 *
 * Why every frame rather than on the chart's own events: an order marker has
 * to stay welded to a price and an instant, and both axes move for reasons of
 * their own. Panning and zooming move the time axis, and the price scale
 * refits to the visible bars on every tick, as a new high or low widens the
 * range. So the projection is a derived value on the UI thread, recomputed
 * whenever the view, the clock or the live price moves, and the layer draws
 * from it in the same frame.
 *
 * Nothing here touches React state. The markers' views and the canvas layer
 * both read the result on the UI thread.
 */
export interface OrderAnchor {
  id: string;
  entryTimestamp: number;
  expiryTimestamp: number;
  /** The resolved entry price - live market price for a tracking preview. */
  price: number | null;
}

/**
 * What the UI thread is handed for each order, from which it resolves the
 * anchor itself every frame.
 *
 * A preview that has not been committed slides with the clock: its entry is
 * "now" and its expiry "now plus the duration", so both are recomputed here,
 * per frame, instead of stepping a second's width once a second with the
 * heartbeat that drives the countdowns. Its price is the live one.
 */
export interface AnchorSource {
  id: string;
  tracksClock: boolean;
  entryTimestamp: number;
  expiryTimestamp: number;
  expirySeconds: number;
  /** Frozen once committed; null while the line follows the market. */
  entryPrice: number | null;
}

export interface OrderGeometry {
  id: string;
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
  /** The price the anchor resolved to this frame. */
  price: number | null;
}

/** Sub-pixel movement does not count as a clamp. */
const EPSILON = 0.5;

/**
 * The anchor an order resolves to now. Exposed so the tap handler on the JS
 * thread resolves exactly what the frame drew.
 */
export function resolveAnchor(source: AnchorSource, now: number, live: number | null, fallback: number | null): OrderAnchor {
  'worklet';
  return {
    id: source.id,
    entryTimestamp: source.tracksClock ? now : source.entryTimestamp,
    expiryTimestamp: source.tracksClock ? now + source.expirySeconds * 1000 : source.expiryTimestamp,
    /*
     * The feed's last price is the fallback, matching what the ticket quotes
     * from. Without it a preview opened before the first tick lands - or
     * during a dropped socket - would have no price to sit at and would
     * silently not draw, which reads as the preview being broken rather than
     * the feed being quiet. A committed order never reaches either: its entry
     * price is frozen.
     */
    price: source.entryPrice ?? live ?? fallback,
  };
}

interface Options<T extends AnchorSource> {
  engine: Pick<ChartEngine, 'view' | 'series'>;
  sources: SharedValue<T[]>;
  now: SharedValue<number>;
  /** The live market price, mirrored from the tick store without a React render. */
  livePrice: SharedValue<number | null>;
  fallbackPrice: number | null;
}

export function useOrderProjection<T extends AnchorSource>({
  engine,
  sources,
  now,
  livePrice,
  fallbackPrice,
}: Options<T>): DerivedValue<OrderGeometry[]> {
  const { view, series } = engine;
  return useDerivedValue<OrderGeometry[]>(() => {
    const current = view.value;
    const data = series.value;
    const list = sources.value;
    const out: OrderGeometry[] = [];
    if (list.length === 0) return out;
    const plotWidth = current.pw;
    const plotHeight = current.ph;
    const time = now.value;
    const live = livePrice.value;

    /*
     * Milliseconds to a pixel on the time axis, through the same instant-to-
     * index step the expiry guide and the drawings use (chart/timeProjection),
     * so a line drawn to an order's expiry lands exactly on it. An entry is a
     * wall-clock moment in the middle of a bar and an expiry is usually past
     * the data entirely; logical indices handle both.
     */
    const toX = (ms: number): number | null => {
      'worklet';
      if (!current.ok) return null;
      const logical = timeToLogicalTimes(ms, data.t, data.bucket);
      return logical === null ? null : current.x0 + logical * current.s;
    };

    for (let i = 0; i < list.length; i += 1) {
      const anchor = resolveAnchor(list[i]!, time, live, fallbackPrice);
      const rawEntryX = toX(anchor.entryTimestamp);
      const rawExpiryX = toX(anchor.expiryTimestamp);
      const rawY = anchor.price === null || !current.ok ? null : yOf(current, anchor.price);
      const hidden = rawEntryX === null || rawExpiryX === null || rawY === null || plotWidth <= 0 || plotHeight <= 0;

      const entryX = clampTo(rawEntryX ?? 0, 0, plotWidth);
      const expiryX = clampTo(rawExpiryX ?? 0, 0, plotWidth);
      const y = clampTo(rawY ?? 0, 0, plotHeight);

      out.push({
        id: anchor.id,
        entryX,
        expiryX,
        y,
        entryClamped: rawEntryX !== null && Math.abs(rawEntryX - entryX) > EPSILON,
        expiryClamped: rawExpiryX !== null && Math.abs(rawExpiryX - expiryX) > EPSILON,
        yClamped: rawY !== null && Math.abs(rawY - y) > EPSILON,
        hidden,
        plotWidth,
        plotHeight,
        price: anchor.price,
      });
    }
    return out;
  }, [fallbackPrice]);
}

function clampTo(value: number, min: number, max: number): number {
  'worklet';
  return value < min ? min : value > max ? max : value;
}
