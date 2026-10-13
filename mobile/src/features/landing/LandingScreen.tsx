import { useCallback, useEffect, useRef, useState, type ComponentType, type ReactNode } from 'react';
import { Pressable, ScrollView, StyleSheet, View, useWindowDimensions, type LayoutChangeEvent } from 'react-native';
import Animated, {
  cancelAnimation,
  Easing,
  Extrapolation,
  FadeIn,
  FadeInDown,
  interpolate,
  useAnimatedRef,
  useAnimatedScrollHandler,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withRepeat,
  withTiming,
  type SharedValue,
} from 'react-native-reanimated';
import { router } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { LinearGradient } from 'expo-linear-gradient';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import {
  ArrowRight,
  BarChart3,
  ChevronRight,
  Clock,
  FileClock,
  LineChart,
  Menu,
  PenLine,
  ScrollText,
  ShieldCheck,
  Timer,
  Wallet,
  X,
} from 'lucide-react-native';
import { Icon, Text, type IconGlyphProps } from '../../components/ui';
import { AuthPanel, type AuthTab } from '../auth/AuthPanel';
import { Devices, Faq, Funding, ProductPreview } from './sections';
import { AmountDuration } from './AmountDuration';
import { Backdrop, ChartArt, ClockArt, CoinArt, HeroPortrait, OrderArt, RisingBars } from './artwork';
import {
  BrandMark,
  LandingCard,
  LandingCta,
  LandingGhost,
  LandingMotionProvider,
  RISE_EASING,
  Reveal,
  Section,
  useLandingMotion,
  useScreenFocused,
} from './kit';

/**
 * The screen a signed-out visitor lands on.
 *
 * Two rules shape it. The first is that it does not follow the app's theme:
 * the artwork is built for a black page and a marketing screen that inverted
 * under a visitor would read as broken, so the colours here are fixed.
 *
 * The second is that every figure on it is read off the platform - the seeded
 * instruments, their payout tables, the stake limits, the expiries those
 * tables are keyed by. Nothing is invented. A landing page is the one place
 * where a made-up number stops being decoration and becomes a claim to a
 * customer, so where a figure would have to be marketing rather than fact,
 * the section says what the platform does instead of how many people use it.
 */

/** Straight from the seeded payout tables, in percent by expiry seconds. */
const MARKETS: Array<{ symbol: string; name: string; kind: string; payouts: Record<number, number> }> = [
  { symbol: 'BTC/USD', name: 'Bitcoin', kind: 'Crypto', payouts: { 30: 82, 60: 84, 300: 87 } },
  { symbol: 'ETH/USD', name: 'Ethereum', kind: 'Crypto', payouts: { 30: 80, 60: 83, 300: 86 } },
  { symbol: 'EUR/USD', name: 'Euro', kind: 'Forex', payouts: { 30: 78, 60: 80, 300: 84 } },
  { symbol: 'GBP/USD', name: 'Pound Sterling', kind: 'Forex', payouts: { 60: 79, 300: 83 } },
  { symbol: 'XAU/USD', name: 'Gold', kind: 'Metals', payouts: { 60: 77, 300: 81 } },
];

const BEST_PAYOUT = Math.max(...MARKETS.flatMap((m) => Object.values(m.payouts)));

const CHIPS = ['Live streaming prices', 'Demo funded on sign-up', 'Expiries from 30 seconds', 'Close early'];

const STEPS = [
  {
    title: 'Open a demo',
    body: 'Register and a demo wallet is funded for you. Same prices, same chart, same order flow - no deposit.',
  },
  {
    title: 'Pick a market',
    body: 'Choose an instrument, a stake from ₹100 and an expiry of 30 seconds, 1 minute or 5 minutes.',
  },
  {
    title: 'Call the direction',
    body: 'UP or DOWN. Your entry is frozen the moment the order lands and stays drawn until it expires.',
  },
  {
    title: 'Settle or close early',
    body: 'A win credits automatically. Or take the live sell-now quote and end the position before expiry.',
  },
];

