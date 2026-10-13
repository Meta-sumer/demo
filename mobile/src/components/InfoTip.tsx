import { useRef, useState, type ReactNode } from 'react';
import { AccessibilityInfo, Modal, Pressable, View, useWindowDimensions } from 'react-native';
import Animated, { FadeIn } from 'react-native-reanimated';
import { CircleHelp } from 'lucide-react-native';
import { cn } from '../utils/cn';
import { Icon, Text } from './ui';

/**
 * A "?" beside a term that explains it (brief UX-09 and UX-18, reference
 * records 12 and 19 - Olymptrade, Pocket Option and Quotex put one beside
 * their payout terms).
 *
 * A toggletip: it opens on a tap and closes on a second tap anywhere. The
 * bubble is drawn in an overlay above everything, because the terms it
 * explains sit inside scrolling panels and sheets, and a bubble drawn in place
 * would be clipped by whichever of them is narrowest. Screen readers get the
 * words announced as it opens.
 */
const MARGIN = 8;
const GAP = 6;
const MAX_WIDTH = 272;

interface Props {
  /** The term being explained, for the button's name: "About Payout". */
  label: string;
  children: ReactNode;
  className?: string;
}

export function InfoTip({ label, children, className }: Props) {
  const [anchor, setAnchor] = useState<{ x: number; y: number; width: number; height: number } | null>(null);
  const [bubbleHeight, setBubbleHeight] = useState(0);
  const buttonRef = useRef<View>(null);
  const { width: screenWidth, height: screenHeight } = useWindowDimensions();

  function open() {
    buttonRef.current?.measureInWindow((x, y, width, height) => {
      setAnchor({ x, y, width, height });
      if (typeof children === 'string') AccessibilityInfo.announceForAccessibility(children);
    });
  }

  const width = Math.min(MAX_WIDTH, screenWidth - MARGIN * 2);
  const left = anchor
    ? Math.max(MARGIN, Math.min(anchor.x + anchor.width / 2 - width / 2, screenWidth - width - MARGIN))
    : 0;
  const below = anchor ? anchor.y + anchor.height + GAP : 0;
  const top =
    anchor && below + bubbleHeight > screenHeight - MARGIN ? Math.max(MARGIN, anchor.y - GAP - bubbleHeight) : below;

  return (
    <View className={cn('self-center', className)}>
      <Pressable
        ref={buttonRef}
        onPress={() => (anchor ? setAnchor(null) : open())}
        hitSlop={8}
        accessibilityRole="button"
        accessibilityLabel={'About ' + label}
        accessibilityState={{ expanded: !!anchor }}
        className="-my-1 h-6 w-6 items-center justify-center rounded-full"
      >
        <Icon as={CircleHelp} size={14} className={anchor ? 'text-accent' : 'text-content-400'} />
      </Pressable>

      <Modal transparent visible={!!anchor} animationType="none" statusBarTranslucent onRequestClose={() => setAnchor(null)}>
        <Pressable style={{ flex: 1 }} onPress={() => setAnchor(null)} accessibilityLabel={'Close ' + label}>
          <Animated.View
            entering={FadeIn.duration(150)}
            onLayout={(event) => setBubbleHeight(event.nativeEvent.layout.height)}
            style={{ position: 'absolute', left, top, maxWidth: width }}
            className="rounded-lg border border-surface-700 bg-surface-800 px-3 py-2"
          >
            {typeof children === 'string' || typeof children === 'number' ? (
              <Text className="text-[12px] leading-4 text-content-200">{children}</Text>
            ) : (
              children
            )}
          </Animated.View>
        </Pressable>
      </Modal>
    </View>
  );
}
