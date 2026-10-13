import { useEffect, useState, type ReactNode } from 'react';
import { KeyboardAvoidingView, Modal as RNModal, Platform, Pressable, ScrollView, View } from 'react-native';
import { Gesture, GestureDetector, GestureHandlerRootView } from 'react-native-gesture-handler';
import Animated, {
  Easing,
  runOnJS,
  useAnimatedStyle,
  useSharedValue,
  withSpring,
  withTiming,
} from 'react-native-reanimated';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { X } from 'lucide-react-native';
import { cn } from '../../utils/cn';
import { useThemeStore } from '../../stores/themeStore';
import { SCRIM_ALPHA, tokenColor } from '../../theme/tokens';
import { Icon } from './Icon';
import { Text } from './Text';

/**
 * A bottom sheet.
 *
 * The phone form of every panel the web client opened over the workspace -
 * the market selector, the duration picker, the deposit flow, help. It rises
 * from where the thumb already is, keeps the top of the screen in view, and
 * goes back down with a drag on its header, a tap on the veil or the system
 * back gesture.
 *
 * It stays mounted while it animates out, so closing reads as sending it back
 * rather than deleting it.
 */
const OPEN_MS = 280;
const CLOSE_MS = 220;
/** How far the sheet has to be dragged down before letting go closes it. */
const DISMISS_DRAG_PX = 90;
const EASE = Easing.bezier(0.32, 0.72, 0, 1);

export interface SheetProps {
  open: boolean;
  onClose: () => void;
  title: string;
  /**
   * Drawn in place of the title text - a title with a word in colour, say.
   * `title` still names the sheet for screen readers and the close buttons.
   */
  heading?: ReactNode;
  /**
   * How dark the veil is, 0-1. Defaults to the theme's scrim. Lighter for a
   * sheet whose point is what stays visible behind it (the trade
   * confirmation, with the order preview on the chart).
   */
  scrimAlpha?: number;
  /** A line under the title. */
  subtitle?: ReactNode;
  /** Between the title and the scrolling body, e.g. a switch or a search box. */
  toolbar?: ReactNode;
  /** Pinned under the body, e.g. a confirm button. */
  footer?: ReactNode;
  /** `auto` sizes to the content up to 85% of the screen; `large` always takes 92%. */
  size?: 'auto' | 'large';
  /**
   * Whether the body scrolls. Turn it off when the body brings its own list
   * (a FlatList) so the two do not nest.
   */
  scroll?: boolean;
  /** Body padding; the default suits a list of rows that bring their own. */
  bodyClassName?: string;
  children: ReactNode;
}