const PLATFORM: Array<{ title: string; body: string; icon: ComponentType<IconGlyphProps> }> = [
  {
    title: 'Ten timeframes',
    body: '5 seconds through to 1 day, with candles, bars, line and area on the same chart.',
    icon: Clock,
  },
  {
    title: 'Orders on the chart',
    body: 'Every open position draws its entry, its run and its expiry, with a countdown.',
    icon: LineChart,
  },
  {
    title: 'Indicators and levels',
    body: 'Moving averages, exponential MA and Bollinger bands, plus price levels you draw yourself.',
    icon: PenLine,
  },
  {
    title: 'Streaming prices',
    body: 'Quotes arrive over a websocket and the chart updates in place - no refresh, no redraw.',
    icon: BarChart3,
  },
  { title: 'Early close', body: 'A live sell-now quote on every open position.', icon: Timer },
  {
    title: 'Full history',
    body: 'Every trade, transfer and settlement listed with its entry, exit and result. Exports to CSV.',
    icon: ScrollText,
  },
];

const SAFEGUARDS: Array<{ title: string; body: string; icon: ComponentType<IconGlyphProps> }> = [
  {
    title: 'Price-move protection',
    body: 'If the quote moves between your tap and the order landing, nothing is placed. You are shown the new price and asked to confirm.',
    icon: ShieldCheck,
  },
  {
    title: 'Transfers cannot double-run',
    body: 'Deposits and withdrawals are idempotent, so a retried or duplicated request settles once and only once.',
    icon: Wallet,
  },
  {
    title: 'Audited actions',
    body: 'Administrative changes - payouts, limits, balances - are written to an audit log with who made them and when.',
    icon: FileClock,
  },
];

/** The sections the nav and the footer can jump to. */
type AnchorId = 'preview' | 'markets' | 'how' | 'safeguards' | 'funding' | 'faq';

const NAV_LINKS: Array<{ id: AnchorId; label: string }> = [
  { id: 'preview', label: 'Platform' },
  { id: 'markets', label: 'Markets' },
  { id: 'how', label: 'How it works' },
  { id: 'safeguards', label: 'Safeguards' },
  { id: 'funding', label: 'Funding' },
  { id: 'faq', label: 'FAQ' },
];

/** The top bar's height under the status bar; a jump lands a section just below it. */
const NAV_HEIGHT = 60;

export function LandingScreen() {
  const [authTab, setAuthTab] = useState<AuthTab | null>(null);
  const [navOpen, setNavOpen] = useState(false);
  const closeAuth = useCallback(() => setAuthTab(null), []);
  const focused = useScreenFocused();
  const reduced = useReducedMotion();
  const insets = useSafeAreaInsets();

  const scrollRef = useAnimatedRef<Animated.ScrollView>();
  const scrollY = useSharedValue(0);
  const onScroll = useAnimatedScrollHandler((event) => {
    scrollY.value = event.contentOffset.y;
  });

  // Where each section starts, measured as it lays out - the phone form of
  // the web's `#markets` anchors.
  const anchors = useRef<Partial<Record<AnchorId, number>>>({});
  const anchor = (id: AnchorId) => (event: LayoutChangeEvent) => {
    anchors.current[id] = event.nativeEvent.layout.y;
  };
  const goTo = (id: AnchorId) => {
    const y = anchors.current[id];
    if (y == null) return;
    scrollRef.current?.scrollTo({ y: Math.max(0, y - insets.top - NAV_HEIGHT), animated: !reduced });
  };

  const openAuth = (tab: AuthTab) => {
    setNavOpen(false);
    setAuthTab(tab);
  };

  return (
    // The page's loops stop while something covers it: a document pushed over
    // it, or the sign-in panel.
    <LandingMotionProvider value={focused && authTab === null}>
      <View className="flex-1 bg-black">
        {focused ? <StatusBar style="light" /> : null}

        <Animated.ScrollView
          ref={scrollRef}
          onScroll={onScroll}
          scrollEventThrottle={16}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ paddingBottom: insets.bottom }}
        >
          <Hero onAuth={openAuth} onLearnMore={() => goTo('how')} />
          <Ticker />
          <Preview onLayout={anchor('preview')} />
          <ChooseTrade />
          <Highlights />
          <Markets onLayout={anchor('markets')} />
          <Steps onLayout={anchor('how')} />
          <Platform />
          <AnyDevice />
          <Money onLayout={anchor('funding')} />
          <Safeguards onLayout={anchor('safeguards')} />
          <Questions onLayout={anchor('faq')} />
          <ClosingCta onAuth={openAuth} />
          <SiteFooter onAnchor={goTo} />
        </Animated.ScrollView>

        <SiteNav
          open={navOpen}
          scrollY={scrollY}
          onToggle={() => setNavOpen((o) => !o)}
          onAuth={openAuth}
          onTop={() => {
            setNavOpen(false);
            scrollRef.current?.scrollTo({ y: 0, animated: !reduced });
          }}
          onAnchor={(id) => {
            setNavOpen(false);
            goTo(id);
          }}
        />

        <AuthPanel tab={authTab} onTab={setAuthTab} onClose={closeAuth} />
      </View>
    </LandingMotionProvider>
  );
}

