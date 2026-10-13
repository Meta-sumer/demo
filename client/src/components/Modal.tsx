import { useEffect, useRef } from 'react';
import clsx from 'clsx';
import { X } from 'lucide-react';
import { usePanelTransition } from '../hooks/usePanelTransition';

/**
 * Overlay dialog.
 *
 * Rises from below rather than fading in place: the motion tells you where it
 * came from, so dismissing it feels like sending it back rather than deleting
 * it. Below `sm` it becomes a bottom sheet anchored to the edge it rose from.
 *
 * The page behind stays mounted - a deposit is something a trader starts while
 * watching a position, and losing the chart to a full page navigation is a
 * worse trade-off than the width this costs.
 */
const TRANSITION_MS = 340;
const EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]';

interface Props {
  open: boolean;
  onClose: () => void;
  title: string;
  /** Rendered under the title, above the divider. */
  subtitle?: string;
  /** Tailwind width for the panel at `sm` and above. */
  width?: string;
  children: React.ReactNode;
}

export function Modal({ open, onClose, title, subtitle, width = 'sm:w-[min(1060px,94vw)]', children }: Props) {
  const { mounted, entered } = usePanelTransition(open, TRANSITION_MS);
  const panelRef = useRef<HTMLDivElement | null>(null);

  // Escape closes, and the page behind must not scroll under the sheet.
  useEffect(() => {
    if (!open) return;

    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose();
    };
    document.addEventListener('keydown', onKeyDown);

    const previousOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';

    return () => {
      document.removeEventListener('keydown', onKeyDown);
      document.body.style.overflow = previousOverflow;
    };
  }, [open, onClose]);

  // Move focus into the dialog so the keyboard follows the eye.
  useEffect(() => {
    if (entered) panelRef.current?.focus();
  }, [entered]);

  if (!mounted) return null;

  return (
    <div className="fixed inset-0 z-[60] flex items-end justify-center sm:items-center" role="presentation">
      <button
        type="button"
        aria-label={'Close ' + title}
        onClick={onClose}
        className={clsx(
          'absolute inset-0 bg-scrim/70 backdrop-blur-sm transition-opacity',
          EASE,
          entered ? 'opacity-100' : 'opacity-0',
        )}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
      />

      <div
        ref={panelRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        className={clsx(
          'relative flex max-h-[92svh] w-full flex-col rounded-t-2xl border border-surface-700 bg-surface-900',
          'shadow-overlay outline-none sm:max-h-[88svh] sm:rounded-2xl',
          width,
          'transition-[transform,opacity]',
          EASE,
          entered
            ? 'translate-y-0 opacity-100 sm:scale-100'
            : 'translate-y-full opacity-0 sm:translate-y-8 sm:scale-[0.98]',
        )}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
      >
        {/* Grab handle: on a phone this is the affordance that says "drag me
            down", even though dismissal is by tap. */}
        <div className="mx-auto mt-2 h-1 w-10 shrink-0 rounded-full bg-surface-600 sm:hidden" />

        <div className="flex shrink-0 items-start gap-3 px-5 pb-4 pt-4 sm:px-6 sm:pt-5">
          <div className="min-w-0 flex-1">
            <h2 className="text-lg font-bold text-content-100 sm:text-xl">{title}</h2>
            {subtitle ? <p className="mt-0.5 text-xs text-content-400">{subtitle}</p> : null}
          </div>
          <button
            type="button"
            onClick={onClose}
            aria-label={'Close ' + title}
            className="-mr-1 shrink-0 rounded-lg p-1.5 text-content-400 transition hover:bg-surface-850 hover:text-content-100"
          >
            <X className="h-5 w-5" />
          </button>
        </div>

        <div className="mx-5 shrink-0 border-t border-dashed border-surface-700 sm:mx-6" />

        <div className="min-h-0 flex-1 overflow-y-auto p-5 sm:p-6">{children}</div>
      </div>
    </div>
  );
}
