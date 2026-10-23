import { useState, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import Animated, { FadeIn, useReducedMotion } from 'react-native-reanimated';
import { Check, ChevronDown, Globe, Laptop, Smartphone } from 'lucide-react-native';
import { Icon, Text } from '../../components/ui';
import { cn } from '../../utils/cn';
import { WorkspaceMock } from './WorkspaceMock';
import { FitBox } from './FitBox';
import { PaymentCarousel } from './PaymentCarousel';
import { LANDING, LandingCard, Reveal, accentAlpha, useFloatStyle } from './kit';

/**
 * The sections that carry the product rather than describe it.
 *
 * Everything here is drawn from the app itself, so the page cannot promise
 * something the platform does not do. Where a claim would need a fact nobody
 * has - user counts, awards, app-store ratings - the section is simply not
 * here rather than filled with something plausible.
 */

/* ------------------------------------------------------------- preview -- */

const PREVIEW_POINTS = [
  'Your order drawn on the chart, from entry to expiry',
  'Countdown, live payout and sell-now on every position',
  'Ten timeframes, four chart types, three indicators',
];

/** The workspace itself, in a browser chrome, with the parts called out. */
export function ProductPreview() {
  return (
    <View className="gap-10">
      <BrowserFrame>
        <FitBox width={900} height={450}>
          <WorkspaceMock />
        </FitBox>
      </BrowserFrame>

      <View className="min-w-0">
        <Text accessibilityRole="header" className="text-3xl font-black tracking-tight text-white">
          This is the whole platform
        </Text>
        <Text className="mt-4 text-base leading-[26px] text-white/50">
          One screen. The chart, the ticket and your open positions side by side, so placing a trade never means
          leaving the price you are watching.
        </Text>
        <View className="mt-7 gap-3">
          {PREVIEW_POINTS.map((point) => (
            <View key={point} className="flex-row items-start gap-3">
              <View
                className="mt-0.5 h-5 w-5 shrink-0 items-center justify-center rounded-full"
                style={{ backgroundColor: accentAlpha(0.16) }}
              >
                <Icon as={Check} size={12} color={LANDING.accent} />
              </View>
              <Text className="flex-1 text-sm leading-[23px] text-white/70">{point}</Text>
            </View>
          ))}
        </View>
        <Text className="mt-6 text-xs text-white/35">
          An illustration: the figures in it are examples. Live payouts are on the ticket.
        </Text>
      </View>
    </View>
  );
}

/** A window, so the workspace reads as a running app rather than a diagram. */
function BrowserFrame({ children }: { children: ReactNode }) {
  return (
    <LandingCard>
      <View className="flex-row items-center gap-2 border-b border-white/10 bg-white/[0.03] px-4 py-2.5">
        <View className="flex-row gap-1.5">
          {['#ff5f57', '#febc2e', '#28c840'].map((colour) => (
            <View key={colour} className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: colour }} />
          ))}
        </View>
        <View className="mx-auto rounded-md bg-white/[0.06] px-3 py-1">
          <Text className="text-[10px] text-white/40">binary.trade/trade</Text>
        </View>
      </View>
      {children}
    </LandingCard>
  );
}

/* ------------------------------------------------------------- devices -- */

const DEVICES = [
  { icon: Laptop, title: 'Desktop', body: 'The full workspace, with the trade list beside the chart.' },
  { icon: Smartphone, title: 'Phone', body: 'The same chart and ticket, stacked for a narrow screen.' },
  { icon: Globe, title: 'Any browser', body: 'The same account on the web, with nothing to install.' },
];

export function Devices() {
  const floatStyle = useFloatStyle();

  return (
    <View className="gap-12">
      <View className="min-w-0">
        <Text accessibilityRole="header" className="text-3xl font-black tracking-tight text-white">
          Trade from whatever you have open
        </Text>
        <Text className="mt-4 text-base leading-[26px] text-white/50">
          One account. The layout rearranges for the screen it is on — the chart, the order ticket and your positions
          stay where you expect them.
        </Text>

        <View className="mt-8 gap-3">
          {DEVICES.map((device) => (
            <View
              key={device.title}
              className="flex-row items-start gap-4 rounded-2xl border border-white/10 bg-white/[0.04] p-4"
            >
              <View className="mt-0.5">
                <Icon as={device.icon} size={20} color={LANDING.accent} />
              </View>
              <View className="flex-1">
                <Text className="text-sm font-bold text-white">{device.title}</Text>
                <Text className="mt-0.5 text-sm text-white/45">{device.body}</Text>
              </View>
            </View>
          ))}
        </View>
      </View>

      {/* A phone in front of a laptop, both running the same workspace. */}
      <View className="relative w-full max-w-[520px] self-center">
        <View className="rounded-2xl border border-white/10 bg-[#17181b] p-2.5">
          <FitBox width={900} height={506} className="rounded-xl">
            <WorkspaceMock />
          </FitBox>
        </View>
        <View className="h-2.5 w-2/3 self-center rounded-b-xl border border-t-0 border-white/10 bg-[#17181b]" />

        <Animated.View style={[{ position: 'absolute', bottom: -32, right: -8, width: 132 }, floatStyle]}>
          <View className="rounded-[26px] border border-white/15 bg-[#17181b] p-1.5">
            <View className="relative overflow-hidden rounded-[20px]" style={{ aspectRatio: 9 / 17 }}>
              <FitBox width={220} height={415} className="h-full w-full">
                <WorkspaceMock compact />
              </FitBox>
              <View className="absolute left-1/2 top-1 -ml-4 h-1 w-8 rounded-full bg-white/25" />
            </View>
          </View>
        </Animated.View>
      </View>
    </View>
  );
}