/* ------------------------------------------------------------------ nav -- */

/**
 * The site nav as a phone's top bar: the mark, both ways in, and a menu of the
 * page's sections.
 *
 * It floats over the page and is clear over the hero, taking a dark backing as
 * soon as the page scrolls so the copy underneath never runs through it.
 */
function SiteNav({
  open,
  scrollY,
  onToggle,
  onAuth,
  onTop,
  onAnchor,
}: {
  open: boolean;
  scrollY: SharedValue<number>;
  onToggle: () => void;
  onAuth: (tab: AuthTab) => void;
  onTop: () => void;
  onAnchor: (id: AnchorId) => void;
}) {
  const insets = useSafeAreaInsets();
  const reduced = useReducedMotion();

  const backing = useAnimatedStyle(() => ({
    opacity: open ? 0.96 : interpolate(scrollY.value, [0, 64], [0, 0.92], Extrapolation.CLAMP),
  }));

  return (
    <View pointerEvents="box-none" style={{ position: 'absolute', top: 0, left: 0, right: 0 }}>
      <Animated.View
        pointerEvents="none"
        style={[
          { position: 'absolute', top: 0, left: 0, right: 0, height: insets.top + NAV_HEIGHT, backgroundColor: '#000' },
          backing,
        ]}
      />

      {/* Clear space in the bar passes a drag through to the page beneath. */}
      <View pointerEvents="box-none" style={{ paddingTop: insets.top }}>
        <View pointerEvents="box-none" className="flex-row items-center gap-2 px-4" style={{ height: NAV_HEIGHT }}>
          <Pressable onPress={onTop} accessibilityRole="button" accessibilityLabel="BINARY" className="shrink-0">
            <BrandMark />
          </Pressable>

          <View pointerEvents="box-none" className="ml-auto flex-row items-center gap-1.5">
            <LandingGhost
              label="Sign in"
              onPress={() => onAuth('login')}
              className="px-3.5 py-2"
              textClassName="text-[13px]"
              maxFontSizeMultiplier={1.15}
            />
            <LandingCta
              label="Try for free"
              onPress={() => onAuth('register')}
              className="px-4 py-2"
              textClassName="text-[13px]"
              maxFontSizeMultiplier={1.15}
            />
            <Pressable
              onPress={onToggle}
              accessibilityRole="button"
              accessibilityLabel={open ? 'Close navigation' : 'Open navigation'}
              accessibilityState={{ expanded: open }}
              hitSlop={4}
              className="rounded-full p-2 active:bg-white/10"
            >
              <Icon as={open ? X : Menu} size={20} color="rgba(255,255,255,0.7)" />
            </Pressable>
          </View>
        </View>
      </View>

      {open ? (
        <Animated.View
          entering={reduced ? undefined : FadeIn.duration(150)}
          className="mx-4 rounded-2xl border border-white/10 bg-[#0e0f11] p-2"
        >
          {NAV_LINKS.map((link) => (
            <Pressable
              key={link.id}
              onPress={() => onAnchor(link.id)}
              accessibilityRole="link"
              className="rounded-xl px-4 py-3 active:bg-white/5"
            >
              <Text className="text-sm font-medium text-white/80">{link.label}</Text>
            </Pressable>
          ))}
        </Animated.View>
      ) : null}
    </View>
  );
}

