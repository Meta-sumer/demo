import { describe, expect, it } from 'vitest';
import {
  creditEligibility,
  dayRangeToIso,
  depositStatusMeta,
  estimateFee,
  explorerAddressUrl,
  explorerTxUrl,
  formatUsdt,
  networkLabel,
  parseUsdt,
  processingStatusMeta,
  safeExternalUrl,
  shortenMiddle,
  toUsdtInput,
  withdrawalStatusMeta,
} from '../src/utils/usdt';
import { toPaginated } from '../src/utils/paging';

/**
 * The amount an operator approves a payout against must be the amount the
 * server holds, to the last micro-USDT. These tests pin the formatter to exact
 * digit strings, including values where float arithmetic would drift.
 */
describe('formatUsdt', () => {
  it('formats micro-USDT with grouping, six decimals and the unit', () => {
    expect(formatUsdt(1_250_500_000)).toBe('1,250.500000 USDT');
    expect(formatUsdt(1_000_000)).toBe('1.000000 USDT');
    expect(formatUsdt(1)).toBe('0.000001 USDT');
    expect(formatUsdt(0)).toBe('0.000000 USDT');
    expect(formatUsdt(999_999)).toBe('0.999999 USDT');
  });

  it('handles negatives and an explicit sign', () => {
    expect(formatUsdt(-1_250_500_000)).toBe('-1,250.500000 USDT');
    expect(formatUsdt(-5)).toBe('-0.000005 USDT');
    expect(formatUsdt(5_000_000, { sign: true })).toBe('+5.000000 USDT');
    expect(formatUsdt(0, { sign: true })).toBe('0.000000 USDT');
  });

  it('omits the unit on request', () => {
    expect(formatUsdt(12_345_678_901, { unit: false })).toBe('12,345.678901');
  });

  it('is exact where float division would drift', () => {
    // 0.1 + 0.2 territory, and the largest safe integer.
    expect(formatUsdt(300_000)).toBe('0.300000 USDT');
    expect(formatUsdt(Number.MAX_SAFE_INTEGER)).toBe('9,007,199,254.740991 USDT');
    expect(formatUsdt(123_456_789_012_345)).toBe('123,456,789.012345 USDT');
  });

  it('accepts bigint and integer strings', () => {
    expect(formatUsdt(10_000_000_000_000_000_001n)).toBe('10,000,000,000,000.000001 USDT');
    expect(formatUsdt('2500000')).toBe('2.500000 USDT');
    expect(formatUsdt('-0')).toBe('0.000000 USDT');
    expect(formatUsdt('000123')).toBe('0.000123 USDT');
  });

  it('refuses to guess for anything that is not an exact integer', () => {
    expect(formatUsdt(1.5)).toBe('--');
    expect(formatUsdt(Number.NaN)).toBe('--');
    expect(formatUsdt(Number.POSITIVE_INFINITY)).toBe('--');
    expect(formatUsdt(2 ** 60)).toBe('--');
    expect(formatUsdt('1.5')).toBe('--');
    expect(formatUsdt(null)).toBe('--');
    expect(formatUsdt(undefined)).toBe('--');
  });
});

describe('parseUsdt / toUsdtInput', () => {
  it('parses decimals into exact micro-USDT', () => {
    expect(parseUsdt('1,250.5')).toBe(1_250_500_000);
    expect(parseUsdt('10')).toBe(10_000_000);
    expect(parseUsdt('0.000001')).toBe(1);
    expect(parseUsdt('.5')).toBe(500_000);
    expect(parseUsdt('5.')).toBe(5_000_000);
    expect(parseUsdt(' 0.3 ')).toBe(300_000);
  });

  it('rejects malformed, negative, over-precise and oversized input', () => {
    expect(parseUsdt('')).toBeNull();
    expect(parseUsdt('.')).toBeNull();
    expect(parseUsdt('-1')).toBeNull();
    expect(parseUsdt('1.0000001')).toBeNull();
    expect(parseUsdt('1e6')).toBeNull();
    expect(parseUsdt('abc')).toBeNull();
    expect(parseUsdt('99999999999')).toBeNull();
  });

  it('round-trips through the edit field', () => {
    expect(toUsdtInput(10_500_000)).toBe('10.5');
    expect(toUsdtInput(10_000_000)).toBe('10');
    expect(toUsdtInput(1)).toBe('0.000001');
    expect(toUsdtInput(0)).toBe('0');
    expect(toUsdtInput(null)).toBe('');
    for (const micro of [1, 999_999, 1_000_000, 1_250_500_000, 123_456_789]) {
      expect(parseUsdt(toUsdtInput(micro))).toBe(micro);
    }
  });
});

describe('estimateFee', () => {
  it('adds the fixed fee to the percentage, rounded half up to the micro', () => {
    expect(estimateFee(100_000_000, 1, 1_000_000)).toBe(2_000_000);
    expect(estimateFee(100_000_000, 1.5, 0)).toBe(1_500_000);
    expect(estimateFee(3, 50, 0)).toBe(2); // 1.5 rounds up
    expect(estimateFee(1, 0.1, 0)).toBe(0);
  });

  it('falls back to the fixed fee for a missing or invalid percentage', () => {
    expect(estimateFee(100_000_000, 0, 1_000_000)).toBe(1_000_000);
    expect(estimateFee(100_000_000, Number.NaN, 1_000_000)).toBe(1_000_000);
    expect(estimateFee(0, 1, 1_000_000)).toBe(1_000_000);
  });
});

