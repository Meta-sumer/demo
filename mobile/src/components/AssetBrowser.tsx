import { useEffect, useMemo, useRef, useState } from 'react';
import { FlatList, Pressable, RefreshControl, ScrollView, TextInput, View } from 'react-native';
import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import { Check, ChevronDown, Search, X } from 'lucide-react-native';
import { useFavouritesStore } from '../stores/favouritesStore';
import { persistStorage } from '../stores/storage';
import { cn } from '../utils/cn';
import { useTokenColor } from '../utils/theme-colors';
import { AssetRow } from './AssetRow';
import { Button, EmptyState, Field, Icon, Sheet, Skeleton, Text } from './ui';
import {
  ASSET_SORTS,
  assetsInFilter,
  catalogueOrder,
  categoriesOf,
  isAssetSort,
  matchesSearch,
  openCount,
  sortAssets,
  type AssetFilter,
  type AssetSort,
} from './assetList';
import type { Asset } from '../types/api';

/**
 * Search, filters and the instrument list.
 *
 * The Markets page and the selector inside Trade are the same task - find an
 * instrument and pick it - so they share this body and only their frame
 * differs (brief UX-02/UX-03). Filtering is local and instant: the whole
 * catalogue is already in the `['assets']` cache.
 *
 * People look either for a name or for the best payout (reference record 03),
 * so the list sorts by either. Each chip says how many instruments it holds -
 * of those matching the search, when there is one, so the chips also say
 * where the matches are - and a line says how many of the listed ones take
 * trades right now (Binomo's "147 in total · 85 active").
 *
 * The list is a FlatList, so it can be the scrolling body of a bottom sheet
 * (`Sheet` with `scroll={false}`) as well as fill the Markets screen; the
 * search and chips stay above it, in view while the list scrolls.
 */

/**
 * The sort is a per-device preference (`bt.marketSort`, as on the web); if
 * storage fails it just means Default. A store rather than a read on mount,
 * so Markets and the selector follow one choice.
 */
interface MarketSortState {
  sort: AssetSort;
  setSort: (sort: AssetSort) => void;
}

const useMarketSortStore = create<MarketSortState>()(
  persist(
    (set) => ({
      sort: 'default',
      setSort: (sort) => set({ sort }),
    }),
    { name: 'bt.marketSort', storage: persistStorage, partialize: (state) => ({ sort: state.sort }) },
  ),
);

interface Props {
  assets: Asset[];
  loading?: boolean;
  onPick: (asset: Asset) => void;
  selectedId?: string | null;
  /** Classes for the scrolling list, so a sheet can bound its height. */
  listClassName?: string;
  /** Classes for the search + chips block. */
  headerClassName?: string;
  className?: string;
  /**
   * The list has the page's width (Markets). The web lays such a list out as a
   * table from lg; a phone never has that width, so rows stay rows.
   */
  wide?: boolean;
  /** Focus the search box on mount (the selector sheet). */
  autoFocusSearch?: boolean;
  /** Pull to refresh on the list. */
  onRefresh?: () => void;
  refreshing?: boolean;
}

/** Long enough for a sheet to finish rising before the keyboard comes up over it. */
const FOCUS_DELAY_MS = 320;

