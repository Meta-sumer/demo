import { useEffect, useState } from 'react';

/**
 * Milliseconds remaining until `target`, ticking on an interval.
 *
 * Recomputed from the wall clock on every tick rather than decremented, so a
 * backgrounded tab that stops firing timers still shows the correct value the
 * moment it is foregrounded again.
 */
export function useCountdown(target: number | null | undefined, intervalMs = 250): number {
  const [remaining, setRemaining] = useState(() => (target ? Math.max(0, target - Date.now()) : 0));

  useEffect(() => {
    if (!target) {
      setRemaining(0);
      return;
    }
    setRemaining(Math.max(0, target - Date.now()));
    const timer = setInterval(() => {
      const next = Math.max(0, target - Date.now());
      setRemaining(next);
      if (next === 0) clearInterval(timer);
    }, intervalMs);
    return () => clearInterval(timer);
  }, [target, intervalMs]);

  return remaining;
}

/**
 * A `now` value that advances on an interval, for relative timestamps.
 *
 * Each tick lands just after a multiple of the interval on the wall clock
 * (a whole second for the default), not at an offset set by when the
 * component mounted. So every clock on screen turns over together: the
 * ticket's "Expires at" and the chart's expiry label, which is drawn per
 * frame, show the same second.
 */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined;
    const schedule = () => {
      timer = setTimeout(() => {
        setNow(Date.now());
        schedule();
      }, intervalMs - (Date.now() % intervalMs));
    };
    schedule();
    return () => clearTimeout(timer);
  }, [intervalMs]);
  return now;
}
