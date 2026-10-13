import { describe, expect, it } from 'vitest';
import { durationChips, durationLabel, durationRow } from '../src/features/trade/ticketModel';

/**
 * The ticket's duration row: four chips, the rest behind "More", and the
 * selected duration always on the row (reference record 10).
 */
const payout = (expirySeconds: number, payoutPercent: number) => ({
  id: String(expirySeconds),
  assetId: 'x',
  expirySeconds,
  payoutPercent,
  minStake: null,
  maxStake: null,
  version: 1,
});

const asset = {
  supportedExpiries: [1800, 30, 300, 900, 60],
  payouts: [payout(30, 82), payout(60, 84), payout(300, 87), payout(900, 88), payout(1800, 89)],
};
const chips = durationChips(asset);
const seconds = (list: Array<{ seconds: number }>) => list.map((chip) => chip.seconds);

describe('durationLabel', () => {
  it('writes durations the way the chips read', () => {
    expect([30, 60, 300, 900, 1800].map(durationLabel)).toEqual(['30s', '1m', '5m', '15m', '30m']);
  });

  it('writes hours without a decimal point', () => {
    expect(durationLabel(3600)).toBe('1h');
    expect(durationLabel(5400)).toBe('1h 30m');
    expect(durationLabel(4 * 3600)).toBe('4h');
  });
});

describe('durationRow', () => {
  it('shows every duration, and no More, when there are four or fewer', () => {
    const few = chips.slice(0, 3);
    expect(durationRow(few, 60)).toEqual({ chips: few, hidden: 0 });
    expect(durationRow(chips.slice(0, 4), 60).hidden).toBe(0);
  });

  it('shows the four shortest and counts the rest behind More', () => {
    const row = durationRow(chips, 60);
    expect(seconds(row.chips)).toEqual([30, 60, 300, 900]);
    expect(row.hidden).toBe(1);
  });

  it('keeps a longer selected duration on the row, in the last place', () => {
    const row = durationRow(chips, 1800);
    expect(seconds(row.chips)).toEqual([30, 60, 300, 1800]);
    expect(row.hidden).toBe(1);
  });

  it('falls back to the four shortest when the selection is not offered at all', () => {
    expect(seconds(durationRow(chips, 7200).chips)).toEqual([30, 60, 300, 900]);
  });

  it('honours a different row size', () => {
    const row = durationRow(chips, 900, 3);
    expect(seconds(row.chips)).toEqual([30, 60, 900]);
    expect(row.hidden).toBe(2);
  });
});
