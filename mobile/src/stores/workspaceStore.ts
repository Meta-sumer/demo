import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { persistStorage } from './storage';

/**
 * Trading workspace.
 *
 * The asset tabs are the trader's working set - which instruments they are
 * watching right now - so they survive a reload. Panels that are transient
 * (the settings drawer, the asset picker) deliberately do not.
 */
interface WorkspaceState {
  /** Asset ids open as tabs, in the order the trader put them. */
  openAssetIds: string[];
  activeAssetId: string | null;
  settingsOpen: boolean;
  /** Which transfer flow is over the workspace, if any. */
  transferModal: 'DEPOSIT' | 'WITHDRAWAL' | null;
  assetPickerOpen: boolean;
  /**
   * Advanced workspace on a wide screen: one chart, or two side by side. Set
   * from the top bar, beside the asset tabs; lasts for the session.
   */
  chartLayout: 1 | 2;

  openAsset: (assetId: string) => void;
  closeAsset: (assetId: string) => void;
  setActive: (assetId: string) => void;
  ensureOpen: (assetIds: string[]) => void;
  setSettingsOpen: (open: boolean) => void;
  toggleSettings: () => void;
  openTransferModal: (kind: 'DEPOSIT' | 'WITHDRAWAL') => void;
  closeTransferModal: () => void;
  setAssetPickerOpen: (open: boolean) => void;
  setChartLayout: (layout: 1 | 2) => void;
}

const MAX_TABS = 8;

export const useWorkspaceStore = create<WorkspaceState>()(
  persist(
    (set, get) => ({
      openAssetIds: [],
      activeAssetId: null,
      settingsOpen: false,
      transferModal: null,
      assetPickerOpen: false,
      chartLayout: 1,

      openAsset(assetId) {
        const { openAssetIds } = get();
        if (openAssetIds.includes(assetId)) {
          set({ activeAssetId: assetId, assetPickerOpen: false });
          return;
        }
        // Oldest tab makes way once the strip is full, like a browser.
        const next = [...openAssetIds, assetId].slice(-MAX_TABS);
        set({ openAssetIds: next, activeAssetId: assetId, assetPickerOpen: false });
      },

      closeAsset(assetId) {
        const { openAssetIds, activeAssetId } = get();
        const next = openAssetIds.filter((id) => id !== assetId);
        set({
          openAssetIds: next,
          // Closing the active tab moves to its neighbour rather than nothing.
          activeAssetId: activeAssetId === assetId ? (next[next.length - 1] ?? null) : activeAssetId,
        });
      },

      setActive: (activeAssetId) => set({ activeAssetId }),

      /** Seeds the strip on first load, once the asset list is known. */
      ensureOpen(assetIds) {
        const { openAssetIds, activeAssetId } = get();
        const stillValid = openAssetIds.filter((id) => assetIds.includes(id));

        if (stillValid.length === 0) {
          const seeded = assetIds.slice(0, 2);
          set({ openAssetIds: seeded, activeAssetId: seeded[0] ?? null });
          return;
        }

        if (stillValid.length !== openAssetIds.length || !activeAssetId || !stillValid.includes(activeAssetId)) {
          set({
            openAssetIds: stillValid,
            activeAssetId: stillValid.includes(activeAssetId ?? '') ? activeAssetId : stillValid[0]!,
          });
        }
      },

      setSettingsOpen: (settingsOpen) => set({ settingsOpen }),
      toggleSettings: () => set((state) => ({ settingsOpen: !state.settingsOpen })),
      openTransferModal: (transferModal) => set({ transferModal }),
      closeTransferModal: () => set({ transferModal: null }),
      setAssetPickerOpen: (assetPickerOpen) => set({ assetPickerOpen }),
      setChartLayout: (chartLayout) => set({ chartLayout }),
    }),
    {
      name: 'bt.workspace',
      storage: persistStorage,
      partialize: (state) => ({ openAssetIds: state.openAssetIds, activeAssetId: state.activeAssetId }),
    },
  ),
);
