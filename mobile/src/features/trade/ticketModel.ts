import { formatExpiry, formatMoney, toMinor } from '../../utils/format';
import type { AccountType, Asset, PayoutConfig, Trade } from '../../types/api';

/**
 * The trade ticket's rules, without React.
 *
 * Everything here decides what the ticket lets the trader do - which durations
 * can be picked, which amounts are offered, and what is wrong with the one
 * typed in - so it is kept pure and tested on its own. The component only
 * renders the answers.
 */

/* ----------------------------------------------------------------- duration */

export interface DurationChip {
  seconds: number;
  label: string;
  /** Null when no payout is configured for this duration. */
  payoutPercent: number | null;
  /** Why the chip cannot be picked, or null when it can. */
  unavailableReason: string | null;
}

/**
 * "30s", "1m", "15m", "1h", "1h 30m": how a duration is written on the ticket.
 *
 * The same as `formatExpiry` for every duration offered today; it differs only
 * past an hour, where a chip reading "1.0h" would look like a measurement
 * rather than a choice.
 */
export function durationLabel(seconds: number): string {
  if (seconds < 3600) return formatExpiry(seconds);
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.round((seconds % 3600) / 60);
  return minutes ? hours + 'h ' + minutes + 'm' : hours + 'h';
}

/**
 * One chip per duration the asset supports, shortest first.
 *
 * A duration without a payout stays visible but disabled, with its reason:
 * hiding it would make the set of durations change from asset to asset for
 * no reason the trader can see.
 */
export function durationChips(asset: Pick<Asset, 'supportedExpiries' | 'payouts'> | null): DurationChip[] {
  if (!asset) return [];
  return [...asset.supportedExpiries]
    .sort((a, b) => a - b)
    .map((seconds) => {
      const payout = asset.payouts.find((entry) => entry.expirySeconds === seconds) ?? null;
      return {
        seconds,
        label: durationLabel(seconds),
        payoutPercent: payout ? payout.payoutPercent : null,
        unavailableReason: payout ? null : 'No payout for ' + durationLabel(seconds),
      };
    });
}

/** The duration to use: the wanted one if it can be traded, else the first that can. */
export function pickDuration(chips: DurationChip[], wanted: number): number | null {
  const usable = chips.filter((chip) => chip.unavailableReason === null);
  if (usable.some((chip) => chip.seconds === wanted)) return wanted;
  return usable[0]?.seconds ?? chips[0]?.seconds ?? null;
}

/** How many duration chips the ticket's row shows before "More". */
export const MAX_DURATION_CHIPS = 4;

export interface DurationRow {
  /** The chips on the row, shortest first. Always holds the selected duration. */
  chips: DurationChip[];
  /** Durations reachable only through "More"; zero means the row has no More chip. */
  hidden: number;
}

/**
 * The durations the ticket's row shows directly (reference record 10).
 *
 * Up to four, shortest first - the brief's "1m · 5m · 15m · 30m · More". When
 * an asset offers more, the rest wait behind "More", which lists every one of
 * them. The selected duration is always on the row, taking the last place when
 * it is one of the longer ones: a choice the trader cannot see is a choice they
 * will not notice is wrong.
 */
export function durationRow(chips: DurationChip[], selected: number, max = MAX_DURATION_CHIPS): DurationRow {
  if (chips.length <= max) return { chips, hidden: 0 };
  const first = chips.slice(0, max);
  const selectedChip = chips.find((chip) => chip.seconds === selected);
  const shown =
    !selectedChip || first.includes(selectedChip) ? first : [...chips.slice(0, max - 1), selectedChip];
  return { chips: shown, hidden: chips.length - shown.length };
}

/**
 * "30 sec", "1 min", "1 min 30 sec", "1 hour", "1 hour 30 min": a duration in
 * words, for the small line under each choice in the Time sheet, where the big
 * line is the clock time the trade would end at.
 */
export function durationWords(seconds: number): string {
  if (seconds < 60) return seconds + ' sec';
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60);
    const rest = seconds % 60;
    return rest ? minutes + ' min ' + rest + ' sec' : minutes + ' min';
  }
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.round((seconds % 3600) / 60);
  return hours + (hours === 1 ? ' hour' : ' hours') + (minutes ? ' ' + minutes + ' min' : '');
}

export interface ExpiryGroup {
  key: 'short' | 'medium' | 'long';
  label: string;
  /** Shortest first, as `durationChips` gives them. */
  chips: DurationChip[];
}

/**
 * The Time sheet's sections: up to five minutes, up to an hour, and longer.
 *
 * Every chip lands in exactly one section, so no duration the asset offers can
 * fall out of the sheet. The middle section is named for what it holds - "15–60
 * min." when its shortest choice is 15 minutes, as on the platforms traders
 * know, and "5–60 min." when the asset offers something in between. Empty
 * sections are left out.
 */
export function expiryGroups(chips: DurationChip[]): ExpiryGroup[] {
  const short = chips.filter((chip) => chip.seconds <= 300);
  const medium = chips.filter((chip) => chip.seconds > 300 && chip.seconds <= 3600);
  const long = chips.filter((chip) => chip.seconds > 3600);
  const groups: ExpiryGroup[] = [
    { key: 'short', label: 'Up to 5 min.', chips: short },
    {
      key: 'medium',
      label: medium.every((chip) => chip.seconds >= 900) ? '15–60 min.' : '5–60 min.',
      chips: medium,
    },
    { key: 'long', label: 'Over 1 hour', chips: long },
  ];
  return groups.filter((group) => group.chips.length > 0);
}

/* -------------------------------------------------------------- stake limits */

