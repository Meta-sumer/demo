import { describe, expect, it } from 'vitest';
import { isVoided } from '../src/utils/tradeStatus';

/** A trade voided for want of a price is not shown as a tie; old trades read as before. */
describe('isVoided', () => {
  it('reads the voided flag and the settlement rule', () => {
    expect(isVoided({ status: 'DRAW', voided: true })).toBe(true);
    expect(isVoided({ status: 'DRAW', expiryPriceSource: 'NO_PRICE_VOID_REFUND' })).toBe(true);
  });

  it('keeps a real tie and older trades as a draw', () => {
    expect(isVoided({ status: 'DRAW', expiryPriceSource: 'TICK_AT_OR_BEFORE_EXPIRY' })).toBe(false);
    expect(isVoided({ status: 'DRAW' })).toBe(false);
    expect(isVoided({ status: 'WON', voided: true })).toBe(false);
  });
});
