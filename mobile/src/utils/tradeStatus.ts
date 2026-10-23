import type { Trade, TradeStatus } from '../types/api';

/**
 * The words used for a trade's state, everywhere.
 *
 * The brief (section 28) requires one terminology across Trade, Active,
 * Result, History and Trade Detail. Screens used to print the raw status
 * codes in some places ("CLOSED_EARLY") and their own words in others
 * ("CLOSED"); every screen now asks here.
 */
const LABELS: Record<TradeStatus, string> = {
  OPEN: 'Active',
  SETTLING: 'Settling',
  WON: 'Won',
  LOST: 'Lost',
  DRAW: 'Draw · refunded',
  CANCELLED: 'Cancelled',
  CLOSED_EARLY: 'Sold early',
};

export function statusLabel(status: TradeStatus): string {
  return LABELS[status] ?? status;
}

export const VOID_LABEL = 'Void · refunded';

const VOID_RULE = 'NO_PRICE_VOID_REFUND';

const VOID_REASON = 'No valid market price at expiry; your stake was returned.';

type VoidFields = Pick<Trade, 'status' | 'voided' | 'expiryPriceSource'>;

/**
 * A trade voided because no valid expiry price existed (stake returned). The
 * server keeps its status DRAW for older apps and says so in `voided`; a
 * server between the two only sends the settlement rule.
 */
export function isVoided(trade: VoidFields): boolean {
  return trade.status === 'DRAW' && (trade.voided === true || trade.expiryPriceSource === VOID_RULE);
}

/** The trade's state in words: statusLabel, except that a void reads as one. */
export function tradeLabel(trade: VoidFields): string {
  return isVoided(trade) ? VOID_LABEL : statusLabel(trade.status);
}

/** Why a voided trade was voided; null for any other trade. */
export function voidReason(trade: VoidFields & Pick<Trade, 'voidReason'>): string | null {
  if (!isVoided(trade)) return null;
  return trade.voidReason ? trade.voidReason : VOID_REASON;
}

export type StatusTone = 'up' | 'down' | 'neutral' | 'live';

/** Colour role for a status: won green, lost red, running live, the rest neutral. */
export function statusTone(trade: Pick<Trade, 'status' | 'settlementAmount'>): StatusTone {
  switch (trade.status) {
    case 'OPEN':
    case 'SETTLING':
      return 'live';
    case 'WON':
      return 'up';
    case 'LOST':
      return 'down';
    case 'CLOSED_EARLY':
      return (trade.settlementAmount ?? 0) > 0 ? 'up' : (trade.settlementAmount ?? 0) < 0 ? 'down' : 'neutral';
    default:
      return 'neutral';
  }
}

export function isActive(status: TradeStatus): boolean {
  return status === 'OPEN' || status === 'SETTLING';
}

/** How long the trade was opened for, in seconds (format with formatExpiry). */
export function durationOf(trade: Pick<Trade, 'entryTimestamp' | 'expiryTimestamp'>): number {
  return Math.round((trade.expiryTimestamp - trade.entryTimestamp) / 1000);
}
