import { useMemo, type ComponentType } from 'react';
import { Pressable, View } from 'react-native';
import { router } from 'expo-router';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  BookOpen,
  CandlestickChart,
  ChevronRight,
  History,
  LineChart,
  RotateCcw,
} from 'lucide-react-native';
import { api } from '../../services/api';
import { useAuthStore } from '../../stores/authStore';
import { useAccountStore, useActiveWallet } from '../../stores/accountStore';
import { useFavouritesStore } from '../../stores/favouritesStore';
import { formatMoney } from '../../utils/format';
import { cn } from '../../utils/cn';
import { AccountMarker, AccountSwitch } from '../../components/AccountSwitch';
import { AssetRow, useOpenMarket } from '../../components/AssetRow';
import { Screen } from '../../components/Screen';
import { Button, Icon, Panel, Skeleton, Text, type IconGlyphProps } from '../../components/ui';
import { openFunds, useResetDemo } from '../account/funds';
import { usePullRefresh } from '../../hooks/usePullRefresh';
import { useScreenFocused } from '../../hooks/useScreenFocused';
import type { Asset } from '../../types/api';

/**
 * Home (brief UX-01).
 *
 * In the brief's order: greeting, balance, deposit, favourites, recent
 * markets, quick actions, then short guides. Nothing else - no leaderboards,
 * promotions, social feed or "hot" markets. It is where a session starts, so
 * its job is to say where the money is and get the customer to the market
 * they want in one tap.
 *
 * A phone reads it top to bottom in that order, which is also the order a
 * screen reader follows.
 */
