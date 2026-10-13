import { useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import clsx from 'clsx';
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  BookOpen,
  CandlestickChart,
  ChevronRight,
  History,
  LineChart,
  RotateCcw,
  type LucideIcon,
} from 'lucide-react';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { useAccountStore, useActiveWallet } from '../stores/accountStore';
import { useFavouritesStore } from '../stores/favouritesStore';
import { formatMoney } from '../utils/format';
import { AccountMarker, AccountSwitch } from '../components/AccountSwitch';
import { AssetRow, useOpenMarket } from '../components/AssetRow';
import { Panel, Skeleton, Spinner } from '../components/ui';
import { openFunds, useResetDemo } from '../features/account/funds';
import type { Asset } from '../types/api';

/**
 * Home (brief UX-01).
 *
 * In the brief's order: greeting, balance, deposit, favourites, recent
 * markets, quick actions, then short guides. Nothing else - no leaderboards,
 * promotions, social feed or "hot" markets. It is where a session starts, so
 * its job is to say where the money is and get the customer to the market
 * they want in one tap.
 *
 * A phone reads it top to bottom in that order. A wide screen puts the money
 * and the quick actions on the left and the markets on the right, so the page
 * uses the width instead of leaving a narrow column in the middle; the source
 * order, which is what a screen reader and a phone follow, stays the brief's.
 */
