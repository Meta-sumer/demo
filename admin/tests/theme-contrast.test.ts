import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';

/**
 * Console theme contrast.
 *
 * The console's palette lives in src/index.css. It shares the brand yellow
 * with the web client but has its own, deeper surface ramp, so it is checked
 * here: real WCAG ratios computed from the stylesheet, for every theme, so a
 * colour change that quietly makes the console dull or unreadable fails.
 */
type Rgb = [number, number, number];

function relativeLuminance([r, g, b]: Rgb): number {
  const channel = (value: number) => {
    const s = value / 255;
    return s <= 0.03928 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4;
  };
  return 0.2126 * channel(r) + 0.7152 * channel(g) + 0.0722 * channel(b);
}

function contrast(a: Rgb, b: Rgb): number {
  const [lighter, darker] =
    relativeLuminance(a) > relativeLuminance(b)
      ? [relativeLuminance(a), relativeLuminance(b)]
      : [relativeLuminance(b), relativeLuminance(a)];
  return (lighter + 0.05) / (darker + 0.05);
}

/** `top` at `alpha` over `base`, as `bg-up/15` paints on a panel. */
function over(top: Rgb, alpha: number, base: Rgb): Rgb {
  return [0, 1, 2].map((i) => Math.round(top[i]! * alpha + base[i]! * (1 - alpha))) as Rgb;
}

const css = readFileSync(path.resolve(__dirname, '../src/index.css'), 'utf8');

/** Reads the RGB channel triples out of one `:root` block. */
function tokens(startMarker: string): Record<string, Rgb> {
  const start = css.indexOf(startMarker);
  expect(start, 'token block not found: ' + startMarker).toBeGreaterThan(-1);

  const block = css.slice(start, css.indexOf('}', start));
  const found: Record<string, Rgb> = {};
  for (const match of block.matchAll(/--([a-z0-9-]+):\s*(\d+)\s+(\d+)\s+(\d+);/g)) {
    found[match[1]!] = [Number(match[2]), Number(match[3]), Number(match[4])];
  }
  return found;
}

const THEMES: Array<[string, Record<string, Rgb>]> = [
  ['dark', tokens(':root {')],
  ['twilight', tokens(":root[data-theme='twilight'] {")],
  ['light', tokens(":root[data-theme='light'] {")],
];

const WHITE: Rgb = [255, 255, 255];

/** Body text needs 4.5:1 (WCAG AA) wherever it is drawn. */
const TEXT_PAIRS: Array<[string, string, string]> = [
  ['body text on the page', 'content-100', 'surface-950'],
  ['secondary text on the page', 'content-200', 'surface-950'],
  ['muted text on the page', 'content-300', 'surface-950'],
  ['subtle text on the page', 'content-400', 'surface-950'],
  ['body text on a panel', 'content-100', 'surface-900'],
  ['labels on a panel', 'content-300', 'surface-900'],
  ['help text on a panel', 'content-400', 'surface-900'],
  ['placeholder in an input', 'content-400', 'surface-850'],
  ['subtle text on a divider-coloured chip', 'content-400', 'surface-800'],
  ['accent text on a panel', 'accent', 'surface-900'],
  ['up text on a panel', 'up', 'surface-900'],
  ['down text on a panel', 'down', 'surface-900'],
  ['warn text on a panel', 'warn', 'surface-900'],
  ['critical text on a panel', 'critical', 'surface-900'],
  ['accent text on a hovered row', 'accent', 'surface-850'],
  ['up text on a hovered row', 'up', 'surface-850'],
  ['down text on a hovered row', 'down', 'surface-850'],
];

/** Badge text on its own tint over a panel (`bg-up/15 text-up`, critical at /20). */
const BADGES: Array<[string, number]> = [
  ['accent', 0.15],
  ['up', 0.15],
  ['down', 0.15],
  ['warn', 0.15],
  ['critical', 0.2],
];

