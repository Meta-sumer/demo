import { useEffect, useRef, useState, type ReactNode } from 'react';
import { Platform, Pressable, View, type TextStyle } from 'react-native';
import { router } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import Animated, {
  Easing,
  cancelAnimation,
  useAnimatedStyle,
  useSharedValue,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';
import { AlertTriangle, Check, ChevronRight, Copy } from 'lucide-react-native';
import { toast } from '../../hooks/useToast';
import { Badge, Field, Icon, Label, Skeleton, Text } from '../../components/ui';
import { cn } from '../../utils/cn';
import { formatUsdt, toUsdtInput } from '../../utils/usdt';
import type { UsdtNetworkView } from '../../types/api';
import { useUsdtOverview } from './queries';
import type { BadgeTone } from './model';

/**
 * A monospace face for addresses, hashes, UTRs and account numbers.
 *
 * The NativeWind preset's `font-mono` names a family Android does not have,
 * so these values are given the platform's own monospace face directly.
 */
export const monoStyle: TextStyle = {
  fontFamily: Platform.select({ ios: 'Menlo', default: 'monospace' }),
};

/**
 * "Copied" for a moment after a copy - the tick that replaces the copy icon.
 * Addresses and account details are copied, never retyped.
 */
export function useCopied(resetMs = 1500) {
  const [copied, setCopied] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
    },
    [],
  );

  async function copy(value: string) {
    try {
      await Clipboard.setStringAsync(value);
      setCopied(true);
      if (timer.current) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(false), resetMs);
    } catch {
      toast('error', 'Could not copy', 'Select the text and copy it manually');
    }
  }

  return { copied, copy };
}

/** A glyph that turns while something is in progress (the web's `animate-spin`). */
export function Spin({ children }: { children: ReactNode }) {
  const rotation = useSharedValue(0);
  useEffect(() => {
    rotation.value = withRepeat(withTiming(360, { duration: 1000, easing: Easing.linear }), -1, false);
    return () => cancelAnimation(rotation);
  }, [rotation]);
  const style = useAnimatedStyle(() => ({ transform: [{ rotate: rotation.value + 'deg' }] }));
  return <Animated.View style={style}>{children}</Animated.View>;
}

const MARK_SIZE = {
  sm: { box: 'h-6 w-6', text: 'text-xs' },
  md: { box: 'h-8 w-8', text: 'text-sm' },
  lg: { box: 'h-12 w-12', text: 'text-xl' },
} as const;

/** The USDT glyph, in Tether's green family (the theme's up tone, so it follows the theme). */
export function UsdtMark({ className, size = 'md' }: { className?: string; size?: keyof typeof MARK_SIZE }) {
  const sizing = MARK_SIZE[size];
  return (
    <View
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      className={cn(
        'shrink-0 items-center justify-center rounded-full border border-up/30 bg-up/15',
        sizing.box,
        className,
      )}
    >
      <Text className={cn('font-black text-up', sizing.text)}>₮</Text>
    </View>
  );
}

/**
 * A USDT figure for a headline: whole units and cents at full strength, the
 * remaining four decimals quieter. Every digit is still there - the ledger
 * holds six - but the eye lands on the part people read.
 *
 * `size` is the font size of the figure; the unit is set at about half of it.
 */
export function UsdtFigure({ micro, className, size = 28 }: { micro: number; className?: string; size?: number }) {
  const text = formatUsdt(micro, { unit: false });
  const point = text.indexOf('.');
  const head = point >= 0 ? text.slice(0, point + 3) : text;
  const tail = point >= 0 ? text.slice(point + 3) : '';
  return (
    <Text
      className={cn('font-bold text-content-100 tnum', className)}
      style={{ fontSize: size, lineHeight: Math.round(size * 1.25) }}
    >
      {head}
      <Text className="font-bold text-content-400 tnum" style={{ fontSize: size }}>
        {tail}
      </Text>
      <Text className="font-semibold text-content-400" style={{ fontSize: Math.round(size * 0.55) }}>
        {' USDT'}
      </Text>
    </Text>
  );
}

export function StatusBadge({ tone, children }: { tone: BadgeTone; children: ReactNode }) {
  return <Badge tone={tone}>{children}</Badge>;
}

/** Network label as a small chip: "TRC20". */
export function NetworkChip({ label }: { label: string }) {
  return (
    <View className="shrink-0 self-center rounded border border-surface-700 bg-surface-850 px-1.5 py-px">
      <Text className="text-[10px] font-bold tracking-wide text-content-300">{label}</Text>
    </View>
  );
}

