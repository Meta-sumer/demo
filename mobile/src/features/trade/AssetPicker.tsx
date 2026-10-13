import { useWorkspaceStore } from '../../stores/workspaceStore';
import { useFavouritesStore } from '../../stores/favouritesStore';
import { AssetBrowser } from '../../components/AssetBrowser';
import { Sheet } from '../../components/ui';
import type { Asset } from '../../types/api';

/**
 * Asset selector inside Trade (brief UX-03).
 *
 * A bottom sheet: it rises from where the thumb already is, keeps the top of
 * the chart visible above it, and goes back down with a drag, a tap on the
 * veil or the back gesture.
 *
 * The body - search, Favourites / Recent / categories and the rows - is the
 * same component the Markets screen uses, so the two can never disagree. The
 * search box does not take focus as the sheet opens: that would raise the
 * keyboard over half the sheet before the customer has seen their favourites.
 *
 * Mounted by the trade screen; opened through `useWorkspaceStore.setAssetPickerOpen`.
 */
export function AssetPicker({ assets }: { assets: Asset[] }) {
  const open = useWorkspaceStore((s) => s.assetPickerOpen);
  const setOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);
  const openAsset = useWorkspaceStore((s) => s.openAsset);
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const pushRecent = useFavouritesStore((s) => s.pushRecent);

  function pick(asset: Asset) {
    // Opening an asset also closes the picker (see the workspace store).
    openAsset(asset.id);
    pushRecent(asset.id);
  }

  return (
    <Sheet open={open} onClose={() => setOpen(false)} title="Choose a market" size="large" scroll={false}>
      <AssetBrowser
        assets={assets}
        loading={assets.length === 0}
        onPick={pick}
        selectedId={activeAssetId}
        className="flex-1"
        headerClassName="border-b border-surface-800 px-4 pb-3"
        listClassName="flex-1"
      />
    </Sheet>
  );
}
