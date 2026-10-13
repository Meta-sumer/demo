import { useEffect, useRef } from 'react';
import type { IChartApi } from 'lightweight-charts';

/**
 * A requestAnimationFrame loop for things drawn over the chart.
 *
 * Why a loop rather than the chart's own events: an overlay has to stay welded
 * to an instant and a price, and both axes move for reasons the library does
 * not announce - the price scale autoscales on every tick and fires nothing.
 * Each consumer compares what it computed with what it last wrote and touches
 * the DOM only on a real change, so a still chart costs a few comparisons a
 * frame and no layout.
 *
 * The callback is held in a ref, so a new closure every render does not tear
 * the loop down and restart it.
 */
export function useFrameLoop(callback: (now: number) => void, { enabled = true, maxFps = 60 } = {}): void {
  const callbackRef = useRef(callback);
  callbackRef.current = callback;

  useEffect(() => {
    if (!enabled) return;

    let frame = 0;
    let lastRun = 0;
    // A frame's timestamp jitters around 1000/60, so a strict comparison with
    // the budget would drop every other frame and quietly halve the rate. The
    // two milliseconds of slack only matter when the budget is the display's.
    const minInterval = 1000 / Math.max(1, maxFps) - 2;

    const run = (timestamp: number) => {
      frame = window.requestAnimationFrame(run);
      if (timestamp - lastRun < minInterval) return;
      lastRun = timestamp;
      callbackRef.current(timestamp);
    };

    frame = window.requestAnimationFrame(run);
    return () => window.cancelAnimationFrame(frame);
  }, [enabled, maxFps]);
}

/**
 * Whether the view is sitting at the live edge.
 *
 * `scrollPosition` is the gap from the right edge to the newest bar, measured
 * in bars, and `rightOffset` is the gap the chart keeps there when it is
 * following real time. Equal means the trader has not panned away; a bar and a
 * half of slack absorbs the fractional drift that a pan in progress leaves
 * behind, without treating a deliberate scroll as if it were noise.
 */
export function isFollowingRealTime(chart: IChartApi): boolean {
  const scale = chart.timeScale();
  return Math.abs(scale.scrollPosition() - scale.options().rightOffset) < 1.5;
}

/** Keeps a value inside a range, tolerating an inverted one on a tiny plot. */
export function clamp(value: number, min: number, max: number): number {
  if (max < min) return min;
  return value < min ? min : value > max ? max : value;
}
