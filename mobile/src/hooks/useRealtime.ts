import { useEffect } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { channels, socket, WsEvents } from '../websocket/client';
import { useAuthStore } from '../stores/authStore';
import { useMarketStore } from '../stores/marketStore';
import { useTradeStore } from '../stores/tradeStore';
import { useWalletStore } from '../stores/walletStore';
import { useSupportStore } from '../stores/supportStore';
import { useAccountStore } from '../stores/accountStore';
import { useFavouritesStore } from '../stores/favouritesStore';
import { api } from '../services/api';
import type { AccountType, SupportConversation, SupportMessage, Tick, Trade, Wallet } from '../types/api';
import { announceResult } from '../features/trade/announceResult';
import { USDT_KEY } from '../features/usdt/queries';

/** How often live prices reach the screen, at most. */
const TICK_FLUSH_MS = 250;

/** One cache entry for both balances; an `['wallet']` invalidation reaches it too. */
export const ACCOUNTS_KEY = ['wallet', 'accounts'] as const;

/**
 * Bridges the websocket into client state.
 *
 * Live events update the Zustand stores and patch the query cache directly, so
 * a settled trade or a new balance appears without a refetch. Mounted once, at
 * the app shell.
 */
export function useRealtime(): void {
  const status = useAuthStore((s) => s.status);
  const userId = useAuthStore((s) => s.user?.id);
  const queryClient = useQueryClient();

  useAccounts(status === 'authenticated' && !!userId);

  // The single-wallet store still has readers (the trade ticket among them).
  // Keeping it pointed at the ACTIVE account means a switch between demo and
  // live reaches them without each one having to learn about accounts.
  useEffect(() => {
    const sync = () => {
      const { wallets, active } = useAccountStore.getState();
      const wallet = wallets[active] ?? null;
      const current = useWalletStore.getState().wallet;
      if (wallet && current !== wallet) useWalletStore.getState().setWallet(wallet);
      if (!wallet && current) useWalletStore.getState().reset();
    };
    sync();
    return useAccountStore.subscribe(sync);
  }, []);

  useEffect(() => {
    if (status !== 'authenticated' || !userId) return;

    socket.connect();
    socket.subscribe([channels.user(userId)]);

    // Favourites are saved to the account. The local copy has already
    // rendered; this replaces it with the server's list once it arrives.
    void useFavouritesStore.getState().load();

    // Loaded up front so the navigation can show unread replies on any page.
    // Best effort: the support page loads it again if this fails.
    api.support
      .thread()
      .then((thread) => useSupportStore.getState().setThread(thread))
      .catch(() => undefined);

    /*
     * Prices arrive up to 20 times a second per asset (the feed ticks every
     * 50 ms). Applying each one re-renders the ticket, the header and the chart
     * that often, which a phone's JS thread cannot keep up with: taps then
     * queue behind the renders and land late, several at once. Only the newest
     * tick per asset is kept and the store is updated at most every
     * TICK_FLUSH_MS. Nothing here decides a price - the server fills and
     * settles every trade on its own ticks.
     */
    const pendingTicks = new Map<string, Tick>();
    let tickFlush: ReturnType<typeof setTimeout> | null = null;
    const flushTicks = () => {
      tickFlush = null;
      const batch = [...pendingTicks.values()];
      pendingTicks.clear();
      useMarketStore.getState().applyTicks(batch);
    };

    const unsubscribers = [
      socket.on(WsEvents.MARKET_TICK, (payload) => {
        const tick = payload as Tick;
        pendingTicks.set(tick.assetId, tick);
        if (tickFlush === null) tickFlush = setTimeout(flushTicks, TICK_FLUSH_MS);
      }),

      socket.on(WsEvents.WALLET_UPDATED, (payload) => {
        // Any balance change may be a USDT one (a transfer to trading, a
        // credited deposit); only the USDT screens on show refetch.
        void queryClient.invalidateQueries({ queryKey: USDT_KEY });
        const raw = payload as Wallet;
        // A USDT balance is not the INR Live or Demo balance of the top bar,
        // and must never be filed in its place.
        if (isUsdtWallet(raw)) return;
        // An event from a server that predates demo accounts carries no type;
        // it can only be the live wallet.
        const wallet: Wallet = { ...raw, accountType: raw.accountType ?? 'LIVE' };
        useAccountStore.getState().upsertWallet(wallet);
        queryClient.setQueryData<Wallet[]>(ACCOUNTS_KEY, (previous) =>
          previous ? mergeWallet(previous, wallet) : previous,
        );
      }),

      socket.on(WsEvents.USDT_UPDATED, () => {
        // The event says which record moved; refetching the USDT queries on
        // screen picks up the record, the lists and the balances together.
        void queryClient.invalidateQueries({ queryKey: USDT_KEY });
      }),

      socket.on(WsEvents.TRADE_CREATED, (payload) => {
        useTradeStore.getState().upsertTrade(payload as Trade);
        void queryClient.invalidateQueries({ queryKey: ['trades'] });
      }),

      socket.on(WsEvents.TRADE_UPDATED, (payload) => {
        useTradeStore.getState().upsertTrade(payload as Trade);
      }),

      socket.on(WsEvents.TRADE_SETTLED, (payload) => {
        const trade = payload as Trade;
        useTradeStore.getState().settleTrade(trade);
        announceResult(trade);
        queryClient.setQueryData(['trade', trade.id], trade);
        void queryClient.invalidateQueries({ queryKey: ['trades'] });
        void queryClient.invalidateQueries({ queryKey: ['wallet', 'transactions'] });
      }),

      socket.on(WsEvents.SUPPORT_MESSAGE, (payload) => {
        useSupportStore.getState().addMessage(payload as SupportMessage);
      }),

      socket.on(WsEvents.SUPPORT_CONVERSATION, (payload) => {
        useSupportStore.getState().setConversation(payload as SupportConversation);
      }),

      socket.on(WsEvents.SUPPORT_TYPING, (payload) => {
        const signal = payload as { side: string; name?: string };
        if (signal.side === 'AGENT') useSupportStore.getState().setAgentTyping(signal.name ?? 'Support');
      }),

      socket.on(WsEvents.NOTIFICATION_CREATED, () => {
        void queryClient.invalidateQueries({ queryKey: ['notifications'] });
      }),
    ];

    return () => {
      for (const off of unsubscribers) off();
      if (tickFlush !== null) clearTimeout(tickFlush);
      pendingTicks.clear();
      socket.disconnect();
      useSupportStore.getState().reset();
      // Balances belong to the session. Which account was in use (demo or
      // live) is a device preference and deliberately survives signing out.
      useAccountStore.getState().reset();
      useWalletStore.getState().reset();
    };
  }, [status, userId, queryClient]);
}

