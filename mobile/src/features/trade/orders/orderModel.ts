import type { ChartPalette } from '../../../utils/theme-colors';
import { withAlpha } from '../../../utils/theme-colors';
import type { Direction, Trade } from '../../../types/api';
import type { OrderPreview } from '../../../stores/orderPreviewStore';
import { settlementOf } from '../settlement';

/**
 * The order overlay's view model.
 *
 * The chart draws orders, not trades. A trade is what the server owns - a row
 * with a status; an order is what the trader sees - a line at a price, a tag,
 * a countdown, and a result. The two are not the same shape, and the preview
 * that appears before a trade exists has no server row at all.
 *
 * Everything in this file is pure. The overlay is redrawn from the same inputs
 * every second, so any state that lived here would drift out of step with the
 * trade store the moment a settlement arrived.
 */
export type OrderVisualState = 'PREVIEW' | 'ACTIVE' | 'EXPIRED' | 'WON' | 'LOST' | 'CANCELLED';

export interface OverlayOrder {
  id: string;
  state: OrderVisualState;
  direction: Direction;
  assetId: string;
  stake: number;
  currency: string;
  /**
   * Frozen the instant the order is committed and never recomputed.
   *
   * `null` means the preview is still tracking the market, in which case the
   * layer substitutes the live price each frame. Once a value is present it is
   * the price the position is measured against, so nothing may overwrite it.
   */
  entryPrice: number | null;
  entryTimestamp: number;
  expiryTimestamp: number;
  potentialReturn: number;
  payoutPercent: number;
  /** Signed settlement in minor units; null until the trade settles. */
  settlement: number | null;
  expiryPrice: number | null;
  /** True once the countdown has run out but the server has not settled yet. */
  awaitingSettlement: boolean;
  /**
   * The order's window slides with the clock rather than being pinned.
   *
   * Only an uncommitted preview does this. Its entry is "now" and its expiry
   * is "now plus the chosen duration", so both have to be recomputed as the
   * clock moves. Taking them from the one-second heartbeat made the whole
   * preview hop a second's width once a second; the projection recomputes
   * them per frame instead, and it glides.
   */
  tracksClock: boolean;
  /** The chosen duration, for recomputing a tracking expiry. */
  expirySeconds: number;
  /** Wall clock at which a finished order stops being drawn. */
  fadeAt: number | null;
}

/** How long a settled order stays on the chart before it fades out. */
export const SETTLED_LINGER_MS = 6000;
/** How long a rejected order shows its cancelled state. */
export const CANCELLED_LINGER_MS = 1800;

interface BuildInput {
  assetId: string | null;
  preview: OrderPreview | null;
  openTrades: Trade[];
  settled: Trade[];
  /** Trade id -> when this client first saw it settled, for the fade clock. */
  seenAt: Map<string, number>;
  now: number;
}

/**
 * Collapses the preview, the open trades and the recently settled ones into a
 * single list for the layer to draw.
 *
 * Ordering matters: the list is rendered in order and later entries paint over
 * earlier ones, so the preview - the thing the trader is actively pointing at -
 * goes last.
 */
export function buildOverlayOrders({
  assetId,
  preview,
  openTrades,
  settled,
  seenAt,
  now,
}: BuildInput): OverlayOrder[] {
  if (!assetId) return [];
  const orders: OverlayOrder[] = [];

  for (const trade of settled) {
    if (trade.assetId !== assetId) continue;
    const first = seenAt.get(trade.id);
    if (first === undefined) continue;
    const fadeAt = first + SETTLED_LINGER_MS;
    if (now >= fadeAt) continue;
    orders.push(fromTrade(trade, now, fadeAt));
  }

  for (const trade of openTrades) {
    if (trade.assetId !== assetId) continue;
    orders.push(fromTrade(trade, now, null));
  }

  if (preview && preview.assetId === assetId) orders.push(fromPreview(preview, now));

  return orders;
}

