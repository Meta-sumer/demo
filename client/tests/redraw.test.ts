import { describe, expect, it } from 'vitest';
import { PERFORMANCE_REDRAW_MS, shouldRun, type RedrawState } from '../src/utils/redraw';

/**
 * Performance mode may drop repeated work; it may never drop a response to
 * something the trader did. These separate the two cases, because a throttle
 * that swallows an indicator toggle reads as a broken control, not a fast one.
 */
function state(): { current: RedrawState } {
  return { current: { at: 0, key: '' } };
}

describe('shouldRun', () => {
  it('always runs when performance mode is off', () => {
    const ref = state();

    expect(shouldRun(ref, 'a', false, 1000)).toBe(true);
    expect(shouldRun(ref, 'a', false, 1001)).toBe(true);
    expect(shouldRun(ref, 'a', false, 1002)).toBe(true);
  });

  it('runs the first time even in performance mode', () => {
    expect(shouldRun(state(), 'a', true, 5000)).toBe(true);
  });

  it('skips a repeat of the same work inside the window', () => {
    const ref = state();
    shouldRun(ref, 'a', true, 5000);

    expect(shouldRun(ref, 'a', true, 5000 + PERFORMANCE_REDRAW_MS - 1)).toBe(false);
  });

  it('runs again once the window has passed', () => {
    const ref = state();
    shouldRun(ref, 'a', true, 5000);

    expect(shouldRun(ref, 'a', true, 5000 + PERFORMANCE_REDRAW_MS)).toBe(true);
  });

  it('never throttles a changed key, however soon it arrives', () => {
    const ref = state();
    shouldRun(ref, 'sma', true, 5000);

    // The trader just switched indicator sets; a 1ms-later skip would look
    // like the toggle did nothing.
    expect(shouldRun(ref, 'sma,ema', true, 5001)).toBe(true);
  });

  it('restarts the window from the run it allowed, not from the skip', () => {
    const ref = state();
    shouldRun(ref, 'a', true, 0);
    expect(shouldRun(ref, 'a', true, 500)).toBe(false);

    // A skipped call must not extend the window, so this still runs on time.
    expect(shouldRun(ref, 'a', true, PERFORMANCE_REDRAW_MS)).toBe(true);
  });

  it('lets a key change reset the window for subsequent repeats', () => {
    const ref = state();
    shouldRun(ref, 'a', true, 0);
    shouldRun(ref, 'b', true, 10);

    expect(shouldRun(ref, 'b', true, 500)).toBe(false);
    expect(shouldRun(ref, 'b', true, 10 + PERFORMANCE_REDRAW_MS)).toBe(true);
  });

  it('does not throttle after performance mode is turned off mid-stream', () => {
    const ref = state();
    shouldRun(ref, 'a', true, 0);

    expect(shouldRun(ref, 'a', false, 1)).toBe(true);
  });
});
