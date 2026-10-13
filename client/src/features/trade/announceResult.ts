import { useTradeResultStore } from '../../stores/tradeResultStore';
import type { Trade } from '../../types/api';

/**
 * Puts a settled trade on screen as a result.
 *
 * A function rather than a call at each site, because a trade can settle down
 * two paths - the socket's settlement event, and an early close resolving from
 * its own response - and both have to describe the result identically. The
 * store drops a trade id it has already announced, so a trade that arrives
 * down both paths is still only shown once. The figures themselves are worked
 * out where they are drawn, from `settlementOf` and `payoutOf`.
 */
export function announceResult(trade: Trade): void {
  useTradeResultStore.getState().announce(trade);
}
