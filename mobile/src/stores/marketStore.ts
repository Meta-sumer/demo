import { create } from 'zustand';
import type { FeedHealth, Tick } from '../types/api';

interface MarketState {
  /** Latest tick per asset id. */
  ticks: Record<string, Tick>;
  /** Previous price per asset, used to colour the up/down flash. */
  previous: Record<string, number>;
  feeds: Record<string, FeedHealth>;

  applyTick: (tick: Tick) => void;
  /** The newest tick of several assets in one update (see useRealtime). */
  applyTicks: (ticks: Tick[]) => void;
  setFeeds: (feeds: FeedHealth[]) => void;
  setFeed: (feed: FeedHealth) => void;
}

export const useMarketStore = create<MarketState>((set, get) => ({
  ticks: {},
  previous: {},
  feeds: {},

  applyTick(tick) {
    const current = get().ticks[tick.assetId];
    set((state) => ({
      ticks: { ...state.ticks, [tick.assetId]: tick },
      previous: current ? { ...state.previous, [tick.assetId]: current.price } : state.previous,
    }));
  },

  applyTicks(batch) {
    if (batch.length === 0) return;
    set((state) => {
      const ticks = { ...state.ticks };
      const previous = { ...state.previous };
      for (const tick of batch) {
        const current = ticks[tick.assetId];
        if (current) previous[tick.assetId] = current.price;
        ticks[tick.assetId] = tick;
      }
      return { ticks, previous };
    });
  },

  setFeeds(feeds) {
    set({ feeds: Object.fromEntries(feeds.map((f) => [f.assetId, f])) });
  },

  setFeed(feed) {
    set((state) => ({ feeds: { ...state.feeds, [feed.assetId]: feed } }));
  },
}));
