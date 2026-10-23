import { describe, expect, it } from 'vitest';
import { formatAge, freshness, queryMaxAge } from '../src/utils/freshness';

/**
 * Stale and missing figures are flagged, never shown as plain numbers
 * (metric dictionary, F-18).
 */
describe('freshness', () => {
  const now = Date.parse('2026-10-21T12:00:00Z');

  it('is fresh within the allowed age', () => {
    expect(freshness(now - 4000, 5000, now)).toEqual({ state: 'fresh', asOf: now - 4000 });
  });

  it('is stale beyond it, with the age', () => {
    expect(freshness('2026-10-21T11:59:00Z', 30_000, now)).toEqual({
      state: 'stale',
      asOf: now - 60_000,
      ageMs: 60_000,
    });
  });

  it('is unavailable without data (never 0)', () => {
    expect(freshness(null, 5000, now).state).toBe('unavailable');
    expect(freshness(undefined, 5000, now).state).toBe('unavailable');
    expect(freshness(0, 5000, now).state).toBe('unavailable');
    expect(freshness('not a date', 5000, now).state).toBe('unavailable');
  });

  it('allows a query two missed refreshes', () => {
    expect(queryMaxAge(5000)).toBe(11_000);
  });

  it('formats ages', () => {
    expect(formatAge(45_000)).toBe('45s');
    expect(formatAge(12 * 60_000)).toBe('12m');
    expect(formatAge(3 * 3_600_000)).toBe('3h');
    expect(formatAge(2 * 86_400_000)).toBe('2d');
  });
});
