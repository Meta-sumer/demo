import { useEffect, useRef, useState, type RefObject } from 'react';
import { Pressable, View } from 'react-native';
import { BlurStyle, createPicture, Skia, type SkCanvas, type SkFont, type SkPaint } from '@shopify/react-native-skia';
import Animated, { useAnimatedStyle, useDerivedValue, useSharedValue, type SharedValue } from 'react-native-reanimated';
import { Trash2, X } from 'lucide-react-native';
import { Icon, Text } from '../../../components/ui';
import {
  POINTS_NEEDED,
  selectDrawings,
  useDrawingsStore,
  type ChartPoint,
  type Drawing,
  type DrawingKind,
} from '../../../stores/drawingsStore';
import { useTokenColor, type ChartPalette } from '../../../utils/theme-colors';
import { topmostHit, type Plot, type ProjectedDrawing, type Pt } from './drawingGeometry';
import { DrawingIcon, toolInfo } from './drawingTools';
import { TAP_PRIORITY, type ChartHost } from './engine/host';
import { useChartFonts } from './engine/fonts';
import { clockText, priceText } from './engine/format';
import { clipTo, fillPaint, roundStroke, strokePaint, tag } from './engine/paint';
import { priceAt, yOf } from './engine/scale';
import type { View as ChartView, ZoneInfo } from './engine/types';
import { logicalToTime, timeToLogicalTimes, xToLogical } from './timeProjection';
import { clamp } from './useFrameLoop';

/**
 * Hand-drawn lines on the advanced chart (record 09).
 *
 * The engine draws no user shapes, so they are drawn here, as a layer of the
 * chart's canvas. Every frame the stored drawings - instants and prices - are
 * projected through the same time mapping the orders use and through the
 * price scale, on the UI thread. A pan, a zoom, a resize or the price scale
 * refitting under a tick therefore carries every drawing with the candles.
 *
 * The layer takes no touches of its own: the chart keeps panning and zooming
 * exactly as before, and hands this layer its taps. While a tool is armed a
 * tap places a point; otherwise it selects the drawing under it (or clears
 * the selection). The selected drawing gets a delete button beside it.
 */
interface Props {
  host: ChartHost;
  assetId: string;
  barsRef: RefObject<ReadonlyArray<{ time: number }>>;
  bucketMs: number;
  palette: ChartPalette;
  zone: SharedValue<ZoneInfo>;
  digits: number;
}

/** How far a tap may land from a line and still select it - a finger's reach. */
const TOUCH_REACH = 14;

/** The retracement levels (drawingGeometry's FIB_LEVELS), held here for the UI thread. */
const LEVELS = [0, 0.236, 0.382, 0.5, 0.618, 1];

