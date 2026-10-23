import { describe, expect, it } from 'vitest';
import { adjustmentCurrency, formatAdjustment, parseAdjustmentAmount } from '../src/utils/adjustment';

/** Manual adjustments are typed in the selected wallet's currency. */
describe('wallet adjustment amount', () => {
  it('reads INR in paise, two decimals at most', () => {
    expect(parseAdjustmentAmount('1000', 'LIVE')).toBe(100_000);
    expect(parseAdjustmentAmount('1,234.56', 'DEMO')).toBe(123_456);
    expect(parseAdjustmentAmount('.5', 'LIVE')).toBe(50);
    expect(parseAdjustmentAmount('0.001', 'LIVE')).toBeNull();
    expect(parseAdjustmentAmount('0', 'LIVE')).toBeNull();
    expect(parseAdjustmentAmount('-5', 'LIVE')).toBeNull();
    expect(parseAdjustmentAmount('abc', 'LIVE')).toBeNull();
    expect(parseAdjustmentAmount('', 'LIVE')).toBeNull();
  });

  it('reads USDT in micro-USDT, six decimals at most, without float drift', () => {
    expect(parseAdjustmentAmount('12.345678', 'FUNDING')).toBe(12_345_678);
    expect(parseAdjustmentAmount('0.1', 'FUNDING')).toBe(100_000);
    expect(parseAdjustmentAmount('1,000', 'FUNDING')).toBe(1_000_000_000);
    expect(parseAdjustmentAmount('0.0000001', 'FUNDING')).toBeNull();
    expect(parseAdjustmentAmount('0', 'FUNDING')).toBeNull();
  });

  it('names and formats the currency of the wallet', () => {
    expect(adjustmentCurrency('FUNDING')).toBe('USDT');
    expect(adjustmentCurrency('LIVE')).toBe('INR');
    expect(formatAdjustment(12_345_678, 'FUNDING')).toBe('12.345678 USDT');
    expect(formatAdjustment(123_456, 'LIVE')).toContain('1,234.56');
  });
});