/* ----------------------------------------------------------------- hero -- */

function Hero({ onAuth, onLearnMore }: { onAuth: (tab: AuthTab) => void; onLearnMore: () => void }) {
  const insets = useSafeAreaInsets();
  const { height } = useWindowDimensions();
  const reduced = useReducedMotion();
  const rise = (delay: number) => (reduced ? undefined : FadeInDown.duration(620).delay(delay).easing(RISE_EASING));

  return (
    <View
      className="relative justify-center overflow-hidden px-4"
      style={{ minHeight: height * 0.92, paddingTop: insets.top + 112 }}
    >
      {/* All three are backdrop, so they are painted before the copy. */}
      <Backdrop />
      <HeroPortrait />
      <RisingBars />

      <View className="w-full items-center">
        <Animated.View entering={rise(40)}>
          <Text className="text-center text-sm font-semibold text-white/50">
            Fixed-return trading on crypto, forex and gold
          </Text>
        </Animated.View>

        {/* The shadow costs nothing on a black page and is what keeps the
            headline legible once a photograph is behind it. */}
        <Animated.View entering={rise(120)} className="mt-5">
          <Text
            accessibilityRole="header"
            className="text-center text-[42px] font-black leading-[43px] tracking-tight text-white"
            style={{ textShadowColor: 'rgba(0,0,0,0.65)', textShadowOffset: { width: 0, height: 2 }, textShadowRadius: 28 }}
          >
            Build confidence{'\n'}with every single trade
          </Text>
        </Animated.View>

        <Animated.View entering={rise(200)} className="mt-6 max-w-xl">
          <Text className="text-center text-base leading-[26px] text-white/55">
            Pick a direction and an expiry from 30 seconds. You know the payout before you commit — up to{' '}
            <Text className="text-base leading-[26px] text-[#f6d84a]">{BEST_PAYOUT}%</Text>.
          </Text>
        </Animated.View>

        <Animated.View entering={rise(280)} className="mt-9 w-full items-center gap-3">
          <LandingCta
            label="Start now for ₹0"
            icon={ArrowRight}
            onPress={() => onAuth('register')}
            className="w-full"
          />
          <Pressable
            onPress={onLearnMore}
            accessibilityRole="link"
            className="flex-row items-center gap-1 px-2 py-3 active:opacity-70"
          >
            <Text className="text-sm font-semibold text-white/70">Learn more</Text>
            <Icon as={ChevronRight} size={16} color="rgba(255,255,255,0.7)" />
          </Pressable>
        </Animated.View>

        <Animated.View entering={rise(360)} className="mt-14 flex-row flex-wrap justify-center gap-2">
          {CHIPS.map((chip) => (
            // Solid rather than a translucent tint: the bars rise behind these,
            // and a see-through chip stops being readable the moment one does.
            <View key={chip} className="rounded-full border border-white/10 bg-[#0e0f11]/90 px-4 py-2">
              <Text className="text-xs font-medium text-white/70">{chip}</Text>
            </View>
          ))}
        </Animated.View>
      </View>
    </View>
  );
}

/* --------------------------------------------------------------- ticker -- */

/**
 * The instrument strip.
 *
 * A signed-out visitor cannot be shown live quotes - the price feed is behind
 * authentication - so this carries the one thing that is both public and
 * decided in advance: the payout each instrument pays at one minute.
 *
 * The strip sits in a scroll view that does not scroll, so the row keeps its
 * full width instead of being squeezed to the screen's; it is moved by half
 * its length per loop, which is exactly one run of the instruments.
 */