export function DrawingLayer({ host, assetId, barsRef, bucketMs, palette, zone, digits }: Props) {
  const { engine } = host;
  const drawings = useDrawingsStore((s) => selectDrawings(s, assetId));
  const armed = useDrawingsStore((s) => (s.armed && s.armed.assetId === assetId ? s.armed.kind : null));
  const selectedId = useDrawingsStore((s) => s.selectedId);
  const add = useDrawingsStore((s) => s.add);
  const remove = useDrawingsStore((s) => s.remove);
  const select = useDrawingsStore((s) => s.select);
  const disarm = useDrawingsStore((s) => s.disarm);

  const selectedHere = selectedId !== null && drawings.some((drawing) => drawing.id === selectedId);

  /** The first tap of a two-tap tool. */
  const [anchor, setAnchor] = useState<ChartPoint | null>(null);

  // Everything the tap handler reads, refreshed every render so it never
  // holds a stale closure.
  const liveRef = useRef({ drawings, armed, anchor, selectedId, selectedHere, bucketMs });
  liveRef.current = { drawings, armed, anchor, selectedId, selectedHere, bucketMs };

  // A new tool, or a new instrument, starts from a clean first tap.
  useEffect(() => setAnchor(null), [armed, assetId]);

  // What the UI thread draws from.
  const stored = useSharedValue<readonly Drawing[]>([]);
  const selected = useSharedValue<string | null>(null);
  const pending = useSharedValue<ChartPoint | null>(null);
  useEffect(() => {
    stored.value = drawings.map((drawing) => ({ id: drawing.id, kind: drawing.kind, points: drawing.points.map((p) => ({ ...p })) }));
  }, [drawings, stored]);
  useEffect(() => {
    selected.value = selectedHere ? selectedId : null;
  }, [selectedId, selectedHere, selected]);
  useEffect(() => {
    pending.value = armed && anchor ? { ...anchor } : null;
  }, [armed, anchor, pending]);

  const { view, series } = engine;
  const projected = useDerivedValue(() => projectAll(stored.value, view.value, series.value.t, series.value.bucket));

  const fonts = useChartFonts();
  const font = fonts.bold;
  /*
   * The drawings, in the theme's strongest text colour.
   *
   * Not the accent: in the dark themes that is the brand yellow every placed
   * order is drawn in, and a trend line must never be read as a position. Not
   * green or red either - those are the candles. The strong text colour is the
   * one ink that is neither, and it inverts with the theme.
   */
  const ink = palette.strongText;
  const panel = palette.panel;
  const picture = useDerivedValue(
    () =>
      createPicture((canvas) => {
        const current = view.value;
        const point = pending.value;
        let anchorPt: Pt | null = null;
        if (point && current.ok) {
          const logical = timeToLogicalTimes(point.time, series.value.t, series.value.bucket);
          if (logical !== null) anchorPt = { x: current.x0 + logical * current.s, y: yOf(current, point.price) };
        }
        paintDrawings(canvas, {
          shapes: projected.value,
          drawings: stored.value,
          view: current,
          ink,
          panel,
          digits,
          zone: zone.value,
          selectedId: selected.value,
          anchorPt,
          font,
        });
      }),
    [ink, panel, digits, font],
  );

  useEffect(() => {
    host.setLayer('drawings', picture);
    return () => host.setLayer('drawings', null);
  }, [host, picture]);

  // Taps, handed over by the chart below the orders and above the crosshair.
  useEffect(() => {
    host.setTap('drawings', TAP_PRIORITY.drawings, (x, y) => {
      const current = engine.readView();
      // The axes are for scaling, not for drawing on.
      if (!current.ok || x < 0 || y < 0 || x > current.pw || y > current.ph) return false;
      const live = liveRef.current;
      const bars = barsRef.current ?? [];

      if (live.armed) {
        // A pixel on the plot to an instant and a price.
        const time = logicalToTime(xToLogical({ x0: current.x0, spacing: current.s }, x), bars, live.bucketMs);
        const price = priceAt(current, y);
        if (time === null || !Number.isFinite(price)) return true;
        const point = { time: Math.round(time), price };
        if (POINTS_NEEDED[live.armed] === 1) {
          add(assetId, live.armed, [point]);
        } else if (!live.anchor) {
          setAnchor(point);
        } else {
          add(assetId, live.armed, [live.anchor, point]);
          setAnchor(null);
        }
        return true;
      }

      const shapes = projectAll(
        live.drawings,
        current,
        bars.map((bar) => bar.time),
        live.bucketMs,
      );
      const plot: Plot = { width: current.pw, height: current.ph };
      const hit = topmostHit(shapes, { x, y }, plot, TOUCH_REACH);
      if (hit !== live.selectedId && (hit !== null || live.selectedHere)) select(hit);
      // A tap that only let go of a selection is spent; it does not also
      // bring up the crosshair.
      return hit !== null || live.selectedHere;
    });
    return () => host.setTap('drawings', 0, null);
  }, [host, engine, barsRef, assetId, add, select]);

  /* The selected drawing's delete button, kept beside it and inside the plot. */
  const chipW = useSharedValue(86);
  const chipH = useSharedValue(36);
  const chipStyle = useAnimatedStyle(() => {
    const id = selected.value;
    const shapes = projected.value;
    const current = view.value;
    let shape: ProjectedDrawing | undefined;
    for (let i = 0; i < shapes.length; i += 1) if (shapes[i]!.id === id) shape = shapes[i];
    const at = shape ? actionAnchorOf(shape, current.pw) : null;
    if (!at) return { opacity: 0, transform: [{ translateX: -9999 }, { translateY: 0 }] };
    const x = clamp(at.x + 8, 4, current.pw - chipW.value - 4);
    const y = clamp(at.y - chipH.value - 8, 4, current.ph - chipH.value - 4);
    return { opacity: 1, transform: [{ translateX: Math.round(x) }, { translateY: Math.round(y) }] };
  });

  const toolInk = useTokenColor('content-200');
  const tool = armed ? toolInfo(armed) : null;
  const step = tool ? tool.steps[anchor && tool.steps.length > 1 ? 1 : 0]! : '';
  // The tools' words say "click"; on a phone every press is a tap.
  const hint = step.replace(/\bclick\b/g, 'tap').replace(/^Click/, 'Tap');

  return (
    <>
      {selectedHere ? (
        <Animated.View
          className="absolute left-0 top-0"
          style={chipStyle}
          onLayout={(event) => {
            chipW.value = event.nativeEvent.layout.width;
            chipH.value = event.nativeEvent.layout.height;
          }}
        >
          <Pressable
            onPress={() => selectedId && remove(assetId, selectedId)}
            accessibilityRole="button"
            accessibilityLabel="Delete drawing"
            className="h-9 flex-row items-center gap-1.5 rounded-lg border border-surface-700 bg-surface-900/95 px-2.5 active:border-down/60"
          >
            <Icon as={Trash2} size={14} className="text-content-100" />
            <Text className="text-[11px] font-semibold text-content-100">Delete</Text>
          </Pressable>
        </Animated.View>
      ) : null}

      {/* What the armed tool is waiting for. Taps pass straight through it -
          it covers the top of the plot, and the point the trader wants may
          well be under it - except on its Cancel button. */}
      {tool ? (
        <View pointerEvents="box-none" className="absolute inset-x-3 top-3 items-center">
          <View
            pointerEvents="box-none"
            accessibilityRole="text"
            accessibilityLiveRegion="polite"
            className="max-w-full flex-row items-center gap-2 rounded-xl border border-surface-700 bg-surface-900/90 py-1 pl-2.5 pr-1"
          >
            <DrawingIcon kind={tool.kind} size={16} color={toolInk} />
            <Text numberOfLines={1} className="shrink text-[11px]">
              <Text className="text-[11px] font-bold text-content-100">{tool.name}</Text>
              <Text className="text-[11px] text-content-400"> · {hint}</Text>
            </Text>
            <Pressable
              onPress={disarm}
              hitSlop={6}
              accessibilityRole="button"
              accessibilityLabel={'Stop drawing ' + tool.name.toLowerCase()}
              className="h-8 flex-row items-center gap-1 rounded-lg px-2 active:bg-surface-800"
            >
              <Icon as={X} size={14} className="text-content-300" />
            </Pressable>
          </View>
        </View>
      ) : null}
    </>
  );
}

