import { useCallback, useEffect, useRef, useState, type CSSProperties, type ReactNode } from 'react';
import { NavLink } from 'react-router-dom';
import clsx from 'clsx';
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
} from 'lucide-react';
import { AuthPanel, type AuthTab } from '../features/auth/AuthPanel';
import { Devices, Faq, Funding, ProductPreview } from '../features/landing/sections';
import { AmountDuration } from '../features/landing/AmountDuration';
import { expiryLabel, payoutText, tickerPayout, type LandingBoard } from '../features/landing/markets';
import { useLandingBoard } from '../features/landing/useLandingBoard';

/**
 * The page a signed-out visitor lands on.
 *
 * Two rules shape it. The first is that it does not follow the app's theme:
 * the artwork is built for a black page and a marketing site that inverted
 * under a visitor would read as broken, so the colours here are fixed.
 *
 * The second is that every figure on it is read off the platform - the
 * instruments the pricing engine offers and its live payouts (`GET
 * /public/markets`, polled), the stake limits, the expiries. Nothing is
 * invented. A landing page is the one place where a made-up number stops
 * being decoration and becomes a claim to a customer, so where a figure would
 * have to be marketing rather than fact, the section says what the platform
 * does instead of how many people use it - and while the live payouts cannot
 * be read, the page shows none rather than a stand-in.
 */

type Board = LandingBoard & { loading: boolean };

const CHIPS = ['Live streaming prices', 'Demo funded on sign-up', 'Expiries from 30 seconds', 'Close early'];

