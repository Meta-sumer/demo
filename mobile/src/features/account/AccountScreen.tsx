import { useCallback, useEffect, useRef, useState, type ComponentType } from 'react';
import {
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  View,
  type LayoutChangeEvent,
  type NativeScrollEvent,
  type NativeSyntheticEvent,
} from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { cn } from '../../utils/cn';
import { Text } from '../../components/ui';
import { UnreadDot, useSupportUnread } from '../../components/BottomNav';
import {
  Funds,
  LegalRisk,
  Notifications,
  PersonalData,
  Preferences,
  Security,
  Support,
  Transactions,
  Verification,
} from './AccountSections';

/**
 * Account (brief UX-19).
 *
 * One screen, in the brief's order: Profile, Verification, Funds,
 * Transactions, Security, Preferences, Notifications, Support, Legal & risk. A
 * strip of chips at the top jumps to each, so a phone does not have to scroll
 * past the profile form to reach the support chat. The strip stays in view
 * while the sections scroll under it and marks the section being read, the way
 * the web's column of links does on a wide screen. Trading controls (stake,
 * expiry, chart) are not here - they belong to the trade screen and its
 * settings.
 *
 * Everything on this screen either saves to the server or says plainly that it
 * cannot. The platform has no KYC, no email delivery and no second factor - so
 * rather than showing controls for them, this screen states their absence. A
 * toggle labelled "two-step verification" that changes nothing would leave a
 * customer believing their account is protected when it is not, which is worse
 * than not offering it at all.
 */
const SECTIONS: ReadonlyArray<{ id: string; label: string; body: ComponentType }> = [
  { id: 'profile', label: 'Profile', body: PersonalData },
  { id: 'verification', label: 'Verification', body: Verification },
  { id: 'funds', label: 'Funds', body: Funds },
  { id: 'transactions', label: 'Transactions', body: Transactions },
  { id: 'security', label: 'Security', body: Security },
  { id: 'preferences', label: 'Preferences', body: Preferences },
  { id: 'notifications', label: 'Notifications', body: Notifications },
  { id: 'support', label: 'Support', body: Support },
  { id: 'legal', label: 'Legal & risk', body: LegalRisk },
];

const SECTION_IDS: readonly string[] = SECTIONS.map((section) => section.id);
const FIRST_SECTION = SECTION_IDS[0]!;
const LAST_SECTION = SECTION_IDS[SECTION_IDS.length - 1]!;

/** Room between the section strip and the title of the section it jumps to. */
const JUMP_MARGIN = 12;
/** A section chosen from the strip holds the marker while the screen scrolls to it. */
const HOLD_MS = 1000;
/**
 * A jump keeps re-aiming for this long, while the sections above the target
 * finish loading and change height under it.
 */
const SETTLE_MS = 1500;

interface Box {
  y: number;
  height: number;
}

