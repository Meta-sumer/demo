import { useSyncExternalStore } from 'react';

/**
 * Whether a media query matches, kept current as the window changes.
 *
 * For layout decisions CSS cannot make alone - mounting a second chart only
 * where there is room for it, rather than mounting it everywhere and hiding
 * it, which would still fetch its candles and subscribe to its prices.
 */
export function useMediaQuery(query: string): boolean {
  return useSyncExternalStore(
    (notify) => {
      const list = window.matchMedia(query);
      list.addEventListener('change', notify);
      return () => list.removeEventListener('change', notify);
    },
    () => window.matchMedia(query).matches,
    () => false,
  );
}
