import { loadTokens } from '../services/http';
import { WS_URL } from '../config';

/**
 * Websocket client.
 *
 * Handles the parts that are easy to get wrong in a trading UI: reconnecting
 * with backoff, re-subscribing to the channels the app had before the drop, and
 * a heartbeat so a silently dead connection is noticed rather than leaving a
 * stale price frozen on screen.
 */

export type WsHandler = (payload: unknown, event: string) => void;

interface Frame {
  event: string;
  payload: unknown;
  emittedAt?: number;
  serverTime?: number;
}

export type ConnectionState = 'connecting' | 'open' | 'closed';

class TradingSocket {
  private socket: WebSocket | null = null;
  private handlers = new Map<string, Set<WsHandler>>();
  private stateHandlers = new Set<(state: ConnectionState) => void>();
  private channels = new Set<string>();
  private reconnectAttempts = 0;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private heartbeat: ReturnType<typeof setInterval> | null = null;
  private closedByUser = false;

  get state(): ConnectionState {
    if (!this.socket) return 'closed';
    if (this.socket.readyState === WebSocket.OPEN) return 'open';
    if (this.socket.readyState === WebSocket.CONNECTING) return 'connecting';
    return 'closed';
  }

  connect(): void {
    const tokens = loadTokens();
    if (!tokens?.accessToken || this.socket) return;

    this.closedByUser = false;
    const socket = new WebSocket(WS_URL + '?token=' + encodeURIComponent(tokens.accessToken));
    this.socket = socket;
    this.emitState('connecting');

    socket.onopen = () => {
      this.reconnectAttempts = 0;
      this.emitState('open');
      if (this.channels.size > 0) this.send({ type: 'subscribe', channels: [...this.channels] });
      this.heartbeat = setInterval(() => this.send({ type: 'ping' }), 25_000);
    };

    socket.onmessage = (event) => {
      let frame: Frame;
      try {
        frame = JSON.parse(event.data as string) as Frame;
      } catch {
        return;
      }
      for (const handler of this.handlers.get(frame.event) ?? []) handler(frame.payload, frame.event);
      for (const handler of this.handlers.get('*') ?? []) handler(frame.payload, frame.event);
    };

    socket.onclose = () => {
      this.teardown();
      this.emitState('closed');
      if (!this.closedByUser) this.scheduleReconnect();
    };

    // `onclose` always follows an error, so reconnection is handled in one place.
    socket.onerror = () => undefined;
  }

  disconnect(): void {
    this.closedByUser = true;
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.reconnectTimer = null;

    const socket = this.socket;
    if (socket) {
      /*
       * Closing a socket that is still CONNECTING makes the browser log
       * "WebSocket is closed before the connection is established". React's
       * StrictMode double-invokes effects in development, so the first
       * connection is torn down almost immediately and that warning appears on
       * every load. Waiting for the handshake to finish before closing keeps
       * the console honest - a real problem stays visible.
       */
      if (socket.readyState === WebSocket.CONNECTING) {
        socket.onopen = () => socket.close();
      } else {
        socket.close();
      }
    }

    this.teardown();
    this.channels.clear();
  }

  private teardown(): void {
    if (this.heartbeat) clearInterval(this.heartbeat);
    this.heartbeat = null;
    this.socket = null;
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return;
    this.reconnectAttempts += 1;
    const delay = Math.min(1000 * 2 ** this.reconnectAttempts, 15_000);
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }

  subscribe(channels: string[]): void {
    const added = channels.filter((c) => !this.channels.has(c));
    for (const channel of channels) this.channels.add(channel);
    if (added.length > 0) this.send({ type: 'subscribe', channels: added });
  }

  unsubscribe(channels: string[]): void {
    for (const channel of channels) this.channels.delete(channel);
    this.send({ type: 'unsubscribe', channels });
  }

  on(event: string, handler: WsHandler): () => void {
    const set = this.handlers.get(event) ?? new Set<WsHandler>();
    set.add(handler);
    this.handlers.set(event, set);
    return () => set.delete(handler);
  }

  onStateChange(handler: (state: ConnectionState) => void): () => void {
    this.stateHandlers.add(handler);
    return () => this.stateHandlers.delete(handler);
  }

  private emitState(state: ConnectionState): void {
    for (const handler of this.stateHandlers) handler(state);
  }

  private send(message: unknown): void {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(message));
  }
}

export const socket = new TradingSocket();

export const WsEvents = {
  MARKET_TICK: 'market.tick',
  MARKET_STATUS: 'market.status',
  TRADE_CREATED: 'trade.created',
  TRADE_UPDATED: 'trade.updated',
  TRADE_EXPIRING: 'trade.expiring',
  TRADE_SETTLED: 'trade.settled',
  WALLET_UPDATED: 'wallet.updated',
  /** A USDT deposit, withdrawal or transfer changed: `{ kind, id, status }`. */
  USDT_UPDATED: 'usdt.updated',
  NOTIFICATION_CREATED: 'notification.created',
  SUPPORT_MESSAGE: 'support.message',
  SUPPORT_CONVERSATION: 'support.conversation',
  SUPPORT_TYPING: 'support.typing',
} as const;

export const channels = {
  market: (assetId: string) => 'market:' + assetId,
  user: (userId: string) => 'user:' + userId,
};
