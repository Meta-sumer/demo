import { useEffect, useMemo, useState } from 'react';
import { Keyboard, KeyboardAvoidingView, Platform, View } from 'react-native';
import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import { useMarketStore } from '../../stores/marketStore';
import { useTradeStore } from '../../stores/tradeStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { useAccountStore } from '../../stores/accountStore';
import { useSettingsStore } from '../../stores/settingsStore';
import { useFavouritesStore } from '../../stores/favouritesStore';
import { useMarketSubscription } from '../../hooks/useRealtime';
import { cn } from '../../utils/cn';
import { Sheet } from '../../components/ui';
import { AssetPicker } from './AssetPicker';
import { AssetTabs } from './AssetTabs';
import { PriceChart } from './PriceChart';
import { TradeTicket } from './TradeTicket';
import { PositionsPanel } from './PositionsPanel';
import { TradeAssetHeader } from './TradeAssetHeader';
import { accountOf } from './ticketModel';

/**
 * The trading workspace.
 *
 *   asset header (and, in advanced mode, the asset tabs)
 *   chart
 *   ticket - duration, investment, contract information, UP/DOWN
 *
 * The brief's order - asset, chart, duration, investment, contract
 * information, UP/DOWN (brief section 31) - as one column that never scrolls:
 * the chart has pan and pinch of its own, and a page scrolling around it would
 * fight them. The chart takes whatever height the header and the ticket leave,
 * never less than 280; when that is not enough, the ticket's set-up part
 * scrolls inside the ticket instead. With the keyboard up (typing an amount)
 * the chart gives up its minimum, so the field stays in view.
 *
 * The positions panel the web keeps under the ticket is a sheet here, opened
 * from the ticket's Trades button. The web's second chart (two side by side,
 * advanced mode on a wide screen) needs a desktop's width and is not offered.
 *
 * Nothing here decides a price - the chart draws what the server sent, and the
 * ticket sends only asset, direction, amount, expiry and account.
 */
export function TradeScreen() {
  const activeAssetId = useWorkspaceStore((s) => s.activeAssetId);
  const ensureOpen = useWorkspaceStore((s) => s.ensureOpen);
  const setFeeds = useMarketStore((s) => s.setFeeds);
  const mode = useSettingsStore((s) => s.tradeMode);
  const account = useAccountStore((s) => s.active);
  const pushRecent = useFavouritesStore((s) => s.pushRecent);
  const keyboardUp = useKeyboardVisible();

  const allOpenTrades = useTradeStore((s) => s.openTrades);
  const openTrades = useMemo(
    () => allOpenTrades.filter((trade) => accountOf(trade) === account),
    [allOpenTrades, account],
  );
  // The positions sheet is opened from the ticket's Trades button, so the
  // ticket needs to say whether it is open and the sheet needs to obey.
  const [tradesOpen, setTradesOpen] = useState(false);
  const setOpenTrades = useTradeStore((s) => s.setOpenTrades);

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

  useMarketSubscription(active?.id);

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
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      className="flex-1 bg-surface-950"
    >
      {/* The same header in both modes, so switching mode moves nothing.
          Advanced adds the asset tabs on the header's second line, beside the
          mode toggle, where they add no row. */}
      <TradeAssetHeader asset={active} tabs={mode === 'advanced' ? <AssetTabs assets={assets} /> : undefined} />

      <View className={cn('min-w-0 flex-1', !keyboardUp && 'min-h-[280px]')}>
        <PriceChart asset={active} openTrades={openTrades} mode={mode} />
      </View>

      <TradeTicket
        asset={active}
        mode={mode}
        tradesOpen={tradesOpen}
        onToggleTrades={() => setTradesOpen((open) => !open)}
        openCount={openTrades.length}
      />

      <Sheet
        open={tradesOpen}
        onClose={() => setTradesOpen(false)}
        title="Trades"
        size="large"
        scroll={false}
      >
        <PositionsPanel precisions={precisions} onNavigate={() => setTradesOpen(false)} />
      </Sheet>

      <AssetPicker assets={assets} />
    </KeyboardAvoidingView>
  );
}

/** Whether the on-screen keyboard is up. */
function useKeyboardVisible(): boolean {
  const [visible, setVisible] = useState(false);
  useEffect(() => {
    const show = Keyboard.addListener(Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow', () =>
      setVisible(true),
    );
    const hide = Keyboard.addListener(Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide', () =>
      setVisible(false),
    );
    return () => {
      show.remove();
      hide.remove();
    };
  }, []);
  return visible;
}
