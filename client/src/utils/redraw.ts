/**
 * Redraw pacing for the chart.
 *
 * Performance mode coalesces the passes that would otherwise run on every
 * incoming tick - the indicator recompute walks the whole visible series, so at
 * a few ticks a second it dominates the frame budget on a slow machine.
 *
 * What it must never do is delay something the trader just did. Toggling an
 * indicator or opening a position has to land immediately; only a repeat of the
 * same work, caused by nothing but another tick, is worth skipping.
 */
export interface RedrawState {
  at: number;
  key: string;
}

/** How long performance mode coalesces repeated tick-driven redraws. */
export const PERFORMANCE_REDRAW_MS = 1000;

/**
 * Should a tick-driven pass run now?
 *
 * Yes whenever `key` differs from the last run: that means something other than
 * a tick changed, and holding it back would make the UI feel broken. A repeat
 * under the same key is throttled, and only when performance mode is on.
 */
export function shouldRun(
  ref: { current: RedrawState },
  key: string,
  performanceMode: boolean,
  now: number = Date.now(),
): boolean {
  const structuralChange = ref.current.key !== key;

  if (!structuralChange && performanceMode && now - ref.current.at < PERFORMANCE_REDRAW_MS) return false;

  ref.current = { at: now, key };
  return true;
}