function Ticker() {
  const motion = useLandingMotion();
  const [copyWidth, setCopyWidth] = useState(0);
  const x = useSharedValue(0);

  useEffect(() => {
    const half = copyWidth / 2;
    if (!motion || half <= 0) {
      cancelAnimation(x);
      return;
    }
    const from = -(-x.value % half);
    x.value = from;
    x.value = withRepeat(withTiming(from - half, { duration: 28_000, easing: Easing.linear }), -1, false);
    return () => cancelAnimation(x);
  }, [motion, copyWidth, x]);

  const style = useAnimatedStyle(() => ({ transform: [{ translateX: x.value }] }));
  const row = [...MARKETS, ...MARKETS];

  return (
    <View className="relative border-y border-white/10 py-5">
      <ScrollView
        horizontal
        scrollEnabled={false}
        showsHorizontalScrollIndicator={false}
        pointerEvents="none"
      >
        <Animated.View style={[{ flexDirection: 'row' }, style]}>
          {[0, 1].map((copy) => (
            <View
              key={copy}
              onLayout={copy === 0 ? (event) => setCopyWidth(event.nativeEvent.layout.width) : undefined}
              accessibilityElementsHidden={copy === 1}
              importantForAccessibility={copy === 1 ? 'no-hide-descendants' : 'auto'}
              className="flex-row items-center gap-10 pr-10"
            >
              {row.map((market, index) => (
                <View key={market.symbol + index} className="flex-row items-center gap-3">
                  <Text className="text-sm font-bold text-white/80">{market.symbol}</Text>
                  <Text className="text-sm font-bold text-[#f6d84a] tnum">
                    {(market.payouts[60] ?? market.payouts[300]) + '%'}
                  </Text>
                  <Text className="text-xs text-white/35">{market.kind}</Text>
                </View>
              ))}
            </View>
          ))}
        </Animated.View>
      </ScrollView>
      <LinearGradient
        pointerEvents="none"
        colors={['#000', 'rgba(0,0,0,0)', 'rgba(0,0,0,0)', '#000']}
        locations={[0, 0.12, 0.88, 1]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 0 }}
        style={StyleSheet.absoluteFill}
      />
    </View>
  );
}

/* -------------------------------------------------- product and devices -- */

/**
 * The product, before any claim about it.
 *
 * Its own bare section rather than one of the titled ones: the workspace is
 * the argument, and a heading above it would only be describing the picture
 * underneath.
 */
function Preview({ onLayout }: { onLayout: (event: LayoutChangeEvent) => void }) {
  return (
    <View onLayout={onLayout} className="px-4 py-20">
      <Reveal>
        <ProductPreview />
      </Reveal>
    </View>
  );
}

/** A trade running live: the amount and the duration are the trader's call. */
function ChooseTrade() {
  return (
    <View className="px-4 pb-4">
      <Reveal>
        <AmountDuration />
      </Reveal>
    </View>
  );
}

function AnyDevice() {
  return (
    <View className="px-4 py-20">
      <Reveal>
        <Devices />
      </Reveal>
    </View>
  );
}

function Money({ onLayout }: { onLayout: (event: LayoutChangeEvent) => void }) {
  return (
    <Section
      onLayout={onLayout}
      title={
        <>
          <Text className="text-3xl font-black tracking-tight text-[#f6d84a]">UPI, IMPS and NEFT</Text>
          {'\n'}for money in and out
        </>
      }
      lead="Every payment is settled against a reference you submit yourself, so a deposit can always be traced to a transfer you can see in your own banking app."
    >
      <Funding />
    </Section>
  );
}

function Questions({ onLayout }: { onLayout: (event: LayoutChangeEvent) => void }) {
  return (
    <Section
      onLayout={onLayout}
      title="Questions worth asking first"
      lead="Short answers about how the trades actually settle, before you put money near one."
    >
      <Reveal>
        <Faq />
      </Reveal>
    </Section>
  );
}

