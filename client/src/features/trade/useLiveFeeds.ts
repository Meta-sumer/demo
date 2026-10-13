import { useEffect, useMemo, useRef } from 'react';
import { channels, socket } from '../../websocket/client';
import { useMarketStore } from '../../stores/marketStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';

/**
 * Keeps prices arriving for every asset the trader has a running trade on.
 *
 * The trade screen subscribes to the asset on its chart and nothing else, so a
 * trade on another asset would sit on a price frozen at whenever that asset was
 * last charted - and a live result worked out from it would be wrong without
 * looking wrong. This subscribes the rest for as long as a list of running
 * trades is on screen.
 *
 * The socket's subscriptions are a plain set, not reference-counted: the
 * chart's own subscription is dropped on its unmount whoever else wanted it,
 * and anything dropped here is dropped for the chart too. So:
 *
 *   - with `keepWorkspace` (a list on the trade screen), a channel the
 *     workspace may be drawing - the active asset or an open tab, which is
 *     where the second chart comes from - is never released from here;
 *   - the channels are re-asserted whenever the chart changes asset, and on a
 *     slow timer as a backstop for the second chart, whose asset is the trade
 *     screen's own state. `subscribe` only sends for a channel that is missing,
 *     so re-asserting a healthy set costs nothing.
 *
 * Only one list of running trades is on screen at a time (the trade screen's
 * panel or the Trades page), so two of these never contend for a channel.
 */
const REASSERT_MS = 5_000;

export function useLiveFeeds(assetIds: readonly string[], { keepWorkspace }: { keepWorkspace: boolean }): void {
  const key = useMemo(() => [...new Set(assetIds)].sort().join(' '), [assetIds]);
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const held = useRef<string[]>([]);
  const keepRef = useRef(keepWorkspace);
  keepRef.current = keepWorkspace;

  useEffect(() => {
    const wanted = key ? key.split(' ').map(channels.market) : [];
    release(
      held.current.filter((channel) => !wanted.includes(channel)),
      keepRef.current,
    );
    held.current = wanted;
    if (wanted.length === 0) return;
    socket.subscribe(wanted);
    const timer = window.setInterval(() => socket.subscribe(wanted), REASSERT_MS);
    return () => window.clearInterval(timer);
  }, [key]);

  // The chart has just moved to another asset and unsubscribed the one it
  // left, which may be one of these. Effects clean up before they run again,
  // so this lands after the chart's unsubscribe in the same commit.
  useEffect(() => {
    if (held.current.length > 0) socket.subscribe(held.current);
  }, [activeAssetId]);

  useEffect(
    () => () => {
      release(held.current, keepRef.current);
      held.current = [];
    },
    [],
  );
}

function release(list: string[], keepWorkspace: boolean): void {
  if (list.length === 0) return;
  let drop = list;
  if (keepWorkspace) {
    const { activeAssetId, openAssetIds } = useWorkspaceStore.getState();
    const keep = new Set(
      [activeAssetId, ...openAssetIds].filter((id): id is string => !!id).map((id) => channels.market(id)),
    );
    drop = list.filter((channel) => !keep.has(channel));
  }
  if (drop.length > 0) socket.unsubscribe(drop);
}

/** The latest price for an asset, or null before one has arrived. */
export function useLivePrice(assetId: string): number | null {
  return useMarketStore((s) => s.ticks[assetId]?.price ?? null);
}
