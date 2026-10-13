import { useEffect, useRef } from 'react';
import clsx from 'clsx';
import { Loader2 } from 'lucide-react';
import { usePanelTransition } from '../../hooks/usePanelTransition';
import { formatPrice } from '../../utils/format';
import { ContractInfo, type ContractSummary } from './ContractInfo';
import type { Direction } from '../../types/api';

/**
 * Confirmation (brief UX-11).
 *
 * The last look before money moves: the same contract information the ticket
 * shows, now with the direction filled in, and exactly two ways out - "Confirm
 * Trade" and "Edit". Nothing else is worded as an action.
 *
 * A bottom sheet on a phone and a dialog beside the ticket on a desktop. The
 * veil is light and unblurred on purpose: the order preview is on the chart
 * behind it, and seeing where the trade would sit is half of confirming it.
 *
 * The sheet does not send anything itself. It reports Confirm and Edit to the
 * ticket, which owns the one mutation path - fresh idempotency key, the price
 * on screen at the moment of the press, and the re-quote when the price moved.
 */
const TRANSITION_MS = 260;
const EASE = 'ease-[cubic-bezier(0.32,0.72,0,1)]';

interface Props {
  direction: Direction | null;
  summary: ContractSummary;
  /** A request is in flight. */
  pending: boolean;
  /** Why the trade cannot be sent right now (market closed, prices disconnected...). */
  blockedReason: string | null;
  /** The server refused because the price moved; Confirm will use this price. */
  reQuotePrice: number | null;
  pricePrecision: number;
  /** A refusal other than a price move, in the server's words. */
  error: string | null;
  dontAskAgain: boolean;
  onDontAskAgainChange: (value: boolean) => void;
  onConfirm: () => void;
  onEdit: () => void;
}

export function ConfirmTradeSheet({
  direction,
  summary,
  pending,
  blockedReason,
  reQuotePrice,
  pricePrecision,
  error,
  dontAskAgain,
  onDontAskAgainChange,
  onConfirm,
  onEdit,
}: Props) {
  const open = direction !== null;
  const { mounted, entered } = usePanelTransition(open, TRANSITION_MS);
  const panelRef = useRef<HTMLDivElement | null>(null);
  // The last direction shown, so the sheet does not go blank while it slides out.
  const shownRef = useRef<Direction>('UP');
  if (direction) shownRef.current = direction;
  const shown = shownRef.current;

  useEffect(() => {
    if (!open) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !pending) onEdit();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open, pending, onEdit]);

  // The dialog takes focus, not the Confirm button: a stray Enter must not
  // place a trade the trader has not read.
  useEffect(() => {
    if (entered) panelRef.current?.focus();
  }, [entered]);

  if (!mounted) return null;

  const up = shown === 'UP';

  return (
    <div
      // Below lg the bottom navigation is on screen; the sheet rests on top of
      // it rather than over it, so the Confirm button is never under a nav tab.
      className="fixed inset-0 z-[60] flex items-end justify-center pb-[calc(3.5rem+env(safe-area-inset-bottom))] sm:items-center sm:pb-0 xl:justify-end xl:pr-8"
      role="presentation"
    >
      <button
        type="button"
        aria-label="Edit trade"
        tabIndex={-1}
        onClick={() => (pending ? undefined : onEdit())}
        className={clsx('absolute inset-0 bg-scrim/30 transition-opacity', EASE, entered ? 'opacity-100' : 'opacity-0')}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
      />

      <div
        ref={panelRef}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby="confirm-trade-title"
        className={clsx(
          'relative flex max-h-[92svh] w-full flex-col overflow-y-auto rounded-t-2xl border border-surface-700',
          'bg-surface-900 shadow-overlay outline-none sm:w-[380px] sm:rounded-2xl',
          'transition-[transform,opacity]',
          EASE,
          entered ? 'translate-y-0 opacity-100' : 'translate-y-full opacity-0 sm:translate-y-6',
        )}
        style={{ transitionDuration: TRANSITION_MS + 'ms' }}
      >
        <div className="mx-auto mt-2 h-1 w-10 shrink-0 rounded-full bg-surface-600 sm:hidden" />

        <div className="px-4 pb-4 pt-3 sm:px-5 sm:pt-4">
          <h2 id="confirm-trade-title" className="text-base font-bold text-content-100">
            Confirm your{' '}
            <span className={up ? 'text-up' : 'text-down'}>{shown}</span> trade
          </h2>
          <p className="mt-0.5 text-xs text-content-400">
            {up
              ? 'You win if the price at expiry is above the entry price.'
              : 'You win if the price at expiry is below the entry price.'}
          </p>

          <div className="mt-3">
            <ContractInfo summary={{ ...summary, direction: shown }} variant="confirm" />
          </div>

          {reQuotePrice !== null ? (
            <div className="mt-3 rounded-lg border border-warn/40 bg-warn/10 px-3 py-2" role="alert">
              <p className="text-xs font-semibold text-warn">The price moved before your order landed</p>
              <p className="mt-0.5 text-xs text-content-300">
                Nothing was placed. {shown} is now at{' '}
                <span className="tnum font-bold text-content-100">{formatPrice(reQuotePrice, pricePrecision)}</span>.
                Confirm Trade places it at this price.
              </p>
            </div>
          ) : null}

          {error ? (
            <p className="mt-3 rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-xs text-down" role="alert">
              Trade not placed: {error}
            </p>
          ) : null}

          {blockedReason ? (
            <p className="mt-3 rounded-lg border border-down/30 bg-down/10 px-3 py-2 text-xs font-medium text-down">
              {blockedReason}
            </p>
          ) : null}

          <label className="mt-3 flex cursor-pointer items-center gap-2 text-xs text-content-300">
            <input
              type="checkbox"
              className="h-4 w-4 accent-[rgb(var(--accent))]"
              checked={dontAskAgain}
              onChange={(event) => onDontAskAgainChange(event.target.checked)}
            />
            Don't ask again (one-tap trading)
          </label>

          <div className="mt-4 grid grid-cols-[1fr_auto] gap-2">
            <button
              type="button"
              onClick={onConfirm}
              disabled={pending || !!blockedReason}
              className={clsx(
                'flex h-12 items-center justify-center gap-2 rounded-xl text-sm font-bold text-white transition',
                'hover:brightness-110 active:scale-[0.99] disabled:cursor-not-allowed disabled:opacity-40',
                up ? 'bg-up-solid' : 'bg-down-solid',
              )}
            >
              {pending ? <Loader2 className="h-4 w-4 animate-spin" /> : null}
              Confirm Trade
            </button>
            <button
              type="button"
              onClick={onEdit}
              disabled={pending}
              className="h-12 rounded-xl border border-surface-700 bg-surface-850 px-5 text-sm font-semibold text-content-200 transition hover:text-content-100 disabled:opacity-40"
            >
              Edit
            </button>
          </div>
        </div>
      </div>
    </div>
  );
}
