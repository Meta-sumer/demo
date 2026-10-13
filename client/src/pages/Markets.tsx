import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { AssetBrowser } from '../components/AssetBrowser';
import { useOpenMarket } from '../components/AssetRow';
import { Panel } from '../components/ui';

/**
 * Markets (brief UX-02).
 *
 * A discovery screen, not a statistics dashboard: find an instrument, see
 * whether it is open and what it pays, star it or tap it to trade. Stake
 * limits and the per-duration payout table live on the trade screen, where
 * the choice they inform is made.
 *
 * Same query key and cadence as the trade screen, so moving between the two
 * reuses one cache entry instead of starting a second poll.
 *
 * On a wide screen the list takes the page's width as a table (market, price,
 * 24h, payout, status), which is easier to scan across than a narrow column.
 */
export function MarketsPage() {
  const openMarket = useOpenMarket();
  const query = useQuery({ queryKey: ['assets'], queryFn: api.assets.list, refetchInterval: 4000 });

  return (
    <div className="mx-auto max-w-3xl space-y-3 lg:max-w-6xl">
      <header>
        <h1 className="text-lg font-bold text-content-100">Markets</h1>
        <p className="text-xs text-content-400">Tap a market to trade it. Tap the star to keep it in Favourites.</p>
      </header>

      <Panel className="overflow-hidden">
        <AssetBrowser
          assets={query.data ?? []}
          loading={query.isLoading}
          onPick={openMarket}
          headerClassName="border-b border-surface-800 p-3"
          wide
        />
      </Panel>

      {query.isError ? <p className="text-center text-xs text-down">Markets could not be loaded. Retrying…</p> : null}
    </div>
  );
}
