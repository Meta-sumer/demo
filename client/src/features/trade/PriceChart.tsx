import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  createChart,
  LineStyle,
  type BarData,
  type CandlestickData,
  type IChartApi,
  type ISeriesApi,
  type LineData,
  type LogicalRange,
  type MouseEventParams,
  type UTCTimestamp,
} from 'lightweight-charts';
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
import { Skeleton } from '../../components/ui';
import { useCountdown } from '../../hooks/useCountdown';
import { useChartPalette, withAlpha, type ChartPalette } from '../../utils/theme-colors';
import { bollinger, ema, sma } from '../../utils/indicators';
import { shouldRun } from '../../utils/redraw';
import { formatCountdown, formatMoney } from '../../utils/format';
import { formatClock, formatDayTime, formatHourMinute, resolveZone, zoneChip } from '../../utils/time';
import { BEGINNER_CHART_TYPES, ChartToolbar } from './ChartToolbar';
import { ChartOrderLayer } from './orders/ChartOrderLayer';
import { useTicketStore } from './ticketStore';
import { ChartZoomControls } from './chart/ChartZoomControls';
import { DrawingLayer } from './chart/DrawingLayer';
import { ExpiryGuide } from './chart/ExpiryGuide';
import { IndicatorPicker } from './chart/IndicatorPicker';
import { INDICATOR_CATALOG, INDICATOR_ORDER, isOscillator, type Oscillator } from './chart/indicatorCatalog';
import { OscillatorPane, type PaneHandle } from './chart/OscillatorPane';
import { lastAtOrBefore } from './chart/timeProjection';
import { clamp, isFollowingRealTime } from './chart/useFrameLoop';
import type { Asset, Candle, Trade } from '../../types/api';