export function HomePage() {
  const user = useAuthStore((s) => s.user);
  const favourites = useFavouritesStore((s) => s.favourites);
  const recent = useFavouritesStore((s) => s.recent);
  const openMarket = useOpenMarket();

  // Same key and cadence as Markets and Trade: one cache entry, one poll.
  const assetsQuery = useQuery({
    queryKey: ['assets'],
    queryFn: api.assets.list,
    refetchInterval: 4000,
  });
  const byId = useMemo(() => new Map((assetsQuery.data ?? []).map((a) => [a.id, a])), [assetsQuery.data]);
  const pick = (ids: string[], limit: number) =>
    ids
      .map((id) => byId.get(id))
      .filter((a): a is Asset => !!a)
      .slice(0, limit);

  const favouriteAssets = pick(favourites, 6);
  const recentAssets = pick(recent, 5);
  const name = (user?.fullName?.trim().split(/\s+/)[0] || user?.username) ?? '';

  return (
    <div className="mx-auto max-w-3xl space-y-4 lg:max-w-6xl lg:space-y-6">
      <header className="pt-1">
        <h1 className="text-xl font-bold text-content-100">
          {greeting()}
          {name ? ', ' + name : ''}
        </h1>
        <p className="text-sm text-content-400">Your balance and markets at a glance.</p>
      </header>

      {/* Two rows on the left (balance, then quick actions) beside the markets
          on the right. The second row is 1fr, so a long favourites list grows
          that row rather than pushing the quick actions away from the balance. */}
      <div className="space-y-4 lg:grid lg:grid-cols-2 lg:grid-rows-[auto_1fr] lg:gap-6 lg:space-y-0 xl:grid-cols-[minmax(0,5fr)_minmax(0,7fr)]">
        <div className="lg:col-start-1 lg:row-start-1">
          <BalanceCard />
        </div>

        <div className="space-y-4 lg:col-start-2 lg:row-span-2 lg:row-start-1 lg:space-y-6">
          <section aria-labelledby="home-favourites">
            <SectionHeading id="home-favourites" title="Favourites" to="/markets" linkLabel="All markets" />
            <Panel className="overflow-hidden">
              {assetsQuery.isLoading ? (
                <RowsSkeleton />
              ) : favouriteAssets.length > 0 ? (
                <ul className="divide-y divide-surface-800/70">
                  {favouriteAssets.map((asset) => (
                    <li key={asset.id}>
                      <AssetRow asset={asset} onSelect={openMarket} />
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="flex flex-col items-center gap-2 px-6 py-8 text-center">
                  <p className="text-sm font-medium text-content-200">No favourites yet</p>
                  <p className="max-w-xs text-xs text-content-400">
                    Star the markets you follow in Markets and they will wait for you here.
                  </p>
                  <Link to="/markets" className="btn-ghost mt-1">
                    <LineChart className="h-4 w-4" />
                    Browse markets
                  </Link>
                </div>
              )}
            </Panel>
          </section>

          <section aria-labelledby="home-recent">
            <SectionHeading id="home-recent" title="Recent markets" />
            <Panel className="overflow-hidden">
              {assetsQuery.isLoading ? (
                <RowsSkeleton rows={2} />
              ) : recentAssets.length > 0 ? (
                <ul className="divide-y divide-surface-800/70">
                  {recentAssets.map((asset) => (
                    <li key={asset.id}>
                      <AssetRow asset={asset} onSelect={openMarket} />
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="px-4 py-5 text-center text-xs text-content-400">
                  Markets you open to trade will appear here.
                </p>
              )}
            </Panel>
          </section>
        </div>

        <section aria-labelledby="home-quick" className="lg:col-start-1 lg:row-start-2 lg:self-start">
          <SectionHeading id="home-quick" title="Quick actions" />
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-4 lg:grid-cols-2">
            <QuickAction to="/trade" icon={CandlestickChart} label="Trade" hint="Open the chart" />
            <QuickAction to="/markets" icon={LineChart} label="Markets" hint="Find a market" />
            <QuickAction to="/trades" icon={History} label="History" hint="Your trades" />
            <QuickAction to="/learn" icon={BookOpen} label="Learn" hint="How it works" />
          </div>
        </section>
      </div>

      <section aria-labelledby="home-learn">
        <SectionHeading id="home-learn" title="New here?" to="/learn" linkLabel="All guides" />
        <div className="grid gap-2 sm:grid-cols-3">
          <GuideCard
            to="/learn#how-it-works"
            title="How fixed-time trading works"
            text="Pick a market, a duration and an amount, then choose UP or DOWN."
          />
          <GuideCard
            to="/learn#max-loss"
            title="What you can lose"
            text="The most any trade can cost is the amount you invest in it."
          />
          <GuideCard
            to="/learn#demo-live"
            title="Demo and Live"
            text="Practise with virtual money first. Switch to Live when you are ready."
          />
        </div>
      </section>
    </div>
  );
}

/**
 * The active account's balance, with the switch beside it.
 *
 * Demo is drawn in the warning tone and says "virtual" in words; the colour
 * alone would not be enough for everyone.
 */
function BalanceCard() {
  const active = useAccountStore((s) => s.active);
  const wallet = useActiveWallet();
  const resetDemo = useResetDemo();

  return (
    <Panel className={clsx('p-4 sm:p-5', active === 'DEMO' && 'border-warn/40')}>
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-[11px] font-medium uppercase tracking-wider text-content-400">
              {active === 'DEMO' ? 'Demo balance' : 'Live balance'}
            </span>
            <AccountMarker account={active} />
          </div>
          <div className="mt-1 text-3xl font-bold tnum text-content-100">
            {wallet ? formatMoney(wallet.balance, wallet.currency) : <Skeleton className="h-9 w-40" />}
          </div>
          <dl className="mt-2 flex flex-wrap gap-x-5 gap-y-1 text-xs">
            <div className="flex gap-1.5">
              <dt className="text-content-400">Available</dt>
              <dd className="font-semibold tnum text-content-200">
                {wallet ? formatMoney(wallet.availableBalance, wallet.currency) : '--'}
              </dd>
            </div>
            <div className="flex gap-1.5">
              <dt className="text-content-400">In trades</dt>
              <dd className="font-semibold tnum text-content-200">
                {wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : '--'}
              </dd>
            </div>
          </dl>
        </div>

        {active === 'DEMO' ? (
          <button
            type="button"
            onClick={() => resetDemo.mutate()}
            disabled={resetDemo.isPending}
            className="btn-ghost px-3 py-1.5 text-xs"
          >
            {resetDemo.isPending ? <Spinner className="h-3.5 w-3.5" /> : <RotateCcw className="h-3.5 w-3.5" />}
            Reset demo
          </button>
        ) : null}
      </div>

      <AccountSwitch className="mt-4" />

      {/* Deposit comes straight after the balance (brief UX-01 order). Funds
          are real money, so these act on the Live account even from demo. */}
      <div className="mt-4 grid grid-cols-2 gap-2 border-t border-surface-800 pt-4">
        <button type="button" onClick={() => openFunds('DEPOSIT')} className="btn-primary">
          <ArrowDownToLine className="h-4 w-4" />
          Deposit
        </button>
        <button type="button" onClick={() => openFunds('WITHDRAWAL')} className="btn-ghost">
          <ArrowUpFromLine className="h-4 w-4" />
          Withdraw
        </button>
      </div>
      <p className="mt-2 text-[11px] text-content-400">
        {active === 'DEMO'
          ? 'Deposits and withdrawals go to your Live account. Your demo balance is virtual and cannot be paid out.'
          : 'Deposits are checked by our team before they are credited.'}
      </p>
      <Link
        to="/wallet/usdt"
        className="mt-2 inline-flex items-center gap-0.5 text-xs font-semibold text-accent hover:underline"
      >
        USDT wallet
        <ChevronRight className="h-3.5 w-3.5" />
      </Link>
    </Panel>
  );
}

function SectionHeading({ id, title, to, linkLabel }: { id: string; title: string; to?: string; linkLabel?: string }) {
  return (
    <div className="mb-2 flex items-center justify-between">
      <h2 id={id} className="text-sm font-bold text-content-100">
        {title}
      </h2>
      {to ? (
        <Link to={to} className="inline-flex items-center gap-0.5 text-xs font-semibold text-accent hover:underline">
          {linkLabel}
          <ChevronRight className="h-3.5 w-3.5" />
        </Link>
      ) : null}
    </div>
  );
}

function QuickAction({ to, icon: Icon, label, hint }: { to: string; icon: LucideIcon; label: string; hint: string }) {
  return (
    <Link
      to={to}
      className="panel flex items-center gap-3 px-3 py-3 transition hover:border-surface-600 hover:bg-surface-850"
    >
      <span className="grid h-9 w-9 shrink-0 place-items-center rounded-lg bg-surface-800 text-content-200">
        <Icon className="h-[18px] w-[18px]" />
      </span>
      <span className="min-w-0">
        <span className="block text-sm font-semibold text-content-100">{label}</span>
        <span className="block truncate text-[11px] text-content-400">{hint}</span>
      </span>
    </Link>
  );
}

function GuideCard({ to, title, text }: { to: string; title: string; text: string }) {
  return (
    <Link to={to} className="panel block p-4 transition hover:border-surface-600 hover:bg-surface-850">
      <span className="flex items-center gap-2 text-sm font-semibold text-content-100">
        <BookOpen className="h-4 w-4 shrink-0 text-accent" />
        {title}
      </span>
      <span className="mt-1 block text-xs leading-relaxed text-content-400">{text}</span>
    </Link>
  );
}

function RowsSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <div className="space-y-2 p-3">
      {Array.from({ length: rows }).map((_, index) => (
        <Skeleton key={index} className="h-11 w-full" />
      ))}
    </div>
  );
}

function greeting(): string {
  const hour = new Date().getHours();
  if (hour < 12) return 'Good morning';
  if (hour < 18) return 'Good afternoon';
  return 'Good evening';
}
