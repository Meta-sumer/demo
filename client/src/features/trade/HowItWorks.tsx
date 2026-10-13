import { useId } from 'react';
import { Link } from 'react-router-dom';
import { ArrowRight } from 'lucide-react';
import { Sheet } from '../../components/Sheet';
import { formatMoney, formatSignedMoney, toMinor } from '../../utils/format';
import { contractTerms } from './ticketModel';
import { TIE_CLAUSE, TIE_LABEL, TIE_SENTENCE, useDrawPolicy } from './drawRule';
import type { DrawPolicy } from '../../types/api';

/**
 * "How a trade works", opened from the ticket (brief UX-18, reference record
 * 19 - Deriv's "How to trade Rise/Fall?" link at the top of its trade panel).
 *
 * The explanation has to be reachable at the moment the question comes up,
 * which is on the trade screen, without taking the trade screen away. So on a
 * wide screen it is a panel down the left that leaves the chart and the ticket
 * usable, like Olymptrade's education panel, and on a phone a bottom sheet.
 *
 * One picture and four facts, all matching how the platform actually settles
 * a trade; Learn keeps the full guide. The example uses the trader's own amount
 * and payout when the ticket holds a valid one, so the numbers they read here
 * are the numbers they are about to agree to.
 */

interface Example {
  stake: number | null;
  payoutPercent: number | null;
  currency: string;
}

/** The illustration used when the ticket has nothing valid to show. */
const FALLBACK = { stake: toMinor(100), payoutPercent: 82 };

export function HowItWorksSheet({ open, onClose, example }: { open: boolean; onClose: () => void; example: Example }) {
  const drawPolicy = useDrawPolicy();
  return (
    <Sheet
      open={open}
      onClose={onClose}
      title="How a trade works"
      subtitle="Fixed-time trading in one picture and four facts."
      variant="side"
    >
      <div className="space-y-4 px-4 pb-5 text-sm leading-relaxed text-content-300">
        <p>
          You predict whether the price will be <b className="text-up">higher</b> or{' '}
          <b className="text-down">lower</b> than your entry price when the trade ends.
        </p>

        <TradeDiagram drawPolicy={drawPolicy} />

        <ol className="space-y-2.5">
          <Fact n={1} title="Your entry price">
            It is the platform&apos;s price at the moment your trade is accepted. If the price moves too far while your
            order is on its way, nothing is placed and you are shown the new price instead.
          </Fact>
          <Fact n={2} title="UP or DOWN">
            <b className="text-up">UP</b> wins if the price at expiry is above your entry price.{' '}
            <b className="text-down">DOWN</b> wins if it is below.
          </Fact>
          <Fact n={3} title="The expiry decides">
            The trade ends at the expiry time shown before you confirm. The last price at or before that moment is
            compared with your entry price. {TIE_SENTENCE[drawPolicy]}
          </Fact>
          <Fact n={4} title="What a win pays, what a loss costs">
            A win returns your investment plus the profit: investment × payout, fixed when the trade is accepted. A
            loss costs the investment, never more.
          </Fact>
        </ol>

        <Outcomes example={example} drawPolicy={drawPolicy} />

        <p className="text-xs text-content-400">
          You can also sell a running trade before expiry. That price is worked out live and includes a fee.
        </p>

        <Link
          to="/learn#how-it-works"
          onClick={onClose}
          className="inline-flex items-center gap-1.5 text-sm font-semibold text-accent hover:underline"
        >
          Read the full guide in Learn
          <ArrowRight className="h-4 w-4" />
        </Link>
      </div>
    </Sheet>
  );
}

function Fact({ n, title, children }: { n: number; title: string; children: React.ReactNode }) {
  return (
    <li className="flex gap-3">
      <span
        className="grid h-6 w-6 shrink-0 place-items-center rounded-full bg-surface-800 text-xs font-bold tnum text-content-100"
        aria-hidden
      >
        {n}
      </span>
      <span className="min-w-0">
        <span className="block font-semibold text-content-100">{title}</span>
        {children}
      </span>
    </li>
  );
}

/** The three ways a trade can end, in money, for the ticket's own terms. */
function Outcomes({ example, drawPolicy }: { example: Example; drawPolicy: DrawPolicy }) {
  const own = contractTerms(example.stake, example.payoutPercent);
  const stake = own ? example.stake! : FALLBACK.stake;
  const payout = own ? example.payoutPercent! : FALLBACK.payoutPercent;
  const terms = own ?? contractTerms(FALLBACK.stake, FALLBACK.payoutPercent)!;
  const currency = own ? example.currency : 'INR';
  const money = (minor: number) => formatMoney(minor, currency);

  return (
    <section aria-label="What your trade can pay" className="rounded-xl bg-surface-850 px-3 py-2.5">
      <p className="text-xs text-content-400">
        {own ? 'With your ' : 'For example, '}
        <b className="tnum text-content-200">{money(stake)}</b> at a <b className="tnum text-content-200">{payout}%</b>{' '}
        payout:
      </p>
      <dl className="mt-1.5 grid gap-1 text-[13px]">
        <Row label="You are right" tone="text-up">
          {formatSignedMoney(terms.profit, currency)}{' '}
          <span className="font-normal text-content-400">({money(terms.totalReturn)} back)</span>
        </Row>
        <Row label="You are wrong" tone="text-down">
          {formatSignedMoney(-terms.maxLoss, currency)}
        </Row>
        {drawPolicy === 'LOSS' ? (
          <Row label="Same price" tone="text-down">
            {formatSignedMoney(-terms.maxLoss, currency)} <span className="font-normal text-content-400">(a loss)</span>
          </Row>
        ) : drawPolicy === 'WIN' ? (
          <Row label="Same price" tone="text-up">
            {formatSignedMoney(terms.profit, currency)} <span className="font-normal text-content-400">(a win)</span>
          </Row>
        ) : (
          <Row label="Same price" tone="text-content-100">
            {money(stake)} <span className="font-normal text-content-400">back (draw)</span>
          </Row>
        )}
      </dl>
    </section>
  );
}