/**
 * Price chart.
 *
 * History comes from the server's OHLC endpoint at the selected timeframe; the
 * live tick stream then updates the bucket in progress. Orders - the preview
 * the trader is about to place and the positions already open - are drawn by
 * `ChartOrderLayer` on top of the canvas rather than into it, so hovering an
 * order can never disturb a candle.
 *
 * Three more things sit over the canvas the same way: the expiry guide (where
 * a trade placed now would end), the trader's own drawings, and the zoom
 * controls. Oscillators get bands of their own underneath.
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
const WATERMARK_FONT = '"Inter var", Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

export function PriceChart({ asset, openTrades, mode }: Props) {
  const advanced = mode === 'advanced';
  const rootRef = useRef<HTMLDivElement | null>(null);
  const containerRef = useRef<HTMLDivElement | null>(null);
  const toolbarRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  /**
   * The chart, as state as well as a ref.
   *
   * Children that subscribe to the chart need to know when it exists. Their
   * effects run before this component's, so on the first pass the ref is
   * still empty; the state arrives one render later and carries them in.
   */
  const [chartApi, setChartApi] = useState<IChartApi | null>(null);
  const candleSeriesRef = useRef<ISeriesApi<'Candlestick'> | null>(null);
  const barSeriesRef = useRef<ISeriesApi<'Bar'> | null>(null);
  const lineSeriesRef = useRef<ISeriesApi<'Line'> | null>(null);
  const areaSeriesRef = useRef<ISeriesApi<'Area'> | null>(null);
  const smaSeriesRef = useRef<ISeriesApi<'Line'> | null>(null);
  const emaSeriesRef = useRef<ISeriesApi<'Line'> | null>(null);
  const bandSeriesRef = useRef<Array<ISeriesApi<'Line'>>>([]);

  /**
   * Performance mode throttles the indicator recompute, which walks the whole
   * visible series on every tick. A change the trader actually made, like
   * toggling an indicator, still applies at once: only the tick-driven repeats
   * are skipped, so the feature costs latency on redraws and never on input.
   */
  const indicatorRunRef = useRef({ at: 0, key: '' });

  const lastBucketRef = useRef<Ohlc | null>(null);
  const barsRef = useRef<Ohlc[]>([]);
  /**
   * The bucket width the series currently holds, which is not always the one
   * the settings say. Between a timeframe change and its refetch landing they
   * differ, and that gap is where a mis-bucketed tick would crash the chart.
   */
  const seriesFrameRef = useRef(0);
  const [hovered, setHovered] = useState<Ohlc | null>(null);
  const [pickerOpen, setPickerOpen] = useState(false);

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
  const fullscreen = useFullscreen(rootRef);

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
   * trades. In the advanced split view the second chart is view-only, and a
   * guide there would promise an expiry nobody can place.
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

  // Create the chart once and keep it across asset changes.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const chart = createChart(container, {
      layout: { background: { color: palette.background }, textColor: palette.text, fontSize: 11 },
      grid: { vertLines: { color: palette.grid }, horzLines: { color: palette.grid } },
      rightPriceScale: { borderColor: palette.border, scaleMargins: { top: 0.12, bottom: 0.12 } },
      /*
       * `barSpacing` is the resting width of one bar, and the library's
       * default of 6 is too tight for a platform that draws orders on top of
       * the candles: an order's run is only as wide as the time between its
       * entry and its expiry, so at six pixels a one-bar trade collapses into
       * its own marker. Ten still leaves well over a hundred bars of history
       * on a desktop plot, and the trader can zoom from there.
       */
      timeScale: {
        borderColor: palette.border,
        timeVisible: true,
        secondsVisible: false,
        rightOffset: 6,
        barSpacing: 10,
      },
      crosshair: { mode: 1, vertLine: { color: palette.crosshair }, horzLine: { color: palette.crosshair } },
      handleScale: { axisPressedMouseMove: { time: true, price: false } },
      autoSize: true,
    });

    chartRef.current = chart;
    candleSeriesRef.current = chart.addCandlestickSeries({});
    barSeriesRef.current = chart.addBarSeries({ visible: false });
    lineSeriesRef.current = chart.addLineSeries({ visible: false, lineWidth: 2 });
    areaSeriesRef.current = chart.addAreaSeries({ visible: false, lineWidth: 2 });
    setChartApi(chart);

    return () => {
      setChartApi(null);
      chart.remove();
      chartRef.current = null;
      candleSeriesRef.current = null;
      barSeriesRef.current = null;
      lineSeriesRef.current = null;
      areaSeriesRef.current = null;
      smaSeriesRef.current = null;
      emaSeriesRef.current = null;
      bandSeriesRef.current = [];
    };
  }, []);

  // Theme, grid opacity and colour overrides.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    const grid = withAlpha(palette.grid, gridOpacity / GRID_OPACITY_MAX);

    chart.applyOptions({
      layout: { background: { color: palette.background }, textColor: palette.text },
      grid: { vertLines: { color: grid }, horzLines: { color: grid } },
      rightPriceScale: { borderColor: palette.border },
      timeScale: { borderColor: palette.border },
      crosshair: { vertLine: { color: palette.crosshair }, horzLine: { color: palette.crosshair } },
    });

    const bicolour = {
      upColor: palette.bull,
      downColor: palette.bear,
      borderUpColor: palette.bull,
      borderDownColor: palette.bear,
      wickUpColor: palette.bull,
      wickDownColor: palette.bear,
      priceLineColor: palette.priceLine,
    };

    candleSeriesRef.current?.applyOptions(bicolour);
    barSeriesRef.current?.applyOptions({ upColor: palette.bull, downColor: palette.bear });
    lineSeriesRef.current?.applyOptions({ color: palette.accent });
    areaSeriesRef.current?.applyOptions({
      lineColor: palette.accent,
      topColor: palette.accentFill[0],
      bottomColor: palette.accentFill[1],
    });
  }, [palette, gridOpacity]);

  // Axis and crosshair labels follow the chosen zone. The data is untouched -
  // only how each instant is written changes.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;

    chart.applyOptions({
      localization: { timeFormatter: (time: unknown) => formatClock((time as number) * 1000, zone) },
      timeScale: {
        tickMarkFormatter: (time: unknown, tickMarkType: number) => {
          const ms = (time as number) * 1000;
          // Types 0-2 are year/month/day marks; those need the date shown.
          return tickMarkType <= 2 ? formatDayTime(ms, zone) : formatHourMinute(ms, zone);
        },
      },
    });
  }, [zone]);

  /*
   * "DEMO" behind the candles (record 20).
   *
   * The header's demo marker scrolls away on a phone; the chart does not, so
   * the one thing a practising trader always has in view says it is practice.
   * It is the library's own watermark, drawn under the series, sized to the
   * plot so it reads the same on a phone as on a desktop.
   */
  useEffect(() => {
    const chart = chartApi;
    if (!chart) return;
    const scale = chart.timeScale();
    const apply = () => {
      chart.applyOptions({
        watermark: {
          visible: demo,
          text: 'DEMO',
          color: withAlpha(palette.strongText, WATERMARK_ALPHA[palette.theme]),
          fontSize: clamp(Math.round(scale.width() * 0.17), 44, 150),
          fontStyle: '800',
          fontFamily: WATERMARK_FONT,
          horzAlign: 'center',
          vertAlign: 'center',
        },
      });
    };
    apply();
    scale.subscribeSizeChange(apply);
    return () => scale.unsubscribeSizeChange(apply);
  }, [chartApi, demo, palette]);

  // Show exactly one price series.
  useEffect(() => {
    candleSeriesRef.current?.applyOptions({ visible: chartType === 'candles' });
    barSeriesRef.current?.applyOptions({ visible: chartType === 'bars' });
    lineSeriesRef.current?.applyOptions({ visible: chartType === 'line' });
    areaSeriesRef.current?.applyOptions({ visible: chartType === 'area' });
  }, [chartType]);

  /** Pushes the working series into every visible representation at once. */
  const paint = useCallback((bars: Ohlc[]) => {
    const candles: CandlestickData<UTCTimestamp>[] = bars.map((bar) => ({
      time: Math.floor(bar.time / 1000) as UTCTimestamp,
      open: bar.open,
      high: bar.high,
      low: bar.low,
      close: bar.close,
    }));
    const closes: LineData<UTCTimestamp>[] = bars.map((bar) => ({
      time: Math.floor(bar.time / 1000) as UTCTimestamp,
      value: bar.close,
    }));

    candleSeriesRef.current?.setData(candles);
    barSeriesRef.current?.setData(candles as BarData<UTCTimestamp>[]);
    lineSeriesRef.current?.setData(closes);
    areaSeriesRef.current?.setData(closes);
  }, []);

  /*
   * Oscillator bands.
   *
   * Each band is its own small chart that follows this one's time scale (see
   * OscillatorPane). This chart is the only one the trader pans and zooms, so
   * its visible range is simply handed down; and every price axis is widened
   * to the widest of them, so bar i sits at the same x in every band.
   */
  const panesRef = useRef(new Map<Oscillator, PaneHandle>());

  const syncAxisWidths = useCallback(() => {
    const main = chartRef.current;
    if (!main) return;
    const panes = [...panesRef.current.values()].map((pane) => pane.chart);
    if (panes.length === 0) {
      if (main.options().rightPriceScale.minimumWidth !== 0) main.applyOptions({ rightPriceScale: { minimumWidth: 0 } });
      return;
    }
    const charts = [main, ...panes];
    const widest = Math.max(...charts.map((chart) => chart.priceScale('right').width()));
    for (const chart of charts) {
      if (chart.options().rightPriceScale.minimumWidth !== widest) {
        chart.applyOptions({ rightPriceScale: { minimumWidth: widest } });
      }
    }
  }, []);

  const handlePaneReady = useCallback(
    (kind: Oscillator, handle: PaneHandle | null) => {
      if (handle) {
        panesRef.current.set(kind, handle);
        const range = chartRef.current?.timeScale().getVisibleLogicalRange();
        if (range) handle.chart.timeScale().setVisibleLogicalRange(range);
        handle.chart.timeScale().subscribeSizeChange(syncAxisWidths);
      } else {
        panesRef.current.delete(kind);
      }
      window.requestAnimationFrame(syncAxisWidths);
    },
    [syncAxisWidths],
  );

  useEffect(() => {
    const chart = chartApi;
    if (!chart) return;
    const scale = chart.timeScale();
    const follow = (range: LogicalRange | null) => {
      if (!range) return;
      for (const pane of panesRef.current.values()) pane.chart.timeScale().setVisibleLogicalRange(range);
    };
    scale.subscribeVisibleLogicalRangeChange(follow);
    scale.subscribeSizeChange(syncAxisWidths);
    return () => {
      scale.unsubscribeVisibleLogicalRangeChange(follow);
      scale.unsubscribeSizeChange(syncAxisWidths);
    };
  }, [chartApi, syncAxisWidths]);

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
    const chart = chartRef.current;
    if (!candles || !candleSeriesRef.current || !chart) return;

    const key = (asset?.id ?? '') + '|' + String(timeframe);
    const switched = datasetKeyRef.current !== key;
    datasetKeyRef.current = key;

    const following = isFollowingRealTime(chart);

    barsRef.current = candles.map((candle) => ({ ...candle }));
    paint(barsRef.current);
    lastBucketRef.current = barsRef.current[barsRef.current.length - 1] ?? null;
    // From here the series holds this frame, so ticks may be folded again.
    seriesFrameRef.current = bucketMs;

    // A new instrument can have much narrower prices than the last; let every
    // axis find its own width again before the bands are lined up.
    if (switched) {
      for (const target of [chart, ...[...panesRef.current.values()].map((pane) => pane.chart)]) {
        target.applyOptions({ rightPriceScale: { minimumWidth: 0 } });
      }
      window.requestAnimationFrame(syncAxisWidths);
    }

    if (autoScroll && (switched || following)) chart.timeScale().scrollToRealTime();
  }, [candlesQuery.data, asset?.id, timeframe, paint, autoScroll, syncAxisWidths]);

  /*
   * Fold each live tick into the bucket currently being built.
   *
   * The guard below is the important part. Changing the timeframe changes
   * `bucketMs` immediately, but the series still holds the old frame's bars
   * until the refetch lands. A tick arriving in that window is bucketed with
   * the new width - `floor(now / 3h)` can be nearly three hours in the past -
   * and handing the series a bar older than its newest one makes
   * lightweight-charts throw "Cannot update oldest data", which takes the
   * whole chart down with it: the canvas and the toolbar disappear.
   *
   * So a tick is only folded once the series is known to hold this frame, and
   * never backwards. Skipping is safe - the refetch that is already in flight
   * carries the bucket this tick belongs to.
   */
  useEffect(() => {
    if (!tick || !candleSeriesRef.current) return;
    if (seriesFrameRef.current !== bucketMs) return;

    const openTime = Math.floor(tick.timestamp / bucketMs) * bucketMs;
    const time = Math.floor(openTime / 1000) as UTCTimestamp;
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

    const point = { time, open: bucket.open, high: bucket.high, low: bucket.low, close: bucket.close };
    candleSeriesRef.current.update(point);
    barSeriesRef.current?.update(point);
    lineSeriesRef.current?.update({ time, value: tick.price });
    areaSeriesRef.current?.update({ time, value: tick.price });
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
    const chart = chartRef.current;
    if (!chart) return;

    // Anything the trader changed, or a new history load, must apply at once.
    const key = indicators.join(',') + '|' + palette.theme + '|' + String(candlesQuery.dataUpdatedAt);
    if (!shouldRun(indicatorRunRef, key, performanceMode)) return;

    const bars = barsRef.current.map((bar) => ({ time: Math.floor(bar.time / 1000), close: bar.close }));
    const toLine = (points: Array<{ time: number; value: number }>): LineData<UTCTimestamp>[] =>
      points.map((point) => ({ time: point.time as UTCTimestamp, value: point.value }));

    if (indicators.includes('sma')) {
      smaSeriesRef.current ??= chart.addLineSeries({
        lineWidth: 1,
        priceLineVisible: false,
        lastValueVisible: false,
      });
      smaSeriesRef.current.applyOptions({ color: palette.warn });
      smaSeriesRef.current.setData(toLine(sma(bars, 20)));
    } else if (smaSeriesRef.current) {
      chart.removeSeries(smaSeriesRef.current);
      smaSeriesRef.current = null;
    }

    if (indicators.includes('ema')) {
      emaSeriesRef.current ??= chart.addLineSeries({
        lineWidth: 1,
        priceLineVisible: false,
        lastValueVisible: false,
      });
      emaSeriesRef.current.applyOptions({ color: withAlpha(palette.strongText, 0.6) });
      emaSeriesRef.current.setData(toLine(ema(bars, 50)));
    } else if (emaSeriesRef.current) {
      chart.removeSeries(emaSeriesRef.current);
      emaSeriesRef.current = null;
    }

    if (indicators.includes('bollinger')) {
      if (bandSeriesRef.current.length === 0) {
        bandSeriesRef.current = [0, 1, 2].map(() =>
          chart.addLineSeries({
            lineWidth: 1,
            priceLineVisible: false,
            lastValueVisible: false,
            lineStyle: LineStyle.Dashed,
          }),
        );
      }
      const bands = bollinger(bars, 20, 2);
      const colour = withAlpha(palette.crosshair, 0.9);
      bandSeriesRef.current[0]?.applyOptions({ color: colour });
      bandSeriesRef.current[1]?.applyOptions({ color: colour, lineStyle: LineStyle.Solid });
      bandSeriesRef.current[2]?.applyOptions({ color: colour });
      bandSeriesRef.current[0]?.setData(toLine(bands.upper));
      bandSeriesRef.current[1]?.setData(toLine(bands.middle));
      bandSeriesRef.current[2]?.setData(toLine(bands.lower));
    } else if (bandSeriesRef.current.length > 0) {
      for (const series of bandSeriesRef.current) chart.removeSeries(series);
      bandSeriesRef.current = [];
    }
  }, [indicators, candlesQuery.data, candlesQuery.dataUpdatedAt, tick, palette, performanceMode]);

  /*
   * Hand-drawn lines and orders are overlays.
   *
   * Orders used to be drawn into the canvas, as price lines with an arrow
   * marker on the entry bar, and hand-drawn levels as price lines on the
   * candle series. Both moved out: a price line runs the full width of the
   * plot and cannot stop at an expiry, a series marker is attached to a
   * candle so highlighting one meant redrawing the bar it sat on, and a price
   * line belongs to one series - switch to a line chart and every level on
   * the hidden candle series vanished. `ChartOrderLayer` draws the orders and
   * `DrawingLayer` the drawings, whichever series is showing.
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
  // another - its clicks were meant for the candles that were on screen.
  const previousAssetRef = useRef(asset?.id ?? null);
  useEffect(() => {
    const previous = previousAssetRef.current;
    previousAssetRef.current = asset?.id ?? null;
    const drawings = useDrawingsStore.getState();
    if (previous && previous !== asset?.id && drawings.armed?.assetId === previous) drawings.disarm();
  }, [asset?.id]);

  // OHLC readout follows the crosshair; falls back to the newest bar. The
  // oscillator bands show the same instant.
  useEffect(() => {
    const chart = chartApi;
    if (!chart) return;

    const handler = (params: MouseEventParams) => {
      if (params.time === undefined) {
        setHovered(null);
        for (const pane of panesRef.current.values()) pane.chart.clearCrosshairPosition();
        return;
      }
      const seconds = params.time as number;
      const bar = barsRef.current.find((entry) => Math.floor(entry.time / 1000) === seconds);
      setHovered(bar ?? null);
      for (const pane of panesRef.current.values()) pinCrosshair(pane.chart, pane.series, pane.valueAt(seconds), seconds);
    };

    chart.subscribeCrosshairMove(handler);
    return () => chart.unsubscribeCrosshairMove(handler);
  }, [chartApi]);

  /** The pointer over an oscillator band: the same instant on the price and on the other bands. */
  const handlePaneHover = useCallback((source: Oscillator, seconds: number | null) => {
    const main = chartRef.current;
    const series = candleSeriesRef.current;
    if (!main || !series) return;
    const others = [...panesRef.current.entries()].filter(([kind]) => kind !== source).map(([, pane]) => pane);

    if (seconds === null) {
      main.clearCrosshairPosition();
      for (const pane of others) pane.chart.clearCrosshairPosition();
      setHovered(null);
      return;
    }
    const bars = barsRef.current;
    const index = lastAtOrBefore(bars, seconds * 1000);
    const bar = index >= 0 ? bars[index]! : null;
    pinCrosshair(main, series, bar?.close ?? null, seconds);
    for (const pane of others) pinCrosshair(pane.chart, pane.series, pane.valueAt(seconds), seconds);
    setHovered(bar);
  }, []);

  const readout = hovered ?? lastBucketRef.current;

  const soonest = relevantTrades.reduce<Trade | null>(
    (best, trade) => (!best || trade.expiryTimestamp < best.expiryTimestamp ? trade : best),
    null,
  );

  const overlays = indicators.filter((id) => !isOscillator(id));
  const overlayColour: Partial<Record<Indicator, string>> = {
    sma: palette.warn,
    ema: withAlpha(palette.strongText, 0.6),
    bollinger: withAlpha(palette.crosshair, 0.9),
  };

  return (
    <div
      ref={rootRef}
      className={
        // The fallback for browsers without element fullscreen (iPhone Safari):
        // the chart covers the viewport instead.
        fullscreen.pseudo
          ? 'fixed inset-0 z-[70] flex h-[100dvh] w-screen flex-col bg-surface-900'
          : 'relative flex h-full w-full flex-col bg-surface-900'
      }
    >
      {/* The price, and everything drawn over it. Oscillator bands follow
          underneath, so this box takes whatever height they leave - but never
          less than the readouts, the toolbar and a usable plot need. On a
          phone that floor makes the chart grow instead, and the ticket moves
          down: a price squeezed flat by three bands is not worth the space. */}
      <div className="relative min-h-[220px] flex-1">
        {candlesQuery.isLoading ? (
          <div className="absolute inset-0 z-10 grid place-items-center bg-surface-900/60">
            <Skeleton className="h-[70%] w-[92%]" />
          </div>
        ) : null}

        <div ref={containerRef} className="absolute inset-0" />

        {/* Hand-drawn lines: advanced only. In beginner mode they are kept,
            not drawn. */}
        {advanced && asset ? (
          <DrawingLayer
            assetId={asset.id}
            chartRef={chartRef}
            seriesRef={candleSeriesRef}
            containerRef={containerRef}
            barsRef={barsRef}
            bucketMs={bucketMs}
            palette={palette}
            zone={zone}
            digits={digits}
            performanceMode={performanceMode}
          />
        ) : null}

        {/* Where a trade placed now would end. The preview takes its place
            while it is on the chart: the same line, with a direction and an
            amount. */}
        {guideSeconds !== null && !previewHere ? (
          <ExpiryGuide
            chartRef={chartRef}
            containerRef={containerRef}
            barsRef={barsRef}
            bucketMs={bucketMs}
            expirySeconds={guideSeconds}
            zone={zone}
            palette={palette}
            performanceMode={performanceMode}
          />
        ) : null}

        {/* Orders: the preview being considered and every position open on this
            instrument, each with its own entry line, marker and expiry. */}
        <ChartOrderLayer
          asset={asset}
          palette={palette}
          zone={zone}
          chartRef={chartRef}
          chartReady={chartApi !== null}
          seriesRef={candleSeriesRef}
          containerRef={containerRef}
          barsRef={barsRef}
          bucketMs={bucketMs}
          hideTrades={hideTradeResults}
          showLabels={showOrderLabels}
          performanceMode={performanceMode}
          guideSeconds={guideSeconds}
          passThrough={drawingHere}
        />

        {/* Live figures the chart library cannot draw itself. */}
        <div className="pointer-events-none absolute left-3 top-3 z-20 flex flex-col gap-1.5">
          {readout ? (
            <span className="inline-flex w-fit flex-wrap items-center gap-x-2.5 gap-y-1 rounded-md bg-surface-900/85 px-2 py-1 text-[11px] font-medium backdrop-blur">
              <Ohlc label="O" value={readout.open} digits={digits} />
              <Ohlc label="H" value={readout.high} digits={digits} />
              <Ohlc label="L" value={readout.low} digits={digits} />
              <Ohlc
                label="C"
                value={readout.close}
                digits={digits}
                tone={readout.close >= readout.open ? palette.up : palette.down}
              />
            </span>
          ) : null}

          {tick ? (
            <span className="inline-flex w-fit items-center gap-1.5 rounded-md bg-surface-900/85 px-2 py-1 text-[11px] font-medium text-content-300 backdrop-blur">
              <span className="h-1.5 w-1.5 rounded-full bg-up" />
              {formatClock(tick.timestamp, zone)} {zoneChip(zone)}
            </span>
          ) : null}

          {/* Which line is which, in the colour it is drawn in. */}
          {overlays.length > 0 ? (
            <span className="inline-flex w-fit flex-wrap items-center gap-x-2.5 gap-y-1 rounded-md bg-surface-900/85 px-2 py-1 text-[10px] font-semibold text-content-300 backdrop-blur">
              {overlays.map((id) => (
                <span key={id} className="inline-flex items-center gap-1 whitespace-nowrap">
                  <span className="h-0.5 w-3 rounded-full" style={{ backgroundColor: overlayColour[id] }} />
                  {INDICATOR_CATALOG[id].short}
                  <span className="font-medium text-content-400">{INDICATOR_CATALOG[id].params}</span>
                </span>
              ))}
            </span>
          ) : null}

          {soonest && !hideTradeResults ? <ExpiryBadge trade={soonest} /> : null}
        </div>

        <ChartToolbar
          assetId={asset?.id ?? null}
          mode={mode}
          fullscreen={fullscreen.active}
          onToggleFullscreen={fullscreen.toggle}
          rootRef={toolbarRef}
          indicatorsOpen={pickerOpen}
          onOpenIndicators={() => setPickerOpen(true)}
        />

        <ChartZoomControls chart={chartApi} toolbarRef={toolbarRef} bottom={36} />
      </div>

      {oscillators.map((kind) => (
        <OscillatorPane
          key={kind}
          kind={kind}
          barsRef={barsRef}
          historyKey={(asset?.id ?? '') + '|' + String(timeframe) + '|' + String(candlesQuery.dataUpdatedAt)}
          tickKey={tick?.timestamp ?? 0}
          palette={palette}
          gridOpacity={gridOpacity}
          zone={zone}
          digits={digits}
          performanceMode={performanceMode}
          mainChart={chartApi}
          onReady={handlePaneReady}
          onHover={handlePaneHover}
          onRemove={removeIndicator}
        />
      ))}

      {/* A dialog, rendered here rather than in the toolbar so no layer of the
          chart can paint over it - and inside the chart's box, so it still
          shows when the chart is fullscreen. */}
      {advanced ? <IndicatorPicker open={pickerOpen} onClose={() => setPickerOpen(false)} /> : null}
    </div>
  );
}

