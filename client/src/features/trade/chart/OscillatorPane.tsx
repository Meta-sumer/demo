import { useEffect, useRef, useState, type MutableRefObject } from 'react';
import {
  createChart,
  LineStyle,
  type HistogramData,
  type IChartApi,
  type ISeriesApi,
  type LineData,
  type MouseEventParams,
  type UTCTimestamp,
  type WhitespaceData,
} from 'lightweight-charts';
import { X } from 'lucide-react';
import { GRID_OPACITY_MAX } from '../../../stores/settingsStore';
import { macd, rsi, stochastic, type Point } from '../../../utils/indicators';
import { shouldRun } from '../../../utils/redraw';
import { formatPrice } from '../../../utils/format';
import { formatClock } from '../../../utils/time';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import { INDICATOR_CATALOG, type Oscillator } from './indicatorCatalog';

/**
 * One oscillator, in a band of its own under the price (record 09).
 *
 * lightweight-charts v4 has no panes, and the alternative - a second price
 * scale squeezed into the bottom of the main chart - leaves the main axis
 * printing prices beside an RSI of 70 and runs the price gridlines through it.
 * So each oscillator is a small chart of its own: its own scale with its own
 * labels (0-100, or MACD's values), its own levels, and nothing of the
 * price's.
 *
 * It only follows. The main chart owns the time axis; this band is handed the
 * main chart's visible range and never pans or zooms itself, so the two can
 * never disagree about which candle is where, and a vertical swipe over the
 * band on a phone scrolls the page instead of being trapped by a chart. Every
 * bar gets a slot here - whitespace where the oscillator has no value yet - so
 * index i is the same candle in both, and the main chart makes both price
 * axes the same width so the slots line up to the pixel.
 */
export interface PaneHandle {
  chart: IChartApi;
  /** The series the crosshair is pinned to. */
  series: ISeriesApi<'Line'> | ISeriesApi<'Histogram'>;
  /** The oscillator's value at a bar, in chart seconds, for the synced crosshair. */
  valueAt: (seconds: number) => number | null;
}

interface Bar {
  time: number;
  high: number;
  low: number;
  close: number;
}

interface Props {
  kind: Oscillator;
  barsRef: MutableRefObject<Bar[]>;
  /** Changes when the history is reloaded - applied at once. */
  historyKey: string;
  /** Changes on every tick - applied at the performance-mode pace. */
  tickKey: number;
  palette: ChartPalette;
  gridOpacity: number;
  zone: string;
  digits: number;
  performanceMode: boolean;
  mainChart: IChartApi | null;
  onReady: (kind: Oscillator, handle: PaneHandle | null) => void;
  /** The pointer is over the band, at a bar (seconds), or has left it. */
  onHover: (kind: Oscillator, seconds: number | null) => void;
  onRemove: (kind: Oscillator) => void;
}

/** Reference lines, where the oscillator has conventional ones. */
const LEVELS: Record<Oscillator, number[]> = { rsi: [70, 30], stochastic: [80, 20], macd: [0] };

