import { useState } from 'react';
import { Pressable } from 'react-native';
import Animated, { FadeIn, FadeOut, runOnJS, useAnimatedReaction } from 'react-native-reanimated';
import { ChevronsRight } from 'lucide-react-native';
import { Icon } from '../../../components/ui';
import { withAlpha } from '../../../utils/theme-colors';
import type { ChartLook } from './engine/colors';
import type { ChartEngine } from './engine/useChartEngine';

/**
 * Back to the latest price (record 08).
 *
 * Zoom is a pinch, as in TradingView's mobile app, so the chart no longer
 * carries permanent zoom buttons. What a phone still needs is one tap back to
 * now after dragging through history: this small button, at the right of the
 * plot just above the time axis, appears only once the view has left the
 * live edge and goes again when it is back.
 */
interface Props {
  engine: ChartEngine;
  look: ChartLook;
  /** Distance from the bottom of the chart box: just above the time axis. */
  bottom: number;
}

const BUTTON = 32;
/** Clearance from the price axis. */
const CLEAR = 8;

export function ChartZoomControls({ engine, look, bottom }: Props) {
  const [following, setFollowing] = useState(true);
  const [plot, setPlot] = useState(0);
  const { rightOffset, restingOffset, view } = engine;

  // Whether it is needed, from every movement of the view - but only crossing
  // the line reaches React.
  useAnimatedReaction(
    () => Math.abs(rightOffset.value - restingOffset.value) < 1.5,
    (now, before) => {
      if (now !== before) runOnJS(setFollowing)(now);
    },
  );

  // The plot's width, which the price axis takes its share of.
  useAnimatedReaction(
    () => Math.round(view.value.pw),
    (now, before) => {
      if (now !== before) runOnJS(setPlot)(now);
    },
  );

  if (following || plot <= 0) return null;

  return (
    <Animated.View
      entering={FadeIn.duration(160)}
      exiting={FadeOut.duration(120)}
      className="absolute"
      style={{ left: plot - CLEAR - BUTTON, bottom }}
    >
      <Pressable
        onPress={engine.scrollToRealTime}
        accessibilityRole="button"
        accessibilityLabel="Back to latest price"
        hitSlop={8}
        className="items-center justify-center rounded-md active:opacity-70"
        style={{
          width: BUTTON,
          height: BUTTON,
          backgroundColor: withAlpha(look.panel, 0.92),
          borderWidth: 1,
          borderColor: look.rule,
        }}
      >
        <Icon as={ChevronsRight} size={18} color={look.strongText} />
      </Pressable>
    </Animated.View>
  );
}