function Row({ label, tone, children }: { label: string; tone: string; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="text-content-400">{label}</dt>
      <dd className={'text-right font-semibold tnum ' + tone}>{children}</dd>
    </div>
  );
}

/**
 * Entry, expiry and the two zones between them.
 *
 * Drawn from theme tokens, so it reads the same in all three themes. The price
 * path stops at the entry: what happens after it is the trader's call, and a
 * drawn continuation would look like a forecast.
 */
function TradeDiagram({ drawPolicy }: { drawPolicy: DrawPolicy }) {
  const titleId = useId();
  const descId = useId();
  const entryX = 104;
  const expiryX = 284;
  const entryY = 80;
  const top = 14;
  const bottom = 146;
  const middle = (entryX + expiryX) / 2;

  return (
    <figure className="rounded-xl border border-surface-700 bg-surface-950/40 p-2">
      <svg viewBox="0 0 320 178" className="h-auto w-full" role="img" aria-labelledby={titleId + ' ' + descId}>
        <title id={titleId}>How a trade is decided</title>
        <desc id={descId}>
          The trade starts at your entry price and runs until expiry. If the price at expiry is above the entry price,
          UP wins. If it is below, DOWN wins. If it is exactly the same, {TIE_CLAUSE[drawPolicy]}.
        </desc>

        {/* The two outcomes, between entry and expiry. */}
        <rect x={entryX} y={top} width={expiryX - entryX} height={entryY - top} className="fill-up/10" />
        <rect x={entryX} y={entryY} width={expiryX - entryX} height={bottom - entryY} className="fill-down/10" />
        <text x={middle} y={42} textAnchor="middle" className="fill-up text-[12px] font-bold">
          UP wins
        </text>
        <text x={middle} y={56} textAnchor="middle" className="fill-content-400 text-[9.5px]">
          price ends above the entry
        </text>
        <text x={middle} y={112} textAnchor="middle" className="fill-down text-[12px] font-bold">
          DOWN wins
        </text>
        <text x={middle} y={126} textAnchor="middle" className="fill-content-400 text-[9.5px]">
          price ends below the entry
        </text>

        {/* The market before the trade. */}
        <polyline
          points={`10,104 22,94 34,99 46,82 58,90 70,72 82,86 94,76 ${entryX},${entryY}`}
          fill="none"
          strokeWidth={2}
          strokeLinejoin="round"
          strokeLinecap="round"
          className="stroke-content-300"
        />

        {/* Entry price, carried to expiry. */}
        <line
          x1={entryX}
          y1={entryY}
          x2={expiryX}
          y2={entryY}
          strokeWidth={1.5}
          strokeDasharray="5 4"
          className="stroke-content-200"
        />
        <text x={entryX + 6} y={entryY - 5} className="fill-content-200 text-[9.5px] font-semibold">
          Entry price
        </text>
        <text x={expiryX - 5} y={entryY - 5} textAnchor="end" className="fill-content-400 text-[9px]">
          same price = {TIE_LABEL[drawPolicy]}
        </text>
        <circle cx={entryX} cy={entryY} r={4.5} className="fill-accent stroke-surface-900" strokeWidth={2} />

        {/* Expiry. */}
        <line x1={expiryX} y1={top} x2={expiryX} y2={bottom} strokeWidth={1.5} className="stroke-content-400" />
        <path d={`M${expiryX} ${top} h14 l-4 5 l4 5 h-14 z`} className="fill-content-400" />

        {/* Time. */}
        <line x1={6} y1={bottom + 6} x2={314} y2={bottom + 6} strokeWidth={1} className="stroke-surface-600" />
        <line x1={entryX} y1={bottom + 3} x2={entryX} y2={bottom + 9} strokeWidth={1} className="stroke-content-400" />
        <line x1={expiryX} y1={bottom + 3} x2={expiryX} y2={bottom + 9} strokeWidth={1} className="stroke-content-400" />
        <text x={entryX} y={bottom + 22} textAnchor="middle" className="fill-content-300 text-[10px] font-semibold">
          Entry
        </text>
        <text x={middle} y={bottom + 22} textAnchor="middle" className="fill-content-400 text-[9.5px]">
          ← duration →
        </text>
        <text x={expiryX} y={bottom + 22} textAnchor="middle" className="fill-content-300 text-[10px] font-semibold">
          Expiry
        </text>
      </svg>
    </figure>
  );
}
