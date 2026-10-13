import clsx from 'clsx';
import { useAccountStore } from '../stores/accountStore';
import { formatMoney } from '../utils/format';
import type { AccountType } from '../types/api';

/**
 * Demo / Live switch.
 *
 * Both options carry their own balance, so the choice is made looking at the
 * money it affects. Demo is marked in the warning tone everywhere it appears -
 * the one thing a customer must never be unsure of is whether a trade is
 * spending real money (brief UX-16).
 *
 * Used in the balance menu and on Home; the account store is the single
 * source, so both move together.
 */
const OPTIONS: Array<{ value: AccountType; label: string; hint: string }> = [
  { value: 'DEMO', label: 'Demo', hint: 'Virtual money' },
  { value: 'LIVE', label: 'Live', hint: 'Real money' },
];

export function AccountSwitch({ className }: { className?: string }) {
  const active = useAccountStore((s) => s.active);
  const wallets = useAccountStore((s) => s.wallets);
  const setActive = useAccountStore((s) => s.setActive);

  return (
    <div role="radiogroup" aria-label="Trading account" className={clsx('grid grid-cols-2 gap-1.5', className)}>
      {OPTIONS.map((option) => {
        const wallet = wallets[option.value];
        const selected = active === option.value;
        // A server without demo accounts returns only the live wallet.
        const available = !!wallet || option.value === active;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            disabled={!available}
            onClick={() => setActive(option.value)}
            className={clsx(
              'rounded-lg border px-3 py-2 text-left transition disabled:cursor-not-allowed disabled:opacity-45',
              selected
                ? option.value === 'DEMO'
                  ? 'border-warn/60 bg-warn/10'
                  : 'border-accent/60 bg-accent/10'
                : 'border-surface-700 bg-surface-850 hover:border-surface-600',
            )}
          >
            <span className="flex items-center gap-1.5">
              <span
                className={clsx(
                  'h-2 w-2 shrink-0 rounded-full',
                  option.value === 'DEMO' ? 'bg-warn' : 'bg-up',
                  !selected && 'opacity-50',
                )}
              />
              <span
                className={clsx(
                  'text-[11px] font-bold uppercase tracking-wider',
                  selected ? (option.value === 'DEMO' ? 'text-warn' : 'text-content-100') : 'text-content-300',
                )}
              >
                {option.label}
              </span>
            </span>
            <span className="mt-0.5 block truncate text-sm font-bold tnum text-content-100">
              {wallet ? formatMoney(wallet.balance, wallet.currency) : available ? '--' : 'Unavailable'}
            </span>
            <span className="block truncate text-[10px] text-content-400">{option.hint}</span>
          </button>
        );
      })}
    </div>
  );
}

/** "DEMO · virtual" / "LIVE" - the marker shown wherever a balance is. */
export function AccountMarker({ account, className }: { account: AccountType; className?: string }) {
  return account === 'DEMO' ? (
    <span
      className={clsx(
        'inline-flex items-center gap-1 rounded px-1.5 py-px text-[9px] font-bold uppercase tracking-wider',
        'bg-warn/15 text-warn ring-1 ring-warn/30',
        className,
      )}
    >
      Demo · virtual
    </span>
  ) : (
    <span
      className={clsx(
        'inline-flex items-center gap-1 rounded px-1.5 py-px text-[9px] font-bold uppercase tracking-wider',
        'bg-up/15 text-up ring-1 ring-up/30',
        className,
      )}
    >
      Live
    </span>
  );
}
