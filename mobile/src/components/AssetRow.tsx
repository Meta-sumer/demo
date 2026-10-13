import { useCallback } from 'react';
import { Pressable, View } from 'react-native';
import { router } from 'expo-router';
import { Star } from 'lucide-react-native';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { useMarketStore } from '../stores/marketStore';
import { useFavouritesStore } from '../stores/favouritesStore';
import { useSettingsStore } from '../stores/settingsStore';
import { formatPrice } from '../utils/format';
import { resolveZone } from '../utils/time';
import { cn } from '../utils/cn';
import { useTokenColor } from '../utils/theme-colors';
import { assetAvailability, bestPayout, formatNextOpen, type AssetAvailability } from './assetList';
import { Icon, Text } from './ui';
import type { Asset } from '../types/api';

// The list's rules live in assetList.ts, where they are tested without React;
// these names stay importable from here, where they have always been.
export { assetAvailability, bestPayout, type AssetAvailability };

/**
 * The one way an instrument is shown in a list.
 *
 * Home, Markets and the selector inside Trade all render this, so a symbol,
 * its price, whether it can be traded and whether it is a favourite look and
 * read the same wherever the customer meets them (brief section 28). The star
 * is its own control beside the row rather than inside it: pressing it must
 * never also open the market.
 */

/**
 * Opens an instrument on the trade screen from anywhere outside it.
 *
 * Makes it the workspace's active asset (a tab too, in advanced mode) and
 * records it as recent, so Home and the selector can offer it back.
 */
export function useOpenMarket(): (asset: Pick<Asset, 'id'>) => void {
  const openAsset = useWorkspaceStore((s) => s.openAsset);
  const pushRecent = useFavouritesStore((s) => s.pushRecent);
  return useCallback(
    (asset) => {
      openAsset(asset.id);
      pushRecent(asset.id);
      router.navigate('/trade');
    },
    [openAsset, pushRecent],
  );
}

const AVAILABILITY_LABEL: Record<AssetAvailability, string> = {
  open: 'Open',
  paused: 'Paused',
  closed: 'Market closed',
};

/** Live tick when one has arrived, else the last price the list endpoint saw. */
export function useAssetPrice(asset: Asset): number | null {
  const tick = useMarketStore((s) => s.ticks[asset.id]?.price);
  return tick ?? asset.feed.lastPrice ?? null;
}

export function AvailabilityLabel({ asset, className }: { asset: Asset; className?: string }) {
  const state = assetAvailability(asset);
  const tone = state === 'open' ? 'text-up' : state === 'paused' ? 'text-warn' : 'text-content-400';
  return (
    <View className={cn('flex-row items-center gap-1', className)}>
      <View
        className={cn(
          'h-1.5 w-1.5 rounded-full',
          state === 'open' ? 'bg-up' : state === 'paused' ? 'bg-warn' : 'bg-surface-500',
        )}
      />
      <Text className={cn('text-[11px] font-medium', tone)}>{AVAILABILITY_LABEL[state]}</Text>
    </View>
  );
}

/** 24-hour movement with its sign. Nothing at all when there is no history. */
export function Movement({ percent, className }: { percent: number | null | undefined; className?: string }) {
  if (percent == null || !Number.isFinite(percent)) return null;
  const tone = percent > 0 ? 'text-up' : percent < 0 ? 'text-down' : 'text-content-400';
  const sign = percent > 0 ? '+' : '';
  return (
    <Text accessibilityLabel={'Change over 24 hours ' + sign + percent.toFixed(2) + '%'} className={cn('text-[11px] font-semibold tnum', className, tone)}>
      {sign}
      {percent.toFixed(2)}%
    </Text>
  );
}

export function FavouriteButton({ asset, className }: { asset: Asset; className?: string }) {
  const favourite = useFavouritesStore((s) => s.favourites.includes(asset.id));
  const toggle = useFavouritesStore((s) => s.toggle);
  const warn = useTokenColor('warn');
  return (
    <Pressable
      onPress={() => toggle(asset.id)}
      hitSlop={4}
      accessibilityRole="button"
      accessibilityState={{ selected: favourite }}
      accessibilityLabel={(favourite ? 'Remove ' : 'Add ') + asset.symbol + (favourite ? ' from favourites' : ' to favourites')}
      className={cn('w-11 items-center justify-center', className)}
    >
      <Icon as={Star} size={18} className={favourite ? 'text-warn' : 'text-content-400'} fill={favourite ? warn : 'none'} />
    </Pressable>
  );
}

interface AssetRowProps {
  asset: Asset;
  onSelect: (asset: Asset) => void;
  /** The instrument currently on the trade screen. */
  selected?: boolean;
  /**
   * The web client lays a wide row out as a table from the lg breakpoint. A
   * phone has no such width; accepted so callers keep their shape.
   */
  wide?: boolean;
}

/**
 * Column titles over a list of wide rows. The web shows them from lg only,
 * where rows become a table; a phone never does, so this renders nothing.
 */
export function AssetTableHeader(_props: { className?: string }) {
  return null;
}

/**
 * "Opens Mon 09:00" for a closed market whose schedule the server sent, in the
 * trader's chosen zone. Null otherwise - an unknown opening says nothing
 * rather than guessing.
 */
export function useReopening(asset: Pick<Asset, 'marketOpen' | 'nextOpenAt'>): string | null {
  const zone = resolveZone(useSettingsStore((s) => s.timezone));
  if (asset.marketOpen) return null;
  // The list refreshes every few seconds, which keeps "today" current enough.
  return formatNextOpen(asset.nextOpenAt, Date.now(), zone);
}

export function AssetRow({ asset, onSelect, selected }: AssetRowProps) {
  const price = useAssetPrice(asset);
  const payout = bestPayout(asset);
  const reopens = useReopening(asset);

  return (
    <View className={cn('flex-row items-stretch', selected && 'bg-accent/10')}>
      <Pressable
        onPress={() => onSelect(asset)}
        accessibilityRole="button"
        accessibilityState={{ selected: !!selected }}
        className="min-w-0 flex-1 flex-row items-center gap-3 py-2.5 pl-3 active:bg-surface-850"
      >
        <View className="min-w-0 flex-1">
          <View className="flex-row items-baseline gap-2">
            <Text numberOfLines={1} className="shrink text-sm font-semibold text-content-100">
              {asset.symbol}
            </Text>
            <AvailabilityLabel asset={asset} />
          </View>
          <View className="min-w-0 flex-row items-baseline gap-1">
            <Text numberOfLines={1} className="shrink text-[11px] text-content-400">
              {asset.name}
            </Text>
            {reopens ? <Text className="text-[11px] font-semibold text-content-300">· {reopens}</Text> : null}
          </View>
        </View>

        <View className="items-end">
          <Text className="text-sm font-semibold tnum text-content-100">
            {price !== null ? formatPrice(price, asset.pricePrecision) : '--'}
          </Text>
          <View className="flex-row items-center justify-end gap-2">
            <Movement percent={asset.change24hPercent} />
            {payout > 0 ? (
              <Text className="text-[11px] text-content-400">
                <Text className="text-[11px] font-semibold tnum text-content-200">{payout}%</Text> payout
              </Text>
            ) : (
              <Text className="text-[11px] text-content-400">No payout</Text>
            )}
          </View>
        </View>
      </Pressable>

      <FavouriteButton asset={asset} />
    </View>
  );
}
