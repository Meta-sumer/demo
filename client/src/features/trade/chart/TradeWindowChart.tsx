import { useCallback, useEffect, useRef, useState, type MutableRefObject } from 'react';
import {
  createChart,
  LineStyle,
  type AreaData,
  type CandlestickData,
  type IChartApi,
  type ISeriesApi,
  type IPriceLine,
  type UTCTimestamp,
  type WhitespaceData,
} from 'lightweight-charts';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import { Flag, Maximize, Minus, Plus, RefreshCw, Timer } from 'lucide-react';
import { api } from '../../../services/api';
import { useMarketStore } from '../../../stores/marketStore';
import { useMarketSubscription } from '../../../hooks/useRealtime';
import { Skeleton } from '../../../components/ui';
import { formatCountdown, formatPrice } from '../../../utils/format';
import { formatClock, formatDayTime, formatHourMinute } from '../../../utils/time';
import { statusTone } from '../../../utils/tradeStatus';
import { useChartPalette, withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import type { Candle, Trade } from '../../../types/api';
import { readLogicalAxis, timeToX, type Timed } from './timeProjection';
import {
  candlesForWindow,
  candleWindow,
  MAX_CANDLES,
  resampleTicks,
  tickRequestWindow,
  ticksReachEntry,
  ticksRetained,
  tickWindow,
  tradeMoments,
  type GridPoint,
  type RawTick,
  type TickWindow,
} from './tradeWindow';
import { clamp, useFrameLoop } from './useFrameLoop';

/**
 * The trade drawn against the price it was decided on (Deriv's "Contract
 * details").
 *
 * The line - from ticks while the server still has them, from one-minute
 * candles after that - with the trade's moments marked on it the way the
 * details panel lists them:
 *
 *   start      a dashed vertical at the entry, a stopwatch at its foot;
 *   entry spot a hollow circle on the price, and the entry price carried
 *              across as the barrier the result is measured against;
 *   exit spot  a filled dot, with its price in the colour of the result;
 *   expiry     a solid vertical with a flag at its foot.
 *
 * A running trade keeps drawing: the price is live and a countdown hangs on
 * the expiry line. Nothing here is an input to anything - the entry, the exit
 * and the result are the platform's figures, drawn where they happened.
 */
interface Props {
  trade: Trade;
  precision: number;
  zone: string;
}

type WindowData =
  | { kind: 'ticks'; ticks: RawTick[]; window: TickWindow }
  | { kind: 'candles'; candles: Candle[] }
  | { kind: 'none' };

/** How long each resample of a live line waits for more ticks. */
const LIVE_REDRAW_MS = 500;

/**
 * Fetches the window, choosing the source by age.
 *
 * Written to degrade against a server that does not yet understand `from` and
 * `to`: ticks are asked for with a trailing window long enough to reach the
 * trade and cut to it here, and candles are checked for actually containing
 * the trade - a server that ignored `to` answers with today's candles, and
 * drawing a trade on those would be drawing it on the wrong prices.
 */
async function loadWindow(trade: Trade): Promise<WindowData> {
  const now = Date.now();
  const window = tickWindow(trade);

  if (ticksRetained(window.from, now)) {
    try {
      const raw = await api.market.ticks(trade.assetId, {
        from: window.from,
        to: Math.min(window.to, now),
        windowMs: tickRequestWindow(window.from, now),
      });
      const ticks = raw
        .map((tick) => ({ timestamp: Number(tick.timestamp), price: Number(tick.price) }))
        .filter((tick) => Number.isFinite(tick.timestamp) && Number.isFinite(tick.price) && tick.timestamp <= window.to);
      if (ticksReachEntry(ticks, trade.entryTimestamp)) return { kind: 'ticks', ticks, window };
    } catch {
      // Candles are the fallback either way.
    }
  }

  const plan = candleWindow(trade, now);
  let candles = candlesForWindow(await api.market.candles(trade.assetId, plan.limit, 60, plan.to), plan, trade.entryTimestamp);
  if (!candles && plan.limit < MAX_CANDLES) {
    candles = candlesForWindow(await api.market.candles(trade.assetId, MAX_CANDLES, 60), plan, trade.entryTimestamp);
  }
  return candles ? { kind: 'candles', candles } : { kind: 'none' };
}

export function TradeWindowChart({ trade, precision, zone }: Props) {
  const palette = useChartPalette();
  // Owned here rather than by the canvas, so the zoom buttons can sit in the
  // bar under the chart instead of on top of the price.
  const chartRef = useRef<IChartApi | null>(null);
  const running = trade.status === 'OPEN' || trade.status === 'SETTLING';
  const query = useQuery({
    queryKey: ['trade-window', trade.id],
    queryFn: () => loadWindow(trade),
    // The past does not change. A running trade drawn from ticks is kept
    // current from the feed; one drawn from candles - a long trade whose
    // start has left the tick history - asks again every candle or so, so
    // its chart does not stop at the moment the page was opened.
    staleTime: Infinity,
    refetchInterval: (current) => (running && current.state.data?.kind === 'candles' ? 15_000 : false),
    refetchOnWindowFocus: false,
    retry: 1,
  });

  const data = query.data;
  return (
    <div className="flex h-full flex-col">
      <div className="relative min-h-[260px] flex-1 sm:min-h-[340px]">
        {query.isLoading ? (
          <div className="absolute inset-0 grid place-items-center p-4">
            <Skeleton className="h-full w-full" />
          </div>
        ) : query.isError ? (
          <ChartMessage
            title="The price history could not be loaded"
            body="The trade itself is unaffected - its figures are on this page."
            action={
              <button type="button" onClick={() => void query.refetch()} className="btn-ghost mt-3 h-9 px-3 text-xs">
                <RefreshCw className="h-3.5 w-3.5" />
                Try again
              </button>
            }
          />
        ) : !data || data.kind === 'none' ? (
          <ChartMessage
            title="Chart not available for this trade"
            body="The prices for this period are no longer kept. The trade's entry, expiry and result are listed on this page."
          />
        ) : (
          <WindowCanvas
            // The series is built for one kind of data; a change of source
            // gets a fresh chart rather than candles poured into a line.
            key={trade.id + ':' + data.kind}
            trade={trade}
            data={data}
            precision={precision}
            zone={zone}
            palette={palette}
            chartRef={chartRef}
          />
        )}
      </div>

      {/* What the markers are, where the line comes from, and the zoom -
          under the chart, where none of it can cover the price. */}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-surface-800 px-4 py-2">
        <Legend data={data} palette={palette} />
        {data && data.kind !== 'none' ? <WindowZoom chartRef={chartRef} /> : null}
      </div>
    </div>
  );
}

function ChartMessage({ title, body, action }: { title: string; body: string; action?: React.ReactNode }) {
  return (
    <div className="absolute inset-0 flex flex-col items-center justify-center px-6 text-center" role="status">
      <p className="text-sm font-semibold text-content-200">{title}</p>
      <p className="mt-1 max-w-sm text-xs leading-relaxed text-content-400">{body}</p>
      {action}
    </div>
  );
}

function Legend({ data, palette }: { data: WindowData | undefined; palette: ChartPalette }) {
  // Where the line comes from, so a trader comparing two trades knows why one
  // is a smooth line and the other is candles.
  const source =
    data?.kind === 'ticks'
      ? 'Tick prices, every ' + String(data.window.stepMs / 1000) + ' s'
      : data?.kind === 'candles'
        ? 'One-minute candles'
        : null;
  return (
    <div className="flex min-w-0 flex-1 flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-content-400">
      <span className="inline-flex items-center gap-1.5">
        <Timer className="h-3.5 w-3.5" /> Start
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className="h-3 w-3 rounded-full border-2" style={{ borderColor: palette.strongText }} /> Entry spot
      </span>
      <span className="inline-flex items-center gap-1.5">
        <span className="h-3 w-3 rounded-full" style={{ backgroundColor: palette.strongText }} /> Exit spot
      </span>
      <span className="inline-flex items-center gap-1.5">
        <Flag className="h-3.5 w-3.5" /> Expiry
      </span>
      {source ? <span className="basis-full text-[10px] sm:basis-auto">{source}</span> : null}
    </div>
  );
}

interface CanvasProps {
  trade: Trade;
  data: Exclude<WindowData, { kind: 'none' }>;
  precision: number;
  zone: string;
  palette: ChartPalette;
  chartRef: MutableRefObject<IChartApi | null>;
}

function WindowCanvas({ trade, data, precision, zone, palette, chartRef }: CanvasProps) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const seriesRef = useRef<ISeriesApi<'Area'> | ISeriesApi<'Candlestick'> | null>(null);
  const barrierRef = useRef<IPriceLine | null>(null);
  /** The slots on the time axis, in ms, for projecting the markers. */
  const slotsRef = useRef<Timed[]>([]);
  const stepRef = useRef(data.kind === 'ticks' ? data.window.stepMs : 60_000);
  const liveTicksRef = useRef<RawTick[]>([]);
  /**
   * Whether this chart has been fitted to the whole trade yet. Once it has, a
   * refresh keeps whatever view the trader zoomed to. It belongs to the chart
   * instance, not the component: a rebuilt chart (React's development
   * double-mount does exactly that) starts unfitted again.
   */
  const fittedRef = useRef(false);
  const tradeRef = useRef(trade);
  tradeRef.current = trade;

  const moments = tradeMoments(trade);
  const running = trade.status === 'OPEN';
  const windowEnd = data.kind === 'ticks' ? data.window.to : Number.NaN;
  // Live while the window is still being drawn: a running trade, or one that
  // ended moments ago and still has its margin to fill.
  const [live, setLive] = useState(() => data.kind === 'ticks' && Date.now() < windowEnd);
  useMarketSubscription(live ? trade.assetId : null);

  // The chart, once.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const chart = createChart(container, {
      layout: { background: { color: palette.background }, textColor: palette.text, fontSize: 11 },
      grid: {
        vertLines: { color: withAlpha(palette.grid, 0.6) },
        horzLines: { color: withAlpha(palette.grid, 0.6) },
      },
      rightPriceScale: { borderColor: palette.border, scaleMargins: { top: 0.16, bottom: 0.14 } },
      timeScale: {
        borderColor: palette.border,
        timeVisible: true,
        secondsVisible: data.kind === 'ticks',
        rightOffset: data.kind === 'ticks' ? 0 : 2,
        minBarSpacing: 0.2,
      },
      crosshair: { mode: 0, vertLine: { color: palette.crosshair }, horzLine: { color: palette.crosshair } },
      handleScale: { axisPressedMouseMove: { time: true, price: false } },
      localization: {
        timeFormatter: (time: unknown) => formatClock((time as number) * 1000, zone),
        priceFormatter: (price: number) => formatPrice(price, precision),
      },
      autoSize: true,
    });
    chart.applyOptions({
      timeScale: {
        tickMarkFormatter: (time: unknown, type: number) => {
          const ms = (time as number) * 1000;
          // 4 is a mark within a minute, 3 a minute; 0-2 open a new day.
          return type >= 4 ? formatClock(ms, zone) : type === 3 ? formatHourMinute(ms, zone) : formatDayTime(ms, zone);
        },
      },
    });
    const format = { type: 'price' as const, precision, minMove: 1 / 10 ** precision };
    seriesRef.current =
      data.kind === 'ticks'
        ? chart.addAreaSeries({ lineWidth: 2, priceFormat: format, crosshairMarkerRadius: 3 })
        : // A past window has no "last price": the newest candle here closed
          // long ago, and a live-looking price tab on it would be a lie.
          chart.addCandlestickSeries({ priceFormat: format, lastValueVisible: false, priceLineVisible: false });
    chartRef.current = chart;
    fittedRef.current = false;
    return () => {
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
      barrierRef.current = null;
    };
    // Built once per trade; the page keys this component on the trade id.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Colours follow the theme.
  useEffect(() => {
    const chart = chartRef.current;
    const series = seriesRef.current;
    if (!chart || !series) return;
    chart.applyOptions({
      layout: { background: { color: palette.background }, textColor: palette.text },
      grid: { vertLines: { color: withAlpha(palette.grid, 0.6) }, horzLines: { color: withAlpha(palette.grid, 0.6) } },
      rightPriceScale: { borderColor: palette.border },
      timeScale: { borderColor: palette.border },
      crosshair: { vertLine: { color: palette.crosshair }, horzLine: { color: palette.crosshair } },
    });
    if (data.kind === 'ticks') {
      (series as ISeriesApi<'Area'>).applyOptions({
        lineColor: palette.strongText,
        topColor: withAlpha(palette.strongText, 0.16),
        bottomColor: withAlpha(palette.strongText, 0.01),
        priceLineColor: palette.priceLine,
      });
    } else {
      (series as ISeriesApi<'Candlestick'>).applyOptions({
        upColor: palette.bull,
        downColor: palette.bear,
        borderUpColor: palette.bull,
        borderDownColor: palette.bear,
        wickUpColor: palette.bull,
        wickDownColor: palette.bear,
      });
    }

    // The entry price, carried across as the barrier.
    if (barrierRef.current) series.removePriceLine(barrierRef.current);
    barrierRef.current = series.createPriceLine({
      price: trade.entryPrice,
      color: withAlpha(palette.strongText, 0.7),
      lineWidth: 1,
      lineStyle: LineStyle.Solid,
      axisLabelVisible: true,
      title: 'Entry',
    });
  }, [palette, data.kind, trade.entryPrice]);

  /** Lays the data onto the chart; for ticks, resampled with whatever has arrived live since. */
  const draw = useCallback(
    (fit: boolean) => {
      const chart = chartRef.current;
      const series = seriesRef.current;
      if (!chart || !series) return;
      const now = Date.now();
      const current = tradeRef.current;
      const currentMoments = tradeMoments(current);

      if (data.kind === 'ticks') {
        const pins = [{ time: currentMoments.entry, price: currentMoments.entryPrice }];
        if (currentMoments.exit !== null && currentMoments.exitPrice !== null) {
          pins.push({ time: currentMoments.exit, price: currentMoments.exitPrice });
        }
        const points = resampleTicks([...data.ticks, ...liveTicksRef.current], data.window, current.entryTimestamp, now, pins);
        slotsRef.current = points.map((point) => ({ time: point.time }));
        stepRef.current = data.window.stepMs;
        (series as ISeriesApi<'Area'>).setData(points.map(toAreaPoint));
        // The live price's own line and label only while there is a live price.
        series.applyOptions({ lastValueVisible: current.status === 'OPEN', priceLineVisible: current.status === 'OPEN' });
      } else {
        slotsRef.current = data.candles.map((candle) => ({ time: candle.time }));
        stepRef.current = 60_000;
        (series as ISeriesApi<'Candlestick'>).setData(
          data.candles.map(
            (candle): CandlestickData<UTCTimestamp> => ({
              time: (candle.time / 1000) as UTCTimestamp,
              open: candle.open,
              high: candle.high,
              low: candle.low,
              close: candle.close,
            }),
          ),
        );
      }
      if (fit) chart.timeScale().fitContent();
    },
    [data],
  );

  useEffect(() => {
    draw(!fittedRef.current);
    fittedRef.current = true;
  }, [draw]);

  // A running window keeps collecting ticks from the feed and redraws twice a
  // second, then stops once its margin after the expiry has filled.
  useEffect(() => {
    if (!live) return;
    const assetId = trade.assetId;
    const unsubscribe = useMarketStore.subscribe((state, previous) => {
      const tick = state.ticks[assetId];
      if (!tick || tick === previous.ticks[assetId]) return;
      const buffer = liveTicksRef.current;
      if (buffer.length === 0 || tick.timestamp > buffer[buffer.length - 1]!.timestamp) {
        buffer.push({ timestamp: tick.timestamp, price: tick.price });
      }
    });
    const timer = window.setInterval(() => {
      draw(false);
      if (Date.now() >= windowEnd) setLive(false);
    }, LIVE_REDRAW_MS);
    return () => {
      unsubscribe();
      window.clearInterval(timer);
    };
  }, [live, trade.assetId, draw, windowEnd]);

  // A settlement arriving while the page is open lands on the line at once.
  useEffect(() => {
    if (data.kind === 'ticks') draw(false);
  }, [trade.status, trade.expiryPrice, draw, data.kind]);

  // The markers, projected each frame so they follow pan, zoom and the live line.
  const startRef = useRef<HTMLDivElement | null>(null);
  const expiryRef = useRef<HTMLDivElement | null>(null);
  const entryRef = useRef<HTMLDivElement | null>(null);
  const exitRef = useRef<HTMLDivElement | null>(null);
  const writtenRef = useRef('');

  useFrameLoop(() => {
    const chart = chartRef.current;
    const series = seriesRef.current;
    const container = containerRef.current;
    if (!chart || !series || !container) return;
    const current = tradeMoments(tradeRef.current);
    const scale = chart.timeScale();
    const plotWidth = scale.width();
    const plotHeight = container.clientHeight - scale.height();
    const axis = readLogicalAxis(chart);
    const slots = slotsRef.current;
    const step = stepRef.current;
    const x = (ms: number) => timeToX(axis, ms, slots, step);
    const y = (price: number | null) => (price === null ? null : series.priceToCoordinate(price));

    const startX = x(current.entry);
    const expiryX = x(current.expiry);
    const entryY = y(current.entryPrice);
    const exitX = current.exit === null ? null : x(current.exit);
    const exitY = y(current.exitPrice);

    const signature = [plotWidth, plotHeight, startX, expiryX, entryY, exitX, exitY]
      .map((value) => (typeof value === 'number' ? value.toFixed(1) : String(value)))
      .join('|');
    if (signature === writtenRef.current) return;
    writtenRef.current = signature;

    placeLine(startRef.current, startX, plotWidth, plotHeight);
    placeLine(expiryRef.current, expiryX, plotWidth, plotHeight);
    placeSpot(entryRef.current, startX, entryY, plotWidth, plotHeight);
    placeSpot(exitRef.current, exitX, exitY, plotWidth, plotHeight);
  });

  const tone = statusTone(trade);
  const resultColour = tone === 'up' ? palette.up : tone === 'down' ? palette.down : palette.strongText;

  return (
    <div className="absolute inset-0">
      <div ref={containerRef} className="absolute inset-0" />

      {/* Everything that must stop at the plot's edge. */}
      <div className="pointer-events-none absolute inset-0 z-10 overflow-hidden" aria-hidden>
        {/* Start: dashed, with a stopwatch at its foot. */}
        <div ref={startRef} className="absolute left-0 top-0 w-0" style={{ visibility: 'hidden' }}>
          <div
            className="absolute inset-y-0 left-0 w-px"
            style={{
              backgroundImage:
                'repeating-linear-gradient(to bottom, ' + withAlpha(palette.strongText, 0.75) + ' 0 5px, transparent 5px 9px)',
            }}
          />
          <span
            className="absolute bottom-1 left-0 grid h-6 w-6 -translate-x-1/2 place-items-center rounded-full"
            style={{ color: palette.strongText, backgroundColor: withAlpha(palette.panel, 0.9) }}
          >
            <Timer className="h-3.5 w-3.5" />
          </span>
        </div>

        {/* Expiry: solid, with a flag at its foot and, while running, the time left. */}
        <div ref={expiryRef} className="absolute left-0 top-0 w-0" style={{ visibility: 'hidden' }}>
          <div className="absolute inset-y-0 left-0 w-px" style={{ backgroundColor: withAlpha(palette.strongText, 0.8) }} />
          <span
            className="absolute bottom-1 left-0 grid h-6 w-6 -translate-x-1/2 place-items-center rounded-full"
            style={{ color: palette.strongText, backgroundColor: withAlpha(palette.panel, 0.9) }}
          >
            <Flag className="h-3.5 w-3.5" />
          </span>
          {running ? <Countdown expiry={moments.expiry} palette={palette} /> : null}
        </div>
      </div>

      {/* The spots sit over the line and outside the clip, so a dot at the
          very edge is drawn whole. */}
      <div className="pointer-events-none absolute inset-0 z-20" aria-hidden>
        <div ref={entryRef} className="absolute left-0 top-0 h-0 w-0" style={{ visibility: 'hidden' }}>
          <span
            className="absolute h-3.5 w-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2"
            style={{ borderColor: palette.strongText, backgroundColor: palette.background }}
          />
        </div>

        {moments.exit !== null && moments.exitPrice !== null ? (
          <div ref={exitRef} className="absolute left-0 top-0 h-0 w-0" style={{ visibility: 'hidden' }}>
            <span
              className="absolute h-3.5 w-3.5 -translate-x-1/2 -translate-y-1/2 rounded-full"
              style={{ backgroundColor: resultColour, boxShadow: '0 0 0 3px ' + withAlpha(palette.background, 0.9) }}
            />
            <span className="absolute bottom-3 left-0 flex -translate-x-1/2 flex-col items-center gap-0.5">
              <span
                className="whitespace-nowrap rounded px-1 text-[10px] font-semibold tnum"
                style={{ color: palette.text, backgroundColor: withAlpha(palette.background, 0.8) }}
              >
                {formatClock(moments.exit, zone)}
              </span>
              <span
                className="whitespace-nowrap rounded-md px-2 py-0.5 text-xs font-bold tnum shadow-raised"
                style={{ backgroundColor: resultColour, color: palette.panel }}
              >
                {formatPrice(moments.exitPrice, precision)}
              </span>
            </span>
          </div>
        ) : null}
      </div>

    </div>
  );
}