/* ------------------------------------------------------------- projection */

/**
 * Stored drawings to plot pixels. A drawing with no coordinate yet (no data)
 * is skipped. A worklet, and called on the JS thread too, so a tap is tested
 * against exactly what was drawn.
 */
function projectAll(
  drawings: ReadonlyArray<Drawing>,
  view: ChartView,
  times: ReadonlyArray<number>,
  bucket: number,
): ProjectedDrawing[] {
  'worklet';
  const out: ProjectedDrawing[] = [];
  if (!view.ok) return out;
  const x = (ms: number): number | null => {
    'worklet';
    const logical = timeToLogicalTimes(ms, times, bucket);
    return logical === null ? null : view.x0 + logical * view.s;
  };
  for (let i = 0; i < drawings.length; i += 1) {
    const drawing = drawings[i]!;
    const a = drawing.points[0];
    const b = drawing.points[1];
    if (!a) continue;
    if (drawing.kind === 'hline') {
      out.push({ id: drawing.id, kind: 'hline', points: [{ x: 0, y: yOf(view, a.price) }] });
      continue;
    }
    if (drawing.kind === 'vline') {
      const ax = x(a.time);
      if (ax !== null) out.push({ id: drawing.id, kind: 'vline', points: [{ x: ax, y: 0 }] });
      continue;
    }
    if (!b) continue;
    const ax = x(a.time);
    const bx = x(b.time);
    if (ax === null || bx === null) continue;
    out.push({
      id: drawing.id,
      kind: drawing.kind,
      points: [
        { x: ax, y: yOf(view, a.price) },
        { x: bx, y: yOf(view, b.price) },
      ],
    });
  }
  return out;
}

/*
 * The UI thread's copies of drawingGeometry's helpers (rayEnd, boxOf,
 * actionAnchor, fibPrice), line for line: a worklet can only call worklets.
 */

function rayEndOf(a: Pt, b: Pt, width: number, height: number): Pt {
  'worklet';
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const length = Math.hypot(dx, dy);
  if (length === 0) return b;
  const reach =
    Math.max(Math.hypot(a.x, a.y), Math.hypot(a.x - width, a.y), Math.hypot(a.x, a.y - height), Math.hypot(a.x - width, a.y - height)) +
    Math.hypot(width, height);
  return { x: a.x + (dx / length) * reach, y: a.y + (dy / length) * reach };
}

