import type { DrawPolicy, Trade } from '../../types/api';

/**
 * What a running trade would pay if it ended at a given price (brief UX-12,
 * reference record 15).
 *
 * The comparison is the server's own settlement rule, applied to the price on
 * screen: UP wins above the entry price, DOWN below, and an exact match
 * follows the platform's draw policy (see `drawRule`) - a draw that refunds
 * the investment by default. The amounts are the ones fixed when the
 * trade was accepted - the profit the server wrote onto the trade, or the stake
 * - so the client prices nothing here; it only says which of three known
 * outcomes the current price points at.
 *
 * It is a projection and is always worded as one ("If it ends now"). It is not
 * a result: the price that decides is the last one at or before expiry, which
 * the screen may not have seen yet, and a settled trade's result is only ever
 * read with `settlementOf`. It is not the "Sell now" value either - that is
 * what the platform would pay to close early, and it comes from the server.
 */

export type Standing = 'winning' | 'losing' | 'draw';

export interface LiveResult {
  standing: Standing;
  /** Signed, minor units: the profit on a win, minus the stake on a loss, zero on a draw. */
  amount: number;
}

/** The words after the amount: "+₹82.00 · winning". */
export const STANDING_WORDS: Record<Standing, string> = {
  winning: 'winning',
  losing: 'losing',
  draw: 'draw, refunded',
};

type Terms = Pick<Trade, 'direction' | 'entryPrice' | 'stake' | 'profit' | 'drawPolicy'>;

const WINNING = (trade: Terms): LiveResult => ({ standing: 'winning', amount: trade.profit });
const LOSING = (trade: Terms): LiveResult => ({ standing: 'losing', amount: -trade.stake });

/**
 * Null without a usable price: no number is better than one computed from nothing.
 * A tie follows the rule frozen on the trade when the server sends it; `drawPolicy`
 * (the platform's current rule) is only the fallback for older trades.
 */
export function liveResult(
  trade: Terms,
  price: number | null | undefined,
  drawPolicy: DrawPolicy = 'REFUND',
): LiveResult | null {
  if (price === null || price === undefined || !Number.isFinite(price)) return null;
  if (price === trade.entryPrice) {
    const policy = trade.drawPolicy ?? drawPolicy;
    if (policy === 'LOSS') return LOSING(trade);
    if (policy === 'WIN') return WINNING(trade);
    return { standing: 'draw', amount: 0 };
  }
  const above = price > trade.entryPrice;
  const winning = trade.direction === 'UP' ? above : !above;
  return winning ? WINNING(trade) : LOSING(trade);
}

export interface LiveTotal {
  /** Sum over the trades that are still running and have a price, minor units. */
  amount: number;
  /** Trades still running (expiry in the future). */
  running: number;
  /** Of those, how many had a price to project from. */
  priced: number;
}

/**
 * The same projection summed over every open trade of an account.
 *
 * A trade past its expiry is left out: its outcome is already decided by a
 * price the screen cannot be sure it saw, so projecting it from the price now
 * would be answering a different question. Trades without a price yet are
 * counted separately, so the total never silently covers fewer trades than
 * the line claims.
 */
export function liveTotal(
  trades: ReadonlyArray<Terms & Pick<Trade, 'assetId' | 'expiryTimestamp'>>,
  priceOf: (assetId: string) => number | null | undefined,
  now: number,
  drawPolicy: DrawPolicy = 'REFUND',
): LiveTotal {
  let amount = 0;
  let running = 0;
  let priced = 0;
  for (const trade of trades) {
    if (trade.expiryTimestamp <= now) continue;
    running += 1;
    const result = liveResult(trade, priceOf(trade.assetId), drawPolicy);
    if (!result) continue;
    priced += 1;
    amount += result.amount;
  }
  return { amount, running, priced };
}

/** The colour role of an amount: gains green, losses red, nothing neutral. */
export function amountTone(amount: number): 'up' | 'down' | 'neutral' {
  return amount > 0 ? 'up' : amount < 0 ? 'down' : 'neutral';
}
