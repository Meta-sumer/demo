import { useEffect, useMemo, useRef, useState, type MutableRefObject } from 'react';
import { Pressable, StyleSheet, View } from 'react-native';
import { Canvas, createPicture, Picture, Skia, type SkCanvas, type SkFont } from '@shopify/react-native-skia';
import { Gesture, GestureDetector } from 'react-native-gesture-handler';
import { useDerivedValue, useSharedValue, type SharedValue } from 'react-native-reanimated';
import { X } from 'lucide-react-native';
import { Icon, Text } from '../../../components/ui';
import { GRID_OPACITY_MAX } from '../../../stores/settingsStore';
import { macd, rsi, stochastic, type Point } from '../../../utils/indicators';
import { shouldRun } from '../../../utils/redraw';
import { formatPrice } from '../../../utils/format';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import { INDICATOR_CATALOG, type Oscillator } from './indicatorCatalog';
import { contrastText } from './engine/colors';
import { useChartFonts } from './engine/fonts';
import { priceText } from './engine/format';
import { clipTo, crisp, DASHED, fillPaint, linePath, rect, roundStroke, strokePaint, tag } from './engine/paint';
import { horizontal, indexAt, priceMarks, timeMarks, yOf } from './engine/scale';
import { HIDDEN_CROSSHAIR, type Crosshair, type Size, type View as ChartView, type ZoneInfo } from './engine/types';
import type { ChartEngine } from './engine/useChartEngine';

/**
 * One oscillator, in a band of its own under the price (record 09).
 *
 * A second price scale squeezed into the bottom of the main chart would leave
 * the main axis printing prices beside an RSI of 70 and run the price
 * gridlines through it. So each oscillator is a small canvas of its own: its
 * own scale with its own labels (0-100, or MACD's values), its own levels,
 * and nothing of the price's.
 *
 * It only follows. The main chart owns the time axis; this band reads the
 * main chart's spacing and offset straight from the engine and never pans or
 * zooms itself, so the two can never disagree about which candle is where.
 * Every bar has a slot here - empty where the oscillator has no value yet -
 * so index i is the same candle in both, and the main chart makes every price
 * axis the same width so the slots line up to the pixel. A tap or a long
 * press here moves the shared crosshair, so the price and every band show the
 * same instant.
 */
interface Bar {
  time: number;
  high: number;
  low: number;
  close: number;
}

interface Props {
  kind: Oscillator;
  engine: ChartEngine;
  zone: SharedValue<ZoneInfo>;
  barsRef: MutableRefObject<Bar[]>;
  /** Changes when the history is reloaded - applied at once. */
  historyKey: string;
  /** Changes on every tick - applied at the performance-mode pace. */
  tickKey: number;
  palette: ChartPalette;
  gridOpacity: number;
  digits: number;
  performanceMode: boolean;
  /** The width this band's price labels need, so every axis can be made as wide as the widest. */
  onAxisWidth: (kind: Oscillator, width: number | null) => void;
  onRemove: (kind: Oscillator) => void;
}

/** Reference lines, where the oscillator has conventional ones. */
const LEVELS: Record<Oscillator, number[]> = { rsi: [70, 30], stochastic: [80, 20], macd: [0] };

/** How tall a band is on a phone. */
const PANE_HEIGHT = 76;

interface PaneLine {
  v: number[];
  color: string;
  width: number;
}

interface PaneData {
  lines: PaneLine[];
  /** MACD's histogram, aligned like the lines; empty for the others. */
  histogram: number[];
  histogramUp: string;
  histogramDown: string;
  levels: number[];
  /** The bounded oscillators sit on a fixed 0-100 scale. */
  bounded: boolean;
  /** Price digits for the labels: 0 for the bounded ones. */
  digits: number;
  /** Which line the crosshair is pinned to. */
  anchor: number;
}

interface PaneStyle {
  background: string;
  grid: string;
  text: string;
  border: string;
  crosshair: string;
  crosshairText: string;
  level: string;
  levelText: string;
}

const EMPTY: PaneData = {
  lines: [],
  histogram: [],
  histogramUp: '#000',
  histogramDown: '#000',
  levels: [],
  bounded: true,
  digits: 0,
  anchor: 0,
};

