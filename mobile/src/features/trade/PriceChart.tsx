import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Modal as RNModal, StyleSheet, View, type LayoutChangeEvent } from 'react-native';
import { Canvas, createPicture, Picture, type SkPicture } from '@shopify/react-native-skia';
import { GestureDetector, GestureHandlerRootView } from 'react-native-gesture-handler';
import { runOnJS, useAnimatedReaction, useDerivedValue, useSharedValue, type SharedValue } from 'react-native-reanimated';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useMarketStore } from '../../stores/marketStore';
import { useAccountStore } from '../../stores/accountStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { useOrderPreviewStore } from '../../stores/orderPreviewStore';
import {
  GRID_OPACITY_MAX,
  isIndicator,
  useSettingsStore,
  type Indicator,
  type TradeMode,
} from '../../stores/settingsStore';
import { useDrawingsStore } from '../../stores/drawingsStore';
import { Skeleton, Text } from '../../components/ui';
import { useCountdown } from '../../hooks/useCountdown';
import { useChartPalette, withAlpha, type ChartPalette } from '../../utils/theme-colors';
import { bollinger, ema, sma, type Point } from '../../utils/indicators';
import { shouldRun } from '../../utils/redraw';
import { formatCountdown, formatMoney } from '../../utils/format';
import { formatClock, resolveZone, zoneChip } from '../../utils/time';
import { cn } from '../../utils/cn';
import { BEGINNER_CHART_TYPES, ChartToolbar } from './ChartToolbar';
import { ChartOrderLayer } from './orders/ChartOrderLayer';
import { useTicketStore } from './ticketStore';
import { ChartZoomControls } from './chart/ChartZoomControls';
import { DrawingLayer } from './chart/DrawingLayer';
import { ExpiryGuide } from './chart/ExpiryGuide';
import { IndicatorPicker } from './chart/IndicatorPicker';
import { INDICATOR_CATALOG, INDICATOR_ORDER, isOscillator, type Oscillator } from './chart/indicatorCatalog';
import { OscillatorPane } from './chart/OscillatorPane';
import { clamp } from './chart/useFrameLoop';
import { Pulse } from './chart/Pulse';
import { useChartEngine } from './chart/engine/useChartEngine';
import { useChartFonts, useDisplayFont } from './chart/engine/fonts';
import { contrastText } from './chart/engine/colors';
import { buildZoneInfo, priceText } from './chart/engine/format';
import { paintMain, type MainStyle } from './chart/engine/mainPainter';
import type { ChartHost, LayerKey, TapHandler } from './chart/engine/host';
import { UTC_ZONE, type OverlayLine, type SeriesData, type ZoneInfo } from './chart/engine/types';
import type { Asset, Candle, Trade } from '../../types/api';

/**
 * Price chart.
 *
 * History comes from the server's OHLC endpoint at the selected timeframe; the
 * live tick stream then updates the bucket in progress. Orders - the preview
 * the trader is about to place and the positions already open - are drawn by
 * `ChartOrderLayer` as a layer of their own over the candles, so pressing an
 * order can never disturb a candle.
 *
 * Three more things sit over the price the same way: the expiry guide (where
 * a trade placed now would end), the trader's own drawings, and the zoom
 * controls. Oscillators get bands of their own underneath.
 *
 * The canvas is Skia, driven by the chart engine (./chart/engine): the
 * viewport lives on the UI thread, so panning, pinching and the live edge
 * scrolling run at the display's rate without a React render per frame.
 *
 * Every price shown here is the server's. Nothing drawn on this chart is ever
 * sent back as an input to a trade - the client renders what it is told.
 */
interface Props {
  asset: Asset | null;
  openTrades: Trade[];
  /**
   * Beginner draws the price and the orders only. Indicators and hand-drawn
   * lines the trader saved stay saved, and come back in advanced mode.
   */
  mode: TradeMode;
}

/** Shared empty set, so beginner mode does not hand the effects a new array every render. */
const NO_INDICATORS: readonly Indicator[] = Object.freeze([]);

interface Ohlc {
  time: number;
  open: number;
  high: number;
  low: number;
  close: number;
}

/**
 * How strongly "DEMO" shows behind the candles, per theme.
 *
 * Tuned by eye against each theme's background: enough to be read at a
 * glance on every screen, faint enough that no candle, level or label is ever
 * harder to read because of it.
 */
