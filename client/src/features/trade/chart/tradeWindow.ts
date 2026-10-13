import type { Candle, Trade } from '../../../types/api';

/**
 * The price history a trade is drawn against, on its detail page.
 *
 * Two sources, chosen by age. The server keeps raw ticks for fifteen minutes,
 * and a trade inside that window is drawn from them - a trade that lasted
 * thirty seconds is barely one candle wide, and only the ticks show what the
 * price actually did between the entry and the expiry. Anything older falls
 * back to one-minute candles, with more context either side so the trade has
 * something to be read against.
 *
 * All of this is pure; the page does the fetching.
 */

/** How long the server keeps raw ticks. */
export const TICK_RETENTION_MS = 15 * 60_000;

/**
 * Held back from the retention edge. A request takes time, clocks disagree by
 * a few seconds, and a window that starts a moment too early comes back with
 * its beginning missing - which would draw the entry in empty space.
 */
const RETENTION_MARGIN_MS = 45_000;

const MINUTE = 60_000;

export interface TradeMoments {
  entry: number;
  entryPrice: number;
  /** When the trade was due to end. */
  expiry: number;
  /**
   * When the position actually ended, and at what price: the expiry for a
   * settled trade, the moment it was sold for one closed early. Null while it
   * is still running, and for a cancelled trade, which never had an exit.
   */
  exit: number | null;
  exitPrice: number | null;
}

export function tradeMoments(trade: Trade): TradeMoments {
  const soldAt = trade.status === 'CLOSED_EARLY' && trade.settledAt ? Date.parse(trade.settledAt) : Number.NaN;
  const ended = trade.status !== 'OPEN' && trade.status !== 'SETTLING' && trade.status !== 'CANCELLED';
  const exit = ended && trade.expiryPrice !== null ? (Number.isFinite(soldAt) ? soldAt : trade.expiryTimestamp) : null;

  return {
    entry: trade.entryTimestamp,
    entryPrice: trade.entryPrice,
    expiry: trade.expiryTimestamp,
    exit,
    exitPrice: exit === null ? null : trade.expiryPrice,
  };
}

export interface TickWindow {
  from: number;
  to: number;
  /** Spacing of the resampled line. */
  stepMs: number;
}

/**
 * The span drawn from ticks: the trade plus a margin either side.
 *
 * A quarter of the trade's length each way, between ten seconds and two
 * minutes, so the entry is not drawn against the left edge nor the exit
 * against the right. The step keeps the line to a few hundred points: finer
 * for the short trades, where every half-second is a visible part of it.
 */
export function tickWindow(trade: Pick<Trade, 'entryTimestamp' | 'expiryTimestamp'>): TickWindow {
  const duration = Math.max(0, trade.expiryTimestamp - trade.entryTimestamp);
  const pad = clamp(duration * 0.25, 10_000, 120_000);
  const stepMs = duration <= MINUTE ? 500 : duration <= 5 * MINUTE ? 1000 : 2000;
  return { from: trade.entryTimestamp - pad, to: trade.expiryTimestamp + pad, stepMs };
}

/** Whether the server still has the ticks for a window starting at `from`. */
export function ticksRetained(from: number, now: number): boolean {
  return now - from < TICK_RETENTION_MS - RETENTION_MARGIN_MS;
}

/** How much history to ask an older server for, which only understands "the last N ms". */
export function tickRequestWindow(from: number, now: number): number {
  return clamp(now - from + 5_000, 1_000, TICK_RETENTION_MS);
}

export interface RawTick {
  timestamp: number;
  price: number;
}

/** A point of the resampled line. No `value` is whitespace: a slot on the axis with nothing drawn. */
export interface GridPoint {
  time: number;
  value?: number;
}

/**
 * Raw ticks resampled onto a regular grid anchored at the entry.
 *
 * The chart spaces points by index, not by time, so raw ticks - which arrive
 * unevenly - would stretch the quiet stretches and squeeze the busy ones, and
 * the time axis would lie. A regular grid keeps it honest. Anchoring the grid
 * on the entry puts the entry exactly on a point, and the expiry too whenever
 * the duration is a whole number of steps, which it always is here: the
 * markers then sit on the line rather than beside it.
 *
 * Each grid point takes the last tick at or before it - the same rule the
 * server uses for the expiry price. Points before the first tick, and points
 * still in the future, are whitespace: they hold the axis open without
 * drawing anything, which is what lets a running trade show where it will
 * end.
 *
 * `pins` override the grid point they fall on, so the entry and exit are
 * drawn at the prices the platform actually recorded, not a neighbouring tick.
 */
