import { describe, expect, it } from 'vitest';
import { formatUsdt, parseUsdt, toUsdtInput } from '../src/utils/usdt';

/**
 * USDT amounts are integer micro-USDT on the wire. A parse that went through
 * a float could turn "0.1" into 99_999 or 100_001 micro, which is a real
 * difference to a ledger that must balance to the last unit - so these pin
 * down exact results, including the awkward inputs a person actually types.
 */
describe('parseUsdt', () => {
  it('reads whole and fractional amounts exactly', () => {
    expect(parseUsdt('100')).toBe(100_000_000);
    expect(parseUsdt('100.5')).toBe(100_500_000);
    expect(parseUsdt('1250.500000')).toBe(1_250_500_000);
    expect(parseUsdt('0.1')).toBe(100_000);
    expect(parseUsdt('0.3')).toBe(300_000);
    expect(parseUsdt('19.99')).toBe(19_990_000);
  });

  it('reads the smallest unit', () => {
    expect(parseUsdt('0.000001')).toBe(1);
    expect(parseUsdt('.000001')).toBe(1);
  });

  it('accepts a leading point and a trailing point', () => {
    expect(parseUsdt('.5')).toBe(500_000);
    // Mid-entry: the customer has typed "5." on the way to "5.25".
    expect(parseUsdt('5.')).toBe(5_000_000);
  });

  it('parses zero as zero (whether zero is allowed is the caller’s call)', () => {
    expect(parseUsdt('0')).toBe(0);
    expect(parseUsdt('0.000000')).toBe(0);
    expect(parseUsdt('000')).toBe(0);
  });

  it('ignores surrounding whitespace and leading zeros', () => {
    expect(parseUsdt('  42  ')).toBe(42_000_000);
    expect(parseUsdt('007.25')).toBe(7_250_000);
  });

  it('handles large values without losing digits', () => {
    expect(parseUsdt('1000000000')).toBe(1_000_000_000_000_000);
    expect(parseUsdt('9007199254.740991')).toBe(Number.MAX_SAFE_INTEGER);
    expect(parseUsdt('123456789.123456')).toBe(123_456_789_123_456);
  });

  it('refuses a value too large to hold exactly', () => {
    expect(parseUsdt('9007199254.740992')).toBeNull();
    expect(parseUsdt('99999999999')).toBeNull();
    expect(parseUsdt('1' + '0'.repeat(30))).toBeNull();
  });

  it('refuses more than six decimals rather than rounding', () => {
    expect(parseUsdt('0.0000001')).toBeNull();
    expect(parseUsdt('1.1234567')).toBeNull();
  });

  it('refuses signs, exponents and junk', () => {
    for (const input of ['-1', '+1', '-0.5', '1e3', '1E3', '1e-3', 'Infinity', 'NaN', '0x10', 'abc', '1a', '$5', '5 USDT']) {
      expect(parseUsdt(input), input).toBeNull();
    }
  });

  it('refuses empty input and a lone point', () => {
    expect(parseUsdt('')).toBeNull();
    expect(parseUsdt('   ')).toBeNull();
    expect(parseUsdt('.')).toBeNull();
    expect(parseUsdt('1..5')).toBeNull();
    expect(parseUsdt('1.5.')).toBeNull();
  });

  it('refuses separators consistently - thousands commas, comma decimals and spaces alike', () => {
    expect(parseUsdt('1,000')).toBeNull();
    expect(parseUsdt('1,000.50')).toBeNull();
    expect(parseUsdt('1.000,50')).toBeNull();
    expect(parseUsdt('0,5')).toBeNull();
    expect(parseUsdt('1 000')).toBeNull();
    expect(parseUsdt('1_000')).toBeNull();
  });
});

describe('formatUsdt', () => {
  it('shows six decimals and the unit by default', () => {
    expect(formatUsdt(1_250_500_000)).toBe('1,250.500000 USDT');
    expect(formatUsdt(0)).toBe('0.000000 USDT');
    expect(formatUsdt(1)).toBe('0.000001 USDT');
    expect(formatUsdt(100_000)).toBe('0.100000 USDT');
  });

  it('groups thousands in large values', () => {
    expect(formatUsdt(1_000_000_000_000_000)).toBe('1,000,000,000.000000 USDT');
    expect(formatUsdt(Number.MAX_SAFE_INTEGER)).toBe('9,007,199,254.740991 USDT');
    expect(formatUsdt(999_999_999)).toBe('999.999999 USDT');
  });

  it('trims trailing zeros but keeps two decimals when asked', () => {
    expect(formatUsdt(1_250_500_000, { trim: true })).toBe('1,250.50 USDT');
    expect(formatUsdt(100_000_000, { trim: true })).toBe('100.00 USDT');
    expect(formatUsdt(1, { trim: true })).toBe('0.000001 USDT');
    expect(formatUsdt(1_234_567, { trim: true })).toBe('1.234567 USDT');
    expect(formatUsdt(10_100_000, { trim: true })).toBe('10.10 USDT');
  });

  it('signs negatives always and positives on request, never zero', () => {
    expect(formatUsdt(-5_000_000)).toBe('-5.000000 USDT');
    expect(formatUsdt(-1, { trim: true })).toBe('-0.000001 USDT');
    expect(formatUsdt(5_000_000, { signed: true })).toBe('+5.000000 USDT');
    expect(formatUsdt(0, { signed: true })).toBe('0.000000 USDT');
  });

  it('can leave the unit off', () => {
    expect(formatUsdt(2_500_000, { unit: false })).toBe('2.500000');
  });

  it('prints a missing value as a dash, not NaN', () => {
    expect(formatUsdt(Number.NaN)).toBe('--');
    expect(formatUsdt(undefined as unknown as number)).toBe('--');
  });

  it('round-trips with parseUsdt', () => {
    for (const micro of [0, 1, 10, 999_999, 1_000_000, 1_250_500_000, 123_456_789_123_456]) {
      const text = formatUsdt(micro, { unit: false }).replace(/,/g, '');
      expect(parseUsdt(text)).toBe(micro);
    }
  });
});

describe('toUsdtInput', () => {
  it('writes an amount back the way a person would type it', () => {
    expect(toUsdtInput(1_250_500_000)).toBe('1250.5');
    expect(toUsdtInput(100_000_000)).toBe('100');
    expect(toUsdtInput(1)).toBe('0.000001');
    expect(toUsdtInput(0)).toBe('0');
  });

  it('is read back as exactly the same amount', () => {
    for (const micro of [1, 99, 100_000, 1_000_000, 7_250_001, 123_456_789_123_456]) {
      expect(parseUsdt(toUsdtInput(micro))).toBe(micro);
    }
  });
});
