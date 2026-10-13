import { memo, useEffect, useState, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import Animated, { useAnimatedStyle, useSharedValue, withTiming } from 'react-native-reanimated';
import { LinearGradient } from 'expo-linear-gradient';
import Svg, { Path, Text as SvgText } from 'react-native-svg';
import { RISE_EASING, useLandingMotion } from './kit';

/**
 * The payment rails, as a coverflow.
 *
 * Only the rails this platform actually settles through: UPI, and a bank
 * transfer by IMPS or NEFT. Those come from the seeded payment_methods
 * collection, and the deposit screen offers nothing else - so a carousel that
 * added card networks, wallets or crypto to look fuller would be advertising
 * a checkout that does not exist.
 *
 * Three rails is too few to fill a coverflow without showing one twice at
 * once, so the track holds the set twice but only ever shows three
 * consecutive cards - always the three distinct rails. The duplicate is only
 * there so that a card can leave on one side and re-enter on the other while
 * it is invisible, instead of sliding across behind the centre.
 *
 * The UPI and IMPS marks belong to NPCI and are shown here for the reason
 * they exist: to say the platform accepts them.
 */

interface Rail {
  key: string;
  label: string;
  logo: ReactNode;
}

const RAILS: Rail[] = [
  { key: 'upi', label: 'UPI', logo: <UpiMark /> },
  { key: 'imps', label: 'IMPS', logo: <ImpsMark /> },
  { key: 'neft', label: 'NEFT', logo: <NeftMark /> },
];

/** The set twice: see the note above on why a duplicate is never visible. */
const TRACK = [...RAILS, ...RAILS].map((rail, index) => ({ ...rail, id: rail.key + '-' + index }));

const ADVANCE_MS = 2800;
const MOVE_MS = 700;

/** The card at phone width (the web's `w-[190px]`, `aspect-[1.1/1]`). */
const CARD_W = 190;
const CARD_H = CARD_W / 1.1;
/** The mark inside it: 78% of the card, at the marks' 2:1 viewBox. */
const LOGO_W = Math.round(CARD_W * 0.78);
const LOGO_H = LOGO_W / 2;

/** Where a card sits for a given distance from the centre. */
function placement(offset: number): { x: number; rotate: number; scale: number; opacity: number; z: number } {
  const side = Math.sign(offset);
  const distance = Math.abs(offset);
  if (distance === 0) return { x: 0, rotate: 0, scale: 1, opacity: 1, z: 30 };
  if (distance === 1) return { x: side * 0.8 * CARD_W, rotate: -side * 48, scale: 0.82, opacity: 1, z: 20 };
  // Off stage: faded out, further round the curve, and behind.
  return { x: side * 1.3 * CARD_W, rotate: -side * 50, scale: 0.62, opacity: 0, z: 10 };
}

export function PaymentCarousel() {
  const motion = useLandingMotion();
  const [active, setActive] = useState(0);
  const [paused, setPaused] = useState(false);

  // Turns on its own while the page is in front, and holds still while a
  // finger is on it.
  useEffect(() => {
    if (!motion || paused) return;
    const timer = setInterval(() => setActive((i) => (i + 1) % TRACK.length), ADVANCE_MS);
    return () => clearInterval(timer);
  }, [motion, paused]);

  const n = TRACK.length;
  const offsetOf = (index: number) => {
    const raw = (((index - active) % n) + n) % n;
    return raw > n / 2 ? raw - n : raw;
  };

  const centre = TRACK[active]!;

  // For a screen reader the carousel is one control: it reads out which rail
  // is in front, and swiping up or down turns it.
  return (
    <View
      accessible
      accessibilityRole="adjustable"
      accessibilityLabel="Payment methods"
      accessibilityValue={{ text: centre.label }}
      accessibilityActions={[{ name: 'increment' }, { name: 'decrement' }]}
      onAccessibilityAction={(event) => {
        const step = event.nativeEvent.actionName === 'increment' ? 1 : -1;
        setActive((i) => (i + step + n) % n);
      }}
      className="relative h-[230px] w-full overflow-hidden"
    >
      {TRACK.map((rail, index) => (
        <Card
          key={rail.id}
          rail={rail}
          offset={offsetOf(index)}
          animate={motion}
          onPress={() => setActive(index)}
          onPressIn={() => setPaused(true)}
          onPressOut={() => setPaused(false)}
        />
      ))}
    </View>
  );
}

const Card = memo(function Card({
  rail,
  offset,
  animate,
  onPress,
  onPressIn,
  onPressOut,
}: {
  rail: (typeof TRACK)[number];
  offset: number;
  animate: boolean;
  onPress: () => void;
  onPressIn: () => void;
  onPressOut: () => void;
}) {
  const target = placement(offset);
  const x = useSharedValue(target.x);
  const rotate = useSharedValue(target.rotate);
  const scale = useSharedValue(target.scale);
  const opacity = useSharedValue(target.opacity);

  useEffect(() => {
    const next = placement(offset);
    const move = (value: number) => (animate ? withTiming(value, { duration: MOVE_MS, easing: RISE_EASING }) : value);
    x.value = move(next.x);
    rotate.value = move(next.rotate);
    scale.value = move(next.scale);
    opacity.value = move(next.opacity);
  }, [offset, animate, x, rotate, scale, opacity]);

  const style = useAnimatedStyle(() => ({
    opacity: opacity.value,
    transform: [
      { perspective: 850 },
      { translateX: x.value },
      { rotateY: rotate.value + 'deg' },
      { scale: scale.value },
    ],
  }));

  const onStage = Math.abs(offset) <= 1;

  return (
    <Animated.View
      pointerEvents={onStage ? 'auto' : 'none'}
      style={[
        {
          position: 'absolute',
          left: '50%',
          top: '50%',
          width: CARD_W,
          height: CARD_H,
          marginLeft: -CARD_W / 2,
          marginTop: -CARD_H / 2,
          zIndex: target.z,
        },
        style,
      ]}
    >
      <Pressable
        accessibilityRole="button"
        accessibilityLabel={rail.label}
        accessibilityState={{ selected: offset === 0 }}
        accessibilityElementsHidden={!onStage}
        importantForAccessibility={onStage ? 'auto' : 'no-hide-descendants'}
        onPress={onPress}
        onPressIn={onPressIn}
        onPressOut={onPressOut}
        className="h-full w-full overflow-hidden rounded-3xl border border-white/10"
      >
        <LinearGradient
          colors={['#1b1c1f', '#111214']}
          start={{ x: 0.33, y: 0 }}
          end={{ x: 0.67, y: 1 }}
          style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}
        >
          {rail.logo}
        </LinearGradient>
      </Pressable>
    </Animated.View>
  );
});

