import { useEffect, type ReactNode } from 'react';
import { ActivityIndicator, View, type ViewProps } from 'react-native';
import Animated, { useAnimatedStyle, useSharedValue, withRepeat, withTiming } from 'react-native-reanimated';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import type { TokenName } from '../../theme/tokens';
import { Text } from './Text';

export function Panel({ className, children, ...props }: ViewProps & { className?: string; children?: ReactNode }) {
  return (
    <View className={cn('rounded-xl border border-surface-700 bg-surface-900', className)} {...props}>
      {children}
    </View>
  );
}

export function Stat({
  label,
  value,
  hint,
  tone = 'neutral',
  className,
}: {
  label: string;
  value: ReactNode;
  hint?: ReactNode;
  tone?: 'neutral' | 'up' | 'down' | 'accent';
  className?: string;
}) {
  const toneClass =
    tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : tone === 'accent' ? 'text-accent' : 'text-content-100';
  return (
    <Panel className={cn('px-4 py-3', className)}>
      <Text className="text-[11px] font-medium uppercase tracking-wider text-content-400">{label}</Text>
      {typeof value === 'string' || typeof value === 'number' ? (
        <Text className={cn('mt-1 text-xl font-semibold tnum', toneClass)}>{value}</Text>
      ) : (
        <View className="mt-1">{value}</View>
      )}
      {hint ? (
        typeof hint === 'string' ? <Text className="mt-0.5 text-xs text-content-400">{hint}</Text> : hint
      ) : null}
    </Panel>
  );
}

export type BadgeTone = 'neutral' | 'up' | 'down' | 'warn' | 'accent';

const BADGE_BOX: Record<BadgeTone, string> = {
  neutral: 'bg-surface-700',
  up: 'bg-up/15 border border-up/30',
  down: 'bg-down/15 border border-down/30',
  warn: 'bg-warn/15 border border-warn/30',
  accent: 'bg-accent/15 border border-accent/30',
};

const BADGE_TEXT: Record<BadgeTone, string> = {
  neutral: 'text-content-200',
  up: 'text-up',
  down: 'text-down',
  warn: 'text-warn',
  accent: 'text-accent',
};

export function badgeTextClass(tone: BadgeTone): string {
  return BADGE_TEXT[tone];
}

export function Badge({
  children,
  tone = 'neutral',
  className,
  textClassName,
}: {
  children: ReactNode;
  tone?: BadgeTone;
  className?: string;
  textClassName?: string;
}) {
  return (
    <View className={cn('flex-row items-center gap-1 self-start rounded-md px-2 py-0.5', BADGE_BOX[tone], className)}>
      {typeof children === 'string' || typeof children === 'number' ? (
        <Text className={cn('text-[11px] font-semibold', BADGE_TEXT[tone], textClassName)}>{children}</Text>
      ) : (
        children
      )}
    </View>
  );
}

/** Feed status pill - the trader needs this visible at all times. */
export function FeedBadge({ status, latencyMs }: { status: string; latencyMs?: number | null }) {
  const tone: BadgeTone = status === 'HEALTHY' ? 'up' : status === 'DEGRADED' ? 'warn' : 'down';
  const dot = status === 'HEALTHY' ? 'bg-up' : status === 'DEGRADED' ? 'bg-warn' : 'bg-down';
  return (
    <Badge tone={tone}>
      <View className={cn('h-1.5 w-1.5 rounded-full', dot)} />
      <Text className={cn('text-[11px] font-semibold', BADGE_TEXT[tone])}>{status}</Text>
      {latencyMs != null ? (
        <Text className={cn('text-[11px] font-semibold opacity-80 tnum', BADGE_TEXT[tone])}>
          {Math.round(latencyMs)}ms
        </Text>
      ) : null}
    </Badge>
  );
}

/** A placeholder block that breathes while its content loads. */
export function Skeleton({ className }: { className?: string }) {
  const opacity = useSharedValue(0.55);
  useEffect(() => {
    opacity.value = withRepeat(withTiming(1, { duration: 700 }), -1, true);
  }, [opacity]);
  const style = useAnimatedStyle(() => ({ opacity: opacity.value }));
  return <Animated.View className={cn('rounded bg-surface-800', className)} style={style} />;
}

export function Spinner({ tone = 'accent', size = 'small' }: { tone?: TokenName; size?: 'small' | 'large' }) {
  const color = useTokenColor(tone);
  return <ActivityIndicator size={size} color={color} accessibilityLabel="Loading" />;
}

export function EmptyState({ title, description, className }: { title: string; description?: string; className?: string }) {
  return (
    <View className={cn('items-center justify-center gap-1 px-6 py-12', className)}>
      <Text className="text-center text-sm font-medium text-content-200">{title}</Text>
      {description ? <Text className="max-w-sm text-center text-xs text-content-400">{description}</Text> : null}
    </View>
  );
}

/** A hairline rule between rows. */
export function Divider({ className }: { className?: string }) {
  return <View className={cn('h-px bg-surface-800', className)} />;
}

/** Small uppercase field label, the web's `.label`. */
export function Label({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <Text className={cn('mb-1.5 text-[11px] font-medium uppercase tracking-wider text-content-400', className)}>
      {children}
    </Text>
  );
}
