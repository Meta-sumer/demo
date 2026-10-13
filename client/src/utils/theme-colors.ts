import { useEffect, useState } from 'react';
import { useThemeStore, type ResolvedTheme } from '../stores/themeStore';

/**
 * Theme tokens for code that cannot use Tailwind classes.
 *
 * Charting libraries take colours as strings, so they have to read the same
 * custom properties the stylesheet defines rather than carrying their own
 * palette - otherwise a chart keeps its dark grid lines on a white page.
 *
 * The tokens are stored as space-separated RGB channels ("100 108 126") so
 * Tailwind can apply an alpha to them. That has to be converted before it
 * reaches a chart: `rgb(100 108 126)` is valid CSS Color Level 4, but several
 * libraries ship their own parsers that only understand hex or comma-separated
 * rgb(). lightweight-charts throws "Cannot parse color" on the modern form and
 * takes the whole chart down with it.
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

function readToken(name: string, fallback: string): string {
  if (typeof window === 'undefined') return fallback;
  const raw = getComputedStyle(document.documentElement).getPropertyValue('--' + name).trim();
  return channelsToHex(raw) ?? fallback;
}

function readTokenAlpha(name: string, alpha: number, fallback: string): string {
  if (typeof window === 'undefined') return fallback;
  const raw = getComputedStyle(document.documentElement).getPropertyValue('--' + name).trim();
  return channelsToRgba(raw, alpha) ?? fallback;
}

export function readChartPalette(theme: ResolvedTheme): ChartPalette {
  return {
    theme,
    background: readToken('chart-bg', '#1e2024'),
    text: readToken('content-400', '#8890a3'),
    strongText: readToken('content-100', '#f2f4f9'),
    grid: readTokenAlpha('chart-grid', 0.55, 'rgba(40, 46, 62, 0.55)'),
    crosshair: readToken('chart-crosshair', '#636e88'),
    border: readToken('surface-700', '#363d50'),
    panel: readToken('surface-900', '#181c27'),
    up: readToken('up', '#67d39a'),
    down: readToken('down', '#ef6b6b'),
    upBright: readToken('up-bright', '#7be0a7'),
    downBright: readToken('down-bright', '#f27878'),
    accent: readToken('accent', '#5b7cfa'),
    warn: readToken('warn', '#f6d84a'),
    brand: readToken('brand', '#f6d84a'),
    bull: readToken('chart-bull', '#62c88a'),
    bear: readToken('chart-bear', '#ea6d6d'),
    priceLine: readToken('chart-price-line', '#8a9099'),
    accentFill: [
      readTokenAlpha('accent', 0.28, 'rgba(91, 124, 250, 0.28)'),
      readTokenAlpha('accent', 0.02, 'rgba(91, 124, 250, 0.02)'),
    ],
  };
}

/**
 * Re-reads the palette whenever the theme changes.
 *
 * The values are read from the DOM rather than duplicated in JavaScript, so the
 * stylesheet stays the single source of truth for the palette.
 */
export function useChartPalette(): ChartPalette {
  const theme = useThemeStore((s) => s.resolved);
  const [palette, setPalette] = useState<ChartPalette>(() => readChartPalette(theme));

  useEffect(() => {
    // One frame after the attribute flips, so the new values are computed.
    const id = window.requestAnimationFrame(() => setPalette(readChartPalette(theme)));
    return () => window.cancelAnimationFrame(id);
  }, [theme]);

  return palette;
}

/**
 * Applies an alpha to a palette colour.
 *
 * The palette hands out hex or `rgba(...)`; both have to keep working, and the
 * space-separated CSS Color Level 4 form must never be produced because
 * lightweight-charts cannot parse it.
 *
 * An alpha already carried by the colour is multiplied rather than replaced, so
 * dimming a token that is itself translucent dims it further.
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
      color.length === 4
        ? '#' + color[1]! + color[1]! + color[2]! + color[2]! + color[3]! + color[3]!
        : color;
    const r = parseInt(full.slice(1, 3), 16);
    const g = parseInt(full.slice(3, 5), 16);
    const b = parseInt(full.slice(5, 7), 16);
    return 'rgba(' + r + ', ' + g + ', ' + b + ', ' + clamped + ')';
  }

  return color;
}