const WATERMARK_ALPHA: Record<ChartPalette['theme'], number> = { dark: 0.06, twilight: 0.075, light: 0.055 };

/** The price scale's empty bands above and below the data, as on the web. */
const MARGINS = { top: 0.12, bottom: 0.12 };

/** Where the toolbar and the zoom controls sit: just above the 28px time axis. */
const CONTROLS_BOTTOM = 36;

const DAY = 86_400_000;

function toSeries(bars: ReadonlyArray<Ohlc>, bucket: number): SeriesData {
  const n = bars.length;
  const t = new Array<number>(n);
  const o = new Array<number>(n);
  const h = new Array<number>(n);
  const l = new Array<number>(n);
  const c = new Array<number>(n);
  for (let i = 0; i < n; i += 1) {
    const bar = bars[i]!;
    t[i] = bar.time;
    o[i] = bar.open;
    h[i] = bar.high;
    l[i] = bar.low;
    c[i] = bar.close;
  }
  return { t, o, h, l, c, bucket };
}

/** An indicator's points laid onto the bars' slots, NaN where it has no value yet. */
function aligned(points: Point[], bars: ReadonlyArray<Ohlc>): number[] {
  const byTime = new Map(points.map((point) => [point.time, point.value]));
  return bars.map((bar) => byTime.get(bar.time) ?? Number.NaN);
}

