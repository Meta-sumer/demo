import type { ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import { ChevronDown, Settings2, Star } from 'lucide-react-native';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { useFavouritesStore } from '../../stores/favouritesStore';
import { useSettingsStore, type TradeMode } from '../../stores/settingsStore';
import { formatPrice } from '../../utils/format';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { AvailabilityLabel, useAssetPrice } from '../../components/AssetRow';
import { Icon, IconButton, Text } from '../../components/ui';
import type { Asset } from '../../types/api';

/**
 * The trade header (brief UX-05): what is being traded, and whether it can be.
 *
 * Symbol and name, the live price, the market status in words, the favourite
 * star, the switch to another asset, and the Beginner / Advanced toggle. The
 * asset part is the switch - tapping it opens the same picker the "+" tab
 * does - while the star and the toggle are their own buttons beside it, so a
 * tap meant for one can never land on the other.
 *
 * A phone has no room for all of that on one line, and no desktop rail to hold
 * the chart settings, so the header is two lines: the asset, its price, the
 * star and the settings gear; then the mode toggle, with the asset tabs beside
 * it in advanced mode. The second line is there in both modes, so switching
 * mode moves nothing: the toggle stays under the finger that pressed it and
 * the chart keeps its place.
 */
export function TradeAssetHeader({ asset, tabs }: { asset: Asset | null; tabs?: ReactNode }) {
  const setPickerOpen = useWorkspaceStore((s) => s.setAssetPickerOpen);
  const favourite = useFavouritesStore((s) => (asset ? s.favourites.includes(asset.id) : false));
  const toggleFavourite = useFavouritesStore((s) => s.toggle);
  const accent = useTokenColor('accent');

  return (
    <View className="border-b border-surface-800 bg-surface-900">
      <View className="flex-row items-center gap-1.5 px-3 pb-1 pt-2">
        <Pressable
          onPress={() => setPickerOpen(true)}
          accessibilityRole="button"
          accessibilityLabel={asset ? 'Change asset, now ' + asset.symbol : 'Choose an asset'}
          className="min-w-0 flex-1 flex-row items-center gap-3 rounded-lg py-0.5 active:bg-surface-850/60"
        >
          <View className="min-w-0 flex-1">
            <View className="flex-row items-center gap-1.5">
              <Text numberOfLines={1} className="shrink text-sm font-bold text-content-100">
                {asset?.symbol ?? 'Choose an asset'}
              </Text>
              <Icon as={ChevronDown} size={16} className="text-content-400" />
            </View>
            {asset ? (
              <Text numberOfLines={1} className="text-[11px] text-content-400">
                {asset.name}
              </Text>
            ) : null}
          </View>

          {asset ? <HeaderPrice asset={asset} /> : null}
        </Pressable>

        {asset ? (
          <Pressable
            onPress={() => toggleFavourite(asset.id)}
            hitSlop={4}
            accessibilityRole="button"
            accessibilityState={{ selected: favourite }}
            accessibilityLabel={
              favourite ? 'Remove ' + asset.symbol + ' from favourites' : 'Add ' + asset.symbol + ' to favourites'
            }
            className="h-9 w-9 items-center justify-center rounded-lg active:bg-surface-850"
          >
            <Icon
              as={Star}
              size={16}
              className={favourite ? 'text-accent' : 'text-content-400'}
              fill={favourite ? accent : 'none'}
            />
          </Pressable>
        ) : null}

        {/* The chart settings. On a desktop they hang off the icon rail; a
            phone has none, so they open from here, beside the chart they
            change. */}
        <IconButton
          icon={Settings2}
          size={18}
          accessibilityLabel="Chart settings"
          onPress={() => useWorkspaceStore.getState().setSettingsOpen(true)}
          className="h-9 w-9 p-0"
        />
      </View>

      <View className="flex-row items-center gap-2 pb-2 pl-3">
        <ModeToggle />
        {tabs ? <View className="min-w-0 flex-1">{tabs}</View> : null}
      </View>
    </View>
  );
}

/**
 * Live price and the market status in words - the same words and helpers the
 * asset rows on Home and Markets use, so "Paused" means one thing everywhere.
 * The 24h movement the web header adds on wider screens has no room here.
 */
function HeaderPrice({ asset }: { asset: Asset }) {
  const price = useAssetPrice(asset);
  return (
    <View className="shrink-0 items-end">
      <Text className="text-sm font-bold tnum text-content-100">
        {price !== null ? formatPrice(price, asset.pricePrecision) : '--'}
      </Text>
      <AvailabilityLabel asset={asset} className="justify-end" />
    </View>
  );
}

const MODES: TradeMode[] = ['beginner', 'advanced'];
const MODE_LABEL: Record<TradeMode, string> = { beginner: 'Beginner', advanced: 'Advanced' };

/**
 * Beginner / Advanced.
 *
 * Two labelled segments rather than a switch: a switch says "on" or "off",
 * and neither mode is the other one switched off.
 */
export function ModeToggle({ className }: { className?: string }) {
  const mode = useSettingsStore((s) => s.tradeMode);
  const setMode = useSettingsStore((s) => s.setTradeMode);

  return (
    <View
      accessibilityRole="radiogroup"
      accessibilityLabel="Trading mode"
      className={cn('shrink-0 flex-row rounded-lg bg-surface-850 p-0.5', className)}
    >
      {MODES.map((value) => (
        <Pressable
          key={value}
          onPress={() => setMode(value)}
          accessibilityRole="radio"
          accessibilityState={{ checked: mode === value }}
          className={cn('rounded-md px-2 py-1', mode === value ? 'bg-surface-700' : 'active:bg-surface-800')}
        >
          <Text
            className={cn('text-[11px] font-semibold', mode === value ? 'text-content-100' : 'text-content-400')}
          >
            {MODE_LABEL[value]}
          </Text>
        </Pressable>
      ))}
    </View>
  );
}
