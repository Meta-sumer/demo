import { readFileSync } from 'node:fs';
import path from 'node:path';
import { describe, expect, it } from 'vitest';
import { THEME_TOKENS, type ResolvedTheme } from '../src/theme/tokens';

/**
 * Theme contrast.
 *
 * The app's palette lives in src/theme/tokens.ts; the web client's lives in its
 * stylesheet. A colour change that quietly breaks readability is easy to make
 * and hard to see, so this computes the real WCAG ratios from the tokens - and
 * checks the two palettes have not drifted apart, so the app and the website
 * keep looking like one product.
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

function toRgb(channels: string): Rgb {
  const [r, g, b] = channels.trim().split(/\s+/).map(Number);
  return [r!, g!, b!];
}

function palette(theme: ResolvedTheme): Record<string, Rgb> {
  return Object.fromEntries(Object.entries(THEME_TOKENS[theme]).map(([name, value]) => [name, toRgb(value)]));
}

const THEMES: Array<[string, Record<string, Rgb>]> = [
  ['dark', palette('dark')],
  ['twilight', palette('twilight')],
  ['light', palette('light')],
];

const WHITE: Rgb = [255, 255, 255];

/** Normal-size text needs 4.5:1; a large bold button label needs 3:1. */
const TEXT_PAIRS: Array<[string, string, string]> = [
  ['body text on the page', 'content-100', 'surface-950'],
  ['secondary text on the page', 'content-200', 'surface-950'],
  ['muted text on the page', 'content-300', 'surface-950'],
  ['subtle text on a panel', 'content-400', 'surface-900'],
  ['subtle text in an input', 'content-400', 'surface-850'],
  ['accent text on a panel', 'accent', 'surface-900'],
  ['up text on a panel', 'up', 'surface-900'],
  ['down text on a panel', 'down', 'surface-900'],
  ['warn text on a panel', 'warn', 'surface-900'],
  ['critical text on a panel', 'critical', 'surface-900'],
];

describe.each(THEMES)('%s theme', (_theme, palette) => {
  it('defines every token the components use', () => {
    for (const name of [
      'surface-950', 'surface-900', 'surface-850', 'surface-800', 'surface-700', 'surface-600', 'surface-500',
      'content-100', 'content-200', 'content-300', 'content-400',
      'accent', 'accent-solid', 'up', 'up-solid', 'down', 'down-solid', 'warn', 'critical', 'scrim',
    ]) {
      expect(palette[name], name + ' is missing').toBeDefined();
    }
  });

  it.each(TEXT_PAIRS)('%s reaches 4.5:1', (_name, foreground, background) => {
    expect(contrast(palette[foreground]!, palette[background]!)).toBeGreaterThanOrEqual(4.5);
  });

  /*
   * The primary button's label is the page's own background colour, not white:
   * `bg-accent-solid text-surface-950`. That pairing inverts with the theme -
   * dark text on a bright fill in the dark themes, light text on a deep fill in
   * the light one - so the ratio has to be measured between the two tokens the
   * button actually puts together. White is asserted where white is used, on
   * the UP and DOWN buttons below.
   */
  it('keeps the primary button label readable', () => {
    expect(contrast(palette['surface-950']!, palette['accent-solid']!)).toBeGreaterThanOrEqual(4.5);
  });

  it('keeps white readable on the DOWN button', () => {
    expect(contrast(WHITE, palette['down-solid']!)).toBeGreaterThanOrEqual(4.5);
  });

  it('keeps white readable on the large UP button label', () => {
    // 18px bold qualifies as large text, where the threshold is 3:1.
    expect(contrast(WHITE, palette['up-solid']!)).toBeGreaterThanOrEqual(3);
  });

  /*
   * Surface hierarchy.
   *
   * Contrast ratios alone do not catch a washed-out theme: a page at #f6f7fb
   * behind white panels passes every text check and still renders as one flat
   * white sheet, because the step between them is 1.07:1. These assert that the
   * layers are actually distinguishable from each other.
   */
  it('separates the page from the panels sitting on it', () => {
    expect(contrast(palette['surface-950']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.15);
  });

  it('gives a panel border enough presence to outline the panel', () => {
    expect(contrast(palette['surface-700']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.4);
  });

  it('keeps an input distinguishable from the panel it sits on', () => {
    expect(contrast(palette['surface-850']!, palette['surface-900']!)).toBeGreaterThanOrEqual(1.03);
  });

  it('opens the border ramp progressively away from the panel', () => {
    /*
     * Not a monotonic luminance ramp: dark ascends from the page while light
     * peaks at the white panel and descends. What holds in both is that each
     * step from the divider outwards stands further clear of the panel than the
     * one before, so dividers read lighter than borders, which read lighter
     * than strong borders.
     */
    const panel = palette['surface-900']!;
    const steps = ['surface-800', 'surface-700', 'surface-600', 'surface-500'].map((name) =>
      contrast(palette[name]!, panel),
    );

    for (let i = 1; i < steps.length; i += 1) {
      expect(steps[i]!).toBeGreaterThan(steps[i - 1]!);
    }
  });

  it('darkens the modal scrim, whichever theme is active', () => {
    // A light veil over a light page would not read as a veil at all.
    expect(relativeLuminance(palette.scrim!)).toBeLessThan(0.1);
  });
});

/**
 * The app and the website share one palette.
 *
 * Every token the app defines must carry exactly the value the web client's
 * stylesheet gives it, theme for theme.
 */
describe('palette matches the web client', () => {
  const css = readFileSync(path.resolve(__dirname, '../../client/src/index.css'), 'utf8');

  function webTokens(startMarker: string): Record<string, string> {
    const start = css.indexOf(startMarker);
    expect(start, 'token block not found: ' + startMarker).toBeGreaterThan(-1);
    const block = css.slice(start, css.indexOf('}', start));
    const found: Record<string, string> = {};
    for (const match of block.matchAll(/--([a-z0-9-]+):\s*(\d+\s+\d+\s+\d+);/g)) found[match[1]!] = match[2]!;
    return found;
  }

  it.each([
    ['dark', ':root {'],
    ['twilight', ":root[data-theme='twilight'] {"],
    ['light', ":root[data-theme='light'] {"],
  ] as Array<[ResolvedTheme, string]>)('%s', (theme, marker) => {
    const web = webTokens(marker);
    for (const [name, value] of Object.entries(THEME_TOKENS[theme])) {
      expect(web[name], name + ' is not in the web stylesheet').toBeDefined();
      expect(value.split(/\s+/).join(' '), name).toBe(web[name]!.split(/\s+/).join(' '));
    }
  });
});