export function Sheet({
  open,
  onClose,
  title,
  heading,
  scrimAlpha,
  subtitle,
  toolbar,
  footer,
  size = 'auto',
  scroll = true,
  bodyClassName,
  children,
}: SheetProps) {
  const [mounted, setMounted] = useState(open);
  const progress = useSharedValue(0);
  const drag = useSharedValue(0);
  const travel = useSharedValue(900);
  const insets = useSafeAreaInsets();
  const theme = useThemeStore((s) => s.resolved);
  const scrim = tokenColor(theme, 'scrim', scrimAlpha ?? SCRIM_ALPHA[theme]);

  useEffect(() => {
    if (open) {
      setMounted(true);
      return;
    }
    if (!mounted) return;
    progress.value = withTiming(0, { duration: CLOSE_MS, easing: EASE }, (finished) => {
      if (finished) runOnJS(setMounted)(false);
    });
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (!mounted || !open) return;
    drag.value = 0;
    progress.value = withTiming(1, { duration: OPEN_MS, easing: EASE });
  }, [mounted, open]); // eslint-disable-line react-hooks/exhaustive-deps

  const pan = Gesture.Pan()
    .activeOffsetY(6)
    .onUpdate((event) => {
      drag.value = Math.max(0, event.translationY);
    })
    .onEnd((event) => {
      if (event.translationY > DISMISS_DRAG_PX || event.velocityY > 900) {
        runOnJS(onClose)();
      } else {
        drag.value = withSpring(0, { damping: 22, stiffness: 260 });
      }
    });

  const veilStyle = useAnimatedStyle(() => ({ opacity: progress.value }));
  const panelStyle = useAnimatedStyle(() => ({
    transform: [{ translateY: (1 - progress.value) * travel.value + drag.value }],
  }));

  if (!mounted) return null;

  return (
    <RNModal
      transparent
      visible
      animationType="none"
      statusBarTranslucent
      navigationBarTranslucent
      onRequestClose={onClose}
    >
      <GestureHandlerRootView style={{ flex: 1 }}>
        <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} style={{ flex: 1 }}>
          <View style={{ flex: 1, justifyContent: 'flex-end' }}>
            <Animated.View style={[{ position: 'absolute', inset: 0, backgroundColor: scrim }, veilStyle]}>
              <Pressable
                style={{ flex: 1 }}
                onPress={onClose}
                accessibilityRole="button"
                accessibilityLabel={'Close ' + title}
              />
            </Animated.View>

            <Animated.View
              accessibilityViewIsModal
              onLayout={(event) => {
                travel.value = event.nativeEvent.layout.height + 40;
              }}
              className={cn(
                'overflow-hidden rounded-t-2xl border border-b-0 border-surface-700 bg-surface-900',
                size === 'large' ? 'h-[92%]' : 'max-h-[85%]',
              )}
              style={panelStyle}
            >
              <GestureDetector gesture={pan}>
                <View>
                  <View className="items-center pb-1 pt-2.5">
                    <View className="h-1 w-10 rounded-full bg-surface-600" />
                  </View>
                  <View className="flex-row items-start gap-3 px-4 pb-2 pt-1">
                    <View className="min-w-0 flex-1">
                      {heading ? (
                        <View accessible accessibilityRole="header" accessibilityLabel={title}>
                          {heading}
                        </View>
                      ) : (
                        <Text accessibilityRole="header" className="text-base font-bold text-content-100">
                          {title}
                        </Text>
                      )}
                      {subtitle ? (
                        typeof subtitle === 'string' ? (
                          <Text className="mt-0.5 text-xs text-content-400">{subtitle}</Text>
                        ) : (
                          subtitle
                        )
                      ) : null}
                    </View>
                    <Pressable
                      onPress={onClose}
                      hitSlop={8}
                      accessibilityRole="button"
                      accessibilityLabel={'Close ' + title}
                      className="-mr-1.5 rounded-lg p-1.5 active:bg-surface-850"
                    >
                      <Icon as={X} size={18} className="text-content-400" />
                    </Pressable>
                  </View>
                  {toolbar ? <View className="px-4 pb-3">{toolbar}</View> : null}
                </View>
              </GestureDetector>

              {scroll ? (
                <ScrollView
                  className="shrink"
                  contentContainerClassName={bodyClassName}
                  contentContainerStyle={footer ? undefined : { paddingBottom: insets.bottom + 12 }}
                  keyboardShouldPersistTaps="handled"
                  showsVerticalScrollIndicator={false}
                >
                  {children}
                </ScrollView>
              ) : (
                <View
                  className={cn(size === 'large' ? 'flex-1' : 'shrink', bodyClassName)}
                  style={footer ? undefined : { paddingBottom: insets.bottom }}
                >
                  {children}
                </View>
              )}

              {footer ? (
                <View
                  className="border-t border-surface-800 px-4 pt-3"
                  style={{ paddingBottom: Math.max(insets.bottom, 12) }}
                >
                  {footer}
                </View>
              ) : null}
            </Animated.View>
          </View>
        </KeyboardAvoidingView>
      </GestureHandlerRootView>
    </RNModal>
  );
}

/**
 * A larger dialog - the deposit and withdrawal flows. On a phone it is a tall
 * sheet with padded content; the page behind stays mounted, so funding an
 * account never costs sight of an open position.
 */
export function Modal(props: Omit<SheetProps, 'size' | 'bodyClassName'> & { bodyClassName?: string }) {
  return <Sheet {...props} size="large" bodyClassName={cn('px-5 pb-5 pt-2', props.bodyClassName)} />;
}