export function ErrorNote({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <View
      accessibilityRole="alert"
      accessibilityLiveRegion="polite"
      className="rounded-lg border border-down/30 bg-down/10 px-3 py-2"
    >
      <Text className="text-sm text-down">{message}</Text>
    </View>
  );
}

const NETWORK_WARNING =
  'Only send USDT on the selected network. Sending another token or using another network may result in permanent loss.';

/** The same-network warning, wherever an address or a network is chosen. */
export function NetworkWarning({ children }: { children?: ReactNode }) {
  return (
    <View className="flex-row gap-2.5 rounded-lg border border-warn/30 bg-warn/10 px-3 py-2.5">
      <View className="mt-0.5">
        <Icon as={AlertTriangle} size={16} className="text-warn" />
      </View>
      <Text className="min-w-0 flex-1 text-xs leading-5 text-content-200">{children ?? NETWORK_WARNING}</Text>
    </View>
  );
}

/**
 * Network choice as cards. Only networks open for this purpose are passed in;
 * a single one is still shown, so the customer always sees which chain the
 * money is on.
 */
export function NetworkPicker({
  networks,
  value,
  onChange,
  purpose,
}: {
  networks: UsdtNetworkView[];
  value: string;
  onChange: (code: string) => void;
  purpose: 'deposit' | 'withdraw';
}) {
  return (
    <View>
      <Label>Network</Label>
      <View accessibilityRole="radiogroup" accessibilityLabel="Network" className="flex-row flex-wrap gap-2">
        {networks.map((network) => {
          const selected = network.code === value;
          return (
            <Pressable
              key={network.code}
              accessibilityRole="radio"
              accessibilityState={{ checked: selected }}
              accessibilityLabel={network.label + ', ' + (network.name || network.chain)}
              onPress={() => onChange(network.code)}
              className={cn(
                'min-w-[45%] flex-1 rounded-lg border px-3 py-2.5',
                selected ? 'border-accent bg-accent/10' : 'border-surface-700 bg-surface-850 active:border-surface-600',
              )}
            >
              <View className="flex-row items-center justify-between gap-2">
                <Text className="text-sm font-bold text-content-100">{network.label}</Text>
                <View
                  className={cn(
                    'h-4 w-4 shrink-0 items-center justify-center rounded-full border',
                    selected ? 'border-accent bg-accent' : 'border-surface-600',
                  )}
                >
                  {selected ? <View className="h-1.5 w-1.5 rounded-full bg-surface-950" /> : null}
                </View>
              </View>
              <Text numberOfLines={1} className="mt-0.5 text-[11px] text-content-400">
                {network.name || network.chain}
              </Text>
              {purpose === 'deposit' && network.minDeposit > 0 ? (
                <Text className="mt-0.5 text-[11px] text-content-400 tnum">
                  Min {formatUsdt(network.minDeposit, { trim: true })}
                </Text>
              ) : null}
            </Pressable>
          );
        })}
      </View>
    </View>
  );
}

/**
 * A USDT amount, typed as text and parsed exactly by the caller. "USDT" sits
 * inside the field so the unit is never in doubt; Max, when offered, fills in
 * the whole available balance.
 */
export function AmountInput({
  id,
  label = 'Amount',
  value,
  onChange,
  error,
  hint,
  max,
}: {
  id: string;
  label?: string;
  value: string;
  onChange: (text: string) => void;
  error?: string | null;
  hint?: ReactNode;
  /** Available balance in micro-USDT; shows the Max button. */
  max?: number;
}) {
  return (
    <View>
      <Field
        testID={id}
        label={label}
        accessibilityLabel={label}
        keyboardType="decimal-pad"
        autoComplete="off"
        autoCorrect={false}
        autoCapitalize="none"
        spellCheck={false}
        placeholder="0.00"
        inputClassName="text-base"
        value={value}
        onChangeText={onChange}
        error={error ?? null}
        right={
          <View className="flex-row items-center gap-2">
            {max !== undefined ? (
              <Pressable
                onPress={() => onChange(toUsdtInput(max))}
                disabled={max <= 0}
                accessibilityRole="button"
                accessibilityLabel={'Use the maximum, ' + formatUsdt(max)}
                className={cn('rounded-md bg-surface-700 px-2 py-1 active:opacity-80', max <= 0 && 'opacity-45')}
              >
                <Text className="text-[11px] font-bold text-content-100">Max</Text>
              </Pressable>
            ) : null}
            <Text className="text-xs font-semibold text-content-400">USDT</Text>
          </View>
        }
      />
      {hint ? (
        <View className="mt-1">
          {typeof hint === 'string' ? <Text className="text-[11px] text-content-400">{hint}</Text> : hint}
        </View>
      ) : null}
    </View>
  );
}

