import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Pressable, ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import * as Haptics from 'expo-haptics';
import Animated, { FadeInDown, FadeOutDown, LinearTransition } from 'react-native-reanimated';
import { ChevronDown, ChevronUp, X } from 'lucide-react-native';
import { useTradeResultStore } from '../../stores/tradeResultStore';
import { useSettingsStore } from '../../stores/settingsStore';
import { useWorkspaceStore } from '../../stores/workspaceStore';
import { formatExpiry, formatMoney, formatSignedMoney } from '../../utils/format';
import { durationOf, tradeLabel } from '../../utils/tradeStatus';
import { cn } from '../../utils/cn';
import { Icon, Text } from '../../components/ui';
import { payoutOf, settlementOf } from './settlement';
import { useTicketStore } from './ticketStore';
import { DirectionWord, TradeDemoTag, useClockFormatter } from './tradeBits';
import type { Trade } from '../../types/api';

/**
 * The result of a trade, announced as it settles (brief UX-13).
 *
 * Deliberately not a toast. The toasts carry things that happened to the
 * session - an order accepted, a request refused - and are worded as sentences.
 * This is the answer the trader is waiting on, so it is built like a receipt:
 * the status in words, the terms the trade ran under, what came back and the
 * net result, and three ways on - Trade Again, View Trade, View History.
 *
 * The newest result stays until it is closed; older ones fold under it as
 * "+N more". Nothing promotional ever shares this card.
 *
 * A win is gold and a loss is plain. That asymmetry is on purpose - a loss is
 * already a zero, and dressing it in red would make it louder than the wins.
 *
 * Each new result also arrives as a tap on the wrist: the notification haptic
 * for a gain, the error one for a loss, a warning for money back - so a trader
 * looking at the chart feels the answer land.
 *
 * The "hide trade results" setting silences it entirely, haptic included; the
 * results are still in the history.
 *
 * Mounted once by the app shell, above the bottom navigation on the tabs
 * (`bottomOffset` is the navigation's height there, 0 elsewhere).
 */
const MAX_WIDTH = 400;

export function TradeResultToasts({ bottomOffset = 0 }: { bottomOffset?: number }) {
  const results = useTradeResultStore((s) => s.results);
  const dismiss = useTradeResultStore((s) => s.dismiss);
  const dismissAll = useTradeResultStore((s) => s.dismissAll);
  const hidden = useSettingsStore((s) => s.hideTradeResults);
  const [expanded, setExpanded] = useState(false);
  const insets = useSafeAreaInsets();

  // Results already on screen when this mounted are not news; nor is an older
  // result that becomes the newest because the one above it was closed.
  const feltRef = useRef<Set<string> | null>(null);
  if (feltRef.current === null) feltRef.current = new Set(results.map((trade) => trade.id));
  useEffect(() => {
    const felt = feltRef.current!;
    const fresh = results.filter((trade) => !felt.has(trade.id));
    for (const trade of fresh) felt.add(trade.id);
    if (hidden || fresh.length === 0) return;
    const net = settlementOf(fresh[0]!) ?? 0;
    const type =
      net > 0
        ? Haptics.NotificationFeedbackType.Success
        : net < 0
          ? Haptics.NotificationFeedbackType.Error
          : Haptics.NotificationFeedbackType.Warning;
    void Haptics.notificationAsync(type).catch(() => undefined);
  }, [results, hidden]);

  if (hidden || results.length === 0) return null;

  const [latest, ...older] = results as [Trade, ...Trade[]];

  return (
    <View
      pointerEvents="box-none"
      accessibilityLiveRegion="polite"
      style={{ position: 'absolute', left: 12, right: 12, bottom: insets.bottom + bottomOffset + 12 }}
      className="items-center"
    >
      <View pointerEvents="box-none" className="w-full gap-2" style={{ maxWidth: MAX_WIDTH }}>
        <ResultCard key={latest.id} trade={latest} onClose={() => dismiss(latest.id)} />

        {older.length > 0 ? (
          <Animated.View
            entering={FadeInDown.duration(180)}
            exiting={FadeOutDown.duration(160)}
            layout={LinearTransition}
            className="rounded-2xl border border-surface-700 bg-surface-850"
            style={SHADOW}
          >
            <View className="flex-row items-center">
              <Pressable
                onPress={() => setExpanded((value) => !value)}
                accessibilityRole="button"
                accessibilityState={{ expanded }}
                className="flex-1 flex-row items-center gap-1.5 px-3 py-2.5"
              >
                <Icon as={expanded ? ChevronDown : ChevronUp} size={16} className="text-content-300" />
                <Text className="text-xs font-semibold text-content-300">
                  +{older.length} more result{older.length === 1 ? '' : 's'}
                </Text>
              </Pressable>
              <Pressable
                onPress={() => {
                  dismissAll();
                  setExpanded(false);
                }}
                accessibilityRole="button"
                className="px-3 py-2.5"
              >
                <Text className="text-xs font-semibold text-content-400">Clear all</Text>
              </Pressable>
            </View>
            {expanded ? (
              <ScrollView className="max-h-48 border-t border-surface-700/60" nestedScrollEnabled>
                {older.map((trade, index) => (
                  <OlderRow
                    key={trade.id}
                    trade={trade}
                    first={index === 0}
                    onClose={() => dismiss(trade.id)}
                  />
                ))}
              </ScrollView>
            ) : null}
          </Animated.View>
        ) : null}
      </View>
    </View>
  );
}

