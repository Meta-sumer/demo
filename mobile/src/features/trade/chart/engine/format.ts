import type { ZoneInfo } from './types';

/**
 * Text the chart draws on the UI thread.
 *
 * Worklet twins of `utils/format` and `utils/time`, producing the same
 * strings: the axis labels, the crosshair tags and the drawings' price tags
 * are formatted every frame, where Intl is unavailable or far too slow.
 */

/** Minutes ahead of UTC at `ms`. */
export function offsetAt(zone: ZoneInfo, ms: number): number {
  'worklet';
  let i = 0;
  while (i < zone.at.length && ms >= zone.at[i]!) i += 1;
  return zone.off[i] ?? 0;
}

function pad2(value: number): string {
  'worklet';
  return value < 10 ? '0' + value : String(value);
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

/** The instant as wall-clock fields read through UTC getters. */
function local(ms: number, zone: ZoneInfo): Date {
  'worklet';
  return new Date(ms + offsetAt(zone, ms) * 60_000);
}

/** "12:30:52" - `formatClock`. */
export function clockText(ms: number, zone: ZoneInfo): string {
  'worklet';
  const d = local(ms, zone);
  return pad2(d.getUTCHours()) + ':' + pad2(d.getUTCMinutes()) + ':' + pad2(d.getUTCSeconds());
}

/** "12:30" - `formatHourMinute`. */
export function hourMinuteText(ms: number, zone: ZoneInfo): string {
  'worklet';
  const d = local(ms, zone);
  return pad2(d.getUTCHours()) + ':' + pad2(d.getUTCMinutes());
}

/** "5 Sep 12:30" - `formatDayTime`, where a tick mark opens a new day. */
export function dayTimeText(ms: number, zone: ZoneInfo): string {
  'worklet';
  const d = local(ms, zone);
  return d.getUTCDate() + ' ' + MONTHS[d.getUTCMonth()] + ' ' + pad2(d.getUTCHours()) + ':' + pad2(d.getUTCMinutes());
}

/** Which local day an instant falls on, as a number that changes at local midnight. */
export function localDay(ms: number, zone: ZoneInfo): number {
  'worklet';
  return Math.floor((ms + offsetAt(zone, ms) * 60_000) / 86_400_000);
}

/** `formatPrice`: fixed decimals with thousands separators, as `toLocaleString('en-US')` writes them. */
export function priceText(price: number, digits: number): string {
  'worklet';
  if (!Number.isFinite(price)) return '--';
  const fixed = Math.abs(price).toFixed(Math.max(0, Math.min(10, digits)));
  const dot = fixed.indexOf('.');
  const whole = dot === -1 ? fixed : fixed.slice(0, dot);
  const fraction = dot === -1 ? '' : fixed.slice(dot);
  let grouped = '';
  for (let i = 0; i < whole.length; i += 1) {
    const fromEnd = whole.length - i;
    grouped += whole[i];
    if (fromEnd > 1 && fromEnd % 3 === 1) grouped += ',';
  }
  const negative = price < 0 && Number(fixed) !== 0;
  return (negative ? '-' : '') + grouped + fraction;
}

/** `formatCountdown`: mm:ss, clamped so an overdue order shows 00:00. */
export function countdownText(msRemaining: number): string {
  'worklet';
  const total = Math.max(0, Math.ceil(msRemaining / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return pad2(minutes) + ':' + pad2(seconds);
}

/* -------------------------------------------------------------- JS thread */

/**
 * One formatter per zone, kept: constructing an Intl.DateTimeFormat is the
 * slow part, and a zone table for a year of daily candles asks for a few
 * hundred offsets.
 */
const formatters = new Map<string, Intl.DateTimeFormat>();

function zoneOffset(timeZone: string, ms: number): number {
  let formatter = formatters.get(timeZone);
  if (!formatter) {
    try {
      formatter = new Intl.DateTimeFormat('en-US', {
        timeZone,
        hour12: false,
        year: 'numeric',
        month: '2-digit',
        day: '2-digit',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      });
    } catch {
      return 0;
    }
    formatters.set(timeZone, formatter);
  }
  const parts = formatter.formatToParts(new Date(ms));
  const field = (type: string): number => Number(parts.find((part) => part.type === type)?.value ?? 0);
  // Some engines render midnight as hour 24; normalise before rebuilding.
  const asIfUtc = Date.UTC(field('year'), field('month') - 1, field('day'), field('hour') % 24, field('minute'), field('second'));
  const truncated = Math.floor(ms / 1000) * 1000;
  return Math.round((asIfUtc - truncated) / 60_000);
}

const SAMPLE_MS = 3 * 86_400_000;

/**
 * The zone's offsets over `[from, to]`, with every switch found to the second.
 *
 * Sampled every three days - no zone switches twice in that time - and each
 * change narrowed down by halving, so a year costs about a hundred and fifty
 * offsets rather than one per label per frame.
 */
export function buildZoneInfo(timeZone: string, from: number, to: number): ZoneInfo {
  if (timeZone === 'UTC' || !(to > from)) return { at: [], off: [zoneOffset(timeZone, from)] };
  const at: number[] = [];
  const off: number[] = [];
  let previousAt = from;
  let previous = zoneOffset(timeZone, from);
  off.push(previous);
  for (let sample = from + SAMPLE_MS; ; sample += SAMPLE_MS) {
    const time = Math.min(sample, to);
    const offset = zoneOffset(timeZone, time);
    if (offset !== previous) {
      let low = previousAt;
      let high = time;
      while (high - low > 1000) {
        const mid = Math.floor((low + high) / 2);
        if (zoneOffset(timeZone, mid) === previous) low = mid;
        else high = mid;
      }
      at.push(high);
      off.push(offset);
      previous = offset;
    }
    previousAt = time;
    if (time >= to) break;
  }
  return { at, off };
}