export function resampleTicks(
  ticks: ReadonlyArray<RawTick>,
  window: TickWindow,
  anchor: number,
  now: number,
  pins: ReadonlyArray<{ time: number; price: number }> = [],
): GridPoint[] {
  const { from, to, stepMs } = window;
  if (!(stepMs > 0) || to < from) return [];

  const sorted = isAscending(ticks) ? ticks : [...ticks].sort((a, b) => a.timestamp - b.timestamp);
  const first = Math.ceil((from - anchor) / stepMs);
  const last = Math.floor((to - anchor) / stepMs);
  const out: GridPoint[] = [];

  let cursor = -1;
  for (let k = first; k <= last; k += 1) {
    const time = anchor + k * stepMs;
    if (time > now) {
      out.push({ time });
      continue;
    }
    while (cursor + 1 < sorted.length && sorted[cursor + 1]!.timestamp <= time) cursor += 1;
    out.push(cursor >= 0 ? { time, value: sorted[cursor]!.price } : { time });
  }

  for (const pin of pins) {
    if (pin.time > now) continue;
    const k = (pin.time - anchor) / stepMs;
    const index = Math.round(k) - first;
    // Only a pin that is really on the grid; anything between two points is
    // drawn where it happened, and the line is left as the ticks made it.
    if (Math.abs(k - Math.round(k)) > 1e-6 || index < 0 || index >= out.length) continue;
    out[index] = { time: out[index]!.time, value: pin.price };
  }

  return out;
}

/** Whether the ticks reach back to the entry - without that the entry would float in empty space. */
export function ticksReachEntry(ticks: ReadonlyArray<RawTick>, entry: number): boolean {
  return ticks.some((tick) => tick.timestamp <= entry);
}

export interface CandleWindow {
  from: number;
  to: number;
  limit: number;
}

/**
 * The span drawn from one-minute candles: twenty minutes of lead-in and five
 * after the expiry, never past now.
 */
export function candleWindow(trade: Pick<Trade, 'entryTimestamp' | 'expiryTimestamp'>, now: number): CandleWindow {
  const from = Math.floor((trade.entryTimestamp - 20 * MINUTE) / MINUTE) * MINUTE;
  const to = Math.max(from + MINUTE, Math.min(now, trade.expiryTimestamp + 5 * MINUTE));
  const limit = clamp(Math.ceil((to - from) / MINUTE) + 2, 10, MAX_CANDLES);
  return { from, to, limit };
}

/**
 * The candles that belong to the window, or null when the answer does not
 * contain the trade.
 *
 * Null is the honest answer for two different reasons that look the same from
 * here: a server that ignored `to` and sent the latest candles instead, and
 * history that was never kept. Either way there is nothing true to draw.
 */
export function candlesForWindow(candles: ReadonlyArray<Candle>, window: CandleWindow, entry: number): Candle[] | null {
  const inside = candles
    .filter((candle) => candle.time + MINUTE > window.from && candle.time <= window.to)
    .sort((a, b) => a.time - b.time);
  const holdsEntry = inside.some((candle) => candle.time <= entry && entry < candle.time + MINUTE);
  return holdsEntry ? inside : null;
}

/**
 * The most candles the server sends in one answer.
 *
 * Also the fallback for a server that does not understand `to`: its latest
 * thousand often still reach an older trade, because the stored history has
 * gaps and a thousand candles can span far more than a thousand minutes.
 */
export const MAX_CANDLES = 1000;

function isAscending(ticks: ReadonlyArray<RawTick>): boolean {
  for (let i = 1; i < ticks.length; i += 1) if (ticks[i]!.timestamp < ticks[i - 1]!.timestamp) return false;
  return true;
}

function clamp(value: number, min: number, max: number): number {
  return value < min ? min : value > max ? max : value;
}
