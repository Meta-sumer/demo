/**
 * Theme tokens.
 *
 * The same values as the web client's stylesheet (client/src/index.css), as
 * space-separated RGB channels. On the web they were CSS custom properties;
 * here they are set as NativeWind variables on the root view (`ThemeRoot`),
 * which is what keeps `bg-surface-900` and `text-accent/60` working, and they
 * are read directly by the chart, which draws with Skia and cannot use classes.
 * One table, two readers - the palette cannot drift between them.
 */
export type ResolvedTheme = 'light' | 'twilight' | 'dark';

export const TOKEN_NAMES = [
  'surface-950',
  'surface-900',
  'surface-850',
  'surface-800',
  'surface-700',
  'surface-600',
  'surface-500',
  'content-100',
  'content-200',
  'content-300',
  'content-400',
  'accent',
  'accent-soft',
  'accent-solid',
  'up',
  'up-soft',
  'up-solid',
  'up-bright',
  'down',
  'down-soft',
  'down-solid',
  'down-bright',
  'warn',
  'critical',
  'brand',
  'scrim',
  'chart-grid',
  'chart-axis',
  'chart-crosshair',
  'chart-bg',
  'chart-bull',
  'chart-bear',
  'chart-price-line',
] as const;

export type TokenName = (typeof TOKEN_NAMES)[number];

export type TokenTable = Record<TokenName, string>;

/** "Full Night". */
const dark: TokenTable = {
  'surface-950': '29 31 35',
  'surface-900': '41 44 50',
  'surface-850': '36 39 44',
  'surface-800': '48 52 58',
  'surface-700': '68 74 84',
  'surface-600': '90 96 106',
  'surface-500': '119 125 135',
  'content-100': '245 245 245',
  'content-200': '200 204 211',
  'content-300': '167 171 179',
  'content-400': '146 154 165',
  accent: '246 216 74',
  'accent-soft': '58 50 16',
  'accent-solid': '246 216 74',
  up: '103 211 154',
  'up-soft': '25 52 40',
  'up-solid': '46 153 98',
  'up-bright': '123 224 167',
  down: '239 107 107',
  'down-soft': '58 32 32',
  'down-solid': '204 71 71',
  'down-bright': '242 120 120',
  warn: '246 216 74',
  critical: '255 107 133',
  brand: '246 216 74',
  scrim: '4 5 8',
  'chart-grid': '48 52 58',
  'chart-axis': '146 154 165',
  'chart-crosshair': '119 125 135',
  'chart-bg': '30 32 36',
  'chart-bull': '98 200 138',
  'chart-bear': '234 109 109',
  'chart-price-line': '138 144 153',
};

const twilight: TokenTable = {
  'surface-950': '26 32 48',
  'surface-900': '38 47 69',
  'surface-850': '48 59 85',
  'surface-800': '60 73 103',
  'surface-700': '80 96 132',
  'surface-600': '105 124 164',
  'surface-500': '136 154 190',
  'content-100': '236 241 250',
  'content-200': '201 211 229',
  'content-300': '161 174 199',
  'content-400': '158 171 198',
  accent: '247 206 70',
  'accent-soft': '66 57 22',
  'accent-solid': '247 206 70',
  up: '45 205 110',
  'up-soft': '24 62 40',
  'up-solid': '19 140 63',
  'up-bright': '90 224 150',
  down: '252 112 112',
  'down-soft': '71 34 38',
  'down-solid': '199 45 45',
  'down-bright': '255 140 140',
  warn: '247 170 40',
  critical: '252 114 142',
  brand: '247 206 70',
  scrim: '8 11 20',
  'chart-grid': '60 73 103',
  'chart-axis': '158 171 198',
  'chart-crosshair': '136 154 190',
  'chart-bg': '34 42 62',
  'chart-bull': '45 205 110',
  'chart-bear': '252 112 112',
  'chart-price-line': '158 171 198',
};

const light: TokenTable = {
  'surface-950': '233 237 244',
  'surface-900': '255 255 255',
  'surface-850': '243 245 250',
  'surface-800': '224 229 238',
  'surface-700': '203 210 222',
  'surface-600': '172 181 197',
  'surface-500': '134 144 161',
  'content-100': '15 18 28',
  'content-200': '46 53 68',
  'content-300': '74 82 100',
  'content-400': '100 108 126',
  accent: '59 91 219',
  'accent-soft': '226 232 253',
  'accent-solid': '47 78 200',
  up: '21 128 61',
  'up-soft': '220 243 228',
  'up-solid': '21 128 61',
  'up-bright': '16 110 52',
  down: '200 30 30',
  'down-soft': '253 226 226',
  'down-solid': '200 30 30',
  'down-bright': '180 24 24',
  warn: '161 98 7',
  critical: '190 18 60',
  brand: '180 130 8',
  scrim: '15 23 42',
  'chart-grid': '224 229 238',
  'chart-axis': '100 108 126',
  'chart-crosshair': '134 144 161',
  'chart-bg': '255 255 255',
  'chart-bull': '21 128 61',
  'chart-bear': '200 30 30',
  'chart-price-line': '100 108 126',
};

export const THEME_TOKENS: Record<ResolvedTheme, TokenTable> = { dark, twilight, light };

/** How dark the modal veil is, per theme. It darkens in every theme. */
export const SCRIM_ALPHA: Record<ResolvedTheme, number> = { dark: 0.72, twilight: 0.7, light: 0.45 };

function parseChannels(channels: string): [number, number, number] | null {
  const parts = channels
    .trim()
    .split(/[\s,]+/)
    .map(Number);
  if (parts.length < 3) return null;
  const [r, g, b] = parts;
  if (![r, g, b].every((value) => typeof value === 'number' && Number.isFinite(value))) return null;
  return [r as number, g as number, b as number];
}

/** "100 108 126" -> "#646c7e". Returns null for anything unparseable. */
export function channelsToHex(channels: string): string | null {
  const parts = parseChannels(channels);
  if (!parts) return null;
  return (
    '#' +
    parts
      .map((value) => Math.max(0, Math.min(255, Math.round(value))).toString(16).padStart(2, '0'))
      .join('')
  );
}

/** "100 108 126" -> "rgba(100, 108, 126, 0.55)". */
export function channelsToRgba(channels: string, alpha: number): string | null {
  const parts = parseChannels(channels);
  if (!parts) return null;
  return 'rgba(' + parts.join(', ') + ', ' + alpha + ')';
}

/** A token as a colour string, for style props and Skia. */
export function tokenColor(theme: ResolvedTheme, name: TokenName, alpha = 1): string {
  const channels = THEME_TOKENS[theme][name];
  return (alpha >= 1 ? channelsToHex(channels) : channelsToRgba(channels, alpha)) ?? '#000000';
}
