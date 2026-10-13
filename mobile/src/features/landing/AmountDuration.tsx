import { useEffect, useState } from 'react';
import { StyleSheet, View } from 'react-native';
import Animated, {
  cancelAnimation,
  Easing,
  runOnJS,
  useAnimatedReaction,
  useAnimatedStyle,
  useSharedValue,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';
import Svg, { Circle, Defs, G, Line, LinearGradient, Path, Polygon, Rect, Stop, Text as SvgText } from 'react-native-svg';
import { Text } from '../../components/ui';
import { cn } from '../../utils/cn';
import { useTokenColor } from '../../utils/theme-colors';
import { LANDING, LandingCard, useLandingMotion } from './kit';

/**
 * "You choose the amount and duration" - an animated trade, running.
 *
 * A price line scrolls in from the right and meets the present at a dashed
 * rule; the level it is at right now is carried across to a price tab on the
 * edge. Behind it an order sits where it was placed - the same banner, dot,
 * run and expiry cap the live chart draws - and below, the expiry chips step
 * through the durations this platform actually offers, with the ticket's
 * Duration box following them.
 *
 * Everything moves from one value animated on the UI thread, which moves the
 * line and carries its level to the dot, the rule and the tab, so the card
 * costs the JavaScript thread nothing but a few price updates a second. It
 * only runs while the page is in front, and anyone who has asked for reduced
 * motion gets a still frame instead.
 *
 * The figures in the copy are this platform's: a ₹100 minimum stake and
 * expiries of 30 seconds, 1 minute and 5 minutes.
 */

/** One period of the price line, in viewBox units. The line is periodic in W. */
const W = 480;
/** The present: the line stops here and the price is read off it. */
const NOW_X = 300;
const EXPIRY_X = 364;
/** How fast the market scrolls, in viewBox units per millisecond. */
const SPEED = 0.028;
/** Where the still frame (and the first paint) sits. */
const START = 96;

const VIEW_W = 460;
const VIEW_H = 400;

const DURATIONS = ['30 sec', '1 min', '5 min'];

/**
 * The price line's height at x.
 *
 * A sum of sines whose frequencies are whole multiples of the period, so the
 * curve joins itself exactly at W and the scroll can loop forever without a
 * seam. Four terms is enough to read as a market rather than a wave.
 */
function yAt(x: number): number {
  'worklet';
  const t = (2 * Math.PI * x) / W;
  return (
    132 -
    38 * Math.sin(2 * t + 0.6) -
    22 * Math.sin(5 * t + 1.9) -
    10 * Math.sin(11 * t + 0.3) -
    5 * Math.sin(23 * t + 2.2)
  );
}

/** Two periods side by side, so a one-period shift always leaves it covered. */
const LINE_PATH = (() => {
  let d = '';
  for (let x = 0; x <= 2 * W; x += 2) d += (x === 0 ? 'M' : 'L') + x + ' ' + yAt(x).toFixed(2);
  return d;
})();

/** Maps a height on the chart to a price for the tab. Illustrative only. */
function priceFromY(y: number): string {
  'worklet';
  return (1900 + (240 - y) * 0.12).toFixed(2);
}

export function AmountDuration() {
  const motion = useLandingMotion();
  const [width, setWidth] = useState(0);
  const [active, setActive] = useState(1);

  // The expiry chips step through what is on offer.
  useEffect(() => {
    if (!motion) return;
    const timer = setInterval(() => setActive((i) => (i + 1) % DURATIONS.length), 2600);
    return () => clearInterval(timer);
  }, [motion]);

  return (
    <LandingCard>
      {/* The scene. */}
      <View
        className="relative w-full"
        style={{ aspectRatio: VIEW_W / VIEW_H }}
        onLayout={(event) => setWidth(event.nativeEvent.layout.width)}
      >
        {width > 0 ? <Scene width={width} motion={motion} /> : null}

        {/* The ticket, as it would look at this moment. */}
        <View className="absolute inset-x-0 items-center gap-2 px-4" style={{ top: '65%' }}>
          <View className="flex-row gap-2">
            {DURATIONS.map((label, index) => (
              <View
                key={label}
                className={cn('rounded-xl px-4 py-2', index === active ? 'bg-white/[0.14]' : 'bg-white/[0.05]')}
              >
                <Text className={cn('text-sm font-semibold', index === active ? 'text-white' : 'text-white/55')}>
                  {label}
                </Text>
              </View>
            ))}
          </View>
          <View className="w-full max-w-[280px] flex-row gap-2">
            <View className="flex-1 items-center rounded-2xl bg-white/[0.07] px-4 py-2.5">
              <Text className="text-[11px] text-white/45">Amount</Text>
              <Text className="text-base font-bold text-white tnum">₹100</Text>
            </View>
            <View className="flex-1 items-center rounded-2xl border border-white/5 bg-white/[0.04] px-4 py-2.5">
              <Text className="text-[11px] text-white/35">Duration</Text>
              <Text className="text-base font-bold text-white/80 tnum">{DURATIONS[active]}</Text>
            </View>
          </View>
        </View>
      </View>

      {/* The claim. */}
      <View className="items-center px-8 pb-12 pt-4">
        <Text accessibilityRole="header" className="text-center text-3xl font-black leading-[38px] tracking-tight text-white">
          You choose{'\n'}
          the{' '}
          <Text className="text-3xl font-black leading-[38px] tracking-tight text-[#f6d84a]">amount and duration</Text>
          {'\n'}of your trade
        </Text>
        <Text className="mt-5 text-center text-base leading-[26px] text-white/50">
          Open trades from ₹100, with expiries as short as 30 seconds.
        </Text>
      </View>
    </LandingCard>
  );
}

/**
 * The chart behind the ticket, drawn in layers so that only plain views move:
 * the grid and the fade are still pictures, the line slides inside a clipped
 * window, and the dot, the rule and the tab ride on the line's level.
 */
function Scene({ width, motion }: { width: number; motion: boolean }) {
  const up = useTokenColor('up');
  const s = width / VIEW_W;
  const height = VIEW_H * s;

  /** How far the market has scrolled, 0..W, beyond the still frame. */
  const travel = useSharedValue(0);
  const lastText = useSharedValue(0);
  const [price, setPrice] = useState(() => priceFromY(yAt(NOW_X + START)));

  // The market: scroll the line on the UI thread. Resuming carries on from
  // where it stopped rather than jumping back to the start.
  useEffect(() => {
    if (!motion) {
      cancelAnimation(travel);
      return;
    }
    const from = travel.value % W;
    travel.value = from;
    travel.value = withRepeat(withTiming(from + W, { duration: W / SPEED, easing: Easing.linear }), -1, false);
    return () => cancelAnimation(travel);
  }, [motion, travel]);

  // Digits change a few times a second, not every frame, so the tab reads as
  // a quote updating rather than a number flickering.
  useAnimatedReaction(
    () => travel.value,
    (value) => {
      const now = Date.now();
      if (now - lastText.value < 220) return;
      lastText.value = now;
      runOnJS(setPrice)(priceFromY(yAt((NOW_X + START + value) % W)));
    },
    [],
  );

  const lineStyle = useAnimatedStyle(() => ({
    transform: [{ translateX: -((START + travel.value) % W) * s }],
  }));
  const levelStyle = useAnimatedStyle(() => ({
    transform: [{ translateY: yAt((NOW_X + START + travel.value) % W) * s }],
  }));

  const rule = Math.max(StyleSheet.hairlineWidth, s);

  return (
    <View
      pointerEvents="none"
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      style={StyleSheet.absoluteFill}
    >
      {/* The present and the expiry. */}
      <Svg width={width} height={height} viewBox={`0 0 ${VIEW_W} ${VIEW_H}`} style={StyleSheet.absoluteFill}>
        <Line x1={NOW_X} y1="0" x2={NOW_X} y2={VIEW_H} stroke="rgba(255,255,255,0.35)" strokeDasharray="3 4" />
        <Line x1={EXPIRY_X} y1="0" x2={EXPIRY_X} y2={VIEW_H} stroke="rgba(255,255,255,0.35)" />
      </Svg>

      {/* The market, clipped at the present. */}
      <View style={{ position: 'absolute', left: 0, top: 0, width: NOW_X * s, height, overflow: 'hidden' }}>
        <Animated.View style={[{ width: 2 * W * s, height }, lineStyle]}>
          <Svg width={2 * W * s} height={height} viewBox={`0 0 ${2 * W} ${VIEW_H}`}>
            <Path d={LINE_PATH} fill="none" stroke={up} strokeWidth="2" strokeLinejoin="round" />
          </Svg>
        </Animated.View>
      </View>

      {/* History fades out to the left; only the recent part is sharp. */}
      <Svg width={width} height={height} viewBox={`0 0 ${VIEW_W} ${VIEW_H}`} style={StyleSheet.absoluteFill}>
        <Defs>
          <LinearGradient id="ad-fade" x1="0" y1="0" x2="140" y2="0" gradientUnits="userSpaceOnUse">
            <Stop offset="0" stopColor={LANDING.card} stopOpacity="1" />
            <Stop offset="1" stopColor={LANDING.card} stopOpacity="0" />
          </LinearGradient>
        </Defs>
        <Rect x="0" y="0" width="140" height={VIEW_H} fill="url(#ad-fade)" />
      </Svg>

      {/* The level right now, carried across to its tab. */}
      <Animated.View
        style={[
          { position: 'absolute', left: 0, top: -rule / 2, width: 394 * s, height: rule },
          { backgroundColor: 'rgba(255,255,255,0.75)' },
          levelStyle,
        ]}
      />
      <Animated.View
        style={[
          {
            position: 'absolute',
            left: (NOW_X - 11) * s,
            top: -11 * s,
            width: 22 * s,
            height: 22 * s,
            borderRadius: 11 * s,
            backgroundColor: 'rgba(255,255,255,0.06)',
            alignItems: 'center',
            justifyContent: 'center',
          },
          levelStyle,
        ]}
      >
        <View style={{ width: 8 * s, height: 8 * s, borderRadius: 4 * s, backgroundColor: '#fff' }} />
      </Animated.View>
      <Animated.View
        style={[
          {
            position: 'absolute',
            left: 378 * s,
            top: -15 * s,
            width: 76 * s,
            height: 30 * s,
            borderRadius: 15 * s,
            backgroundColor: '#fff',
            alignItems: 'center',
            justifyContent: 'center',
          },
          levelStyle,
        ]}
      >
        <Text
          allowFontScaling={false}
          className="font-bold tnum"
          style={{ fontSize: 15 * s, lineHeight: 18 * s, color: LANDING.ink }}
        >
          {price}
        </Text>
      </Animated.View>

      {/* An UP order placed a moment ago, now in the money. */}
      <Svg width={width} height={height} viewBox={`0 0 ${VIEW_W} ${VIEW_H}`} style={StyleSheet.absoluteFill}>
        <Line x1="236" y1="236" x2={EXPIRY_X} y2="236" stroke={LANDING.accent} strokeWidth="2" />
        <Polygon points={`${EXPIRY_X},229 ${EXPIRY_X - 5},236 ${EXPIRY_X + 5},236`} fill={LANDING.accent} />
        <Circle cx="236" cy="236" r="5" fill={LANDING.accent} />
        <G transform="translate(142, 221)">
          <Path d="M0 15 A15 15 0 0 1 15 0 H78 L88 15 L78 30 H15 A15 15 0 0 1 0 15 Z" fill={LANDING.accent} />
          <SvgText x="14" y="20" fontSize="14" fontWeight="800" fill={LANDING.ink}>
            ₹500
          </SvgText>
          <SvgText x="62" y="20.5" fontSize="15" fontWeight="900" fill={LANDING.ink}>
            ↑
          </SvgText>
        </G>
      </Svg>
    </View>
  );
}
