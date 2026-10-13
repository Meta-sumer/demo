import {
  ClipOp,
  PaintStyle,
  Skia,
  StrokeCap,
  StrokeJoin,
  TileMode,
  type SkCanvas,
  type SkFont,
  type SkPaint,
  type SkPath,
} from '@shopify/react-native-skia';
import type { View } from './types';

/**
 * Drawing primitives for the engine and its layers. All worklets: they run on
 * the UI thread while a picture is recorded.
 */

/**
 * lightweight-charts' line styles, as dash intervals for a one-pixel line.
 * Dashed is two on, two off; large-dashed (its crosshair default) six and six.
 */
export const DASHED = [2, 2];
export const LARGE_DASHED = [6, 6];

export function fillPaint(color: string): SkPaint {
  'worklet';
  const paint = Skia.Paint();
  paint.setAntiAlias(true);
  paint.setColor(Skia.Color(color));
  return paint;
}

export function strokePaint(color: string, width: number, dash?: ReadonlyArray<number> | null): SkPaint {
  'worklet';
  const paint = Skia.Paint();
  paint.setAntiAlias(true);
  paint.setColor(Skia.Color(color));
  paint.setStyle(PaintStyle.Stroke);
  paint.setStrokeWidth(width);
  if (dash && dash.length > 0) paint.setPathEffect(Skia.PathEffect.MakeDash([...dash], 0));
  return paint;
}

export function roundStroke(paint: SkPaint): SkPaint {
  'worklet';
  paint.setStrokeCap(StrokeCap.Round);
  paint.setStrokeJoin(StrokeJoin.Round);
  return paint;
}

/** A one-pixel line lands on a pixel rather than straddling two. */
export function crisp(value: number): number {
  'worklet';
  return Math.round(value) + 0.5;
}

export function line(canvas: SkCanvas, x0: number, y0: number, x1: number, y1: number, paint: SkPaint): void {
  'worklet';
  canvas.drawLine(x0, y0, x1, y1, paint);
}

/** Appends a rectangle to a path without allocating a rect per call. */
export function addRect(path: SkPath, x: number, y: number, width: number, height: number): void {
  'worklet';
  path.moveTo(x, y);
  path.lineTo(x + width, y);
  path.lineTo(x + width, y + height);
  path.lineTo(x, y + height);
  path.close();
}

export function rect(canvas: SkCanvas, x: number, y: number, width: number, height: number, paint: SkPaint): void {
  'worklet';
  canvas.drawRect(Skia.XYWHRect(x, y, width, height), paint);
}

export function roundRect(
  canvas: SkCanvas,
  x: number,
  y: number,
  width: number,
  height: number,
  radius: number,
  paint: SkPaint,
): void {
  'worklet';
  canvas.drawRRect(Skia.RRectXY(Skia.XYWHRect(x, y, width, height), radius, radius), paint);
}

export function clipTo(canvas: SkCanvas, x: number, y: number, width: number, height: number): void {
  'worklet';
  canvas.clipRect(Skia.XYWHRect(x, y, Math.max(0, width), Math.max(0, height)), ClipOp.Intersect, true);
}

/** A vertical gradient fill between two y coordinates. */
export function verticalGradient(paint: SkPaint, top: number, bottom: number, from: string, to: string): void {
  'worklet';
  paint.setShader(
    Skia.Shader.MakeLinearGradient(
      Skia.Point(0, top),
      Skia.Point(0, bottom === top ? top + 1 : bottom),
      [Skia.Color(from), Skia.Color(to)],
      null,
      TileMode.Clamp,
    ),
  );
}

/** The baseline that centres a line of text on `y`. */
export function baselineFor(font: SkFont, y: number): number {
  'worklet';
  const metrics = font.getMetrics();
  return y - (metrics.ascent + metrics.descent) / 2;
}

export function textWidth(font: SkFont, text: string): number {
  'worklet';
  return font.getTextWidth(text);
}

/**
 * Text on a filled tag, the shape every axis label and chip uses.
 *
 * `x` is the tag's left edge, its centre or its right edge per `align`; `y`
 * is its vertical centre. Returns the tag's left edge and width so callers
 * can keep the next one clear of it.
 */
export function tag(
  canvas: SkCanvas,
  font: SkFont,
  text: string,
  x: number,
  y: number,
  align: 'left' | 'center' | 'right',
  background: string,
  foreground: string,
  options?: { padX?: number; height?: number; radius?: number; border?: string | null },
): { left: number; width: number } {
  'worklet';
  const padX = options?.padX ?? 5;
  const height = options?.height ?? 16;
  const radius = options?.radius ?? 3;
  const width = font.getTextWidth(text) + padX * 2;
  const left = align === 'left' ? x : align === 'center' ? x - width / 2 : x - width;
  const top = y - height / 2;
  roundRect(canvas, left, top, width, height, radius, fillPaint(background));
  if (options?.border) {
    roundRect(canvas, left + 0.5, top + 0.5, width - 1, height - 1, radius, strokePaint(options.border, 1));
  }
  canvas.drawText(text, left + padX, baselineFor(font, y), fillPaint(foreground), font);
  return { left, width };
}

