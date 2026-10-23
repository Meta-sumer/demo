import { create } from 'zustand';

/**
 * Theme preference.
 *
 * Light, twilight and dark, plus "system", which follows the operating system
 * and keeps following it as it changes. The resolved value is mirrored onto
 * <html data-theme> because that is what the CSS tokens key off, and onto
 * color-scheme so native controls (scrollbars, date pickers, form fields)
 * match.
 *
 * A visitor who has never chosen gets the brand's dark theme (DEFAULT_THEME),
 * the same black-and-yellow the landing page is drawn in, whatever the OS
 * says; a stored choice, "system" included, always wins.
 *
 * The same key and default are applied before first paint by
 * public/theme-init.js. Changing either here means changing it there too.
 */
export type ThemePreference = 'light' | 'twilight' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'twilight' | 'dark';

/** Every theme a stored preference may name. */
export const THEMES: ThemePreference[] = ['light', 'twilight', 'dark', 'system'];

export const THEME_STORAGE_KEY = 'bt.theme';

/** What a visitor without a stored preference gets: the brand's dark theme. */
export const DEFAULT_THEME: ThemePreference = 'dark';

interface ThemeState {
  preference: ThemePreference;
  resolved: ResolvedTheme;
  setPreference: (preference: ThemePreference) => void;
  /** Cycles light -> twilight -> dark -> system, for the header button. */
  cycle: () => void;
}

function systemTheme(): ResolvedTheme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'dark';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

/** The preference a stored value names; anything else - nothing stored, an old or unknown value - is the default. */
export function preferenceFrom(stored: string | null | undefined): ThemePreference {
  return (THEMES as Array<string | null | undefined>).includes(stored) ? (stored as ThemePreference) : DEFAULT_THEME;
}

function readStored(): ThemePreference {
  try {
    return preferenceFrom(localStorage.getItem(THEME_STORAGE_KEY));
  } catch {
    /* private browsing - the default, as for a first visit */
    return DEFAULT_THEME;
  }
}

function resolve(preference: ThemePreference): ResolvedTheme {
  return preference === 'system' ? systemTheme() : preference;
}

function apply(resolved: ResolvedTheme): void {
  if (typeof document === 'undefined') return;
  document.documentElement.dataset.theme = resolved;
  document.documentElement.style.colorScheme = resolved;
}

const initialPreference = readStored();

export const useThemeStore = create<ThemeState>((set, get) => ({
  preference: initialPreference,
  resolved: resolve(initialPreference),

  setPreference(preference) {
    const resolved = resolve(preference);
    try {
      localStorage.setItem(THEME_STORAGE_KEY, preference);
    } catch {
      /* the choice simply will not survive a reload */
    }
    apply(resolved);
    set({ preference, resolved });
  },

  cycle() {
    const next = THEMES[(THEMES.indexOf(get().preference) + 1) % THEMES.length]!;
    get().setPreference(next);
  },
}));

/**
 * Keeps "system" live.
 *
 * Without this, a user on the system setting would stay on whatever the OS was
 * when the tab opened, and not follow it into night mode.
 */
export function watchSystemTheme(): () => void {
  if (typeof window === 'undefined' || !window.matchMedia) return () => undefined;

  const media = window.matchMedia('(prefers-color-scheme: dark)');
  const onChange = () => {
    if (useThemeStore.getState().preference !== 'system') return;
    const resolved: ResolvedTheme = media.matches ? 'dark' : 'light';
    apply(resolved);
    useThemeStore.setState({ resolved });
  };

  media.addEventListener('change', onChange);
  onChange();
  return () => media.removeEventListener('change', onChange);
}
