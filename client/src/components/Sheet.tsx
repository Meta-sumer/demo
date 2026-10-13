import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type KeyboardEvent as ReactKeyboardEvent,
  type PointerEvent as ReactPointerEvent,
  type ReactNode,
  type RefObject,
} from 'react';
import clsx from 'clsx';
import { X } from 'lucide-react';
import { usePanelTransition } from '../hooks/usePanelTransition';
import { useMediaQuery } from '../features/trade/useMediaQuery';
import { useWorkspaceStore } from '../stores/workspaceStore';

/**
 * A panel of help or choices that opens over the trading screen without
 * taking it away.
 *
 * On a phone it is a bottom sheet, like the market selector: it rises from
 * where the thumb already is, keeps the top of the chart in view, and goes back
 * down with a drag, a tap on the veil or Escape. It is modal there - there is
 * no room to use anything else while it is up.
 *
 * On a wide screen there is room, so it is not modal and has no veil:
 *
 *   - `side`: a panel down the left of the workspace, beside the rail (and the
 *     settings drawer, when that is open), like Olymptrade's education panel -
 *     the chart and the ticket stay in view and usable while it is read;
 *   - `popover`: a small panel beside the control that opened it. When the
 *     ticket sits at the right edge it opens to the ticket's left, over the
 *     chart, like Olymptrade's duration picker; otherwise under the control.
 *     A press anywhere else closes it.
 *
 * Escape closes every variant, and focus goes back to whatever opened it.
 */
const TRANSITION_MS = 280;
const EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]';
/** How far the sheet has to be dragged down before letting go closes it. */
const DISMISS_DRAG_PX = 90;
const MARGIN = 8;
/** Clear of the 60px navigation rail. */
const RAIL_EDGE = 68;

interface Props {
  open: boolean;
  onClose: () => void;
  title: string;
  /** A line under the title. */
  subtitle?: ReactNode;
  variant: 'side' | 'popover';
  /** Popover: the control it belongs to. */
  anchorRef?: RefObject<HTMLElement | null>;
  /** Popover: what to open beside rather than over (the ticket). */
  besideRef?: RefObject<HTMLElement | null>;
  /** Where focus goes on opening; the panel itself when absent. */
  initialFocusRef?: RefObject<HTMLElement | null>;
  /** Between the title and the scrolling body, e.g. a switch. */
  toolbar?: ReactNode;
  /** Popover width on a wide screen, px. */
  popoverWidth?: number;
  children: ReactNode;
}

