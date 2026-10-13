import { useEffect, useRef, useState, type ReactNode } from 'react';
import clsx from 'clsx';
import { Check, ChevronDown, Globe, Laptop, Smartphone } from 'lucide-react';
import { WorkspaceMock } from './WorkspaceMock';
import { FitBox } from './FitBox';
import { PaymentCarousel } from './PaymentCarousel';

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
    <div className="grid grid-cols-1 items-center gap-10 lg:grid-cols-[minmax(0,1.15fr)_minmax(0,1fr)] lg:gap-14">
      <BrowserFrame>
        <FitBox width={900} height={450}>
          <WorkspaceMock />
        </FitBox>
      </BrowserFrame>

      <div className="min-w-0">
        <h2 className="text-3xl font-black tracking-tight sm:text-4xl">This is the whole platform</h2>
        <p className="mt-4 text-base leading-relaxed text-white/50">
          One screen. The chart, the ticket and your open positions side by side, so placing a trade never means
          leaving the price you are watching.
        </p>
        <ul className="mt-7 space-y-3">
          {PREVIEW_POINTS.map((point) => (
            <li key={point} className="flex items-start gap-3">
              <span
                className="mt-0.5 grid h-5 w-5 shrink-0 place-items-center rounded-full"
                style={{ backgroundColor: 'rgb(var(--landing-accent) / 0.16)' }}
              >
                <Check className="h-3 w-3" style={{ color: 'rgb(var(--landing-accent))' }} />
              </span>
              <span className="text-sm leading-relaxed text-white/70">{point}</span>
            </li>
          ))}
        </ul>
      </div>
    </div>
  );
}

/** A window, so the workspace reads as a running app rather than a diagram. */
function BrowserFrame({ children }: { children: ReactNode }) {
  return (
    <div className="landing-card hover:translate-y-0">
      <div
        aria-hidden
        className="pointer-events-none absolute -inset-16 -z-10 blur-[90px]"
        style={{ background: 'rgb(var(--landing-accent) / 0.10)' }}
      />
      <div className="flex items-center gap-2 border-b border-white/10 bg-white/[0.03] px-4 py-2.5">
        <span className="flex gap-1.5">
          {['#ff5f57', '#febc2e', '#28c840'].map((colour) => (
            <span key={colour} className="h-2.5 w-2.5 rounded-full" style={{ backgroundColor: colour }} />
          ))}
        </span>
        <span className="mx-auto rounded-md bg-white/[0.06] px-3 py-1 text-[10px] text-white/40">
          binary.trade/trade
        </span>
      </div>
      {children}
    </div>
  );
}

/* ------------------------------------------------------------- devices -- */

const DEVICES = [
  { icon: Laptop, title: 'Desktop', body: 'The full workspace, with the trade list beside the chart.' },
  { icon: Smartphone, title: 'Phone', body: 'The same chart and ticket, stacked for a narrow screen.' },
  { icon: Globe, title: 'Any browser', body: 'Nothing to install and nothing to keep updated.' },
];

export function Devices() {
  return (
    <div className="grid grid-cols-1 items-center gap-12 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.1fr)]">
      <div className="min-w-0">
        <h2 className="text-3xl font-black tracking-tight sm:text-4xl">Trade from whatever you have open</h2>
        <p className="mt-4 text-base leading-relaxed text-white/50">
          One build, one account. The layout rearranges for the screen it is on — the chart, the order ticket and
          your positions stay where you expect them.
        </p>

        <div className="mt-8 space-y-3">
          {DEVICES.map((device) => (
            <div
              key={device.title}
              className="flex items-start gap-4 rounded-2xl bg-white/[0.04] p-4 ring-1 ring-inset ring-white/10"
            >
              <device.icon className="mt-0.5 h-5 w-5 shrink-0" style={{ color: 'rgb(var(--landing-accent))' }} />
              <span>
                <span className="block text-sm font-bold">{device.title}</span>
                <span className="mt-0.5 block text-sm text-white/45">{device.body}</span>
              </span>
            </div>
          ))}
        </div>
      </div>

      {/* A phone in front of a laptop, both running the same workspace. */}
      <div className="relative mx-auto w-full min-w-0 max-w-[520px]">
        <div className="rounded-2xl bg-[#17181b] p-2.5 ring-1 ring-inset ring-white/10">
          <FitBox width={900} height={506} className="rounded-xl">
            <WorkspaceMock />
          </FitBox>
        </div>
        <div
          aria-hidden
          className="mx-auto h-2.5 w-2/3 rounded-b-xl bg-[#17181b] ring-1 ring-inset ring-white/10"
        />

        <div className="animate-float absolute -bottom-8 -right-2 w-[132px] sm:-right-6 sm:w-[156px]">
          <div className="rounded-[1.6rem] bg-[#17181b] p-1.5 shadow-2xl ring-1 ring-inset ring-white/15">
            <div className="relative aspect-[9/17] overflow-hidden rounded-[1.25rem]">
              <span
                aria-hidden
                className="absolute left-1/2 top-1 z-10 h-1 w-8 -translate-x-1/2 rounded-full bg-white/25"
              />
              <FitBox width={220} height={415} className="h-full w-full">
                <WorkspaceMock compact />
              </FitBox>
            </div>
          </div>
        </div>
      </div>
    </div>
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
    q: 'What happens if the price moves as I click?',
    a: 'The order is refused rather than filled at a price you did not choose. You are shown what the market has moved to and asked whether you still want it - so a fast market costs you a confirmation, not a worse entry.',
  },
  {
    q: 'Can I get out before the expiry?',
    a: 'Yes. Every open position carries a live sell-now quote. Taking it closes the trade immediately at that value, which may be above or below your stake depending on where the price has gone and how long is left.',
  },
  {
    q: 'Which markets can I trade?',
    a: 'Bitcoin and Ethereum against the US dollar, EUR/USD and GBP/USD, and gold. Each one has its own payout table, and longer expiries pay more than shorter ones.',
  },
  {
    q: 'Do I need to install anything?',
    a: 'No. The platform runs in any modern browser on a desktop or a phone. There is nothing to download and nothing to keep updated.',
  },
  {
    q: 'How is my entry price decided?',
    a: 'By the market at the instant the server accepts the order, not at the instant you clicked. Once accepted it is frozen: it is drawn on your chart and used to settle the trade, and nothing that happens afterwards changes it.',
  },
];

