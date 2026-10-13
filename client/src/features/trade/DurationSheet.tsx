import { useRef, useState, type RefObject } from 'react';
import clsx from 'clsx';
import { Sheet } from '../../components/Sheet';
import { useNow } from '../../hooks/useCountdown';
import { useClockFormatter } from './tradeBits';
import type { DurationChip } from './ticketModel';

/**
 * Every duration an asset offers (reference record 10).
 *
 * The ticket's row carries four chips; this is behind its "More" chip. Each
 * option shows its payout, because the payout is part of the choice - a longer
 * duration usually pays differently. The "Duration | Ends at" switch is
 * Quotex's SWITCH TIME: the same options written as the clock time the trade
 * would end if it were accepted now, ticking with the clock, for the trader
 * who thinks "until 14:30" rather than "for 15 minutes". It changes how the
 * options are written, never what they are: the trade still runs for the
 * duration from the moment the platform accepts it.
 *
 * A popover beside the ticket on a wide screen and a bottom sheet on a phone.
 */

type Mode = 'duration' | 'endsAt';

/** Remembered while the page is open: a trader who reads end times keeps them. */
let lastMode: Mode = 'duration';

interface Props {
  open: boolean;
  onClose: () => void;
  chips: DurationChip[];
  selected: number;
  onSelect: (seconds: number) => void;
  /** The More chip. */
  anchorRef: RefObject<HTMLElement | null>;
  /** The ticket, so a wide screen opens this beside it rather than over it. */
  besideRef: RefObject<HTMLElement | null>;
}

export function DurationSheet({ open, onClose, chips, selected, onSelect, anchorRef, besideRef }: Props) {
  const [mode, setModeState] = useState<Mode>(lastMode);
  const selectedRef = useRef<HTMLButtonElement>(null);

  function setMode(next: Mode) {
    lastMode = next;
    setModeState(next);
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="Duration"
      subtitle="Every duration this market offers, with its payout."
      variant="popover"
      anchorRef={anchorRef}
      besideRef={besideRef}
      initialFocusRef={selectedRef}
      popoverWidth={340}
      toolbar={
        <div role="group" aria-label="Show as" className="grid grid-cols-2 rounded-lg bg-surface-850 p-0.5 text-xs font-semibold">
          {(
            [
              ['duration', 'Duration'],
              ['endsAt', 'Ends at'],
            ] as const
          ).map(([value, label]) => (
            <button
              key={value}
              type="button"
              aria-pressed={mode === value}
              onClick={() => setMode(value)}
              className={clsx(
                'rounded-md px-3 py-1.5 transition',
                mode === value ? 'bg-surface-700 text-content-100 shadow-raised' : 'text-content-400 hover:text-content-200',
              )}
            >
              {label}
            </button>
          ))}
        </div>
      }
    >
      <Options
        chips={chips}
        selected={selected}
        mode={mode}
        selectedRef={selectedRef}
        onSelect={(seconds) => {
          onSelect(seconds);
          onClose();
        }}
      />
    </Sheet>
  );
}

/**
 * The grid itself. Mounted only while the sheet is, so the one-second clock
 * the end times need is not running behind a closed sheet.
 */
function Options({
  chips,
  selected,
  mode,
  selectedRef,
  onSelect,
}: {
  chips: DurationChip[];
  selected: number;
  mode: Mode;
  selectedRef: RefObject<HTMLButtonElement>;
  onSelect: (seconds: number) => void;
}) {
  const now = useNow(1000);
  const clock = useClockFormatter();
  const endsAt = mode === 'endsAt';

  return (
    <div className="px-4 pb-4">
      <ul className="grid grid-cols-3 gap-1.5">
        {chips.map((chip) => {
          const isSelected = chip.seconds === selected;
          const unavailable = chip.unavailableReason !== null;
          return (
            <li key={chip.seconds}>
              <button
                ref={isSelected ? selectedRef : undefined}
                type="button"
                disabled={unavailable}
                aria-pressed={isSelected}
                title={chip.unavailableReason ?? undefined}
                onClick={() => onSelect(chip.seconds)}
                className={clsx(
                  'flex w-full flex-col items-center rounded-lg border px-1 py-2 transition',
                  isSelected
                    ? 'border-accent bg-accent/15 text-content-100'
                    : 'border-surface-700 bg-surface-850 text-content-200 hover:border-surface-600',
                  'disabled:cursor-not-allowed disabled:opacity-40 disabled:hover:border-surface-700',
                )}
              >
                <span className="text-sm font-bold tnum leading-tight">
                  {endsAt ? clock(now + chip.seconds * 1000) : chip.label}
                </span>
                <span
                  className={clsx(
                    'mt-0.5 text-[10px] font-semibold tnum leading-tight',
                    unavailable ? 'text-content-400' : isSelected ? 'text-accent' : 'text-up',
                  )}
                >
                  {endsAt ? <span className="font-medium text-content-400">{chip.label} · </span> : null}
                  {chip.payoutPercent !== null ? chip.payoutPercent + '%' : 'N/A'}
                  <span className="sr-only"> payout</span>
                </span>
              </button>
            </li>
          );
        })}
      </ul>
      <p className="mt-3 text-[11px] leading-snug text-content-400">
        {endsAt
          ? 'Counted from now. The exact expiry is set when your trade is accepted.'
          : 'The payout is fixed when your trade is accepted.'}
      </p>
    </div>
  );
}