export function OscillatorPane({
  kind,
  barsRef,
  historyKey,
  tickKey,
  palette,
  gridOpacity,
  zone,
  digits,
  performanceMode,
  mainChart,
  onReady,
  onHover,
  onRemove,
}: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<Array<ISeriesApi<'Line'> | ISeriesApi<'Histogram'>>>([]);
  const valuesRef = useRef(new Map<number, number>());
  const runRef = useRef({ at: 0, key: '' });
  const [latest, setLatest] = useState<number[]>([]);
  const info = INDICATOR_CATALOG[kind];

  // The callbacks are the page's; holding them in refs keeps the chart from
  // being rebuilt when the page re-renders.
  const readyRef = useRef(onReady);
  const hoverRef = useRef(onHover);
  readyRef.current = onReady;
  hoverRef.current = onHover;

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const chart = createChart(container, {
      layout: {
        background: { color: palette.background },
        textColor: palette.text,
        fontSize: 10,
        // The main chart on the same page carries the attribution.
        attributionLogo: false,
      },
      grid: { vertLines: { color: palette.grid }, horzLines: { color: palette.grid } },
      // The bounded oscillators get just enough margin to keep 0 and 100 off
      // the edges - more, and the axis starts printing values like 120 that
      // no reading can ever reach. MACD is unbounded and keeps room for its
      // header above the bars.
      rightPriceScale: {
        borderColor: palette.border,
        scaleMargins: kind === 'macd' ? { top: 0.2, bottom: 0.08 } : { top: 0.12, bottom: 0.06 },
      },
      timeScale: { visible: false, borderVisible: false, rightOffset: 6, barSpacing: 10 },
      crosshair: { mode: 0, vertLine: { color: palette.crosshair }, horzLine: { color: palette.crosshair } },
      handleScroll: false,
      handleScale: false,
      kineticScroll: { touch: false, mouse: false },
      autoSize: true,
    });
    chartRef.current = chart;
    // A new chart has no data yet, whatever the throttle last saw.
    runRef.current = { at: 0, key: '' };

    const fixedScale = kind === 'macd' ? undefined : () => ({ priceRange: { minValue: 0, maxValue: 100 } });
    const format =
      kind === 'macd'
        ? { type: 'price' as const, precision: digits, minMove: 1 / 10 ** digits }
        : { type: 'price' as const, precision: 0, minMove: 1 };
    const base = { priceFormat: format, priceLineVisible: false, lastValueVisible: true, autoscaleInfoProvider: fixedScale };

    const series: Array<ISeriesApi<'Line'> | ISeriesApi<'Histogram'>> =
      kind === 'macd'
        ? [
            chart.addHistogramSeries({ ...base, lastValueVisible: false }),
            chart.addLineSeries({ ...base, lineWidth: 2 }),
            chart.addLineSeries({ ...base, lineWidth: 1 }),
          ]
        : kind === 'stochastic'
          ? [chart.addLineSeries({ ...base, lineWidth: 2 }), chart.addLineSeries({ ...base, lineWidth: 1 })]
          : [chart.addLineSeries({ ...base, lineWidth: 2 })];
    seriesRef.current = series;

    // The conventional levels, with their values on the axis.
    const anchor = series[kind === 'macd' ? 1 : 0]!;
    for (const level of LEVELS[kind]) {
      anchor.createPriceLine({
        price: level,
        color: withAlpha(palette.text, 0.55),
        lineWidth: 1,
        lineStyle: LineStyle.Dashed,
        axisLabelVisible: true,
        title: '',
      });
    }

    const onMove = (params: MouseEventParams) => {
      hoverRef.current(kind, params.time === undefined || !params.point ? null : (params.time as number));
    };
    chart.subscribeCrosshairMove(onMove);

    readyRef.current(kind, {
      chart,
      series: anchor,
      valueAt: (seconds) => valuesRef.current.get(seconds) ?? null,
    });

    return () => {
      readyRef.current(kind, null);
      chart.unsubscribeCrosshairMove(onMove);
      chart.remove();
      chartRef.current = null;
      seriesRef.current = [];
    };
    // Built once per oscillator; colours and precision are applied below.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [kind]);

  // Theme and grid.
  useEffect(() => {
    const chart = chartRef.current;
    if (!chart) return;
    const grid = withAlpha(palette.grid, gridOpacity / GRID_OPACITY_MAX);
    chart.applyOptions({
      layout: { background: { color: palette.background }, textColor: palette.text },
      grid: { vertLines: { color: grid }, horzLines: { color: grid } },
      rightPriceScale: { borderColor: palette.border },
      crosshair: { vertLine: { color: palette.crosshair }, horzLine: { color: palette.crosshair } },
    });

    const [first, second, third] = seriesRef.current;
    if (kind === 'macd') {
      second?.applyOptions({ color: palette.strongText });
      third?.applyOptions({ color: palette.accent });
    } else if (kind === 'stochastic') {
      first?.applyOptions({ color: palette.strongText });
      second?.applyOptions({ color: palette.accent });
    } else {
      first?.applyOptions({ color: palette.accent });
    }
  }, [palette, gridOpacity, kind]);

  // Time labels in the chosen zone, the same as the main chart's.
  useEffect(() => {
    chartRef.current?.applyOptions({
      localization: { timeFormatter: (time: unknown) => formatClock((time as number) * 1000, zone) },
    });
  }, [zone]);

  useEffect(() => {
    const format = { type: 'price' as const, precision: digits, minMove: 1 / 10 ** digits };
    if (kind === 'macd') for (const series of seriesRef.current) series.applyOptions({ priceFormat: format });
  }, [digits, kind]);

  // The values, recomputed from the bars the main chart holds.
  useEffect(() => {
    const chart = chartRef.current;
    const series = seriesRef.current;
    if (!chart || series.length === 0) return;
    const key = historyKey + '|' + palette.theme;
    if (!shouldRun(runRef, key, performanceMode)) return;

    const bars = barsRef.current.map((bar) => ({
      time: Math.floor(bar.time / 1000),
      close: bar.close,
      high: bar.high,
      low: bar.low,
    }));
    const slots = bars.map((bar) => bar.time);
    const values = new Map<number, number>();
    const last: number[] = [];

    if (kind === 'rsi') {
      const line = rsi(bars, 14);
      series[0]!.setData(fill(slots, line));
      remember(values, line);
      last.push(line.at(-1)?.value ?? Number.NaN);
    } else if (kind === 'stochastic') {
      const { k, d } = stochastic(bars, 14, 3, 3);
      series[0]!.setData(fill(slots, k));
      series[1]!.setData(fill(slots, d));
      remember(values, k);
      last.push(k.at(-1)?.value ?? Number.NaN, d.at(-1)?.value ?? Number.NaN);
    } else {
      const lines = macd(bars, 12, 26, 9);
      const up = withAlpha(palette.bull, 0.55);
      const down = withAlpha(palette.bear, 0.55);
      (series[0] as ISeriesApi<'Histogram'>).setData(
        fill(slots, lines.histogram).map((point) =>
          'value' in point ? ({ ...point, color: point.value >= 0 ? up : down } as HistogramData<UTCTimestamp>) : point,
        ),
      );
      series[1]!.setData(fill(slots, lines.macd));
      series[2]!.setData(fill(slots, lines.signal));
      remember(values, lines.macd);
      last.push(lines.macd.at(-1)?.value ?? Number.NaN, lines.signal.at(-1)?.value ?? Number.NaN);
    }

    valuesRef.current = values;
    setLatest((previous) => (sameNumbers(previous, last) ? previous : last));

    // New data can nudge this chart's own range; put it back on the main one's.
    const range = mainChart?.timeScale().getVisibleLogicalRange();
    if (range) chart.timeScale().setVisibleLogicalRange(range);
  }, [historyKey, tickKey, kind, palette, performanceMode, barsRef, mainChart]);

  const colours =
    kind === 'rsi' ? [palette.accent] : [palette.strongText, palette.accent];
  const names = kind === 'macd' ? ['MACD', 'Signal'] : kind === 'stochastic' ? ['%K', '%D'] : [info.short];

  return (
    <div className="relative h-[76px] shrink-0 border-t border-surface-800 sm:h-[104px]" data-part={'pane-' + kind}>
      <div ref={containerRef} className="h-full w-full" />
      {/* Name, parameters and the latest values, in the colours of their
          lines, and the way to take the band off again. */}
      <div className="pointer-events-none absolute left-1.5 top-1 z-10 flex max-w-[calc(100%-4.5rem)] items-center gap-2 rounded bg-surface-900/75 py-0.5 pl-1 text-[10px] font-semibold leading-none backdrop-blur-sm">
        <span className="whitespace-nowrap text-content-200">
          {info.short} <span className="font-medium text-content-400">{info.params}</span>
        </span>
        {latest.map((value, index) =>
          Number.isFinite(value) ? (
            <span key={names[index]} className="min-w-0 truncate whitespace-nowrap tnum" style={{ color: colours[index] }}>
              {names.length > 1 ? names[index] + ' ' : ''}
              {kind === 'macd' ? formatPrice(value, digits) : value.toFixed(1)}
            </span>
          ) : null,
        )}
        <button
          type="button"
          onClick={() => onRemove(kind)}
          className="pointer-events-auto grid h-6 w-6 shrink-0 place-items-center rounded text-content-400 transition hover:bg-surface-800 hover:text-content-100"
          aria-label={'Remove ' + info.name}
          title={'Remove ' + info.name}
        >
          <X className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  );
}

/** One slot per bar: the value where there is one, whitespace where there is not yet. */
function fill(slots: number[], points: Point[]): Array<LineData<UTCTimestamp> | WhitespaceData<UTCTimestamp>> {
  const byTime = new Map(points.map((point) => [point.time, point.value]));
  return slots.map((time) => {
    const value = byTime.get(time);
    return value === undefined
      ? { time: time as UTCTimestamp }
      : { time: time as UTCTimestamp, value };
  });
}

function remember(target: Map<number, number>, points: Point[]) {
  for (const point of points) target.set(point.time, point.value);
}

function sameNumbers(a: number[], b: number[]): boolean {
  return a.length === b.length && a.every((value, index) => Object.is(value, b[index]) || Math.abs(value - b[index]!) < 1e-9);
}
