import type { ReactNode } from 'react';
import { KeyboardAvoidingView, Platform, RefreshControl, ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { ChevronLeft } from 'lucide-react-native';
import { cn } from '../utils/cn';
import { useTokenColor } from '../utils/theme-colors';
import { IconButton } from './ui/Button';
import { Text } from './ui/Text';

/**
 * Padding and scrolling for the ordinary screens - the web's PageContainer.
 *
 * The trading screen opts out: it manages its own regions, and a scroll view
 * around the chart would fight the chart's own pan gesture.
 *
 * `bottomInset` is for screens pushed over the tab bar, which have to clear
 * the home indicator themselves.
 */
export function Screen({
  children,
  scroll = true,
  onRefresh,
  refreshing = false,
  bottomInset = false,
  className,
  contentClassName,
}: {
  children: ReactNode;
  scroll?: boolean;
  /** Pull to refresh. */
  onRefresh?: () => void;
  refreshing?: boolean;
  bottomInset?: boolean;
  className?: string;
  contentClassName?: string;
}) {
  const insets = useSafeAreaInsets();
  const tint = useTokenColor('accent');
  const bottom = (bottomInset ? insets.bottom : 0) + 24;

  if (!scroll) {
    return (
      <View className={cn('flex-1 bg-surface-950', className)} style={{ paddingBottom: bottomInset ? insets.bottom : 0 }}>
        {children}
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      className={cn('flex-1 bg-surface-950', className)}
    >
      <ScrollView
        className="flex-1"
        contentContainerClassName={cn('px-3 pt-4', contentClassName)}
        contentContainerStyle={{ paddingBottom: bottom }}
        keyboardShouldPersistTaps="handled"
        refreshControl={
          onRefresh ? (
            <RefreshControl refreshing={refreshing} onRefresh={onRefresh} tintColor={tint} colors={[tint]} />
          ) : undefined
        }
      >
        {children}
      </ScrollView>
    </KeyboardAvoidingView>
  );
}

/**
 * Title bar for a screen pushed over the tabs: back, title, and room on the
 * right for an action. Clears the status bar itself.
 */
export function ScreenHeader({
  title,
  subtitle,
  right,
  onBack,
  fallbackHref = '/home',
}: {
  title: string;
  subtitle?: string;
  right?: ReactNode;
  onBack?: () => void;
  /** Where back goes when there is nothing to go back to (opened from a link). */
  fallbackHref?: string;
}) {
  const insets = useSafeAreaInsets();
  return (
    <View
      className="flex-row items-center gap-1 border-b border-surface-800 bg-surface-900 px-1.5 pb-2"
      style={{ paddingTop: insets.top + 6 }}
    >
      <IconButton
        icon={ChevronLeft}
        size={22}
        accessibilityLabel="Back"
        iconClassName="text-content-100"
        onPress={() => {
          if (onBack) return onBack();
          if (router.canGoBack()) router.back();
          else router.replace(fallbackHref as never);
        }}
      />
      <View className="min-w-0 flex-1">
        <Text accessibilityRole="header" numberOfLines={1} className="text-base font-bold text-content-100">
          {title}
        </Text>
        {subtitle ? (
          <Text numberOfLines={1} className="text-[11px] text-content-400">
            {subtitle}
          </Text>
        ) : null}
      </View>
      {right ? <View className="flex-row items-center pr-1.5">{right}</View> : null}
    </View>
  );
}