export function AssetBrowser({
  assets,
  loading,
  onPick,
  selectedId,
  listClassName,
  headerClassName,
  className,
  wide = false,
  autoFocusSearch = false,
  onRefresh,
  refreshing = false,
}: Props) {
  const favourites = useFavouritesStore((s) => s.favourites);
  const recent = useFavouritesStore((s) => s.recent);
  const [search, setSearch] = useState('');
  const [filter, setFilter] = useState<AssetFilter>('all');
  const storedSort = useMarketSortStore((s) => s.sort);
  const setSort = useMarketSortStore((s) => s.setSort);
  const sort: AssetSort = isAssetSort(storedSort) ? storedSort : 'default';
  const searchRef = useRef<TextInput>(null);
  const tint = useTokenColor('accent');

  useEffect(() => {
    if (!autoFocusSearch) return;
    const timer = setTimeout(() => searchRef.current?.focus(), FOCUS_DELAY_MS);
    return () => clearTimeout(timer);
  }, [autoFocusSearch]);

  const catalogue = useMemo(() => catalogueOrder(assets), [assets]);
  const categories = useMemo(() => categoriesOf(assets), [assets]);
  const context = useMemo(() => ({ favourites, recent }), [favourites, recent]);
  const term = search.trim();

  const chips = useMemo(() => {
    const list: Array<{ value: AssetFilter; label: string }> = [
      { value: 'all', label: 'All' },
      { value: 'favourites', label: 'Favourites' },
      { value: 'recent', label: 'Recent' },
      ...categories.map((category) => ({
        value: `category:${category}` as AssetFilter,
        label: categoryLabel(category),
      })),
    ];
    return list.map((chip) => ({
      ...chip,
      count: assetsInFilter(chip.value, catalogue, context).filter((asset) => matchesSearch(asset, term)).length,
    }));
  }, [categories, catalogue, context, term]);

  const visible = useMemo(
    () =>
      sortAssets(
        assetsInFilter(filter, catalogue, context).filter((asset) => matchesSearch(asset, term)),
        sort,
      ),
    [filter, catalogue, context, term, sort],
  );
  // A search that finds nothing in a narrow filter may still find it overall;
  // offer that instead of a dead end.
  const elsewhere = term && visible.length === 0 && filter !== 'all' ? chips[0]!.count : 0;
  const openNow = openCount(visible);

  const empty = loading ? (
    <View className="gap-2 p-3">
      {Array.from({ length: 6 }).map((_, index) => (
        <Skeleton key={index} className="h-12 w-full" />
      ))}
    </View>
  ) : elsewhere > 0 ? (
    <View className="items-center px-6 py-10">
      <Text className="text-center text-sm text-content-200">Nothing here matches “{search.trim()}”.</Text>
      <Button variant="ghost" className="mt-3" onPress={() => setFilter('all')}>
        {'Show ' + elsewhere + ' ' + (elsewhere === 1 ? 'match' : 'matches') + ' in all markets'}
      </Button>
    </View>
  ) : term ? (
    <EmptyState title="No market matches that search" description="Try the symbol, e.g. BTC, or part of the name." />
  ) : filter === 'favourites' ? (
    <EmptyState
      title="No favourites yet"
      description="Tap the star on any market to keep it here, on Home and in the trade screen."
    />
  ) : filter === 'recent' ? (
    <EmptyState title="Nothing recent" description="Markets you open to trade appear here." />
  ) : (
    <EmptyState title="No markets available" description="Please check again shortly." />
  );

  return (
    <View className={cn('min-h-0', className)}>
      <View className={cn('gap-2.5', headerClassName)}>
        <Field
          ref={searchRef}
          value={search}
          onChangeText={setSearch}
          placeholder="Search markets"
          accessibilityLabel="Search markets by symbol or name"
          autoComplete="off"
          autoCorrect={false}
          autoCapitalize="none"
          returnKeyType="search"
          left={<Icon as={Search} size={16} className="text-content-400" />}
          right={
            search ? (
              <Pressable
                onPress={() => setSearch('')}
                hitSlop={8}
                accessibilityRole="button"
                accessibilityLabel="Clear search"
                className="-mr-1.5 rounded-md p-1.5 active:bg-surface-800"
              >
                <Icon as={X} size={14} className="text-content-400" />
              </Pressable>
            ) : null
          }
        />

        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
          accessibilityRole="tablist"
          accessibilityLabel="Filter markets"
          className="-mx-1 grow-0"
          contentContainerClassName="gap-1.5 px-1 pb-0.5"
        >
          {chips.map((chip) => {
            const selected = filter === chip.value;
            return (
              <Pressable
                key={chip.value}
                accessibilityRole="tab"
                accessibilityState={{ selected }}
                onPress={() => setFilter(chip.value)}
                className={cn(
                  'flex-row items-center rounded-full border px-3 py-1',
                  selected ? 'border-transparent bg-accent' : 'border-surface-700 bg-surface-850 active:bg-surface-800',
                )}
              >
                <Text className={cn('text-xs font-semibold', selected ? 'text-surface-950' : 'text-content-300')}>
                  {chip.label}
                </Text>
                <Text
                  className={cn('ml-1 text-xs font-semibold tnum opacity-70', selected ? 'text-surface-950' : 'text-content-300')}
                >
                  {chip.count}
                </Text>
              </Pressable>
            );
          })}
        </ScrollView>

        {!loading && assets.length > 0 ? (
          <View className="flex-row items-center justify-between gap-3">
            <Text numberOfLines={1} className="min-w-0 shrink text-[11px] text-content-400">
              {visible.length > 0 ? (
                <>
                  <Text className="text-[11px] font-semibold tnum text-content-200">{openNow}</Text> of{' '}
                  <Text className="text-[11px] tnum text-content-400">{visible.length}</Text> open now
                </>
              ) : null}
            </Text>
            <SortControl value={sort} onChange={setSort} />
          </View>
        ) : null}
      </View>

      <FlatList
        data={loading ? [] : visible}
        keyExtractor={(asset) => asset.id}
        extraData={selectedId}
        renderItem={({ item }) => (
          <AssetRow asset={item} onSelect={onPick} selected={item.id === selectedId} wide={wide} />
        )}
        ItemSeparatorComponent={RowDivider}
        ListEmptyComponent={empty}
        className={cn('min-h-0', listClassName)}
        keyboardShouldPersistTaps="handled"
        keyboardDismissMode="on-drag"
        initialNumToRender={14}
        windowSize={7}
        refreshControl={
          onRefresh ? (
            <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={tint} colors={[tint]} />
          ) : undefined
        }
      />
    </View>
  );
}

