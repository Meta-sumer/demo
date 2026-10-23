/**
 * Freshness of a figure (docs/audit/operations/metric-dictionary.md).
 *
 * Every figure on the risk screens has a refresh interval. A value whose data
 * is older than its allowed age is shown with a "stale since" marker, and a
 * value that could not be loaded is shown as "unavailable" - never as 0.
 */

export type Freshness =
  | { state: 'fresh'; asOf: number }
  | { state: 'stale'; asOf: number; ageMs: number }
  | { state: 'unavailable' };

/** asOf: epoch ms, ISO string, or null/undefined when there is no data. */
export function freshness(asOf: number | string | null | undefined, maxAgeMs: number, nowMs: number): Freshness {
  if (asOf === null || asOf === undefined || asOf === '' || asOf === 0) return { state: 'unavailable' };
  const at = typeof asOf === 'number' ? asOf : Date.parse(asOf);
  if (!Number.isFinite(at)) return { state: 'unavailable' };
  const ageMs = nowMs - at;
  if (ageMs > maxAgeMs) return { state: 'stale', asOf: at, ageMs };
  return { state: 'fresh', asOf: at };
}

/** A query's data is stale once it missed two refreshes. */
export function queryMaxAge(refetchIntervalMs: number): number {
  return refetchIntervalMs * 2 + 1000;
}

/** "12:04:31" for today, "21 Oct 12:04" otherwise. */
export function formatSince(at: number, nowMs: number): string {
  const d = new Date(at);
  const sameDay = new Date(nowMs).toDateString() === d.toDateString();
  if (sameDay) return d.toLocaleTimeString('en-GB', { hour12: false });
  return d.toLocaleString('en-GB', { day: '2-digit', month: 'short', hour: '2-digit', minute: '2-digit', hour12: false });
}

/** Human age: "45s", "12m", "3h", "2d". */
export function formatAge(ms: number): string {
  const s = Math.max(0, Math.round(ms / 1000));
  if (s < 60) return s + 's';
  if (s < 3600) return Math.floor(s / 60) + 'm';
  if (s < 86_400) return Math.floor(s / 3600) + 'h';
  return Math.floor(s / 86_400) + 'd';
}