/* ----------------------------------------------------------- highlights -- */

function Highlights() {
  return (
    <Section title="Everything you need to place a trade" lead="">
      <View className="gap-4">
        <Card title="Start from ₹100" sub="Up to ₹50,000 on a single position">
          <CoinArt />
        </Card>
        <Card title="Expiries from 30 seconds" sub="30 seconds, 1 minute or 5 minutes">
          <ClockArt />
        </Card>
        <Card title={'Payouts up to ' + BEST_PAYOUT + '%'} sub="Fixed before you commit, for the whole trade">
          <ChartArt />
        </Card>
        <Card title="Your entry never moves" sub="Frozen the moment the order lands">
          <OrderArt />
        </Card>
      </View>
    </Section>
  );
}

function Card({ title, sub, children }: { title: string; sub: string; children: ReactNode }) {
  return (
    <Reveal>
      <LandingCard className="min-h-[320px] p-7">
        <Text className="text-center text-2xl font-bold tracking-tight text-white">{title}</Text>
        <Text className="mt-1.5 text-center text-sm text-white/45">{sub}</Text>
        <View className="relative mt-6 flex-1 items-center justify-center">{children}</View>
      </LandingCard>
    </Reveal>
  );
}

/* -------------------------------------------------------------- markets -- */

function Markets({ onLayout }: { onLayout: (event: LayoutChangeEvent) => void }) {
  return (
    <Section
      onLayout={onLayout}
      title="Five instruments, one rate card"
      lead="The return is fixed before you commit and does not move while your position is open. Longer expiries pay more."
    >
      <Reveal>
        <LandingCard className="p-2">
          {MARKETS.map((market) => (
            <View key={market.symbol} className="rounded-2xl px-5 py-4">
              <View className="flex-row items-center gap-3">
                <View className="h-9 w-9 shrink-0 items-center justify-center rounded-xl bg-white/[0.07]">
                  <Text className="text-[10px] font-black text-white/60">{market.symbol.slice(0, 3)}</Text>
                </View>
                <View className="min-w-0 flex-1">
                  <Text numberOfLines={1} className="text-sm font-bold text-white">
                    {market.symbol}
                  </Text>
                  <Text numberOfLines={1} className="text-xs text-white/40">
                    {market.name + ' · ' + market.kind}
                  </Text>
                </View>
              </View>

              <View className="-mx-2 mt-1 flex-row flex-wrap">
                {[30, 60, 300].map((expiry) => (
                  <View key={expiry} className="w-1/2 px-2 py-1">
                    <Payout expiry={expiry} percent={market.payouts[expiry]} />
                  </View>
                ))}
              </View>
            </View>
          ))}
        </LandingCard>
      </Reveal>

      <Text className="mt-4 text-xs text-white/35">
        Rates shown are the platform defaults. A dash means that expiry is not offered on that instrument.
      </Text>
    </Section>
  );
}

function Payout({ expiry, percent }: { expiry: number; percent: number | undefined }) {
  const label = expiry === 30 ? '30 sec' : expiry === 60 ? '1 min' : '5 min';
  return (
    <View className="flex-row items-baseline justify-between gap-2">
      <Text className="text-[11px] font-semibold uppercase tracking-wide text-white/35">{label}</Text>
      <Text className={percent ? 'text-sm font-bold text-[#f6d84a] tnum' : 'text-sm font-bold text-white/30 tnum'}>
        {percent ? percent + '%' : '—'}
      </Text>
    </View>
  );
}

/* ---------------------------------------------------------------- steps -- */

