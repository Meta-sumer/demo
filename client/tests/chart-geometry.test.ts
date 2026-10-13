import { describe, expect, it } from 'vitest';
import {
  lastAtOrBefore,
  logicalToTime,
  logicalToX,
  timeToLogical,
  timeToX,
  xToLogical,
} from '../src/features/trade/chart/timeProjection';
import {
  actionAnchor,
  distanceToSegment,
  fibPrice,
  FIB_LEVELS,
  hitsDrawing,
  rayEnd,
  topmostHit,
  type ProjectedDrawing,
} from '../src/features/trade/chart/drawingGeometry';

const MINUTE = 60_000;
const T0 = 1_790_000_000_000;
const bars = (count: number, bucket = MINUTE, start = T0) =>
  Array.from({ length: count }, (_, i) => ({ time: start + i * bucket }));

describe('timeToLogical', () => {
  it('puts each bar open on its own slot and mid-bar instants between slots', () => {
    const series = bars(10);

    expect(timeToLogical(T0, series, MINUTE)).toBe(0);
    expect(timeToLogical(T0 + 3 * MINUTE, series, MINUTE)).toBe(3);
    expect(timeToLogical(T0 + 3 * MINUTE + 15_000, series, MINUTE)).toBeCloseTo(3.25, 10);
  });

  it('extrapolates past the last bar, which is where an expiry lives', () => {
    const series = bars(10);

    // Last bar is index 9; five minutes after it opened is 5 slots on.
    expect(timeToLogical(T0 + 9 * MINUTE + 5 * MINUTE, series, MINUTE)).toBe(14);
  });

  it('extrapolates before the first bar too', () => {
    expect(timeToLogical(T0 - 2 * MINUTE, bars(10), MINUTE)).toBe(-2);
  });

  it('holds an instant inside a gap at the next bar rather than stretching the one before', () => {
    // A closed market: bars at 0, 1 and then 10 minutes.
    const series = [{ time: T0 }, { time: T0 + MINUTE }, { time: T0 + 10 * MINUTE }];

    expect(timeToLogical(T0 + MINUTE + 30_000, series, MINUTE)).toBeCloseTo(1.5, 10);
    expect(timeToLogical(T0 + 5 * MINUTE, series, MINUTE)).toBe(2);
    expect(timeToLogical(T0 + 10 * MINUTE, series, MINUTE)).toBe(2);
  });

  it('has no answer without bars or with a nonsense bucket', () => {
    expect(timeToLogical(T0, [], MINUTE)).toBeNull();
    expect(timeToLogical(T0, bars(3), 0)).toBeNull();
    expect(timeToLogical(Number.NaN, bars(3), MINUTE)).toBeNull();
  });

  it('round-trips through logicalToTime inside, before and after the data', () => {
    const series = bars(50, 5_000);
    for (const logical of [-7.5, 0, 0.3, 12.75, 49, 49.4, 80]) {
      const time = logicalToTime(logical, series, 5_000)!;
      expect(timeToLogical(time, series, 5_000)).toBeCloseTo(logical, 9);
    }
  });
});

describe('lastAtOrBefore', () => {
  const series = bars(5);

  it('finds the bar an instant falls in', () => {
    expect(lastAtOrBefore(series, T0 + 2 * MINUTE + 1)).toBe(2);
    expect(lastAtOrBefore(series, T0 + 2 * MINUTE)).toBe(2);
  });

  it('says -1 before the first bar', () => {
    expect(lastAtOrBefore(series, T0 - 1)).toBe(-1);
  });
});

describe('the logical axis', () => {
  const axis = { x0: 100, spacing: 10 };

  it('is a straight line through the whole indices, fractional ones included', () => {
    expect(logicalToX(axis, 0)).toBe(100);
    expect(logicalToX(axis, 2.5)).toBe(125);
    expect(xToLogical(axis, 125)).toBe(2.5);
  });

  it('projects an instant straight to a pixel', () => {
    expect(timeToX(axis, T0 + 90_000, bars(10), MINUTE)).toBe(115);
    expect(timeToX(null, T0, bars(10), MINUTE)).toBeNull();
  });
});

describe('fibPrice', () => {
  it('puts 0 at the end of the move and 1 back at its start', () => {
    // A move up from 100 to 200.
    expect(fibPrice(100, 200, 0)).toBe(200);
    expect(fibPrice(100, 200, 1)).toBe(100);
    expect(fibPrice(100, 200, 0.5)).toBe(150);
    expect(fibPrice(100, 200, 0.618)).toBeCloseTo(138.2, 10);
  });

  it('works the same way for a move down', () => {
    expect(fibPrice(200, 100, 0.382)).toBeCloseTo(138.2, 10);
  });

  it('draws the six usual levels', () => {
    expect([...FIB_LEVELS]).toEqual([0, 0.236, 0.382, 0.5, 0.618, 1]);
  });
});

