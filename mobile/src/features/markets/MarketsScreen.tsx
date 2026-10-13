import { View } from 'react-native';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { AssetBrowser } from '../../components/AssetBrowser';
import { useOpenMarket } from '../../components/AssetRow';
import { Panel, Text } from '../../components/ui';
import { usePullRefresh } from '../../hooks/usePullRefresh';
import { useScreenFocused } from '../../hooks/useScreenFocused';

/**
 * Markets (brief UX-02).
 *
 * A discovery screen, not a statistics dashboard: find an instrument, see
 * whether it is open and what it pays, star it or tap it to trade. Stake
 * limits and the per-duration payout table live on the trade screen, where
 * the choice they inform is made.
 *
 * Same query key and cadence as the trade screen, so moving between the two
 * reuses one cache entry instead of starting a second poll. The poll pauses
 * while another tab is in front: tab screens stay mounted, where the web
 * would have unmounted the page.
 *
 * The list fills the screen under the search and chips, which stay in view
 * while it scrolls; pulling the list down refreshes it.
 */
export function MarketsScreen() {
  const openMarket = useOpenMarket();
  const focused = useScreenFocused();
  const query = useQuery({
    queryKey: ['assets'],
    queryFn: api.assets.list,
    refetchInterval: focused ? 4000 : false,
  });
  const { refreshing, onRefresh } = usePullRefresh(() => query.refetch());

  return (
    <View className="flex-1 gap-3 bg-surface-950 px-3 pb-3 pt-4">
      <View>
        <Text accessibilityRole="header" className="text-lg font-bold text-content-100">
          Markets
        </Text>
        <Text className="text-xs text-content-400">Tap a market to trade it. Tap the star to keep it in Favourites.</Text>
      </View>

      <Panel className="flex-1 overflow-hidden">
        <AssetBrowser
          assets={query.data ?? []}
          loading={query.isLoading}
          onPick={openMarket}
          className="flex-1"
          headerClassName="border-b border-surface-800 p-3"
          listClassName="flex-1"
          wide
          onRefresh={onRefresh}
          refreshing={refreshing}
        />
      </Panel>

      {query.isError ? (
        <Text className="text-center text-xs text-down">Markets could not be loaded. Retrying…</Text>
      ) : null}
    </View>
  );
}
