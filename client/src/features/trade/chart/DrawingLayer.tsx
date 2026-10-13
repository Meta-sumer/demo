import { useEffect, useRef, useState, type MutableRefObject, type RefObject } from 'react';
import type { IChartApi, ISeriesApi } from 'lightweight-charts';
import { Trash2, X } from 'lucide-react';
import {
  POINTS_NEEDED,
  selectDrawings,
  useDrawingsStore,
  type ChartPoint,
  type Drawing,
} from '../../../stores/drawingsStore';
import { formatPrice } from '../../../utils/format';
import { formatClock } from '../../../utils/time';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import {
  actionAnchor,
  boxOf,
  FIB_LEVELS,
  fibPrice,
  rayEnd,
  topmostHit,
  type Plot,
  type ProjectedDrawing,
  type Pt,
} from './drawingGeometry';
import { DrawingIcon, toolInfo } from './drawingTools';
import { logicalToTime, logicalToX, readLogicalAxis, timeToLogical, xToLogical, type LogicalAxis } from './timeProjection';
import { clamp, useFrameLoop } from './useFrameLoop';

/**
 * Hand-drawn lines on the advanced chart (record 09).
 *
 * The library draws no user shapes, so they are drawn here, on a canvas laid
 * over the plot. Every frame the stored drawings - instants and prices - are
 * projected through the same time mapping the orders use and through the
 * series' own price scale, and the canvas is repainted only when that
 * projection, the selection or the pointer actually changed. A pan, a zoom, a
 * resize or the price scale autoscaling under a tick therefore carries every
 * drawing with the candles, and a still chart costs a string comparison.
 *
 * The canvas takes no pointer events: the chart underneath keeps panning and
 * zooming exactly as before. Clicks are read from the chart's own container
 * instead, with a movement threshold so the end of a pan is never mistaken
 * for a click, and without the library's click event - which swallows the
 * second of two quick clicks as a double click, and two quick clicks are
 * exactly how a trend line gets drawn.
 *
 * While a tool is armed a click places a point; otherwise it selects the
 * drawing under it (or clears the selection). The selected drawing gets a
 * delete button beside it, and Delete or Backspace removes it; Escape puts a
 * tool down or clears the selection.
 */
interface Props {
  assetId: string;
  chartRef: MutableRefObject<IChartApi | null>;
  seriesRef: MutableRefObject<ISeriesApi<'Candlestick'> | null>;
  containerRef: MutableRefObject<HTMLDivElement | null>;
  barsRef: RefObject<ReadonlyArray<{ time: number }>>;
  bucketMs: number;
  palette: ChartPalette;
  zone: string;
  digits: number;
  performanceMode: boolean;
}

/** How far a click may land from a line and still select it. */
const MOUSE_REACH = 6;
const TOUCH_REACH = 14;
/** Movement that turns a press into a pan rather than a click. */
const MOUSE_SLOP = 4;
const TOUCH_SLOP = 10;
/** A press held longer than this is a long-press, not a click. */
const CLICK_MS = 700;

const FONT = '600 10px "Inter var", Inter, ui-sans-serif, system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

type PointerKind = 'mouse' | 'touch';