const SHADOW = {
  elevation: 10,
  shadowColor: '#000',
  shadowOpacity: 0.35,
  shadowRadius: 16,
  shadowOffset: { width: 0, height: 8 },
} as const;

function ResultCard({ trade, onClose }: { trade: Trade; onClose: () => void }) {
  const openAsset = useWorkspaceStore((s) => s.openAsset);
  const prefill = useTicketStore((s) => s.prefill);
  const clock = useClockFormatter();

  const net = settlementOf(trade) ?? 0;
  const returned = payoutOf(trade);
  const won = net > 0;

  function tradeAgain() {
    // Same asset, same duration, same amount - and the ticket takes focus, so
    // the only thing left to choose is the direction.
    openAsset(trade.assetId);
    prefill(trade.stake, durationOf(trade));
    onClose();
    router.navigate('/trade');
  }

  return (
    <Animated.View
      entering={FadeInDown.duration(200)}
      exiting={FadeOutDown.duration(160)}
      layout={LinearTransition}
      accessibilityLabel={'Result: ' + tradeLabel(trade) + ' ' + trade.symbol}
      className={cn('rounded-2xl border bg-surface-850 p-3', won ? 'border-brand/50' : 'border-surface-700')}
      style={SHADOW}
    >
      <View className="flex-row items-center gap-2">
        <View className={cn('rounded-md px-2 py-0.5', won ? 'bg-brand' : 'bg-content-100')}>
          <Text className="text-xs font-bold text-surface-950">{tradeLabel(trade)}</Text>
        </View>
        <Text numberOfLines={1} className="min-w-0 flex-1 text-sm font-semibold text-content-100">
          {trade.symbol}
        </Text>
        <TradeDemoTag trade={trade} />
        <Pressable
          onPress={onClose}
          hitSlop={6}
          accessibilityRole="button"
          accessibilityLabel={'Close result for ' + trade.symbol}
          className="h-7 w-7 items-center justify-center rounded-lg active:bg-surface-800"
        >
          <Icon as={X} size={16} className="text-content-400" />
        </Pressable>
      </View>

      <View className="mt-2 flex-row flex-wrap gap-y-1.5">
        <Cell label="Direction">
          <DirectionWord direction={trade.direction} className="text-[11px]" />
        </Cell>
        <Cell label="Investment">{formatMoney(trade.stake, trade.currency)}</Cell>
        <Cell label="Expiry">
          {formatExpiry(durationOf(trade))} · {clock(trade.expiryTimestamp)}
        </Cell>
        <Cell label="Returned">{formatMoney(returned, trade.currency)}</Cell>
        <Cell label="Net result" wide>
          <Text className={cn('text-base font-semibold tnum', won ? 'text-brand' : 'text-content-100')}>
            {formatSignedMoney(net, trade.currency)}
          </Text>
        </Cell>
      </View>

      <View className="mt-3 flex-row gap-1.5">
        <Pressable
          onPress={tradeAgain}
          accessibilityRole="button"
          className="flex-1 items-center justify-center rounded-lg bg-accent-solid px-2 py-2.5 active:opacity-80"
        >
          <Text numberOfLines={1} className="text-xs font-semibold text-surface-950">
            Trade Again
          </Text>
        </Pressable>
        <Pressable
          onPress={() => {
            onClose();
            router.push('/trades/' + trade.id);
          }}
          accessibilityRole="button"
          className="flex-1 items-center justify-center rounded-lg border border-surface-700 bg-surface-850 px-2 py-2.5 active:bg-surface-800"
        >
          <Text numberOfLines={1} className="text-xs font-semibold text-content-200">
            View Trade
          </Text>
        </Pressable>
        <Pressable
          onPress={() => {
            onClose();
            router.navigate({ pathname: '/trades', params: { tab: 'history' } });
          }}
          accessibilityRole="button"
          className="flex-1 items-center justify-center rounded-lg border border-surface-700 bg-surface-850 px-2 py-2.5 active:bg-surface-800"
        >
          <Text numberOfLines={1} className="text-xs font-semibold text-content-200">
            View History
          </Text>
        </Pressable>
      </View>
    </Animated.View>
  );
}

