import type { AdminTrade } from '../types/api';

/**
 * A trade voided because no valid expiry price existed: its stake was
 * returned. The server keeps the status DRAW for older readers and says so in
 * `voided`; a server between the two only sends the settlement rule.
 */
export function isVoided(trade: Pick<AdminTrade, 'status' | 'voided' | 'expiryPriceSource'>): boolean {
  return trade.status === 'DRAW' && (trade.voided === true || trade.expiryPriceSource === 'NO_PRICE_VOID_REFUND');
}

export const VOID_LABEL = 'VOID · REFUNDED';
