import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { KeyboardAvoidingView, Platform, Pressable, ScrollView, View } from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { ChevronDown, PlayCircle, Search, X } from 'lucide-react-native';
import { ScreenHeader } from '../../components/Screen';
import { Field, Icon, Panel, Text } from '../../components/ui';
import { matchesQuery, nodeText } from '../../utils/textSearch';
import { cn } from '../../utils/cn';
import { useDrawPolicy } from '../trade/drawRule';
import type { DrawPolicy } from '../../types/api';

/**
 * Learn (brief UX-18).
 *
 * Short guides and answers, written to match how the platform actually
 * behaves - the server sets every price, payouts differ by market and
 * duration, a tie settles by the configured draw policy (refunded by default),
 * selling early is priced live with a fee, and
 * deposits are checked by hand. If any of those change, this page has to
 * change with them; a guide that describes a different product is worse than
 * none.
 *
 * It supports trading and stays out of the way of it: nothing here is linked
 * from inside the trade ticket except by the customer's choice - the ticket's
 * "How a trade works" panel links here for the full guide.
 *
 * A search box at the top narrows the guides and the answers to the ones that
 * mention every word typed (reference record 19, Olymptrade's "Find the help
 * you need"); a matching answer opens, so the answer is what the search finds.
 *
 * Other screens link to one section with `?section=<id>` (the web's
 * `#<id>`): any guide's id, or `faq`. The screen scrolls to the one asked for
 * once it has been laid out, and again whenever the parameter changes.
 */
interface Guide {
  id: string;
  title: string;
  body: ReactNode;
}

/** A paragraph of guide text. */
function P({ children, className }: { children: ReactNode; className?: string }) {
  return <Text className={cn('text-sm leading-[22px] text-content-300', className)}>{children}</Text>;
}

/** Emphasis inside a paragraph, in its own colour. */
function B({ children, className }: { children: ReactNode; className?: string }) {
  return <Text className={cn('text-sm font-bold leading-[22px]', className)}>{children}</Text>;
}

/** The guides, stating the tie rule the platform settles by. */
function buildGuides(drawPolicy: DrawPolicy): Guide[] {
  return [
    {
      id: 'how-it-works',
      title: 'What is fixed-time trading?',
      body: (
        <>
          <P>
            You choose a market, a duration and an amount, then predict whether the price will be higher (
            <B className="text-up">UP</B>) or lower (<B className="text-down">DOWN</B>) than your entry price when the
            duration ends.
          </P>
          <P>
            If you are right you receive your investment back plus the payout. If you are wrong you lose the amount
            you invested. You do not own the asset, and there is no leverage: a trade can never cost more than you
            put into it.
          </P>
        </>
      ),
    },
    {
      id: 'duration-expiry',
      title: 'Duration and expiry',
      body: (
        <>
          <P>
            The duration is how long the trade runs - for example 1 minute or 5 minutes. Each market offers its own
            set of durations.
          </P>
          <P>
            The expiry is the clock time the trade ends: the moment it was accepted plus the duration. It is shown
            before you confirm and on the running trade. The platform's own clock decides expiry, not your device.
          </P>
        </>
      ),
    },
    {
      id: 'payout-return',
      title: 'Payout and return',
      body: (
        <>
          <P>
            The payout is a percentage of your investment, set per market and per duration, and shown before you
            trade. It is fixed when your trade is accepted, even if the displayed payout changes afterwards.
          </P>
          <P>
            Return = investment + payout. ₹100 at an 82% payout returns ₹182 if you are right: a net result of +₹82.
          </P>
          <P>
            Your entry price is the platform's price at the moment the trade is accepted. If the price moves too far
            while your order is on its way, the trade is not placed and you are shown the new price instead.
          </P>
        </>
      ),
    },
    {
      id: 'max-loss',
      title: 'Maximum loss',
      body: (
        <>
          <P>
            The most you can lose on a trade is the amount you invest in it. If the price ends on the wrong side of
            your entry price, that amount is lost in full.
          </P>
          <P>
            If the closing price is exactly equal to your entry price,{' '}
            {drawPolicy === 'LOSS'
              ? 'the trade counts as a loss and that amount is lost too.'
              : drawPolicy === 'WIN'
                ? 'the trade counts as a win.'
                : 'the trade is a draw and your investment is refunded.'}
          </P>
        </>
      ),
    },
    {
      id: 'selling-early',
      title: 'Selling early',
      body: (
        <>
          <P>
            While a trade is running you can sell it before expiry. The value offered is calculated live from the
            current price and the time left, minus a small fee, and it can be more or less than you invested.
          </P>
          <P>Selling is not available in the final seconds before expiry. After selling, the result is final.</P>
        </>
      ),
    },
    {
      id: 'demo-live',
      title: 'Demo and Live accounts',
      body: (
        <>
          <P>
            You have two accounts. <B className="text-warn">Demo</B> holds virtual money for practice; it uses the same
            markets, prices and rules, and you can reset its balance. <B className="text-content-100">Live</B> holds
            real money.
          </P>
          <P>
            The balance at the top of the screen always shows which account you are using. Switch from its menu or
            from Home. Every trade is placed on the account that is active when you confirm it.
          </P>
        </>
      ),
    },
    {
      id: 'deposits-withdrawals',
      title: 'Deposits and withdrawals',
      body: (
        <>
          <P>
            Deposits and withdrawals are for your Live account only. To deposit, pay by UPI or bank transfer to the
            details shown, then submit the UTR / reference number (or a screenshot). Our team checks it against the
            bank record and credits your balance once it matches.
          </P>
          <P>
            A withdrawal reserves the amount straight away and is paid manually to the UPI ID or bank account you
            enter. If it is rejected, the amount is returned to your balance. Each method has a minimum and a maximum.
          </P>
        </>
      ),
    },
    {
      id: 'risk',
      title: 'Risk',
      body: (
        <>
          <P>
            Fixed-time trading is high risk. Short-term price movements are close to unpredictable, and because a win
            pays back less than double, trading without an edge loses money over time.
          </P>
          <P>
            Only trade with money you can afford to lose. Set yourself a limit, take breaks, and never trade more to
            win back a loss. Practise on Demo first.
          </P>
        </>
      ),
    },
  ];
}

