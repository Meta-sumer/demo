import { create } from 'zustand';
import type { Trade } from '../types/api';

/**
 * Settled-trade results waiting to be read (brief UX-13).
 *
 * One entry per trade, newest first. The newest is shown in full and stays
 * until the trader closes it - a result is the number they were waiting on,
 * and a card that retires itself on a timer is gone exactly when they look
 * back from the chart. Older results fold under it as "+N more" rather than
 * stacking up the screen.
 *
 * This holds only what is being shown right now. The permanent record is the
 * trade history; if a result is dismissed, nothing is lost.
 */
export const MAX_RESULTS = 10;

interface TradeResultState {
  /** Newest first. */
  results: Trade[];
  /** Trade ids already announced, so a replayed event cannot show twice. */
  seen: Set<string>;
  announce: (trade: Trade) => void;
  dismiss: (tradeId: string) => void;
  dismissAll: () => void;
  reset: () => void;
}

export const useTradeResultStore = create<TradeResultState>((set, get) => ({
  results: [],
  seen: new Set<string>(),

  announce(trade) {
    // The settlement can reach this client twice - once over the socket and
    // once from an early close resolving locally. Showing it twice would read
    // as two trades.
    if (get().seen.has(trade.id)) return;

    set((state) => {
      const seen = new Set(state.seen);
      seen.add(trade.id);
      return { seen, results: [trade, ...state.results].slice(0, MAX_RESULTS) };
    });
  },

  dismiss(tradeId) {
    set((state) => ({ results: state.results.filter((trade) => trade.id !== tradeId) }));
  },

  dismissAll: () => set({ results: [] }),

  reset: () => set({ results: [], seen: new Set<string>() }),
}));
