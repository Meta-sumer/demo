import { create } from 'zustand';
import { useAuthStore } from './authStore';
import type { Trade } from '../types/api';

/**
 * Running trades and this session's settlements.
 *
 * Read on every screen now - the navigation counts the running trades - so it
 * is filled from anywhere (see `useActiveTradeCount`) and kept current by the
 * websocket, not only while the trade screen is open.
 */
interface TradeState {
  openTrades: Trade[];
  /** Trades settled during this session, newest first, for the results strip. */
  recentlySettled: Trade[];
  setOpenTrades: (trades: Trade[]) => void;
  upsertTrade: (trade: Trade) => void;
  settleTrade: (trade: Trade) => void;
  reset: () => void;
}

const OPEN_STATUSES = new Set(['OPEN', 'SETTLING']);

export const useTradeStore = create<TradeState>((set) => ({
  openTrades: [],
  recentlySettled: [],

  setOpenTrades: (trades) => set({ openTrades: trades.filter((t) => OPEN_STATUSES.has(t.status)) }),

  upsertTrade(trade) {
    set((state) => {
      const without = state.openTrades.filter((t) => t.id !== trade.id);
      if (!OPEN_STATUSES.has(trade.status)) return { openTrades: without };
      // Soonest expiry first: that is the order a trader watches them in.
      return { openTrades: [...without, trade].sort((a, b) => a.expiryTimestamp - b.expiryTimestamp) };
    });
  },

  settleTrade(trade) {
    set((state) => ({
      openTrades: state.openTrades.filter((t) => t.id !== trade.id),
      recentlySettled: [trade, ...state.recentlySettled.filter((t) => t.id !== trade.id)].slice(0, 20),
    }));
  },

  reset: () => set({ openTrades: [], recentlySettled: [] }),
}));

// Trades belong to the person who placed them. Signing out - or in as someone
// else on the same device - must not leave the last person's positions on the
// chart or in the navigation count.
useAuthStore.subscribe((state, previous) => {
  if (state.user?.id !== previous.user?.id) useTradeStore.getState().reset();
});
