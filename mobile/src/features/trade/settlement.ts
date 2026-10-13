import type { Trade } from '../../types/api';

/**
 * What a settled trade was worth.
 *
 * Kept in one place because two very different screens ask the same question -
 * the chart overlay, which colours a marker by it, and the result popup, which
 * announces it - and an answer that disagreed between them would be a bug the
 * trader reads as the platform miscounting their money.
 */

/**
 * Signed profit or loss, in minor units.
 *
 * `settlementAmount` is already net and already signed: the server writes
 * `payoutAmount - stake` on a win, `-stake` on a loss, zero on a draw, and the
 * customer's result on an early close. It is not a gross return, so nothing
 * may be deducted from it here. The fallbacks only cover a trade that reached
 * a terminal status without the field being populated.
 */
export function settlementOf(trade: Trade): number | null {
  if (trade.settlementAmount !== null) return trade.settlementAmount;
  if (trade.status === 'WON') return trade.profit;
  if (trade.status === 'LOST') return -trade.stake;
  if (trade.status === 'DRAW' || trade.status === 'CANCELLED') return 0;
  return null;
}

/**
 * What came back to the wallet, in minor units.
 *
 * The stake left the wallet when the trade opened, so the amount returned is
 * the stake plus the signed result: the full payout on a win, nothing at all
 * on a loss, the stake back on a draw. This is the figure the result popup
 * shows, which is why a loss reads as a plain zero rather than as a negative -
 * the money is already gone, and it does not go again.
 */
export function payoutOf(trade: Trade): number {
  return trade.stake + (settlementOf(trade) ?? -trade.stake);
}
