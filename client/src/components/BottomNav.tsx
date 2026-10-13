import { Link, useLocation } from 'react-router-dom';
import clsx from 'clsx';
import { BookOpen, CandlestickChart, History, House, LineChart, User, type LucideIcon } from 'lucide-react';
import { useSupportStore } from '../stores/supportStore';
import { useActiveTradeCount } from '../features/trade/useActiveTrades';

/**
 * Primary destinations (brief section 5).
 *
 * Phone: Home · Markets · Trade · Trades · Account, nothing else. Desktop adds
 * Learn, which a phone reaches from Home instead of spending a fifth of the
 * bar on it. Wallet, deposits, settings and support are all inside Account,
 * so the Account item stays lit on those screens - the bar should always say
 * where you are.
 */
export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** Extra paths that belong to this destination. */
  also?: string[];
  desktopOnly?: boolean;
}

export const NAV_ITEMS: NavItem[] = [
  { to: '/home', label: 'Home', icon: House },
  { to: '/markets', label: 'Markets', icon: LineChart },
  { to: '/trade', label: 'Trade', icon: CandlestickChart },
  { to: '/trades', label: 'Trades', icon: History },
  { to: '/learn', label: 'Learn', icon: BookOpen, desktopOnly: true, also: ['/onboarding'] },
  {
    to: '/account',
    label: 'Account',
    icon: User,
    also: ['/wallet', '/deposit', '/withdraw', '/settings', '/profile', '/support'],
  },
];

/** Whether `item` is the destination the current path belongs to. */
export function isNavActive(item: NavItem, pathname: string): boolean {
  const matches = (base: string) => pathname === base || pathname.startsWith(base + '/');
  return matches(item.to) || (item.also ?? []).some(matches);
}

/** Replies waiting in the support chat. Support lives in Account, so the badge does too. */
export function useSupportUnread(): number {
  return useSupportStore((s) => s.conversation?.unreadByUser ?? 0);
}

export function UnreadDot({ count, className }: { count: number; className?: string }) {
  if (count <= 0) return null;
  return (
    <span
      className={clsx(
        'absolute grid h-4 min-w-4 place-items-center rounded-full bg-down px-1 text-[9px] font-bold leading-none text-white',
        className,
      )}
      aria-label={count + ' unread support ' + (count === 1 ? 'reply' : 'replies')}
    >
      {count > 9 ? '9+' : count}
    </span>
  );
}

/**
 * Running trades on the account being traded, on the Trades destination
 * (reference record 15 - Deriv's "Positions 1").
 *
 * In the colour the platform uses for live orders, not the red of an unread
 * reply: a running trade is not something waiting to be dealt with. The ring
 * in the bar's own colour keeps it readable on the highlighted item, where the
 * highlight can be the same yellow. The number is for the eye; the link's
 * name carries the words (see `ActiveTradesLabel`).
 */
export function ActiveTradesBadge({ count, className }: { count: number; className?: string }) {
  if (count <= 0) return null;
  return (
    <span
      aria-hidden
      className={clsx(
        'absolute grid h-4 min-w-4 place-items-center rounded-full bg-brand px-1 text-[9px] font-black leading-none',
        'text-surface-950 ring-2 ring-surface-900 tnum',
        className,
      )}
    >
      {count > 9 ? '9+' : count}
    </span>
  );
}

/** ", 2 active" after the item's label, for screen readers. */
export function ActiveTradesLabel({ count }: { count: number }) {
  if (count <= 0) return null;
  return <span className="sr-only">{', ' + count + ' active'}</span>;
}

/**
 * Phone navigation.
 *
 * A flex child at the foot of the shell rather than `position: fixed`: the
 * outlet above it ends where the bar begins, so no page needs to know the
 * bar's height to keep its last row visible. The safe-area padding keeps the
 * labels clear of the home indicator.
 */
export function BottomNav() {
  const { pathname } = useLocation();
  const unread = useSupportUnread();
  const activeTrades = useActiveTradeCount();

  return (
    <nav
      aria-label="Primary"
      className="shrink-0 border-t border-surface-800 bg-surface-900 pb-[env(safe-area-inset-bottom)] lg:hidden"
    >
      <ul className="grid grid-cols-5">
        {NAV_ITEMS.filter((item) => !item.desktopOnly).map((item) => {
          const active = isNavActive(item, pathname);
          return (
            <li key={item.to}>
              <Link
                to={item.to}
                aria-current={active ? 'page' : undefined}
                className={clsx(
                  'flex h-14 flex-col items-center justify-center gap-1 text-[10px] font-semibold transition',
                  active ? 'text-content-100' : 'text-content-400 hover:text-content-200',
                )}
              >
                <span
                  className={clsx(
                    'relative grid h-7 w-12 place-items-center rounded-full transition',
                    active && 'bg-accent text-surface-950',
                  )}
                >
                  <item.icon className="h-[18px] w-[18px]" />
                  {item.to === '/account' ? <UnreadDot count={unread} className="-right-0.5 -top-1" /> : null}
                  {item.to === '/trades' ? <ActiveTradesBadge count={activeTrades} className="-right-0.5 -top-1" /> : null}
                </span>
                {item.label}
                {item.to === '/trades' ? <ActiveTradesLabel count={activeTrades} /> : null}
              </Link>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
