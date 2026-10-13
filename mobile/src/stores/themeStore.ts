import { Appearance } from 'react-native';
import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { persistStorage } from './storage';
import type { ResolvedTheme } from '../theme/tokens';

export type { ResolvedTheme } from '../theme/tokens';

/**
 * Theme preference.
 *
 * Four templates: Light Mode, Twilight, Full Night and System. "System"
 * follows the operating system and keeps following it as it changes (see
 * `watchSystemTheme`), which is what a user who never touches the control
 * expects. The resolved value is what `ThemeRoot` keys the palette off.
 */
export type ThemePreference = 'light' | 'twilight' | 'dark' | 'system';

/** Every theme a stored preference may name. */
export const THEMES: ThemePreference[] = ['light', 'twilight', 'dark', 'system'];

export const THEME_STORAGE_KEY = 'bt.theme';

interface ThemeState {
  preference: ThemePreference;
  resolved: ResolvedTheme;
  setPreference: (preference: ThemePreference) => void;
  /** Cycles through the templates, for the header button. */
  cycle: () => void;
}

function systemTheme(): ResolvedTheme {
  return Appearance.getColorScheme() === 'light' ? 'light' : 'dark';
}

function resolve(preference: ThemePreference): ResolvedTheme {
  return preference === 'system' ? systemTheme() : preference;
}

export const useThemeStore = create<ThemeState>()(
  persist(
    (set, get) => ({
      preference: 'system',
      resolved: systemTheme(),

      setPreference(preference) {
        set({ preference, resolved: resolve(preference) });
      },

      cycle() {
        const next = THEMES[(THEMES.indexOf(get().preference) + 1) % THEMES.length]!;
        get().setPreference(next);
      },
    }),
    {
      name: THEME_STORAGE_KEY,
      storage: persistStorage,
      partialize: (state) => ({ preference: state.preference }),
      // The resolved theme is derived, so it is worked out again from the
      // stored preference rather than restored stale.
      onRehydrateStorage: () => (state) => {
        if (state) useThemeStore.setState({ resolved: resolve(state.preference) });
      },
    },
  ),
);

/**
 * Keeps "system" live.
 *
 * Without this, a user on the system setting would stay on whatever the OS was
 * when the app started, and not follow it into night mode.
 */
export function watchSystemTheme(): () => void {
  const subscription = Appearance.addChangeListener(({ colorScheme }) => {
    if (useThemeStore.getState().preference !== 'system') return;
    useThemeStore.setState({ resolved: colorScheme === 'light' ? 'light' : 'dark' });
  });
  return () => subscription.remove();
}