function Cell({ label, children, wide }: { label: string; children: ReactNode; wide?: boolean }) {
  const plain = typeof children === 'string' || typeof children === 'number' || Array.isArray(children);
  return (
    <View className={cn('min-w-0 pr-3', wide ? 'w-2/3' : 'w-1/3')}>
      <Text className="text-[11px] text-content-400">{label}</Text>
      {plain ? (
        <Text numberOfLines={1} className="text-[11px] font-semibold tnum text-content-100">
          {children}
        </Text>
      ) : (
        <View className="flex-row">{children}</View>
      )}
    </View>
  );
}

function OlderRow({ trade, first, onClose }: { trade: Trade; first: boolean; onClose: () => void }) {
  const net = settlementOf(trade) ?? 0;
  return (
    <View className={cn('flex-row items-center gap-2 px-3 py-1.5', !first && 'border-t border-surface-700/60')}>
      <Pressable
        onPress={() => {
          onClose();
          router.push('/trades/' + trade.id);
        }}
        accessibilityRole="button"
        className="min-w-0 flex-1 flex-row items-center gap-2"
      >
        <Text numberOfLines={1} className="w-16 text-xs font-semibold text-content-300">
          {tradeLabel(trade)}
        </Text>
        <Text numberOfLines={1} className="min-w-0 flex-1 text-xs font-semibold text-content-100">
          {trade.symbol}
        </Text>
        <TradeDemoTag trade={trade} />
        <Text className={cn('text-xs font-bold tnum', net > 0 ? 'text-brand' : 'text-content-100')}>
          {formatSignedMoney(net, trade.currency)}
        </Text>
      </Pressable>
      <Pressable
        onPress={onClose}
        hitSlop={6}
        accessibilityRole="button"
        accessibilityLabel={'Close result for ' + trade.symbol}
        className="h-6 w-6 items-center justify-center rounded"
      >
        <Icon as={X} size={14} className="text-content-400" />
      </Pressable>
    </View>
  );
}
