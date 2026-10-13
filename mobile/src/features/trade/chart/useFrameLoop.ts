import { useCallback, useEffect } from 'react';
import { useFrameCallback, useSharedValue, type SharedValue } from 'react-native-reanimated';
import type { ChartEngine } from './engine/useChartEngine';

/**
 * A clock for things drawn over the chart.
 *
 * On the web this was a requestAnimationFrame loop that moved DOM overlays.
 * Here every overlay is already redrawn by the UI thread whenever the
 * viewport moves, so the only thing left that changes on its own is time
 * itself: the expiry guide sits at "now plus the duration", and an order
 * preview slides with the clock until it is committed.
 *
 * The clock runs on the UI thread and only writes when the new instant would
 * actually move something - by half a pixel at the current zoom, or to a new
 * second for the labels - so a still chart costs a comparison a frame and
 * redraws a few times a second at most. Performance mode halves the rate.
 */
export function useFrameClock(
  engine: Pick<ChartEngine, 'view' | 'series'>,
  {
    enabled = true,
    maxFps = 60,
    continuous = false,
  }: {
    enabled?: boolean;
    maxFps?: number;
    /** Write every frame: something is animating against the clock (a fade, a pulse). */
    continuous?: boolean;
  } = {},
): SharedValue<number> {
  const now = useSharedValue(Date.now());
  const lastRun = useSharedValue(0);
  const { view, series } = engine;

  // A frame's timestamp jitters around 1000/60, so a strict comparison with
  // the budget would drop every other frame and quietly halve the rate. The
  // two milliseconds of slack only matter when the budget is the display's.
  const minInterval = 1000 / Math.max(1, maxFps) - 2;

  const tick = useCallback(
    (frame: { timestamp: number }) => {
      'worklet';
      if (frame.timestamp - lastRun.value < minInterval) return;
      lastRun.value = frame.timestamp;
      const time = Date.now();
      const previous = now.value;
      const s = view.value.s;
      const bucket = series.value.bucket;
      // How long it takes the clock to move half a pixel at this zoom.
      const halfPixelMs = s > 0 && bucket > 0 ? (bucket / s) * 0.5 : 1000;
      const newSecond = Math.floor(time / 1000) !== Math.floor(previous / 1000);
      if (continuous || newSecond || time - previous >= halfPixelMs) now.value = time;
    },
    [minInterval, continuous, lastRun, now, view, series],
  );
  const callback = useFrameCallback(tick, false);

  useEffect(() => {
    callback.setActive(enabled);
    if (enabled) now.value = Date.now();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [enabled]);

  return now;
}

/**
 * Whether the view is sitting at the live edge.
 *
 * The right offset is the gap from the right edge to the newest bar, in bars,
 * and the resting offset is the gap the chart keeps there when it is following
 * real time. Equal means the trader has not panned away; a bar and a half of
 * slack absorbs the fractional drift a pan leaves behind, without treating a
 * deliberate scroll as if it were noise.
 */
export function isFollowingRealTime(engine: Pick<ChartEngine, 'isFollowing'>): boolean {
  return engine.isFollowing();
}

/** Keeps a value inside a range, tolerating an inverted one on a tiny plot. */
export function clamp(value: number, min: number, max: number): number {
  'worklet';
  if (max < min) return min;
  return value < min ? min : value > max ? max : value;
}
