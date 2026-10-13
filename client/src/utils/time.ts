/**
 * Time zone display.
 *
 * The server speaks epoch milliseconds and nothing else; a zone changes only
 * how a moment is *written*, never which moment it is. Trade terms, expiries
 * and settlement all continue to be decided in UTC on the server, so switching
 * this cannot move an expiry by a second.
 */

/** Zones offered in Settings. `SYSTEM` resolves to whatever the device says. */
export const SYSTEM_ZONE = 'SYSTEM';

export const TIME_ZONES = [
  SYSTEM_ZONE,
  'UTC',
  'Asia/Kolkata',
  'Asia/Dubai',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Europe/London',
  'Europe/Berlin',
  'America/New_York',
  'America/Chicago',
  'America/Los_Angeles',
  'Australia/Sydney',
] as const;

/** Turns the stored preference into an IANA zone Intl will accept. */
export function resolveZone(preference: string): string {
  if (preference !== SYSTEM_ZONE) return preference;
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/**
 * Minutes that `timeZone` is ahead of UTC at a given instant.
 *
 * Derived by formatting the instant in the zone and reading the wall-clock
 * fields back, because there is no API that hands over the offset directly.
 * Computed per instant rather than cached, so a series that spans a daylight
 * saving change is labelled correctly on both sides of it.
 */
export function zoneOffsetMinutes(timeZone: string, at: Date = new Date()): number {
  const parts = new Intl.DateTimeFormat('en-US', {
    timeZone,
    hour12: false,
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).formatToParts(at);

  const field = (type: string): number => Number(parts.find((part) => part.type === type)?.value ?? 0);

  // Some engines render midnight as hour 24; normalise before rebuilding.
  const asIfUtc = Date.UTC(
    field('year'),
    field('month') - 1,
    field('day'),
    field('hour') % 24,
    field('minute'),
    field('second'),
  );

  // The source instant is truncated to the second, matching the parsed fields.
  const truncated = Math.floor(at.getTime() / 1000) * 1000;
  return Math.round((asIfUtc - truncated) / 60_000);
}

/** "UTC+05:30", "UTC-04:00", "UTC+00:00". */
export function zoneOffsetLabel(timeZone: string, at: Date = new Date()): string {
  const minutes = zoneOffsetMinutes(timeZone, at);
  const sign = minutes < 0 ? '-' : '+';
  const absolute = Math.abs(minutes);
  const hours = String(Math.floor(absolute / 60)).padStart(2, '0');
  const rest = String(absolute % 60).padStart(2, '0');
  return 'UTC' + sign + hours + ':' + rest;
}

/** The label shown in the Settings select. */
export function zoneLabel(preference: string, at: Date = new Date()): string {
  const zone = resolveZone(preference);
  const offset = '(' + zoneOffsetLabel(zone, at) + ')';
  if (preference === SYSTEM_ZONE) return 'System ' + offset + ' - ' + zone;
  if (preference === 'UTC') return offset + ' UTC';
  return offset + ' ' + preference.split('/').pop()!.replace(/_/g, ' ');
}

/** "12:30:52" in the given zone. */
export function formatClock(ms: number, timeZone: string): string {
  return new Intl.DateTimeFormat('en-GB', {
    timeZone,
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(ms));
}

/** "12:30" - the chart's axis and crosshair label. */
export function formatHourMinute(ms: number, timeZone: string): string {
  return new Intl.DateTimeFormat('en-GB', {
    timeZone,
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(ms));
}

/** "5 Sep 12:30" - used where a tick mark opens a new day. */
export function formatDayTime(ms: number, timeZone: string): string {
  return new Intl.DateTimeFormat('en-GB', {
    timeZone,
    hour12: false,
    day: 'numeric',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(ms));
}

/** Short zone tag for the chart chip, e.g. "UTC" or "UTC+5:30". */
export function zoneChip(timeZone: string, at: Date = new Date()): string {
  const minutes = zoneOffsetMinutes(timeZone, at);
  if (minutes === 0) return 'UTC';
  const sign = minutes < 0 ? '-' : '+';
  const absolute = Math.abs(minutes);
  const hours = Math.floor(absolute / 60);
  const rest = absolute % 60;
  return 'UTC' + sign + hours + (rest ? ':' + String(rest).padStart(2, '0') : '');
}
