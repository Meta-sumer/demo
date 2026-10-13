import type { SkCanvas, SkFont } from '@shopify/react-native-skia';
import { clockText, priceText } from './format';
import {
  areaPath,
  clipTo,
  crisp,
  DASHED,
  fillPaint,
  hLine,
  LARGE_DASHED,
  linePath,
  paintBars,
  paintCandles,
  rect,
  roundStroke,
  strokePaint,
  tag,
  verticalGradient,
} from './paint';
import { priceMarks, timeMarks, yOf } from './scale';
import type { Crosshair, OverlayLine, SeriesData, View, ZoneInfo } from './types';

/**
 * The price chart's own canvas: what lightweight-charts drew on the web.
 *
 * Grid, the series in the chosen representation, the overlay indicators, the
 * last-price line and tag, both axes, the crosshair with its tags, and the
 * DEMO watermark under everything. The orders, the drawings and the expiry
 * guide are separate layers recorded over this one.
 */
export type SeriesKind = 'candles' | 'bars' | 'line' | 'area';

export interface MainStyle {
  kind: SeriesKind;
  background: string;
  /** Already dimmed by the grid-opacity setting. */
  grid: string;
  text: string;
  border: string;
  crosshair: string;
  crosshairText: string;
  bull: string;
  bear: string;
  bullText: string;
  bearText: string;
  /** Line and area series. */
  accent: string;
  accentText: string;
  accentTop: string;
  accentBottom: string;
  /** The candle series' last-price line. */
  priceLine: string;
  /** "DEMO" behind the candles, or null on a live account. */
  watermark: string | null;
  digits: number;
  /** Show the last price's line and tag. */
  lastPrice: boolean;
}

export interface MainFonts {
  axis: SkFont;
  watermark: SkFont | null;
}

/** Pixels between price labels, and between time labels. */
const PRICE_GAP = 34;
const TIME_GAP = 64;

