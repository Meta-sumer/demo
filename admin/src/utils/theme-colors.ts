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
  accent: string;
  warn: string;
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
    background: 'transparent',
    text: readToken('content-400', '#8890a3'),
    strongText: readToken('content-100', '#f2f4f9'),
    grid: readTokenAlpha('chart-grid', 0.55, 'rgba(40, 46, 62, 0.55)'),
    crosshair: readToken('chart-crosshair', '#636e88'),
    border: readToken('surface-700', '#363d50'),
    panel: readToken('surface-900', '#181c27'),
    up: readToken('up', '#22c55e'),
    down: readToken('down', '#f04444'),
    accent: readToken('accent', '#5b7cfa'),
    warn: readToken('warn', '#f59e0b'),
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
