import { createContext, useCallback, useContext, useEffect, useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, View, type LayoutChangeEvent, type StyleProp, type ViewStyle } from 'react-native';
import Animated, {
  cancelAnimation,
  Easing,
  FadeInDown,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';
import { useFocusEffect } from 'expo-router';
import { Icon, Text, type IconGlyphProps } from '../../components/ui';
import { cn } from '../../utils/cn';

/**
 * The landing page's own building blocks.
 *
 * The page does not follow the app's theme: the artwork is built for a black
 * page, and a marketing screen that inverted under a visitor would read as
 * broken. So these are fixed values - the web's `.landing` variables - and the
 * brand yellow is repeated here so the page cannot drift when a theme
 * redefines the accent.
 */
export const LANDING = {
  /** `--landing-accent: 246 216 74` */
  accent: '#f6d84a',
  /** `--landing-card: 14 15 17` */
  card: '#0e0f11',
  /** Text on the accent. */
  ink: '#0b0c0e',
} as const;

/** The landing accent at an alpha, for SVG fills and gradients. */
export function accentAlpha(alpha: number): string {
  return 'rgba(246, 216, 74, ' + alpha + ')';
}

/** The page's easing: `rise`, `candle-grow` and the carousel all use it. */
export const RISE_EASING = Easing.bezier(0.22, 0.9, 0.3, 1);

/* --------------------------------------------------------------- motion -- */

/**
 * Whether the page's looping motion should run: the screen is in front and
 * nobody has asked their system to reduce motion. Motion is an enhancement,
 * never the thing that makes content appear - everything is drawn in place
 * first, and a loop that is switched off leaves a still frame.
 */
const LandingMotion = createContext(true);

export const LandingMotionProvider = LandingMotion.Provider;

export function useLandingMotion(): boolean {
  const active = useContext(LandingMotion);
  const reduced = useReducedMotion();
  return active && !reduced;
}

/** True while this screen is the one in front (not covered by a pushed screen). */
export function useScreenFocused(): boolean {
  const [focused, setFocused] = useState(true);
  useFocusEffect(
    useCallback(() => {
      setFocused(true);
      return () => setFocused(false);
    }, []),
  );
  return focused;
}

/**
 * The `float` keyframe: up 14px and back over seven seconds, slow enough that
 * the eye reads it as depth rather than motion.
 */
export function useFloatStyle() {
  const motion = useLandingMotion();
  const y = useSharedValue(0);

  useEffect(() => {
    if (!motion) {
      cancelAnimation(y);
      y.value = withTiming(0, { duration: 300 });
      return;
    }
    y.value = withRepeat(withTiming(-14, { duration: 3500, easing: Easing.inOut(Easing.ease) }), -1, true);
    return () => cancelAnimation(y);
  }, [motion, y]);

  return useAnimatedStyle(() => ({ transform: [{ translateY: y.value }] }));
}

/**
 * Plays an entrance as it mounts: content starts a little low and slightly
 * transparent and rises into place.
 *
 * The web plays this the first time a section scrolls into view. Here it is a
 * plain entrance - the content is laid out from the first frame either way,
 * and anyone who has asked for reduced motion gets it already in place.
 */
export function Reveal({
  children,
  delay = 0,
  className,
  style,
}: {
  children: ReactNode;
  delay?: number;
  className?: string;
  style?: StyleProp<ViewStyle>;
}) {
  const reduced = useReducedMotion();
  return (
    <Animated.View
      entering={reduced ? undefined : FadeInDown.duration(620).delay(delay).easing(RISE_EASING)}
      className={className}
      style={style}
    >
      {children}
    </Animated.View>
  );
}

/* ----------------------------------------------------------- scaffolding -- */

/** A titled section: heading and lead centred over its content. */
export function Section({
  title,
  lead,
  children,
  onLayout,
}: {
  /** Usually a string; a node when a line of it takes the brand colour. */
  title: ReactNode;
  lead: string;
  children: ReactNode;
  /** Where the section sits on the page, for the nav's anchors. */
  onLayout?: (event: LayoutChangeEvent) => void;
}) {
  return (
    <View onLayout={onLayout} className="px-4 py-20">
      <Reveal>
        <View className="items-center">
          <Text accessibilityRole="header" className="text-center text-3xl font-black tracking-tight text-white">
            {title}
          </Text>
          {lead ? <Text className="mt-4 text-center text-base leading-[26px] text-white/50">{lead}</Text> : null}
        </View>
      </Reveal>
      <View className="mt-12">{children}</View>
    </View>
  );
}

/** The big rounded panels the sections are built from (`.landing-card`). */
export function LandingCard({
  children,
  className,
  style,
}: {
  children: ReactNode;
  className?: string;
  style?: StyleProp<ViewStyle>;
}) {
  return (
    <View
      className={cn('overflow-hidden rounded-3xl border border-white/10 bg-[#0e0f11]', className)}
      style={style}
    >
      {children}
    </View>
  );
}

/** The pill used for every primary action on the page (`.landing-cta`). */
export function LandingCta({
  label,
  onPress,
  icon,
  className,
  textClassName,
  accessibilityLabel,
  maxFontSizeMultiplier,
}: {
  label: string;
  onPress: () => void;
  icon?: ComponentType<IconGlyphProps>;
  className?: string;
  textClassName?: string;
  accessibilityLabel?: string;
  /** For the top bar, where the row has no room to grow. */
  maxFontSizeMultiplier?: number;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={accessibilityLabel}
      onPress={onPress}
      className={cn(
        'flex-row items-center justify-center gap-2 rounded-full bg-[#f6d84a] px-6 py-3 active:opacity-85',
        className,
      )}
    >
      <Text
        numberOfLines={1}
        maxFontSizeMultiplier={maxFontSizeMultiplier}
        className={cn('text-base font-bold text-[#0b0c0e]', textClassName)}
      >
        {label}
      </Text>
      {icon ? <Icon as={icon} size={16} color={LANDING.ink} /> : null}
    </Pressable>
  );
}

/** The outlined secondary pill (`.landing-ghost`). */
export function LandingGhost({
  label,
  onPress,
  className,
  textClassName,
  maxFontSizeMultiplier,
}: {
  label: string;
  onPress: () => void;
  className?: string;
  textClassName?: string;
  maxFontSizeMultiplier?: number;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      onPress={onPress}
      className={cn(
        'flex-row items-center justify-center rounded-full border border-white/15 px-5 py-2.5 active:bg-white/10',
        className,
      )}
    >
      <Text
        numberOfLines={1}
        maxFontSizeMultiplier={maxFontSizeMultiplier}
        className={cn('text-sm font-semibold text-white/85', textClassName)}
      >
        {label}
      </Text>
    </Pressable>
  );
}

/** The "BT" mark from the top bar. */
export function BrandMark({ className }: { className?: string }) {
  return (
    <View className={cn('h-9 w-9 items-center justify-center rounded-xl bg-[#f6d84a]', className)}>
      <Text className="text-[13px] font-black text-black">BT</Text>
    </View>
  );
}