/** The time left on a running trade, at the top of its expiry line. */
function Countdown({ expiry, palette }: { expiry: number; palette: ChartPalette }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 250);
    return () => window.clearInterval(timer);
  }, []);
  const remaining = Math.max(0, expiry - now);
  return (
    <span
      className={clsx(
        'absolute left-0 top-2 -translate-x-1/2 whitespace-nowrap rounded-md px-1.5 py-0.5 text-[11px] font-bold tnum',
        remaining <= 10_000 && 'animate-order-pulse',
      )}
      style={{
        color: remaining <= 10_000 ? palette.brand : palette.strongText,
        backgroundColor: withAlpha(palette.panel, 0.92),
        boxShadow: 'inset 0 0 0 1px ' + withAlpha(palette.brand, 0.6),
      }}
    >
      {formatCountdown(remaining)} left
    </span>
  );
}

/** Zoom out, fit the whole trade, zoom in - the same three a trader has on the trade screen. */
function WindowZoom({ chartRef }: { chartRef: MutableRefObject<IChartApi | null> }) {
  const zoom = (factor: number) => {
    const scale = chartRef.current?.timeScale();
    const range = scale?.getVisibleLogicalRange();
    if (!scale || !range) return;
    const centre = (range.from + range.to) / 2;
    const half = Math.max(2, ((range.to - range.from) / 2) / factor);
    scale.setVisibleLogicalRange({ from: centre - half, to: centre + half });
  };
  const button =
    'grid h-9 w-9 place-items-center rounded-lg border border-surface-700 bg-surface-850 text-content-300 transition hover:border-surface-600 hover:text-content-100';
  return (
    <div role="group" aria-label="Chart zoom" className="ml-auto flex shrink-0 gap-1">
      <button type="button" className={button} onClick={() => zoom(1 / 1.4)} aria-label="Zoom out" title="Zoom out">
        <Minus className="h-4 w-4" />
      </button>
      <button
        type="button"
        className={button}
        onClick={() => chartRef.current?.timeScale().fitContent()}
        aria-label="Show the whole trade"
        title="Show the whole trade"
      >
        <Maximize className="h-4 w-4" />
      </button>
      <button type="button" className={button} onClick={() => zoom(1.4)} aria-label="Zoom in" title="Zoom in">
        <Plus className="h-4 w-4" />
      </button>
    </div>
  );
}

