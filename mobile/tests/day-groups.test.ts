import { describe, expect, it } from 'vitest';
import { dayKey, dayLabel, daysBetween, groupByDay, shiftDay, weekdayOf } from '../src/utils/dayGroups';

/**
 * History grouped by day. The day has to be the one the row's own times read
 * as - the trader's chosen zone, not the device's - or a trade at 00:30 sits
 * under "Yesterday" for no reason the trader can see.
 */
const at = (iso: string) => Date.parse(iso);

describe('dayKey', () => {
  it('reads the calendar day in the given zone', () => {
    const late = at('2026-09-29T20:00:00Z'); // 01:30 on the 30th in Kolkata
    expect(dayKey(late, 'UTC')).toBe('2026-09-29');
    expect(dayKey(late, 'Asia/Kolkata')).toBe('2026-09-30');
    expect(dayKey(late, 'America/New_York')).toBe('2026-09-29');
  });
});

describe('calendar arithmetic', () => {
  it('shifts across month and year ends', () => {
    expect(shiftDay('2026-10-01', -1)).toBe('2026-09-30');
    expect(shiftDay('2026-12-31', 1)).toBe('2027-01-01');
    expect(shiftDay('2024-03-01', -1)).toBe('2024-02-29');
  });

  it('counts whole days, whatever daylight saving does', () => {
    expect(daysBetween('2026-03-28', '2026-03-30')).toBe(2);
    expect(daysBetween('2026-09-30', '2026-09-30')).toBe(0);
    expect(daysBetween('2026-10-02', '2026-09-30')).toBe(-2);
  });

  it('names the weekday of a key', () => {
    expect(weekdayOf('2026-09-30')).toBe('Wed');
    expect(weekdayOf('2026-10-05')).toBe('Mon');
  });
});

describe('dayLabel', () => {
  it('says Today and Yesterday', () => {
    expect(dayLabel('2026-09-30', '2026-09-30')).toBe('Today');
    expect(dayLabel('2026-09-29', '2026-09-30')).toBe('Yesterday');
    expect(dayLabel('2026-09-30', '2026-10-01')).toBe('Yesterday');
  });

  it('writes older days as a date, with the year only when it is not this one', () => {
    const label = dayLabel('2026-09-28', '2026-09-30');
    expect(label).toMatch(/^Mon 28 Sep/);
    expect(label).not.toMatch(/2026/);
    expect(dayLabel('2025-12-31', '2026-09-30')).toMatch(/31 Dec.* 2025$/);
  });
});

describe('groupByDay', () => {
  const now = at('2026-09-30T12:00:00Z');
  const trades = [
    { id: 'a', t: at('2026-09-30T09:00:00Z') },
    { id: 'b', t: at('2026-09-30T00:10:00Z') },
    { id: 'c', t: at('2026-09-29T23:50:00Z') },
    { id: 'd', t: at('2026-09-27T10:00:00Z') },
  ];

  it('splits a newest-first list into days, in order, keeping each day in order', () => {
    const groups = groupByDay(trades, (trade) => trade.t, now, 'UTC');
    expect(groups.map((group) => group.label.slice(0, 9))).toEqual(['Today', 'Yesterday', 'Sun 27 Se']);
    expect(groups.map((group) => group.items.map((trade) => trade.id))).toEqual([['a', 'b'], ['c'], ['d']]);
  });

  it('draws the line at midnight in the chosen zone', () => {
    // In Kolkata (UTC+5:30) 23:50 UTC on the 29th is already the 30th.
    const groups = groupByDay(trades, (trade) => trade.t, now, 'Asia/Kolkata');
    expect(groups[0]!.label).toBe('Today');
    expect(groups[0]!.items.map((trade) => trade.id)).toEqual(['a', 'b', 'c']);
  });

  it('joins an out-of-order item to its day rather than repeating a heading', () => {
    const shuffled = [trades[0]!, trades[2]!, trades[1]!];
    const groups = groupByDay(shuffled, (trade) => trade.t, now, 'UTC');
    expect(groups.map((group) => group.label)).toEqual(['Today', 'Yesterday']);
    expect(groups[0]!.items.map((trade) => trade.id)).toEqual(['a', 'b']);
  });

  it('returns nothing for nothing', () => {
    expect(groupByDay([], () => 0, now, 'UTC')).toEqual([]);
  });
});