export interface DetailRow {
  label: ReactNode;
  value: ReactNode;
  strong?: boolean;
  mono?: boolean;
}

/** Label / value pairs in a bordered list - every review and record screen uses it. */
export function DetailList({ rows, className }: { rows: Array<DetailRow | null | false>; className?: string }) {
  return (
    <View className={cn('rounded-lg border border-surface-700 bg-surface-850', className)}>
      {rows
        .filter((row): row is DetailRow => !!row)
        .map((row, index) => (
          <View
            key={index}
            className={cn(
              'flex-row items-start justify-between gap-4 px-3.5 py-2.5',
              index > 0 && 'border-t border-surface-800',
            )}
          >
            {typeof row.label === 'string' ? (
              <Text className="shrink-0 text-xs text-content-400">{row.label}</Text>
            ) : (
              <View className="shrink-0">{row.label}</View>
            )}
            {typeof row.value === 'string' || typeof row.value === 'number' ? (
              <Text
                selectable={row.mono}
                className={cn(
                  'min-w-0 flex-1 text-right tnum',
                  row.mono ? 'text-xs' : 'text-sm',
                  row.strong ? 'text-base font-bold text-content-100' : 'font-medium text-content-200',
                )}
                style={row.mono ? monoStyle : undefined}
              >
                {row.value}
              </Text>
            ) : (
              <View className="min-w-0 flex-1 items-end">{row.value}</View>
            )}
          </View>
        ))}
    </View>
  );
}

/** An address or a hash, in full, with a copy button - these are pasted elsewhere, never retyped. */
export function CopyValue({ label, value }: { label: string; value: string }) {
  const { copied, copy } = useCopied();
  return (
    <View className="flex-row items-start gap-2 rounded-lg border border-surface-700 bg-surface-850 px-3 py-2.5">
      <View className="min-w-0 flex-1">
        <Text className="text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</Text>
        <Text selectable className="mt-0.5 text-sm text-content-100" style={monoStyle}>
          {value}
        </Text>
      </View>
      <Pressable
        accessibilityRole="button"
        accessibilityLabel={'Copy ' + label.toLowerCase()}
        hitSlop={6}
        className="shrink-0 rounded-md p-1.5 active:bg-surface-800"
        onPress={() => void copy(value)}
      >
        {copied ? (
          <Icon as={Check} size={16} className="text-up" />
        ) : (
          <Icon as={Copy} size={16} className="text-content-400" />
        )}
      </Pressable>
    </View>
  );
}

/**
 * A door to the USDT wallet from the places that list balances (Account ›
 * Funds, Transactions). Shows what is available there once it has loaded;
 * if the server does not offer USDT yet the door still opens, onto the screen
 * that says so.
 */
export function UsdtWalletLink({ className }: { className?: string }) {
  const overview = useUsdtOverview();
  const data = overview.data;
  const status = !data
    ? overview.isError
      ? 'Deposit and withdraw USDT on TRC20 or BSC'
      : null
    : !data.enabled
      ? 'Not available yet'
      : data.wallet
        ? 'Available ' + formatUsdt(data.wallet.availableBalance, { trim: true })
        : 'Deposit and withdraw USDT on TRC20 or BSC';

  return (
    <Pressable
      accessibilityRole="link"
      accessibilityLabel={'USDT wallet' + (status ? ', ' + status : '')}
      onPress={() => router.push('/wallet/usdt')}
      className={cn(
        'flex-row items-center gap-3 rounded-xl border border-surface-700 bg-surface-900 px-4 py-3 active:border-surface-600 active:bg-surface-850',
        className,
      )}
    >
      <UsdtMark />
      <View className="min-w-0 flex-1">
        <Text className="text-sm font-semibold text-content-100">USDT wallet</Text>
        {status ? (
          <Text numberOfLines={1} className="text-[11px] text-content-400 tnum">
            {status}
          </Text>
        ) : (
          <Skeleton className="mt-1 h-3 w-28" />
        )}
      </View>
      <Icon as={ChevronRight} size={16} className="text-content-400" />
    </Pressable>
  );
}
