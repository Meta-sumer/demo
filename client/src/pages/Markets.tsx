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
 * limits live on the trade screen, where the choice they inform is made.
 *
 * Same query key and cadence as the trade screen, so moving between the two
 * reuses one cache entry instead of starting a second poll.
 *
 * While only a few instruments are offered (launch scope: BTC/USD and
 * ETH/USD) each is a card - live price, the last hour's line, payout by
 * duration, status, star and a Trade button - so the page is not a two-row
 * table on an empty screen. Beyond MAX_CARDS the list is the table it was: on
 * a wide screen market, price, 24h, payout and status in columns, which is
 * easier to scan down than a wall of cards.
 */
const MAX_CARDS = 9;

export function MarketsPage() {
  const openMarket = useOpenMarket();
  const query = useQuery({ queryKey: ['assets'], queryFn: api.assets.list, refetchInterval: 4000 });
  const assets = query.data ?? [];
  const cards = assets.length <= MAX_CARDS;

  const browser = (
    <AssetBrowser
      assets={assets}
      loading={query.isLoading}
      onPick={openMarket}
      headerClassName={cards ? 'panel p-3' : 'border-b border-surface-800 p-3'}
      listClassName={cards ? 'mt-3' : undefined}
      wide
      cards={cards}
    />
  );

  return (
    <div className="mx-auto max-w-3xl space-y-3 lg:max-w-6xl">
      <header>
        <h1 className="text-lg font-bold text-content-100">Markets</h1>
        <p className="text-xs text-content-400">
          {cards
            ? 'Live prices and payouts. Press Trade to open a market; tap the star to keep it in Favourites.'
            : 'Tap a market to trade it. Tap the star to keep it in Favourites.'}
        </p>
      </header>

      {cards ? browser : <Panel className="overflow-hidden">{browser}</Panel>}

      {cards && assets.length > 0 ? (
        <p className="px-1 text-[11px] leading-relaxed text-content-400">
          Payouts are the pricing engine&apos;s live indicative figures: at each duration, the lower of UP and DOWN.
          They move with the market. The ticket shows yours before you commit, and it is locked once your trade is
          accepted.
        </p>
      ) : null}

      {query.isError ? <p className="text-center text-xs text-down">Markets could not be loaded. Retrying…</p> : null}
    </div>
  );
}
