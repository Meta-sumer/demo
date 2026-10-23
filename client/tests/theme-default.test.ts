import { readFileSync } from 'node:fs';
import path from 'node:path';
import { runInNewContext } from 'node:vm';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_THEME, THEME_STORAGE_KEY, preferenceFrom } from '../src/stores/themeStore';

/**
 * The theme a visitor gets.
 *
 * Nobody who has not chosen a theme is handed one by their operating system:
 * the app opens in the brand's dark theme, like the landing page. A stored
 * choice, "system" included, always wins. Two pieces of code decide this - the
 * store, and the script that paints the theme before the app loads - and they
 * must agree, or the page flashes from one theme to the other on every load.
 */
const script = readFileSync(path.resolve(__dirname, '../public/theme-init.js'), 'utf8');

type Os = 'light' | 'dark';

function matchMediaFor(os: Os) {
  return (query: string) => ({
    matches: query.includes('dark') ? os === 'dark' : os === 'light',
    media: query,
    addEventListener: () => undefined,
    removeEventListener: () => undefined,
  });
}

/** What public/theme-init.js writes onto <html> for a stored value and an OS preference. */
function paintedTheme(stored: string | null, os: Os): string | undefined {
  const documentElement = { dataset: {} as Record<string, string>, style: {} as Record<string, string> };
  runInNewContext(script, {
    localStorage: { getItem: (key: string) => (key === THEME_STORAGE_KEY ? stored : null) },
    window: { matchMedia: matchMediaFor(os) },
    document: { documentElement },
  });
  return documentElement.dataset.theme;
}

/** What the store starts with for a stored value and an OS preference. */
async function storeTheme(stored: string | null, os: Os) {
  localStorage.clear();
  if (stored !== null) localStorage.setItem(THEME_STORAGE_KEY, stored);
  (globalThis as { matchMedia?: unknown }).matchMedia = matchMediaFor(os);
  vi.resetModules();
  const { useThemeStore } = await import('../src/stores/themeStore');
  return useThemeStore.getState();
}

afterEach(() => {
  localStorage.clear();
  delete (globalThis as { matchMedia?: unknown }).matchMedia;
});

describe('theme default', () => {
  it('is the dark theme', () => {
    expect(DEFAULT_THEME).toBe('dark');
  });

  it('keeps every stored choice and defaults anything else', () => {
    for (const choice of ['light', 'twilight', 'dark', 'system']) expect(preferenceFrom(choice)).toBe(choice);
    for (const other of [null, undefined, '', 'auto', 'Dark']) expect(preferenceFrom(other)).toBe('dark');
  });

  it('opens a first visit in dark even when the OS prefers light', async () => {
    const state = await storeTheme(null, 'light');
    expect(state.preference).toBe('dark');
    expect(state.resolved).toBe('dark');
    expect(paintedTheme(null, 'light')).toBe('dark');
  });

  it('still follows the OS for a visitor who chose "system"', async () => {
    expect((await storeTheme('system', 'light')).resolved).toBe('light');
    expect(paintedTheme('system', 'light')).toBe('light');
    expect((await storeTheme('system', 'dark')).resolved).toBe('dark');
    expect(paintedTheme('system', 'dark')).toBe('dark');
  });

  it.each([null, 'light', 'twilight', 'dark', 'system', 'unknown'])(
    'store and pre-paint script agree for stored %s',
    async (stored) => {
      for (const os of ['light', 'dark'] as Os[]) {
        const state = await storeTheme(stored, os);
        expect(paintedTheme(stored, os), 'OS ' + os).toBe(state.resolved);
      }
    },
  );
});