/* ----------------------------------------------------------------- marks -- */

const MARK_COLOUR = '#ffffff';

/** UPI - Unified Payments Interface. NPCI's mark. */
function UpiMark() {
  return (
    <Svg viewBox="0 0 128 64" fill="none" width={LOGO_W} height={LOGO_H}>
      <Path
        fill={MARK_COLOUR}
        d="M94.41 41.316h-5.842L96.686 12h5.842L94.41 41.316zM91.376 12.918c-.4-.559-1.025-.838-1.876-.838H57.403l-1.584 5.748H85.015l-1.703 6.135H59.958v-.013h-5.842l-4.844 17.499h5.842l3.247-11.737h26.255c.825 0 1.597-.28 2.316-.838.718-.56 1.197-1.251 1.423-2.076l3.247-11.737c.253-.879.173-1.584-.226-2.143zM45.865 39.481a2.691 2.691 0 01-2.581 1.97H13.17c-.825 0-1.437-.28-1.837-.839-.399-.559-.492-1.25-.252-2.076l7.345-26.454h5.842l-6.56 23.633h23.367l6.56-23.633h5.842l-7.611 27.4zM112.614 12.053L120 26.744l-15.53 14.691 8.144-29.382z"
      />
      <Path fill={MARK_COLOUR} d="M107.425 12.053l7.385 14.691-15.543 14.691 8.158-29.382z" />
      <SvgText
        x="64"
        y="53"
        textAnchor="middle"
        fontSize="6.4"
        fontStyle="italic"
        fontWeight="700"
        letterSpacing="0.4"
        fill={MARK_COLOUR}
      >
        UNIFIED PAYMENTS INTERFACE
      </SvgText>
    </Svg>
  );
}

