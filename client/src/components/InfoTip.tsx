import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { createPortal } from 'react-dom';
import clsx from 'clsx';
import { CircleHelp } from 'lucide-react';

/**
 * A "?" beside a term that explains it (brief UX-09 and UX-18, reference
 * records 12 and 19 - Olymptrade, Pocket Option and Quotex put one beside
 * their payout terms).
 *
 * A toggletip, not a tooltip. It opens on a tap, a click, or Enter/Space on
 * the focused button - never on hover alone, because a phone has no hover and
 * an explanation that exists only for a mouse is one most traders never see.
 * It closes on a second press, a tap anywhere else, or Escape, and only one is
 * open at a time.
 *
 * The words are written into a polite live region beside the button as it
 * opens, so a screen reader says them without the reader having to go looking.
 * The bubble itself is drawn in a portal: the terms it explains sit inside
 * scrolling panels and dialogs, and a bubble drawn in place would be clipped
 * by whichever of them is narrowest.
 */

/** The open tip's close, so opening another one shuts it. */
let closeOpenTip: (() => void) | null = null;

const MARGIN = 8;
const GAP = 6;

interface Props {
  /** The term being explained, for the button's name: "About Payout". */
  label: string;
  children: ReactNode;
  className?: string;
}

export function InfoTip({ label, children, className }: Props) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const bubbleRef = useRef<HTMLDivElement>(null);
  const regionId = useId();
  const close = useCallback(() => setOpen(false), []);

  useEffect(() => {
    if (!open) return;
    if (closeOpenTip && closeOpenTip !== close) closeOpenTip();
    closeOpenTip = close;
    return () => {
      if (closeOpenTip === close) closeOpenTip = null;
    };
  }, [open, close]);

  // A press anywhere but the button or the bubble closes it. `pointerdown`,
  // so the press that starts a scroll or a drag on the chart closes it too.
  // Escape is also caught here for when focus is somewhere else entirely.
  useEffect(() => {
    if (!open) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (buttonRef.current?.contains(target) || bubbleRef.current?.contains(target)) return;
      setOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setOpen(false);
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  // Under the button, or above it when there is no room below; centred on it
  // but kept inside the viewport. Measured before paint so it never flashes
  // at the corner, and re-measured as anything around it scrolls.
  useLayoutEffect(() => {
    if (!open) {
      setPosition(null);
      return;
    }
    let frame = 0;
    const place = () => {
      const button = buttonRef.current;
      const bubble = bubbleRef.current;
      if (!button || !bubble) return;
      const anchor = button.getBoundingClientRect();
      const viewportWidth = document.documentElement.clientWidth;
      const viewportHeight = window.innerHeight;
      // Scrolled out of sight: an explanation pointing at nothing is noise.
      if (anchor.bottom < 0 || anchor.top > viewportHeight) {
        setOpen(false);
        return;
      }
      const width = bubble.offsetWidth;
      const height = bubble.offsetHeight;
      const left = Math.max(
        MARGIN,
        Math.min(anchor.left + anchor.width / 2 - width / 2, viewportWidth - width - MARGIN),
      );
      const below = anchor.bottom + GAP;
      const top = below + height <= viewportHeight - MARGIN ? below : Math.max(MARGIN, anchor.top - GAP - height);
      setPosition({ left, top });
    };
    const schedule = () => {
      cancelAnimationFrame(frame);
      frame = requestAnimationFrame(place);
    };
    place();
    window.addEventListener('scroll', schedule, true);
    window.addEventListener('resize', schedule);
    return () => {
      cancelAnimationFrame(frame);
      window.removeEventListener('scroll', schedule, true);
      window.removeEventListener('resize', schedule);
    };
  }, [open]);

  return (
    <span className={clsx('inline-flex align-middle', className)}>
      <button
        ref={buttonRef}
        type="button"
        aria-expanded={open}
        aria-controls={regionId}
        aria-label={'About ' + label}
        onClick={() => setOpen((value) => !value)}
        onKeyDown={(event) => {
          // Handled here first, so an open tip inside a dialog closes on its
          // own - the dialog's Escape listener sits further out.
          if (event.key === 'Escape' && open) {
            event.stopPropagation();
            setOpen(false);
          }
        }}
        className={clsx(
          // 24px to press, without making the row it sits in any taller.
          '-my-1 grid h-6 w-6 shrink-0 place-items-center rounded-full transition',
          open ? 'text-accent' : 'text-content-400 hover:text-content-100',
        )}
      >
        <CircleHelp className="h-3.5 w-3.5" aria-hidden />
      </button>
      <span id={regionId} role="status" className="sr-only">
        {open ? children : null}
      </span>
      {open
        ? createPortal(
            <div
              ref={bubbleRef}
              aria-hidden
              className={clsx(
                'fixed z-[80] w-max max-w-[min(17rem,calc(100vw-1rem))] rounded-lg border border-surface-700 bg-surface-800',
                'px-3 py-2 text-left text-[12px] font-normal normal-case leading-snug tracking-normal text-content-200 shadow-overlay',
                'animate-fade-in',
              )}
              style={{
                left: position?.left ?? 0,
                top: position?.top ?? 0,
                visibility: position ? 'visible' : 'hidden',
              }}
            >
              {children}
            </div>,
            document.body,
          )
        : null}
    </span>
  );
}