function RowDivider() {
  return <View className="h-px bg-surface-800/70" />;
}

/**
 * "Sort  Default ▾" - the web's small select. Its options open in a sheet of
 * their own, which also works when the list is already inside one.
 */
function SortControl({ value, onChange }: { value: AssetSort; onChange: (sort: AssetSort) => void }) {
  const [open, setOpen] = useState(false);
  const current = ASSET_SORTS.find((option) => option.value === value) ?? ASSET_SORTS[0]!;

  return (
    <>
      <View className="shrink-0 flex-row items-center gap-1.5">
        <Text className="text-[11px] text-content-400">Sort</Text>
        <Pressable
          onPress={() => setOpen(true)}
          accessibilityRole="button"
          accessibilityLabel={'Sort: ' + current.label}
          className="flex-row items-center gap-1 rounded-md border border-surface-700 bg-surface-850 py-1 pl-2 pr-1 active:border-accent"
        >
          <Text className="text-[11px] font-semibold text-content-100">{current.label}</Text>
          <Icon as={ChevronDown} size={13} className="text-content-400" />
        </Pressable>
      </View>

      <Sheet open={open} onClose={() => setOpen(false)} title="Sort">
        <View className="px-2 pb-2">
          {ASSET_SORTS.map((option) => {
            const selected = option.value === value;
            return (
              <Pressable
                key={option.value}
                accessibilityRole="radio"
                accessibilityState={{ checked: selected }}
                onPress={() => {
                  onChange(isAssetSort(option.value) ? option.value : 'default');
                  setOpen(false);
                }}
                className={cn(
                  'flex-row items-center gap-3 rounded-lg px-3 py-3',
                  selected ? 'bg-accent/10' : 'active:bg-surface-850',
                )}
              >
                <Text
                  className={cn('min-w-0 flex-1 text-sm', selected ? 'font-semibold text-content-100' : 'text-content-200')}
                >
                  {option.label}
                </Text>
                {selected ? <Icon as={Check} size={16} className="text-accent" /> : null}
              </Pressable>
            );
          })}
        </View>
      </Sheet>
    </>
  );
}

/** "crypto" -> "Crypto", "FOREX" -> "Forex". Categories come from the server as codes. */
export function categoryLabel(category: string): string {
  const lower = category.toLowerCase().replace(/_/g, ' ');
  return lower.charAt(0).toUpperCase() + lower.slice(1);
}
