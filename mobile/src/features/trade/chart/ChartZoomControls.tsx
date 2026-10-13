import { useState, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import Animated, { FadeIn, runOnJS, useAnimatedReaction } from 'react-native-reanimated';
import { ArrowRightToLine, Minus, Plus } from 'lucide-react-native';
import { Icon } from '../../../components/ui';
import { cn } from '../../../utils/cn';
import type { ChartEngine } from './engine/useChartEngine';

/**
 * Zoom out, zoom in, and back to the latest price (record 08).
 *
 * Every reference platform has these, and they matter most on touch: a pinch
 * cannot be made precise, and a trader who has dragged back through history
 * needs one tap to return to now rather than a long swipe the other way.
 *
 * Bottom-centre of the plot, in the toolbar's row. Where the toolbar leaves no
 * room there - a phone in advanced mode - the pair stands upright at the right
 * edge of the plot instead, in the gap the chart keeps ahead of the newest
 * candle, rather than landing on top of the toolbar or the candles.
 *
 * "Back to latest" appears only once the view has left the live edge. It hangs
 * off the end of the pair rather than sitting inside it, so the zoom buttons
 * do not shift under the thumb when it comes and goes.
 */
interface Props {
  engine: ChartEngine;
  /** Where the toolbar in the same row ends, which the controls must stay clear of. */
  toolbarRight: number;
  /** Distance from the bottom of the chart box, matching the toolbar's row. */
  bottom: number;
}

/** 36px targets: the smallest a thumb hits reliably (brief: touch-friendly). */
const BUTTON = 36;
const GAP = 4;
/** The pair. */
const PAIR = BUTTON * 2 + GAP;
/** "Back to latest", with the gap before it. */
const LATEST = BUTTON + GAP;
/** Clearance from the toolbar and from the price axis. */
const CLEAR = 8;

/** One press zooms by this factor. */
const STEP = 1.3;
/** Bar spacing bounds, in pixels: from a wide overview to a handful of candles. */
const MIN_SPACING = 2;
const MAX_SPACING = 48;

export function ChartZoomControls({ engine, toolbarRight, bottom }: Props) {
  const [following, setFollowing] = useState(true);
  const [plot, setPlot] = useState(0);
  const { rightOffset, restingOffset, view } = engine;

  // Whether "back to latest" is needed, from every movement of the view - but
  // only crossing the line reaches React.
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

  if (plot <= 0) return null;

  /*
   * Placement. Measured, not guessed: the toolbar's width depends on the mode
   * and the price axis's on the instrument, and a fixed breakpoint would be
   * wrong for one of them. Room is judged with "back to latest" showing, so
   * the controls do not jump between the two layouts as it comes and goes.
   */
  const left = Math.max(plot / 2 - PAIR / 2, toolbarRight + CLEAR);
  const column = !(left + PAIR + LATEST <= plot - CLEAR);
  const placedLeft = Math.round(column ? plot - CLEAR - BUTTON : left);

  /**
   * Following the live edge, a zoom keeps the newest candle where it is and
   * changes the bar width around it - the natural zoom for someone watching a
   * trade run. Anywhere else it zooms about the middle of the view, so the
   * stretch of history being read stays in front of the reader.
   */
  const zoom = (factor: number) => engine.zoom(factor, MIN_SPACING, MAX_SPACING);

  return (
    <View
      pointerEvents="box-none"
      className={cn('absolute items-center gap-1', column ? 'flex-col-reverse' : 'flex-row')}
      style={{ left: placedLeft, bottom }}
    >
      <View accessibilityRole="toolbar" accessibilityLabel="Chart zoom" className={cn('gap-1', column ? 'flex-col' : 'flex-row')}>
        {column ? (
          <>
            <ZoomButton label="Zoom in" onPress={() => zoom(STEP)}>
              <Icon as={Plus} size={16} className="text-content-300" />
            </ZoomButton>
            <ZoomButton label="Zoom out" onPress={() => zoom(1 / STEP)}>
              <Icon as={Minus} size={16} className="text-content-300" />
            </ZoomButton>
          </>
        ) : (
          <>
            <ZoomButton label="Zoom out" onPress={() => zoom(1 / STEP)}>
              <Icon as={Minus} size={16} className="text-content-300" />
            </ZoomButton>
            <ZoomButton label="Zoom in" onPress={() => zoom(STEP)}>
              <Icon as={Plus} size={16} className="text-content-300" />
            </ZoomButton>
          </>
        )}
      </View>

      {following ? null : (
        <Animated.View entering={FadeIn.duration(160)}>
          <Pressable
            onPress={engine.scrollToRealTime}
            accessibilityRole="button"
            accessibilityLabel="Back to latest price"
            className="h-9 w-9 items-center justify-center rounded-lg border border-accent/70 bg-surface-900/85 active:border-accent"
          >
            <Icon as={ArrowRightToLine} size={16} className="text-content-100" />
          </Pressable>
        </Animated.View>
      )}
    </View>
  );
}

function ZoomButton({ label, onPress, children }: { label: string; onPress: () => void; children: ReactNode }) {
  return (
    <Pressable
      onPress={onPress}
      accessibilityRole="button"
      accessibilityLabel={label}
      className="h-9 w-9 items-center justify-center rounded-lg border border-surface-700/70 bg-surface-900/80 active:opacity-70"
    >
      {children}
    </Pressable>
  );
}