const FAQ: Array<{ q: string; a: ReactNode }> = [
  {
    q: 'Why was my trade refused?',
    a: 'A trade is refused when the market is closed or paused (its price feed is delayed), the amount is outside the market’s limits, your available balance is too low, or the price moved too far while the order was on its way. The trade screen says which.',
  },
  {
    q: 'Why is the payout different on another duration?',
    a: 'Payouts are set per market and per duration. The trade screen always shows the payout for the duration you have selected.',
  },
  {
    q: 'What does “Paused” mean on a market?',
    a: 'The market is open but its price feed is delayed or offline. New trades are refused until prices are flowing again, so nobody trades on an old price.',
  },
  {
    q: 'Can I lose more than I invest?',
    a: 'No. The most any trade can cost is the amount you invested in it.',
  },
  {
    q: 'How long does a deposit take?',
    a: 'It is credited once our team has matched your UTR or screenshot against the bank record. You can see its status under Account → Funds.',
  },
  {
    q: 'Can I withdraw my demo balance?',
    a: 'No. Demo money is virtual and exists only for practice. Only your Live balance can be withdrawn.',
  },
  {
    q: 'What is “confirmation” and one-tap trading?',
    a: 'By default you review each trade before it is placed. If you turn on one-tap trading (Account → Preferences), pressing UP or DOWN places the trade immediately.',
  },
];

/** Each answer with the words it says, read once from its JSX. The guides are read when built. */
const FAQ_TEXT = FAQ.map((item) => item.q + ' ' + nodeText(item.a));
const INTRO_TEXT = 'Replay the introduction onboarding. Five short steps, ending in your demo account.';

/** The questions' anchor, the web's `#faq`. Every guide is an anchor by its own id. */
const FAQ_SECTION = 'faq';