/**
 * Puts a chart's crosshair on an instant, for the synced bands.
 *
 * The library throws when asked for a position on a chart that has no data
 * yet, and a band that is still loading is not worth an error: it simply
 * shows no crosshair until it has something to show it on.
 */
function pinCrosshair(
  chart: IChartApi,
  series: ISeriesApi<'Line'> | ISeriesApi<'Histogram'> | ISeriesApi<'Candlestick'>,
  value: number | null,
  seconds: number,
) {
  if (value === null || !Number.isFinite(value)) {
    chart.clearCrosshairPosition();
    return;
  }
  try {
    chart.setCrosshairPosition(value, seconds as UTCTimestamp, series);
  } catch {
    chart.clearCrosshairPosition();
  }
}

function Ohlc({ label, value, digits, tone }: { label: string; value: number; digits: number; tone?: string }) {
  return (
    <span className="inline-flex items-baseline gap-1">
      <span className="text-content-400">{label}</span>
      <span className="tnum font-semibold" style={tone ? { color: tone } : undefined}>
        {value.toFixed(digits)}
      </span>
    </span>
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
    <span className="inline-flex w-fit items-center gap-2 rounded-md bg-surface-900/85 px-2 py-1 text-[11px] font-bold backdrop-blur">
      <span className={trade.direction === 'UP' ? 'text-up' : 'text-down'}>
        {trade.direction === 'UP' ? '▲' : '▼'}
      </span>
      <span className="tnum text-content-200">
        {formatMoney(trade.stake, trade.currency)}
        <span className="mx-1 text-content-400">{'->'}</span>
        <span className="text-brand">{formatMoney(trade.potentialReturn, trade.currency)}</span>
      </span>
      <span className={'tnum ' + (urgent ? 'text-brand animate-order-pulse' : 'text-content-100')}>
        {formatCountdown(remaining)}
      </span>
    </span>
  );
}

