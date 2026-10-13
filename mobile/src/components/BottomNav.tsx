import { Pressable, View } from 'react-native';
import type { BottomTabBarProps } from '@react-navigation/bottom-tabs';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { CandlestickChart, History, House, LineChart, User } from 'lucide-react-native';
import type { ComponentType } from 'react';
import { useSupportStore } from '../stores/supportStore';
import { useActiveTradeCount } from '../features/trade/useActiveTrades';
import { cn } from '../utils/cn';
import { Icon, Text, type IconGlyphProps } from './ui';

/**
 * Phone navigation (brief section 5): Home · Markets · Trade · Trades ·
 * Account, nothing else. Wallet, deposits, settings and support all live
 * inside Account.
 */
export interface NavItem {
  name: string;
  label: string;
  icon: ComponentType<IconGlyphProps>;
}

export const NAV_ITEMS: NavItem[] = [
  { name: 'home', label: 'Home', icon: House },
  { name: 'markets', label: 'Markets', icon: LineChart },
  { name: 'trade', label: 'Trade', icon: CandlestickChart },
  { name: 'trades', label: 'Trades', icon: History },
  { name: 'account', label: 'Account', icon: User },
];

/** Replies waiting in the support chat. Support lives in Account, so the badge does too. */
export function useSupportUnread(): number {
  return useSupportStore((s) => s.conversation?.unreadByUser ?? 0);
}

export function UnreadDot({ count, className }: { count: number; className?: string }) {
  if (count <= 0) return null;
  return (
    <View
      accessibilityLabel={count + ' unread support ' + (count === 1 ? 'reply' : 'replies')}
      className={cn('absolute h-4 min-w-4 items-center justify-center rounded-full bg-down px-1', className)}
    >
      <Text className="text-[9px] font-bold leading-3 text-white">{count > 9 ? '9+' : count}</Text>
    </View>
  );
}

/**
 * Running trades on the account being traded, on the Trades destination
 * (reference record 15 - Deriv's "Positions 1"). In the colour the platform
 * uses for live orders, not the red of an unread reply: a running trade is
 * not something waiting to be dealt with.
 */
export function ActiveTradesBadge({ count, className }: { count: number; className?: string }) {
  if (count <= 0) return null;
  return (
    <View
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      className={cn(
        'absolute h-4 min-w-4 items-center justify-center rounded-full border-2 border-surface-900 bg-brand px-1',
        className,
      )}
    >
      <Text className="text-[9px] font-black leading-3 text-surface-950 tnum">{count > 9 ? '9+' : count}</Text>
    </View>
  );
}

export function BottomNav({ state, navigation }: BottomTabBarProps) {
  const insets = useSafeAreaInsets();
  const unread = useSupportUnread();
  const activeTrades = useActiveTradeCount();

  return (
    <View
      accessibilityRole="tablist"
      className="flex-row border-t border-surface-800 bg-surface-900"
      style={{ paddingBottom: insets.bottom }}
    >
      {state.routes.map((route, index) => {
        const item = NAV_ITEMS.find((entry) => entry.name === route.name);
        if (!item) return null;
        const active = state.index === index;
        const label =
          item.label + (item.name === 'trades' && activeTrades > 0 ? ', ' + activeTrades + ' active' : '');

        return (
          <Pressable
            key={route.key}
            accessibilityRole="tab"
            accessibilityState={{ selected: active }}
            accessibilityLabel={label}
            onPress={() => {
              const event = navigation.emit({ type: 'tabPress', target: route.key, canPreventDefault: true });
              if (!active && !event.defaultPrevented) navigation.navigate(route.name, route.params);
            }}
            className="h-16 flex-1 items-center justify-center gap-1"
          >
            <View className={cn('relative h-7 w-12 items-center justify-center rounded-full', active && 'bg-accent')}>
              <Icon as={item.icon} size={18} className={active ? 'text-surface-950' : 'text-content-400'} />
              {item.name === 'account' ? <UnreadDot count={unread} className="-right-0.5 -top-1" /> : null}
              {item.name === 'trades' ? <ActiveTradesBadge count={activeTrades} className="-right-0.5 -top-1" /> : null}
            </View>
            <Text className={cn('text-[10px] font-semibold', active ? 'text-content-100' : 'text-content-400')}>
              {item.label}
            </Text>
          </Pressable>
        );
      })}
    </View>
  );
}
