import { useMemo } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { LANDING_REFRESH_MS, landingBoard, type LandingBoard } from './markets';

/**
 * The live market board for the landing page (`GET /public/markets`, no
 * session needed). One query however many sections read it, refreshed every
 * `LANDING_REFRESH_MS` while `polling` is on. A failed refresh hides the
 * figures until the next one succeeds, rather than leaving old ones up.
 */
export function useLandingBoard(polling = true): LandingBoard & { loading: boolean } {
  const query = useQuery({
    queryKey: ['public', 'markets'],
    queryFn: api.public.markets,
    refetchInterval: polling ? LANDING_REFRESH_MS : false,
  });
  const data = query.isError ? undefined : query.data;
  const board = useMemo(() => landingBoard(data), [data]);
  return { ...board, loading: query.isPending };
}
