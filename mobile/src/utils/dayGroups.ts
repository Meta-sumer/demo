/**
 * Day groups for a list of trades: "Today", "Yesterday", then dates (brief
 * reference record 17 - Pocket Option groups closed trades by date).
 *
 * Days are counted in the zone the trader chose for clocks in Settings, not
 * the device's: every time on these rows is written in that zone, so a trade
 * that reads 00:30 must sit under the day it says, whatever the laptop is set
 * to. All arithmetic is on calendar keys ("2026-09-30"), which is what keeps a
 * daylight-saving change from producing a 23- or 25-hour "day".
 */

const keyFormatters = new Map<string, Intl.DateTimeFormat>();

function keyFormatter(timeZone: string): Intl.DateTimeFormat {
  let formatter = keyFormatters.get(timeZone);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat('en-GB', { timeZone, year: 'numeric', month: '2-digit', day: '2-digit' });
    keyFormatters.set(timeZone, formatter);
  }
  return formatter;
}

/** "2026-09-30": the calendar day `ms` falls on in `timeZone`. */
export function dayKey(ms: number, timeZone: string): string {
  const parts = keyFormatter(timeZone).formatToParts(new Date(ms));
  const field = (type: Intl.DateTimeFormatPartTypes) => parts.find((part) => part.type === type)?.value ?? '';
  return field('year') + '-' + field('month') + '-' + field('day');
}

function keyToUtc(key: string): Date {
  const [year, month, day] = key.split('-').map(Number) as [number, number, number];
  return new Date(Date.UTC(year, month - 1, day));
}

/** The key `days` calendar days from `key` (negative for earlier). */
export function shiftDay(key: string, days: number): string {
  const date = keyToUtc(key);
  date.setUTCDate(date.getUTCDate() + days);
  return date.toISOString().slice(0, 10);
}

/** Whole calendar days from `from` to `to`. */
export function daysBetween(from: string, to: string): number {
  return Math.round((keyToUtc(to).getTime() - keyToUtc(from).getTime()) / 86_400_000);
}

/** "Mon" for a calendar key. */
export function weekdayOf(key: string): string {
  return new Intl.DateTimeFormat('en-GB', { timeZone: 'UTC', weekday: 'short' }).format(keyToUtc(key));
}

/** "Today", "Yesterday", "Mon 28 Sep" - with the year once it is not this one. */
export function dayLabel(key: string, todayKey: string): string {
  if (key === todayKey) return 'Today';
  if (key === shiftDay(todayKey, -1)) return 'Yesterday';
  const sameYear = key.slice(0, 4) === todayKey.slice(0, 4);
  return new Intl.DateTimeFormat('en-GB', {
    timeZone: 'UTC',
    weekday: 'short',
    day: 'numeric',
    month: 'short',
    ...(sameYear ? {} : { year: 'numeric' }),
  }).format(keyToUtc(key));
}

export interface DayGroup<T> {
  key: string;
  label: string;
  items: T[];
}

/**
 * Splits a list into days, keeping its order.
 *
 * Groups come out in the order their first item appears, so a list sorted
 * newest first gives Today before Yesterday. An item that turns up out of
 * order joins its own day rather than opening a second group with the same
 * heading.
 */
export function groupByDay<T>(
  items: readonly T[],
  timeOf: (item: T) => number,
  now: number,
  timeZone: string,
): DayGroup<T>[] {
  const today = dayKey(now, timeZone);
  const groups = new Map<string, DayGroup<T>>();
  for (const item of items) {
    const key = dayKey(timeOf(item), timeZone);
    let group = groups.get(key);
    if (!group) {
      group = { key, label: dayLabel(key, today), items: [] };
      groups.set(key, group);
    }
    group.items.push(item);
  }
  return [...groups.values()];
}
