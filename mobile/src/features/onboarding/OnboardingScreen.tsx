import { useEffect, useRef, useState, type ComponentType, type ReactNode } from 'react';
import { AccessibilityInfo, Pressable, ScrollView, View, type Text as RNText } from 'react-native';
import { router } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, Clock, ShieldAlert, Sparkles, Wallet } from 'lucide-react-native';
import { useAccountStore } from '../../stores/accountStore';
import { useOnboardingStore } from '../../stores/onboardingStore';
import { cn } from '../../utils/cn';
import { Button, Icon, Text, type IconGlyphProps } from '../../components/ui';
import { useDrawPolicy } from '../trade/drawRule';

/**
 * Onboarding (brief UX-17).
 *
 * Five short steps, then the demo account - no long tutorial between a new
 * customer and a practice trade. Every step can be skipped. The risk step is
 * plain about the one thing that matters: any trade can lose the whole amount
 * put into it.
 *
 * Shown once after registration; Learn can replay it at any time. Completion
 * is remembered on this device only (see onboardingStore).
 */
interface Step {
  icon: ComponentType<IconGlyphProps>;
  title: string;
  body: ReactNode;
}

/** A paragraph of step text. */
function P({ children, className }: { children: ReactNode; className?: string }) {
  return <Text className={cn('text-[15px] leading-6 text-content-300', className)}>{children}</Text>;
}

/** Emphasis inside a paragraph, in its own colour. */
function B({ children, className }: { children: ReactNode; className?: string }) {
  return <Text className={cn('text-[15px] font-bold leading-6', className)}>{children}</Text>;
}

const STEPS: Step[] = [
  {
    icon: Sparkles,
    title: 'Trade on where a price goes next',
    body: (
      <>
        <P>
          BINARY lets you make short, fixed-time trades on markets such as crypto and currencies. You do not buy
          the asset itself - you decide whether its price will be higher or lower after a time you choose.
        </P>
        <P>You always know, before you trade, how much you could get back and how much you could lose.</P>
      </>
    ),
  },
  {
    icon: ArrowUp,
    title: 'How a fixed-time trade works',
    body: (
      <>
        <View className="gap-2">
          <Numbered n={1}>Pick a market.</Numbered>
          <Numbered n={2}>Choose a duration, for example 1 minute.</Numbered>
          <Numbered n={3}>Enter how much you want to invest.</Numbered>
          <Numbered n={4}>
            Press <B className="text-up">UP</B> if you think the price will end higher, or{' '}
            <B className="text-down">DOWN</B> if you think it will end lower.
          </Numbered>
        </View>
        <P>Your entry price is the platform's price at the moment your trade is accepted.</P>
      </>
    ),
  },
  {
    icon: Clock,
    title: 'Duration and outcome',
    body: (
      <>
        <P>When the duration ends, the closing price is compared with your entry price.</P>
        <View className="gap-2">
          <Outcome tone="up" label="You were right">
            You get your investment back plus the payout. ₹100 at an 82% payout returns ₹182.
          </Outcome>
          <Outcome tone="down" label="You were wrong">
            You lose the amount you invested - ₹100 in the same example.
          </Outcome>
          <TieOutcome />
        </View>
        <P>You can also sell a running trade early at its live value, minus a small fee.</P>
      </>
    ),
  },
  {
    icon: ShieldAlert,
    title: 'The risk, honestly',
    body: (
      <>
        <P className="font-semibold text-content-100">You can lose the whole amount you invest in any trade.</P>
        <P>
          Short-term prices are hard to predict, and because a win pays back less than double, trading at random
          loses money over time. Many people lose more than they win.
        </P>
        <P>Only trade with money you can afford to lose, and never try to win back a loss by trading more.</P>
      </>
    ),
  },
  {
    icon: Wallet,
    title: 'Start with Demo',
    body: (
      <>
        <P>
          Your demo account holds <B className="text-warn">virtual money</B>. It uses the same prices and the same
          rules as a real trade, so you can learn without risking anything.
        </P>
        <P>
          When you are ready, switch to your Live account from the balance at the top of the screen. You can reset
          the demo balance whenever you like.
        </P>
      </>
    ),
  },
];

