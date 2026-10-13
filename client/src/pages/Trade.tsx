import { useEffect, useMemo, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { useMarketStore } from '../stores/marketStore';
import { useTradeStore } from '../stores/tradeStore';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { useAccountStore } from '../stores/accountStore';
import { useSettingsStore } from '../stores/settingsStore';
import { useFavouritesStore } from '../stores/favouritesStore';
import { useMarketSubscription } from '../hooks/useRealtime';
import { AssetPicker } from '../features/trade/AssetPicker';
import { PriceChart } from '../features/trade/PriceChart';
import { TradeTicket } from '../features/trade/TradeTicket';
import { PositionsPanel } from '../features/trade/PositionsPanel';
import { TradeAssetHeader } from '../features/trade/TradeAssetHeader';
import { accountOf } from '../features/trade/ticketModel';
import { useMediaQuery } from '../features/trade/useMediaQuery';

/**
 * The trading workspace.
 *
 *   asset header
 *   chart (or two, advanced on wide screens)  | ticket
 *                                              | positions (toggled from the ticket)
 *
 * Stacked below xl in the brief's order - asset, chart, duration, investment,
 * contract information, UP/DOWN - and side by side above it, which rearranges
 * the same journey without changing it (brief section 31).
 *
 * Nothing here decides a price - the chart draws what the server sent, and the
 * ticket sends only asset, direction, amount, expiry and account.
 */
export function TradePage() {
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const openAssetIds = useWorkspaceStore((s) => s.openAssetIds);
  const ensureOpen = useWorkspaceStore((s) => s.ensureOpen);
  const setFeeds = useMarketStore((s) => s.setFeeds);
  const mode = useSettingsStore((s) => s.tradeMode);
  const account = useAccountStore((s) => s.active);
  const pushRecent = useFavouritesStore((s) => s.pushRecent);
  const wide = useMediaQuery('(min-width: 1280px)');

  const allOpenTrades = useTradeStore((s) => s.openTrades);
  const openTrades = useMemo(
    () => allOpenTrades.filter((trade) => accountOf(trade) === account),
    [allOpenTrades, account],
  );
  // The positions panel is opened from the ticket's Trades button, so the
  // ticket needs to say whether it is open and the aside needs to obey.
  const [tradesOpen, setTradesOpen] = useState(true);
  const setOpenTrades = useTradeStore((s) => s.setOpenTrades);

  /** Advanced workspace, wide screens only: one chart or two side by side (set in the top bar). */
  const layout = useWorkspaceStore((s) => s.chartLayout);
  const [secondId, setSecondId] = useState<string | null>(null);

  const assetsQuery = useQuery({
    queryKey: ['assets'],
    queryFn: api.assets.list,
    // The payload carries feed status and sentiment, both of which move fast.
    refetchInterval: 4000,
  });
  const openTradesQuery = useQuery({
    queryKey: ['trades', 'open'],
    queryFn: api.trades.open,
  });

  const assets = useMemo(() => assetsQuery.data ?? [], [assetsQuery.data]);
  const active = assets.find((asset) => asset.id === activeAssetId) ?? assets[0] ?? null;

  // The second chart shows another open tab. Never the active asset: that is
  // already the main chart, and subscribing to it twice would let closing one
  // chart unsubscribe the other's prices.
  const splitOn = mode === 'advanced' && wide && layout === 2;
  const secondChoices = assets.filter((asset) => openAssetIds.includes(asset.id) && asset.id !== active?.id);
  const second = splitOn ? (secondChoices.find((asset) => asset.id === secondId) ?? secondChoices[0] ?? null) : null;

  useMarketSubscription(active?.id);
  useMarketSubscription(second?.id);

  useEffect(() => {
    if (active) pushRecent(active.id);
  }, [active?.id, pushRecent]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (assets.length > 0) ensureOpen(assets.map((asset) => asset.id));
  }, [assets, ensureOpen]);

  useEffect(() => {
    if (assetsQuery.data) setFeeds(assetsQuery.data.map((asset) => asset.feed));
  }, [assetsQuery.data, setFeeds]);

  useEffect(() => {
    if (openTradesQuery.data) setOpenTrades(openTradesQuery.data);
  }, [openTradesQuery.data, setOpenTrades]);

  const precisions = useMemo(
    () => Object.fromEntries(assets.map((asset) => [asset.id, asset.pricePrecision])),
    [assets],
  );

  return (
    /*
     * Height is only fixed side by side (xl). Stacked, the page grows with its
     * content and the outlet scrolls: pinning it to the viewport let the chart
     * box shrink under its 280px chart, which then covered the ticket below.
     */
    <div className="flex flex-1 flex-col xl:min-h-0">
      {/* The same header in both modes, so switching mode moves nothing: the
          toggle stays under the pointer that pressed it and the chart keeps
          its place. Advanced adds its tools where they add no row - the asset
          tabs and the chart layout in the top bar, and the second chart's
          asset over that chart. */}
      <TradeAssetHeader asset={active} />

      <div className="flex flex-1 flex-col xl:min-h-0 xl:flex-row">
        <div className="flex min-w-0 flex-1 xl:min-h-0">
          <div className="min-h-[280px] min-w-0 flex-1">
            <PriceChart asset={active} openTrades={openTrades} mode={mode} />
          </div>
          {second ? (
            /* Read-only: orders are placed from the active asset's ticket.
               The label says which asset this is, since no tab points at it. */
            <div className="flex min-h-[280px] min-w-0 flex-1 flex-col border-l border-surface-800">
              <div className="flex shrink-0 items-center gap-2 border-b border-surface-800 bg-surface-900 px-3 py-1 text-[11px]">
                <select
                  aria-label="Second chart"
                  className="rounded-md border border-surface-700 bg-surface-850 px-2 py-0.5 text-[11px] font-bold text-content-100"
                  value={second.id}
                  onChange={(event) => setSecondId(event.target.value)}
                >
                  {secondChoices.map((asset) => (
                    <option key={asset.id} value={asset.id}>
                      {asset.symbol}
                    </option>
                  ))}
                </select>
                <span className="min-w-0 truncate text-content-400">
                  View only · trades are placed on {active?.symbol ?? 'the main chart'}
                </span>
              </div>
              <div className="min-h-0 flex-1">
                <PriceChart asset={second} openTrades={openTrades} mode={mode} />
              </div>
            </div>
          ) : null}
        </div>

        <aside className="flex w-full shrink-0 flex-col border-t border-surface-800 bg-surface-900 xl:w-[300px] xl:overflow-y-auto xl:border-l xl:border-t-0">
          <TradeTicket
            asset={active}
            mode={mode}
            tradesOpen={tradesOpen}
            onToggleTrades={() => setTradesOpen((open) => !open)}
            openCount={openTrades.length}
          />
          {tradesOpen ? (
            <div className="flex min-h-[200px] flex-1 flex-col border-t border-surface-800">
              <PositionsPanel precisions={precisions} />
            </div>
          ) : null}
        </aside>
      </div>

      <AssetPicker assets={assets} />
    </div>
  );
}
