import { create } from 'zustand';

/**
 * Theme preference.
 *
 * Three states, not two: "system" follows the operating system and keeps
 * following it as it changes, which is what a user who never touches the toggle
 * expects. The resolved value is mirrored onto <html data-theme> because that
 * is what the CSS tokens key off, and onto color-scheme so native controls
 * (scrollbars, date pickers, form fields) match.
 *
 * The same key is read by the inline script in index.html, which applies the
 * theme before first paint. Changing it here means changing it there too.
 */
export type ThemePreference = 'light' | 'twilight' | 'dark' | 'system';
export type ResolvedTheme = 'light' | 'twilight' | 'dark';

/** Every theme a stored preference may name. */
export const THEMES: ThemePreference[] = ['light', 'twilight', 'dark', 'system'];

export const THEME_STORAGE_KEY = 'bt.theme';

interface ThemeState {
  preference: ThemePreference;
  resolved: ResolvedTheme;
  setPreference: (preference: ThemePreference) => void;
  /** Cycles light -> dark -> system, for the header button. */
  cycle: () => void;
}

function systemTheme(): ResolvedTheme {
  if (typeof window === 'undefined' || !window.matchMedia) return 'dark';
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function readStored(): ThemePreference {
  try {
    const stored = localStorage.getItem(THEME_STORAGE_KEY);
    if ((THEMES as string[]).includes(stored ?? '')) return stored as ThemePreference;
  } catch {
    /* private browsing - fall back to following the system */
  }
  return 'system';
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
