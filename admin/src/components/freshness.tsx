import { useEffect, useState } from 'react';
import { Badge } from './ui';
import { formatAge, formatSince, freshness } from '../utils/freshness';

/** Re-render every few seconds so ages keep moving between refetches. */
export function useNow(intervalMs = 5000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}

/**
 * Marks a figure whose data is older than its refresh allows ("stale since
 * 12:04:31") or missing ("unavailable"). Renders nothing while fresh.
 */
export function StaleBadge({
  asOf,
  maxAgeMs,
  label,
}: {
  asOf: number | string | null | undefined;
  maxAgeMs: number;
  label?: string;
}) {
  const now = useNow();
  const f = freshness(asOf, maxAgeMs, now);
  if (f.state === 'fresh') return null;
  if (f.state === 'unavailable') {
    return (
      <Badge tone="warn" className="ml-1">
        {label ? label + ' ' : ''}unavailable
      </Badge>
    );
  }
  return (
    <Badge tone="warn" className="ml-1">
      {label ? label + ' ' : ''}stale since {formatSince(f.asOf, now)} ({formatAge(f.ageMs)})
    </Badge>
  );
}

/** "unavailable" in place of a value that could not be loaded. */
export function Unavailable() {
  return <span className="text-sm font-medium text-warn">unavailable</span>;
}
