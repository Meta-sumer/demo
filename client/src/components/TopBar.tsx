import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom';
import { useCallback, useRef, useState } from 'react';
import clsx from 'clsx';
import { ArrowDownToLine, ArrowUpFromLine, Bell, ChevronDown, ChevronRight, LogOut, RotateCcw, User } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { useAccountStore, useActiveWallet } from '../stores/accountStore';
import { useSettingsStore } from '../stores/settingsStore';
import { useDismiss } from '../hooks/useDismiss';
import { formatMoney } from '../utils/format';
import type { ConnectionState } from '../websocket/client';
import { AssetTabs } from '../features/trade/AssetTabs';
import { openFunds, useResetDemo } from '../features/account/funds';
import { AccountMarker, AccountSwitch } from './AccountSwitch';
import { Spinner } from './ui';

/**
 * Top bar.
 *
 * The balance is the most-checked number on the platform, so it sits in its own
 * raised block next to the two actions that change it. Deposit is the only
 * filled button up here - everything else is secondary to it.
 *
 * The balance block always names the account it belongs to. Demo carries a
 * "DEMO · virtual" marker in the warning tone, so practice money can never be
 * mistaken for real money (brief UX-16); its menu switches between the two.
 *
 * There is no menu button: phones navigate with the bottom bar.
 */
