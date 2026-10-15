import { describe, expect, it } from 'vitest';
import { durationChips, durationWords, expiryGroups } from '../src/features/trade/ticketModel';

/**
 * The Time sheet: every duration the asset offers, in sections by length, each
 * with its duration written out under the clock time it would end at.
 */
const payout = (expirySeconds: number) => ({
  id: String(expirySeconds),
  assetId: 'x',
  expirySeconds,
  payoutPercent: 85,
  minStake: null,
  maxStake: null,
  version: 1,
});

const chipsFor = (supportedExpiries: number[]) =>
  durationChips({ supportedExpiries, payouts: supportedExpiries.map(payout) });

const seconds = (list: Array<{ seconds: number }>) => list.map((chip) => chip.seconds);

describe('durationWords', () => {
  it('writes seconds, minutes and hours in words', () => {
    expect([30, 60, 300, 900, 1800].map(durationWords)).toEqual(['30 sec', '1 min', '5 min', '15 min', '30 min']);
    expect(durationWords(90)).toBe('1 min 30 sec');
    expect(durationWords(3600)).toBe('1 hour');
    expect(durationWords(5400)).toBe('1 hour 30 min');
    expect(durationWords(7200)).toBe('2 hours');
  });
});

describe('expiryGroups', () => {
  it('splits the usual set into up to five minutes and 15 to 60 minutes', () => {
    const groups = expiryGroups(chipsFor([1800, 30, 300, 900, 60]));
    expect(groups.map((group) => group.label)).toEqual(['Up to 5 min.', '15–60 min.']);
    expect(groups.map((group) => seconds(group.chips))).toEqual([
      [30, 60, 300],
      [900, 1800],
    ]);
  });

  it('adds a section for durations over an hour, and drops empty ones', () => {
    const groups = expiryGroups(chipsFor([3600, 7200]));
    expect(groups.map((group) => group.label)).toEqual(['15–60 min.', 'Over 1 hour']);
    expect(groups.map((group) => seconds(group.chips))).toEqual([[3600], [7200]]);
  });

  it('never loses a duration between 5 and 15 minutes', () => {
    const all = [60, 300, 600, 900];
    const groups = expiryGroups(chipsFor(all));
    expect(groups.map((group) => group.label)).toEqual(['Up to 5 min.', '5–60 min.']);
    expect(groups.flatMap((group) => seconds(group.chips))).toEqual(all);
  });

  it('is empty when the asset offers nothing', () => {
    expect(expiryGroups([])).toEqual([]);
  });
});