export function PriceChart({ asset, openTrades, mode }: Props) {
  const advanced = mode === 'advanced';

  const barsRef = useRef<Ohlc[]>([]);
  const lastBucketRef = useRef<Ohlc | null>(null);
  /**
   * The bucket width the series currently holds, which is not always the one
   * the settings say. Between a timeframe change and its refetch landing they
   * differ, and that gap is where a mis-bucketed tick would corrupt the chart.
   */
  const seriesFrameRef = useRef(0);
  /**
   * Performance mode throttles the indicator recompute, which walks the whole
   * visible series on every tick. A change the trader actually made, like
   * toggling an indicator, still applies at once: only the tick-driven repeats
   * are skipped, so the feature costs latency on redraws and never on input.
   */
  const indicatorRunRef = useRef({ at: 0, key: '' });

  const [hoveredIndex, setHoveredIndex] = useState(-1);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [fullscreen, setFullscreen] = useState(false);
  const [box, setBox] = useState({ w: 0, h: 0 });
  const [axisWidth, setAxisWidth] = useState(56);
  const [toolbarRight, setToolbarRight] = useState(0);

  const storedChartType = useSettingsStore((s) => s.chartType);
  const chartType = advanced || BEGINNER_CHART_TYPES.includes(storedChartType) ? storedChartType : 'candles';
  const timeframe = useSettingsStore((s) => s.timeframe);
  const gridOpacity = useSettingsStore((s) => s.gridOpacity);
  const autoScroll = useSettingsStore((s) => s.autoScroll);
  const showOrderLabels = useSettingsStore((s) => s.showOrderLabels);
  const hideTradeResults = useSettingsStore((s) => s.hideTradeResults);
  const upOverride = useSettingsStore((s) => s.upColorOverride);
  const downOverride = useSettingsStore((s) => s.downColorOverride);
  const storedIndicators = useSettingsStore((s) => s.indicators);
  const indicators = useMemo<readonly Indicator[]>(
    // Filtered on the way in: a list saved by another build may name an
    // indicator this one does not know.
    () => (advanced ? storedIndicators.filter(isIndicator) : NO_INDICATORS),
    [advanced, storedIndicators],
  );
  const oscillators = useMemo(
    () => INDICATOR_ORDER.filter((id): id is Oscillator => indicators.includes(id) && isOscillator(id)),
    [indicators],
  );
  const removeIndicator = useSettingsStore((s) => s.removeIndicator);
  const timezonePreference = useSettingsStore((s) => s.timezone);
  const performanceMode = useSettingsStore((s) => s.performanceMode);
  const showExpiryGuide = useSettingsStore((s) => s.showExpiryGuide);

  const demo = useAccountStore((s) => s.active) === 'DEMO';
  const drawingHere = useDrawingsStore((s) => advanced && !!asset && s.armed?.assetId === asset.id);

  const themePalette = useChartPalette();
  const palette = useMemo<ChartPalette>(
    () => ({
      ...themePalette,
      up: upOverride ?? themePalette.up,
      down: downOverride ?? themePalette.down,
      // Candles carry their own pair so an order accent stays distinguishable
      // from the bar underneath it. An explicit override still wins for both.
      bull: upOverride ?? themePalette.bull,
      bear: downOverride ?? themePalette.bear,
    }),
    [themePalette, upOverride, downOverride],
  );

  const tick = useMarketStore((s) => (asset ? s.ticks[asset.id] : undefined));
  const bucketMs = timeframe * 1000;
  const zone = resolveZone(timezonePreference);
  const digits = asset?.pricePrecision ?? 5;

  /*
   * The expiry guide (records 07 and 10) belongs to the chart the ticket
   * trades. In the advanced split view on the web the second chart is
   * view-only, and a guide there would promise an expiry nobody can place.
   */
  const expirySeconds = useTicketStore((s) => s.expirySeconds);
  const tradedAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const previewHere = useOrderPreviewStore((s) => s.preview !== null && s.preview.assetId === asset?.id);
  const guideSeconds =
    showExpiryGuide &&
    asset &&
    asset.marketOpen &&
    (tradedAssetId === null || tradedAssetId === asset.id) &&
    asset.supportedExpiries.includes(expirySeconds)
      ? expirySeconds
      : null;

  const candlesQuery = useQuery({
    queryKey: ['candles', asset?.id, timeframe],
    queryFn: () => api.market.candles(asset!.id, 300, timeframe),
    enabled: !!asset,
    // Re-pull one bucket's worth apart, capped so short frames do not hammer.
    refetchInterval: Math.max(15_000, bucketMs),
  });

  const relevantTrades = useMemo(
    () => (asset ? openTrades.filter((trade) => trade.assetId === asset.id) : []),
    [openTrades, asset],
  );

  /* ------------------------------------------------------------ the engine */

  const tapHandlers = useRef(new Map<string, { priority: number; handler: TapHandler }>());
  const panHandlers = useRef(new Map<string, () => void>());

  const engine = useChartEngine({
    // `barSpacing` is the resting width of one bar. Six is too tight for a
    // platform that draws orders on top of the candles: an order's run is only
    // as wide as the time between its entry and its expiry, so at six pixels
    // a one-bar trade collapses into its own marker. Ten still leaves plenty
    // of history on a phone, and the trader can zoom from there.
    initialSpacing: 10,
    restingOffset: 6,
    minSpacing: 0.5,
    maxSpacing: 50,
    axisH: 28,
    margins: MARGINS,
    onTap: (x, y) => {
      const ordered = [...tapHandlers.current.values()].sort((a, b) => b.priority - a.priority);
      for (const entry of ordered) if (entry.handler(x, y)) return;
      // Nothing claimed it: the crosshair, as a tap on lightweight-charts'
      // touch surface does. A second tap puts it away.
      if (engine.crosshair.value.visible) engine.hideCrosshair();
      else engine.showCrosshairAt(x, y);
    },
    onPanBegin: () => {
      for (const handler of panHandlers.current.values()) handler();
    },
  });

  const [layers, setLayers] = useState<Partial<Record<LayerKey, SharedValue<SkPicture>>>>({});
  const host = useMemo<ChartHost>(
    () => ({
      engine,
      setLayer: (key, picture) =>
        setLayers((current) => {
          if ((current[key] ?? null) === picture) return current;
          const next = { ...current };
          if (picture) next[key] = picture;
          else delete next[key];
          return next;
        }),
      setTap: (key, priority, handler) => {
        if (handler) tapHandlers.current.set(key, { priority, handler });
        else tapHandlers.current.delete(key);
      },
      setPanBegin: (key, handler) => {
        if (handler) panHandlers.current.set(key, handler);
        else panHandlers.current.delete(key);
      },
    }),
    [engine],
  );

  /* ------------------------------------------------------- what is drawn */

  const style = useSharedValue<MainStyle | null>(null);
  const overlays = useSharedValue<OverlayLine[]>([]);
  const zoneInfo = useSharedValue<ZoneInfo>(UTC_ZONE);
  const fonts = useChartFonts();
  // Sized to the plot, so it reads the same on any phone.
  const watermarkFont = useDisplayFont(clamp(Math.round(Math.max(0, box.w - axisWidth) * 0.17), 44, 150));

  // Theme, grid opacity, colour overrides, the representation and the mark.
  useEffect(() => {
    const accent = palette.accent;
    style.value = {
      kind: chartType,
      background: palette.background,
      grid: withAlpha(palette.grid, gridOpacity / GRID_OPACITY_MAX),
      text: palette.text,
      border: palette.border,
      crosshair: palette.crosshair,
      crosshairText: contrastText(palette.crosshair),
      bull: palette.bull,
      bear: palette.bear,
      bullText: contrastText(palette.bull),
      bearText: contrastText(palette.bear),
      accent,
      accentText: contrastText(accent),
      accentTop: palette.accentFill[0],
      accentBottom: palette.accentFill[1],
      priceLine: palette.priceLine,
      watermark: demo ? withAlpha(palette.strongText, WATERMARK_ALPHA[palette.theme]) : null,
      digits,
      lastPrice: true,
    };
    engine.closeOnly.value = chartType === 'line' || chartType === 'area';
    engine.minMove.value = 1 / 10 ** digits;
  }, [palette, gridOpacity, chartType, demo, digits, style, engine]);

  const { view, series, crosshair } = engine;
  const axisFont = fonts.axis;
  const basePicture = useDerivedValue(
    () =>
      createPicture((canvas) => {
        const current = style.value;
        if (!current) return;
        paintMain(canvas, view.value, series.value, current, overlays.value, crosshair.value, zoneInfo.value, {
          axis: axisFont,
          watermark: watermarkFont,
        });
      }),
    [axisFont, watermarkFont],
  );

  /* ------------------------------------------------------ the price axis */

  /*
   * Oscillator bands sit under the price with price axes of their own, and
   * every axis is made as wide as the widest of them, so bar i sits at the
   * same x in every band.
   */
  const mainAxisRef = useRef(0);
  const paneAxesRef = useRef(new Map<Oscillator, number>());
  const applyAxisWidth = useCallback(() => {
    const widest = Math.max(44, mainAxisRef.current, ...paneAxesRef.current.values());
    const rounded = Math.ceil(widest);
    engine.axisW.value = rounded;
    setAxisWidth(rounded);
  }, [engine]);

  const measureMainAxis = useCallback(
    (bars: ReadonlyArray<Ohlc>) => {
      let widest = 0;
      for (const bar of bars) widest = Math.max(widest, Math.abs(bar.high), Math.abs(bar.low));
      const width = axisFont.getTextWidth(priceText(widest, digits)) + 16;
      if (Math.abs(width - mainAxisRef.current) < 0.5) return;
      mainAxisRef.current = width;
      applyAxisWidth();
    },
    [axisFont, digits, applyAxisWidth],
  );

  const handlePaneAxis = useCallback(
    (kind: Oscillator, width: number | null) => {
      if (width === null) paneAxesRef.current.delete(kind);
      else paneAxesRef.current.set(kind, width);
      applyAxisWidth();
    },
    [applyAxisWidth],
  );

  useEffect(() => {
    measureMainAxis(barsRef.current);
  }, [measureMainAxis]);

  /* --------------------------------------------------------- the time zone */

  const zoneKeyRef = useRef('');
  const refreshZone = useCallback(
    (bars: ReadonlyArray<Ohlc>) => {
      const first = bars[0]?.time ?? Date.now();
      const last = Math.max(bars[bars.length - 1]?.time ?? Date.now(), Date.now());
      const from = Math.floor(first / DAY) * DAY - 2 * DAY;
      const to = Math.floor(last / DAY) * DAY + 3 * DAY;
      const key = zone + '|' + from + '|' + to;
      if (key === zoneKeyRef.current) return;
      zoneKeyRef.current = key;
      zoneInfo.value = buildZoneInfo(zone, from, to);
    },
    [zone, zoneInfo],
  );

  // Axis and crosshair labels follow the chosen zone. The data is untouched -
  // only how each instant is written changes.
  useEffect(() => {
    refreshZone(barsRef.current);
  }, [refreshZone]);

  /*
   * Load history whenever the asset or the timeframe changes.
   *
   * This also runs on every background refetch, which is why the jump back to
   * the live edge is conditional. Scrolling unconditionally meant that a
   * trader who had panned into history to read a past move was dragged back to
   * the present a few seconds later, with no idea what had grabbed the chart.
   * A new instrument or timeframe is a new dataset and should start at the
   * live edge; the same dataset arriving again should leave the view alone
   * unless the trader was already there.
   */
  const datasetKeyRef = useRef('');
  useEffect(() => {
    const candles: Candle[] | undefined = candlesQuery.data;
    if (!candles) return;

    const key = (asset?.id ?? '') + '|' + String(timeframe);
    const switched = datasetKeyRef.current !== key;
    datasetKeyRef.current = key;

    const following = engine.isFollowing();

    barsRef.current = candles.map((candle) => ({ ...candle }));
    lastBucketRef.current = barsRef.current[barsRef.current.length - 1] ?? null;
    engine.series.value = toSeries(barsRef.current, bucketMs);
    // From here the series holds this frame, so ticks may be folded again.
    seriesFrameRef.current = bucketMs;

    // A new instrument can have much narrower prices than the last; let the
    // axis find its own width again before the bands are lined up.
    if (switched) mainAxisRef.current = 0;
    measureMainAxis(barsRef.current);
    refreshZone(barsRef.current);

    if (autoScroll && (switched || following)) engine.scrollToRealTime();
  }, [candlesQuery.data, asset?.id, timeframe, bucketMs, autoScroll, engine, measureMainAxis, refreshZone]);

  /*
   * Fold each live tick into the bucket currently being built.
   *
   * The guard below is the important part. Changing the timeframe changes
   * `bucketMs` immediately, but the series still holds the old frame's bars
   * until the refetch lands. A tick arriving in that window is bucketed with
   * the new width - `floor(now / 3h)` can be nearly three hours in the past -
   * and handing the series a bar older than its newest one would corrupt it.
   *
   * So a tick is only folded once the series is known to hold this frame, and
   * never backwards. Skipping is safe - the refetch that is already in flight
   * carries the bucket this tick belongs to.
   */
  useEffect(() => {
    if (!tick) return;
    if (seriesFrameRef.current !== bucketMs) return;

    const openTime = Math.floor(tick.timestamp / bucketMs) * bucketMs;
    const previous = lastBucketRef.current;
    if (previous && openTime < previous.time) return;

    const bucket: Ohlc =
      previous && previous.time === openTime
        ? {
            time: openTime,
            open: previous.open,
            high: Math.max(previous.high, tick.price),
            low: Math.min(previous.low, tick.price),
            close: tick.price,
          }
        : { time: openTime, open: tick.price, high: tick.price, low: tick.price, close: tick.price };

    lastBucketRef.current = bucket;

    const bars = barsRef.current;
    if (bars.length > 0 && bars[bars.length - 1]!.time === openTime) bars[bars.length - 1] = bucket;
    else bars.push(bucket);

    const { time, open, high, low, close } = bucket;
    const { rightOffset, view: viewValue } = engine;
    engine.series.modify((data) => {
      'worklet';
      if (data.bucket !== bucketMs) return data;
      const n = data.t.length;
      if (n > 0 && data.t[n - 1] === time) {
        data.o[n - 1] = open;
        data.h[n - 1] = high;
        data.l[n - 1] = low;
        data.c[n - 1] = close;
      } else if (n === 0 || time > data.t[n - 1]!) {
        // A new bar moves a view that shows the newest one along with it,
        // and leaves a view parked in history where it is.
        const current = viewValue.value;
        const offset = rightOffset.value;
        const newestVisible = offset >= -0.5 && offset * current.s <= current.pw;
        data.t.push(time);
        data.o.push(open);
        data.h.push(high);
        data.l.push(low);
        data.c.push(close);
        if (!newestVisible) rightOffset.value = offset + 1;
      }
      return data;
    });

    if (Math.abs(tick.price) > 0) {
      const widest = axisFont.getTextWidth(priceText(Math.abs(tick.price), digits)) + 16;
      if (widest > mainAxisRef.current + 0.5) {
        mainAxisRef.current = widest;
        applyAxisWidth();
      }
    }
    // The tick itself is the trigger; everything else is read through refs.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [tick, bucketMs]);

  /*
   * Indicators drawn on the price itself, recomputed from whatever is on
   * screen. The oscillators are the bands' own work, from the same bars.
   *
   * Each has its own colour, because two lines in one colour are one line to
   * the eye: the moving average in the warning tone, the exponential one in a
   * soft version of the strongest text colour, the bands in the crosshair
   * grey. None of them is the brand yellow of a placed order.
   */
  useEffect(() => {
    // Anything the trader changed, or a new history load, must apply at once.
    const key = indicators.join(',') + '|' + palette.theme + '|' + String(candlesQuery.dataUpdatedAt);
    if (!shouldRun(indicatorRunRef, key, performanceMode)) return;

    const source = barsRef.current;
    const bars = source.map((bar) => ({ time: bar.time, close: bar.close }));
    const lines: OverlayLine[] = [];

    if (indicators.includes('sma')) {
      lines.push({ v: aligned(sma(bars, 20), source), color: palette.warn, dashed: false });
    }
    if (indicators.includes('ema')) {
      lines.push({ v: aligned(ema(bars, 50), source), color: withAlpha(palette.strongText, 0.6), dashed: false });
    }
    if (indicators.includes('bollinger')) {
      const bands = bollinger(bars, 20, 2);
      const colour = withAlpha(palette.crosshair, 0.9);
      lines.push({ v: aligned(bands.upper, source), color: colour, dashed: true });
      lines.push({ v: aligned(bands.middle, source), color: colour, dashed: false });
      lines.push({ v: aligned(bands.lower, source), color: colour, dashed: true });
    }

    overlays.value = lines;
    engine.extents.value = lines.map((entry) => entry.v);
  }, [indicators, candlesQuery.data, candlesQuery.dataUpdatedAt, tick, palette, performanceMode, overlays, engine]);

  /*
   * Hand-drawn lines and orders are layers.
   *
   * On the web they moved out of the charting library's canvas: a price line
   * runs the full width of the plot and cannot stop at an expiry, a series
   * marker is attached to a candle, and a price line belongs to one series -
   * switch to a line chart and every level on the hidden candle series
   * vanished. `ChartOrderLayer` draws the orders and `DrawingLayer` the
   * drawings, whichever representation is showing.
   */

  // Beginner mode shows no drawings; a tool left waiting or a selection left
  // behind must not carry on invisibly.
  useEffect(() => {
    if (advanced) return;
    const drawings = useDrawingsStore.getState();
    if (drawings.armed) drawings.disarm();
    if (drawings.selectedId) drawings.select(null);
  }, [advanced]);

  // A tool picked on one instrument is put down when this chart moves to
  // another - its taps were meant for the candles that were on screen.
  const previousAssetRef = useRef(asset?.id ?? null);
  useEffect(() => {
    const previous = previousAssetRef.current;
    previousAssetRef.current = asset?.id ?? null;
    const drawings = useDrawingsStore.getState();
    if (previous && previous !== asset?.id && drawings.armed?.assetId === previous) drawings.disarm();
  }, [asset?.id]);

  // OHLC readout follows the crosshair; falls back to the newest bar. The
  // oscillator bands show the same instant, from the same shared crosshair.
  useAnimatedReaction(
    () => (crosshair.value.visible ? crosshair.value.index : -1),
    (now, before) => {
      if (now !== before) runOnJS(setHoveredIndex)(now);
    },
  );

  // A new dataset puts the crosshair away: its index meant another bar.
  useEffect(() => {
    engine.hideCrosshair();
  }, [asset?.id, timeframe, engine]);

  const hovered = hoveredIndex >= 0 ? (barsRef.current[hoveredIndex] ?? null) : null;
  const readout = hovered ?? lastBucketRef.current;

  const soonest = relevantTrades.reduce<Trade | null>(
    (best, trade) => (!best || trade.expiryTimestamp < best.expiryTimestamp ? trade : best),
    null,
  );

  const overlayIds = indicators.filter((id) => !isOscillator(id));
  const overlayColour: Partial<Record<Indicator, string>> = {
    sma: palette.warn,
    ema: withAlpha(palette.strongText, 0.6),
    bollinger: withAlpha(palette.crosshair, 0.9),
  };

  const onMainLayout = useCallback(
    (event: LayoutChangeEvent) => {
      const { width, height } = event.nativeEvent.layout;
      engine.size.value = { w: width, h: height };
      setBox((current) => (current.w === width && current.h === height ? current : { w: width, h: height }));
    },
    [engine],
  );

  const historyKey = (asset?.id ?? '') + '|' + String(timeframe) + '|' + String(candlesQuery.dataUpdatedAt);

  const body = (
    <View className="flex-1 bg-surface-900">
      {/* The price, and everything drawn over it. Oscillator bands follow
          underneath, so this box takes whatever height they leave - but never
          less than the readouts, the toolbar and a usable plot need. */}
      <View className="relative min-h-[160px] flex-1" onLayout={onMainLayout}>
        <GestureDetector gesture={engine.gesture}>
          <View style={StyleSheet.absoluteFill} collapsable={false}>
            <Canvas style={StyleSheet.absoluteFill}>
              <Picture picture={basePicture} />
              {layers.drawings ? <Picture picture={layers.drawings} /> : null}
              {layers.guide ? <Picture picture={layers.guide} /> : null}
              {layers.orders ? <Picture picture={layers.orders} /> : null}
            </Canvas>
          </View>
        </GestureDetector>

        {candlesQuery.isLoading ? (
          <View pointerEvents="none" className="absolute inset-0 items-center justify-center bg-surface-900/60">
            <Skeleton className="h-[70%] w-[92%]" />
          </View>
        ) : null}

        {/* Where a trade placed now would end. The preview takes its place
            while it is on the chart: the same line, with a direction and an
            amount. */}
        {guideSeconds !== null && !previewHere ? (
          <ExpiryGuide
            host={host}
            expirySeconds={guideSeconds}
            zone={zone}
            palette={palette}
            performanceMode={performanceMode}
          />
        ) : null}

        {/* Live figures the canvas does not draw itself. */}
        <View pointerEvents="none" className="absolute left-3 top-3 gap-1.5" style={{ maxWidth: Math.max(120, box.w - axisWidth - 24) }}>
          {readout ? (
            <View className="flex-row flex-wrap items-center gap-x-2.5 gap-y-1 self-start rounded-md bg-surface-900/85 px-2 py-1">
              <OhlcValue label="O" value={readout.open} digits={digits} />
              <OhlcValue label="H" value={readout.high} digits={digits} />
              <OhlcValue label="L" value={readout.low} digits={digits} />
              <OhlcValue
                label="C"
                value={readout.close}
                digits={digits}
                tone={readout.close >= readout.open ? palette.up : palette.down}
              />
            </View>
          ) : null}

          {tick ? (
            <View className="flex-row items-center gap-1.5 self-start rounded-md bg-surface-900/85 px-2 py-1">
              <View className="h-1.5 w-1.5 rounded-full bg-up" />
              <Text className="text-[11px] font-medium text-content-300 tnum">
                {formatClock(tick.timestamp, zone)} {zoneChip(zone)}
              </Text>
            </View>
          ) : null}

          {/* Which line is which, in the colour it is drawn in. */}
          {overlayIds.length > 0 ? (
            <View className="flex-row flex-wrap items-center gap-x-2.5 gap-y-1 self-start rounded-md bg-surface-900/85 px-2 py-1">
              {overlayIds.map((id) => (
                <View key={id} className="flex-row items-center gap-1">
                  <View className="h-0.5 w-3 rounded-full" style={{ backgroundColor: overlayColour[id] }} />
                  <Text className="text-[10px] font-semibold text-content-300">{INDICATOR_CATALOG[id].short}</Text>
                  <Text className="text-[10px] font-medium text-content-400">{INDICATOR_CATALOG[id].params}</Text>
                </View>
              ))}
            </View>
          ) : null}

          {soonest && !hideTradeResults ? <ExpiryBadge trade={soonest} /> : null}
        </View>

        <ChartToolbar
          assetId={asset?.id ?? null}
          mode={mode}
          fullscreen={fullscreen}
          onToggleFullscreen={() => setFullscreen((current) => !current)}
          onRowLayout={setToolbarRight}
          indicatorsOpen={pickerOpen}
          onOpenIndicators={() => setPickerOpen(true)}
        />

        <ChartZoomControls engine={engine} toolbarRight={toolbarRight} bottom={CONTROLS_BOTTOM} />

        {/* Orders: the preview being considered and every position open on this
            instrument, each with its own entry line, marker and expiry. Above
            the toolbar: a marker that cannot be reached is worse than a
            toolbar button covered for a moment. */}
        <ChartOrderLayer
          host={host}
          asset={asset}
          palette={palette}
          zone={zone}
          barsRef={barsRef}
          bucketMs={bucketMs}
          hideTrades={hideTradeResults}
          showLabels={showOrderLabels}
          performanceMode={performanceMode}
          guideSeconds={guideSeconds}
          passThrough={drawingHere}
        />

        {/* Hand-drawn lines: advanced only. In beginner mode they are kept,
            not drawn. */}
        {advanced && asset ? (
          <DrawingLayer
            host={host}
            assetId={asset.id}
            barsRef={barsRef}
            bucketMs={bucketMs}
            palette={palette}
            zone={zoneInfo}
            digits={digits}
          />
        ) : null}
      </View>

      {oscillators.map((kind) => (
        <OscillatorPane
          key={kind}
          kind={kind}
          engine={engine}
          zone={zoneInfo}
          barsRef={barsRef}
          historyKey={historyKey}
          tickKey={tick?.timestamp ?? 0}
          palette={palette}
          gridOpacity={gridOpacity}
          digits={digits}
          performanceMode={performanceMode}
          onAxisWidth={handlePaneAxis}
          onRemove={removeIndicator}
        />
      ))}

      {/* A dialog, rendered here rather than in the toolbar so it belongs to
          the chart rather than to one row of buttons. */}
      {advanced ? <IndicatorPicker open={pickerOpen} onClose={() => setPickerOpen(false)} /> : null}
    </View>
  );

  if (!fullscreen) return body;

  /*
   * Fullscreen: the same chart over the whole screen. The engine and the data
   * live in this component, so the view the trader had - zoom, scroll,
   * crosshair - carries across; only the canvas is mounted again inside the
   * modal. The system back gesture, or the same button, brings it back.
   */
  return (
    <>
      <View className="min-h-[160px] flex-1 bg-surface-900" />
      <RNModal
        visible
        animationType="fade"
        statusBarTranslucent
        supportedOrientations={['portrait', 'landscape']}
        onRequestClose={() => setFullscreen(false)}
      >
        <GestureHandlerRootView style={{ flex: 1 }}>
          <SafeAreaView style={{ flex: 1, backgroundColor: palette.panel }}>{body}</SafeAreaView>
        </GestureHandlerRootView>
      </RNModal>
    </>
  );
}

function OhlcValue({ label, value, digits, tone }: { label: string; value: number; digits: number; tone?: string }) {
  return (
    <View className="flex-row items-baseline gap-1">
      <Text className="text-[11px] font-medium text-content-400">{label}</Text>
      <Text className="text-[11px] font-semibold text-content-100 tnum" style={tone ? { color: tone } : undefined}>
        {value.toFixed(digits)}
      </Text>
    </View>
  );
}

/**
 * The nearest expiry on this instrument, as a chip.
 *
 * Duplicates the countdown that sits on the order's own expiry line, on
 * purpose: that line can be scrolled or zoomed off screen, and the trader
 * still has to be able to see how long is left.
 */
function ExpiryBadge({ trade }: { trade: Trade }) {
  const remaining = useCountdown(trade.expiryTimestamp);
  const urgent = remaining <= 10_000;

  return (
    <View className="flex-row items-center gap-2 self-start rounded-md bg-surface-900/85 px-2 py-1">
      <Text className={cn('text-[11px] font-bold', trade.direction === 'UP' ? 'text-up' : 'text-down')}>
        {trade.direction === 'UP' ? '▲' : '▼'}
      </Text>
      <Text className="text-[11px] font-bold text-content-200 tnum">
        {formatMoney(trade.stake, trade.currency)}
        <Text className="text-[11px] font-bold text-content-400">{'  ->  '}</Text>
        <Text className="text-[11px] font-bold text-brand">{formatMoney(trade.potentialReturn, trade.currency)}</Text>
      </Text>
      <Pulse active={urgent}>
        <Text className={cn('text-[11px] font-bold tnum', urgent ? 'text-brand' : 'text-content-100')}>
          {formatCountdown(remaining)}
        </Text>
      </Pulse>
    </View>
  );
}