function toAreaPoint(point: GridPoint): AreaData<UTCTimestamp> | WhitespaceData<UTCTimestamp> {
  const time = (point.time / 1000) as UTCTimestamp;
  return point.value === undefined ? { time } : { time, value: point.value };
}

/**
 * Moves a vertical marker to an instant, spanning the plot's height. One off
 * the plot is hidden rather than pinned to an edge it is not at.
 */
function placeLine(node: HTMLDivElement | null, x: number | null, width: number, height: number) {
  if (!node) return;
  const visible = x !== null && x >= -1 && x <= width + 1;
  node.style.visibility = visible ? 'visible' : 'hidden';
  if (!visible) return;
  node.style.transform = 'translate3d(' + clamp(x, 0, width) + 'px, 0, 0)';
  node.style.height = Math.max(0, height) + 'px';
}

/** Moves a spot to an instant and a price, hiding it when either is off the plot. */
function placeSpot(node: HTMLDivElement | null, x: number | null, y: number | null, width: number, height: number) {
  if (!node) return;
  const visible = x !== null && y !== null && x >= -1 && x <= width + 1 && y >= -1 && y <= height + 1;
  node.style.visibility = visible ? 'visible' : 'hidden';
  if (!visible) return;
  node.style.transform = 'translate3d(' + x + 'px, ' + y + 'px, 0)';
}
