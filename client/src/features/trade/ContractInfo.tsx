import clsx from 'clsx';
import { useNow } from '../../hooks/useCountdown';
import { formatMoney } from '../../utils/format';
import { InfoTip } from '../../components/InfoTip';
import { contractTerms, durationLabel, type ContractTerms } from './ticketModel';
import { DirectionWord, useClockFormatter } from './tradeBits';
import { TIE_CLAUSE, useDrawPolicy } from './drawRule';
import type { AccountType, Direction, DrawPolicy } from '../../types/api';

/**
 * Contract information (brief UX-09).
 *
 * Every term of the trade, together, before anything is sent: what is traded,
 * which way, for how much, until when, what it pays and what it can cost. It
 * is not behind a tap - a term the trader has to go looking for is a term they
 * did not agree to - and it is the same component inside the confirmation, so
 * the two can never describe different trades.
 *
 * The terms that need explaining carry a "?" (reference record 12): payout,
 * profit, total return, maximum loss, fees and the expiry time. The
 * explanations are one or two plain sentences that describe what this
 * platform does - where a number is known they use it, so "profit" is shown
 * as the sum that produced it rather than as a definition.
 *
 * "Expires at" is computed from the clock every second: the trade has not been
 * placed, so its expiry is always "the duration from now", and a frozen time
 * would be wrong by the time the trader reads it.
 */
export interface ContractSummary {
  symbol: string | null;
  /** Null until the trader has chosen. */
  direction: Direction | null;
  /** Minor units; null when the amount field does not hold a valid amount. */
  stake: number | null;
  currency: string;
  expirySeconds: number | null;
  payoutPercent: number | null;
  account: AccountType;
}

export const FEE_NOTE = 'No fee to open. Selling before expiry is priced live and includes a fee.';

/**
 * What each "?" says. Kept together so the wording can be checked against the
 * rules in one place. The two that depend on what a tie settles as take the
 * platform's draw policy.
 */
export const TERM_HELP = {
  expiresAt: (drawPolicy: DrawPolicy) =>
    'The trade ends at this time, counted from the moment the platform accepts it. The last price at or before it is compared with your entry price; if it is exactly the same, ' +
    TIE_CLAUSE[drawPolicy] +
    '.',
  payout:
    'The share of your investment you earn as profit if the trade wins. It is fixed when your trade is accepted, even if the payout shown here changes afterwards.',
  totalReturn: 'What comes back to your balance if the trade wins: your investment plus the profit.',
  maxLoss: (drawPolicy: DrawPolicy) =>
    'The most this trade can cost you: the whole investment, if the price ends on the wrong side of your entry price' +
    (drawPolicy === 'LOSS' ? ' or exactly at it' : '') +
    '. Never more than you invest.',
  fees: 'Opening a trade is free: all of your investment goes into it. If you sell before expiry, the price offered is worked out live from the current price and the time left, and it includes a fee.',
} as const;

/** "Investment × payout: ₹500.00 × 83% = ₹415.00" when both are known. */
function profitHelp(stake: number | null, payoutPercent: number | null, terms: ContractTerms | null, currency: string) {
  const rule = 'Your investment × the payout. It is what you gain on top of your investment if the trade wins.';
  if (!terms || stake === null || payoutPercent === null) return rule;
  return (
    <>
      {rule}{' '}
      <span className="tnum font-semibold text-content-100">
        {formatMoney(stake, currency)} × {payoutPercent}% = {formatMoney(terms.profit, currency)}
      </span>
    </>
  );
}

export function ContractInfo({
  summary,
  variant = 'ticket',
}: {
  summary: ContractSummary;
  variant?: 'ticket' | 'confirm';
}) {
  const now = useNow(1000);
  const clock = useClockFormatter();
  const drawPolicy = useDrawPolicy();
  const terms = contractTerms(summary.stake, summary.payoutPercent);
  const money = (minor: number) => formatMoney(minor, summary.currency);
  const dash = <span className="text-content-400">--</span>;

  return (
    <section
      aria-label="Contract information"
      className={clsx(
        'rounded-xl bg-surface-850',
        variant === 'confirm' ? 'px-4 py-3' : 'px-3 py-2.5',
      )}
    >
      {variant === 'ticket' ? (
        <h3 className="mb-1.5 text-[11px] font-semibold uppercase tracking-wider text-content-400">
          Contract information
        </h3>
      ) : null}

      <dl className={clsx('grid gap-y-1', variant === 'confirm' ? 'text-[13px]' : 'text-[12px]')}>
        <Row label="Asset">{summary.symbol ?? dash}</Row>
        <Row label="Direction">
          {summary.direction ? (
            <DirectionWord direction={summary.direction} />
          ) : (
            <span className="text-content-300">UP or DOWN — you choose next</span>
          )}
        </Row>
        <Row label="Investment">{summary.stake !== null ? money(summary.stake) : dash}</Row>
        <Row label="Duration">{summary.expirySeconds !== null ? durationLabel(summary.expirySeconds) : dash}</Row>
        <Row label="Expires at" help={TERM_HELP.expiresAt(drawPolicy)}>
          {summary.expirySeconds !== null ? (
            <span className="tnum">{clock(now + summary.expirySeconds * 1000)}</span>
          ) : (
            dash
          )}
        </Row>
        <Row label="Payout" help={TERM_HELP.payout}>
          {summary.payoutPercent !== null ? summary.payoutPercent + '%' : dash}
        </Row>
        <Row
          label="Profit if it wins"
          help={profitHelp(summary.stake, summary.payoutPercent, terms, summary.currency)}
        >
          {terms ? <span className="text-up">+{money(terms.profit)}</span> : dash}
        </Row>
        <Row label="Total return" help={TERM_HELP.totalReturn}>
          {terms ? money(terms.totalReturn) : dash}
        </Row>
        <Row label="Maximum loss" help={TERM_HELP.maxLoss(drawPolicy)}>
          {terms ? money(terms.maxLoss) : dash}
        </Row>
        <Row label="Account">
          {summary.account === 'DEMO' ? (
            <span className="text-accent">Demo (virtual money)</span>
          ) : (
            'Live (real money)'
          )}
        </Row>
      </dl>

      <p className="mt-2 border-t border-surface-700/60 pt-1.5 text-[11px] leading-snug text-content-400">
        <span className="inline-flex items-center font-semibold text-content-300">
          Fees
          <InfoTip label="Fees">{TERM_HELP.fees}</InfoTip>
        </span>{' '}
        {FEE_NOTE}
      </p>
    </section>
  );
}

function Row({ label, help, children }: { label: string; help?: React.ReactNode; children: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="flex shrink-0 items-center text-content-400">
        {label}
        {help ? <InfoTip label={label}>{help}</InfoTip> : null}
      </dt>
      <dd className="min-w-0 text-right font-semibold tnum text-content-100">{children}</dd>
    </div>
  );
}
