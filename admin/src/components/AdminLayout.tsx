import { NavLink, Outlet, useNavigate } from 'react-router-dom';
import { useEffect, useState } from 'react';
import clsx from 'clsx';
import {
  Activity,
  ArrowDownToLine,
  ArrowUpFromLine,
  BarChart3,
  CandlestickChart,
  CircleDollarSign,
  Coins,
  FileClock,
  Gauge,
  Headset,
  Landmark,
  LayoutDashboard,
  LogOut,
  Menu,
  Network,
  Percent,
  Scale,
  ScrollText,
  Settings,
  Shield,
  Users,
  Wallet,
  Webhook,
  X,
} from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { useAuthStore } from '../stores/authStore';
import { api } from '../services/api';
import { socket, type ConnectionState } from '../websocket/client';
import { useAdminRealtime } from '../hooks/useAdminRealtime';
import { useUsdtRefetchInterval } from '../hooks/useUsdtLive';
import { Badge, Toaster } from './ui';
import { ThemeToggle } from './ThemeToggle';

/**
 * Console shell (spec section 19).
 *
 * Navigation is grouped by responsibility and each entry declares the
 * permission it needs, so a FINANCE user simply does not see the risk section.
 * The server enforces the same permissions on every request.
 */
interface NavItem {
  to: string;
  label: string;
  icon: typeof LayoutDashboard;
  permission?: string;
  /** Active only on the exact path, not on its children. */
  end?: boolean;
}

const NAV_GROUPS: Array<{ title: string; items: NavItem[] }> = [
  {
    title: 'Overview',
    items: [{ to: '/admin/dashboard', label: 'Dashboard', icon: LayoutDashboard }],
  },
  {
    title: 'Operations',
    items: [
      { to: '/admin/users', label: 'Users', icon: Users, permission: 'users:read' },
      { to: '/admin/binary/trades', label: 'Trades', icon: CandlestickChart, permission: 'trades:read' },
      { to: '/admin/assets', label: 'Assets', icon: Coins, permission: 'assets:read' },
      { to: '/admin/payouts', label: 'Payouts', icon: Percent, permission: 'payouts:read' },
      { to: '/admin/market-data', label: 'Market data', icon: Activity, permission: 'market-data:read' },
      { to: '/admin/support', label: 'Support', icon: Headset, permission: 'support:read' },
    ],
  },
  {
    title: 'Risk',
    items: [
      { to: '/admin/exposure', label: 'Exposure', icon: Scale, permission: 'exposure:read' },
      { to: '/admin/risk', label: 'Risk engine', icon: Gauge, permission: 'risk:read' },
      { to: '/admin/hedging', label: 'Hedging', icon: Shield, permission: 'hedge:read' },
    ],
  },
  {
    title: 'Finance',
    items: [
      { to: '/admin/wallets', label: 'Wallets', icon: Wallet, permission: 'wallets:read' },
      { to: '/admin/deposits', label: 'Deposits', icon: ArrowDownToLine, permission: 'deposits:read' },
      { to: '/admin/withdrawals', label: 'Withdrawals', icon: ArrowUpFromLine, permission: 'withdrawals:read' },
      { to: '/admin/payment-methods', label: 'Payment methods', icon: Landmark, permission: 'deposits:read' },
      { to: '/admin/reports', label: 'Reports', icon: BarChart3, permission: 'reports:read' },
    ],
  },
  {
    title: 'USDT',
    items: [
      { to: '/admin/usdt', label: 'Dashboard', icon: CircleDollarSign, permission: 'deposits:read', end: true },
      { to: '/admin/usdt/deposits', label: 'Deposits', icon: ArrowDownToLine, permission: 'deposits:read' },
      { to: '/admin/usdt/withdrawals', label: 'Withdrawals', icon: ArrowUpFromLine, permission: 'withdrawals:read' },
      { to: '/admin/usdt/webhook-events', label: 'Webhook events', icon: Webhook, permission: 'deposits:read' },
      { to: '/admin/usdt/networks', label: 'Networks', icon: Network, permission: 'deposits:read' },
    ],
  },
  {
    title: 'Governance',
    items: [
      { to: '/admin/reconciliation', label: 'Reconciliation', icon: FileClock, permission: 'reconciliation:read' },
      { to: '/admin/audit-logs', label: 'Audit log', icon: ScrollText, permission: 'audit:read' },
      { to: '/admin/settings', label: 'Settings', icon: Settings, permission: 'risk:read' },
    ],
  },
];

