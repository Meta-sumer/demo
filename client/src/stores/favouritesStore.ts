import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { api } from '../services/api';

/**
 * Favourite and recently used instruments.
 *
 * Favourites are saved to the account, so they follow the customer between
 * devices; the local copy makes the star respond instantly and keeps working
 * if a save fails. Recents are this device's own history and stay local.
 */
const MAX_RECENT = 8;

interface FavouritesState {
  favourites: string[];
  recent: string[];
  /** Replaces the local list with the account's. */
  load: () => Promise<void>;
  toggle: (assetId: string) => void;
  isFavourite: (assetId: string) => boolean;
  pushRecent: (assetId: string) => void;
}

export const useFavouritesStore = create<FavouritesState>()(
  persist(
    (set, get) => ({
      favourites: [],
      recent: [],

      async load() {
        try {
          const { assetIds } = await api.favourites.get();
          set({ favourites: assetIds });
        } catch {
          /* keep the local list */
        }
      },

      toggle(assetId) {
        const current = get().favourites;
        const next = current.includes(assetId) ? current.filter((id) => id !== assetId) : [...current, assetId];
        set({ favourites: next });
        api.favourites.set(next).catch(() => undefined);
      },

      isFavourite: (assetId) => get().favourites.includes(assetId),

      pushRecent(assetId) {
        set((state) => ({ recent: [assetId, ...state.recent.filter((id) => id !== assetId)].slice(0, MAX_RECENT) }));
      },
    }),
    { name: 'bt.favourites' },
  ),
);
