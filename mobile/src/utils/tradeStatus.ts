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