export function Faq() {
  const [open, setOpen] = useState<number | null>(0);

  return (
    <div className="mx-auto max-w-3xl">
      <div className="space-y-2.5">
        {FAQ.map((item, index) => {
          const isOpen = open === index;
          return (
            <div
              key={item.q}
              className={clsx(
                'overflow-hidden rounded-2xl ring-1 ring-inset transition',
                isOpen ? 'bg-white/[0.06] ring-white/15' : 'bg-white/[0.03] ring-white/10 hover:ring-white/20',
              )}
            >
              <button
                type="button"
                onClick={() => setOpen(isOpen ? null : index)}
                aria-expanded={isOpen}
                className="flex w-full items-center gap-4 px-5 py-4 text-left sm:px-6 sm:py-5"
              >
                <span className="flex-1 text-base font-bold sm:text-lg">{item.q}</span>
                <ChevronDown
                  className={clsx('h-5 w-5 shrink-0 text-white/40 transition-transform', isOpen && 'rotate-180')}
                />
              </button>
              {isOpen ? (
                <p className="animate-fade-in px-5 pb-5 text-sm leading-relaxed text-white/55 sm:px-6 sm:pb-6">
                  {item.a}
                </p>
              ) : null}
            </div>
          );
        })}
      </div>
    </div>
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
    <div>
      {/* The rails first, as marks a visitor already recognises from their own
          banking app; the limits and the steps follow for each one. */}
      <Reveal>
        <PaymentCarousel />
      </Reveal>

      <div className="mt-10 grid gap-4 md:grid-cols-2">
        {FUNDING.map((method) => (
          <Reveal key={method.label}>
            <div className="landing-card flex h-full flex-col p-7 sm:p-8">
              <div className="flex items-baseline justify-between gap-4">
                <h3 className="text-2xl font-bold tracking-tight">{method.label}</h3>
                <span className="text-sm text-white/40">{method.detail}</span>
              </div>

              <dl className="mt-7 grid grid-cols-2 gap-px overflow-hidden rounded-2xl bg-white/10">
                {[
                  ['Minimum', method.min],
                  ['Maximum', method.max],
                ].map(([term, value]) => (
                  <div key={term} className="bg-[#0e0f11] px-5 py-4">
                    <dt className="text-[11px] font-bold uppercase tracking-wider text-white/35">{term}</dt>
                    <dd className="mt-1 text-xl font-black tnum" style={{ color: 'rgb(var(--landing-accent))' }}>
                      {value}
                    </dd>
                  </div>
                ))}
              </dl>

              <p className="mt-6 text-sm leading-relaxed text-white/50">{method.how}</p>
            </div>
          </Reveal>
        ))}
      </div>
    </div>
  );
}

/**
 * Plays an entrance the first time its contents scroll into view.
 *
 * A local copy of the landing page's own reveal, so this file does not have to
 * import from the page that imports it.
 */
function Reveal({ children }: { children: ReactNode }) {
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
    <div ref={ref} className={clsx('h-full', shown ? 'animate-rise' : 'opacity-0')}>
      {children}
    </div>
  );
}