export function Sheet({
  open,
  onClose,
  title,
  subtitle,
  variant,
  anchorRef,
  besideRef,
  initialFocusRef,
  toolbar,
  popoverWidth = 320,
  children,
}: Props) {
  const wide = useMediaQuery('(min-width: 1024px)');
  const settingsOpen = useWorkspaceStore((s) => s.settingsOpen);
  const { mounted, entered } = usePanelTransition(open, TRANSITION_MS);
  const panelRef = useRef<HTMLDivElement>(null);
  const returnFocusRef = useRef<HTMLElement | null>(null);
  const [drag, setDrag] = useState<{ startY: number; dy: number } | null>(null);
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null);
  const titleId = useId();
  const modal = !wide;
  const popover = wide && variant === 'popover';
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;

  // Remember what opened it and give focus back there on the way out, so a
  // keyboard user is not dropped at the top of the document.
  useEffect(() => {
    if (open) {
      returnFocusRef.current = document.activeElement as HTMLElement | null;
      return;
    }
    const target = returnFocusRef.current;
    returnFocusRef.current = null;
    if (target && document.contains(target)) target.focus({ preventScroll: true });
  }, [open]);

  useEffect(() => {
    if (!entered) return;
    (initialFocusRef?.current ?? panelRef.current)?.focus({ preventScroll: true });
  }, [entered, initialFocusRef]);

  // A popover is a passing choice: a press outside it - on the chart, on
  // another control - means the trader has moved on.
  useEffect(() => {
    if (!open || !popover) return;
    const onPointerDown = (event: PointerEvent) => {
      const target = event.target as Node;
      if (panelRef.current?.contains(target) || anchorRef?.current?.contains(target)) return;
      onCloseRef.current();
    };
    document.addEventListener('pointerdown', onPointerDown);
    return () => document.removeEventListener('pointerdown', onPointerDown);
  }, [open, popover, anchorRef]);

  // Beside the ticket when there is room to its left, else under the control.
  // Measured before paint, and again as the page scrolls or resizes.
  useLayoutEffect(() => {
    if (!mounted || !popover) {
      setPosition(null);
      return;
    }
    let frame = 0;
    const place = () => {
      const panel = panelRef.current;
      const anchor = anchorRef?.current?.getBoundingClientRect();
      if (!panel || !anchor) return;
      const beside = besideRef?.current?.getBoundingClientRect();
      const width = panel.offsetWidth;
      const height = panel.offsetHeight;
      const viewportWidth = document.documentElement.clientWidth;
      const viewportHeight = window.innerHeight;
      const clampTop = (top: number) => Math.max(MARGIN, Math.min(top, viewportHeight - height - MARGIN));
      if (beside && beside.left - width - MARGIN >= RAIL_EDGE) {
        setPosition({ left: beside.left - width - MARGIN, top: clampTop(anchor.top - 12) });
        return;
      }
      const left = Math.max(MARGIN, Math.min(anchor.right - width, viewportWidth - width - MARGIN));
      const below = anchor.bottom + 6;
      setPosition({
        left,
        top: below + height <= viewportHeight - MARGIN ? below : clampTop(anchor.top - 6 - height),
      });
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
  }, [mounted, popover, anchorRef, besideRef]);

  if (!mounted) return null;

  /** Escape closes; on a phone, Tab cycles inside the sheet while it is up. */
  function onKeyDown(event: ReactKeyboardEvent<HTMLDivElement>) {
    if (event.key === 'Escape') {
      event.stopPropagation();
      onClose();
      return;
    }
    if (!modal || event.key !== 'Tab' || !panelRef.current) return;
    const focusable = Array.from(
      panelRef.current.querySelectorAll<HTMLElement>(
        'button:not([disabled]), input:not([disabled]), select:not([disabled]), [href], [tabindex]:not([tabindex="-1"])',
      ),
    );
    if (focusable.length === 0) return;
    const first = focusable[0]!;
    const last = focusable[focusable.length - 1]!;
    if (event.shiftKey && (document.activeElement === first || document.activeElement === panelRef.current)) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  }

  function onHandleDown(event: ReactPointerEvent<HTMLDivElement>) {
    event.currentTarget.setPointerCapture(event.pointerId);
    setDrag({ startY: event.clientY, dy: 0 });
  }
  function onHandleMove(event: ReactPointerEvent<HTMLDivElement>) {
    if (!drag) return;
    setDrag({ ...drag, dy: Math.max(0, event.clientY - drag.startY) });
  }
  function onHandleUp() {
    if (drag && drag.dy > DISMISS_DRAG_PX) onClose();
    setDrag(null);
  }

  return (
    <>
      {modal ? (
        <button
          type="button"
          aria-label={'Close ' + title}
          tabIndex={-1}
          onClick={onClose}
          className={clsx(
            'fixed inset-0 z-[55] bg-scrim/60 backdrop-blur-[2px] transition-opacity',
            EASE,
            entered ? 'opacity-100' : 'opacity-0',
          )}
          style={{ transitionDuration: TRANSITION_MS + 'ms' }}
        />
      ) : null}

      <div
        ref={panelRef}
        role="dialog"
        aria-modal={modal}
        aria-labelledby={titleId}
        tabIndex={-1}
        onKeyDown={onKeyDown}
        className={clsx(
          'fixed z-[56] flex flex-col overflow-hidden border border-surface-700 bg-surface-900 shadow-overlay outline-none',
          modal
            ? 'inset-x-0 bottom-0 max-h-[85svh] rounded-t-2xl pb-[env(safe-area-inset-bottom)]'
            : popover
              ? 'max-h-[min(560px,calc(100vh-16px))] rounded-xl'
              : clsx('bottom-2 top-[72px] w-[380px] rounded-xl', settingsOpen ? 'left-[398px]' : 'left-[68px]'),
          !drag && 'transition-[transform,opacity]',
          EASE,
          entered
            ? 'translate-x-0 translate-y-0 opacity-100'
            : modal
              ? 'translate-y-full opacity-0'
              : popover
                ? '-translate-y-1 opacity-0'
                : '-translate-x-3 opacity-0',
        )}
        style={{
          transitionDuration: TRANSITION_MS + 'ms',
          transform: modal && drag && drag.dy > 0 ? `translateY(${drag.dy}px)` : undefined,
          ...(popover
            ? {
                width: popoverWidth,
                left: position?.left ?? 0,
                top: position?.top ?? 0,
                visibility: position ? 'visible' : 'hidden',
              }
            : {}),
        }}
      >
        {modal ? (
          // The whole strip is the grab area, not just the pill.
          <div
            className="flex shrink-0 cursor-grab touch-none justify-center pb-1 pt-2.5"
            onPointerDown={onHandleDown}
            onPointerMove={onHandleMove}
            onPointerUp={onHandleUp}
            onPointerCancel={() => setDrag(null)}
            aria-hidden="true"
          >
            <span className="h-1 w-10 rounded-full bg-surface-600" />
          </div>
        ) : null}

        <div className={clsx('flex shrink-0 items-start gap-3 px-4 pb-2', modal ? 'pt-1' : 'pt-3')}>
          <div className="min-w-0 flex-1">
            <h2 id={titleId} className="text-base font-bold text-content-100">
              {title}
            </h2>
            {subtitle ? <p className="mt-0.5 text-xs text-content-400">{subtitle}</p> : null}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={'Close ' + title}
            className="-mr-1.5 shrink-0 rounded-lg p-1.5 text-content-400 transition hover:bg-surface-850 hover:text-content-100"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        {toolbar ? <div className="shrink-0 px-4 pb-3">{toolbar}</div> : null}

        <div className="min-h-0 flex-1 overflow-y-auto overscroll-contain">{children}</div>
      </div>
    </>
  );
}