describe('segment and ray geometry', () => {
  it('measures to the nearest point of a segment, clamped at its ends', () => {
    const a = { x: 0, y: 0 };
    const b = { x: 10, y: 0 };

    expect(distanceToSegment({ x: 5, y: 3 }, a, b)).toBe(3);
    expect(distanceToSegment({ x: 13, y: 4 }, a, b)).toBe(5);
    expect(distanceToSegment({ x: 2, y: 2 }, a, a)).toBeCloseTo(Math.SQRT2 * 2, 10);
  });

  it('extends a ray past the plot in the direction it points', () => {
    const plot = { width: 400, height: 300 };
    const end = rayEnd({ x: 100, y: 150 }, { x: 110, y: 150 }, plot);

    expect(end.y).toBe(150);
    expect(end.x).toBeGreaterThan(400);
  });
});

describe('hit testing', () => {
  const plot = { width: 400, height: 300 };
  const shapes: ProjectedDrawing[] = [
    { id: 'box', kind: 'rect', points: [{ x: 50, y: 50 }, { x: 150, y: 120 }] },
    { id: 'level', kind: 'hline', points: [{ x: 0, y: 100 }] },
    { id: 'trend', kind: 'trend', points: [{ x: 200, y: 200 }, { x: 300, y: 250 }] },
    { id: 'ray', kind: 'ray', points: [{ x: 10, y: 280 }, { x: 20, y: 280 }] },
    { id: 'time', kind: 'vline', points: [{ x: 350, y: 0 }] },
    { id: 'fib', kind: 'fib', points: [{ x: 220, y: 20 }, { x: 320, y: 80 }] },
  ];

  it('finds each kind where it is drawn', () => {
    expect(topmostHit(shapes, { x: 250, y: 226 }, plot, 6)).toBe('trend');
    expect(topmostHit(shapes, { x: 380, y: 282 }, plot, 6)).toBe('ray');
    expect(topmostHit(shapes, { x: 352, y: 150 }, plot, 6)).toBe('time');
    expect(topmostHit(shapes, { x: 270, y: 50 }, plot, 6)).toBe('fib');
    expect(topmostHit(shapes, { x: 60, y: 60 }, plot, 6)).toBe('box');
  });

  it('prefers the drawing on top where two overlap', () => {
    // The level runs through the box and was drawn after it.
    expect(topmostHit(shapes, { x: 100, y: 101 }, plot, 6)).toBe('level');
  });

  it('misses open chart', () => {
    expect(topmostHit(shapes, { x: 180, y: 160 }, plot, 6)).toBeNull();
  });

  it('reaches further for a finger than for a mouse', () => {
    const trend = shapes[2]!;
    expect(hitsDrawing(trend, { x: 250, y: 236 }, plot, 6)).toBe(false);
    expect(hitsDrawing(trend, { x: 250, y: 236 }, plot, 14)).toBe(true);
  });

  it('does not extend a trend line past its ends the way a ray does', () => {
    expect(hitsDrawing(shapes[2]!, { x: 340, y: 270 }, plot, 6)).toBe(false);
    expect(hitsDrawing({ ...shapes[2]!, kind: 'ray' }, { x: 340, y: 270 }, plot, 6)).toBe(true);
  });
});

describe('actionAnchor', () => {
  const plot = { width: 400, height: 300 };

  it('hangs off the top-right of a box and the upper end of a line', () => {
    expect(actionAnchor({ id: 'r', kind: 'rect', points: [{ x: 50, y: 120 }, { x: 150, y: 50 }] }, plot)).toEqual({
      x: 150,
      y: 50,
    });
    expect(actionAnchor({ id: 't', kind: 'trend', points: [{ x: 50, y: 120 }, { x: 150, y: 50 }] }, plot)).toEqual({
      x: 150,
      y: 50,
    });
  });

  it('puts a level at the right edge and a vertical at the top', () => {
    expect(actionAnchor({ id: 'h', kind: 'hline', points: [{ x: 0, y: 80 }] }, plot)).toEqual({ x: 400, y: 80 });
    expect(actionAnchor({ id: 'v', kind: 'vline', points: [{ x: 90, y: 0 }] }, plot)).toEqual({ x: 90, y: 0 });
  });
});