/** IMPS - Immediate Payment Service. NPCI's mark. */
function ImpsMark() {
  return (
    <Svg viewBox="0 0 128 64" fill="none" width={LOGO_W} height={LOGO_H}>
      <Path fill={MARK_COLOUR} d="M113.889 18.0958L119 28.3539L108.252 38.612L113.889 18.0958Z" />
      <Path fill={MARK_COLOUR} d="M110.297 18.0958L115.408 28.3539L104.652 38.612L110.297 18.0958Z" />
      <Path
        fill={MARK_COLOUR}
        d="M106.182 18.0217C106.061 18.4696 105.949 18.8914 105.833 19.3176C105.613 20.1264 105.38 20.9309 105.169 21.7397C105.117 21.931 105.048 22.0006 104.837 22.0006C98.2343 21.9919 91.6356 21.9962 85.0327 21.9962C84.6793 21.9962 84.7224 21.9397 84.619 22.3137C84.2742 23.5661 83.925 24.8184 83.5802 26.0708C83.5587 26.1534 83.5415 26.236 83.5113 26.3491C83.632 26.3578 83.7268 26.3708 83.8216 26.3708C89.847 26.3708 95.8724 26.3708 101.902 26.3708C102.643 26.3708 103.143 26.7013 103.355 27.3319C103.467 27.6754 103.436 28.0146 103.346 28.3581C102.648 30.902 101.958 33.4458 101.251 35.9854C101.126 36.4289 101.006 36.8942 100.773 37.2769C100.247 38.1335 99.4626 38.6119 98.4325 38.6119C91.0753 38.6119 83.7225 38.6119 76.3653 38.6119C76.2963 38.6119 76.2231 38.6119 76.1196 38.6119C76.1886 38.3423 76.2489 38.1031 76.3136 37.8596C76.5851 36.8638 76.8653 35.868 77.1325 34.8722C77.1842 34.6852 77.2618 34.6243 77.4557 34.6243C80.2788 34.6286 83.1018 34.6286 85.9249 34.6286C89.6961 34.6286 93.4674 34.6286 97.2386 34.6286C97.6567 34.6286 97.5921 34.6591 97.6955 34.2808C98.0274 33.0762 98.3592 31.8717 98.6868 30.6672C98.7083 30.5845 98.7256 30.5019 98.7601 30.3671C98.592 30.3671 98.4541 30.3671 98.3161 30.3671C92.3382 30.3628 86.3645 30.3584 80.3865 30.3584C79.9685 30.3584 79.5849 30.2758 79.2789 29.9757C78.8306 29.5409 78.7617 29.006 78.9168 28.432C79.6711 25.649 80.4296 22.8703 81.2054 20.096C81.5114 19.0045 82.2441 18.3218 83.3432 18.0565C83.494 18.0217 83.6578 18.0217 83.813 18.0217C91.1917 18.0217 98.5661 18.0217 105.945 18.0217C106.014 18.0087 106.087 18.0174 106.182 18.0217Z"
      />
      <Path
        fill={MARK_COLOUR}
        d="M40.5879 38.5902C41.4801 35.3158 42.368 32.0675 43.2515 28.8192C43.2343 28.8061 43.2127 28.7931 43.1955 28.78C39.644 32.0544 36.0926 35.3245 32.511 38.625C30.3474 34.8984 28.201 31.1934 26.0503 27.4929C26.0287 27.4972 26.0115 27.5016 25.99 27.5016C25.8865 27.8712 25.7831 28.2452 25.6796 28.6148C24.7875 31.8761 23.891 35.1332 23.0031 38.3945C22.9643 38.5337 22.9126 38.6076 22.7445 38.6076C21.5377 38.5989 20.3309 38.6033 19.1241 38.6033C19.0681 38.6033 19.0121 38.5946 18.9172 38.5859C20.8007 31.724 22.6756 24.8838 24.5547 18.0436C24.572 18.0392 24.5935 18.0305 24.6107 18.0262C27.4941 22.8487 30.3775 27.6668 33.2825 32.5154C38.9415 27.6929 44.5747 22.8922 50.2553 18.0523C50.0398 18.8394 49.8415 19.5699 49.6432 20.2961C48.4666 24.5881 47.29 28.8757 46.1134 33.1677C45.6349 34.9071 45.1565 36.6508 44.6824 38.3902C44.648 38.5163 44.6135 38.6076 44.4454 38.6076C43.2041 38.5989 41.9585 38.6033 40.7172 38.6033C40.6827 38.6033 40.6569 38.5989 40.5879 38.5902Z"
      />
      <Path
        fill={MARK_COLOUR}
        d="M48.4495 38.6032C48.8589 37.1161 49.2597 35.6811 49.652 34.2504C50.3502 31.7109 51.0527 29.167 51.7423 26.6231C51.7983 26.4144 51.8975 26.3622 52.1 26.3622C55.3325 26.3666 58.565 26.3666 61.7975 26.3666C65.1292 26.3666 68.4651 26.3622 71.7967 26.3579C72.1674 26.3579 72.1674 26.3579 72.2665 25.9969C72.6113 24.7446 72.9561 23.4879 73.2966 22.2355C73.3139 22.1659 73.3268 22.0963 73.3483 21.9963C66.5687 21.9963 59.8149 21.9963 53.031 21.9963C53.0482 21.892 53.0569 21.8137 53.0784 21.7441C53.3973 20.5787 53.7163 19.4177 54.0309 18.2523C54.0783 18.0696 54.1602 18.0001 54.3542 18.0001C61.7243 18.0044 69.0944 18.0044 76.4645 18.0001C77.0679 18.0001 77.5721 18.1827 77.8695 18.7437C78.0937 19.1655 78.0549 19.6134 77.9342 20.0569C77.2834 22.4486 76.6239 24.8359 75.9688 27.2232C75.874 27.5668 75.7663 27.9016 75.6887 28.2451C75.4301 29.3801 74.2103 30.3498 72.9734 30.3454C67.004 30.3237 61.0347 30.3367 55.0653 30.3324C54.8326 30.3324 54.7334 30.3933 54.6688 30.6281C53.9792 33.1807 53.2767 35.7289 52.5741 38.2771C52.4707 38.6467 52.531 38.5945 52.1647 38.5989C51.0225 38.5989 49.8804 38.5989 48.7382 38.5989C48.6477 38.6032 48.5658 38.6032 48.4495 38.6032Z"
      />
      <Path
        fill={MARK_COLOUR}
        d="M10.8145 38.59C10.905 38.2552 10.9825 37.9508 11.0644 37.6464C12.7152 31.6281 14.3659 25.6141 16.0209 19.5958C16.1502 19.1305 16.2752 18.6609 16.4045 18.1956C16.4347 18.0912 16.4649 18.0216 16.6028 18.0216C17.8527 18.026 19.1026 18.026 20.3525 18.026C20.3783 18.026 20.4085 18.0347 20.4645 18.0434C20.4473 18.1303 20.4387 18.213 20.4171 18.2912C19.6112 21.2221 18.8052 24.153 17.9992 27.0839C17.1243 30.267 16.2494 33.4501 15.3787 36.6332C15.215 37.2246 15.0555 37.816 14.896 38.4117C14.8659 38.5292 14.8271 38.6118 14.6719 38.6118C13.4436 38.6031 12.2152 38.6074 10.9869 38.6074C10.9438 38.6031 10.9007 38.5944 10.8145 38.59Z"
      />
      <SvgText
        x="64"
        y="47.5"
        textAnchor="middle"
        fontSize="5.6"
        fontStyle="italic"
        fontWeight="700"
        letterSpacing="0.5"
        fill={MARK_COLOUR}
      >
        IMMEDIATE PAYMENT SERVICE
      </SvgText>
    </Svg>
  );
}

/**
 * NEFT - National Electronic Funds Transfer.
 *
 * The RBI's rail has no consumer mark to show, so this is a plain wordmark
 * set to sit at the same weight as the two logos beside it.
 */
function NeftMark() {
  return (
    <Svg viewBox="0 0 128 64" fill="none" width={LOGO_W} height={LOGO_H}>
      <SvgText
        x="64"
        y="37"
        textAnchor="middle"
        fontSize="28"
        fontWeight="900"
        fontStyle="italic"
        letterSpacing="1"
        fill={MARK_COLOUR}
      >
        NEFT
      </SvgText>
      <SvgText
        x="64"
        y="49"
        textAnchor="middle"
        fontSize="4.7"
        fontStyle="italic"
        fontWeight="700"
        letterSpacing="0.25"
        fill={MARK_COLOUR}
      >
        NATIONAL ELECTRONIC FUNDS TRANSFER
      </SvgText>
    </Svg>
  );
}
