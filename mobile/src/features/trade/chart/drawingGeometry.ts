import type { DrawingKind } from '../../../stores/drawingsStore';

/**
 * Drawing geometry, in pixels.
 *
 * The drawing layer projects each stored drawing (instants and prices) into
 * plot pixels every frame, and everything after that - what to stroke, what a
 * click landed on, where the delete button goes - is plain 2D geometry. It is
 * kept here, free of the chart and the DOM, so it can be tested on its own.
 */
export interface Pt {
  x: number;
  y: number;
}

export interface Plot {
  width: number;
  height: number;
}

/** A drawing projected into the plot. A horizontal line reads only `y`, a vertical one only `x`. */
export interface ProjectedDrawing {
  id: string;
  kind: DrawingKind;
  points: Pt[];
}

/**
 * The retracement levels drawn, as fractions of the move.
 *
 * The usual six. 0.786 and the extensions past 1 are left out: this is the
 * advanced mode of a platform whose trades last minutes, and every extra line
 * is one more thing sitting between the trader and the candles.
 */
export const FIB_LEVELS = [0, 0.236, 0.382, 0.5, 0.618, 1] as const;

/**
 * The price at a retracement level.
 *
 * A retracement measures how much of a move has been given back, so 0 sits at
 * the end of the move (nothing given back) and 1 at its start (all of it). The
 * first click is the start: drawing from a swing low up to a swing high puts
 * 0 at the high and 0.618 below it, which is how the tool is read everywhere
 * else.
 */
export function fibPrice(start: number, end: number, level: number): number {
  return end - (end - start) * level;
}

/** Shortest distance from `p` to the segment a-b. */
export function distanceToSegment(p: Pt, a: Pt, b: Pt): number {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const lengthSquared = dx * dx + dy * dy;
  if (lengthSquared === 0) return Math.hypot(p.x - a.x, p.y - a.y);
  const t = Math.max(0, Math.min(1, ((p.x - a.x) * dx + (p.y - a.y) * dy) / lengthSquared));
  return Math.hypot(p.x - (a.x + t * dx), p.y - (a.y + t * dy));
}

/**
 * A point on the ray from `a` through `b` that is certainly past the plot.
 *
 * A ray has no end, but a canvas stroke and a hit test both need one. Going as
 * far again as the farthest the plot could possibly be from `a` is enough in
 * every direction, and the canvas clips the excess.
 */
export function rayEnd(a: Pt, b: Pt, plot: Plot): Pt {
  const dx = b.x - a.x;
  const dy = b.y - a.y;
  const length = Math.hypot(dx, dy);
  if (length === 0) return b;
  const reach =
    Math.max(Math.hypot(a.x, a.y), Math.hypot(a.x - plot.width, a.y), Math.hypot(a.x, a.y - plot.height),
      Math.hypot(a.x - plot.width, a.y - plot.height)) + Math.hypot(plot.width, plot.height);
  return { x: a.x + (dx / length) * reach, y: a.y + (dy / length) * reach };
}

/** The axis-aligned box two points span. */
export function boxOf(a: Pt, b: Pt): { left: number; right: number; top: number; bottom: number } {
  return {
    left: Math.min(a.x, b.x),
    right: Math.max(a.x, b.x),
    top: Math.min(a.y, b.y),
    bottom: Math.max(a.y, b.y),
  };
}

/**
 * Whether a click at `p` lands on a drawing.
 *
 * Lines are hit along their length. A rectangle and a retracement are hit
 * anywhere inside them as well as on their edges: both are drawn with a fill,
 * and a fill that cannot be clicked reads as broken. `tolerance` is the reach
 * in pixels - wider for a finger than for a mouse.
 */
export function hitsDrawing(shape: ProjectedDrawing, p: Pt, plot: Plot, tolerance: number): boolean {
  const [a, b] = shape.points;
  if (!a) return false;

  switch (shape.kind) {
    case 'hline':
      return Math.abs(p.y - a.y) <= tolerance;
    case 'vline':
      return Math.abs(p.x - a.x) <= tolerance;
    case 'trend':
      return !!b && distanceToSegment(p, a, b) <= tolerance;
    case 'ray':
      return !!b && distanceToSegment(p, a, rayEnd(a, b, plot)) <= tolerance;
    case 'rect':
    case 'fib': {
      if (!b) return false;
      const box = boxOf(a, b);
      return (
        p.x >= box.left - tolerance &&
        p.x <= box.right + tolerance &&
        p.y >= box.top - tolerance &&
        p.y <= box.bottom + tolerance
      );
    }
    default:
      return false;
  }
}

/**
 * The drawing a click selects: the topmost one it lands on.
 *
 * Drawings are painted in order, so the last one in the list is on top and is
 * the one the trader can see under the pointer.
 */
export function topmostHit(shapes: ProjectedDrawing[], p: Pt, plot: Plot, tolerance: number): string | null {
  for (let i = shapes.length - 1; i >= 0; i -= 1) {
    if (hitsDrawing(shapes[i]!, p, plot, tolerance)) return shapes[i]!.id;
  }
  return null;
}

/**
 * Where the selected drawing's delete button hangs, before it is kept inside
 * the plot: off the end of a line, at the top of a vertical, at the top-right
 * corner of a box.
 */
export function actionAnchor(shape: ProjectedDrawing, plot: Plot): Pt | null {
  const [a, b] = shape.points;
  if (!a) return null;

  switch (shape.kind) {
    case 'hline':
      return { x: plot.width, y: a.y };
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
      const box = boxOf(a, b);
      return { x: box.right, y: box.top };
    }
    default:
      return null;
  }
}