/** A wallet held in USDT - the USDT wallet or its trading balance, never an INR account. */
function isUsdtWallet(wallet: Pick<Wallet, 'currency'> | null | undefined): boolean {
  return wallet?.currency?.toUpperCase() === 'USDT';
}

function mergeWallet(list: Wallet[], wallet: Wallet): Wallet[] {
  const type: AccountType = wallet.accountType ?? 'LIVE';
  return [...list.filter((w) => (w.accountType ?? 'LIVE') !== type), wallet];
}

/**
 * Loads both balances into the account store.
 *
 * `/wallet/accounts` is new. While a server without it is running, the single
 * live wallet stands in so the header still has a number to show. If the
 * account this device last used does not exist on that server (demo, on an
 * older build), the switch moves to one that does rather than leaving every
 * balance on screen blank.
 */
function useAccounts(enabled: boolean): void {
  const query = useQuery({
    queryKey: ACCOUNTS_KEY,
    enabled,
    staleTime: 10_000,
    queryFn: async (): Promise<Wallet[]> => {
      try {
        const wallets = await api.wallet.accounts();
        // The INR Live and Demo accounts only: a USDT trading balance listed
        // here would otherwise take the Live slot in the top bar.
        return wallets.filter((w) => !isUsdtWallet(w)).map((w) => ({ ...w, accountType: w.accountType ?? 'LIVE' }));
      } catch {
        const live = await api.wallet.get('LIVE');
        return [{ ...live, accountType: live.accountType ?? 'LIVE' }];
      }
    },
  });

  useEffect(() => {
    const wallets = query.data;
    if (!wallets || wallets.length === 0) return;
    const store = useAccountStore.getState();
    store.setWallets(wallets);
    const types = wallets.map((w) => w.accountType ?? 'LIVE');
    if (!types.includes(store.active)) store.setActive(types[0]!);
  }, [query.data]);
}

/** Subscribes to one asset's tick channel for as long as it is selected. */
export function useMarketSubscription(assetId: string | null | undefined): void {
  useEffect(() => {
    if (!assetId) return;
    const channel = channels.market(assetId);
    socket.subscribe([channel]);
    return () => socket.unsubscribe([channel]);
  }, [assetId]);
}
