import { memo, useEffect, useState } from 'react';
import { StyleSheet, View, useWindowDimensions, type ViewStyle } from 'react-native';
import Animated, {
  cancelAnimation,
  Easing,
  useAnimatedProps,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withDelay,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';
import { LinearGradient } from 'expo-linear-gradient';
import { Image } from 'expo-image';
import Svg, {
  Circle,
  Defs,
  G,
  Line,
  LinearGradient as SvgLinearGradient,
  Mask,
  Path,
  Polygon,
  RadialGradient,
  Rect,
  Stop,
  Text as SvgText,
} from 'react-native-svg';
import { Text } from '../../components/ui';
import { useTokenColor } from '../../utils/theme-colors';
import { LANDING, RISE_EASING, accentAlpha, useFloatStyle, useLandingMotion } from './kit';

/**
 * The landing page's artwork: the hero's glow, photograph and rising bars, and
 * the four pictures on the highlight cards.
 *
 * All of it is drawn, not shipped as images - it carries the brand colour,
 * cannot pixelate, and costs nothing to download. None of it says anything a
 * screen reader needs, except the order marker, which is labelled.
 */

const AnimatedPath = Animated.createAnimatedComponent(Path);

/* ---------------------------------------------------------- rising bars -- */

/**
 * The rising bars along the foot of the hero.
 *
 * Eleven capsules stepping upward, fading into the page - the market going the
 * way the trader called it, which is the whole promise of the section above.
 *
 * Generated from a shape rather than hand-drawn, so it can take the brand
 * colour, animate in, and be re-proportioned by changing numbers instead of
 * path data. `BARS` is the outline in the original 340x367 space: the top and
 * bottom of each capsule, left to right.
 */
const BARS: Array<[x: number, top: number, height: number]> = [
  [0, 271.493, 90.205],
  [31.125, 243.97, 90.205],
  [62.373, 183.54, 175.784],
  [93.617, 214.771, 109.174],
  [124.861, 200.899, 90.206],
  [156.102, 193.899, 109.175],
  [187.539, 173.853, 127.773],
  [218.969, 145.017, 109.174],
  [250.586, 174.153, 90.205],
  [282.203, 80.474, 114.298],
  [314.148, 11.017, 114.298],
];

const BAR_WIDTH = 25.852;
/** How much higher the right edge of a bar sits than its left. */
const BAR_SHEAR = 6.551;
const BAR_RADIUS = 5.7;

/**
 * One capsule.
 *
 * Not a rounded rectangle: the left and right edges are vertical but the top
 * and bottom are slanted, so each bar leans as though it were being read at an
 * angle. That tilt is the whole character of the shape, and a plain `rect`
 * loses it.
 *
 * Built by walking the four corners and rounding each one, so the geometry
 * stays legible and the tilt is a number that can be changed rather than a
 * string of bezier control points.
 *
 * `grow` (0 to 1) squashes the finished shape towards its foot - the web's
 * `candle-grow`, a vertical scale from the bottom left corner - so the bar can
 * rise into place one frame at a time on the UI thread.
 */
function barPath(x: number, top: number, height: number, grow: number): string {
  'worklet';
  const foot = top + height;
  const corners = [
    { x, y: top }, // top left
    { x: x + BAR_WIDTH, y: top - BAR_SHEAR }, // top right, lifted by the shear
    { x: x + BAR_WIDTH, y: top - BAR_SHEAR + height }, // bottom right
    { x, y: top + height }, // bottom left
  ];

  const unit = (from: { x: number; y: number }, to: { x: number; y: number }) => {
    const dx = to.x - from.x;
    const dy = to.y - from.y;
    const length = Math.hypot(dx, dy) || 1;
    return { x: dx / length, y: dy / length };
  };
  const point = (px: number, py: number) => px.toFixed(2) + ' ' + (foot + (py - foot) * grow).toFixed(2);
  const along = (p: { x: number; y: number }, u: { x: number; y: number }, d: number) =>
    point(p.x + u.x * d, p.y + u.y * d);

  let d = '';
  for (let i = 0; i < 4; i += 1) {
    const here = corners[i]!;
    const next = corners[(i + 1) % 4]!;
    const into = unit(here, next);
    const from = unit(corners[(i + 3) % 4]!, here);

    // Round the corner we are standing on, then run to the next one.
    d += (i === 0 ? 'M' : 'L') + along(here, from, -BAR_RADIUS);
    d += 'Q' + point(here.x, here.y) + ' ' + along(here, into, BAR_RADIUS);
    d += 'L' + along(next, into, -BAR_RADIUS);
  }
  return d + 'Z';
}

export const RisingBars = memo(function RisingBars() {
  const { height: screenHeight } = useWindowDimensions();
  const reduced = useReducedMotion();
  const height = Math.min(screenHeight * 0.34, 330);

  return (
    <View
      pointerEvents="none"
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      style={{ position: 'absolute', left: 0, right: 0, bottom: 0, alignItems: 'center' }}
    >
      <Svg width={340} height={height} viewBox="0 0 340 367" preserveAspectRatio="xMidYMax meet">
        <Defs>
          {/* Flat to halfway down, then away to black - so the bars read as
              standing in the dark rather than sitting on a band. */}
          <SvgLinearGradient id="landing-bars" x1="170" y1="0" x2="170" y2="366.164" gradientUnits="userSpaceOnUse">
            <Stop offset="0.5" stopColor="#1b1b1b" />
            <Stop offset="1" stopColor="#000000" />
          </SvgLinearGradient>
          {/* They rise out of the bottom edge rather than standing in front of
              the copy, so the tops dissolve before they reach the chips. */}
          <SvgLinearGradient id="landing-bars-fade" x1="0" y1="0" x2="0" y2="367" gradientUnits="userSpaceOnUse">
            <Stop offset="0.02" stopColor="#ffffff" stopOpacity="0" />
            <Stop offset="0.68" stopColor="#ffffff" stopOpacity="1" />
          </SvgLinearGradient>
          <Mask id="landing-bars-mask" maskUnits="userSpaceOnUse" x="0" y="0" width="340" height="367">
            <Rect x="0" y="0" width="340" height="367" fill="url(#landing-bars-fade)" />
          </Mask>
        </Defs>

        <G mask="url(#landing-bars-mask)">
          {BARS.map(([x, top, barHeight], index) => (
            <Bar key={index} x={x} top={top} height={barHeight} index={index} animate={!reduced} />
          ))}
        </G>
      </Svg>
    </View>
  );
});

function Bar({ x, top, height, index, animate }: { x: number; top: number; height: number; index: number; animate: boolean }) {
  const grow = useSharedValue(animate ? 0 : 1);

  useEffect(() => {
    if (!animate) return;
    grow.value = withDelay(120 + index * 55, withTiming(1, { duration: 620, easing: RISE_EASING }));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const animatedProps = useAnimatedProps(() => ({ d: barPath(x, top, height, grow.value) }));

  return <AnimatedPath animatedProps={animatedProps} fill="url(#landing-bars)" />;
}

/* ------------------------------------------------------------- portrait -- */

/**
 * The photograph behind the hero.
 *
 * An optional file rather than a shipped asset. Save a picture as
 * `mobile/assets/hero.jpg` and set this to `require('../../../assets/hero.jpg')`
 * and it appears behind the headline; leave it out and the hero is lit by the
 * gradient backdrop alone. Nothing here ships a stock photo of its own: a face
 * on a trading app reads as a customer or an endorsement, and that is the
 * owner's call to make, not a default to inherit. (`client/public/README.md`
 * says what works in that slot.)
 *
 * The treatment is what makes a photograph usable as a background rather than
 * a distraction - it is dimmed, and faded out at the top and bottom so the nav
 * has something to sit on and the headline keeps its contrast wherever the
 * subject happens to be.
 */
const HERO_PHOTO = null as number | null;

export function HeroPortrait() {
  const [failed, setFailed] = useState(false);
  const reduced = useReducedMotion();

  // No photograph, nothing drawn in its place: the glow and the rising bars
  // carry the hero on their own.
  if (HERO_PHOTO == null || failed) return null;

  return (
    <View
      pointerEvents="none"
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      style={[StyleSheet.absoluteFill, { overflow: 'hidden' }]}
    >
      <Image
        source={HERO_PHOTO}
        onError={() => setFailed(true)}
        contentFit="cover"
        contentPosition="top"
        transition={reduced ? 0 : 180}
        style={StyleSheet.absoluteFill}
      />
      {/* The web's brightness(0.72). */}
      <View style={[StyleSheet.absoluteFill, { backgroundColor: 'rgba(0,0,0,0.28)' }]} />
      {/* Readability, from three directions: down from the nav, up from the
          chips, and in from the sides. */}
      <LinearGradient
        colors={['#000', 'rgba(0,0,0,0.42)', 'rgba(0,0,0,0.55)', 'rgba(0,0,0,0.82)', '#000']}
        locations={[0, 0.28, 0.52, 0.78, 0.96]}
        style={StyleSheet.absoluteFill}
      />
      <LinearGradient
        colors={['#000', 'rgba(0,0,0,0)', 'rgba(0,0,0,0)', '#000']}
        locations={[0, 0.22, 0.78, 1]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 0 }}
        style={StyleSheet.absoluteFill}
      />
    </View>
  );
}

/* ------------------------------------------------------------- backdrop -- */

/** How far the web's `blur-[120px]` carries a glow past its edge. */
const GLOW_BLUR = 120;

/**
 * The glow behind the hero.
 *
 * Two slow-drifting radial washes rather than an image: it carries the brand
 * colour into the page, costs nothing to load, and cannot pixelate. The blur
 * the web applies is drawn here as the gradient's own falloff.
 */
export const Backdrop = memo(function Backdrop() {
  const up = useTokenColor('up');
  return (
    <View
      pointerEvents="none"
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      style={[StyleSheet.absoluteFill, { overflow: 'hidden' }]}
    >
      <Glow
        id="glow-accent"
        size={560}
        color={LANDING.accent}
        alpha={0.16}
        style={{ top: -160 - GLOW_BLUR, left: '50%', marginLeft: -(560 / 2 + GLOW_BLUR) }}
      />
      <Glow
        id="glow-up"
        size={420}
        color={up}
        alpha={0.12}
        reverse
        style={{ right: -128 - GLOW_BLUR, top: '33%', marginTop: -GLOW_BLUR }}
      />
      <LinearGradient
        colors={['rgba(0,0,0,0)', '#000']}
        style={{ position: 'absolute', left: 0, right: 0, bottom: 0, height: 224 }}
      />
    </View>
  );
});

/**
 * One wash, drifting on the web's 18-second `drift`. `reverse` starts it half
 * a cycle on, so the two never move together.
 */
function Glow({
  id,
  size,
  color,
  alpha,
  reverse = false,
  style,
}: {
  id: string;
  size: number;
  color: string;
  alpha: number;
  reverse?: boolean;
  style: ViewStyle;
}) {
  const motion = useLandingMotion();
  const t = useSharedValue(reverse ? 1 : 0);

  useEffect(() => {
    if (!motion) {
      cancelAnimation(t);
      return;
    }
    t.value = withRepeat(
      withTiming(reverse ? 0 : 1, { duration: 9000, easing: Easing.inOut(Easing.ease) }),
      -1,
      true,
    );
    return () => cancelAnimation(t);
  }, [motion, reverse, t]);

  const drift = useAnimatedStyle(() => ({
    transform: [{ translateX: 18 * t.value }, { translateY: -24 * t.value }, { scale: 1 + 0.06 * t.value }],
  }));

  const extent = size + 2 * GLOW_BLUR;
  const half = extent / 2;
  return (
    <Animated.View style={[{ position: 'absolute', width: extent, height: extent }, style, drift]}>
      <Svg width={extent} height={extent}>
        <Defs>
          <RadialGradient id={id} cx="50%" cy="50%" r="50%">
            <Stop offset="0" stopColor={color} stopOpacity={alpha} />
            <Stop offset={(size / 2 - GLOW_BLUR) / half} stopColor={color} stopOpacity={alpha} />
            <Stop offset={size / 2 / half} stopColor={color} stopOpacity={alpha * 0.5} />
            <Stop offset="1" stopColor={color} stopOpacity="0" />
          </RadialGradient>
        </Defs>
        <Circle cx={half} cy={half} r={half} fill={`url(#${id})`} />
      </Svg>
    </Animated.View>
  );
}

/* --------------------------------------------------------- card artwork -- */

/** A stack of coins, lit from the top left. */
export function CoinArt() {
  const floatStyle = useFloatStyle();
  return (
    <Animated.View style={[{ width: 160, height: 160 }, floatStyle]}>
      {/* The lower two coins, back to front. */}
      {[2, 1].map((i) => (
        <View key={i} style={{ position: 'absolute', left: 0, top: 16 + i * 9 }}>
          <Svg width={128} height={128}>
            <Defs>
              <SvgLinearGradient id={'coin-edge-' + i} x1="0" y1="0" x2="0" y2="1">
                <Stop offset="0" stopColor="#8d761a" />
                <Stop offset="1" stopColor="#5d4d0f" />
              </SvgLinearGradient>
            </Defs>
            <Circle cx="64" cy="64" r="64" fill={'url(#coin-edge-' + i + ')'} />
          </Svg>
        </View>
      ))}

      {/* The top coin's glow, which falls over the coins beneath it. */}
      <View style={{ position: 'absolute', left: -32, top: 2 }}>
        <Svg width={192} height={192}>
          <Defs>
            <RadialGradient id="coin-glow" cx="50%" cy="50%" r="50%">
              <Stop offset="0" stopColor={LANDING.accent} stopOpacity="0.6" />
              <Stop offset="0.31" stopColor={LANDING.accent} stopOpacity="0.5" />
              <Stop offset="0.54" stopColor={LANDING.accent} stopOpacity="0.25" />
              <Stop offset="1" stopColor={LANDING.accent} stopOpacity="0" />
            </RadialGradient>
          </Defs>
          <Circle cx="96" cy="96" r="96" fill="url(#coin-glow)" />
        </Svg>
      </View>

      <View style={{ position: 'absolute', left: 0, top: 16 }}>
        <Svg width={128} height={128}>
          <Defs>
            <RadialGradient id="coin-face" cx="32%" cy="28%" fx="32%" fy="28%" r="99%">
              <Stop offset="0" stopColor="#fff6c9" />
              <Stop offset="0.42" stopColor={LANDING.accent} />
              <Stop offset="1" stopColor="#9e8412" />
            </RadialGradient>
          </Defs>
          <Circle cx="64" cy="64" r="64" fill="url(#coin-face)" />
        </Svg>
      </View>

      <View
        style={{ position: 'absolute', left: 0, top: 16, width: 128, height: 128 }}
        className="items-center justify-center"
      >
        <Text className="text-2xl font-black text-black/70">₹100</Text>
      </View>
    </Animated.View>
  );
}

/** A clock whose second hand is the only thing moving. */
export function ClockArt() {
  const hand = (angle: number, length: number) => {
    const radians = (angle * Math.PI) / 180;
    return { x2: 80 + Math.cos(radians) * length, y2: 80 + Math.sin(radians) * length };
  };
  const minute = hand(-58, 56);
  const second = hand(-12, 72);

  return (
    <View
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      style={{ width: 160, height: 160 }}
    >
      <Svg width={160} height={160} viewBox="0 0 160 160">
        <Circle cx="80" cy="80" r="79" fill="none" stroke="rgba(255,255,255,0.1)" strokeWidth="2" />
        {Array.from({ length: 60 }, (_, i) => {
          const w = i % 5 === 0 ? 3 : 1.5;
          const h = i % 5 === 0 ? 10 : 6;
          return (
            <Rect
              key={i}
              x={80 - w / 2}
              y={0}
              width={w}
              height={h}
              rx={w / 2}
              fill={i < 5 ? LANDING.accent : 'rgba(255,255,255,0.22)'}
              transform={`rotate(${i * 6}, 80, 80)`}
            />
          );
        })}
        <Line x1="80" y1="80" {...minute} stroke="rgba(255,255,255,0.7)" strokeWidth="3" strokeLinecap="round" />
        <Line x1="80" y1="80" {...second} stroke={LANDING.accent} strokeWidth="3" strokeLinecap="round" />
        <Circle cx="80" cy="80" r="6" fill="#fff" />
      </Svg>
    </View>
  );
}

/** A rising run of candles that grows as it appears. */
export function ChartArt() {
  const upTop = useTokenColor('up', 0.85);
  const upFoot = useTokenColor('up', 0.2);
  const bars = [30, 44, 38, 56, 50, 68, 62, 84];
  return (
    <View
      accessibilityElementsHidden
      importantForAccessibility="no-hide-descendants"
      className="h-40 w-full flex-row items-end justify-center gap-2.5"
    >
      {bars.map((height, i) => (
        <GrowingCandle
          key={i}
          index={i}
          height={(160 * height) / 100}
          colors={i === bars.length - 1 ? [LANDING.accent, accentAlpha(0.35)] : [upTop, upFoot]}
        />
      ))}
    </View>
  );
}

function GrowingCandle({ index, height, colors }: { index: number; height: number; colors: [string, string] }) {
  const reduced = useReducedMotion();
  const grow = useSharedValue(reduced ? 1 : 0);

  useEffect(() => {
    if (reduced) return;
    grow.value = withDelay(index * 70, withTiming(1, { duration: 620, easing: RISE_EASING }));
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const style = useAnimatedStyle(() => ({ transform: [{ scaleY: grow.value }] }));

  return (
    <Animated.View
      style={[{ width: 20, height, borderRadius: 6, overflow: 'hidden', transformOrigin: 'bottom' }, style]}
    >
      <LinearGradient colors={colors} style={{ flex: 1 }} />
    </Animated.View>
  );
}

/** The order marker the live chart actually draws. */
export function OrderArt() {
  const up = useTokenColor('up');
  const down = useTokenColor('down');
  return (
    <View
      accessible
      accessibilityRole="image"
      accessibilityLabel="An order marked on a chart"
      className="w-full max-w-[400px]"
      style={{ aspectRatio: 260 / 120 }}
    >
      <Svg width="100%" height="100%" viewBox="0 0 260 120">
        {[30, 60, 90].map((y) => (
          <Line key={y} x1="0" y1={y} x2="260" y2={y} stroke="rgba(255,255,255,0.06)" strokeWidth="1" />
        ))}
        {[
          [18, 74],
          [40, 66],
          [62, 80],
          [84, 58],
          [106, 64],
          [128, 50],
        ].map(([x, y], i) => (
          <Rect
            key={i}
            x={x}
            y={Math.min(y!, 62)}
            width="9"
            height={Math.max(10, Math.abs(y! - 62))}
            rx="2"
            fill={y! < 62 ? up : down}
            opacity="0.75"
          />
        ))}
        <Line x1="170" y1="52" x2="236" y2="52" stroke={LANDING.accent} strokeWidth="2" />
        <Polygon points="236,45 231,52 241,52" fill={LANDING.accent} />
        <Circle cx="170" cy="52" r="6" fill={LANDING.accent} />
        <G transform="translate(86, 41)">
          <Path d="M0 0 H70 L78 11 L70 22 H0 Z" fill={LANDING.accent} />
          <SvgText x="12" y="15" fontSize="11" fontWeight="700" fill={LANDING.ink}>
            ↑ ₹500
          </SvgText>
        </G>
        <Line
          x1="236"
          y1="8"
          x2="236"
          y2="112"
          stroke={LANDING.accent}
          strokeWidth="1"
          strokeDasharray="4 4"
          opacity="0.5"
        />
      </Svg>
    </View>
  );
}