export function LearnScreen() {
  const params = useLocalSearchParams<{ section?: string }>();
  const insets = useSafeAreaInsets();
  const [query, setQuery] = useState('');
  const searching = query.trim() !== '';

  const drawPolicy = useDrawPolicy();
  const allGuides = useMemo(
    () => buildGuides(drawPolicy).map((guide) => ({ guide, text: guide.title + ' ' + nodeText(guide.body) })),
    [drawPolicy],
  );
  const guides = useMemo(
    () => allGuides.filter((entry) => matchesQuery(entry.text, query)).map((entry) => entry.guide),
    [allGuides, query],
  );
  const answers = useMemo(() => FAQ.filter((_, index) => matchesQuery(FAQ_TEXT[index]!, query)), [query]);
  const intro = matchesQuery(INTRO_TEXT, query);
  const found = [
    plural(guides.length, 'guide'),
    plural(answers.length, 'answer'),
    searching && intro ? 'the introduction' : '',
  ].filter(Boolean);

  /*
   * Where each section sits in the scrolling content: every guide by its id,
   * and the questions as `faq` - the anchors the web page had. A guide's
   * layout is relative to the list that holds it, so the list's own offset is
   * added.
   */
  const scrollRef = useRef<ScrollView>(null);
  const listY = useRef<number | null>(null);
  const guideY = useRef<Record<string, number>>({});
  const faqY = useRef<number | null>(null);
  const pending = useRef<string | null>(typeof params.section === 'string' ? params.section : null);

  const scrollToSection = useCallback((id: string, animated: boolean): boolean => {
    let y: number | null = null;
    if (id === FAQ_SECTION) y = faqY.current;
    else if (listY.current !== null && guideY.current[id] !== undefined) y = listY.current + guideY.current[id]!;
    if (y === null) return false;
    scrollRef.current?.scrollTo({ y: Math.max(0, y - 12), animated });
    return true;
  }, []);

  // Other screens link to single sections; scroll to the one asked for once it has been laid out.
  const settlePending = useCallback(() => {
    if (pending.current && scrollToSection(pending.current, false)) pending.current = null;
  }, [scrollToSection]);

  useEffect(() => {
    if (typeof params.section !== 'string' || !params.section) return;
    pending.current = params.section;
    settlePending();
  }, [params.section, settlePending]);

  return (
    <View className="flex-1 bg-surface-950">
      <ScreenHeader title="Learn" subtitle="Short guides to how trading on this platform works." />
      <KeyboardAvoidingView behavior={Platform.OS === 'ios' ? 'padding' : undefined} className="flex-1">
        <ScrollView
          ref={scrollRef}
          className="flex-1"
          contentContainerClassName="gap-4 px-3 pt-4"
          contentContainerStyle={{ paddingBottom: insets.bottom + 24 }}
          keyboardShouldPersistTaps="handled"
        >
          <View accessibilityRole="search" className="gap-1.5">
            <Field
              value={query}
              onChangeText={setQuery}
              placeholder="Find the help you need, e.g. payout or draw"
              accessibilityLabel="Search guides and answers"
              autoComplete="off"
              autoCorrect={false}
              returnKeyType="search"
              left={<Icon as={Search} size={16} className="text-content-400" />}
              right={
                query ? (
                  <Pressable
                    onPress={() => setQuery('')}
                    hitSlop={8}
                    accessibilityRole="button"
                    accessibilityLabel="Clear search"
                    className="-mr-1.5 rounded-md p-1.5 active:bg-surface-800"
                  >
                    <Icon as={X} size={14} className="text-content-400" />
                  </Pressable>
                ) : null
              }
            />
            {searching ? (
              <Text accessibilityLiveRegion="polite" className="text-xs text-content-400">
                {found.length === 0
                  ? 'Nothing matches “' + query.trim() + '”.'
                  : 'Found ' + listed(found) + ' for “' + query.trim() + '”.'}
              </Text>
            ) : null}
          </View>

          {searching && found.length === 0 ? (
            <Panel className="items-center p-5">
              <Text className="text-center text-sm text-content-200">
                Try another word - for example payout, expiry, draw or withdrawal.
              </Text>
              <Text className="mt-2 text-center text-xs text-content-400">
                Or{' '}
                <Text
                  accessibilityRole="link"
                  onPress={() => router.push('/support')}
                  className="text-xs font-semibold text-accent"
                >
                  ask support
                </Text>
                .
              </Text>
            </Panel>
          ) : null}

          {intro ? (
            <Pressable
              onPress={() => router.push('/onboarding')}
              accessibilityRole="link"
              className="flex-row items-center gap-3 rounded-xl border border-surface-700 bg-surface-900 p-4 active:border-surface-600 active:bg-surface-850"
            >
              <Icon as={PlayCircle} size={24} className="text-accent" />
              <View className="min-w-0 flex-1">
                <Text className="text-sm font-semibold text-content-100">Replay the introduction</Text>
                <Text className="text-xs text-content-400">Five short steps, ending in your demo account.</Text>
              </View>
            </Pressable>
          ) : null}

          {guides.length > 0 ? (
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              accessibilityLabel="Guides"
              className="-mx-1 grow-0"
              contentContainerClassName="gap-1.5 px-1 pb-1"
            >
              {guides.map((guide) => (
                <Pressable
                  key={guide.id}
                  onPress={() => scrollToSection(guide.id, true)}
                  accessibilityRole="link"
                  className="rounded-full border border-surface-700 bg-surface-850 px-3 py-1 active:bg-surface-800"
                >
                  <Text className="text-xs font-semibold text-content-300">{guide.title}</Text>
                </Pressable>
              ))}
            </ScrollView>
          ) : null}

          <View
            className="gap-2"
            onLayout={(event) => {
              listY.current = event.nativeEvent.layout.y;
              settlePending();
            }}
          >
            {guides.map((guide) => (
              <Panel
                key={guide.id}
                className="p-4"
                onLayout={(event) => {
                  guideY.current[guide.id] = event.nativeEvent.layout.y;
                  settlePending();
                }}
              >
                <Text accessibilityRole="header" className="text-base font-bold text-content-100">
                  {guide.title}
                </Text>
                <View className="mt-2 gap-2">{guide.body}</View>
              </Panel>
            ))}
          </View>

          {answers.length > 0 ? (
            <View
              onLayout={(event) => {
                faqY.current = event.nativeEvent.layout.y;
                settlePending();
              }}
            >
              <Text accessibilityRole="header" className="mb-2 mt-2 text-base font-bold text-content-100">
                Frequently asked questions
              </Text>
              <Panel className="overflow-hidden">
                {answers.map((item, index) => (
                  // Keyed by the search too: a search opens the answers it finds,
                  // and clearing it puts them back the way they were.
                  <FaqItem
                    key={item.q + (searching ? ':found' : '')}
                    question={item.q}
                    answer={item.a}
                    defaultOpen={searching}
                    first={index === 0}
                  />
                ))}
              </Panel>
            </View>
          ) : null}

          <Text className="pb-2 text-center text-xs text-content-400">
            Still stuck?{' '}
            <Text
              accessibilityRole="link"
              onPress={() => router.push('/support')}
              className="text-xs font-semibold text-accent"
            >
              Ask support
            </Text>
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>
    </View>
  );
}

