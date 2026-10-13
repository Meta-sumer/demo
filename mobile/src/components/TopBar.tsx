import { useState } from 'react';
import { Pressable, View } from 'react-native';
import { Link, router } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useQuery } from '@tanstack/react-query';
import {
  ArrowDownToLine,
  ArrowUpFromLine,
  Bell,
  ChevronDown,
  ChevronRight,
  LogOut,
  RotateCcw,
  User,
} from 'lucide-react-native';
import { api } from '../services/api';
import { useAuthStore } from '../stores/authStore';
import { useAccountStore, useActiveWallet } from '../stores/accountStore';
import { formatMoney } from '../utils/format';
import { cn } from '../utils/cn';
import { useConnectionState } from '../features/trade/useConnectionState';
import { openFunds, useResetDemo } from '../features/account/funds';
import { AccountMarker, AccountSwitch } from './AccountSwitch';
import { Button, Icon, Sheet, Spinner, Text } from './ui';

/**
 * Top bar.
 *
 * The balance is the most-checked number on the platform, so it sits in its own
 * raised block. It always names the account it belongs to: Demo carries a
 * "DEMO · virtual" marker in the warning tone, so practice money can never be
 * mistaken for real money (brief UX-16); tapping it opens the accounts sheet,
 * which switches between the two and holds deposit and withdraw.
 *
 * There is no menu button: phones navigate with the bottom bar.
 */
export function TopBar() {
  const insets = useSafeAreaInsets();
  const connection = useConnectionState();
  const active = useAccountStore((s) => s.active);
  const wallet = useActiveWallet();
  const [menuOpen, setMenuOpen] = useState(false);

  const notificationsQuery = useQuery({
    queryKey: ['notifications'],
    queryFn: () => api.notifications.list({ limit: 10 }),
    staleTime: 30_000,
  });
  const unread = (notificationsQuery.data?.items ?? []).filter((n) => !n.read).length;

  return (
    <View
      className="flex-row items-center gap-2 border-b border-surface-800 bg-surface-900 px-3 pb-2"
      style={{ paddingTop: insets.top + 8 }}
    >
      <Link href="/home" asChild>
        <Pressable accessibilityRole="link" accessibilityLabel="Home" className="flex-row items-center gap-2.5">
          <View className="h-9 w-9 items-center justify-center rounded-xl bg-accent">
            <Text className="text-[13px] font-black text-surface-950">BT</Text>
          </View>
        </Pressable>
      </Link>

      <View className="ml-auto flex-row items-center gap-1.5">
        <View
          accessibilityLabel={connection === 'open' ? 'Live data connected' : 'Reconnecting'}
          className={cn(
            'h-2 w-2 rounded-full',
            connection === 'open' ? 'bg-up' : connection === 'connecting' ? 'bg-warn' : 'bg-down',
          )}
        />

        <Pressable
          onPress={() => setMenuOpen(true)}
          accessibilityRole="button"
          accessibilityLabel={
            (active === 'DEMO' ? 'Demo account, virtual balance ' : 'Live account, balance ') +
            (wallet ? formatMoney(wallet.balance, wallet.currency) : 'loading')
          }
          className={cn(
            'flex-row items-center gap-2 rounded-lg border px-2.5 py-1',
            active === 'DEMO' ? 'border-warn/40 bg-warn/5' : 'border-surface-700 bg-surface-850',
          )}
        >
          <View className="min-w-0">
            <AccountMarker account={active} />
            <Text className="text-sm font-bold tnum text-content-100">
              {wallet ? formatMoney(wallet.balance, wallet.currency) : '--'}
            </Text>
          </View>
          <Icon as={ChevronDown} size={16} className="text-content-400" />
        </Pressable>

        <Pressable
          onPress={() => router.navigate({ pathname: '/account', params: { section: 'notifications' } })}
          accessibilityRole="button"
          accessibilityLabel={unread > 0 ? unread + ' unread notifications' : 'Notifications'}
          className="relative rounded-lg p-2 active:bg-surface-850"
        >
          <Icon as={Bell} size={20} className="text-content-300" />
          {unread > 0 ? (
            <View className="absolute right-0.5 top-0.5 h-4 min-w-4 items-center justify-center rounded-full bg-down px-1">
              <Text className="text-[10px] font-bold text-white">{unread > 9 ? '9+' : unread}</Text>
            </View>
          ) : null}
        </Pressable>
      </View>

      <AccountsSheet open={menuOpen} onClose={() => setMenuOpen(false)} />
    </View>
  );
}

