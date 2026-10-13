import { describe, expect, it } from 'vitest';
import {
  formatClock,
  formatHourMinute,
  resolveZone,
  SYSTEM_ZONE,
  zoneChip,
  zoneLabel,
  zoneOffsetLabel,
  zoneOffsetMinutes,
} from '../src/utils/time';

/**
 * A zone changes how a moment is written, never which moment it is. These fix
 * the offsets against known instants, including both sides of a daylight saving
 * change - a chart axis that silently shifts by an hour is worse than one
 * labelled UTC.
 */
const WINTER = new Date('2026-01-15T12:00:00Z');
const SUMMER = new Date('2026-07-15T12:00:00Z');

describe('zoneOffsetMinutes', () => {
  it('is zero for UTC', () => {
    expect(zoneOffsetMinutes('UTC', WINTER)).toBe(0);
    expect(zoneOffsetMinutes('UTC', SUMMER)).toBe(0);
  });

  it('handles a half-hour zone', () => {
    expect(zoneOffsetMinutes('Asia/Kolkata', WINTER)).toBe(330);
    // India does not observe daylight saving, so summer must match.
    expect(zoneOffsetMinutes('Asia/Kolkata', SUMMER)).toBe(330);
  });

  it('is negative west of Greenwich', () => {
    expect(zoneOffsetMinutes('America/New_York', WINTER)).toBe(-300);
  });

  it('follows daylight saving on both sides of the change', () => {
    expect(zoneOffsetMinutes('Europe/London', WINTER)).toBe(0);
    expect(zoneOffsetMinutes('Europe/London', SUMMER)).toBe(60);
    expect(zoneOffsetMinutes('America/New_York', SUMMER)).toBe(-240);
  });

  it('handles a zone that crosses the date line relative to UTC', () => {
    expect(zoneOffsetMinutes('Australia/Sydney', WINTER)).toBe(660);
    expect(zoneOffsetMinutes('Australia/Sydney', SUMMER)).toBe(600);
  });

  it('is correct across midnight, where hour 24 can appear', () => {
    // 18:30 UTC is 00:00 the next day in Kolkata.
    const midnightInKolkata = new Date('2026-03-01T18:30:00Z');
    expect(zoneOffsetMinutes('Asia/Kolkata', midnightInKolkata)).toBe(330);
  });
});

describe('zoneOffsetLabel', () => {
  it('pads to the conventional shape', () => {
    expect(zoneOffsetLabel('UTC', WINTER)).toBe('UTC+00:00');
    expect(zoneOffsetLabel('Asia/Kolkata', WINTER)).toBe('UTC+05:30');
    expect(zoneOffsetLabel('America/New_York', WINTER)).toBe('UTC-05:00');
    expect(zoneOffsetLabel('Australia/Sydney', WINTER)).toBe('UTC+11:00');
  });
});

describe('zoneChip', () => {
  it('drops the padding and a zero minute part', () => {
    expect(zoneChip('UTC', WINTER)).toBe('UTC');
    expect(zoneChip('Asia/Kolkata', WINTER)).toBe('UTC+5:30');
    expect(zoneChip('America/New_York', WINTER)).toBe('UTC-5');
  });
});

describe('resolveZone', () => {
  it('passes a real zone straight through', () => {
    expect(resolveZone('Asia/Tokyo')).toBe('Asia/Tokyo');
  });

  it('resolves the system marker to something Intl accepts', () => {
    const resolved = resolveZone(SYSTEM_ZONE);

    expect(resolved).not.toBe(SYSTEM_ZONE);
    expect(() => new Intl.DateTimeFormat('en-GB', { timeZone: resolved })).not.toThrow();
  });
});

describe('formatting', () => {
  it('writes the same instant differently per zone', () => {
    const at = Date.UTC(2026, 8, 5, 12, 30, 52);

    expect(formatClock(at, 'UTC')).toBe('12:30:52');
    expect(formatClock(at, 'Asia/Kolkata')).toBe('18:00:52');
    expect(formatHourMinute(at, 'UTC')).toBe('12:30');
    expect(formatHourMinute(at, 'Asia/Kolkata')).toBe('18:00');
  });

  it('uses a 24-hour clock, never am/pm', () => {
    const evening = Date.UTC(2026, 8, 5, 21, 5, 0);

    expect(formatClock(evening, 'UTC')).toBe('21:05:00');
    expect(formatClock(evening, 'UTC')).not.toMatch(/[ap]m/i);
  });

  it('renders midnight as 00, not 24', () => {
    expect(formatClock(Date.UTC(2026, 8, 5, 0, 0, 0), 'UTC')).toBe('00:00:00');
  });
});

describe('zoneLabel', () => {
  it('names the offset for a plain zone', () => {
    expect(zoneLabel('UTC', WINTER)).toBe('(UTC+00:00) UTC');
    expect(zoneLabel('Asia/Kolkata', WINTER)).toBe('(UTC+05:30) Kolkata');
    expect(zoneLabel('America/New_York', WINTER)).toBe('(UTC-05:00) New York');
  });

  it('says which zone the system resolved to', () => {
    expect(zoneLabel(SYSTEM_ZONE, WINTER)).toMatch(/^System \(UTC[+-]\d{2}:\d{2}\) - /);
  });
});
