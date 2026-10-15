import type { ChartPalette } from '../../../../utils/theme-colors';

/**
 * Text colour for a label drawn on a coloured tag.
 *
 * lightweight-charts picks black or white for its axis labels from the
 * background's brightness; this is the same rule, so a last-price tag in the
 * light theme's dark green gets white text and the dark theme's pale green
 * gets black.
 */
export function contrastText(background: string): string {
  const rgb = parseColor(background);
  if (!rgb) return '#ffffff';
  const brightness = 0.199 * rgb[0] + 0.687 * rgb[1] + 0.114 * rgb[2];
  return brightness > 160 ? '#000000' : '#ffffff';
}

function parseColor(color: string): [number, number, number] | null {
  if (color.startsWith('#')) {
    const hex = color.length === 4 ? color[1]! + color[1]! + color[2]! + color[2]! + color[3]! + color[3]! : color.slice(1, 7);
    if (hex.length !== 6) return null;
    return [parseInt(hex.slice(0, 2), 16), parseInt(hex.slice(2, 4), 16), parseInt(hex.slice(4, 6), 16)];
  }
  const match = /rgba?\(([^)]+)\)/.exec(color);
  if (!match) return null;
  const parts = match[1]!.split(',').map((part) => Number(part.trim()));
  if (parts.length < 3 || parts.slice(0, 3).some((part) => !Number.isFinite(part))) return null;
  return [parts[0]!, parts[1]!, parts[2]!];
}

/* --------------------------------------------------------- the chart's look */


/** TradingView's candle colours, the same in every theme. */
export const TV_UP = '#089981';
export const TV_DOWN = '#F23645';

/**
 * The price chart's palette: the theme's, dressed the way the TradingView
 * mobile app draws its charts.
 *
 * Kept here rather than in the theme tokens, which mirror the web
 * stylesheet value for value. Dark is a pure black plot with TradingView's
 * greys; light is its white plot; twilight keeps its navy ground with the
 * same structure. The candles are TradingView's green and red everywhere,
 * unless the trader picked colours of their own in Settings, which win.
 */
export interface ChartLook extends ChartPalette {
  /** Round price labels, emphasised over the rest. */
  axisStrong: string;
  /** Text on a tag in the candle colours. */
  candleText: string;
  /** Hairlines between the chart and its toolbar. */
  rule: string;
}

export function chartLook(theme: ChartPalette, upOverride: string | null, downOverride: string | null): ChartLook {
  const candles = {
    bull: upOverride ?? TV_UP,
    bear: downOverride ?? TV_DOWN,
    up: upOverride ?? theme.up,
    down: downOverride ?? theme.down,
  };
  switch (theme.theme) {
    case 'dark':
      return {
        ...theme,
        ...candles,
        background: '#000000',
        grid: 'rgba(255, 255, 255, 0.16)',
        text: '#b2b5be',
        axisStrong: '#e0e3eb',
        border: 'rgba(255, 255, 255, 0.10)',
        crosshair: '#787b86',
        priceLine: '#787b86',
        candleText: '#ffffff',
        rule: 'rgba(255, 255, 255, 0.10)',
      };
    case 'light':
      return {
        ...theme,
        ...candles,
        background: '#ffffff',
        grid: 'rgba(19, 23, 34, 0.14)',
        text: '#787b86',
        axisStrong: '#131722',
        border: 'rgba(19, 23, 34, 0.10)',
        crosshair: '#9598a1',
        priceLine: '#9598a1',
        candleText: '#ffffff',
        rule: 'rgba(19, 23, 34, 0.10)',
      };
    case 'twilight':
    default:
      return {
        ...theme,
        ...candles,
        axisStrong: theme.strongText,
        candleText: '#ffffff',
        rule: theme.border,
      };
  }
}