/** The balance menu: who is signed in, both accounts, funds, and the way out. */
function AccountsSheet({ open, onClose }: { open: boolean; onClose: () => void }) {
  const user = useAuthStore((s) => s.user);
  const logout = useAuthStore((s) => s.logout);
  const active = useAccountStore((s) => s.active);
  const wallet = useActiveWallet();
  const resetDemo = useResetDemo();

  function go(path: '/account' | '/wallet/usdt') {
    onClose();
    router.navigate(path);
  }

  return (
    <Sheet open={open} onClose={onClose} title="Accounts" subtitle={user?.email ? 'Signed in as ' + user.email : undefined}>
      <View className="px-4 pb-2">
        <AccountSwitch />

        <View className="mt-3 border-t border-surface-800 pt-2">
          <Row label="Available" value={wallet ? formatMoney(wallet.availableBalance, wallet.currency) : '--'} />
          <Row label="In open trades" value={wallet ? formatMoney(wallet.lockedBalance, wallet.currency) : '--'} />
        </View>

        {active === 'DEMO' ? (
          <Pressable
            onPress={() => resetDemo.mutate()}
            disabled={resetDemo.isPending}
            className="mt-1 flex-row items-center gap-2 rounded-lg px-3 py-2.5 active:bg-surface-850"
          >
            {resetDemo.isPending ? <Spinner tone="content-300" /> : <Icon as={RotateCcw} size={14} className="text-content-300" />}
            <Text className="text-xs font-semibold text-content-300">Reset demo balance</Text>
          </Pressable>
        ) : null}

        {/* Real money only. On demo these switch to Live first and say so,
            rather than refusing. */}
        <View className="mt-2 border-t border-surface-800 pt-3">
          <View className="flex-row gap-2">
            <Button
              className="flex-1"
              icon={ArrowDownToLine}
              textClassName="font-bold"
              onPress={() => {
                onClose();
                openFunds('DEPOSIT');
              }}
            >
              Deposit
            </Button>
            <Button
              className="flex-1"
              variant="subtle"
              icon={ArrowUpFromLine}
              onPress={() => {
                onClose();
                openFunds('WITHDRAWAL');
              }}
            >
              Withdraw
            </Button>
          </View>
          <Text className="mt-1.5 px-1 text-[10px] leading-4 text-content-400">
            Deposits and withdrawals use your Live account.
          </Text>
          {/* USDT is held apart from both INR accounts, so it is a link rather
              than a third balance in this menu. */}
          <Pressable onPress={() => go('/wallet/usdt')} className="mt-1 flex-row items-center justify-between px-1 py-2">
            <Text className="text-xs font-semibold text-accent">USDT wallet</Text>
            <Icon as={ChevronRight} size={14} className="text-accent" />
          </Pressable>
        </View>

        <View className="mt-2 flex-row gap-1 border-t border-surface-800 pt-2">
          <Pressable onPress={() => go('/account')} className="flex-1 flex-row items-center gap-2 rounded-lg px-3 py-2.5 active:bg-surface-850">
            <Icon as={User} size={14} className="text-content-300" />
            <Text className="text-xs font-semibold text-content-300">Account</Text>
          </Pressable>
          <Pressable
            onPress={() => {
              onClose();
              void logout();
            }}
            className="flex-1 flex-row items-center gap-2 rounded-lg px-3 py-2.5 active:bg-surface-850"
          >
            <Icon as={LogOut} size={14} className="text-content-300" />
            <Text className="text-xs font-semibold text-content-300">Sign out</Text>
          </Pressable>
        </View>
      </View>
    </Sheet>
  );
}

function Row({ label, value }: { label: string; value: string }) {
  return (
    <View className="flex-row items-center justify-between px-3 py-1.5">
      <Text className="text-xs text-content-300">{label}</Text>
      <Text className="text-xs font-semibold tnum text-content-100">{value}</Text>
    </View>
  );
}