export function TopBar({ connection }: { connection: ConnectionState }) {
  const navigate = useNavigate();
  const user = useAuthStore((s) => s.user);
  const logout = useAuthStore((s) => s.logout);
  const active = useAccountStore((s) => s.active);
  const wallet = useActiveWallet();
  const tradeMode = useSettingsStore((s) => s.tradeMode);
  const [menuOpen, setMenuOpen] = useState(false);
  const accountRef = useRef<HTMLDivElement>(null);
  const closeMenu = useCallback(() => setMenuOpen(false), []);
  useDismiss(menuOpen, accountRef, closeMenu);
  const resetDemo = useResetDemo();

  // Instrument tabs are an advanced-mode tool (brief section 27); beginners
  // see the one asset they are trading in the trade screen's own header.
  const onTrade = useLocation().pathname === '/trade';
  const showTabs = onTrade && tradeMode === 'advanced';

  // Same key and cadence as the workspace's own query, so the two share one
  // cache entry and one request rather than polling the endpoint twice.
  const assetsQuery = useQuery({
    queryKey: ['assets'],
    queryFn: api.assets.list,
    enabled: showTabs,
    refetchInterval: 4000,
  });
  const assets = assetsQuery.data ?? [];

  const notificationsQuery = useQuery({
    queryKey: ['notifications'],
    queryFn: () => api.notifications.list({ limit: 10 }),
    staleTime: 30_000,
  });
  const unread = (notificationsQuery.data?.items ?? []).filter((n) => !n.read).length;

  async function signOut() {
    await logout();
    navigate('/login', { replace: true });
  }

  return (
    <header className="relative flex h-14 shrink-0 items-center gap-2 border-b border-surface-800 bg-surface-900 px-3 sm:h-16 sm:gap-3 sm:px-4">
      <Link to="/home" className="flex shrink-0 items-center gap-2.5" aria-label="Home">
        <span className="grid h-9 w-9 place-items-center rounded-xl bg-accent text-[13px] font-black text-surface-950">
          BT
        </span>
        <span className="hidden text-base font-bold tracking-tight sm:block">BINARY</span>
      </Link>

      {/* The instruments live here on the trading screen: they are what the
          trader switches between, and a row of their own cost the chart 44px
          of height to say it. Matched exactly - /trades is the history page,
          not the workspace, and must not grow tabs. Not on a phone: there is
          no width for them next to the balance. */}
      {showTabs ? (
        <div className="hidden min-w-0 flex-1 md:flex">
          <AssetTabs assets={assets} />
        </div>
      ) : null}

      <div className="ml-auto flex min-w-0 shrink-0 items-center gap-1.5 sm:gap-3">
        <span
          className={clsx(
            'hidden h-2 w-2 rounded-full sm:block',
            connection === 'open' ? 'bg-up' : connection === 'connecting' ? 'bg-warn' : 'bg-down',
          )}
          title={connection === 'open' ? 'Live data connected' : 'Reconnecting'}
        />

        {/* The menu lives with the button that opens it, rather than beside
            it in the bar. That makes one subtree the whole control, which is
            what lets an outside click be recognised as outside at all, and it
            anchors the panel to its own trigger instead of to a header height
            it would have to be kept in step with. */}
        <div ref={accountRef} className="relative">
          <button
            type="button"
            onClick={() => setMenuOpen((open) => !open)}
            className={clsx(
              'flex items-center gap-2 rounded-lg border px-2.5 py-1 text-left sm:px-3 sm:py-1.5',
              active === 'DEMO' ? 'border-warn/40 bg-warn/5' : 'border-surface-700 bg-surface-850',
            )}
            aria-expanded={menuOpen}
            aria-haspopup="dialog"
            aria-label={
              (active === 'DEMO' ? 'Demo account, virtual balance ' : 'Live account, balance ') +
              (wallet ? formatMoney(wallet.balance, wallet.currency) : 'loading')
            }
          >
            <span className="min-w-0">
              <AccountMarker account={active} />
              <span className="block text-sm font-bold tnum text-content-100">
                {wallet ? formatMoney(wallet.balance, wallet.currency) : '--'}
              </span>
            </span>
            <ChevronDown className={clsx('h-4 w-4 text-content-400 transition', menuOpen && 'rotate-180')} />
          </button>

          {menuOpen ? (
            <div
              role="dialog"
              aria-label="Accounts"
              className="absolute right-0 top-full z-40 mt-2 w-[min(300px,calc(100vw-1.5rem))] rounded-xl border border-surface-700 bg-surface-900 p-2 shadow-overlay"
            >
              <div className="px-2 pb-2 pt-1">
                <div className="text-[11px] text-content-400">Signed in as</div>
                <div className="truncate text-sm font-medium text-content-100">{user?.email}</div>
              </div>

              <AccountSwitch className="px-1" />

              <div className="mt-2 space-y-0.5 border-t border-surface-800 pt-2">
                <Row
                  label="Available"
                  value={wallet ? formatMoney(wallet.availableBalance, wallet.currency) : '--'}
                />
                <Row
                  label="In open trades"
                  value={wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : '--'}
                />
              </div>

              {active === 'DEMO' ? (
                <button
                  type="button"
                  onClick={() => resetDemo.mutate()}
                  disabled={resetDemo.isPending}
                  className="mt-1 flex w-full items-center gap-2 rounded-lg px-3 py-2 text-xs font-semibold text-content-300 transition hover:bg-surface-850 hover:text-content-100"
                >
                  {resetDemo.isPending ? <Spinner className="h-3.5 w-3.5" /> : <RotateCcw className="h-3.5 w-3.5" />}
                  Reset demo balance
                </button>
              ) : null}

              {/* Real money only. On demo these switch to Live first and say
                  so, rather than refusing. */}
              <div className="mt-2 border-t border-surface-800 pt-2">
                <div className="grid grid-cols-2 gap-1.5">
                  <button
                    type="button"
                    onClick={() => {
                      setMenuOpen(false);
                      openFunds('DEPOSIT');
                    }}
                    className="btn justify-center bg-accent-solid px-2 py-2 text-xs font-bold text-surface-950 hover:brightness-110"
                  >
                    <ArrowDownToLine className="h-4 w-4" />
                    Deposit
                  </button>
                  <button
                    type="button"
                    onClick={() => {
                      setMenuOpen(false);
                      openFunds('WITHDRAWAL');
                    }}
                    className="btn justify-center bg-surface-700 px-2 py-2 text-xs font-semibold text-content-100 hover:brightness-110"
                  >
                    <ArrowUpFromLine className="h-4 w-4" />
                    Withdraw
                  </button>
                </div>
                <p className="mt-1.5 px-1 text-[10px] leading-snug text-content-400">
                  Deposits and withdrawals use your Live account.
                </p>
                {/* USDT is held apart from both INR accounts, so it is a link
                    rather than a third balance in this menu. */}
                <Link
                  to="/wallet/usdt"
                  onClick={() => setMenuOpen(false)}
                  className="mt-1 flex items-center justify-between rounded-lg px-1 py-1 text-[11px] font-semibold text-accent hover:underline"
                >
                  USDT wallet
                  <ChevronRight className="h-3.5 w-3.5" />
                </Link>
              </div>

              <div className="mt-2 flex gap-1 border-t border-surface-800 pt-2">
                <Link
                  to="/account"
                  onClick={() => setMenuOpen(false)}
                  className="flex flex-1 items-center gap-2 rounded-lg px-3 py-2 text-xs font-semibold text-content-300 hover:bg-surface-850 hover:text-content-100"
                >
                  <User className="h-3.5 w-3.5" />
                  Account
                </Link>
                <button
                  type="button"
                  onClick={signOut}
                  className="flex flex-1 items-center gap-2 rounded-lg px-3 py-2 text-xs font-semibold text-content-300 hover:bg-surface-850 hover:text-down"
                >
                  <LogOut className="h-3.5 w-3.5" />
                  Sign out
                </button>
              </div>
            </div>
          ) : null}
        </div>

        {/* Over the workspace rather than away from it - funding an account
            should not cost you sight of an open position. */}
        <button
          type="button"
          onClick={() => openFunds('DEPOSIT')}
          className="btn hidden bg-accent-solid px-3 py-2 font-bold text-surface-950 hover:brightness-110 sm:inline-flex"
        >
          <ArrowDownToLine className="h-4 w-4" />
          Deposit
        </button>
        <button
          type="button"
          onClick={() => openFunds('WITHDRAWAL')}
          className="btn hidden bg-surface-700 px-3 py-2 font-semibold text-content-100 hover:brightness-110 xl:inline-flex"
        >
          <ArrowUpFromLine className="h-4 w-4" />
          Withdraw
        </button>

        <NavLink
          to="/account#notifications"
          className="relative rounded-lg p-2 text-content-300 hover:bg-surface-850"
          aria-label={unread > 0 ? unread + ' unread notifications' : 'Notifications'}
        >
          <Bell className="h-5 w-5" />
          {unread > 0 ? (
            <span className="absolute right-0.5 top-0.5 grid h-4 min-w-4 place-items-center rounded-full bg-down px-1 text-[10px] font-bold text-white">
              {unread > 9 ? '9+' : unread}
            </span>
          ) : null}
        </NavLink>

        {/* On a phone sign-out is in the balance menu and in Account; the bar
            has no width to spare for it. */}
        <button
          onClick={signOut}
          className="hidden rounded-lg p-2 text-content-300 hover:bg-surface-850 hover:text-down sm:block"
          title={'Sign out ' + (user?.username ?? '')}
          aria-label="Sign out"
        >
          <LogOut className="h-5 w-5" />
        </button>
      </div>
    </header>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between px-3 py-1.5 text-xs text-content-300">
      {label}
      <span className="font-semibold tnum text-content-100">{value}</span>
    </div>
  );
}
