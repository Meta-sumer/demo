import { useEffect, type ReactNode } from 'react';
import Animated, {
  cancelAnimation,
  Easing,
  useAnimatedStyle,
  useSharedValue,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';

/**
 * The web's `animate-order-pulse`: opacity 0.55 to 1 and back over 1.4 s,
 * for as long as `active` - an expiry in its last ten seconds.
 */
export function Pulse({ active, children }: { active: boolean; children: ReactNode }) {
  const opacity = useSharedValue(1);
  useEffect(() => {
    if (active) {
      opacity.value = 0.55;
      opacity.value = withRepeat(withTiming(1, { duration: 700, easing: Easing.inOut(Easing.ease) }), -1, true);
    } else {
      cancelAnimation(opacity);
      opacity.value = 1;
    }
  }, [active, opacity]);
  const animated = useAnimatedStyle(() => ({ opacity: opacity.value }));
  return <Animated.View style={animated}>{children}</Animated.View>;
}

/** The same rhythm as a value, for things a worklet draws: 0.55-1, period 1.4 s. */
export function pulseAt(now: number): number {
  'worklet';
  const phase = (now % 1400) / 1400;
  return 0.55 + 0.45 * (0.5 - 0.5 * Math.cos(phase * Math.PI * 2));
}
