import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router-dom';
import clsx from 'clsx';
import { ArrowDown, ArrowLeft, ArrowRight, ArrowUp, Clock, ShieldAlert, Sparkles, Wallet } from 'lucide-react';
import { useAccountStore } from '../stores/accountStore';
import { useOnboardingStore } from '../stores/onboardingStore';
import { useDrawPolicy } from '../features/trade/drawRule';

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
  icon: ReactNode;
  title: string;
  body: ReactNode;
}

const STEPS: Step[] = [
  {
    icon: <Sparkles className="h-6 w-6" />,
    title: 'Trade on where a price goes next',
    body: (
      <>
        <p>
          BINARY lets you make short, fixed-time trades on markets such as crypto and currencies. You do not buy
          the asset itself - you decide whether its price will be higher or lower after a time you choose.
        </p>
        <p>You always know, before you trade, how much you could get back and how much you could lose.</p>
      </>
    ),
  },
  {
    icon: <ArrowUp className="h-6 w-6" />,
    title: 'How a fixed-time trade works',
    body: (
      <>
        <ol className="space-y-2">
          <Numbered n={1}>Pick a market.</Numbered>
          <Numbered n={2}>Choose a duration, for example 1 minute.</Numbered>
          <Numbered n={3}>Enter how much you want to invest.</Numbered>
          <Numbered n={4}>
            Press <b className="text-up">UP</b> if you think the price will end higher, or{' '}
            <b className="text-down">DOWN</b> if you think it will end lower.
          </Numbered>
        </ol>
        <p>Your entry price is the platform's price at the moment your trade is accepted.</p>
      </>
    ),
  },
  {
    icon: <Clock className="h-6 w-6" />,
    title: 'Duration and outcome',
    body: (
      <>
        <p>When the duration ends, the closing price is compared with your entry price.</p>
        <div className="grid gap-2 text-sm">
          <Outcome tone="up" label="You were right">
            You get your investment back plus the payout. ₹100 at an 82% payout returns ₹182.
          </Outcome>
          <Outcome tone="down" label="You were wrong">
            You lose the amount you invested - ₹100 in the same example.
          </Outcome>
          <TieOutcome />
        </div>
        <p>You can also sell a running trade early at its live value, minus a small fee.</p>
      </>
    ),
  },
  {
    icon: <ShieldAlert className="h-6 w-6" />,
    title: 'The risk, honestly',
    body: (
      <>
        <p className="font-semibold text-content-100">You can lose the whole amount you invest in any trade.</p>
        <p>
          Short-term prices are hard to predict, and because a win pays back less than double, trading at random
          loses money over time. Many people lose more than they win.
        </p>
        <p>Only trade with money you can afford to lose, and never try to win back a loss by trading more.</p>
      </>
    ),
  },
  {
    icon: <Wallet className="h-6 w-6" />,
    title: 'Start with Demo',
    body: (
      <>
        <p>
          Your demo account holds <b className="text-warn">virtual money</b>. It uses the same prices and the same
          rules as a real trade, so you can learn without risking anything.
        </p>
        <p>
          When you are ready, switch to your Live account from the balance at the top of the screen. You can reset
          the demo balance whenever you like.
        </p>
      </>
    ),
  },
];

export function OnboardingPage() {
  const navigate = useNavigate();
  const complete = useOnboardingStore((s) => s.complete);
  const setActive = useAccountStore((s) => s.setActive);
  const [index, setIndex] = useState(0);
  const headingRef = useRef<HTMLHeadingElement>(null);

  const step = STEPS[index]!;
  const last = index === STEPS.length - 1;

  // Each step is a new screen for a screen reader too.
  useEffect(() => headingRef.current?.focus(), [index]);

  function skip() {
    complete();
    navigate('/home', { replace: true });
  }

  function startDemo() {
    complete();
    setActive('DEMO');
    navigate('/trade', { replace: true });
  }

  return (
    <div className="flex min-h-[100dvh] flex-col bg-surface-950 px-4 pb-[max(1rem,env(safe-area-inset-bottom))] pt-4">
      <div className="mx-auto flex w-full max-w-md flex-1 flex-col">
        <div className="flex items-center justify-between">
          <span className="text-xs font-semibold text-content-400">
            Step {index + 1} of {STEPS.length}
          </span>
          {!last ? (
            <button
              type="button"
              onClick={skip}
              className="rounded-lg px-2 py-1 text-sm font-semibold text-content-300 hover:text-content-100"
            >
              Skip
            </button>
          ) : null}
        </div>

        <div className="mt-3 flex gap-1.5" aria-hidden="true">
          {STEPS.map((_, i) => (
            <span
              key={i}
              className={clsx('h-1 flex-1 rounded-full transition', i <= index ? 'bg-accent' : 'bg-surface-700')}
            />
          ))}
        </div>

        <div className="flex flex-1 flex-col justify-center py-8">
          <span
            className={clsx(
              'grid h-12 w-12 place-items-center rounded-2xl',
              index === 3 ? 'bg-warn/15 text-warn' : 'bg-accent/15 text-accent',
            )}
          >
            {step.icon}
          </span>
          <h1 ref={headingRef} tabIndex={-1} className="mt-5 text-2xl font-bold text-content-100 outline-none">
            {step.title}
          </h1>
          <div className="mt-4 space-y-3 text-[15px] leading-relaxed text-content-300">{step.body}</div>
        </div>

        <div className="flex gap-2">
          {index > 0 ? (
            <button type="button" onClick={() => setIndex(index - 1)} className="btn-ghost px-4 py-3">
              <ArrowLeft className="h-4 w-4" />
              Back
            </button>
          ) : null}
          {last ? (
            <button type="button" onClick={startDemo} className="btn-primary flex-1 py-3 text-base">
              Start Demo
            </button>
          ) : (
            <button type="button" onClick={() => setIndex(index + 1)} className="btn-primary flex-1 py-3 text-base">
              Next
              <ArrowRight className="h-4 w-4" />
            </button>
          )}
        </div>
        {last ? (
          <button
            type="button"
            onClick={skip}
            className="mt-2 py-2 text-center text-sm font-semibold text-content-400 hover:text-content-200"
          >
            Not now - go to Home
          </button>
        ) : null}
      </div>
    </div>
  );
}

function Numbered({ n, children }: { n: number; children: ReactNode }) {
  return (
    <li className="flex gap-3">
      <span className="grid h-6 w-6 shrink-0 place-items-center rounded-full bg-surface-800 text-xs font-bold text-content-200">
        {n}
      </span>
      <span className="pt-0.5">{children}</span>
    </li>
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
  return (
    <div
      className={clsx(
        'rounded-lg border px-3 py-2',
        tone === 'up' ? 'border-up/30 bg-up/10' : tone === 'down' ? 'border-down/30 bg-down/10' : 'border-surface-700 bg-surface-850',
      )}
    >
      <span
        className={clsx(
          'flex items-center gap-1.5 text-xs font-bold',
          tone === 'up' ? 'text-up' : tone === 'down' ? 'text-down' : 'text-content-200',
        )}
      >
        {tone === 'up' ? <ArrowUp className="h-3.5 w-3.5" /> : tone === 'down' ? <ArrowDown className="h-3.5 w-3.5" /> : null}
        {label}
      </span>
      <span className="mt-0.5 block text-content-300">{children}</span>
    </div>
  );
}
