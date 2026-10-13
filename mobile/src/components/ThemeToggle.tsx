import { Monitor, Moon, Sun, SunMoon } from 'lucide-react-native';
import { useThemeStore, type ThemePreference } from '../stores/themeStore';
import { Segmented, type SegmentOption } from './ui';

/**
 * Theme control.
 *
 * A segment per option rather than a cycling button: it shows the current state
 * instead of hiding it behind a guess about what the next tap does, and every
 * theme is one action away.
 */
const OPTIONS: Array<SegmentOption<ThemePreference>> = [
  { value: 'light', label: 'Light', icon: Sun },
  { value: 'twilight', label: 'Twilight', icon: SunMoon },
  { value: 'dark', label: 'Full night', icon: Moon },
  { value: 'system', label: 'System', icon: Monitor },
];

export function ThemeToggle({ className }: { className?: string }) {
  const preference = useThemeStore((s) => s.preference);
  const setPreference = useThemeStore((s) => s.setPreference);
  return (
    <Segmented
      accessibilityLabel="Colour theme"
      options={OPTIONS}
      value={preference}
      onChange={setPreference}
      size="sm"
      className={className}
    />
  );
}