function boxFrom(a: Pt, b: Pt): { left: number; right: number; top: number; bottom: number } {
  'worklet';
  return { left: Math.min(a.x, b.x), right: Math.max(a.x, b.x), top: Math.min(a.y, b.y), bottom: Math.max(a.y, b.y) };
}

function actionAnchorOf(shape: ProjectedDrawing, plotWidth: number): Pt | null {
  'worklet';
  const a = shape.points[0];
  const b = shape.points[1];
  if (!a) return null;
  switch (shape.kind) {
    case 'hline':
      return { x: plotWidth, y: a.y };
    case 'vline':
      return { x: a.x, y: 0 };
    case 'trend':
    case 'ray':
      if (!b) return null;
      // The end nearer the top, so the button sits above the line, not on it.
      return a.y < b.y ? a : b;
    case 'rect':
    case 'fib': {
      if (!b) return null;
      const box = boxFrom(a, b);
      return { x: box.right, y: box.top };
    }
    default:
      return null;
  }
}

function fibPriceOf(start: number, end: number, level: number): number {
  'worklet';
  return end - (end - start) * level;
}

/* ---------------------------------------------------------------- painting */

interface PaintInput {
  shapes: ProjectedDrawing[];
  drawings: ReadonlyArray<Drawing>;
  view: ChartView;
  ink: string;
  panel: string;
  digits: number;
  zone: ZoneInfo;
  selectedId: string | null;
  anchorPt: Pt | null;
  font: SkFont;
}

function withAlphaOf(paint: SkPaint, alpha: number): SkPaint {
  'worklet';
  paint.setAlphaf(paint.getAlphaf() * alpha);
  return paint;
}

function paintDrawings(canvas: SkCanvas, input: PaintInput): void {
  'worklet';
  const { shapes, drawings, view, ink, panel, digits, zone, selectedId, anchorPt, font } = input;
  if (!view.ok) return;

  canvas.save();
  clipTo(canvas, 0, 0, view.pw, view.ph);

  // The selected drawing last, so it sits on top of anything it crosses.
  let selectedShape: ProjectedDrawing | null = null;
  for (let i = 0; i < shapes.length; i += 1) {
    const shape = shapes[i]!;
    if (shape.id === selectedId) {
      selectedShape = shape;
      continue;
    }
    drawShape(canvas, shape, findDrawing(drawings, shape.id), view, ink, panel, digits, false, font);
  }
  if (selectedShape) drawShape(canvas, selectedShape, findDrawing(drawings, selectedShape.id), view, ink, panel, digits, true, font);

  if (anchorPt) handle(canvas, anchorPt, ink, panel);
  canvas.restore();

  // Axis tags sit outside the plot, on the scales, like the chart's own labels.
  for (let i = 0; i < shapes.length; i += 1) {
    const shape = shapes[i]!;
    const drawing = findDrawing(drawings, shape.id);
    if (!drawing) continue;
    const point = shape.points[0]!;
    if (shape.kind === 'hline' && point.y >= 0 && point.y <= view.ph) {
      tag(canvas, font, priceText(drawing.points[0]!.price, digits), view.pw + 2, point.y, 'left', ink, panel, { padX: 5, height: 16, radius: 3 });
    }
    // A vertical line's time only while it is selected: the time axis is
    // narrow, and a label per line would bury its tick marks.
    if (shape.kind === 'vline' && shape.id === selectedId && point.x >= 0 && point.x <= view.pw) {
      const y = view.axisH > 0 ? view.ph + view.axisH / 2 : view.ph - 10;
      tag(canvas, font, clockText(drawing.points[0]!.time, zone), point.x, y, 'center', ink, panel, { padX: 5, height: 16, radius: 3 });
    }
  }
}

function findDrawing(drawings: ReadonlyArray<Drawing>, id: string): Drawing | undefined {
  'worklet';
  for (let i = 0; i < drawings.length; i += 1) if (drawings[i]!.id === id) return drawings[i];
  return undefined;
}