export interface StakeLimits {
  min: number;
  max: number;
}

/** The tighter of the asset's and the payout's limits, in minor units. */
export function stakeLimits(
  asset: Pick<Asset, 'minStake' | 'maxStake'> | null,
  payout: Pick<PayoutConfig, 'minStake' | 'maxStake'> | null,
): StakeLimits {
  return {
    min: Math.max(asset?.minStake ?? 0, payout?.minStake ?? 0),
    max: Math.min(asset?.maxStake ?? Number.MAX_SAFE_INTEGER, payout?.maxStake ?? Number.MAX_SAFE_INTEGER),
  };
}

/* ---------------------------------------------------------------- amount */

/** Offered amounts, in major units, before the limits are applied. */
export const PRESET_AMOUNTS_MAJOR = [100, 500, 1000, 5000] as const;

export interface AmountPreset {
  /** Minor units. */
  value: number;
  /** "Max" rather than a figure. */
  isMax: boolean;
}

/**
 * The preset chips that can actually be traded on this asset and duration.
 *
 * A chip the server would refuse is left out rather than shown disabled - a
 * preset is a shortcut, and a shortcut to an error is not one. "Max" is the
 * most the trader can put on right now: the smaller of the balance and the
 * maximum stake, and only when that clears the minimum.
 */
export function amountPresets(limits: StakeLimits, available: number | null): AmountPreset[] {
  const presets: AmountPreset[] = PRESET_AMOUNTS_MAJOR.map((major) => toMinor(major))
    .filter((value) => value >= limits.min && value <= limits.max)
    .map((value) => ({ value, isMax: false }));

  if (available !== null) {
    const max = Math.min(available, limits.max);
    if (max >= limits.min && max > 0 && !presets.some((preset) => preset.value === max)) {
      presets.push({ value: max, isMax: true });
    }
  }
  return presets;
}

export type AmountState =
  | { kind: 'empty' }
  | { kind: 'invalid' }
  | { kind: 'below-min'; min: number }
  | { kind: 'above-max'; max: number }
  | { kind: 'insufficient'; available: number }
  | { kind: 'valid'; value: number };

/**
 * Reads what is in the amount field.
 *
 * Returns minor units, or null for text that is not an amount of money: more
 * than two decimals, a second point, letters. Grouping commas are accepted,
 * because that is how the presets and the balance are written back.
 */
export function parseAmount(text: string): number | null {
  const cleaned = text.replace(/,/g, '').trim();
  if (cleaned === '') return null;
  if (!/^\d*(\.\d{0,2})?$/.test(cleaned) || cleaned === '.') return null;
  const value = Number(cleaned);
  return Number.isFinite(value) ? toMinor(value) : null;
}

/**
 * The field-level state of the amount.
 *
 * Checked in the order a trader can fix them: say what to type, then whether
 * it is a number, then the limits, and only then the balance - telling someone
 * they cannot afford an amount the platform would refuse anyway sends them to
 * the deposit screen for nothing.
 */
export function amountState(text: string, limits: StakeLimits, available: number | null): AmountState {
  if (text.trim() === '') return { kind: 'empty' };
  const value = parseAmount(text);
  if (value === null || value <= 0) return { kind: 'invalid' };
  if (value < limits.min) return { kind: 'below-min', min: limits.min };
  if (value > limits.max) return { kind: 'above-max', max: limits.max };
  if (available !== null && value > available) return { kind: 'insufficient', available };
  return { kind: 'valid', value };
}

/** What the field says under itself, or null when the amount is fine. */
export function amountMessage(state: AmountState, currency = 'INR'): string | null {
  switch (state.kind) {
    case 'empty':
      return 'Enter an amount';
    case 'invalid':
      return 'Enter a valid amount, e.g. 500 or 250.50';
    case 'below-min':
      return 'Minimum investment is ' + formatMoney(state.min, currency);
    case 'above-max':
      return 'Maximum investment is ' + formatMoney(state.max, currency);
    case 'insufficient':
      return 'Not enough balance. Available: ' + formatMoney(state.available, currency);
    case 'valid':
      return null;
  }
}

/** Nudges the amount by one step, clamped to the limits. */
export function stepAmount(current: number | null, delta: number, limits: StakeLimits): number {
  const base = current ?? limits.min;
  const next = Math.max(limits.min, Math.min(limits.max, base + delta));
  return Math.max(next, 0);
}

/** How much a stepper press moves the amount: bigger steps for bigger amounts. */
export function amountStep(current: number | null): number {
  const value = current ?? 0;
  if (value >= toMinor(10_000)) return toMinor(1000);
  if (value >= toMinor(1000)) return toMinor(500);
  return toMinor(100);
}

/** The amount written back into the field: no trailing ".00". */
export function amountText(minor: number): string {
  const major = minor / 100;
  return Number.isInteger(major) ? String(major) : major.toFixed(2);
}

/* ---------------------------------------------------------------- contract */

export interface ContractTerms {
  profit: number;
  totalReturn: number;
  maxLoss: number;
}

/** What the trade pays: profit on a win, the return including the stake, and the most it can lose. */
export function contractTerms(stake: number | null, payoutPercent: number | null): ContractTerms | null {
  if (stake === null || payoutPercent === null) return null;
  const profit = Math.round((stake * payoutPercent) / 100);
  return { profit, totalReturn: stake + profit, maxLoss: stake };
}

/* ---------------------------------------------------------------- accounts */

/** The account a trade was placed on. Rows written before the split carry none and are live. */
export function accountOf(trade: Pick<Trade, 'accountType'>): AccountType {
  return trade.accountType ?? 'LIVE';
}
