import { Pressable, ScrollView, View } from 'react-native';
import { Plus, X } from 'lucide-react-native';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { cn } from '../../utils/cn';
import { Icon, Text } from '../../components/ui';
import type { Asset } from '../../types/api';

/**
 * Open instruments, as tabs.
 *
 * Each tab carries the one number that decides whether a trader looks further -
 * the payout - so switching is a decision they can make without opening the
 * instrument first.
 *
 * On the web these sit in the desktop top bar. A phone's top bar has no width
 * for them beside the balance, so they ride on the trade header's second line,
 * beside the mode toggle, and scroll sideways. This owns no bar of its own -
 * no background, no border, no row padding.
 *
 * What the desktop strip adds and a phone leaves out: the price on each tab
 * and the feed badge (no width - the tab's dot says tradable or not, and the
 * ticket refuses the order anyway if the feed is not healthy), and the one
 * chart / two charts switch (two charts side by side need a desktop's width).
 * A tab's close button is always shown rather than on hover, since a finger
 * cannot hover.
 */
export function AssetTabs({ assets }: { assets: Asset[] }) {
  const openAssetIds = useWorkspaceStore((s) => s.openAssetIds);
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const setActive = useWorkspaceStore((s) => s.setActive);
  const closeAsset = useWorkspaceStore((s) => s.closeAsset);
  const setPickerOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);

  const byId = new Map(assets.map((asset) => [asset.id, asset]));
  const openAssets = openAssetIds.map((id) => byId.get(id)).filter((a): a is Asset => !!a);

  return (
    <View className="min-w-0 flex-row items-center gap-1.5">
      <Pressable
        onPress={() => setPickerOpen(true)}
        accessibilityRole="button"
        accessibilityLabel="Add an instrument"
        className="h-8 w-8 items-center justify-center rounded-lg bg-accent active:opacity-80"
      >
        <Icon as={Plus} size={18} className="text-surface-950" />
      </Pressable>

      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        className="min-w-0 flex-1"
        contentContainerClassName="items-center gap-1.5 pr-3"
        keyboardShouldPersistTaps="handled"
      >
        {openAssets.map((asset) => {
          const active = asset.id === activeAssetId;
          const bestPayout = asset.payouts.reduce((best, p) => Math.max(best, p.payoutPercent), 0);
          const tradable = asset.marketOpen && asset.feed.status !== 'OFFLINE' && asset.feed.status !== 'STALE';

          return (
            <View
              key={asset.id}
              className={cn(
                'flex-row items-center gap-1.5 rounded-lg border py-1 pl-2.5',
                openAssets.length > 1 ? 'pr-1' : 'pr-2.5',
                active ? 'border-accent/50 bg-surface-850' : 'border-surface-700 bg-surface-900',
              )}
            >
              <Pressable
                onPress={() => setActive(asset.id)}
                accessibilityRole="tab"
                accessibilityState={{ selected: active }}
                accessibilityLabel={asset.symbol + ', ' + bestPayout + '% payout, ' + (tradable ? 'tradable' : 'paused')}
                className="flex-row items-center gap-2"
              >
                <View className={cn('h-1.5 w-1.5 rounded-full', tradable ? 'bg-up' : 'bg-down')} />
                <View>
                  <Text className="text-[11px] font-semibold leading-tight text-content-100">{asset.symbol}</Text>
                  <Text className="text-[10px] font-bold leading-tight text-warn">{bestPayout}%</Text>
                </View>
              </Pressable>

              {openAssets.length > 1 ? (
                <Pressable
                  onPress={() => closeAsset(asset.id)}
                  hitSlop={6}
                  accessibilityRole="button"
                  accessibilityLabel={'Close ' + asset.symbol}
                  className="rounded-md p-0.5 active:bg-surface-800"
                >
                  <Icon as={X} size={12} className="text-content-400" />
                </Pressable>
              ) : null}
            </View>
          );
        })}
      </ScrollView>
    </View>
  );
}