function fromTrade(trade: Trade, now: number, fadeAt: number | null): OverlayOrder {
  const expired = now >= trade.expiryTimestamp;
  const state = visualStateOf(trade, expired);

  return {
    id: trade.id,
    state,
    direction: trade.direction,
    assetId: trade.assetId,
    stake: trade.stake,
    currency: trade.currency,
    entryPrice: trade.entryPrice,
    entryTimestamp: trade.entryTimestamp,
    expiryTimestamp: trade.expiryTimestamp,
    potentialReturn: trade.potentialReturn,
    payoutPercent: trade.payoutPercent,
    settlement: settlementOf(trade),
    expiryPrice: trade.expiryPrice,
    awaitingSettlement: state === 'EXPIRED',
    tracksClock: false,
    expirySeconds: Math.max(0, Math.round((trade.expiryTimestamp - trade.entryTimestamp) / 1000)),
    fadeAt,
  };
}

function visualStateOf(trade: Trade, expired: boolean): OrderVisualState {
  switch (trade.status) {
    case 'WON':
      return 'WON';
    case 'LOST':
      return 'LOST';
    case 'CANCELLED':
      return 'CANCELLED';
    case 'DRAW':
      return 'EXPIRED';
    case 'CLOSED_EARLY':
      // Closed before expiry: the money already moved, so it reads as a result
      // rather than as an expiry. The amount is signed, so zero is the line -
      // selling out at a small profit is a win, taking a haircut is not.
      return (trade.settlementAmount ?? 0) >= 0 ? 'WON' : 'LOST';
    case 'SETTLING':
      return 'EXPIRED';
    case 'OPEN':
    default:
      // The countdown can reach zero a beat before the settlement lands. That
      // gap is its own state - the order is no longer live, but no result
      // exists yet, and showing it as active would be a lie.
      return expired ? 'EXPIRED' : 'ACTIVE';
  }
}

function fromPreview(preview: OrderPreview, now: number): OverlayOrder {
  // An intent preview has not committed to a moment yet, so its window slides
  // with the clock. A placing one is pinned to the instant it was sent.
  const entryTimestamp = preview.entryTimestamp ?? now;
  const expiryTimestamp = preview.expiryTimestamp ?? now + preview.expirySeconds * 1000;

  return {
    id: preview.id,
    state: preview.phase === 'REJECTED' ? 'CANCELLED' : 'PREVIEW',
    direction: preview.direction,
    assetId: preview.assetId,
    stake: preview.stake,
    currency: preview.currency,
    entryPrice: preview.entryPrice,
    entryTimestamp,
    expiryTimestamp,
    potentialReturn: preview.potentialReturn,
    payoutPercent: preview.payoutPercent,
    settlement: null,
    expiryPrice: null,
    awaitingSettlement: false,
    // Pinned the moment it is committed; sliding until then.
    tracksClock: preview.entryTimestamp === null,
    expirySeconds: preview.expirySeconds,
    fadeAt: preview.phase === 'REJECTED' ? preview.phaseAt + CANCELLED_LINGER_MS : null,
  };
}

/**
 * Whether an order's visuals still have to be refreshed.
 *
 * The layer keeps a one-second heartbeat only while something is counting down
 * or fading; an idle chart does no work at all.
 */
export function needsHeartbeat(orders: OverlayOrder[]): boolean {
  return orders.some((order) => order.state === 'ACTIVE' || order.state === 'PREVIEW' || order.fadeAt !== null);
}

export interface OrderTone {
  /** The order's own colour - direction while live, outcome once settled. */
  accent: string;
  /** Lifted variant for hover and emphasis. */
  bright: string;
  /** Translucent fill for the direction band and marker backgrounds. */
  soft: string;
  /** Glow used when the order is hovered. */
  glow: string;
  /** Colour of the amount tag's background. */
  tag: string;
  /** Readable text on top of `tag`. */
  tagText: string;
  /** Overall opacity; settled and cancelled orders recede. */
  opacity: number;
  /** Dashed rather than solid, for anything not yet real. */
  dashed: boolean;
}