export function OnboardingScreen() {
  const insets = useSafeAreaInsets();
  const complete = useOnboardingStore((s) => s.complete);
  const setActive = useAccountStore((s) => s.setActive);
  const [index, setIndex] = useState(0);
  const headingRef = useRef<RNText>(null);
  const scrollRef = useRef<ScrollView>(null);

  const step = STEPS[index]!;
  const last = index === STEPS.length - 1;

  // Each step is a new screen for a screen reader too, and starts at its top.
  useEffect(() => {
    scrollRef.current?.scrollTo({ y: 0, animated: false });
    const heading = headingRef.current;
    if (heading) AccessibilityInfo.sendAccessibilityEvent(heading, 'focus');
  }, [index]);

  function skip() {
    complete();
    router.replace('/home');
  }

  function startDemo() {
    complete();
    setActive('DEMO');
    router.replace('/trade');
  }

  return (
    <View
      className="flex-1 bg-surface-950 px-4"
      style={{ paddingTop: insets.top + 16, paddingBottom: Math.max(16, insets.bottom + 8) }}
    >
      <View className="w-full max-w-md flex-1 self-center">
        <View className="flex-row items-center justify-between">
          <Text className="text-xs font-semibold text-content-400">
            Step {index + 1} of {STEPS.length}
          </Text>
          {!last ? (
            <Pressable
              onPress={skip}
              accessibilityRole="button"
              hitSlop={8}
              className="rounded-lg px-2 py-1 active:bg-surface-850"
            >
              <Text className="text-sm font-semibold text-content-300">Skip</Text>
            </Pressable>
          ) : null}
        </View>

        <View className="mt-3 flex-row gap-1.5" accessibilityElementsHidden importantForAccessibility="no-hide-descendants">
          {STEPS.map((_, i) => (
            <View key={i} className={cn('h-1 flex-1 rounded-full', i <= index ? 'bg-accent' : 'bg-surface-700')} />
          ))}
        </View>

        <ScrollView
          ref={scrollRef}
          className="flex-1"
          contentContainerStyle={{ flexGrow: 1, justifyContent: 'center', paddingVertical: 32 }}
          showsVerticalScrollIndicator={false}
        >
          <View
            className={cn(
              'h-12 w-12 items-center justify-center rounded-2xl',
              index === 3 ? 'bg-warn/15' : 'bg-accent/15',
            )}
          >
            <Icon as={step.icon} size={24} className={index === 3 ? 'text-warn' : 'text-accent'} />
          </View>
          <Text ref={headingRef} accessibilityRole="header" className="mt-5 text-2xl font-bold text-content-100">
            {step.title}
          </Text>
          <View className="mt-4 gap-3">{step.body}</View>
        </ScrollView>

        <View className="flex-row gap-2">
          {index > 0 ? (
            <Button variant="ghost" size="lg" icon={ArrowLeft} className="px-4" onPress={() => setIndex(index - 1)}>
              Back
            </Button>
          ) : null}
          {last ? (
            <Button size="lg" className="flex-1" onPress={startDemo}>
              Start Demo
            </Button>
          ) : (
            <Button size="lg" iconRight={ArrowRight} className="flex-1" onPress={() => setIndex(index + 1)}>
              Next
            </Button>
          )}
        </View>
        {last ? (
          <Pressable onPress={skip} accessibilityRole="button" className="mt-2 items-center py-2 active:opacity-70">
            <Text className="text-center text-sm font-semibold text-content-400">Not now - go to Home</Text>
          </Pressable>
        ) : null}
      </View>
    </View>
  );
}

function Numbered({ n, children }: { n: number; children: ReactNode }) {
  return (
    <View className="flex-row gap-3">
      <View className="h-6 w-6 shrink-0 items-center justify-center rounded-full bg-surface-800">
        <Text className="text-xs font-bold text-content-200">{n}</Text>
      </View>
      <View className="min-w-0 flex-1 pt-0.5">
        <P>{children}</P>
      </View>
    </View>
  );
}

/** The tie, as the platform's draw policy settles it. */
function TieOutcome() {
  const drawPolicy = useDrawPolicy();
  if (drawPolicy === 'LOSS') {
    return (
      <Outcome tone="down" label="Exactly the same price">
        It counts as a loss: you lose the amount you invested.
      </Outcome>
    );
  }
  if (drawPolicy === 'WIN') {
    return (
      <Outcome tone="up" label="Exactly the same price">
        It counts as a win: you get your investment back plus the payout.
      </Outcome>
    );
  }
  return (
    <Outcome tone="neutral" label="Exactly the same price">
      Your investment is refunded.
    </Outcome>
  );
}

function Outcome({ tone, label, children }: { tone: 'up' | 'down' | 'neutral'; label: string; children: ReactNode }) {
  const labelTone = tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : 'text-content-200';
  return (
    <View
      className={cn(
        'rounded-lg border px-3 py-2',
        tone === 'up' ? 'border-up/30 bg-up/10' : tone === 'down' ? 'border-down/30 bg-down/10' : 'border-surface-700 bg-surface-850',
      )}
    >
      <View className="flex-row items-center gap-1.5">
        {tone === 'up' ? (
          <Icon as={ArrowUp} size={14} className={labelTone} />
        ) : tone === 'down' ? (
          <Icon as={ArrowDown} size={14} className={labelTone} />
        ) : null}
        <Text className={cn('text-xs font-bold', labelTone)}>{label}</Text>
      </View>
      <Text className="mt-0.5 text-sm leading-5 text-content-300">{children}</Text>
    </View>
  );
}