function Steps({ onLayout }: { onLayout: (event: LayoutChangeEvent) => void }) {
  return (
    <Section
      onLayout={onLayout}
      title="Four steps from sign-up to settlement"
      lead="The demo wallet uses the same prices and the same order flow as a funded account, so nothing changes when you switch."
    >
      <View accessibilityRole="list" className="gap-4">
        {STEPS.map((step, index) => (
          <Reveal key={step.title} delay={index * 90}>
            <LandingCard className="p-6">
              <View className="h-10 w-10 items-center justify-center rounded-2xl bg-[#f6d84a]">
                <Text className="text-sm font-black text-black">{index + 1}</Text>
              </View>
              <Text className="mt-5 text-lg font-bold text-white">{step.title}</Text>
              <Text className="mt-2 text-sm leading-[23px] text-white/50">{step.body}</Text>
            </LandingCard>
          </Reveal>
        ))}
      </View>
    </Section>
  );
}

/* ------------------------------------------------------------- platform -- */

function Platform() {
  return (
    <Section
      title="A chart built for short expiries"
      lead="Everything below is on the trading screen today — no upgrade, no separate terminal."
    >
      <View className="gap-4">
        {PLATFORM.map((feature, index) => (
          <Reveal key={feature.title} delay={(index % 3) * 90}>
            <LandingCard className="p-6">
              <Icon as={feature.icon} size={24} color="#f6d84a" />
              <Text className="mt-5 text-lg font-bold text-white">{feature.title}</Text>
              <Text className="mt-2 text-sm leading-[23px] text-white/50">{feature.body}</Text>
            </LandingCard>
          </Reveal>
        ))}
      </View>
    </Section>
  );
}

function Safeguards({ onLayout }: { onLayout: (event: LayoutChangeEvent) => void }) {
  return (
    <Section
      onLayout={onLayout}
      title="What happens when something goes wrong"
      lead="Short expiries mean a fast market can move between your tap and the server. These are the protections that exist for that."
    >
      <View className="gap-4">
        {SAFEGUARDS.map((item, index) => (
          <Reveal key={item.title} delay={index * 90}>
            <LandingCard className="p-6">
              <Icon as={item.icon} size={24} className="text-up" />
              <Text className="mt-5 text-lg font-bold text-white">{item.title}</Text>
              <Text className="mt-2 text-sm leading-[23px] text-white/50">{item.body}</Text>
            </LandingCard>
          </Reveal>
        ))}
      </View>
    </Section>
  );
}

/* ------------------------------------------------------------------ cta -- */

function ClosingCta({ onAuth }: { onAuth: (tab: AuthTab) => void }) {
  return (
    <View className="px-4 py-20">
      <Reveal>
        <View className="overflow-hidden rounded-[32px]">
          <LinearGradient
            colors={['#f6d84a', '#f5c531', '#e2a81f']}
            locations={[0, 0.45, 1]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 1 }}
            style={{ paddingHorizontal: 24, paddingVertical: 64, alignItems: 'center' }}
          >
            <Text accessibilityRole="header" className="text-center text-3xl font-black tracking-tight text-black">
              Start trading confidently
            </Text>
            <Text className="mt-4 max-w-lg text-center text-base text-black/65">
              Open the demo and place one trade. It takes a minute and costs nothing.
            </Text>
            <View className="mt-9 w-full items-center gap-3">
              <Pressable
                onPress={() => onAuth('register')}
                accessibilityRole="button"
                className="w-full flex-row items-center justify-center gap-2 rounded-full bg-black px-7 py-3.5 active:bg-black/85"
              >
                <Text className="text-base font-bold text-white">Create account</Text>
                <Icon as={ArrowRight} size={16} color="#ffffff" />
              </Pressable>
              <Pressable
                onPress={() => onAuth('login')}
                accessibilityRole="button"
                className="w-full items-center justify-center rounded-full border border-black/20 px-7 py-3.5 active:bg-black/10"
              >
                <Text className="text-base font-bold text-black/75">I already have one</Text>
              </Pressable>
            </View>
          </LinearGradient>
        </View>
      </Reveal>
    </View>
  );
}

/* --------------------------------------------------------------- footer -- */