export function AccountScreen() {
  const { section } = useLocalSearchParams<{ section?: string }>();
  const supportUnread = useSupportUnread();
  const [activeSection, setActiveSection] = useState(FIRST_SECTION);

  const scrollRef = useRef<ScrollView>(null);
  const chipsRef = useRef<ScrollView>(null);
  const boxes = useRef<Record<string, Box>>({});
  const chipX = useRef<Record<string, number>>({});
  const stripHeight = useRef(0);
  const heldUntil = useRef(0);
  const jump = useRef<{ id: string; until: number } | null>(null);

  /** Scrolls so the section starts just under the strip. False until it has been laid out. */
  const aim = useCallback((id: string, animated: boolean): boolean => {
    const box = boxes.current[id];
    if (!box) return false;
    scrollRef.current?.scrollTo({ y: Math.max(0, box.y - stripHeight.current - JUMP_MARGIN), animated });
    return true;
  }, []);

  const choose = useCallback(
    (id: string, animated: boolean, settleMs: number) => {
      heldUntil.current = Date.now() + Math.max(HOLD_MS, settleMs);
      jump.current = { id, until: Date.now() + settleMs };
      setActiveSection(id);
      aim(id, animated);
    },
    [aim],
  );

  // The bell in the top bar opens /account?section=notifications; honour any
  // section named that way, including when the tab is already open. Once
  // honoured the parameter is cleared, so the same link works again after the
  // customer has scrolled away.
  useEffect(() => {
    if (!section || !SECTION_IDS.includes(section)) return;
    choose(section, false, SETTLE_MS);
    router.setParams({ section: undefined });
  }, [section, choose]);

  // Keep the marked chip in view as the marker moves along the strip.
  useEffect(() => {
    const x = chipX.current[activeSection];
    if (x != null) chipsRef.current?.scrollTo({ x: Math.max(0, x - 12), animated: true });
  }, [activeSection]);

  function onSectionLayout(id: string, event: LayoutChangeEvent) {
    const { y, height } = event.nativeEvent.layout;
    boxes.current[id] = { y, height };
    // The first layout of a jump's target, or a section above it growing once
    // its data arrives: aim again so the jump lands where it was meant to.
    const pending = jump.current;
    if (pending && Date.now() < pending.until) aim(pending.id, false);
  }

  /**
   * The section being read: the first one with any part in the top 40% of the
   * screen under the strip, or the last one once the screen is scrolled to its
   * end (the short sections at the bottom can never reach the top).
   */
  function onScroll(event: NativeSyntheticEvent<NativeScrollEvent>) {
    if (Date.now() < heldUntil.current) return;
    const { contentOffset, layoutMeasurement, contentSize } = event.nativeEvent;
    const y = contentOffset.y;
    if (y > 0 && y + layoutMeasurement.height >= contentSize.height - 2) {
      setActiveSection(LAST_SECTION);
      return;
    }
    const top = y + stripHeight.current;
    const bottom = Math.max(top + 1, y + layoutMeasurement.height * 0.4);
    const first = SECTION_IDS.find((id) => {
      const box = boxes.current[id];
      return !!box && box.y < bottom && box.y + box.height > top;
    });
    if (first) setActiveSection(first);
  }

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      className="flex-1 bg-surface-950"
    >
      <ScrollView
        ref={scrollRef}
        className="flex-1"
        contentContainerClassName="pt-4"
        contentContainerStyle={{ paddingBottom: 24 }}
        keyboardShouldPersistTaps="handled"
        stickyHeaderIndices={[1]}
        onScroll={onScroll}
        scrollEventThrottle={32}
        onScrollBeginDrag={() => {
          // The customer has taken over: stop re-aiming and let the marker follow.
          jump.current = null;
          heldUntil.current = 0;
        }}
      >
        <View className="px-3">
          <Text accessibilityRole="header" className="text-lg font-bold text-content-100">
            Account
          </Text>
        </View>

        <View
          accessibilityLabel="Account sections"
          className="bg-surface-950 pb-2 pt-4"
          onLayout={(event) => {
            stripHeight.current = event.nativeEvent.layout.height;
          }}
        >
          <ScrollView
            ref={chipsRef}
            horizontal
            showsHorizontalScrollIndicator={false}
            keyboardShouldPersistTaps="handled"
            contentContainerClassName="gap-1.5 px-3 pt-1.5"
          >
            {SECTIONS.map((entry) => {
              const current = activeSection === entry.id;
              return (
                <Pressable
                  key={entry.id}
                  onLayout={(event) => {
                    chipX.current[entry.id] = event.nativeEvent.layout.x;
                  }}
                  onPress={() => choose(entry.id, true, 0)}
                  accessibilityRole="button"
                  accessibilityState={{ selected: current }}
                  className={cn(
                    'relative rounded-full border px-3 py-1',
                    current ? 'border-accent/60 bg-accent/10' : 'border-surface-700 bg-surface-850 active:bg-surface-800',
                  )}
                >
                  <Text className={cn('text-xs font-semibold', current ? 'text-content-100' : 'text-content-300')}>
                    {entry.label}
                  </Text>
                  {entry.id === 'support' ? <UnreadDot count={supportUnread} className="-right-1.5 -top-1.5" /> : null}
                </Pressable>
              );
            })}
          </ScrollView>
        </View>

        {SECTIONS.map((entry, index) => {
          const Body = entry.body;
          return (
            <View
              key={entry.id}
              className={cn('px-3', index === 0 ? 'mt-4' : 'mt-6')}
              onLayout={(event) => onSectionLayout(entry.id, event)}
            >
              <Body />
            </View>
          );
        })}
      </ScrollView>
    </KeyboardAvoidingView>
  );
}