export function DrawingLayer({
  assetId,
  chartRef,
  seriesRef,
  containerRef,
  barsRef,
  bucketMs,
  palette,
  zone,
  digits,
  performanceMode,
}: Props) {
  const drawings = useDrawingsStore((s) => selectDrawings(s, assetId));
  const armed = useDrawingsStore((s) => (s.armed && s.armed.assetId === assetId ? s.armed.kind : null));
  const selectedId = useDrawingsStore((s) => s.selectedId);
  const add = useDrawingsStore((s) => s.add);
  const remove = useDrawingsStore((s) => s.remove);
  const select = useDrawingsStore((s) => s.select);
  const disarm = useDrawingsStore((s) => s.disarm);

  const selectedHere = selectedId !== null && drawings.some((drawing) => drawing.id === selectedId);

  /** The first click of a two-click tool. */
  const [anchor, setAnchor] = useState<ChartPoint | null>(null);
  /** Which way the last press came, so the hint can say "tap" to a finger. */
  const [pointerKind, setPointerKind] = useState<PointerKind>('mouse');

  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const chipRef = useRef<HTMLDivElement | null>(null);
  /** Where the mouse is over the plot; touch has no hover. */
  const hoverRef = useRef<Pt | null>(null);
  const hoverIdRef = useRef<string | null>(null);
  /** The last projection, which a click is tested against. */
  const projectedRef = useRef<{ shapes: ProjectedDrawing[]; plot: Plot } | null>(null);
  const signatureRef = useRef('');

  // Everything the frame loop reads, refreshed every render so the loop never
  // holds a stale closure.
  const liveRef = useRef({ drawings, armed, anchor, selectedId, palette, digits, zone, bucketMs });
  liveRef.current = { drawings, armed, anchor, selectedId, palette, digits, zone, bucketMs };

  // A new tool, or a new instrument, starts from a clean first click.
  useEffect(() => setAnchor(null), [armed, assetId]);

  // Leaving the chart must not leave its cursor behind.
  useEffect(() => {
    const container = containerRef.current;
    return () => {
      if (container) container.style.cursor = '';
    };
  }, [containerRef]);

  /** A pixel on the plot to an instant and a price. */
  const toChartPoint = (p: Pt): ChartPoint | null => {
    const chart = chartRef.current;
    const series = seriesRef.current;
    if (!chart || !series) return null;
    const axis = readLogicalAxis(chart);
    if (!axis) return null;
    const time = logicalToTime(xToLogical(axis, p.x), barsRef.current ?? [], bucketMs);
    const price = series.coordinateToPrice(p.y);
    if (time === null || price === null) return null;
    return { time: Math.round(time), price: Number(price) };
  };

  // Clicks and taps, read from the chart's own container.
  const clickRef = useRef<(p: Pt, kind: PointerKind) => void>(() => undefined);
  clickRef.current = (p, kind) => {
    const projected = projectedRef.current;
    if (!projected) return;
    const { plot } = projected;
    // The axes are for scaling, not for drawing on.
    if (p.x < 0 || p.y < 0 || p.x > plot.width || p.y > plot.height) return;

    const live = liveRef.current;
    if (live.armed) {
      const point = toChartPoint(p);
      if (!point) return;
      if (POINTS_NEEDED[live.armed] === 1) {
        add(assetId, live.armed, [point]);
      } else if (!live.anchor) {
        setAnchor(point);
      } else {
        add(assetId, live.armed, [live.anchor, point]);
        setAnchor(null);
      }
      return;
    }

    const hit = topmostHit(projected.shapes, p, plot, kind === 'mouse' ? MOUSE_REACH : TOUCH_REACH);
    if (hit !== live.selectedId && (hit !== null || selectedHere)) select(hit);
  };

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    let press: { id: number; x: number; y: number; at: number; kind: PointerKind } | null = null;
    const pressed = new Set<number>();
    const local = (event: PointerEvent): Pt => {
      const rect = container.getBoundingClientRect();
      return { x: event.clientX - rect.left, y: event.clientY - rect.top };
    };

    const onDown = (event: PointerEvent) => {
      // A primary pointer going down starts a fresh gesture; anything left in
      // the set is a pointer whose release happened somewhere else.
      if (event.isPrimary) pressed.clear();
      pressed.add(event.pointerId);
      const kind: PointerKind = event.pointerType === 'mouse' ? 'mouse' : 'touch';
      setPointerKind(kind);
      // A second finger makes it a pinch, which is never a click.
      press = pressed.size === 1 ? { id: event.pointerId, ...local(event), at: event.timeStamp, kind } : null;
    };
    const onUp = (event: PointerEvent) => {
      pressed.delete(event.pointerId);
      const started = press;
      if (!started || started.id !== event.pointerId) return;
      press = null;
      const p = local(event);
      const moved = Math.hypot(p.x - started.x, p.y - started.y);
      if (moved > (started.kind === 'mouse' ? MOUSE_SLOP : TOUCH_SLOP)) return;
      if (event.timeStamp - started.at > CLICK_MS) return;
      clickRef.current(p, started.kind);
    };
    const onCancel = (event: PointerEvent) => {
      pressed.delete(event.pointerId);
      if (press?.id === event.pointerId) press = null;
    };
    const onMove = (event: PointerEvent) => {
      if (event.pointerType === 'mouse') hoverRef.current = local(event);
    };
    const onLeave = () => {
      hoverRef.current = null;
    };

    container.addEventListener('pointerdown', onDown);
    container.addEventListener('pointerup', onUp);
    container.addEventListener('pointercancel', onCancel);
    container.addEventListener('pointermove', onMove);
    container.addEventListener('pointerleave', onLeave);
    return () => {
      container.removeEventListener('pointerdown', onDown);
      container.removeEventListener('pointerup', onUp);
      container.removeEventListener('pointercancel', onCancel);
      container.removeEventListener('pointermove', onMove);
      container.removeEventListener('pointerleave', onLeave);
    };
  }, [containerRef]);

  // Escape puts the tool down or lets go of the selection; Delete removes it.
  useEffect(() => {
    if (!armed && !selectedHere) return;
    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (target && (target.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(target.tagName))) return;
      if (event.key === 'Escape') {
        if (armed) disarm();
        else select(null);
        return;
      }
      if ((event.key === 'Delete' || event.key === 'Backspace') && selectedHere && selectedId) {
        event.preventDefault();
        remove(assetId, selectedId);
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [armed, selectedHere, selectedId, assetId, disarm, select, remove]);

  useFrameLoop(
    () => {
      const chart = chartRef.current;
      const series = seriesRef.current;
      const container = containerRef.current;
      const canvas = canvasRef.current;
      if (!chart || !series || !container || !canvas) return;

      const live = liveRef.current;
      const bars = barsRef.current ?? [];
      const scale = chart.timeScale();
      const axis = readLogicalAxis(chart);
      const width = container.clientWidth;
      const height = container.clientHeight;
      const axisHeight = scale.height();
      const plot: Plot = { width: scale.width(), height: Math.max(0, height - axisHeight) };

      const shapes = axis ? project(live.drawings, axis, bars, live.bucketMs, series) : [];
      projectedRef.current = { shapes, plot };

      // Hover: which drawing is under the mouse, and the matching cursor.
      const hover = hoverRef.current;
      const hoverId =
        hover && !live.armed && hover.x <= plot.width && hover.y <= plot.height
          ? topmostHit(shapes, hover, plot, MOUSE_REACH)
          : null;
      hoverIdRef.current = hoverId;
      const cursor = live.armed ? 'crosshair' : hoverId ? 'pointer' : '';
      if (container.style.cursor !== cursor) container.style.cursor = cursor;

      // The shape being drawn, from the first click to the pointer.
      let draft: ProjectedDrawing | null = null;
      let anchorPt: Pt | null = null;
      if (live.armed && live.anchor && axis) {
        const x = projectX(live.anchor.time, axis, bars, live.bucketMs);
        const y = series.priceToCoordinate(live.anchor.price);
        if (x !== null && y !== null) {
          anchorPt = { x, y };
          if (hover) draft = { id: 'draft', kind: live.armed, points: [anchorPt, hover] };
        }
      }

      const dpr = window.devicePixelRatio || 1;
      const signature = [
        width,
        height,
        dpr,
        plot.width,
        axisHeight,
        live.palette.theme,
        live.palette.strongText,
        live.selectedId,
        hoverId,
        live.digits,
        live.zone,
        draft ? draft.points[1]!.x.toFixed(1) + ',' + draft.points[1]!.y.toFixed(1) : '',
        anchorPt ? anchorPt.x.toFixed(1) + ',' + anchorPt.y.toFixed(1) : '',
        shapes.map((shape) => shape.id + ':' + shape.points.map((p) => p.x.toFixed(1) + ',' + p.y.toFixed(1)).join(';')).join('|'),
      ].join('#');

      placeChip(chipRef.current, shapes, live.selectedId, plot);

      if (signature === signatureRef.current) return;
      signatureRef.current = signature;

      if (canvas.width !== Math.round(width * dpr) || canvas.height !== Math.round(height * dpr)) {
        canvas.width = Math.round(width * dpr);
        canvas.height = Math.round(height * dpr);
        canvas.style.width = width + 'px';
        canvas.style.height = height + 'px';
      }
      const ctx = canvas.getContext('2d');
      if (!ctx) return;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      ctx.clearRect(0, 0, width, height);

      paint(ctx, {
        shapes,
        drawings: live.drawings,
        plot,
        axisHeight,
        palette: live.palette,
        digits: live.digits,
        zone: live.zone,
        selectedId: live.selectedId,
        hoverId,
        draft,
        anchorPt,
      });
    },
    { maxFps: performanceMode ? 30 : 60 },
  );

  const tool = armed ? toolInfo(armed) : null;
  const step = tool ? tool.steps[anchor && tool.steps.length > 1 ? 1 : 0]! : '';
  const hint = pointerKind === 'touch' ? step.replace(/\bclick\b/g, 'tap').replace(/^Click/, 'Tap') : step;

  return (
    <>
      <canvas
        ref={canvasRef}
        className="pointer-events-none absolute left-0 top-0 z-[5]"
        aria-hidden
        data-part="drawings"
      />

      {/* The selected drawing's delete button, moved by the frame loop. */}
      {selectedHere ? (
        <div
          ref={chipRef}
          className="pointer-events-auto absolute left-0 top-0 z-[35] animate-fade-in"
          style={{ visibility: 'hidden' }}
        >
          <button
            type="button"
            onClick={() => selectedId && remove(assetId, selectedId)}
            className="flex h-9 items-center gap-1.5 rounded-lg border border-surface-700 bg-surface-900/95 px-2.5 text-[11px] font-semibold text-content-100 shadow-raised backdrop-blur transition hover:border-down/60 hover:text-down sm:h-8"
            aria-label="Delete drawing"
            title="Delete drawing (Delete key)"
          >
            <Trash2 className="h-3.5 w-3.5" />
            Delete
          </button>
        </div>
      ) : null}

      {/* What the armed tool is waiting for. Clicks pass straight through
          it - on a phone it covers the top of the plot, and the point the
          trader wants may well be under it - except on its Cancel button. */}
      {tool ? (
        <div className="pointer-events-none absolute inset-x-3 top-3 z-40 flex justify-center">
          <div
            role="status"
            className="flex max-w-full items-center gap-2 rounded-xl border border-surface-700 bg-surface-900/90 py-1 pl-2.5 pr-1 text-[11px] shadow-raised backdrop-blur-sm animate-fade-in"
          >
            <DrawingIcon kind={tool.kind} className="h-4 w-4 shrink-0 text-content-200" />
            <span className="min-w-0 truncate">
              <span className="font-bold text-content-100">{tool.name}</span>
              <span className="text-content-400"> · {hint}</span>
            </span>
            <button
              type="button"
              onClick={disarm}
              className="pointer-events-auto flex h-8 shrink-0 items-center gap-1 rounded-lg px-2 font-semibold text-content-300 transition hover:bg-surface-800 hover:text-content-100"
              aria-label={'Stop drawing ' + tool.name.toLowerCase()}
            >
              <X className="h-3.5 w-3.5" />
              <span className="hidden sm:inline">Cancel</span>
            </button>
          </div>
        </div>
      ) : null}
    </>
  );
}

function projectX(ms: number, axis: LogicalAxis, bars: ReadonlyArray<{ time: number }>, bucketMs: number): number | null {
  const logical = timeToLogical(ms, bars, bucketMs);
  return logical === null ? null : logicalToX(axis, logical);
}

/** Stored drawings to plot pixels. A drawing with no coordinate yet (no data) is skipped. */
function project(
  drawings: readonly Drawing[],
  axis: LogicalAxis,
  bars: ReadonlyArray<{ time: number }>,
  bucketMs: number,
  series: ISeriesApi<'Candlestick'>,
): ProjectedDrawing[] {
  const out: ProjectedDrawing[] = [];
  for (const drawing of drawings) {
    const [a, b] = drawing.points;
    if (!a) continue;
    if (drawing.kind === 'hline') {
      const y = series.priceToCoordinate(a.price);
      if (y !== null) out.push({ id: drawing.id, kind: 'hline', points: [{ x: 0, y }] });
      continue;
    }
    if (drawing.kind === 'vline') {
      const x = projectX(a.time, axis, bars, bucketMs);
      if (x !== null) out.push({ id: drawing.id, kind: 'vline', points: [{ x, y: 0 }] });
      continue;
    }
    if (!b) continue;
    const ax = projectX(a.time, axis, bars, bucketMs);
    const bx = projectX(b.time, axis, bars, bucketMs);
    const ay = series.priceToCoordinate(a.price);
    const by = series.priceToCoordinate(b.price);
    if (ax === null || bx === null || ay === null || by === null) continue;
    out.push({ id: drawing.id, kind: drawing.kind, points: [{ x: ax, y: ay }, { x: bx, y: by }] });
  }
  return out;
}

/** Keeps the delete button beside the selected drawing and inside the plot. */
function placeChip(chip: HTMLDivElement | null, shapes: ProjectedDrawing[], selectedId: string | null, plot: Plot) {
  if (!chip) return;
  const shape = selectedId ? shapes.find((entry) => entry.id === selectedId) : undefined;
  const anchor = shape ? actionAnchor(shape, plot) : null;
  if (!anchor) {
    if (chip.style.visibility !== 'hidden') chip.style.visibility = 'hidden';
    return;
  }
  const width = chip.offsetWidth || 80;
  const height = chip.offsetHeight || 32;
  const x = clamp(anchor.x + 8, 4, plot.width - width - 4);
  const y = clamp(anchor.y - height - 8, 4, plot.height - height - 4);
  const transform = 'translate3d(' + Math.round(x) + 'px, ' + Math.round(y) + 'px, 0)';
  if (chip.style.transform !== transform) chip.style.transform = transform;
  if (chip.style.visibility !== 'visible') chip.style.visibility = 'visible';
}

interface PaintInput {
  shapes: ProjectedDrawing[];
  drawings: readonly Drawing[];
  plot: Plot;
  axisHeight: number;
  palette: ChartPalette;
  digits: number;
  zone: string;
  selectedId: string | null;
  hoverId: string | null;
  draft: ProjectedDrawing | null;
  anchorPt: Pt | null;
}

/**
 * The drawings, in the theme's strongest text colour.
 *
 * Not the accent: in the dark themes that is the brand yellow every placed
 * order is drawn in, and a trend line must never be read as a position. Not
 * green or red either - those are the candles. The strong text colour is the
 * one ink that is neither, and it inverts with the theme.
 */
function paint(ctx: CanvasRenderingContext2D, input: PaintInput) {
  const { shapes, drawings, plot, axisHeight, palette, digits, zone, selectedId, hoverId, draft, anchorPt } = input;
  const ink = palette.strongText;
  const byId = new Map(drawings.map((drawing) => [drawing.id, drawing]));

  ctx.save();
  ctx.beginPath();
  ctx.rect(0, 0, plot.width, plot.height);
  ctx.clip();
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  ctx.font = FONT;

  // The selected drawing last, so it sits on top of anything it crosses.
  const ordered = [...shapes].sort((a, b) => Number(a.id === selectedId) - Number(b.id === selectedId));
  for (const shape of ordered) {
    const emphasis = shape.id === selectedId ? 'selected' : shape.id === hoverId ? 'hover' : 'rest';
    drawShape(ctx, shape, byId.get(shape.id), { plot, ink, palette, digits, emphasis, dashed: false });
  }

  if (draft) drawShape(ctx, draft, undefined, { plot, ink, palette, digits, emphasis: 'draft', dashed: true });
  if (anchorPt) handle(ctx, anchorPt, ink, palette.panel);

  ctx.restore();

  // Axis tags sit outside the plot, on the scales, like the chart's own labels.
  ctx.save();
  ctx.font = FONT;
  for (const shape of shapes) {
    const drawing = byId.get(shape.id);
    if (!drawing) continue;
    const point = shape.points[0]!;
    if (shape.kind === 'hline' && point.y >= 0 && point.y <= plot.height) {
      axisTag(ctx, formatPrice(drawing.points[0]!.price, digits), plot.width + 2, point.y, 'price', ink, palette.panel);
    }
    // A vertical line's time only while it is being looked at: the time axis
    // is narrow, and a label per line would bury its tick marks.
    if (shape.kind === 'vline' && (shape.id === selectedId || shape.id === hoverId) && point.x >= 0 && point.x <= plot.width) {
      const y = axisHeight > 0 ? plot.height + axisHeight / 2 : plot.height - 10;
      axisTag(ctx, formatClock(drawing.points[0]!.time, zone), point.x, y, 'time', ink, palette.panel);
    }
  }
  ctx.restore();
}

interface ShapeStyle {
  plot: Plot;
  ink: string;
  palette: ChartPalette;
  digits: number;
  emphasis: 'rest' | 'hover' | 'selected' | 'draft';
  dashed: boolean;
}

function drawShape(ctx: CanvasRenderingContext2D, shape: ProjectedDrawing, drawing: Drawing | undefined, style: ShapeStyle) {
  const { plot, ink, palette, emphasis } = style;
  const [a, b] = shape.points;
  if (!a) return;

  const strong = emphasis === 'selected' || emphasis === 'hover';
  ctx.strokeStyle = strong ? ink : withAlpha(ink, emphasis === 'draft' ? 0.7 : 0.82);
  ctx.lineWidth = emphasis === 'selected' ? 2 : strong ? 1.75 : 1.25;
  ctx.setLineDash(style.dashed ? [5, 4] : []);
  // A soft halo marks the selection without a second colour.
  ctx.shadowColor = emphasis === 'selected' ? withAlpha(ink, 0.45) : 'transparent';
  ctx.shadowBlur = emphasis === 'selected' ? 6 : 0;

  switch (shape.kind) {
    case 'hline':
      line(ctx, { x: 0, y: a.y }, { x: plot.width, y: a.y });
      break;
    case 'vline':
      line(ctx, { x: a.x, y: 0 }, { x: a.x, y: plot.height });
      break;
    case 'trend':
      if (b) line(ctx, a, b);
      break;
    case 'ray':
      if (b) line(ctx, a, rayEnd(a, b, plot));
      break;
    case 'rect': {
      if (!b) break;
      const box = boxOf(a, b);
      ctx.fillStyle = withAlpha(ink, emphasis === 'selected' ? 0.1 : 0.06);
      ctx.fillRect(box.left, box.top, box.right - box.left, box.bottom - box.top);
      ctx.strokeRect(box.left, box.top, box.right - box.left, box.bottom - box.top);
      break;
    }
    case 'fib':
      if (b) fibonacci(ctx, a, b, drawing, style);
      break;
    default:
      break;
  }

  ctx.shadowBlur = 0;
  ctx.shadowColor = 'transparent';
  ctx.setLineDash([]);

  if (emphasis === 'selected') {
    if (shape.kind === 'hline') handle(ctx, { x: Math.min(plot.width - 8, plot.width / 2), y: a.y }, ink, palette.panel);
    else if (shape.kind === 'vline') handle(ctx, { x: a.x, y: plot.height / 2 }, ink, palette.panel);
    else for (const point of shape.points) handle(ctx, point, ink, palette.panel);
  }
}

/**
 * A retracement: one line per level across the span of the move, the bands
 * between them tinted alternately, and each level labelled with its ratio and
 * its price. The move itself is a faint dashed diagonal, so it is clear which
 * way it was measured.
 */
function fibonacci(ctx: CanvasRenderingContext2D, a: Pt, b: Pt, drawing: Drawing | undefined, style: ShapeStyle) {
  const { ink, digits, emphasis } = style;
  const left = Math.min(a.x, b.x);
  const right = Math.max(a.x, b.x);
  const width = Math.max(1, right - left);
  // Price is linear in y, so each level's y follows from the two ends.
  const levelY = (level: number) => b.y + (a.y - b.y) * level;
  const strokeStyle = ctx.strokeStyle;

  for (let i = 0; i < FIB_LEVELS.length - 1; i += 1) {
    const top = levelY(FIB_LEVELS[i]!);
    const bottom = levelY(FIB_LEVELS[i + 1]!);
    ctx.fillStyle = withAlpha(ink, i % 2 === 0 ? 0.035 : 0.07);
    ctx.fillRect(left, Math.min(top, bottom), width, Math.abs(bottom - top));
  }

  ctx.save();
  ctx.shadowBlur = 0;
  ctx.setLineDash([3, 4]);
  ctx.lineWidth = 1;
  ctx.strokeStyle = withAlpha(ink, 0.35);
  line(ctx, a, b);
  ctx.restore();

  ctx.strokeStyle = strokeStyle;
  ctx.fillStyle = emphasis === 'rest' ? withAlpha(ink, 0.8) : ink;
  ctx.textBaseline = 'bottom';
  ctx.textAlign = 'left';
  for (const level of FIB_LEVELS) {
    const y = levelY(level);
    line(ctx, { x: left, y }, { x: right, y });
    if (drawing && width >= 64) {
      const [start, end] = drawing.points;
      const price = fibPrice(start!.price, end!.price, level);
      ctx.fillText((level * 100).toFixed(1).replace(/\.0$/, '') + '%  ' + formatPrice(price, digits), left + 4, y - 2);
    }
  }
}

function line(ctx: CanvasRenderingContext2D, a: Pt, b: Pt) {
  ctx.beginPath();
  ctx.moveTo(a.x, a.y);
  ctx.lineTo(b.x, b.y);
  ctx.stroke();
}

/** A point that can be read as "a click went here". */
function handle(ctx: CanvasRenderingContext2D, p: Pt, ink: string, fill: string) {
  ctx.save();
  ctx.setLineDash([]);
  ctx.shadowBlur = 0;
  ctx.beginPath();
  ctx.arc(p.x, p.y, 4, 0, Math.PI * 2);
  ctx.fillStyle = fill;
  ctx.fill();
  ctx.lineWidth = 1.5;
  ctx.strokeStyle = ink;
  ctx.stroke();
  ctx.restore();
}

/** A filled label on the price or the time axis. */
function axisTag(
  ctx: CanvasRenderingContext2D,
  text: string,
  x: number,
  y: number,
  axis: 'price' | 'time',
  ink: string,
  textColor: string,
) {
  const width = ctx.measureText(text).width + 10;
  const height = 16;
  const left = axis === 'price' ? x : x - width / 2;
  const top = y - height / 2;
  ctx.fillStyle = ink;
  roundRect(ctx, left, top, width, height, 3);
  ctx.fill();
  ctx.fillStyle = textColor;
  ctx.textAlign = 'left';
  ctx.textBaseline = 'middle';
  ctx.fillText(text, left + 5, y + 0.5);
}

function roundRect(ctx: CanvasRenderingContext2D, x: number, y: number, width: number, height: number, radius: number) {
  ctx.beginPath();
  ctx.moveTo(x + radius, y);
  ctx.arcTo(x + width, y, x + width, y + height, radius);
  ctx.arcTo(x + width, y + height, x, y + height, radius);
  ctx.arcTo(x, y + height, x, y, radius);
  ctx.arcTo(x, y, x + width, y, radius);
  ctx.closePath();
}

