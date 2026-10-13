import { useEffect, useState } from 'react';
import { View } from 'react-native';
import { createPicture, Skia } from '@shopify/react-native-skia';
import Animated, { runOnJS, useAnimatedReaction, useAnimatedStyle, useDerivedValue, useSharedValue } from 'react-native-reanimated';
import { ArrowRight, Flag } from 'lucide-react-native';
import { Icon, Text } from '../../../components/ui';
import { useNow } from '../../../hooks/useCountdown';
import { formatClock } from '../../../utils/time';
import { withAlpha, type ChartPalette } from '../../../utils/theme-colors';
import type { ChartHost } from './engine/host';
import { clipTo, fillPaint, strokePaint } from './engine/paint';
import { timeToLogicalTimes } from './timeProjection';
import { clamp, useFrameClock } from './useFrameLoop';

/**
 * Where a trade placed now would end (records 07 and 10).
 *
 * Before anything is placed, the chart shows the expiry of the duration the
 * ticket is set to: a vertical line at now plus that duration, with the clock
 * time on the time axis. It moves with the clock and jumps when the duration
 * changes, so changing 1m to 5m visibly moves the end of the trade rather than
 * only changing a number in the ticket (UX-07).
 *
 * It is a guide, not a position, and is drawn so it cannot be mistaken for one:
 *
 *   - neutral grey and dotted, where an order is yellow, green or red and
 *     dashed;
 *   - its label is a clock time on the time axis, where an order's is a
 *     countdown at the top of the plot;
 *   - no price, no entry, no tag - nothing in it says an amount is at stake.
 *
 * The page hides it while an order preview is on the chart: the preview is the
 * same line with a direction and an amount, and two of them on one instant
 * would only be noise.
 *
 * The line is a layer of the chart's canvas and the label a view moved on the
 * UI thread, so neither costs a React render as the clock or the view moves.
 */
interface Props {
  host: ChartHost;
  /** The ticket's duration. */
  expirySeconds: number;
  zone: string;
  palette: ChartPalette;
  performanceMode: boolean;
}

/** Clearance the label keeps from the ends of the time axis. */
const EDGE = 2;

interface Guide {
  visible: boolean;
  x: number;
  clamped: boolean;
  pw: number;
  ph: number;
  axisH: number;
}

export function ExpiryGuide({ host, expirySeconds, zone, palette, performanceMode }: Props) {
  const { engine } = host;
  const { view, series } = engine;
  const now = useFrameClock(engine, { maxFps: performanceMode ? 30 : 60 });
  const span = expirySeconds * 1000;

  const guide = useDerivedValue<Guide>(() => {
    const current = view.value;
    const data = series.value;
    const logical = timeToLogicalTimes(now.value + span, data.t, data.bucket);
    if (logical === null || !current.ok) {
      return { visible: false, x: 0, clamped: false, pw: current.pw, ph: current.ph, axisH: current.axisH };
    }
    const raw = current.x0 + logical * current.s;
    // Past the right edge - a long duration on a short frame - the line is
    // held at the edge and says so, rather than silently disappearing.
    return {
      visible: true,
      x: clamp(raw, 0, current.pw - 1),
      clamped: raw > current.pw - 1,
      pw: current.pw,
      ph: current.ph,
      axisH: current.axisH,
    };
  }, [span]);

  const rule = withAlpha(palette.text, 0.85);
  const picture = useDerivedValue(
    () =>
      createPicture((canvas) => {
        const g = guide.value;
        if (!g.visible) return;
        canvas.save();
        clipTo(canvas, 0, 0, g.pw, g.ph);
        const x = Math.round(g.x) + 0.5;
        const alpha = g.clamped ? 0.5 : 1;
        // Dotted, and finer than an order's dashes.
        const dots = strokePaint(rule, 1, [2, 3]);
        dots.setAlphaf(dots.getAlphaf() * alpha);
        canvas.drawLine(x, 0, x, g.ph, dots);
        // A pennant at the top: the finish of the trade, pointing on in time.
        const pennant = Skia.Path.Make();
        pennant.moveTo(x, 0.5);
        pennant.lineTo(x + 9.5, 0.5);
        pennant.lineTo(x + 6.9, 3.7);
        pennant.lineTo(x + 9.5, 6.9);
        pennant.lineTo(x, 6.9);
        pennant.close();
        const fill = fillPaint(rule);
        fill.setAlphaf(fill.getAlphaf() * alpha);
        canvas.drawPath(pennant, fill);
        canvas.restore();
      }),
    [rule],
  );

  useEffect(() => {
    host.setLayer('guide', picture);
    return () => host.setLayer('guide', null);
  }, [host, picture]);

  // The label's text changes once a second, which is all it shows.
  const second = useNow(1000);
  const text = formatClock(second + span, zone);

  const [clamped, setClamped] = useState(false);
  useAnimatedReaction(
    () => guide.value.clamped,
    (current, previous) => {
      if (current !== previous) runOnJS(setClamped)(current);
    },
  );

  const labelW = useSharedValue(110);
  const labelH = useSharedValue(18);
  // On the time axis, centred on the line - that is where an instant is read.
  // Without an axis (a very short chart), just above the plot's foot instead.
  const labelStyle = useAnimatedStyle(() => {
    const g = guide.value;
    const width = labelW.value;
    const height = labelH.value;
    const left = clamp(g.x - width / 2, EDGE, g.pw - width - EDGE);
    const top = g.axisH > height ? g.ph + (g.axisH - height) / 2 : g.ph - height - 4;
    return { opacity: g.visible ? 1 : 0, transform: [{ translateX: left }, { translateY: top }] };
  });

  return (
    <View pointerEvents="none" className="absolute inset-0 overflow-hidden" accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
      <Animated.View
        onLayout={(event) => {
          labelW.value = event.nativeEvent.layout.width;
          labelH.value = event.nativeEvent.layout.height;
        }}
        className="absolute left-0 top-0 flex-row items-center gap-1 rounded px-1.5 py-[3px]"
        style={[
          {
            backgroundColor: withAlpha(palette.panel, 0.94),
            borderWidth: 1,
            borderColor: withAlpha(palette.text, 0.4),
          },
          labelStyle,
        ]}
      >
        <Icon as={Flag} size={12} strokeWidth={2.25} color={palette.text} />
        <Text className="text-[10px] font-semibold leading-[12px]" style={{ color: palette.text }}>
          Expires
        </Text>
        <Text className="text-[10px] font-bold leading-[12px] tnum" style={{ color: palette.strongText }}>
          {text}
        </Text>
        {/* Only when the instant is past the right edge of the view. */}
        {clamped ? <Icon as={ArrowRight} size={12} strokeWidth={2.5} color={palette.text} /> : null}
      </Animated.View>
    </View>
  );
}
