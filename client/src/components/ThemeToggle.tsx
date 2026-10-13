import clsx from 'clsx';
import { Monitor, Moon, Sun, SunMoon } from 'lucide-react';
import { useThemeStore, type ThemePreference } from '../stores/themeStore';

/**
 * Theme control.
 *
 * A segment per option rather than a cycling button: it shows the current state
 * instead of hiding it behind a guess about what the next click does, and every
 * theme is one action away.
 */
const OPTIONS: Array<{ value: ThemePreference; label: string; icon: typeof Sun }> = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'twilight', label: 'Twilight', icon: SunMoon },
  { value: 'dark', label: 'Full night', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
];

export function ThemeToggle({ size = 'sm' }: { size?: 'sm' | 'md' }) {
  const preference = useThemeStore((s) => s.preference);
  const setPreference = useThemeStore((s) => s.setPreference);

  const pad = size === 'md' ? 'px-3 py-1.5' : 'px-2 py-1';
  const icon = size === 'md' ? 'h-4 w-4' : 'h-3.5 w-3.5';

  return (
    <div
      role="radiogroup"
      aria-label="Colour theme"
      className="inline-flex items-center gap-0.5 rounded-lg border border-surface-700 bg-surface-850 p-0.5"
    >
      {OPTIONS.map((option) => {
        const active = preference === option.value;
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={active}
            aria-label={option.label + ' theme'}
            title={option.label}
            onClick={() => setPreference(option.value)}
            className={clsx(
              'rounded-md transition',
              pad,
              active ? 'bg-surface-700 text-content-100 shadow-panel' : 'text-content-400 hover:text-content-200',
            )}
          >
            <option.icon className={icon} />
            {size === 'md' ? <span className="ml-1.5 text-xs font-medium">{option.label}</span> : null}
          </button>
        );
      })}
    </div>
  );
}