describe.each(THEMES)('%s theme', (theme, palette) => {
  it('defines every token the components use', () => {
    for (const name of [
      'surface-950', 'surface-900', 'surface-850', 'surface-800', 'surface-700', 'surface-600', 'surface-500',
      'content-100', 'content-200', 'content-300', 'content-400',
      'accent', 'accent-solid', 'on-accent', 'up', 'up-solid', 'down', 'down-solid', 'warn', 'critical', 'scrim',
      'chart-grid', 'chart-crosshair',
    ]) {
      expect(palette[name], name + ' is missing').toBeDefined();
    }
  });

  it.each(TEXT_PAIRS)('%s reaches 4.5:1', (_name, foreground, background) => {
    expect(contrast(palette[foreground]!, palette[background]!)).toBeGreaterThanOrEqual(4.5);
  });

  it.each(BADGES)('keeps a %s badge readable on its tint', (tone, alpha) => {
    const fill = over(palette[tone]!, alpha, palette['surface-900']!);
    expect(contrast(palette[tone]!, fill)).toBeGreaterThanOrEqual(4.5);
  });

  /*
   * The yellow fill - primary buttons, the selected nav item, the active
   * theme segment, the count badges - carries near-black text in every theme.
   */
  it('keeps the label on the yellow fill readable', () => {
    expect(contrast(palette['on-accent']!, palette['accent-solid']!)).toBeGreaterThanOrEqual(4.5);
  });

  it('keeps the label on a warn count badge readable', () => {
    // `bg-warn text-surface-950`: dark on orange in the dark themes, light on deep orange in Light.
    expect(contrast(palette['surface-950']!, palette.warn!)).toBeGreaterThanOrEqual(4.5);
  });

  it('keeps white readable on the danger button and the unread badge', () => {
    expect(contrast(WHITE, palette['down-solid']!)).toBeGreaterThanOrEqual(4.5);
  });

  it('draws the focus ring and the input focus border at 3:1 or more', () => {
    // Non-text contrast (WCAG 1.4.11) against the page and the input.
    expect(contrast(palette.accent!, palette['surface-950']!)).toBeGreaterThanOrEqual(3);
    expect(contrast(palette.accent!, palette['surface-850']!)).toBeGreaterThanOrEqual(3);
  });

  if (theme !== 'light') {
    it('makes the yellow fill stand out from a dark panel', () => {
      expect(contrast(palette['accent-solid']!, palette['surface-900']!)).toBeGreaterThanOrEqual(7);
    });
  }

  /*
   * Depth. Text ratios alone do not catch a flat theme: grey panels on a grey
   * page with grey inputs pass every text check and still read as one dull
   * sheet. These keep the page, the panels, the inputs and the borders apart.
   */
  it('sets the page apart from the panels on it', () => {
    expect(contrast(palette['surface-950']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.15);
  });

  it('sets an input apart from the panel it sits on', () => {
    expect(contrast(palette['surface-850']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.05);
  });

  it('draws row dividers that can be seen', () => {
    expect(contrast(palette['surface-800']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.2);
  });

  it('gives a panel border enough presence to outline the panel', () => {
    expect(contrast(palette['surface-700']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.45);
  });

  it('outlines an input against its own fill', () => {
    expect(contrast(palette['surface-700']!, palette['surface-850']!)).toBeGreaterThanOrEqual(1.35);
  });

  it('opens the border ramp progressively away from the panel', () => {
    const panel = palette['surface-900']!;
    const steps = ['surface-800', 'surface-700', 'surface-600', 'surface-500'].map((name) =>
      contrast(palette[name]!, panel),
    );
    for (let i = 1; i < steps.length; i += 1) {
      expect(steps[i]!).toBeGreaterThan(steps[i - 1]!);
    }
  });

  it('darkens the modal scrim, whichever theme is active', () => {
    expect(relativeLuminance(palette.scrim!)).toBeLessThan(0.1);
  });
});

describe('Full night', () => {
  const dark = THEMES[0]![1];

  it('is the attribute-less :root, so a page without the boot script is still the brand theme', () => {
    expect(css).not.toMatch(/prefers-color-scheme/);
    expect(dark['surface-950']).toBeDefined();
  });

  it('is near-black, not grey', () => {
    expect(relativeLuminance(dark['surface-950']!)).toBeLessThan(0.005);
  });
});
