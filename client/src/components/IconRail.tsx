import { Link, useLocation } from 'react-router-dom';
import clsx from 'clsx';
import { Facebook, Instagram, Settings, Volume2, VolumeX } from 'lucide-react';
import { useSettingsStore } from '../stores/settingsStore';
import { useWorkspaceStore } from '../stores/workspaceStore';
import { useActiveTradeCount } from '../features/trade/useActiveTrades';
import {
  ActiveTradesBadge,
  ActiveTradesLabel,
  NAV_ITEMS,
  UnreadDot,
  isNavActive,
  useSupportUnread,
} from './BottomNav';

/**
 * Primary navigation rail.
 *
 * Icon-over-label, always visible: a trader moves between the chart and their
 * account constantly, and a collapsed hamburger would cost a click every time.
 * Utility controls sit at the bottom, away from the destinations.
 *
 * Kept to 60px. Every pixel here is taken from the chart, which is the one
 * thing on this screen that benefits from being wider.
 */
/*
 * Home · Markets · Trade · Trades · Learn · Account - the same list as the
 * phone's bottom bar plus Learn (brief section 5). Support is inside Account
 * now; its unread badge rides on the Account item so a reply is never missed.
 * Trades carries the number of trades running on the active account.
 */
export function IconRail() {
  // Replies waiting in the support chat, shown on the rail from any page.
  const supportUnread = useSupportUnread();
  const activeTrades = useActiveTradeCount();
  const { pathname } = useLocation();
  const soundEnabled = useSettingsStore((s) => s.soundEnabled);
  const toggleSound = useSettingsStore((s) => s.toggleSound);
  const settingsOpen = useWorkspaceStore((s) => s.settingsOpen);
  const toggleSettings = useWorkspaceStore((s) => s.toggleSettings);

  return (
    <nav
      aria-label="Primary"
      className="hidden w-[60px] shrink-0 flex-col border-r border-surface-800 bg-surface-900 lg:flex"
    >
      <div className="flex flex-1 flex-col gap-0.5 px-1.5 py-2">
        {NAV_ITEMS.map((item) => (
          <Link
            key={item.to}
            to={item.to}
            aria-current={isNavActive(item, pathname) ? 'page' : undefined}
            className={clsx(
                'flex flex-col items-center gap-1 rounded-lg px-0.5 py-2 text-[9px] font-semibold uppercase leading-none transition',
                isNavActive(item, pathname)
                  ? 'bg-accent text-surface-950 shadow-raised'
                  : 'text-content-400 hover:bg-surface-850 hover:text-content-200',
              )}
          >
            <span className="relative">
              <item.icon className="h-[18px] w-[18px]" />
              {item.to === '/account' ? <UnreadDot count={supportUnread} className="-right-2 -top-1.5" /> : null}
              {item.to === '/trades' ? (
                <ActiveTradesBadge count={activeTrades} className="-right-2.5 -top-1.5" />
              ) : null}
            </span>
            <span>{item.label}</span>
            {item.to === '/trades' ? <ActiveTradesLabel count={activeTrades} /> : null}
          </Link>
        ))}
      </div>

      <div className="flex flex-col items-center gap-0.5 border-t border-surface-800 px-1.5 py-2">
        {/* The same button closes it again - a panel opened from here should
            not need a second control to dismiss. */}
        <button
          type="button"
          onClick={toggleSettings}
          aria-label={settingsOpen ? 'Close settings' : 'Open settings'}
          aria-expanded={settingsOpen}
          title="Settings"
          className={clsx(
            'rounded-lg p-1.5 transition',
            settingsOpen ? 'bg-accent text-surface-950' : 'text-content-400 hover:bg-surface-850 hover:text-content-200',
          )}
        >
          <Settings
            className={clsx('h-[18px] w-[18px] transition-transform duration-300', settingsOpen && 'rotate-90')}
          />
        </button>

        <button
          type="button"
          onClick={toggleSound}
          className="rounded-lg p-1.5 text-content-400 transition hover:bg-surface-850 hover:text-content-200"
          aria-label={soundEnabled ? 'Mute sounds' : 'Unmute sounds'}
          aria-pressed={soundEnabled}
          title={soundEnabled ? 'Sound on' : 'Sound off'}
        >
          {soundEnabled ? <Volume2 className="h-[18px] w-[18px]" /> : <VolumeX className="h-[18px] w-[18px]" />}
        </button>

        <div className="mt-0.5 flex gap-0.5 text-content-400">
          <span className="rounded-md p-1" title="Facebook">
            <Facebook className="h-3 w-3" />
          </span>
          <span className="rounded-md p-1" title="Instagram">
            <Instagram className="h-3 w-3" />
          </span>
        </div>
      </div>
    </nav>
  );
}
