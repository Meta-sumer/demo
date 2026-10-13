import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { SYSTEM_ZONE } from '../utils/time';

/** Timeframes offered by the chart toolbar, in seconds. Must match the server's set. */
export const TIMEFRAMES = [5, 15, 30, 60, 300, 900, 1800, 3600, 10800, 86400] as const;
export type Timeframe = (typeof TIMEFRAMES)[number];

/**
 * Indicators the chart can draw from the candle series alone - no extra feed.
 *
 * The first three sit on the price itself; RSI, MACD and the stochastic are
 * oscillators with scales of their own, drawn in a band under the price. The
 * catalogue the picker reads (names, categories, parameters) is kept with the
 * chart, in `features/trade/chart/indicatorCatalog.ts`.
 */
export const INDICATORS = ['sma', 'ema', 'bollinger', 'rsi', 'macd', 'stochastic'] as const;
export type Indicator = (typeof INDICATORS)[number];

export function isIndicator(value: unknown): value is Indicator {
  return (INDICATORS as readonly unknown[]).includes(value);
}

export type ChartType = 'candles' | 'line' | 'bars' | 'area';

/**
 * Beginner shows one instrument and the essential chart controls; advanced adds
 * instrument tabs, indicators, drawing tools, split charts and market
 * sentiment. Same visual language, different amount of it (brief section 27).
 */
export type TradeMode = 'beginner' | 'advanced';

interface SettingsState {
  /** Default stake in MINOR units. */
  defaultStake: number;
  defaultExpirySeconds: number;
  soundEnabled: boolean;
  /** Show the confirmation sheet before a trade is sent. Off = one-tap trading. */
  confirmBeforeTrade: boolean;
  tradeMode: TradeMode;
  chartType: ChartType;
  timeframe: Timeframe;

  /** Chart appearance. `gridOpacity` is a 0-10 dial, not a percentage. */
  gridOpacity: number;
  /** IANA zone, or SYSTEM_ZONE. Display only - the server always decides in UTC. */
  timezone: string;
  performanceMode: boolean;
  autoScroll: boolean;
  showOrderLabels: boolean;
  hideTradeResults: boolean;
  /** null means "follow the theme". Hex strings otherwise. */
  upColorOverride: string | null;
  downColorOverride: string | null;

  indicators: Indicator[];
  /**
   * The line on the chart where a trade placed now would expire, drawn before
   * anything is placed. On by default: it is how the chosen duration shows on
   * the chart (brief UX-07).
   */
  showExpiryGuide: boolean;

  setDefaultStake: (minor: number) => void;
  setDefaultExpiry: (seconds: number) => void;
  toggleSound: () => void;
  toggleConfirm: () => void;
  setTradeMode: (mode: TradeMode) => void;
  setChartType: (type: ChartType) => void;
  setTimeframe: (timeframe: Timeframe) => void;
  setGridOpacity: (value: number) => void;
  setTimezone: (zone: string) => void;
  togglePerformanceMode: () => void;
  toggleAutoScroll: () => void;
  toggleOrderLabels: () => void;
  toggleHideTradeResults: () => void;
  setUpColor: (hex: string | null) => void;
  setDownColor: (hex: string | null) => void;
  toggleIndicator: (indicator: Indicator) => void;
  removeIndicator: (indicator: Indicator) => void;
  clearIndicators: () => void;
  toggleExpiryGuide: () => void;
  resetChartAppearance: () => void;
}

export const GRID_OPACITY_MAX = 10;

const APPEARANCE_DEFAULTS = {
  gridOpacity: 7,
  timezone: SYSTEM_ZONE,
  performanceMode: false,
  autoScroll: true,
  showOrderLabels: true,
  hideTradeResults: false,
  upColorOverride: null,
  downColorOverride: null,
  indicators: [] as Indicator[],
  showExpiryGuide: true,
};