const STEPS = [
  {
    title: 'Open a demo',
    body: 'Register and a demo wallet is funded for you. Same prices, same chart, same order flow - no deposit.',
  },
  {
    title: 'Pick a market',
    body: 'Choose Bitcoin or Ethereum, a stake from ₹100 and an expiry from 30 seconds. The payout is shown before you commit.',
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

const PLATFORM: Array<{ title: string; body: string; icon: typeof LineChart }> = [
  {
    title: 'Ten timeframes',
    body: '5 seconds through to 1 day, with candles, bars, line and area on the same chart.',
    icon: Clock,
  },
  {
    title: 'Orders on the chart',
    body: 'Every open position draws its entry, its run and its expiry, with a countdown you can hover.',
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

const SAFEGUARDS: Array<{ title: string; body: string; icon: typeof ShieldCheck }> = [
  {
    title: 'Price-move protection',
    body: 'If the quote moves between your click and the order landing, nothing is placed. You are shown the new price and asked to confirm.',
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

export function LandingPage() {
  const [authTab, setAuthTab] = useState<AuthTab | null>(null);
  const [navOpen, setNavOpen] = useState(false);
  const closeAuth = useCallback(() => setAuthTab(null), []);
  const board = useLandingBoard();

  return (
    <div className="landing min-h-screen bg-black text-white antialiased">
      <SiteNav
        open={navOpen}
        onToggle={() => setNavOpen((o) => !o)}
        onAuth={(tab) => {
          setNavOpen(false);
          setAuthTab(tab);
        }}
      />

      <main>
        <Hero onAuth={setAuthTab} board={board} />
        <Ticker board={board} />
        <Preview />
        <ChooseTrade />
        <Highlights board={board} />
        <Markets board={board} />
        <Steps />
        <Platform />
        <AnyDevice />
        <Money />
        <Safeguards />
        <Questions />
        <ClosingCta onAuth={setAuthTab} />
      </main>

      <SiteFooter />
      <AuthPanel tab={authTab} onTab={setAuthTab} onClose={closeAuth} />
    </div>
  );
}

/* ------------------------------------------------------------------ nav -- */

const NAV_LINKS = [
  { href: '#preview', label: 'Platform' },
  { href: '#markets', label: 'Markets' },
  { href: '#how', label: 'How it works' },
  { href: '#safeguards', label: 'Safeguards' },
  { href: '#funding', label: 'Funding' },
  { href: '#faq', label: 'FAQ' },
];

function SiteNav({
  open,
  onToggle,
  onAuth,
}: {
  open: boolean;
  onToggle: () => void;
  onAuth: (tab: AuthTab) => void;
}) {
  return (
    <header className="fixed inset-x-0 top-0 z-50">
      <div className="mx-auto flex max-w-7xl items-center gap-3 px-4 py-4 sm:px-6">
        <NavLink to="/" className="flex shrink-0 items-center gap-2.5">
          <span
            className="grid h-9 w-9 place-items-center rounded-xl text-[13px] font-black text-black"
            style={{ backgroundColor: 'rgb(var(--landing-accent))' }}
          >
            BT
          </span>
          <span className="text-lg font-black tracking-tight">BINARY</span>
        </NavLink>

        {/* Floating pill, centred independently of the logo and the buttons so
            it stays in the middle of the page rather than the middle of
            whatever is left over. */}
        <nav className="absolute left-1/2 hidden -translate-x-1/2 items-center gap-1 rounded-full bg-white/[0.07] p-1.5 backdrop-blur-xl lg:flex">
          {NAV_LINKS.map((link) => (
            <a
              key={link.href}
              href={link.href}
              className="rounded-full px-4 py-2 text-sm font-medium text-white/70 transition hover:bg-white/10 hover:text-white"
            >
              {link.label}
            </a>
          ))}
        </nav>

        <div className="ml-auto flex shrink-0 items-center gap-2">
          <button type="button" onClick={() => onAuth('login')} className="landing-ghost hidden sm:inline-flex">
            Sign in
          </button>
          <button type="button" onClick={() => onAuth('register')} className="landing-cta px-5 py-2.5 text-sm">
            Try for free
          </button>
          <button
            type="button"
            onClick={onToggle}
            className="rounded-full p-2 text-white/70 transition hover:bg-white/10 hover:text-white lg:hidden"
            aria-label={open ? 'Close navigation' : 'Open navigation'}
            aria-expanded={open}
          >
            {open ? <X className="h-5 w-5" /> : <Menu className="h-5 w-5" />}
          </button>
        </div>
      </div>

      {open ? (
        <div className="mx-4 rounded-2xl bg-[#0e0f11] p-2 ring-1 ring-inset ring-white/10 lg:hidden">
          {NAV_LINKS.map((link) => (
            <a
              key={link.href}
              href={link.href}
              onClick={onToggle}
              className="block rounded-xl px-4 py-3 text-sm font-medium text-white/80 transition hover:bg-white/5"
            >
              {link.label}
            </a>
          ))}
          <button
            type="button"
            onClick={() => onAuth('login')}
            className="mt-1 block w-full rounded-xl px-4 py-3 text-left text-sm font-medium text-white/80 transition hover:bg-white/5 sm:hidden"
          >
            Sign in
          </button>
        </div>
      ) : null}
    </header>
  );
}

/* ----------------------------------------------------------------- hero -- */

function Hero({ onAuth, board }: { onAuth: (tab: AuthTab) => void; board: Board }) {
  return (
    <section className="relative flex min-h-[92vh] items-center overflow-hidden px-4 pt-28 sm:px-6">
      {/* All three are backdrop, so they are painted before the copy. The bars
          were last in the source and landed on top of the chips. */}
      <Backdrop />
      <HeroPortrait />
      <RisingBars />

      <div className="relative z-10 mx-auto w-full max-w-4xl text-center">
        <p className="animate-rise text-sm font-semibold text-white/50" style={{ animationDelay: '40ms' }}>
          Fixed-return trading on Bitcoin and Ethereum
        </p>

        {/* The shadow costs nothing on a black page and is what keeps the
            headline legible once a photograph is behind it. */}
        <h1
          className="animate-rise mt-5 text-[2.6rem] font-black leading-[1.03] tracking-tight sm:text-6xl lg:text-7xl"
          style={{ animationDelay: '120ms', textShadow: '0 2px 28px rgba(0,0,0,0.65)' }}
        >
          Build confidence
          <br />
          with every single trade
        </h1>

        <p
          className="animate-rise mx-auto mt-6 max-w-xl text-base leading-relaxed text-white/55 sm:text-lg"
          style={{ animationDelay: '200ms' }}
        >
          Pick a direction and an expiry from 30 seconds. Payouts move with the market, and you see yours before
          you commit
          {board.best !== null ? (
            <>
              {' '}— up to <span style={{ color: 'rgb(var(--landing-accent))' }}>{payoutText(board.best)}</span>{' '}
              right now
            </>
          ) : null}
          .
        </p>

        <div
          className="animate-rise mt-9 flex flex-col items-center gap-3 sm:flex-row sm:justify-center"
          style={{ animationDelay: '280ms' }}
        >
          <button type="button" onClick={() => onAuth('register')} className="landing-cta w-full sm:w-auto">
            Start now for ₹0
            <ArrowRight className="h-4 w-4" />
          </button>
          <a
            href="#how"
            className="inline-flex items-center gap-1 px-2 py-3 text-sm font-semibold text-white/70 transition hover:text-white"
          >
            Learn more
            <ChevronRight className="h-4 w-4" />
          </a>
        </div>

        <div
          className="animate-rise mt-14 flex flex-wrap justify-center gap-2"
          style={{ animationDelay: '360ms' }}
        >
          {CHIPS.map((chip) => (
            // Solid rather than a translucent tint: the bars rise behind these,
            // and a see-through chip stops being readable the moment one does.
            <span
              key={chip}
              className="rounded-full bg-[#0e0f11]/90 px-4 py-2 text-xs font-medium text-white/70 ring-1 ring-inset ring-white/10 backdrop-blur-sm"
            >
              {chip}
            </span>
          ))}
        </div>
      </div>
    </section>
  );
}

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
 */
function barPath(x: number, top: number, height: number): string {
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
  const along = (p: { x: number; y: number }, u: { x: number; y: number }, d: number) =>
    (p.x + u.x * d).toFixed(2) + ' ' + (p.y + u.y * d).toFixed(2);

  let d = '';
  for (let i = 0; i < 4; i += 1) {
    const here = corners[i]!;
    const next = corners[(i + 1) % 4]!;
    const into = unit(here, next);
    const from = unit(corners[(i + 3) % 4]!, here);

    // Round the corner we are standing on, then run to the next one.
    d += (i === 0 ? 'M' : 'L') + along(here, from, -BAR_RADIUS);
    d += 'Q' + here.x.toFixed(2) + ' ' + here.y.toFixed(2) + ' ' + along(here, into, BAR_RADIUS);
    d += 'L' + along(next, into, -BAR_RADIUS);
  }
  return d + 'Z';
}

function RisingBars() {
  return (
    <div
      aria-hidden
      className="pointer-events-none absolute inset-x-0 bottom-0 flex justify-center"
      style={{
        // They rise out of the bottom edge rather than standing in front of
        // the copy, so the tops dissolve before they reach the chips.
        maskImage: 'linear-gradient(to top, #000 32%, transparent 98%)',
        WebkitMaskImage: 'linear-gradient(to top, #000 32%, transparent 98%)',
      }}
    >
      <svg
        viewBox="0 0 340 367"
        className="h-[34vh] max-h-[330px] w-auto min-w-[340px] sm:h-[38vh]"
        preserveAspectRatio="xMidYMax meet"
      >
        <defs>
          {/* Flat to halfway down, then away to black - so the bars read as
              standing in the dark rather than sitting on a band. */}
          <linearGradient id="landing-bars" x1="170" y1="0" x2="170" y2="366.164" gradientUnits="userSpaceOnUse">
            <stop offset="0.5" stopColor="#1b1b1b" />
            <stop offset="1" stopColor="#000000" />
          </linearGradient>
        </defs>

        {BARS.map(([x, top, height], index) => (
          <path
            key={index}
            d={barPath(x, top, height)}
            fill="url(#landing-bars)"
            className="animate-candle-grow"
            style={{
              transformOrigin: `0 ${top + height}px`,
              animationDelay: 120 + index * 55 + 'ms',
            }}
          />
        ))}
      </svg>
    </div>
  );
}

/**
 * The photograph behind the hero.
 *
 * An optional file rather than a bundled asset. Drop a picture at
 * `client/public/hero.jpg` and it appears behind the headline; leave it out
 * and the hero is exactly what it was, lit by the gradient backdrop alone.
 * Nothing here ships a stock photo of its own: a face on a trading site reads
 * as a customer or an endorsement, and that is the site owner's call to make,
 * not a default to inherit.
 *
 * The treatment is what makes a photograph usable as a background rather than
 * a distraction - it is desaturated slightly, dimmed, and faded out at the top
 * and bottom so the nav has something to sit on and the headline keeps its
 * contrast wherever the subject happens to be.
 */
const HERO_PHOTO = '/hero.jpg';

function HeroPortrait() {
  const [failed, setFailed] = useState(false);

  // No photograph, nothing drawn in its place: the glow and the rising bars
  // carry the hero on their own, and a stand-in figure only read as a smudge
  // behind the headline.
  if (failed) return null;

  return (
    <div aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden">
      <img
        src={HERO_PHOTO}
        alt=""
        onError={() => setFailed(true)}
        className="animate-fade-in absolute left-1/2 top-0 h-full max-w-none -translate-x-1/2 object-cover object-top"
        style={{ filter: 'saturate(0.85) brightness(0.72)' }}
      />
      {/* Readability, from three directions: down from the nav, up from the
          chips, and in from the sides on a wide screen. */}
      <div
        className="absolute inset-0"
        style={{
          background:
            'linear-gradient(to bottom, #000 0%, rgba(0,0,0,0.42) 28%, rgba(0,0,0,0.55) 52%, rgba(0,0,0,0.82) 78%, #000 96%)',
        }}
      />
      <div
        className="absolute inset-0"
        style={{ background: 'linear-gradient(to right, #000, transparent 22%, transparent 78%, #000)' }}
      />
    </div>
  );
}

/**
 * The glow behind the hero.
 *
 * Two slow-drifting radial washes rather than an image: it carries the brand
 * colour into the page, costs nothing to load, and cannot pixelate on a wide
 * screen. Marked `aria-hidden` because it says nothing.
 */
function Backdrop() {
  return (
    <div aria-hidden className="pointer-events-none absolute inset-0 overflow-hidden">
      <div
        className="animate-drift absolute -top-40 left-1/2 h-[560px] w-[560px] -translate-x-1/2 rounded-full blur-[120px]"
        style={{ background: 'rgb(var(--landing-accent) / 0.16)' }}
      />
      <div
        className="animate-drift absolute -right-32 top-1/3 h-[420px] w-[420px] rounded-full blur-[130px]"
        style={{ background: 'rgb(var(--up) / 0.12)', animationDelay: '-9s' }}
      />
      <div
        className="absolute inset-x-0 bottom-0 h-56"
        style={{ background: 'linear-gradient(to top, #000, transparent)' }}
      />
    </div>
  );
}

/* --------------------------------------------------------------- ticker -- */

/**
 * The instrument strip.
 *
 * A signed-out visitor cannot be shown live quotes - the price feed is behind
 * authentication - so this carries what the public board gives: each
 * instrument's current payout at one minute, and no figure while there is
 * none to read.
 */
function Ticker({ board }: { board: Board }) {
  // Repeated so the strip is as long as the five-instrument one was and the
  // loop never shows a gap on a wide screen; an even number of runs, because
  // one loop moves the strip by half its length.
  const runs = Math.ceil(10 / Math.max(1, board.markets.length));
  const row = Array.from({ length: runs + (runs % 2) }, () => board.markets).flat();
  return (
    <section className="relative border-y border-white/10 py-5">
      <div className="flex overflow-hidden [mask-image:linear-gradient(to_right,transparent,#000_12%,#000_88%,transparent)]">
        <div className="animate-marquee flex shrink-0 items-center gap-10 pr-10">
          {row.map((market, index) => (
            <span key={market.symbol + index} className="flex shrink-0 items-center gap-3 whitespace-nowrap">
              <span className="text-sm font-bold text-white/80">{market.symbol}</span>
              <TickerPayout percent={tickerPayout(market)} />
              <span className="text-xs text-white/35">{market.kind}</span>
            </span>
          ))}
        </div>
        <div aria-hidden className="animate-marquee flex shrink-0 items-center gap-10 pr-10">
          {row.map((market, index) => (
            <span key={market.symbol + index} className="flex shrink-0 items-center gap-3 whitespace-nowrap">
              <span className="text-sm font-bold text-white/80">{market.symbol}</span>
              <TickerPayout percent={tickerPayout(market)} />
              <span className="text-xs text-white/35">{market.kind}</span>
            </span>
          ))}
        </div>
      </div>
    </section>
  );
}

/** Nothing at all when there is no live figure: the symbol stands on its own. */
function TickerPayout({ percent }: { percent: number | undefined }) {
  if (percent === undefined) return null;
  return (
    <span className="text-sm font-bold tnum" style={{ color: 'rgb(var(--landing-accent))' }}>
      {payoutText(percent)}
    </span>
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
function Preview() {
  return (
    <section id="preview" className="scroll-mt-24 px-4 py-20 sm:px-6 sm:py-24">
      <div className="mx-auto max-w-6xl">
        <Reveal>
          <ProductPreview />
        </Reveal>
      </div>
    </section>
  );
}

/** A trade running live: the amount and the duration are the trader's call. */
function ChooseTrade() {
  return (
    <section className="px-4 pb-4 sm:px-6">
      <div className="mx-auto max-w-6xl">
        <Reveal>
          <AmountDuration />
        </Reveal>
      </div>
    </section>
  );
}

function AnyDevice() {
  return (
    <section className="px-4 py-20 sm:px-6 sm:py-24">
      <div className="mx-auto max-w-6xl">
        <Reveal>
          <Devices />
        </Reveal>
      </div>
    </section>
  );
}

function Money() {
  return (
    <Section
      id="funding"
      title={
        <>
          <span style={{ color: 'rgb(var(--landing-accent))' }}>UPI, IMPS and NEFT</span>
          <br />
          for money in and out
        </>
      }
      lead="Every payment is settled against a reference you submit yourself, so a deposit can always be traced to a transfer you can see in your own banking app."
    >
      <Funding />
    </Section>
  );
}

function Questions() {
  return (
    <Section
      id="faq"
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

function Highlights({ board }: { board: Board }) {
  return (
    <Section id="highlights" title="Everything you need to place a trade" lead="">
      <div className="grid gap-4 md:grid-cols-2">
        <Card title="Start from ₹100" sub="Up to ₹50,000 on a single position">
          <CoinArt />
        </Card>
        <Card title="Expiries from 30 seconds" sub="30 seconds, 1 minute or 5 minutes">
          <ClockArt />
        </Card>
        <Card
          title={
            board.best !== null ? 'Payouts up to ' + payoutText(board.best) + ' right now' : 'Payout shown up front'
          }
          sub="Moves with the market, locked once your trade is accepted"
        >
          <ChartArt />
        </Card>
        <Card title="Your entry never moves" sub="Frozen the moment the order lands">
          <OrderArt />
        </Card>
      </div>
    </Section>
  );
}

function Card({ title, sub, children }: { title: string; sub: string; children: ReactNode }) {
  return (
    <Reveal>
      <div className="landing-card flex h-full min-h-[320px] flex-col p-7 sm:p-8">
        <h3 className="text-center text-2xl font-bold tracking-tight">{title}</h3>
        <p className="mt-1.5 text-center text-sm text-white/45">{sub}</p>
        <div className="relative mt-6 flex flex-1 items-center justify-center">{children}</div>
      </div>
    </Reveal>
  );
}

/* --------------------------------------------------------------- artwork -- */

/** A stack of coins, lit from the top left. */
function CoinArt() {
  return (
    <div className="animate-float relative h-40 w-40">
      {[0, 1, 2].map((i) => (
        <div
          key={i}
          className="absolute left-0 h-32 w-32 rounded-full"
          style={{
            top: 16 + i * 9,
            background:
              i === 0
                ? 'radial-gradient(circle at 32% 28%, #fff6c9, rgb(var(--landing-accent)) 42%, #9e8412 100%)'
                : 'linear-gradient(180deg, #8d761a, #5d4d0f)',
            boxShadow: i === 0 ? '0 18px 44px -12px rgb(var(--landing-accent) / 0.6)' : undefined,
            zIndex: 3 - i,
          }}
        />
      ))}
      <span className="absolute left-0 top-4 z-10 grid h-32 w-32 place-items-center text-2xl font-black text-black/70">
        ₹100
      </span>
    </div>
  );
}

/** A clock whose second hand is the only thing moving. */
function ClockArt() {
  return (
    <div className="relative h-40 w-40">
      <div className="absolute inset-0 rounded-full ring-2 ring-inset ring-white/10" />
      {Array.from({ length: 60 }, (_, i) => (
        <span
          key={i}
          className="absolute left-1/2 top-0 h-40 w-px origin-bottom"
          style={{ transform: `rotate(${i * 6}deg)`, height: '80px' }}
        >
          <span
            className="block rounded-full"
            style={{
              width: i % 5 === 0 ? 3 : 1.5,
              height: i % 5 === 0 ? 10 : 6,
              background: i < 5 ? 'rgb(var(--landing-accent))' : 'rgba(255,255,255,0.22)',
            }}
          />
        </span>
      ))}
      <span
        className="absolute left-1/2 top-1/2 h-[3px] w-14 origin-left rounded-full bg-white/70"
        style={{ transform: 'rotate(-58deg)' }}
      />
      <span
        className="absolute left-1/2 top-1/2 h-[3px] w-[72px] origin-left rounded-full"
        style={{ background: 'rgb(var(--landing-accent))', transform: 'rotate(-12deg)' }}
      />
      <span className="absolute left-1/2 top-1/2 h-3 w-3 -translate-x-1/2 -translate-y-1/2 rounded-full bg-white" />
    </div>
  );
}

/** A rising run of candles that grows when it scrolls into view. */
function ChartArt() {
  const bars = [30, 44, 38, 56, 50, 68, 62, 84];
  return (
    <div className="flex h-40 w-full items-end justify-center gap-2.5">
      {bars.map((height, i) => (
        <span
          key={i}
          className="animate-candle-grow w-5 origin-bottom rounded-md"
          style={{
            height: height + '%',
            animationDelay: i * 70 + 'ms',
            background:
              i === bars.length - 1
                ? 'linear-gradient(180deg, rgb(var(--landing-accent)), rgb(var(--landing-accent) / 0.35))'
                : 'linear-gradient(180deg, rgb(var(--up) / 0.85), rgb(var(--up) / 0.2))',
          }}
        />
      ))}
    </div>
  );
}

/** The order marker the live chart actually draws. */
function OrderArt() {
  return (
    <svg
      viewBox="0 0 260 120"
      className="w-full max-w-[400px]"
      role="img"
      aria-label="An order marked on a chart"
    >
      {[30, 60, 90].map((y) => (
        <line key={y} x1="0" y1={y} x2="260" y2={y} stroke="rgba(255,255,255,0.06)" strokeWidth="1" />
      ))}
      {[
        [18, 74],
        [40, 66],
        [62, 80],
        [84, 58],
        [106, 64],
        [128, 50],
      ].map(([x, y], i) => (
        <rect
          key={i}
          x={x}
          y={Math.min(y!, 62)}
          width="9"
          height={Math.max(10, Math.abs(y! - 62))}
          rx="2"
          fill={y! < 62 ? 'rgb(var(--up))' : 'rgb(var(--down))'}
          opacity="0.75"
        />
      ))}
      <line x1="170" y1="52" x2="236" y2="52" stroke="rgb(var(--landing-accent))" strokeWidth="2" />
      <polygon points="236,45 231,52 241,52" fill="rgb(var(--landing-accent))" />
      <circle cx="170" cy="52" r="6" fill="rgb(var(--landing-accent))" />
      <g transform="translate(86, 41)">
        <path d="M0 0 H70 L78 11 L70 22 H0 Z" fill="rgb(var(--landing-accent))" />
        <text x="12" y="15" fontSize="11" fontWeight="700" fill="#0b0c0e">
          ↑ ₹500
        </text>
      </g>
      <line
        x1="236"
        y1="8"
        x2="236"
        y2="112"
        stroke="rgb(var(--landing-accent))"
        strokeWidth="1"
        strokeDasharray="4 4"
        opacity="0.5"
      />
    </svg>
  );
}

/* -------------------------------------------------------------- markets -- */

function Markets({ board }: { board: Board }) {
  // One column per expiry the engine offers, however many that is.
  const columns = { '--landing-cols': board.expiries.length } as CSSProperties;
  return (
    <Section
      id="markets"
      title="Bitcoin and Ethereum, priced live"
      lead="Payouts move with the market and differ by expiry. The figure for your trade is on the ticket before you commit, and it does not change once your trade is accepted."
    >
      <Reveal>
        <div className="landing-card p-2 hover:translate-y-0" style={columns}>
          <div className="hidden grid-cols-[1.4fr_repeat(var(--landing-cols),1fr)] gap-4 px-5 py-3 text-[11px] font-bold uppercase tracking-wider text-white/35 sm:grid">
            <span>Instrument</span>
            {board.expiries.map((expiry) => (
              <span key={expiry} className="text-right">
                {expiryLabel(expiry)}
              </span>
            ))}
          </div>

          {board.markets.map((market) => (
            <div
              key={market.symbol}
              className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-2xl px-5 py-4 transition hover:bg-white/[0.04] sm:grid-cols-[1.4fr_repeat(var(--landing-cols),1fr)] sm:items-center sm:gap-4"
            >
              <div className="col-span-2 flex items-center gap-3 sm:col-span-1">
                <span className="grid h-9 w-9 shrink-0 place-items-center rounded-xl bg-white/[0.07] text-[10px] font-black text-white/60">
                  {market.symbol.slice(0, 3)}
                </span>
                <span className="min-w-0">
                  <span className="block truncate text-sm font-bold">{market.symbol}</span>
                  <span className="block truncate text-xs text-white/40">
                    {market.name} · {market.kind}
                  </span>
                </span>
              </div>

              {board.expiries.map((expiry) => (
                <Payout key={expiry} expiry={expiry} percent={market.payouts[expiry]} />
              ))}
            </div>
          ))}
        </div>
      </Reveal>

      <p className="mt-4 text-xs text-white/35">
        {board.best !== null
          ? 'Live indicative payouts from the pricing engine, refreshed every few seconds: at each expiry, the lower of the UP and DOWN figures. A dash means that expiry cannot be priced right now.'
          : board.loading
            ? 'Reading the live payouts…'
            : 'Live payouts cannot be shown right now. The trade ticket shows yours before you commit.'}
      </p>
    </Section>
  );
}

function Payout({ expiry, percent }: { expiry: number; percent: number | undefined }) {
  return (
    <span className="flex items-baseline justify-between gap-2 sm:block sm:text-right">
      <span className="text-[11px] font-semibold uppercase tracking-wide text-white/35 sm:hidden">
        {expiryLabel(expiry)}
      </span>
      <span
        className="text-sm font-bold tnum"
        style={{ color: percent ? 'rgb(var(--landing-accent))' : 'rgba(255,255,255,0.3)' }}
      >
        {percent ? payoutText(percent) : '—'}
      </span>
    </span>
  );
}

/* ---------------------------------------------------------------- steps -- */

function Steps() {
  return (
    <Section
      id="how"
      title="Four steps from sign-up to settlement"
      lead="The demo wallet uses the same prices and the same order flow as a funded account, so nothing changes when you switch."
    >
      <ol className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {STEPS.map((step, index) => (
          <Reveal key={step.title} delay={index * 90}>
            <li className="landing-card h-full p-6">
              <span
                className="grid h-10 w-10 place-items-center rounded-2xl text-sm font-black text-black"
                style={{ backgroundColor: 'rgb(var(--landing-accent))' }}
              >
                {index + 1}
              </span>
              <h3 className="mt-5 text-lg font-bold">{step.title}</h3>
              <p className="mt-2 text-sm leading-relaxed text-white/50">{step.body}</p>
            </li>
          </Reveal>
        ))}
      </ol>
    </Section>
  );
}

/* ------------------------------------------------------------- platform -- */

function Platform() {
  return (
    <Section
      id="platform"
      title="A chart built for short expiries"
      lead="Everything below is on the trading screen today — no upgrade, no separate terminal."
    >
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
        {PLATFORM.map((feature, index) => (
          <Reveal key={feature.title} delay={(index % 3) * 90}>
            <div className="landing-card h-full p-6">
              <feature.icon className="h-6 w-6" style={{ color: 'rgb(var(--landing-accent))' }} />
              <h3 className="mt-5 text-lg font-bold">{feature.title}</h3>
              <p className="mt-2 text-sm leading-relaxed text-white/50">{feature.body}</p>
            </div>
          </Reveal>
        ))}
      </div>
    </Section>
  );
}

function Safeguards() {
  return (
    <Section
      id="safeguards"
      title="What happens when something goes wrong"
      lead="Short expiries mean a fast market can move between your click and the server. These are the protections that exist for that."
    >
      <div className="grid gap-4 md:grid-cols-3">
        {SAFEGUARDS.map((item, index) => (
          <Reveal key={item.title} delay={index * 90}>
            <div className="landing-card h-full p-6">
              <item.icon className="h-6 w-6 text-up" />
              <h3 className="mt-5 text-lg font-bold">{item.title}</h3>
              <p className="mt-2 text-sm leading-relaxed text-white/50">{item.body}</p>
            </div>
          </Reveal>
        ))}
      </div>
    </Section>
  );
}

/* ------------------------------------------------------------------ cta -- */

function ClosingCta({ onAuth }: { onAuth: (tab: AuthTab) => void }) {
  return (
    <section className="px-4 py-20 sm:px-6">
      <Reveal>
        <div
          className="relative mx-auto max-w-6xl overflow-hidden rounded-[2rem] px-6 py-16 text-center sm:px-10 sm:py-20"
          style={{
            background: 'linear-gradient(135deg, rgb(var(--landing-accent)) 0%, #f5c531 45%, #e2a81f 100%)',
          }}
        >
          <h2 className="text-3xl font-black tracking-tight text-black sm:text-5xl">Start trading confidently</h2>
          <p className="mx-auto mt-4 max-w-lg text-base text-black/65">
            Open the demo and place one trade. It takes a minute and costs nothing.
          </p>
          <div className="mt-9 flex flex-col items-center gap-3 sm:flex-row sm:justify-center">
            <button
              type="button"
              onClick={() => onAuth('register')}
              className="inline-flex w-full items-center justify-center gap-2 rounded-full bg-black px-7 py-3.5 text-base font-bold text-white transition hover:bg-black/85 sm:w-auto"
            >
              Create account
              <ArrowRight className="h-4 w-4" />
            </button>
            <button
              type="button"
              onClick={() => onAuth('login')}
              className="inline-flex w-full items-center justify-center rounded-full px-7 py-3.5 text-base font-bold text-black/75 ring-1 ring-inset ring-black/20 transition hover:bg-black/10 sm:w-auto"
            >
              I already have one
            </button>
          </div>
        </div>
      </Reveal>
    </section>
  );
}

/* --------------------------------------------------------------- footer -- */

function SiteFooter() {
  return (
    <footer className="border-t border-white/10 px-4 py-14 sm:px-6">
      <div className="mx-auto max-w-6xl">
        <div className="grid gap-8 sm:grid-cols-2 lg:grid-cols-4">
          <div className="lg:col-span-2">
            <div className="flex items-center gap-2.5">
              <span
                className="grid h-9 w-9 place-items-center rounded-xl text-[13px] font-black text-black"
                style={{ backgroundColor: 'rgb(var(--landing-accent))' }}
              >
                BT
              </span>
              <span className="text-lg font-black tracking-tight">BINARY</span>
            </div>
            <p className="mt-4 max-w-sm text-sm leading-relaxed text-white/40">
              Fixed-return trading on Bitcoin and Ethereum, with expiries from 30 seconds.
            </p>
          </div>

          <div>
            <h3 className="text-[11px] font-bold uppercase tracking-wider text-white/35">Platform</h3>
            <ul className="mt-3 space-y-2">
              {NAV_LINKS.map((link) => (
                <li key={link.href}>
                  <a href={link.href} className="text-sm text-white/55 transition hover:text-white">
                    {link.label}
                  </a>
                </li>
              ))}
            </ul>
          </div>

          <div>
            <h3 className="text-[11px] font-bold uppercase tracking-wider text-white/35">Account</h3>
            <ul className="mt-3 space-y-2">
              <li>
                <NavLink to="/login" className="text-sm text-white/55 transition hover:text-white">
                  Log in
                </NavLink>
              </li>
              <li>
                <NavLink to="/register" className="text-sm text-white/55 transition hover:text-white">
                  Create account
                </NavLink>
              </li>
            </ul>
          </div>
        </div>

        {/* The risk warning is the one thing here that has to be read, so it is
            not tucked into a line of small print beside the copyright. */}
        <div className="mt-12 rounded-2xl bg-white/[0.04] p-6 ring-1 ring-inset ring-white/10">
          <h3 className="text-sm font-bold">Risk warning</h3>
          <p className="mt-2 text-xs leading-relaxed text-white/40">
            Fixed-return trading carries a high level of risk. A losing position costs the full amount staked, and
            it is possible to lose more than you intended over a series of trades. Only trade with money you can
            afford to lose, and do not treat past results — your own or anyone else&apos;s — as an indication of
            what will happen next. Nothing on this site is investment advice.
          </p>
        </div>

        {/*
         * The disclosure block.
         *
         * Everything a visitor is entitled to know before they read another
         * word of marketing: who they would be contracting with, who may not
         * open an account, and what the product is. The operating entity,
         * its licence and the restricted countries are the site owner's to
         * fill in - they are marked here rather than invented, because a made
         * up company number on a financial site is not a placeholder, it is a
         * false statement.
         */}
        <div className="mt-8 space-y-4 border-t border-white/10 pt-8 text-xs leading-relaxed text-white/35">
          <p>
            Transactions offered on this site may be entered into only by competent adults. Trading fixed-return
            contracts involves substantial risk: you can lose the full amount you stake on any single trade, and
            repeated losses can consume an account. Before you begin, read the{' '}
            <NavLink to="/terms" className="underline hover:text-white/60">
              Client Agreement
            </NavLink>{' '}
            and the{' '}
            <NavLink to="/privacy" className="underline hover:text-white/60">
              Privacy Policy
            </NavLink>
            . Nothing on this site is investment advice or a recommendation to trade.
          </p>
          <p>
            Prices shown are provided for settling trades on this platform and are not an offer to buy or sell any
            underlying asset. A fixed-return trade gives you no ownership of, and no rights over, the instrument
            it is priced against.
          </p>
          <p className="text-white/25">
            Operating entity, company registration, registered address, licence details and the list of countries
            from which accounts cannot be opened must be added here before this platform accepts a real client.
          </p>
          <p className="pt-2">© {new Date().getFullYear()} Binary. All rights reserved.</p>
        </div>
      </div>
    </footer>
  );
}

/* ------------------------------------------------------------ scaffolding -- */

function Section({
  id,
  title,
  lead,
  children,
}: {
  id?: string;
  /** Usually a string; a fragment when a line of it takes the brand colour. */
  title: ReactNode;
  lead: string;
  children: ReactNode;
}) {
  return (
    <section id={id} className="scroll-mt-24 px-4 py-20 sm:px-6 sm:py-24">
      <div className="mx-auto max-w-6xl">
        <Reveal>
          <div className="mx-auto max-w-2xl text-center">
            <h2 className="text-3xl font-black tracking-tight sm:text-4xl">{title}</h2>
            {lead ? <p className="mt-4 text-base leading-relaxed text-white/50">{lead}</p> : null}
          </div>
        </Reveal>
        <div className="mt-12">{children}</div>
      </div>
    </section>
  );
}

/**
 * Plays an entrance the first time its contents scroll into view.
 *
 * The class is added rather than the element being rendered late, so the
 * content is in the DOM from the first paint - a crawler, a reader mode or a
 * browser that never fires the observer all still get the whole page. A single
 * observer per element is cheap, and it disconnects once it has fired.
 */
function Reveal({ children, delay = 0 }: { children: ReactNode; delay?: number }) {
  const ref = useRef<HTMLDivElement>(null);
  const [shown, setShown] = useState(false);

  useEffect(() => {
    const node = ref.current;
    if (!node || shown) return;
    if (typeof IntersectionObserver === 'undefined') return setShown(true);

    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) {
          setShown(true);
          observer.disconnect();
        }
      },
      { rootMargin: '0px 0px -12% 0px' },
    );
    observer.observe(node);
    return () => observer.disconnect();
  }, [shown]);

  return (
    <div
      ref={ref}
      className={clsx('h-full', shown ? 'animate-rise' : 'opacity-0')}
      style={shown && delay ? { animationDelay: delay + 'ms' } : undefined}
    >
      {children}
    </div>
  );
}