export function AdminLayout() {
  useAdminRealtime();
  const navigate = useNavigate();
  const user = useAuthStore((s) => s.user);
  const can = useAuthStore((s) => s.can);
  const logout = useAuthStore((s) => s.logout);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [connection, setConnection] = useState<ConnectionState>(socket.state);

  useEffect(() => socket.onStateChange(setConnection), []);

  // Unread customer messages, kept live by the realtime hook invalidating ['support'].
  const canSupport = can('support:read');
  const supportUnread = useQuery({
    queryKey: ['support', 'badge'],
    queryFn: () => api.support.list({ limit: 1 }),
    enabled: canSupport,
    refetchInterval: 60_000,
  }).data?.unreadTotal ?? 0;

  // USDT withdrawals waiting for an operator; the dashboard shares this query.
  const usdtInterval = useUsdtRefetchInterval();
  const usdtUnderReview = useQuery({
    queryKey: ['usdt', 'badge', 'pending-review'],
    queryFn: () => api.usdt.withdrawals({ status: 'pending_review', limit: 1 }),
    enabled: can('withdrawals:read'),
    refetchInterval: usdtInterval,
  }).data?.total ?? 0;

  const groups = NAV_GROUPS.map((group) => ({
    ...group,
    items: group.items.filter((item) => !item.permission || can(item.permission)),
  })).filter((group) => group.items.length > 0);

  return (
    <div className="flex min-h-screen bg-surface-950">
      <aside
        className={clsx(
          'fixed inset-y-0 left-0 z-40 w-60 shrink-0 border-r border-surface-800 bg-surface-900 transition-transform lg:static lg:translate-x-0',
          sidebarOpen ? 'translate-x-0' : '-translate-x-full',
        )}
      >
        <div className="flex h-14 items-center gap-2 border-b border-surface-800 px-4">
          <span className="grid h-7 w-7 place-items-center rounded-lg bg-accent text-[11px] font-black text-white">
            BT
          </span>
          <span className="text-sm font-semibold tracking-tight">Admin Console</span>
          <button
            className="ml-auto rounded-lg p-1.5 text-content-400 hover:bg-surface-800 lg:hidden"
            onClick={() => setSidebarOpen(false)}
            aria-label="Close navigation"
          >
            <X className="h-4 w-4" />
          </button>
        </div>

        <nav className="space-y-5 overflow-y-auto px-3 py-4" style={{ maxHeight: 'calc(100vh - 3.5rem)' }}>
          {groups.map((group) => (
            <div key={group.title}>
              <div className="mb-1.5 px-2 text-[10px] font-semibold uppercase tracking-widest text-content-400">
                {group.title}
              </div>
              <div className="space-y-0.5">
                {group.items.map((item) => (
                  <NavLink
                    key={item.to}
                    to={item.to}
                    end={item.end}
                    onClick={() => setSidebarOpen(false)}
                    className={({ isActive }) =>
                      clsx(
                        'flex items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm font-medium transition',
                        isActive
                          ? 'bg-accent/15 text-content-100 ring-1 ring-accent/30'
                          : 'text-content-300 hover:bg-surface-850 hover:text-content-100',
                      )
                    }
                  >
                    <item.icon className="h-4 w-4 shrink-0" />
                    <span className="truncate">{item.label}</span>
                    {item.to === '/admin/support' && supportUnread > 0 ? (
                      <span className="ml-auto rounded-full bg-down px-1.5 py-0.5 text-[10px] font-bold leading-none text-white">
                        {supportUnread > 99 ? '99+' : supportUnread}
                      </span>
                    ) : null}
                    {item.to === '/admin/usdt/withdrawals' && usdtUnderReview > 0 ? (
                      <span
                        className="ml-auto rounded-full bg-warn/15 px-1.5 py-0.5 text-[10px] font-bold leading-none text-warn ring-1 ring-warn/40"
                        title={usdtUnderReview + ' under review'}
                      >
                        {usdtUnderReview > 99 ? '99+' : usdtUnderReview}
                        <span className="sr-only"> under review</span>
                      </span>
                    ) : null}
                  </NavLink>
                ))}
              </div>
            </div>
          ))}
        </nav>
      </aside>

      {sidebarOpen ? (
        <button
          className="fixed inset-0 z-30 bg-scrim/70 backdrop-blur-sm lg:hidden"
          onClick={() => setSidebarOpen(false)}
          aria-label="Close navigation overlay"
        />
      ) : null}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-3 border-b border-surface-800 bg-surface-900/90 px-4 backdrop-blur">
          <button
            className="rounded-lg p-2 text-content-300 hover:bg-surface-800 lg:hidden"
            onClick={() => setSidebarOpen(true)}
            aria-label="Open navigation"
          >
            <Menu className="h-5 w-5" />
          </button>

          <Badge tone={connection === 'open' ? 'up' : connection === 'connecting' ? 'warn' : 'down'}>
            <span
              className={clsx(
                'h-1.5 w-1.5 rounded-full',
                connection === 'open' ? 'bg-up' : connection === 'connecting' ? 'bg-warn' : 'bg-down',
              )}
            />
            {connection === 'open' ? 'Live' : connection}
          </Badge>

          <div className="ml-auto flex items-center gap-3">
            <ThemeToggle />

            <div className="text-right">
              <div className="text-sm font-medium text-content-100">{user?.fullName ?? user?.username}</div>
              <div className="text-[11px] text-content-400">{user?.role}</div>
            </div>
            <button
              onClick={async () => {
                await logout();
                navigate('/admin/login', { replace: true });
              }}
              className="rounded-lg p-2 text-content-300 hover:bg-surface-800 hover:text-down"
              aria-label="Sign out"
            >
              <LogOut className="h-5 w-5" />
            </button>
          </div>
        </header>

        <main className="min-w-0 flex-1 p-4">
          <Outlet />
        </main>
      </div>

      <Toaster />
    </div>
  );
}