function drawShape(
  canvas: SkCanvas,
  shape: ProjectedDrawing,
  drawing: Drawing | undefined,
  view: ChartView,
  ink: string,
  panel: string,
  digits: number,
  selected: boolean,
  font: SkFont,
): void {
  'worklet';
  const a = shape.points[0];
  const b = shape.points[1];
  if (!a) return;
  const width = selected ? 2 : 1.25;
  const strokeOf = () => {
    'worklet';
    return withAlphaOf(roundStroke(strokePaint(ink, width)), selected ? 1 : 0.82);
  };
  // A soft halo marks the selection without a second colour.
  const haloOf = () => {
    'worklet';
    const halo = withAlphaOf(roundStroke(strokePaint(ink, width + 2)), 0.45);
    halo.setMaskFilter(Skia.MaskFilter.MakeBlur(BlurStyle.Normal, 3, true));
    return halo;
  };
  const segment = (from: Pt, to: Pt) => {
    'worklet';
    if (selected) canvas.drawLine(from.x, from.y, to.x, to.y, haloOf());
    canvas.drawLine(from.x, from.y, to.x, to.y, strokeOf());
  };

  switch (shape.kind as DrawingKind) {
    case 'hline':
      segment({ x: 0, y: a.y }, { x: view.pw, y: a.y });
      break;
    case 'vline':
      segment({ x: a.x, y: 0 }, { x: a.x, y: view.ph });
      break;
    case 'trend':
      if (b) segment(a, b);
      break;
    case 'ray':
      if (b) segment(a, rayEndOf(a, b, view.pw, view.ph));
      break;
    case 'rect': {
      if (!b) break;
      const box = boxFrom(a, b);
      const r = Skia.XYWHRect(box.left, box.top, box.right - box.left, box.bottom - box.top);
      canvas.drawRect(r, withAlphaOf(fillPaint(ink), selected ? 0.1 : 0.06));
      if (selected) canvas.drawRect(r, haloOf());
      canvas.drawRect(r, strokeOf());
      break;
    }
    case 'fib':
      if (b) fibonacci(canvas, a, b, drawing, ink, digits, selected, width, font);
      break;
    default:
      break;
  }

  if (selected) {
    if (shape.kind === 'hline') handle(canvas, { x: Math.min(view.pw - 8, view.pw / 2), y: a.y }, ink, panel);
    else if (shape.kind === 'vline') handle(canvas, { x: a.x, y: view.ph / 2 }, ink, panel);
    else for (let i = 0; i < shape.points.length; i += 1) handle(canvas, shape.points[i]!, ink, panel);
  }
}

/**
 * A retracement: one line per level across the span of the move, the bands
 * between them tinted alternately, and each level labelled with its ratio and
 * its price. The move itself is a faint dashed diagonal, so it is clear which
 * way it was measured.
 */
function fibonacci(
  canvas: SkCanvas,
  a: Pt,
  b: Pt,
  drawing: Drawing | undefined,
  ink: string,
  digits: number,
  selected: boolean,
  width: number,
  font: SkFont,
): void {
  'worklet';
  const left = Math.min(a.x, b.x);
  const right = Math.max(a.x, b.x);
  const span = Math.max(1, right - left);
  // Price is linear in y, so each level's y follows from the two ends.
  const levelY = (level: number) => {
    'worklet';
    return b.y + (a.y - b.y) * level;
  };

  for (let i = 0; i < LEVELS.length - 1; i += 1) {
    const top = levelY(LEVELS[i]!);
    const bottom = levelY(LEVELS[i + 1]!);
    canvas.drawRect(
      Skia.XYWHRect(left, Math.min(top, bottom), span, Math.abs(bottom - top)),
      withAlphaOf(fillPaint(ink), i % 2 === 0 ? 0.035 : 0.07),
    );
  }

  canvas.drawLine(a.x, a.y, b.x, b.y, withAlphaOf(strokePaint(ink, 1, [3, 4]), 0.35));

  const stroke = withAlphaOf(roundStroke(strokePaint(ink, width)), selected ? 1 : 0.82);
  const text = withAlphaOf(fillPaint(ink), selected ? 1 : 0.8);
  for (let i = 0; i < LEVELS.length; i += 1) {
    const level = LEVELS[i]!;
    const y = levelY(level);
    canvas.drawLine(left, y, right, y, stroke);
    if (drawing && span >= 64 && drawing.points[0] && drawing.points[1]) {
      const price = fibPriceOf(drawing.points[0].price, drawing.points[1].price, level);
      const label = (level * 100).toFixed(1).replace(/\.0$/, '') + '%  ' + priceText(price, digits);
      canvas.drawText(label, left + 4, y - 2 - font.getMetrics().descent, text, font);
    }
  }
}

/** A point that can be read as "a tap went here". */
function handle(canvas: SkCanvas, p: Pt, ink: string, fill: string): void {
  'worklet';
  canvas.drawCircle(p.x, p.y, 4, fillPaint(fill));
  canvas.drawCircle(p.x, p.y, 4, strokePaint(ink, 1.5));
}