export const useSettingsStore = create<SettingsState>()(
  persist(
    (set) => ({
      defaultStake: 500_00,
      defaultExpirySeconds: 60,
      soundEnabled: true,
      confirmBeforeTrade: true,
      tradeMode: 'beginner',
      chartType: 'candles',
      timeframe: 60,
      ...APPEARANCE_DEFAULTS,

      setDefaultStake: (minor) => set({ defaultStake: minor }),
      setDefaultExpiry: (seconds) => set({ defaultExpirySeconds: seconds }),
      toggleSound: () => set((s) => ({ soundEnabled: !s.soundEnabled })),
      toggleConfirm: () => set((s) => ({ confirmBeforeTrade: !s.confirmBeforeTrade })),
      setTradeMode: (tradeMode) => set({ tradeMode }),
      setChartType: (chartType) => set({ chartType }),
      setTimeframe: (timeframe) => set({ timeframe }),
      setGridOpacity: (gridOpacity) =>
        set({ gridOpacity: Math.min(GRID_OPACITY_MAX, Math.max(0, Math.round(gridOpacity))) }),
      setTimezone: (timezone) => set({ timezone }),
      togglePerformanceMode: () => set((s) => ({ performanceMode: !s.performanceMode })),
      toggleAutoScroll: () => set((s) => ({ autoScroll: !s.autoScroll })),
      toggleOrderLabels: () => set((s) => ({ showOrderLabels: !s.showOrderLabels })),
      toggleHideTradeResults: () => set((s) => ({ hideTradeResults: !s.hideTradeResults })),
      setUpColor: (upColorOverride) => set({ upColorOverride }),
      setDownColor: (downColorOverride) => set({ downColorOverride }),
      toggleIndicator: (indicator) =>
        set((s) => ({
          indicators: s.indicators.includes(indicator)
            ? s.indicators.filter((entry) => entry !== indicator)
            : [...s.indicators, indicator],
        })),
      removeIndicator: (indicator) =>
        set((s) => ({ indicators: s.indicators.filter((entry) => entry !== indicator) })),
      clearIndicators: () => set({ indicators: [] }),
      toggleExpiryGuide: () => set((s) => ({ showExpiryGuide: !s.showExpiryGuide })),
      resetChartAppearance: () => set({ ...APPEARANCE_DEFAULTS }),
    }),
    {
      name: 'bt.settings',
      version: 5,
      migrate: (persisted, from) => {
        // v1 had no appearance keys; fill them rather than dropping the user's
        // stake and expiry defaults.
        const merged = { ...APPEARANCE_DEFAULTS, ...(persisted as Record<string, unknown>) };

        // v2 stored grid opacity as a percentage. Rescale to the 0-10 dial
        // instead of leaving a 40 that would clamp to full brightness.
        if (from < 3 && typeof merged.gridOpacity === 'number' && merged.gridOpacity > GRID_OPACITY_MAX) {
          merged.gridOpacity = Math.round((merged.gridOpacity / 100) * GRID_OPACITY_MAX);
        }
        // v3 offered a 10-second frame. It is gone, and the server's candle
        // endpoint now rejects it, so anyone still holding it would get an
        // empty chart and no clue why. Move them to the nearest frame that
        // still exists. The same guard covers any future removal.
        const stored = merged as Record<string, unknown>;
        if (!(TIMEFRAMES as readonly number[]).includes(stored.timeframe as number)) {
          stored.timeframe = 15;
        }
        // v5 made confirmation the default (brief UX-11) and added the
        // beginner/advanced split. Everyone starts from the new defaults once;
        // one-tap trading and advanced mode are a switch away. Existing users
        // had every tool on screen, so they land in advanced.
        if (from < 5) {
          stored.confirmBeforeTrade = true;
          stored.tradeMode = 'advanced';
        }
        return merged as unknown as SettingsState;
      },
    },
  ),
);

/** Short label for the toolbar button, which is only 32px wide. */
export function timeframeLabel(seconds: number): string {
  if (seconds < 60) return seconds + 's';
  if (seconds < 3600) return seconds / 60 + 'm';
  if (seconds < 86400) return seconds / 3600 + 'h';
  return seconds / 86400 + 'd';
}

/**
 * Full name, for the picker.
 *
 * The menu has the room the button does not, and "15 minutes" is read without
 * decoding where "15m" has to be. Singular where it matters: "1 minute", not
 * "1 minutes".
 */
export function timeframeName(seconds: number): string {
  const [count, unit] =
    seconds < 60
      ? [seconds, 'second']
      : seconds < 3600
        ? [seconds / 60, 'minute']
        : seconds < 86400
          ? [seconds / 3600, 'hour']
          : [seconds / 86400, 'day'];
  return count + ' ' + unit + (count === 1 ? '' : 's');
}