function SiteFooter({ onAnchor }: { onAnchor: (id: AnchorId) => void }) {
  const smallPrint = 'text-xs leading-5 text-white/35';
  return (
    <View className="border-t border-white/10 px-4 py-14">
      <View className="gap-8">
        <View>
          <View className="flex-row items-center gap-2.5">
            <BrandMark />
            <Text className="text-lg font-black tracking-tight text-white">BINARY</Text>
          </View>
          <Text className="mt-4 max-w-sm text-sm leading-[23px] text-white/40">
            Fixed-return trading on crypto, forex and gold, with expiries from 30 seconds.
          </Text>
        </View>

        <View>
          <Text accessibilityRole="header" className="text-[11px] font-bold uppercase tracking-wider text-white/35">
            Platform
          </Text>
          <View className="mt-3 gap-2">
            {NAV_LINKS.map((link) => (
              <FooterLink key={link.id} label={link.label} onPress={() => onAnchor(link.id)} />
            ))}
          </View>
        </View>

        <View>
          <Text accessibilityRole="header" className="text-[11px] font-bold uppercase tracking-wider text-white/35">
            Account
          </Text>
          <View className="mt-3 gap-2">
            <FooterLink label="Log in" onPress={() => router.push('/login')} />
            <FooterLink label="Create account" onPress={() => router.push('/register')} />
          </View>
        </View>
      </View>

      {/* The risk warning is the one thing here that has to be read, so it is
          not tucked into a line of small print beside the copyright. */}
      <View className="mt-12 rounded-2xl border border-white/10 bg-white/[0.04] p-6">
        <Text accessibilityRole="header" className="text-sm font-bold text-white">
          Risk warning
        </Text>
        <Text className="mt-2 text-xs leading-5 text-white/40">
          Fixed-return trading carries a high level of risk. A losing position costs the full amount staked, and it is
          possible to lose more than you intended over a series of trades. Only trade with money you can afford to
          lose, and do not treat past results — your own or anyone else&apos;s — as an indication of what will happen
          next. Nothing in this app is investment advice.
        </Text>
      </View>

      {/*
       * The disclosure block.
       *
       * Everything a visitor is entitled to know before they read another
       * word of marketing: who they would be contracting with, who may not
       * open an account, and what the product is. The operating entity,
       * its licence and the restricted countries are the owner's to fill
       * in - they are marked here rather than invented, because a made up
       * company number on a financial app is not a placeholder, it is a
       * false statement.
       */}
      <View className="mt-8 gap-4 border-t border-white/10 pt-8">
        <Text className={smallPrint}>
          Transactions offered in this app may be entered into only by competent adults. Trading fixed-return
          contracts involves substantial risk: you can lose the full amount you stake on any single trade, and repeated
          losses can consume an account. Before you begin, read the{' '}
          <Text accessibilityRole="link" className={smallPrint + ' underline'} onPress={() => router.push('/terms')}>
            Client Agreement
          </Text>{' '}
          and the{' '}
          <Text accessibilityRole="link" className={smallPrint + ' underline'} onPress={() => router.push('/privacy')}>
            Privacy Policy
          </Text>
          . Nothing in this app is investment advice or a recommendation to trade.
        </Text>
        <Text className={smallPrint}>
          Prices shown are provided for settling trades on this platform and are not an offer to buy or sell any
          underlying asset. A fixed-return trade gives you no ownership of, and no rights over, the instrument it is
          priced against.
        </Text>
        <Text className="text-xs leading-5 text-white/25">
          Operating entity, company registration, registered address, licence details and the list of countries from
          which accounts cannot be opened must be added here before this platform accepts a real client.
        </Text>
        <Text className={'pt-2 ' + smallPrint}>© {new Date().getFullYear()} Binary. All rights reserved.</Text>
      </View>
    </View>
  );
}

function FooterLink({ label, onPress }: { label: string; onPress: () => void }) {
  return (
    <Pressable onPress={onPress} accessibilityRole="link" hitSlop={6} className="self-start active:opacity-70">
      <Text className="text-sm text-white/55">{label}</Text>
    </Pressable>
  );
}