/**
 * The colours for one order.
 *
 * The accent follows what the order is asking at each stage: direction while
 * it is still being chosen, brand yellow once it is placed and only its own
 * position matters, then the outcome once it settles - a losing UP trade must
 * not still read as green. The amount tag stays brand yellow throughout,
 * because it is the one element that has to be findable at a glance whatever
 * the order is doing.
 */
export function orderTone(order: OverlayOrder, palette: ChartPalette): OrderTone {
  const up = order.direction === 'UP';
  const directional = {
    accent: up ? palette.up : palette.down,
    bright: up ? palette.upBright : palette.downBright,
  };

  switch (order.state) {
    case 'PREVIEW':
      return {
        ...directional,
        soft: withAlpha(directional.accent, 0.15),
        glow: withAlpha(directional.bright, 0.45),
        tag: withAlpha(palette.brand, 0.9),
        tagText: palette.panel,
        opacity: 0.9,
        dashed: true,
      };

    /*
     * A placed order is brand yellow, whichever way it is betting.
     *
     * Direction is the preview's question. Before the order exists the only
     * thing being decided is up or down, so the preview is green or red. Once
     * it is placed the question becomes "where did I enter and when does this
     * end", and a chart of green and red order lines answers that badly -
     * the candles behind them are already green and red. One house colour
     * separates the orders from the market; the arrow in the tag keeps the
     * direction readable.
     */
    case 'ACTIVE':
      return {
        accent: palette.brand,
        bright: palette.brand,
        soft: withAlpha(palette.brand, 0.14),
        glow: withAlpha(palette.brand, 0.55),
        tag: palette.brand,
        tagText: palette.panel,
        opacity: 1,
        dashed: false,
      };

    case 'EXPIRED':
      return {
        accent: palette.priceLine,
        bright: palette.strongText,
        soft: withAlpha(palette.priceLine, 0.12),
        glow: withAlpha(palette.priceLine, 0.35),
        tag: withAlpha(palette.priceLine, 0.85),
        tagText: palette.panel,
        opacity: 0.55,
        dashed: true,
      };

    case 'WON':
      return {
        accent: palette.up,
        bright: palette.upBright,
        soft: withAlpha(palette.up, 0.18),
        glow: withAlpha(palette.upBright, 0.6),
        tag: palette.up,
        tagText: palette.panel,
        opacity: 1,
        dashed: false,
      };

    case 'LOST':
      return {
        accent: palette.down,
        bright: palette.downBright,
        soft: withAlpha(palette.down, 0.18),
        glow: withAlpha(palette.downBright, 0.6),
        tag: palette.down,
        tagText: palette.panel,
        opacity: 1,
        dashed: false,
      };

    case 'CANCELLED':
    default:
      return {
        accent: palette.priceLine,
        bright: palette.priceLine,
        soft: withAlpha(palette.priceLine, 0.1),
        glow: 'transparent',
        tag: withAlpha(palette.priceLine, 0.6),
        tagText: palette.panel,
        opacity: 0.4,
        dashed: true,
      };
  }
}

/** Short badge text for the marker, e.g. the result once it is known. */
export function stateLabel(order: OverlayOrder): string | null {
  switch (order.state) {
    case 'PREVIEW':
      return 'Preview';
    case 'EXPIRED':
      return order.awaitingSettlement ? 'Settling' : 'Expired';
    case 'WON':
      return 'Won';
    case 'LOST':
      return 'Lost';
    case 'CANCELLED':
      return 'Cancelled';
    case 'ACTIVE':
    default:
      return null;
  }
}

/**
 * Whether the position is currently on the winning side.
 *
 * Purely indicative: settlement is the server's, and a position that is in the
 * money right now can still expire out of it.
 */
export function isInTheMoney(order: OverlayOrder, current: number | null): boolean | null {
  const entry = order.entryPrice;
  if (entry === null || current === null) return null;
  if (current === entry) return null;
  return order.direction === 'UP' ? current > entry : current < entry;
}