/** One question that opens to its answer - the web's `<details>`. */
function FaqItem({
  question,
  answer,
  defaultOpen,
  first,
}: {
  question: string;
  answer: ReactNode;
  defaultOpen: boolean;
  first: boolean;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <View className={cn(!first && 'border-t border-surface-800')}>
      <Pressable
        onPress={() => setOpen((value) => !value)}
        accessibilityRole="button"
        accessibilityState={{ expanded: open }}
        className="flex-row items-center gap-3 px-4 py-3 active:bg-surface-850"
      >
        <Text className="min-w-0 flex-1 text-sm font-semibold text-content-100">{question}</Text>
        <View style={{ transform: [{ rotate: open ? '180deg' : '0deg' }] }}>
          <Icon as={ChevronDown} size={16} className="text-content-400" />
        </View>
      </Pressable>
      {open ? <Text className="px-4 pb-4 text-sm leading-[22px] text-content-300">{answer}</Text> : null}
    </View>
  );
}

/** "2 guides", "1 answer", or nothing for none. */
function plural(count: number, noun: string): string {
  return count === 0 ? '' : count + ' ' + noun + (count === 1 ? '' : 's');
}

/** "a", "a and b", "a, b and c". */
function listed(parts: string[]): string {
  return parts.length <= 1 ? (parts[0] ?? '') : parts.slice(0, -1).join(', ') + ' and ' + parts[parts.length - 1];
}