/** The bar spacing below which a candle is drawn as its wick alone. */
const BODY_MIN_SPACING = 3;

/** Candle bodies and wicks, one path per colour. */
export function paintCandles(
  canvas: SkCanvas,
  view: View,
  t: { o: number[]; h: number[]; l: number[]; c: number[] },
  y: (price: number) => number,
  bull: string,
  bear: string,
): void {
  'worklet';
  const up = Skia.Path.Make();
  const down = Skia.Path.Make();
  let body = view.s >= BODY_MIN_SPACING ? Math.max(1, Math.floor(view.s * 0.8)) : 0;
  // An odd body puts the wick on its centre line.
  if (body > 2 && body % 2 === 0) body -= 1;
  for (let i = view.first; i <= view.last; i += 1) {
    const open = t.o[i]!;
    const close = t.c[i]!;
    if (close !== close) continue;
    const x = Math.round(view.x0 + i * view.s);
    const path = close >= open ? up : down;
    const yHigh = y(t.h[i]!);
    const yLow = y(t.l[i]!);
    addRect(path, x - 0.5, Math.min(yHigh, yLow), 1, Math.max(1, Math.abs(yLow - yHigh)));
    if (body > 0) {
      const yOpen = y(open);
      const yClose = y(close);
      const top = Math.min(yOpen, yClose);
      const height = Math.max(1, Math.abs(yClose - yOpen));
      addRect(path, x - body / 2, top, body, height);
    }
  }
  canvas.drawPath(up, fillPaint(bull));
  canvas.drawPath(down, fillPaint(bear));
}

/** OHLC bars: the range, an open tick to the left and a close tick to the right. */
export function paintBars(
  canvas: SkCanvas,
  view: View,
  t: { o: number[]; h: number[]; l: number[]; c: number[] },
  y: (price: number) => number,
  bull: string,
  bear: string,
): void {
  'worklet';
  const up = Skia.Path.Make();
  const down = Skia.Path.Make();
  const reach = Math.max(1, Math.min(8, Math.floor(view.s * 0.35)));
  for (let i = view.first; i <= view.last; i += 1) {
    const open = t.o[i]!;
    const close = t.c[i]!;
    if (close !== close) continue;
    const x = Math.round(view.x0 + i * view.s) + 0.5;
    const path = close >= open ? up : down;
    path.moveTo(x, y(t.h[i]!));
    path.lineTo(x, y(t.l[i]!));
    const yOpen = Math.round(y(open)) + 0.5;
    const yClose = Math.round(y(close)) + 0.5;
    path.moveTo(x - reach, yOpen);
    path.lineTo(x, yOpen);
    path.moveTo(x, yClose);
    path.lineTo(x + reach, yClose);
  }
  canvas.drawPath(up, strokePaint(bull, 1));
  canvas.drawPath(down, strokePaint(bear, 1));
}

/**
 * A polyline through `values` at the bar centres, broken wherever a value is
 * missing. One bar either side of the screen is included so the line runs off
 * the edge rather than stopping short of it.
 */
export function linePath(view: View, values: ReadonlyArray<number>, y: (price: number) => number): SkPath {
  'worklet';
  const path = Skia.Path.Make();
  const from = Math.max(0, view.first - 1);
  const to = Math.min(values.length - 1, view.last + 1);
  let pen = false;
  for (let i = from; i <= to; i += 1) {
    const value = values[i];
    if (value === undefined || value !== value) {
      pen = false;
      continue;
    }
    const px = view.x0 + i * view.s;
    const py = y(value);
    if (pen) path.lineTo(px, py);
    else path.moveTo(px, py);
    pen = true;
  }
  return path;
}

/** The area under a line, closed down to `bottom`, for each unbroken run. */
export function areaPath(view: View, values: ReadonlyArray<number>, y: (price: number) => number, bottom: number): SkPath {
  'worklet';
  const path = Skia.Path.Make();
  const from = Math.max(0, view.first - 1);
  const to = Math.min(values.length - 1, view.last + 1);
  let runStart = -1;
  let lastX = 0;
  for (let i = from; i <= to + 1; i += 1) {
    const value = i <= to ? values[i] : undefined;
    const valid = value !== undefined && value === value;
    if (valid) {
      const px = view.x0 + i * view.s;
      const py = y(value);
      if (runStart < 0) {
        runStart = px;
        path.moveTo(px, bottom);
        path.lineTo(px, py);
      } else {
        path.lineTo(px, py);
      }
      lastX = px;
    } else if (runStart >= 0) {
      path.lineTo(lastX, bottom);
      path.close();
      runStart = -1;
    }
  }
  return path;
}

/** A dashed or solid horizontal line across the plot. */
export function hLine(canvas: SkCanvas, view: View, y: number, color: string, dash: ReadonlyArray<number> | null): void {
  'worklet';
  const yy = crisp(y);
  canvas.drawLine(0, yy, view.pw, yy, strokePaint(color, 1, dash));
}
