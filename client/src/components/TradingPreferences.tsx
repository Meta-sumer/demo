import clsx from 'clsx';
import { useSettingsStore, type TradeMode } from '../stores/settingsStore';

/**
 * The two trading preferences, as one set of controls.
 *
 * They appear in Account → Preferences, on the Settings page and in the
 * settings drawer. Rendering the same components in all three keeps the names
 * and the explanations identical, so "one-tap trading" never means something
 * slightly different depending on where it was switched.
 */

/** On = trades are placed without the confirmation step (`confirmBeforeTrade` off). */
export function OneTapSwitch({ className }: { className?: string }) {
  const confirmBeforeTrade = useSettingsStore((s) => s.confirmBeforeTrade);
  const toggleConfirm = useSettingsStore((s) => s.toggleConfirm);
  const on = !confirmBeforeTrade;

  return (
    <SwitchRow
      className={className}
      label="One-tap trading"
      description={
        on
          ? 'On: pressing UP or DOWN places the trade immediately, without the review step.'
          : 'Off: you review every trade and press Confirm Trade before it is placed.'
      }
      checked={on}
      onChange={toggleConfirm}
    />
  );
}

const MODES: Array<{ value: TradeMode; label: string; hint: string }> = [
  { value: 'beginner', label: 'Beginner', hint: 'One market, the essential controls' },
  { value: 'advanced', label: 'Advanced', hint: 'Market tabs, indicators, drawing tools' },
];

export function TradeModeControl({ className }: { className?: string }) {
  const tradeMode = useSettingsStore((s) => s.tradeMode);
  const setTradeMode = useSettingsStore((s) => s.setTradeMode);

  return (
    <div className={className}>
      <div className="mb-1.5 text-sm font-medium text-content-100">Default trading mode</div>
      <div role="radiogroup" aria-label="Default trading mode" className="grid grid-cols-2 gap-1.5">
        {MODES.map((mode) => {
          const selected = tradeMode === mode.value;
          return (
            <button
              key={mode.value}
              type="button"
              role="radio"
              aria-checked={selected}
              onClick={() => setTradeMode(mode.value)}
              className={clsx(
                'rounded-lg border px-3 py-2 text-left transition',
                selected
                  ? 'border-accent bg-accent/10'
                  : 'border-surface-700 bg-surface-850 hover:border-surface-600',
              )}
            >
              <span className="block text-sm font-semibold text-content-100">{mode.label}</span>
              <span className="block text-[11px] leading-snug text-content-400">{mode.hint}</span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

export function SwitchRow({
  label,
  description,
  checked,
  onChange,
  className,
}: {
  label: string;
  description: string;
  checked: boolean;
  onChange: () => void;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={onChange}
      className={clsx(
        'flex w-full items-center gap-4 rounded-lg border border-surface-700 bg-surface-850 px-4 py-3 text-left',
        className,
      )}
      role="switch"
      aria-checked={checked}
    >
      <span className="min-w-0 flex-1">
        <span className="block text-sm font-medium text-content-100">{label}</span>
        <span className="block text-[11px] leading-snug text-content-400">{description}</span>
      </span>
      <span className={clsx('relative h-6 w-11 shrink-0 rounded-full transition', checked ? 'bg-accent' : 'bg-surface-700')}>
        <span
          className={clsx(
            'absolute top-0.5 h-5 w-5 rounded-full shadow-panel transition-all',
            checked ? 'left-[22px] bg-surface-950' : 'left-0.5 bg-surface-500',
          )}
        />
      </span>
    </button>
  );
}
