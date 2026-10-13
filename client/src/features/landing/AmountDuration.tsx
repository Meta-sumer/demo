import { useEffect, useRef, useState } from 'react';
import clsx from 'clsx';

/**
 * "You choose the amount and duration" - an animated trade, running.
 *
 * A price line scrolls in from the right and meets the present at a dashed
 * rule; the level it is at right now is carried across to a price tab on the
 * edge. Behind it an order sits where it was placed - the same banner, dot,
 * run and expiry cap the live chart draws - and below, the expiry chips step
 * through the durations this platform actually offers, with the ticket's
 * Duration box following them.
 *
 * Everything moves from one frame loop that writes straight to a handful of
 * SVG attributes, so the card costs nothing while it is off screen (it only
 * runs in view) and nothing at all for anyone who has asked for reduced
 * motion, who get a still frame instead.
 *
 * The figures in the copy are this platform's: a ₹100 minimum stake and
 * expiries of 30 seconds, 1 minute and 5 minutes.
 */

/** One period of the price line, in viewBox units. The line is periodic in W. */
const W = 480;
/** The present: the line stops here and the price is read off it. */
const NOW_X = 300;
const EXPIRY_X = 364;
/** How fast the market scrolls, in viewBox units per millisecond. */
const SPEED = 0.028;

const DURATIONS = ['30 sec', '1 min', '5 min'];

/**
 * The price line's height at x.
 *
 * A sum of sines whose frequencies are whole multiples of the period, so the
 * curve joins itself exactly at W and the scroll can loop forever without a
 * seam. Four terms is enough to read as a market rather than a wave.
 */
function yAt(x: number): number {
  const t = (2 * Math.PI * x) / W;
  return (
    132 -
    38 * Math.sin(2 * t + 0.6) -
    22 * Math.sin(5 * t + 1.9) -
    10 * Math.sin(11 * t + 0.3) -
    5 * Math.sin(23 * t + 2.2)
  );
}

/** Two periods side by side, so a one-period shift always leaves it covered. */
const LINE_PATH = (() => {
  let d = '';
  for (let x = 0; x <= 2 * W; x += 2) d += (x === 0 ? 'M' : 'L') + x + ' ' + yAt(x).toFixed(2);
  return d;
})();

/** Maps a height on the chart to a price for the tab. Illustrative only. */
function priceFromY(y: number): string {
  return (1900 + (240 - y) * 0.12).toFixed(2);
}

