import { readFileSync } from 'node:fs';
import path from 'node:path';
import { runInNewContext } from 'node:vm';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DEFAULT_THEME, THEME_STORAGE_KEY, preferenceFrom } from '../src/stores/themeStore';

/**
 * The theme an operator gets.
 *
 * Nobody who has not chosen a theme is handed one by their operating system:
 * the console opens in Full night, the brand's black and yellow, like the web
 * client. A stored choice, "system" included, always wins. Two pieces of code
 * decide this - the store, and the script that paints the theme before the app
 * loads - and they must agree, or the page flashes from one theme to the other
 * on every load.
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

function storageWith(stored: string | null) {
  const values = new Map<string, string>();
  if (stored !== null) values.set(THEME_STORAGE_KEY, stored);
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
  };
}

/** What public/theme-init.js writes onto <html> for a stored value and an OS preference. */
function paintedTheme(stored: string | null, os: Os): string | undefined {
  const documentElement = { dataset: {} as Record<string, string>, style: {} as Record<string, string> };
  runInNewContext(script, {
    localStorage: storageWith(stored),
    window: { matchMedia: matchMediaFor(os) },
    document: { documentElement },
  });
  return documentElement.dataset.theme;
}

/** What the store starts with for a stored value and an OS preference. */
async function storeTheme(stored: string | null, os: Os) {
  vi.stubGlobal('localStorage', storageWith(stored));
  vi.stubGlobal('window', { matchMedia: matchMediaFor(os) });
  vi.resetModules();
  const { useThemeStore } = await import('../src/stores/themeStore');
  return useThemeStore.getState();
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('theme default', () => {
  it('is Full night', () => {
    expect(DEFAULT_THEME).toBe('dark');
  });

  it('keeps every stored choice and defaults anything else', () => {
    for (const choice of ['light', 'twilight', 'dark', 'system']) expect(preferenceFrom(choice)).toBe(choice);
    for (const other of [null, undefined, '', 'auto', 'Dark']) expect(preferenceFrom(other)).toBe('dark');
  });

  it('opens a first visit in Full night even when the OS prefers light', async () => {
    const state = await storeTheme(null, 'light');
    expect(state.preference).toBe('dark');
    expect(state.resolved).toBe('dark');
    expect(paintedTheme(null, 'light')).toBe('dark');
  });

  it('keeps a stored Light or Twilight whatever the OS prefers', async () => {
    expect((await storeTheme('light', 'dark')).resolved).toBe('light');
    expect(paintedTheme('light', 'dark')).toBe('light');
    expect((await storeTheme('twilight', 'light')).resolved).toBe('twilight');
    expect(paintedTheme('twilight', 'light')).toBe('twilight');
  });

  it('still follows the OS for an operator who chose "system"', async () => {
    expect((await storeTheme('system', 'light')).resolved).toBe('light');
    expect(paintedTheme('system', 'light')).toBe('light');
    expect((await storeTheme('system', 'dark')).resolved).toBe('dark');
    expect(paintedTheme('system', 'dark')).toBe('dark');
  });

  it('falls back to Full night when storage cannot be read', async () => {
    vi.stubGlobal('localStorage', {
      getItem: () => {
        throw new Error('blocked');
      },
    });
    vi.stubGlobal('window', { matchMedia: matchMediaFor('light') });
    vi.resetModules();
    const { useThemeStore } = await import('../src/stores/themeStore');
    expect(useThemeStore.getState().resolved).toBe('dark');
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
