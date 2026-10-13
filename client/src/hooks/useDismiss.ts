import { useEffect, type RefObject } from 'react';

/**
 * Closes a transient overlay on an outside click or Escape.
 *
 * Every menu and popover on the platform owes the trader the same two exits,
 * and a panel that only closes by re-pressing the control that opened it
 * reads as stuck - especially over a chart, where the next click is usually
 * meant for the chart rather than for the menu.
 *
 * `pointerdown` rather than `click`: a click fires only once the pointer is
 * released, so a press that begins outside and drags - panning the chart from
 * under an open popover - would leave it open for the whole gesture. It also
 * covers touch and pen without a second listener.
 *
 * The listener is only attached while something is open, so a closed menu
 * costs nothing.
 */
export function useDismiss(open: boolean, ref: RefObject<HTMLElement | null>, onDismiss: () => void): void {
  useEffect(() => {
    if (!open) return;

    const onPointerDown = (event: PointerEvent) => {
      if (!ref.current?.contains(event.target as Node)) onDismiss();
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onDismiss();
    };

    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open, ref, onDismiss]);
}