export function paintMain(
  canvas: SkCanvas,
  view: View,
  series: SeriesData,
  style: MainStyle,
  overlays: ReadonlyArray<OverlayLine>,
  cross: Crosshair,
  zone: ZoneInfo,
  fonts: MainFonts,
): void {
  'worklet';
  if (view.w <= 0 || view.h <= 0) return;
  rect(canvas, 0, 0, view.w, view.h, fillPaint(style.background));

  const y = (price: number) => {
    'worklet';
    return yOf(view, price);
  };

  // The watermark, centred on the plot and under everything else.
  if (style.watermark && fonts.watermark) {
    const text = 'DEMO';
    const width = fonts.watermark.getTextWidth(text);
    const metrics = fonts.watermark.getMetrics();
    canvas.drawText(
      text,
      (view.pw - width) / 2,
      view.ph / 2 - (metrics.ascent + metrics.descent) / 2,
      fillPaint(style.watermark),
      fonts.watermark,
    );
  }

  const prices = view.ok ? priceMarks(view, style.digits, PRICE_GAP) : [];
  const times = view.ok ? timeMarks(view, series.t, series.bucket, zone, TIME_GAP) : [];

  // Grid.
  canvas.save();
  clipTo(canvas, 0, 0, view.pw, view.ph);
  const grid = strokePaint(style.grid, 1);
  for (let i = 0; i < prices.length; i += 1) {
    const py = crisp(prices[i]!.y);
    canvas.drawLine(0, py, view.pw, py, grid);
  }
  for (let i = 0; i < times.length; i += 1) {
    const px = crisp(times[i]!.x);
    canvas.drawLine(px, 0, px, view.ph, grid);
  }

  if (view.ok) {
    // The series.
    if (style.kind === 'candles') {
      paintCandles(canvas, view, series, y, style.bull, style.bear);
    } else if (style.kind === 'bars') {
      paintBars(canvas, view, series, y, style.bull, style.bear);
    } else {
      if (style.kind === 'area') {
        const fill = fillPaint(style.accentTop);
        verticalGradient(fill, 0, view.ph, style.accentTop, style.accentBottom);
        canvas.drawPath(areaPath(view, series.c, y, view.ph), fill);
      }
      canvas.drawPath(linePath(view, series.c, y), roundStroke(strokePaint(style.accent, 2)));
    }

    // Overlay indicators.
    for (let k = 0; k < overlays.length; k += 1) {
      const overlay = overlays[k]!;
      canvas.drawPath(linePath(view, overlay.v, y), strokePaint(overlay.color, 1, overlay.dashed ? DASHED : null));
    }
  }

  // The last price's line.
  const n = series.c.length;
  const lastClose = n > 0 ? series.c[n - 1]! : Number.NaN;
  const lastOpen = n > 0 ? series.o[n - 1]! : Number.NaN;
  const lastColour =
    style.kind === 'line' || style.kind === 'area' ? style.accent : lastClose >= lastOpen ? style.bull : style.bear;
  const lastTextColour =
    style.kind === 'line' || style.kind === 'area' ? style.accentText : lastClose >= lastOpen ? style.bullText : style.bearText;
  const lastY = view.ok && lastClose === lastClose ? y(lastClose) : Number.NaN;
  if (style.lastPrice && lastY === lastY && lastY >= 0 && lastY <= view.ph) {
    hLine(canvas, view, lastY, style.kind === 'candles' ? style.priceLine : lastColour, DASHED);
  }

  // The crosshair's lines.
  let crossY = Number.NaN;
  let crossX = Number.NaN;
  if (cross.visible && view.ok && cross.index >= 0 && cross.index < view.count) {
    crossX = crisp(view.x0 + cross.index * view.s);
    const close = series.c[cross.index]!;
    crossY = close === close ? y(close) : Number.NaN;
    const dashed = strokePaint(style.crosshair, 1, LARGE_DASHED);
    canvas.drawLine(crossX, 0, crossX, view.ph, dashed);
    if (crossY === crossY) canvas.drawLine(0, crisp(crossY), view.pw, crisp(crossY), dashed);
  }
  canvas.restore();

  // Axes: their borders, then the labels.
  const border = strokePaint(style.border, 1);
  canvas.drawLine(crisp(view.pw), 0, crisp(view.pw), view.h, border);
  if (view.axisH > 0) canvas.drawLine(0, crisp(view.ph), view.w, crisp(view.ph), border);

  const font = fonts.axis;
  const textPaint = fillPaint(style.text);
  const metrics = font.getMetrics();
  const centreOffset = -(metrics.ascent + metrics.descent) / 2;
  for (let i = 0; i < prices.length; i += 1) {
    const mark = prices[i]!;
    if (mark.y < 6 || mark.y > view.ph - 6) continue;
    canvas.drawText(priceText(mark.value, style.digits), view.pw + 6, mark.y + centreOffset, textPaint, font);
  }

  if (view.axisH > 0) {
    let lastRight = -Infinity;
    const axisY = view.ph + view.axisH / 2 + centreOffset;
    for (let i = 0; i < times.length; i += 1) {
      const mark = times[i]!;
      const width = font.getTextWidth(mark.text);
      const left = mark.x - width / 2;
      if (left < 2 || left + width > view.pw - 2 || left < lastRight + 8) continue;
      canvas.drawText(mark.text, left, axisY, textPaint, font);
      lastRight = left + width;
    }
  }

  // The last price's tag on the axis.
  if (style.lastPrice && lastY === lastY) {
    const clamped = lastY < 8 ? 8 : lastY > view.ph - 8 ? view.ph - 8 : lastY;
    tag(canvas, font, priceText(lastClose, style.digits), view.pw + 1, clamped, 'left', lastColour, lastTextColour, {
      padX: 5,
      radius: 2,
    });
  }

  // The crosshair's tags, over the axis labels.
  if (crossY === crossY && crossY >= 0 && crossY <= view.ph) {
    tag(canvas, font, priceText(series.c[cross.index]!, style.digits), view.pw + 1, crossY, 'left', style.crosshair, style.crosshairText, {
      padX: 5,
      radius: 2,
    });
  }
  if (crossX === crossX && view.axisH > 0) {
    const text = clockText(series.t[cross.index]!, zone);
    const width = font.getTextWidth(text) + 10;
    const centre = crossX < width / 2 ? width / 2 : crossX > view.pw - width / 2 ? view.pw - width / 2 : crossX;
    tag(canvas, font, text, centre, view.ph + view.axisH / 2, 'center', style.crosshair, style.crosshairText, {
      padX: 5,
      radius: 2,
      height: 18,
    });
  }
}