export function OscillatorPane({
  kind,
  engine,
  zone,
  barsRef,
  historyKey,
  tickKey,
  palette,
  gridOpacity,
  digits,
  performanceMode,
  onAxisWidth,
  onRemove,
}: Props) {
  const info = INDICATOR_CATALOG[kind];
  const runRef = useRef({ at: 0, key: '' });
  const [latest, setLatest] = useState<number[]>([]);
  const fonts = useChartFonts();
  const axisFont = fonts.axis;

  const data = useSharedValue<PaneData>(EMPTY);
  const size = useSharedValue<Size>({ w: 0, h: PANE_HEIGHT });
  const style = useMemo<PaneStyle>(
    () => ({
      background: palette.background,
      grid: withAlpha(palette.grid, gridOpacity / GRID_OPACITY_MAX),
      text: palette.text,
      border: palette.border,
      crosshair: palette.crosshair,
      crosshairText: contrastText(palette.crosshair),
      level: withAlpha(palette.text, 0.55),
      levelText: contrastText(palette.text),
    }),
    [palette, gridOpacity],
  );

  // The callbacks are the page's; holding them in refs keeps the effects
  // below from rerunning when the page re-renders.
  const axisRef = useRef(onAxisWidth);
  axisRef.current = onAxisWidth;
  useEffect(() => () => axisRef.current(kind, null), [kind]);

  // The values, recomputed from the bars the main chart holds.
  useEffect(() => {
    const key = historyKey + '|' + palette.theme + '|' + digits;
    if (!shouldRun(runRef, key, performanceMode)) return;

    const source = barsRef.current;
    const bars = source.map((bar) => ({ time: bar.time, close: bar.close, high: bar.high, low: bar.low }));
    const slots = (points: Point[]) => {
      const byTime = new Map(points.map((point) => [point.time, point.value]));
      return bars.map((bar) => byTime.get(bar.time) ?? Number.NaN);
    };
    const last: number[] = [];
    let next: PaneData;

    if (kind === 'rsi') {
      const line = rsi(bars, 14);
      next = { ...EMPTY, lines: [{ v: slots(line), color: palette.accent, width: 2 }], levels: LEVELS.rsi };
      last.push(line.at(-1)?.value ?? Number.NaN);
    } else if (kind === 'stochastic') {
      const { k, d } = stochastic(bars, 14, 3, 3);
      next = {
        ...EMPTY,
        lines: [
          { v: slots(k), color: palette.strongText, width: 2 },
          { v: slots(d), color: palette.accent, width: 1 },
        ],
        levels: LEVELS.stochastic,
      };
      last.push(k.at(-1)?.value ?? Number.NaN, d.at(-1)?.value ?? Number.NaN);
    } else {
      const lines = macd(bars, 12, 26, 9);
      next = {
        lines: [
          { v: slots(lines.macd), color: palette.strongText, width: 2 },
          { v: slots(lines.signal), color: palette.accent, width: 1 },
        ],
        histogram: slots(lines.histogram),
        histogramUp: withAlpha(palette.bull, 0.55),
        histogramDown: withAlpha(palette.bear, 0.55),
        levels: LEVELS.macd,
        bounded: false,
        digits,
        anchor: 0,
      };
      last.push(lines.macd.at(-1)?.value ?? Number.NaN, lines.signal.at(-1)?.value ?? Number.NaN);
    }

    data.value = next;
    setLatest((previous) => (sameNumbers(previous, last) ? previous : last));

    // The labels this band prints, for the shared axis width.
    let widest = kind === 'macd' ? 0 : 100;
    if (kind === 'macd') {
      for (const line of next.lines) for (const value of line.v) if (value === value) widest = Math.max(widest, Math.abs(value));
      for (const value of next.histogram) if (value === value) widest = Math.max(widest, Math.abs(value));
    }
    const text = kind === 'macd' ? '-' + priceText(widest, digits) : '100';
    axisRef.current(kind, axisFont.getTextWidth(text) + 16);
  }, [historyKey, tickKey, kind, palette, performanceMode, barsRef, digits, data, axisFont]);

  const { view: mainView, crosshair, series } = engine;
  const view = useDerivedValue<ChartView>(() => {
    const main = mainView.value;
    const base = horizontal({
      size: size.value,
      axisW: main.axisW,
      axisH: 0,
      spacing: main.s,
      rightOffset: 0,
      count: main.count,
    });
    // The main chart's own x mapping, so bar i is at the same x here.
    const shared = { ...base, x0: main.x0, first: main.first, last: main.last, ok: main.ok && base.ph > 0 };
    const pane = data.value;
    let lo = 0;
    let hi = 100;
    let top = 0.12;
    let bottom = 0.06;
    if (!pane.bounded) {
      // MACD is unbounded and keeps room for its header above the bars.
      top = 0.2;
      bottom = 0.08;
      lo = Infinity;
      hi = -Infinity;
      const scan = (values: number[]) => {
        'worklet';
        for (let i = shared.first; i <= shared.last; i += 1) {
          const value = values[i];
          if (value !== undefined && value === value) {
            if (value < lo) lo = value;
            if (value > hi) hi = value;
          }
        }
      };
      for (let k = 0; k < pane.lines.length; k += 1) scan(pane.lines[k]!.v);
      scan(pane.histogram);
      if (!(hi >= lo)) {
        lo = -1;
        hi = 1;
      }
      if (hi - lo <= 0) {
        const pad = Math.max(Math.abs(hi) * 0.1, Math.pow(10, -pane.digits));
        lo -= pad;
        hi += pad;
      }
    }
    const span = hi - lo;
    const inner = 1 - top - bottom;
    return { ...shared, lo: lo - (span * bottom) / inner, hi: hi + (span * top) / inner };
  });

  const picture = useDerivedValue(
    () =>
      createPicture((canvas) => {
        paintPane(canvas, view.value, data.value, style, crosshair.value, series.value.t, series.value.bucket, zone.value, axisFont);
      }),
    [style, axisFont],
  );

  // The band never pans or zooms; a tap or a press only moves the crosshair.
  const gesture = useMemo(() => {
    const place = (x: number, y: number) => {
      'worklet';
      const current = view.value;
      if (!current.ok) return;
      crosshair.value = { visible: true, index: indexAt(current, Math.max(0, Math.min(current.pw, x))), y };
    };
    const tap = Gesture.Tap()
      .maxDuration(700)
      .maxDistance(10)
      .onEnd((event, success) => {
        if (!success) return;
        if (crosshair.value.visible) crosshair.value = HIDDEN_CROSSHAIR;
        else place(event.x, event.y);
      });
    const press = Gesture.Pan()
      .activateAfterLongPress(260)
      .onStart((event) => place(event.x, event.y))
      .onUpdate((event) => place(event.x, event.y));
    return Gesture.Race(press, tap);
  }, [view, crosshair]);

  const colours = kind === 'rsi' ? [palette.accent] : [palette.strongText, palette.accent];
  const names = kind === 'macd' ? ['MACD', 'Signal'] : kind === 'stochastic' ? ['%K', '%D'] : [info.short];

  return (
    <View
      className="relative shrink-0 border-t border-surface-800"
      style={{ height: PANE_HEIGHT }}
      onLayout={(event) => {
        size.value = { w: event.nativeEvent.layout.width, h: event.nativeEvent.layout.height };
      }}
    >
      <GestureDetector gesture={gesture}>
        <View style={StyleSheet.absoluteFill} collapsable={false}>
          <Canvas style={StyleSheet.absoluteFill}>
            <Picture picture={picture} />
          </Canvas>
        </View>
      </GestureDetector>

      {/* Name, parameters and the latest values, in the colours of their
          lines, and the way to take the band off again. */}
      <View
        pointerEvents="box-none"
        className="absolute left-1.5 top-1 flex-row items-center gap-2 rounded bg-surface-900/75 py-0.5 pl-1"
        style={{ maxWidth: '80%' }}
      >
        <Text className="text-[10px] font-semibold leading-[12px] text-content-200" numberOfLines={1}>
          {info.short} <Text className="text-[10px] font-medium leading-[12px] text-content-400">{info.params}</Text>
        </Text>
        {latest.map((value, index) =>
          Number.isFinite(value) ? (
            <Text
              key={names[index]}
              numberOfLines={1}
              className="shrink text-[10px] font-semibold leading-[12px] tnum"
              style={{ color: colours[index] }}
            >
              {names.length > 1 ? names[index] + ' ' : ''}
              {kind === 'macd' ? formatPrice(value, digits) : value.toFixed(1)}
            </Text>
          ) : null,
        )}
        <Pressable
          onPress={() => onRemove(kind)}
          hitSlop={8}
          accessibilityRole="button"
          accessibilityLabel={'Remove ' + info.name}
          className="h-6 w-6 items-center justify-center rounded active:bg-surface-800"
        >
          <Icon as={X} size={14} className="text-content-400" />
        </Pressable>
      </View>
    </View>
  );
}