export function AmountDuration() {
  const rootRef = useRef<HTMLDivElement>(null);
  const lineRef = useRef<SVGGElement>(null);
  const priceLineRef = useRef<SVGLineElement>(null);
  const dotRef = useRef<SVGGElement>(null);
  const tabRef = useRef<SVGGElement>(null);
  const tabTextRef = useRef<SVGTextElement>(null);

  const [visible, setVisible] = useState(false);
  const [active, setActive] = useState(1);
  const [reduced] = useState(
    () => typeof window !== 'undefined' && window.matchMedia('(prefers-reduced-motion: reduce)').matches,
  );

  // Only animate while the card is actually on screen.
  useEffect(() => {
    const node = rootRef.current;
    if (!node || typeof IntersectionObserver === 'undefined') {
      setVisible(true);
      return;
    }
    const observer = new IntersectionObserver((entries) => setVisible(entries.some((e) => e.isIntersecting)), {
      rootMargin: '120px 0px',
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);

  // The market: scroll the line, and carry its current level to the tab.
  useEffect(() => {
    const place = (offset: number, withText: boolean) => {
      const y = yAt((NOW_X + offset) % W);
      lineRef.current?.setAttribute('transform', 'translate(' + -offset.toFixed(2) + ' 0)');
      priceLineRef.current?.setAttribute('y1', y.toFixed(2));
      priceLineRef.current?.setAttribute('y2', y.toFixed(2));
      dotRef.current?.setAttribute('transform', 'translate(' + NOW_X + ' ' + y.toFixed(2) + ')');
      tabRef.current?.setAttribute('transform', 'translate(0 ' + y.toFixed(2) + ')');
      if (withText && tabTextRef.current) tabTextRef.current.textContent = priceFromY(y);
    };

    // A still frame for reduced motion and for the first paint.
    place(96, true);
    if (reduced || !visible) return;

    let frame = 0;
    let lastText = 0;
    const start = performance.now();
    const tick = (now: number) => {
      // Digits change a few times a second, not every frame, so the tab
      // reads as a quote updating rather than a number flickering.
      const withText = now - lastText > 220;
      if (withText) lastText = now;
      place(((now - start) * SPEED + 96) % W, withText);
      frame = requestAnimationFrame(tick);
    };
    frame = requestAnimationFrame(tick);
    return () => cancelAnimationFrame(frame);
  }, [reduced, visible]);

  // The expiry chips step through what is on offer.
  useEffect(() => {
    if (reduced || !visible) return;
    const timer = window.setInterval(() => setActive((i) => (i + 1) % DURATIONS.length), 2600);
    return () => window.clearInterval(timer);
  }, [reduced, visible]);

  return (
    <div ref={rootRef} className="landing-card grid overflow-hidden hover:translate-y-0 lg:grid-cols-[1.1fr_1fr]">
      {/* The scene. */}
      <div className="relative aspect-[460/400] w-full">
        <svg
          viewBox="0 0 460 400"
          preserveAspectRatio="xMidYMid meet"
          className="absolute inset-0 h-full w-full"
          aria-hidden
        >
          <defs>
            <clipPath id="ad-past">
              <rect x="0" y="0" width={NOW_X} height="400" />
            </clipPath>
            {/* History fades out to the left; only the recent part is sharp. */}
            <linearGradient id="ad-fade" x1="0" y1="0" x2="140" y2="0" gradientUnits="userSpaceOnUse">
              <stop offset="0" stopColor="rgb(var(--landing-card))" stopOpacity="1" />
              <stop offset="1" stopColor="rgb(var(--landing-card))" stopOpacity="0" />
            </linearGradient>
          </defs>

          {/* The present and the expiry. */}
          <line x1={NOW_X} y1="0" x2={NOW_X} y2="400" stroke="rgba(255,255,255,0.35)" strokeDasharray="3 4" />
          <line x1={EXPIRY_X} y1="0" x2={EXPIRY_X} y2="400" stroke="rgba(255,255,255,0.35)" />

          {/* The market. */}
          <g clipPath="url(#ad-past)">
            <g ref={lineRef}>
              <path d={LINE_PATH} fill="none" stroke="rgb(var(--up))" strokeWidth="2" strokeLinejoin="round" />
            </g>
          </g>
          <rect x="0" y="0" width="140" height="400" fill="url(#ad-fade)" />

          {/* The level right now, carried across to its tab. */}
          <line
            ref={priceLineRef}
            x1="0"
            x2="394"
            y1="150"
            y2="150"
            stroke="rgba(255,255,255,0.75)"
            strokeWidth="1"
          />
          <g ref={dotRef}>
            <circle r="11" fill="rgba(255,255,255,0.06)" />
            <circle r="4" fill="#fff" />
          </g>
          <g ref={tabRef}>
            <rect x="378" y="-15" width="76" height="30" rx="15" fill="#fff" />
            <text
              ref={tabTextRef}
              x="416"
              y="5.5"
              textAnchor="middle"
              fontSize="15"
              fontWeight="700"
              fill="#0b0c0e"
              className="tnum"
            >
              1917.45
            </text>
          </g>

          {/* An UP order placed a moment ago, now in the money. */}
          <line x1="236" y1="236" x2={EXPIRY_X} y2="236" stroke="rgb(var(--landing-accent))" strokeWidth="2" />
          <polygon
            points={`${EXPIRY_X},229 ${EXPIRY_X - 5},236 ${EXPIRY_X + 5},236`}
            fill="rgb(var(--landing-accent))"
          />
          <circle cx="236" cy="236" r="5" fill="rgb(var(--landing-accent))" />
          <g transform="translate(142 221)">
            <path
              d="M0 15 A15 15 0 0 1 15 0 H78 L88 15 L78 30 H15 A15 15 0 0 1 0 15 Z"
              fill="rgb(var(--landing-accent))"
            />
            <text x="14" y="20" fontSize="14" fontWeight="800" fill="#0b0c0e" className="tnum">
              ₹500
            </text>
            <text x="62" y="20.5" fontSize="15" fontWeight="900" fill="#0b0c0e">
              ↑
            </text>
          </g>
        </svg>

        {/* The ticket, as it would look at this moment. */}
        <div className="absolute inset-x-0 flex flex-col items-center gap-2 px-4" style={{ top: '65%' }}>
          <div className="flex gap-2">
            {DURATIONS.map((label, index) => (
              <span
                key={label}
                className={clsx(
                  'rounded-xl px-4 py-2 text-sm font-semibold transition-colors duration-300',
                  index === active ? 'bg-white/[0.14] text-white' : 'bg-white/[0.05] text-white/55',
                )}
              >
                {label}
              </span>
            ))}
          </div>
          <div className="grid w-full max-w-[280px] grid-cols-2 gap-2">
            <span className="rounded-2xl bg-white/[0.07] px-4 py-2.5 text-center">
              <span className="block text-[11px] text-white/45">Amount</span>
              <span className="block text-base font-bold tnum">₹100</span>
            </span>
            <span className="rounded-2xl bg-white/[0.04] px-4 py-2.5 text-center ring-1 ring-inset ring-white/5">
              <span className="block text-[11px] text-white/35">Duration</span>
              <span className="block text-base font-bold tnum text-white/80">{DURATIONS[active]}</span>
            </span>
          </div>
        </div>
      </div>

      {/* The claim. */}
      <div className="flex items-center justify-center px-8 pb-12 pt-4 text-center lg:py-12">
        <div className="max-w-lg">
          <h2 className="text-3xl font-black leading-tight tracking-tight sm:text-4xl">
            You choose
            <br />
            <span className="lg:whitespace-nowrap">
              the <span style={{ color: 'rgb(var(--landing-accent))' }}>amount and duration</span>
            </span>
            <br />
            of your trade
          </h2>
          <p className="mt-5 text-base leading-relaxed text-white/50 sm:text-lg">
            Open trades from ₹100, with expiries as short as 30 seconds.
          </p>
        </div>
      </div>
    </div>
  );
}
