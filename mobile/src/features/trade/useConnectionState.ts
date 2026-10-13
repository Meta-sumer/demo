import { useSyncExternalStore } from 'react';
import { socket, type ConnectionState } from '../../websocket/client';

/**
 * The live-price connection, as a value a component can render.
 *
 * Read through `useSyncExternalStore` so every reader agrees on the state in
 * the same render - the ticket's message and its disabled buttons must never
 * disagree about whether prices are live.
 */
export function useConnectionState(): ConnectionState {
  return useSyncExternalStore(
    (notify) => socket.onStateChange(notify),
    () => socket.state,
    () => 'closed' as const,
  );
}
