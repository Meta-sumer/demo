import { useMemo } from 'react';
import { Platform } from 'react-native';
import { matchFont, type SkFont } from '@shopify/react-native-skia';

/**
 * The fonts the chart draws its own text in.
 *
 * No font files are bundled; these are the platform's sans-serif faces, which
 * carry every character the canvas writes (digits, ASCII, the separators).
 * Anything with a currency symbol is rendered by React Native instead, where
 * the system falls back across fonts.
 */
const FAMILY = Platform.select({ ios: 'Helvetica', default: 'sans-serif' });

export interface ChartFonts {
  /** Axis labels and tags. */
  axis: SkFont;
  /** Bold small text: order countdowns and price tabs. */
  bold: SkFont;
}

let shared: ChartFonts | null = null;

export function chartFonts(): ChartFonts {
  if (!shared) {
    shared = {
      axis: matchFont({ fontFamily: FAMILY, fontSize: 10 }),
      bold: matchFont({ fontFamily: FAMILY, fontSize: 10, fontWeight: 'bold' }),
    };
  }
  return shared;
}

export function useChartFonts(): ChartFonts {
  return useMemo(chartFonts, []);
}

/** A heavy face at an arbitrary size - the DEMO watermark. */
export function useDisplayFont(size: number): SkFont {
  const rounded = Math.max(8, Math.round(size));
  return useMemo(() => matchFont({ fontFamily: FAMILY, fontSize: rounded, fontWeight: '800' }), [rounded]);
}
