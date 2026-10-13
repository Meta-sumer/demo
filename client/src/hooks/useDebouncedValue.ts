import { useEffect, useState } from 'react';

/**
 * `value`, once it has stopped changing for `delayMs`.
 *
 * For a request that follows typing - a quote for the amount being entered -
 * so it is asked for once the customer pauses, not once per keystroke.
 */
export function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const timer = window.setTimeout(() => setSettled(value), delayMs);
    return () => window.clearTimeout(timer);
  }, [value, delayMs]);
  return settled;
}