/**
 * Fullscreen for the chart's own box.
 *
 * The Fullscreen API where the browser has it for elements; where it does not
 * (Safari on iPhone only allows it for video), the box is pinned over the
 * viewport instead and Escape or the same button takes it back. Either way
 * the chart library's autoSize follows the new box.
 */
function useFullscreen(ref: React.RefObject<HTMLDivElement | null>) {
  const [native, setNative] = useState(false);
  const [pseudo, setPseudo] = useState(false);

  useEffect(() => {
    const onChange = () => setNative(document.fullscreenElement === ref.current && ref.current !== null);
    document.addEventListener('fullscreenchange', onChange);
    return () => document.removeEventListener('fullscreenchange', onChange);
  }, [ref]);

  useEffect(() => {
    if (!pseudo) return;
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setPseudo(false);
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [pseudo]);

  const toggle = useCallback(() => {
    const element = ref.current;
    if (!element) return;
    if (pseudo) {
      setPseudo(false);
      return;
    }
    if (document.fullscreenElement) {
      void document.exitFullscreen().catch(() => undefined);
      return;
    }
    if (typeof element.requestFullscreen === 'function' && document.fullscreenEnabled) {
      element.requestFullscreen().catch(() => setPseudo(true));
    } else {
      setPseudo(true);
    }
  }, [ref, pseudo]);

  return { active: native || pseudo, pseudo, toggle };
}
