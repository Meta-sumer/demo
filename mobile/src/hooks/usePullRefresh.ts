import { useCallback, useEffect, useRef, useState } from 'react';

/**
 * Pull-to-refresh state for a screen whose queries also poll.
 *
 * A query's own `isFetching` flips on every poll, so the spinner would flash
 * every few seconds if it were driven by that. This only reports the refresh
 * the trader pulled for, and holds it until every refetch it started settles.
 */
export function usePullRefresh(refresh: () => Promise<unknown> | unknown): {
  refreshing: boolean;
  onRefresh: () => void;
} {
  const [refreshing, setRefreshing] = useState(false);
  const mounted = useRef(true);
  const latest = useRef(refresh);
  latest.current = refresh;

  useEffect(
    () => () => {
      mounted.current = false;
    },
    [],
  );

  const onRefresh = useCallback(() => {
    setRefreshing(true);
    void Promise.resolve()
      .then(() => latest.current())
      .catch(() => undefined)
      .finally(() => {
        if (mounted.current) setRefreshing(false);
      });
  }, []);

  return { refreshing, onRefresh };
}