function paintPane(
  canvas: SkCanvas,
  view: ChartView,
  data: PaneData,
  style: PaneStyle,
  cross: Crosshair,
  times: number[],
  bucket: number,
  zone: ZoneInfo,
  font: SkFont,
): void {
  'worklet';
  if (view.w <= 0 || view.h <= 0) return;
  rect(canvas, 0, 0, view.w, view.h, fillPaint(style.background));
  const y = (value: number) => {
    'worklet';
    return yOf(view, value);
  };

  // The bounded scales get round labels; the levels say the rest.
  const marks = data.bounded ? [] : priceMarks(view, data.digits, 26);
  const verticals = view.ok ? timeMarks(view, times, bucket, zone, 64) : [];

  canvas.save();
  clipTo(canvas, 0, 0, view.pw, view.ph);
  const grid = strokePaint(style.grid, 1);
  for (let i = 0; i < marks.length; i += 1) {
    const py = crisp(marks[i]!.y);
    canvas.drawLine(0, py, view.pw, py, grid);
  }
  for (let i = 0; i < verticals.length; i += 1) {
    const px = crisp(verticals[i]!.x);
    canvas.drawLine(px, 0, px, view.ph, grid);
  }

  // The conventional levels.
  const levelPaint = strokePaint(style.level, 1, DASHED);
  for (let i = 0; i < data.levels.length; i += 1) {
    const py = crisp(y(data.levels[i]!));
    canvas.drawLine(0, py, view.pw, py, levelPaint);
  }

  if (view.ok) {
    // MACD's histogram, bars about the zero line.
    if (data.histogram.length > 0) {
      const up = Skia.Path.Make();
      const down = Skia.Path.Make();
      const width = Math.max(1, Math.floor(view.s * 0.6));
      const zero = y(0);
      for (let i = view.first; i <= view.last; i += 1) {
        const value = data.histogram[i];
        if (value === undefined || value !== value) continue;
        const x = Math.round(view.x0 + i * view.s) - width / 2;
        const top = Math.min(zero, y(value));
        const height = Math.max(1, Math.abs(y(value) - zero));
        const path = value >= 0 ? up : down;
        path.moveTo(x, top);
        path.lineTo(x + width, top);
        path.lineTo(x + width, top + height);
        path.lineTo(x, top + height);
        path.close();
      }
      canvas.drawPath(up, fillPaint(data.histogramUp));
      canvas.drawPath(down, fillPaint(data.histogramDown));
    }
    for (let k = 0; k < data.lines.length; k += 1) {
      const line = data.lines[k]!;
      canvas.drawPath(linePath(view, line.v, y), roundStroke(strokePaint(line.color, line.width)));
    }
  }

  // The crosshair, at the same instant as the price's.
  let crossValue = Number.NaN;
  if (cross.visible && view.ok && cross.index >= 0 && cross.index < view.count) {
    const x = crisp(view.x0 + cross.index * view.s);
    const dashed = strokePaint(style.crosshair, 1, [6, 6]);
    canvas.drawLine(x, 0, x, view.ph, dashed);
    const anchor = data.lines[data.anchor];
    const value = anchor ? anchor.v[cross.index] : undefined;
    if (value !== undefined && value === value) {
      crossValue = value;
      canvas.drawLine(0, crisp(y(value)), view.pw, crisp(y(value)), dashed);
    }
  }
  canvas.restore();

  canvas.drawLine(crisp(view.pw), 0, crisp(view.pw), view.h, strokePaint(style.border, 1));

  const metrics = font.getMetrics();
  const centre = -(metrics.ascent + metrics.descent) / 2;
  const textPaint = fillPaint(style.text);
  for (let i = 0; i < marks.length; i += 1) {
    const mark = marks[i]!;
    if (mark.y < 6 || mark.y > view.ph - 6) continue;
    canvas.drawText(priceText(mark.value, data.digits), view.pw + 6, mark.y + centre, textPaint, font);
  }
  // The levels carry their values on the axis.
  for (let i = 0; i < data.levels.length; i += 1) {
    const level = data.levels[i]!;
    const py = y(level);
    if (py < 0 || py > view.ph) continue;
    tag(canvas, font, priceText(level, data.digits), view.pw + 1, py, 'left', style.level, style.levelText, { padX: 5, radius: 2, height: 14 });
  }
  // Each line's latest value, on the axis in its own colour.
  for (let k = 0; k < data.lines.length; k += 1) {
    const line = data.lines[k]!;
    let value = Number.NaN;
    for (let i = line.v.length - 1; i >= 0; i -= 1) {
      if (line.v[i] === line.v[i]) {
        value = line.v[i]!;
        break;
      }
    }
    if (value !== value) continue;
    const py = Math.max(7, Math.min(view.ph - 7, y(value)));
    tag(canvas, font, data.bounded ? value.toFixed(0) : priceText(value, data.digits), view.pw + 1, py, 'left', line.color, contrastOn(line.color), {
      padX: 5,
      radius: 2,
      height: 14,
    });
  }
  if (crossValue === crossValue) {
    tag(canvas, font, data.bounded ? crossValue.toFixed(0) : priceText(crossValue, data.digits), view.pw + 1, y(crossValue), 'left', style.crosshair, style.crosshairText, {
      padX: 5,
      radius: 2,
      height: 14,
    });
  }
}

/** Black or white text for a tag in a line's colour (the UI-thread twin of `contrastText`). */
function contrastOn(color: string): string {
  'worklet';
  const parsed = Skia.Color(color);
  const brightness = 0.199 * parsed[0]! * 255 + 0.687 * parsed[1]! * 255 + 0.114 * parsed[2]! * 255;
  return brightness > 160 ? '#000000' : '#ffffff';
}

function sameNumbers(a: number[], b: number[]): boolean {
  return a.length === b.length && a.every((value, index) => Object.is(value, b[index]) || Math.abs(value - b[index]!) < 1e-9);
}
