import clsx from 'clsx';
import { ArrowDown, ArrowUp } from 'lucide-react';
import { useSettingsStore } from '../../stores/settingsStore';
import { formatClock, resolveZone } from '../../utils/time';
import { formatSignedMoney } from '../../utils/format';
import { statusTone, tradeLabel } from '../../utils/tradeStatus';
import { accountOf } from './ticketModel';
import { STANDING_WORDS, amountTone, type LiveResult } from './liveResult';
import type { AccountType, Direction, Trade } from '../../types/api';

/**
 * Small pieces every trade screen draws the same way.
 *
 * The brief's alignment rule (section 28) is that direction, status and
 * account read identically from the ticket to the trade detail. Drawing them
 * from one place is what keeps that true after the next edit.
 */

/** Marks anything placed with virtual money. Live needs no tag: it is the default. */
export function DemoTag({ account, className }: { account: AccountType | undefined; className?: string }) {
  if ((account ?? 'LIVE') !== 'DEMO') return null;
  return (
    <span
      className={clsx(
        'inline-flex shrink-0 items-center rounded px-1 py-px text-[9px] font-bold uppercase tracking-wider',
        'bg-accent/15 text-accent ring-1 ring-accent/30',
        className,
      )}
    >
      Demo
    </span>
  );
}

export function TradeDemoTag({ trade, className }: { trade: Pick<Trade, 'accountType'>; className?: string }) {
  return <DemoTag account={accountOf(trade)} className={className} />;
}

/** "▲ UP" / "▼ DOWN": always the word, never the colour or the arrow alone. */
export function DirectionWord({ direction, className }: { direction: Direction; className?: string }) {
  const up = direction === 'UP';
  const Arrow = up ? ArrowUp : ArrowDown;
  return (
    <span className={clsx('inline-flex items-center gap-0.5 font-bold', up ? 'text-up' : 'text-down', className)}>
      <Arrow className="h-3.5 w-3.5 shrink-0" strokeWidth={3} />
      {direction}
    </span>
  );
}

const TONE_CLASS = {
  up: 'bg-up/15 text-up',
  down: 'bg-down/15 text-down',
  live: 'bg-accent/15 text-accent',
  neutral: 'bg-surface-800 text-content-300',
} as const;

/** The trade's status in words, coloured by what it means for the trader. */
export function StatusPill({
  trade,
  className,
}: {
  trade: Pick<Trade, 'status' | 'settlementAmount' | 'voided' | 'expiryPriceSource'>;
  className?: string;
}) {
  return (
    <span
      className={clsx(
        'inline-flex shrink-0 items-center rounded px-1.5 py-0.5 text-[10px] font-bold',
        TONE_CLASS[statusTone(trade)],
        className,
      )}
    >
      {tradeLabel(trade)}
    </span>
  );
}

const AMOUNT_TEXT = { up: 'text-up', down: 'text-down', neutral: 'text-content-100' } as const;
const STRIP_TINT = { up: 'bg-up/10', down: 'bg-down/10', neutral: 'bg-surface-800' } as const;

/**
 * "If it ends now: +₹82.00 · winning" on a running trade (reference record 15).
 *
 * Money, not only a coloured price: "the price is green" leaves the trader to
 * work out what that is worth, and UX-12 asks for the outcome. Worded as a
 * projection because it is one - see `liveResult`. Once the countdown has run
 * out the outcome is being settled on the expiry price, so it stops projecting
 * and says so. `strip` is the tinted row inside a position card; `plain` is
 * the same words for a table cell, where the column heading can carry the
 * label instead (`label={false}`).
 */
export function LiveResultLine({
  result,
  settling,
  currency,
  variant = 'strip',
  label = true,
  className,
}: {
  result: LiveResult | null;
  settling: boolean;
  currency: string;
  variant?: 'strip' | 'plain';
  label?: boolean;
  className?: string;
}) {
  const tone = result && !settling ? amountTone(result.amount) : 'neutral';
  return (
    <p
      className={clsx(
        'text-[11px]',
        variant === 'strip' && ['flex items-baseline justify-between gap-2 rounded-md px-2 py-1', STRIP_TINT[tone]],
        className,
      )}
    >
      {label ? (
        <>
          <span className="shrink-0 text-content-300">{settling ? 'Result' : 'If it ends now:'}</span>{' '}
        </>
      ) : null}
      {settling ? (
        <span className="text-content-400">settling at the expiry price…</span>
      ) : result ? (
        <span className={clsx('min-w-0 font-bold tnum', AMOUNT_TEXT[tone])}>
          {formatSignedMoney(result.amount, currency)}
          <span className="font-semibold"> · {STANDING_WORDS[result.standing]}</span>
        </span>
      ) : (
        <span className="text-content-400">waiting for a price</span>
      )}
    </p>
  );
}

/**
 * A clock time in the zone the trader chose for the chart.
 *
 * Expiry is shown against the chart's axis, so it has to be written in the
 * same zone - an expiry of 12:30 beside an axis in another zone is a trap.
 */
export function useClockFormatter(): (ms: number) => string {
  const zone = useTimeZone();
  return (ms: number) => formatClock(ms, zone);
}

/**
 * The zone clock times are written in, as an IANA name.
 *
 * Also the zone days are counted in, so a trade listed under "Today" is one
 * whose times read as today.
 */
export function useTimeZone(): string {
  return resolveZone(useSettingsStore((s) => s.timezone));
}
