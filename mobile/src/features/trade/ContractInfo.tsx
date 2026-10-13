import { useState, type ReactNode } from 'react';
import { Pressable, View } from 'react-native';
import { ChevronDown, ChevronUp } from 'lucide-react-native';
import { useNow } from '../../hooks/useCountdown';
import { formatMoney } from '../../utils/format';
import { cn } from '../../utils/cn';
import { InfoTip } from '../../components/InfoTip';
import { Icon, Text } from '../../components/ui';
import { contractTerms, durationLabel, type ContractTerms } from './ticketModel';
import { DirectionWord, useClockFormatter } from './tradeBits';
import { TIE_CLAUSE, useDrawPolicy } from './drawRule';
import type { AccountType, Direction, DrawPolicy } from '../../types/api';

/**
 * Contract information (brief UX-09).
 *
 * Every term of the trade, together, before anything is sent: what is traded,
 * which way, for how much, until when, what it pays and what it can cost. It
 * is the same component inside the confirmation, so the two can never
 * describe different trades.
 *
 * On a phone the ticket is docked under the chart and has to stay short, so
 * the ticket's copy opens on the terms that carry money and time - when it
 * expires, what it pays, what it can win and what it can lose - with every
 * other term one tap away ("All terms"), in place. The confirmation always
 * shows all of them: that is the last look before money moves, and nothing
 * there is behind a tap.
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
function profitHelp(
  stake: number | null,
  payoutPercent: number | null,
  terms: ContractTerms | null,
  currency: string,
): ReactNode {
  const rule = 'Your investment × the payout. It is what you gain on top of your investment if the trade wins.';
  if (!terms || stake === null || payoutPercent === null) return rule;
  return (
    <Text className="text-[12px] leading-4 text-content-200">
      {rule}{' '}
      <Text className="text-[12px] font-semibold tnum leading-4 text-content-100">
        {formatMoney(stake, currency)} × {payoutPercent}% = {formatMoney(terms.profit, currency)}
      </Text>
    </Text>
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
  const [allTerms, setAllTerms] = useState(false);
  const terms = contractTerms(summary.stake, summary.payoutPercent);
  const money = (minor: number) => formatMoney(minor, summary.currency);
  const size = variant === 'confirm' ? 'text-[13px]' : 'text-[12px]';
  const dash = <Text className={cn(size, 'font-semibold text-content-400')}>--</Text>;
  const value = (text: string, tone = 'text-content-100') => (
    <Text className={cn(size, 'text-right font-semibold tnum', tone)}>{text}</Text>
  );

  const expiresAt = summary.expirySeconds !== null ? value(clock(now + summary.expirySeconds * 1000)) : dash;
  const payout = summary.payoutPercent !== null ? value(summary.payoutPercent + '%') : dash;
  const profit = terms ? value('+' + money(terms.profit), 'text-up') : dash;
  const maxLoss = terms ? value(money(terms.maxLoss)) : dash;

  // The ticket's short form: the four terms that carry time and money.
  if (variant === 'ticket' && !allTerms) {
    return (
      <View accessibilityLabel="Contract information" className="rounded-xl bg-surface-850 px-3 py-2">
        <Header expanded={false} onToggle={() => setAllTerms(true)} />
        <View className="flex-row flex-wrap gap-y-1">
          <Cell label="Expires at">{expiresAt}</Cell>
          <Cell label="Payout" right>
            {payout}
          </Cell>
          <Cell label="Profit if it wins">{profit}</Cell>
          <Cell label="Maximum loss" right>
            {maxLoss}
          </Cell>
        </View>
      </View>
    );
  }

  return (
    <View
      accessibilityLabel="Contract information"
      className={cn('rounded-xl bg-surface-850', variant === 'confirm' ? 'px-4 py-3' : 'px-3 py-2.5')}
    >
      {variant === 'ticket' ? <Header expanded onToggle={() => setAllTerms(false)} /> : null}

      <View className="gap-y-1">
        <Row size={size} label="Asset">
          {summary.symbol ? value(summary.symbol) : dash}
        </Row>
        <Row size={size} label="Direction">
          {summary.direction ? (
            <DirectionWord direction={summary.direction} className={size} />
          ) : (
            <Text className={cn(size, 'text-right font-semibold text-content-300')}>UP or DOWN — you choose next</Text>
          )}
        </Row>
        <Row size={size} label="Investment">
          {summary.stake !== null ? value(money(summary.stake)) : dash}
        </Row>
        <Row size={size} label="Duration">
          {summary.expirySeconds !== null ? value(durationLabel(summary.expirySeconds)) : dash}
        </Row>
        <Row size={size} label="Expires at" help={TERM_HELP.expiresAt(drawPolicy)}>
          {expiresAt}
        </Row>
        <Row size={size} label="Payout" help={TERM_HELP.payout}>
          {payout}
        </Row>
        <Row
          size={size}
          label="Profit if it wins"
          help={profitHelp(summary.stake, summary.payoutPercent, terms, summary.currency)}
        >
          {profit}
        </Row>
        <Row size={size} label="Total return" help={TERM_HELP.totalReturn}>
          {terms ? value(money(terms.totalReturn)) : dash}
        </Row>
        <Row size={size} label="Maximum loss" help={TERM_HELP.maxLoss(drawPolicy)}>
          {maxLoss}
        </Row>
        <Row size={size} label="Account">
          {summary.account === 'DEMO' ? value('Demo (virtual money)', 'text-accent') : value('Live (real money)')}
        </Row>
      </View>

      <View className="mt-2 flex-row items-start gap-1 border-t border-surface-700/60 pt-1.5">
        <View className="flex-row items-center">
          <Text className="text-[11px] font-semibold leading-snug text-content-300">Fees</Text>
          <InfoTip label="Fees">{TERM_HELP.fees}</InfoTip>
        </View>
        <Text className="flex-1 text-[11px] leading-snug text-content-400">{FEE_NOTE}</Text>
      </View>
    </View>
  );
}

/** The ticket's heading, with the switch between the short and the full terms. */
function Header({ expanded, onToggle }: { expanded: boolean; onToggle: () => void }) {
  return (
    <View className="mb-1.5 flex-row items-center justify-between gap-2">
      <Text accessibilityRole="header" className="text-[11px] font-semibold uppercase tracking-wider text-content-400">
        Contract information
      </Text>
      <Pressable
        onPress={onToggle}
        hitSlop={8}
        accessibilityRole="button"
        accessibilityState={{ expanded }}
        className="-my-1 flex-row items-center gap-0.5 py-1"
      >
        <Text className="text-[11px] font-semibold text-accent">{expanded ? 'Fewer terms' : 'All terms'}</Text>
        <Icon as={expanded ? ChevronUp : ChevronDown} size={14} className="text-accent" />
      </Pressable>
    </View>
  );
}

/** Half a line of the short form: the label, then its value. */
function Cell({ label, right, children }: { label: string; right?: boolean; children: ReactNode }) {
  return (
    <View className={cn('w-1/2 flex-row items-baseline justify-between gap-2', right ? 'pl-2' : 'pr-2')}>
      <Text numberOfLines={1} className="shrink text-[11px] text-content-400">
        {label}
      </Text>
      {children}
    </View>
  );
}

function Row({
  label,
  help,
  size,
  children,
}: {
  label: string;
  help?: ReactNode;
  size: string;
  children: ReactNode;
}) {
  return (
    <View className="flex-row items-center justify-between gap-3">
      <View className="shrink-0 flex-row items-center">
        <Text className={cn(size, 'text-content-400')}>{label}</Text>
        {help ? <InfoTip label={label}>{help}</InfoTip> : null}
      </View>
      <View className="min-w-0 shrink flex-row justify-end">{children}</View>
    </View>
  );
}
