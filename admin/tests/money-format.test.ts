import { describe, expect, it } from 'vitest';
import { formatMs, formatRupees, fromMinor, rupeesInWords, toMinor } from '../src/utils/format';

/**
 * The settings screen shows risk limits in rupees with Indian grouping, the
 * same figure in lakh/crore, and millisecond settings in a readable unit. The
 * API still receives paise: these are display helpers only.
 */
describe('formatRupees', () => {
  it('uses the rupee sign and Indian digit grouping', () => {
    expect(formatRupees(5_000_000)).toBe('₹50,00,000');
    expect(formatRupees(20_000_000)).toBe('₹2,00,00,000');
    expect(formatRupees(100_000)).toBe('₹1,00,000');
    expect(formatRupees(10)).toBe('₹10');
    expect(formatRupees(0)).toBe('₹0');
  });

  it('keeps paise and the sign', () => {
    expect(formatRupees(1234.5)).toBe('₹1,234.5');
    expect(formatRupees(-2500)).toBe('-₹2,500');
  });

  it('reads the API figure (paise) back as the rupees the field shows', () => {
    // Default maxAssetExposure: 5_000_000_00 paise.
    expect(formatRupees(fromMinor(500_000_000))).toBe('₹50,00,000');
    expect(toMinor(5_000_000)).toBe(500_000_000);
  });
});

describe('rupeesInWords', () => {
  it('names thousands, lakhs and crores', () => {
    expect(rupeesInWords(5_000_000)).toBe('50 lakh');
    expect(rupeesInWords(20_000_000)).toBe('2 crore');
    expect(rupeesInWords(250_000)).toBe('2.5 lakh');
    expect(rupeesInWords(100_000)).toBe('1 lakh');
    expect(rupeesInWords(7_500)).toBe('7.5 thousand');
    expect(rupeesInWords(12_345_678_900)).toBe('1,234.57 crore');
  });

  it('says nothing below a thousand', () => {
    expect(rupeesInWords(999)).toBe('');
    expect(rupeesInWords(0)).toBe('');
  });
});

describe('formatMs', () => {
  it('reads milliseconds as s, min, h or days', () => {
    expect(formatMs(1000)).toBe('1 s');
    expect(formatMs(1500)).toBe('1.5 s');
    expect(formatMs(60_000)).toBe('1 min');
    expect(formatMs(900_000)).toBe('15 min');
    expect(formatMs(3_600_000)).toBe('1 h');
    expect(formatMs(86_400_000)).toBe('24 h');
    expect(formatMs(604_800_000)).toBe('7 days');
  });

  it('leaves sub-second and invalid values alone', () => {
    expect(formatMs(300)).toBe('');
    expect(formatMs(0)).toBe('');
    expect(formatMs(Number.NaN)).toBe('');
  });
});
