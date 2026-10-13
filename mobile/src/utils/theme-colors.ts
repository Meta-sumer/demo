import { useMemo } from 'react';
import { useThemeStore } from '../stores/themeStore';
import { tokenColor, type ResolvedTheme } from '../theme/tokens';

export { channelsToHex, channelsToRgba } from '../theme/tokens';

/**
 * Theme tokens for code that cannot use classes: the Skia chart, icon colours,
 * anything that takes a colour string.
 *
 * Read from the same table the class names resolve through
 * (src/theme/tokens.ts), so a chart never keeps dark grid lines on a light
 * screen.
 */
export interface ChartPalette {
  theme: ResolvedTheme;
  background: string;
  text: string;
  strongText: string;
  grid: string;
  crosshair: string;
  border: string;
  panel: string;
  up: string;
  down: string;
  /** Lifted variants, for the hovered and highlighted order states. */
  upBright: string;
  downBright: string;
  accent: string;
  warn: string;
  /** Order amount tags and active-order indicators. */
  brand: string;
  /** Candle bodies. Deliberately not the same value as up/down: an order
   *  accent has to stay distinguishable from the bar it sits on. */
  bull: string;
  bear: string;
  /** The series' own last-price line. */
  priceLine: string;
  accentFill: [string, string];
}

export function readChartPalette(theme: ResolvedTheme): ChartPalette {
  return {
    theme,
    background: tokenColor(theme, 'chart-bg'),
    text: tokenColor(theme, 'content-400'),
    strongText: tokenColor(theme, 'content-100'),
    grid: tokenColor(theme, 'chart-grid', 0.55),
    crosshair: tokenColor(theme, 'chart-crosshair'),
    border: tokenColor(theme, 'surface-700'),
    panel: tokenColor(theme, 'surface-900'),
    up: tokenColor(theme, 'up'),
    down: tokenColor(theme, 'down'),
    upBright: tokenColor(theme, 'up-bright'),
    downBright: tokenColor(theme, 'down-bright'),
    accent: tokenColor(theme, 'accent'),
    warn: tokenColor(theme, 'warn'),
    brand: tokenColor(theme, 'brand'),
    bull: tokenColor(theme, 'chart-bull'),
    bear: tokenColor(theme, 'chart-bear'),
    priceLine: tokenColor(theme, 'chart-price-line'),
    accentFill: [tokenColor(theme, 'accent', 0.28), tokenColor(theme, 'accent', 0.02)],
  };
}

/** The chart palette for the theme on screen. */
export function useChartPalette(): ChartPalette {
  const theme = useThemeStore((s) => s.resolved);
  return useMemo(() => readChartPalette(theme), [theme]);
}

/**
 * Applies an alpha to a palette colour.
 *
 * The palette hands out hex or `rgba(...)`; both keep working. An alpha already
 * carried by the colour is multiplied rather than replaced, so dimming a token
 * that is itself translucent dims it further.
 */
export function withAlpha(color: string, alpha: number): string {
  const clamped = Math.max(0, Math.min(1, alpha));

  if (color.startsWith('rgba(')) {
    const parts = color.slice(5, -1).split(',');
    if (parts.length === 4) {
      const existing = Number(parts[3]);
      const combined = Number.isFinite(existing) ? existing * clamped : clamped;
      return 'rgba(' + parts.slice(0, 3).map((part) => part.trim()).join(', ') + ', ' + combined + ')';
    }
    return color;
  }

  if (color.startsWith('#') && (color.length === 7 || color.length === 4)) {
    const full =
      color.length === 4 ? '#' + color[1]! + color[1]! + color[2]! + color[2]! + color[3]! + color[3]! : color;
    const r = parseInt(full.slice(1, 3), 16);
    const g = parseInt(full.slice(3, 5), 16);
    const b = parseInt(full.slice(5, 7), 16);
    return 'rgba(' + r + ', ' + g + ', ' + b + ', ' + clamped + ')';
  }

  return color;
}

/** One token as a colour string for the theme on screen - icon colours, placeholders. */
export function useTokenColor(name: Parameters<typeof tokenColor>[1], alpha = 1): string {
  const theme = useThemeStore((s) => s.resolved);
  return tokenColor(theme, name, alpha);
}