/* ----------------------------------------------------------------- faq -- */

const FAQ: Array<{ q: string; a: string }> = [
  {
    q: 'What exactly am I buying?',
    a: 'Nothing. A fixed-return trade is a call on direction: you stake an amount, pick UP or DOWN and an expiry. If the price at expiry is on your side you get your stake back plus the payout percentage; if it is not, the stake is lost. You never hold the underlying asset, so there is no position to manage afterwards and nothing to sell.',
  },
  {
    q: 'How much do I need to start?',
    a: 'Nothing to try it. Registering funds a demo wallet, which uses the same live prices, the same chart and the same order flow as a funded account. On a funded account the smallest trade is ₹100 and the largest single position is ₹50,000.',
  },
  {
    q: 'What happens if the price moves as I tap?',
    a: 'The order is refused rather than filled at a price you did not choose. You are shown what the market has moved to and asked whether you still want it - so a fast market costs you a confirmation, not a worse entry.',
  },
  {
    q: 'Can I get out before the expiry?',
    a: 'Yes. Every open position carries a live sell-now quote. Taking it closes the trade immediately at that value, which may be above or below your stake depending on where the price has gone and how long is left.',
  },
  {
    q: 'Which markets can I trade?',
    a: 'Bitcoin and Ethereum against the US dollar. Payouts are not a fixed table: they are priced live and move with the market and with the expiry you choose. The ticket shows the payout for each direction before you trade, and once your trade is accepted its payout is locked.',
  },
  {
    q: 'Do I need to install anything else?',
    a: 'No. This app is everything you need on a phone, and the same account also runs in any modern browser on a desktop, with nothing to download there.',
  },
  {
    q: 'How is my entry price decided?',
    a: 'By the market at the instant the server accepts the order, not at the instant you tapped. Once accepted it is frozen: it is drawn on your chart and used to settle the trade, and nothing that happens afterwards changes it.',
  },
];

export function Faq() {
  const [open, setOpen] = useState<number | null>(0);
  const reduced = useReducedMotion();

  return (
    <View className="gap-2.5">
      {FAQ.map((item, index) => {
        const isOpen = open === index;
        return (
          <View
            key={item.q}
            className={cn(
              'overflow-hidden rounded-2xl border',
              isOpen ? 'border-white/15 bg-white/[0.06]' : 'border-white/10 bg-white/[0.03]',
            )}
          >
            <Pressable
              onPress={() => setOpen(isOpen ? null : index)}
              accessibilityRole="button"
              accessibilityState={{ expanded: isOpen }}
              className="flex-row items-center gap-4 px-5 py-4 active:bg-white/[0.04]"
            >
              <Text className="flex-1 text-base font-bold text-white">{item.q}</Text>
              <View style={{ transform: [{ rotate: isOpen ? '180deg' : '0deg' }] }}>
                <Icon as={ChevronDown} size={20} color="rgba(255,255,255,0.4)" />
              </View>
            </Pressable>
            {isOpen ? (
              <Animated.View entering={reduced ? undefined : FadeIn.duration(180)}>
                <Text className="px-5 pb-5 text-sm leading-[23px] text-white/55">{item.a}</Text>
              </Animated.View>
            ) : null}
          </View>
        );
      })}
    </View>
  );
}

/* ------------------------------------------------------------- funding -- */

/**
 * How money gets in and out.
 *
 * The two methods and both their limits are the ones seeded into the
 * platform's own payment_methods collection, not a plausible-looking list:
 * UPI and an IMPS/NEFT bank transfer, each with the floor and ceiling the
 * server will actually accept. A landing page that advertises a method the
 * deposit screen does not offer is worse than one that lists nothing.
 */
const FUNDING: Array<{ label: string; detail: string; min: string; max: string; how: string }> = [
  {
    label: 'UPI',
    detail: 'Any UPI app',
    min: '₹100',
    max: '₹1,00,000',
    how: 'Pay to the UPI ID shown at checkout, then submit the 12-digit UTR from your payment app.',
  },
  {
    label: 'Bank transfer',
    detail: 'IMPS or NEFT',
    min: '₹500',
    max: '₹5,00,000',
    how: 'Transfer to the account shown at checkout, then submit the UTR from your bank receipt.',
  },
];

export function Funding() {
  return (
    <View>
      {/* The rails first, as marks a visitor already recognises from their own
          banking app; the limits and the steps follow for each one. */}
      <Reveal>
        <PaymentCarousel />
      </Reveal>

      <View className="mt-10 gap-4">
        {FUNDING.map((method) => (
          <Reveal key={method.label}>
            <LandingCard className="p-7">
              <View className="flex-row items-baseline justify-between gap-4">
                <Text className="text-2xl font-bold tracking-tight text-white">{method.label}</Text>
                <Text className="text-sm text-white/40">{method.detail}</Text>
              </View>

              <View className="mt-7 flex-row gap-px overflow-hidden rounded-2xl bg-white/10">
                {[
                  ['Minimum', method.min],
                  ['Maximum', method.max],
                ].map(([term, value]) => (
                  <View key={term} className="flex-1 bg-[#0e0f11] px-5 py-4">
                    <Text className="text-[11px] font-bold uppercase tracking-wider text-white/35">{term}</Text>
                    <Text className="mt-1 text-xl font-black text-[#f6d84a] tnum">{value}</Text>
                  </View>
                ))}
              </View>

              <Text className="mt-6 text-sm leading-[23px] text-white/50">{method.how}</Text>
            </LandingCard>
          </Reveal>
        ))}
      </View>
    </View>
  );
}
