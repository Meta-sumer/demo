import { Outlet } from 'react-router-dom';
import { useEffect, useState } from 'react';
import { socket, type ConnectionState } from '../websocket/client';
import { useRealtime } from '../hooks/useRealtime';
import { IconRail } from './IconRail';
import { TopBar } from './TopBar';
import { BottomNav } from './BottomNav';
import { SettingsDrawer } from './SettingsDrawer';
import { TransferModal } from '../features/transfer/TransferModal';
import { Toaster } from './ui';
import { TradeResultToasts } from '../features/trade/TradeResultToasts';

/**
 * Application shell.
 *
 * The bars are fixed; only the outlet scrolls. That matters on the trading
 * screen, where the chart has to keep its height rather than being pushed
 * around by whatever is below it.
 *
 *   top bar (full width)
 *   rail | settings | outlet          <- wide screens
 *   outlet                            <- phones
 *   bottom navigation                 <- phones
 *
 * Settings is a sibling of the rail rather than an overlay, so opening it
 * narrows the workspace instead of hiding it - a trader adjusting the chart
 * needs to see the chart react.
 *
 * The bottom navigation is the last flex row rather than a fixed layer, so
 * the outlet already ends where it begins and nothing can scroll under it.
 * `100dvh` rather than `100vh`: on a phone the latter is the height with the
 * browser's toolbar hidden, which would push the navigation off the screen.
 *
 * Balances are loaded by `useRealtime` (both accounts, into the account
 * store), so no screen fetches its own copy.
 */
export function Layout() {
  useRealtime();
  const [connection, setConnection] = useState<ConnectionState>(socket.state);

  useEffect(() => socket.onStateChange(setConnection), []);

  return (
    <div className="flex h-[100dvh] flex-col overflow-hidden bg-surface-950">
      <TopBar connection={connection} />

      <div className="flex min-h-0 min-w-0 flex-1">
        <IconRail />
        <SettingsDrawer />

        <main className="flex min-h-0 min-w-0 flex-1 flex-col overflow-y-auto overflow-x-hidden">
          <Outlet />
        </main>
      </div>

      <BottomNav />

      <TransferModal />
      <TradeResultToasts />
      <Toaster />
    </div>
  );
}
