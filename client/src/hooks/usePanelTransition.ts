import { useEffect, useState } from 'react';

/**
 * Keeps a panel mounted while it animates out.
 *
 * Unmounting on the same render the flag flips would skip the exit animation
 * entirely - the element is simply gone - so `mounted` trails `open` by the
 * transition's duration.
 *
 * `entered` flips one paint after mounting rather than immediately, because a
 * transition needs a "from" style the browser has actually rendered. Setting
 * both in the same commit gives it nothing to move away from and the panel
 * appears fully open.
 *
 * Under `prefers-reduced-motion` the stylesheet collapses every transition to
 * ~0ms, so the panel snaps; the trailing unmount stays but is invisible.
 */
export function usePanelTransition(open: boolean, durationMs: number): { mounted: boolean; entered: boolean } {
  const [mounted, setMounted] = useState(open);
  const [entered, setEntered] = useState(open);

  useEffect(() => {
    if (!open) {
      setEntered(false);
      const timer = window.setTimeout(() => setMounted(false), durationMs);
      return () => window.clearTimeout(timer);
    }

    setMounted(true);

    // Two frames: the first lets React commit the closed state, the second lets
    // the browser paint it before the class change starts the transition.
    let inner = 0;
    const outer = window.requestAnimationFrame(() => {
      inner = window.requestAnimationFrame(() => setEntered(true));
    });

    return () => {
      window.cancelAnimationFrame(outer);
      window.cancelAnimationFrame(inner);
    };
  }, [open, durationMs]);

  return { mounted, entered };
}