export function HomeScreen() {
  const user = useAuthStore((s) => s.user);
  const favourites = useFavouritesStore((s) => s.favourites);
  const recent = useFavouritesStore((s) => s.recent);
  const openMarket = useOpenMarket();
  const focused = useScreenFocused();
  const queryClient = useQueryClient();

  // Same key and cadence as Markets and Trade: one cache entry, one poll.
  // Paused while another tab is in front - tab screens stay mounted.
  const assetsQuery = useQuery({
    queryKey: ['assets'],
    queryFn: api.assets.list,
    refetchInterval: focused ? 4000 : false,
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

  // The balances come from the accounts query the shell runs; refreshing it
  // puts both straight back into the account store.
  const { refreshing, onRefresh } = usePullRefresh(() =>
    Promise.all([assetsQuery.refetch(), queryClient.invalidateQueries({ queryKey: ['wallet', 'accounts'] })]),
  );

  return (
    <Screen onRefresh={onRefresh} refreshing={refreshing}>
      <View className="gap-4">
        <View className="pt-1">
          <Text accessibilityRole="header" className="text-xl font-bold text-content-100">
            {greeting()}
            {name ? ', ' + name : ''}
          </Text>
          <Text className="text-sm text-content-400">Your balance and markets at a glance.</Text>
        </View>

        <BalanceCard />

        <View>
          <SectionHeading title="Favourites" onPress={() => router.navigate('/markets')} linkLabel="All markets" />
          <Panel className="overflow-hidden">
            {assetsQuery.isLoading ? (
              <RowsSkeleton />
            ) : favouriteAssets.length > 0 ? (
              <AssetRows assets={favouriteAssets} onSelect={openMarket} />
            ) : (
              <View className="items-center gap-2 px-6 py-8">
                <Text className="text-center text-sm font-medium text-content-200">No favourites yet</Text>
                <Text className="max-w-xs text-center text-xs text-content-400">
                  Star the markets you follow in Markets and they will wait for you here.
                </Text>
                <Button variant="ghost" icon={LineChart} className="mt-1" onPress={() => router.navigate('/markets')}>
                  Browse markets
                </Button>
              </View>
            )}
          </Panel>
        </View>

        <View>
          <SectionHeading title="Recent markets" />
          <Panel className="overflow-hidden">
            {assetsQuery.isLoading ? (
              <RowsSkeleton rows={2} />
            ) : recentAssets.length > 0 ? (
              <AssetRows assets={recentAssets} onSelect={openMarket} />
            ) : (
              <Text className="px-4 py-5 text-center text-xs text-content-400">
                Markets you open to trade will appear here.
              </Text>
            )}
          </Panel>
        </View>

        <View>
          <SectionHeading title="Quick actions" />
          <View className="gap-2">
            <View className="flex-row gap-2">
              <QuickAction
                onPress={() => router.navigate('/trade')}
                icon={CandlestickChart}
                label="Trade"
                hint="Open the chart"
              />
              <QuickAction onPress={() => router.navigate('/markets')} icon={LineChart} label="Markets" hint="Find a market" />
            </View>
            <View className="flex-row gap-2">
              <QuickAction onPress={() => router.navigate('/trades')} icon={History} label="History" hint="Your trades" />
              <QuickAction onPress={() => router.push('/learn')} icon={BookOpen} label="Learn" hint="How it works" />
            </View>
          </View>
        </View>

        <View>
          <SectionHeading title="New here?" onPress={() => router.push('/learn')} linkLabel="All guides" />
          <View className="gap-2">
            <GuideCard
              section="how-it-works"
              title="How fixed-time trading works"
              text="Pick a market, a duration and an amount, then choose UP or DOWN."
            />
            <GuideCard
              section="max-loss"
              title="What you can lose"
              text="The most any trade can cost is the amount you invest in it."
            />
            <GuideCard
              section="demo-live"
              title="Demo and Live"
              text="Practise with virtual money first. Switch to Live when you are ready."
            />
          </View>
        </View>
      </View>
    </Screen>
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
    <Panel className={cn('p-4', active === 'DEMO' && 'border-warn/40')}>
      <View className="flex-row flex-wrap items-start justify-between gap-3">
        <View className="min-w-0 shrink">
          <View className="flex-row items-center gap-2">
            <Text className="text-[11px] font-medium uppercase tracking-wider text-content-400">
              {active === 'DEMO' ? 'Demo balance' : 'Live balance'}
            </Text>
            <AccountMarker account={active} />
          </View>
          <View className="mt-1">
            {wallet ? (
              <Text adjustsFontSizeToFit numberOfLines={1} className="text-3xl font-bold tnum text-content-100">
                {formatMoney(wallet.balance, wallet.currency)}
              </Text>
            ) : (
              <Skeleton className="h-9 w-40" />
            )}
          </View>
          <View className="mt-2 flex-row flex-wrap gap-x-5 gap-y-1">
            <View className="flex-row gap-1.5">
              <Text className="text-xs text-content-400">Available</Text>
              <Text className="text-xs font-semibold tnum text-content-200">
                {wallet ? formatMoney(wallet.availableBalance, wallet.currency) : '--'}
              </Text>
            </View>
            <View className="flex-row gap-1.5">
              <Text className="text-xs text-content-400">In trades</Text>
              <Text className="text-xs font-semibold tnum text-content-200">
                {wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : '--'}
              </Text>
            </View>
          </View>
        </View>

        {active === 'DEMO' ? (
          <Button
            variant="ghost"
            size="sm"
            icon={RotateCcw}
            loading={resetDemo.isPending}
            onPress={() => resetDemo.mutate()}
          >
            Reset demo
          </Button>
        ) : null}
      </View>

      <AccountSwitch className="mt-4" />

      {/* Deposit comes straight after the balance (brief UX-01 order). Funds
          are real money, so these act on the Live account even from demo. */}
      <View className="mt-4 flex-row gap-2 border-t border-surface-800 pt-4">
        <Button icon={ArrowDownToLine} className="flex-1" onPress={() => openFunds('DEPOSIT')}>
          Deposit
        </Button>
        <Button variant="ghost" icon={ArrowUpFromLine} className="flex-1" onPress={() => openFunds('WITHDRAWAL')}>
          Withdraw
        </Button>
      </View>
      <Text className="mt-2 text-[11px] text-content-400">
        {active === 'DEMO'
          ? 'Deposits and withdrawals go to your Live account. Your demo balance is virtual and cannot be paid out.'
          : 'Deposits are checked by our team before they are credited.'}
      </Text>
      <Pressable
        onPress={() => router.push('/wallet/usdt')}
        accessibilityRole="link"
        hitSlop={6}
        className="mt-2 flex-row items-center gap-0.5 self-start active:opacity-70"
      >
        <Text className="text-xs font-semibold text-accent">USDT wallet</Text>
        <Icon as={ChevronRight} size={14} className="text-accent" />
      </Pressable>
    </Panel>
  );
}

function AssetRows({ assets, onSelect }: { assets: Asset[]; onSelect: (asset: Asset) => void }) {
  return (
    <View>
      {assets.map((asset, index) => (
        <View key={asset.id} className={cn(index > 0 && 'border-t border-surface-800/70')}>
          <AssetRow asset={asset} onSelect={onSelect} />
        </View>
      ))}
    </View>
  );
}

function SectionHeading({ title, onPress, linkLabel }: { title: string; onPress?: () => void; linkLabel?: string }) {
  return (
    <View className="mb-2 flex-row items-center justify-between">
      <Text accessibilityRole="header" className="text-sm font-bold text-content-100">
        {title}
      </Text>
      {onPress ? (
        <Pressable
          onPress={onPress}
          accessibilityRole="link"
          hitSlop={8}
          className="flex-row items-center gap-0.5 active:opacity-70"
        >
          <Text className="text-xs font-semibold text-accent">{linkLabel}</Text>
          <Icon as={ChevronRight} size={14} className="text-accent" />
        </Pressable>
      ) : null}
    </View>
  );
}

function QuickAction({
  onPress,
  icon,
  label,
  hint,
}: {
  onPress: () => void;
  icon: ComponentType<IconGlyphProps>;
  label: string;
  hint: string;
}) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="link"
      accessibilityLabel={label + ', ' + hint}
      className="min-w-0 flex-1 flex-row items-center gap-3 rounded-xl border border-surface-700 bg-surface-900 px-3 py-3 active:border-surface-600 active:bg-surface-850"
    >
      <View className="h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-surface-800">
        <Icon as={icon} size={18} className="text-content-200" />
      </View>
      <View className="min-w-0 flex-1">
        <Text className="text-sm font-semibold text-content-100">{label}</Text>
        <Text numberOfLines={1} className="text-[11px] text-content-400">
          {hint}
        </Text>
      </View>
    </Pressable>
  );
}

/** Opens Learn at one guide (the web linked `/learn#<id>`). */
function GuideCard({ section, title, text }: { section: string; title: string; text: string }) {
  return (
    <Pressable
      onPress={() => router.push({ pathname: '/learn', params: { section } })}
      accessibilityRole="link"
      className="rounded-xl border border-surface-700 bg-surface-900 p-4 active:border-surface-600 active:bg-surface-850"
    >
      <View className="flex-row items-center gap-2">
        <Icon as={BookOpen} size={16} className="text-accent" />
        <Text className="min-w-0 flex-1 text-sm font-semibold text-content-100">{title}</Text>
      </View>
      <Text className="mt-1 text-xs leading-5 text-content-400">{text}</Text>
    </Pressable>
  );
}

function RowsSkeleton({ rows = 3 }: { rows?: number }) {
  return (
    <View className="gap-2 p-3">
      {Array.from({ length: rows }).map((_, index) => (
        <Skeleton key={index} className="h-11 w-full" />
      ))}
    </View>
  );
}

function greeting(): string {
  const hour = new Date().getHours();
  if (hour < 12) return 'Good morning';
  if (hour < 18) return 'Good afternoon';
  return 'Good evening';
}
