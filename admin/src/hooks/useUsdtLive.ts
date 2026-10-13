import { useEffect, useState } from 'react';
import { create } from 'zustand';
import { socket, type ConnectionState } from '../websocket/client';

/**
 * Freshness for the USDT screens.
 *
 * USDT state changes come from outside the console - BeeX webhooks, chain
 * confirmations - so the lists must refresh on their own. When the server
 * pushes `usdt.*` events on the admin ops channel, useAdminRealtime invalidates
 * every ['usdt'] query and these screens drop to a slow safety poll. Until such
 * an event has been seen on an open socket they poll every 15 seconds.
 */
export const USDT_POLL_MS = 15_000;
const LIVE_SAFETY_POLL_MS = 60_000;

/** Any `usdt.*` / `usdt:*` / `usdt_*` event counts; the exact names are the server's. */
export function isUsdtEvent(event: string): boolean {
  return /^usdt[.:_]/i.test(event);
}

export const useUsdtLiveStore = create<{ lastEventAt: number | null; mark: () => void }>((set) => ({
  lastEventAt: null,
  mark: () => set({ lastEventAt: Date.now() }),
}));

export function useUsdtRefetchInterval(): number {
  const live = useUsdtLiveStore((s) => s.lastEventAt !== null);
  const [connection, setConnection] = useState<ConnectionState>(socket.state);

  useEffect(() => socket.onStateChange(setConnection), []);

  return live && connection === 'open' ? LIVE_SAFETY_POLL_MS : USDT_POLL_MS;
}