describe('networks and explorers', () => {
  it('reduces codes and labels to the short label', () => {
    expect(networkLabel('USDTTRC20')).toBe('TRC20');
    expect(networkLabel('TRC20')).toBe('TRC20');
    expect(networkLabel('USDTBSC')).toBe('BSC');
    expect(networkLabel('bep20')).toBe('BSC');
    expect(networkLabel('ERC20')).toBe('ERC20');
    expect(networkLabel(null)).toBe('--');
  });

  it('links TRC20 to tronscan and BSC to bscscan', () => {
    expect(explorerTxUrl('USDTTRC20', 'abc123')).toBe('https://tronscan.org/#/transaction/abc123');
    expect(explorerTxUrl('BSC', '0xdead')).toBe('https://bscscan.com/tx/0xdead');
    expect(explorerTxUrl('ERC20', '0xdead')).toBeNull();
    expect(explorerTxUrl('BSC', null)).toBeNull();
    expect(explorerAddressUrl('TRC20', 'TXyz')).toBe('https://tronscan.org/#/address/TXyz');
    expect(explorerAddressUrl('USDTBSC', '0xabc')).toBe('https://bscscan.com/address/0xabc');
  });

  it('cannot be steered off the explorer by a hostile hash', () => {
    expect(explorerTxUrl('BSC', '../../evil?x=1')).toBe('https://bscscan.com/tx/..%2F..%2Fevil%3Fx%3D1');
  });

  it('only lets http(s) payment links out', () => {
    expect(safeExternalUrl('https://pay.example/i/1')).toBe('https://pay.example/i/1');
    expect(safeExternalUrl('javascript:alert(1)')).toBeNull();
    expect(safeExternalUrl('data:text/html,hi')).toBeNull();
    expect(safeExternalUrl(null)).toBeNull();
  });

  it('shortens long identifiers in the middle', () => {
    expect(shortenMiddle('TXyz1234567890abcdef')).toBe('TXyz12…cdef');
    expect(shortenMiddle('short')).toBe('short');
    expect(shortenMiddle(null)).toBe('--');
  });
});

describe('status words', () => {
  it('names every status in words, not colour', () => {
    expect(withdrawalStatusMeta('pending_review').label).toBe('Under review');
    expect(withdrawalStatusMeta('awaiting_approval').label).toBe('BeeX review');
    expect(depositStatusMeta('awaiting_payment').label).toBe('Awaiting payment');
    expect(depositStatusMeta('partially_paid').tone).toBe('warn');
    expect(processingStatusMeta('failed').tone).toBe('down');
  });

  it('humanises a status it does not know rather than hiding it', () => {
    expect(depositStatusMeta('refund_pending')).toMatchObject({ label: 'Refund pending', tone: 'neutral' });
    expect(withdrawalStatusMeta(null).label).toBe('--');
  });
});

describe('creditEligibility', () => {
  const now = Date.parse('2026-10-05T12:00:00Z');
  const base = { credited: false, amountReceived: 5_000_000, expiresAt: '2026-10-05T11:00:00Z' };

  it('allows an underpaid, uncredited deposit that received something', () => {
    expect(creditEligibility({ ...base, status: 'underpaid' }, now).eligible).toBe(true);
  });

  it('allows a partially paid deposit only once its invoice has expired', () => {
    expect(creditEligibility({ ...base, status: 'partially_paid' }, now).eligible).toBe(true);
    expect(creditEligibility({ ...base, status: 'partially_paid', expiresAt: '2026-10-05T13:00:00Z' }, now).eligible).toBe(false);
    expect(creditEligibility({ ...base, status: 'partially_paid', expiresAt: null }, now).eligible).toBe(false);
  });

  it('refuses credited, empty and other statuses', () => {
    expect(creditEligibility({ ...base, status: 'underpaid', credited: true }, now).eligible).toBe(false);
    expect(creditEligibility({ ...base, status: 'underpaid', amountReceived: 0 }, now).eligible).toBe(false);
    expect(creditEligibility({ ...base, status: 'paid' }, now).eligible).toBe(false);
    expect(creditEligibility({ ...base, status: 'expired' }, now).eligible).toBe(false);
  });
});

describe('dayRangeToIso', () => {
  it('turns local calendar days into an inclusive ISO range', () => {
    const range = dayRangeToIso('2026-10-01', '2026-10-05');
    expect(new Date(range.from!).getHours()).toBe(0);
    expect(new Date(range.from!).getDate()).toBe(1);
    expect(new Date(range.to!).getHours()).toBe(23);
    expect(new Date(range.to!).getMilliseconds()).toBe(999);
    expect(dayRangeToIso('', 'nope')).toEqual({ from: undefined, to: undefined });
  });
});

describe('toPaginated', () => {
  it('reads rows from data and page figures from meta', () => {
    const page = toPaginated<{ id: string }>([{ id: 'a' }], { page: 2, limit: 20, total: 41, totalPages: 3 });
    expect(page).toEqual({ items: [{ id: 'a' }], page: 2, limit: 20, total: 41, totalPages: 3 });
  });

  it('reads the classic {items,...} shape inside data', () => {
    const page = toPaginated({ items: [1, 2], page: 1, limit: 2, total: 5, totalPages: 3 });
    expect(page).toEqual({ items: [1, 2], page: 1, limit: 2, total: 5, totalPages: 3 });
  });

  it('derives what is missing', () => {
    expect(toPaginated([1, 2, 3], { total: 45 }, { page: 1, limit: 20 })).toEqual({
      items: [1, 2, 3],
      page: 1,
      limit: 20,
      total: 45,
      totalPages: 3,
    });
    expect(toPaginated([1], { pagination: { page: 3, limit: 10, total: 21 } }).totalPages).toBe(3);
    expect(toPaginated(null).items).toEqual([]);
  });
});
