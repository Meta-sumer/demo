import type { Indicator } from '../../../stores/settingsStore';

/**
 * The indicators the advanced chart offers, as the picker describes them.
 *
 * Six to start, not the thirty-odd some platforms list (record 09): the set a
 * trader who uses indicators expects to find - moving averages, Bollinger
 * Bands, RSI, MACD and the stochastic - with fixed, conventional parameters.
 * Descriptions say what each one measures, never what to do about it.
 */
export type IndicatorCategory = 'trend' | 'momentum' | 'volatility';

export interface IndicatorInfo {
  id: Indicator;
  name: string;
  /** The abbreviation traders use, shown in the badge and on the chart. */
  short: string;
  /** The fixed parameters, as they are usually written. */
  params: string;
  category: IndicatorCategory;
  /** On the price itself, or in a band of its own under it. */
  placement: 'overlay' | 'pane';
  description: string;
  /** Other words someone might search for it by. */
  keywords: string[];
}

export const INDICATOR_CATEGORIES: Array<{ id: IndicatorCategory; label: string; hint: string }> = [
  { id: 'trend', label: 'Trend', hint: 'Averages that smooth the price into its direction' },
  { id: 'momentum', label: 'Momentum', hint: 'Oscillators: how fast and how far the price has moved' },
  { id: 'volatility', label: 'Volatility', hint: 'How widely the price is swinging' },
];

export const INDICATOR_CATALOG: Record<Indicator, IndicatorInfo> = {
  sma: {
    id: 'sma',
    name: 'Simple moving average',
    short: 'SMA',
    params: '20',
    category: 'trend',
    placement: 'overlay',
    description: 'The average close of the last 20 candles, drawn on the price.',
    keywords: ['moving average', 'ma', 'mean'],
  },
  ema: {
    id: 'ema',
    name: 'Exponential moving average',
    short: 'EMA',
    params: '50',
    category: 'trend',
    placement: 'overlay',
    description: 'An average of the last 50 candles that weights the newest most, so it turns sooner.',
    keywords: ['moving average', 'ma', 'exponential'],
  },
  bollinger: {
    id: 'bollinger',
    name: 'Bollinger Bands',
    short: 'BB',
    params: '20, 2',
    category: 'volatility',
    placement: 'overlay',
    description: 'A 20-candle average with bands two standard deviations either side. The bands widen as swings grow.',
    keywords: ['bands', 'deviation', 'envelope', 'volatility'],
  },
  rsi: {
    id: 'rsi',
    name: 'Relative Strength Index',
    short: 'RSI',
    params: '14',
    category: 'momentum',
    placement: 'pane',
    description: 'The balance of recent gains and losses on a 0-100 scale, with lines at 70 and 30.',
    keywords: ['relative strength', 'oscillator', 'overbought', 'oversold', 'wilder'],
  },
  macd: {
    id: 'macd',
    name: 'MACD',
    short: 'MACD',
    params: '12, 26, 9',
    category: 'momentum',
    placement: 'pane',
    description: 'The gap between a fast and a slow average, its signal line, and bars for the difference.',
    keywords: ['moving average convergence divergence', 'convergence', 'divergence', 'signal', 'histogram'],
  },
  stochastic: {
    id: 'stochastic',
    name: 'Stochastic Oscillator',
    short: 'Stoch',
    params: '14, 3, 3',
    category: 'momentum',
    placement: 'pane',
    description: 'Where the close sits in the range of the last 14 candles, 0-100, with lines at 80 and 20.',
    keywords: ['stochastic', 'oscillator', '%k', '%d', 'kd'],
  },
};

/** Catalogue order: the order the picker lists them in, and the order the bands stack in. */
export const INDICATOR_ORDER: Indicator[] = ['sma', 'ema', 'bollinger', 'rsi', 'macd', 'stochastic'];

export type Oscillator = 'rsi' | 'macd' | 'stochastic';

export function isOscillator(id: Indicator): id is Oscillator {
  return INDICATOR_CATALOG[id].placement === 'pane';
}

/**
 * Indicators matching a search, in catalogue order.
 *
 * Every word typed has to appear somewhere in the name, the abbreviation or
 * the keywords, so "moving" finds both averages and "rsi 14" still finds RSI.
 */
export function searchIndicators(query: string): IndicatorInfo[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return INDICATOR_ORDER.map((id) => INDICATOR_CATALOG[id]);
  return INDICATOR_ORDER.map((id) => INDICATOR_CATALOG[id]).filter((info) => {
    const haystack = [info.name, info.short, info.params, CATEGORY_LABEL[info.category], ...info.keywords]
      .join(' ')
      .toLowerCase();
    return words.every((word) => haystack.includes(word));
  });
}

const CATEGORY_LABEL: Record<IndicatorCategory, string> = {
  trend: 'trend',
  momentum: 'momentum oscillator',
  volatility: 'volatility',
};
