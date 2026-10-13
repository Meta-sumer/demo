/**
 * USDT amounts.
 *
 * USDT crosses the wire as an integer number of micro-USDT (1 USDT =
 * 1_000_000), the same way INR crosses it in paise. Unlike `toMinor` these
 * helpers never multiply or divide a float: an amount is parsed and printed by
 * moving digits around a string, so "0.1" is exactly 100_000 and never
 * 99_999.99999 rounded one way or the other.
 */
export const USDT_DECIMALS = 6;
export const MICRO_PER_USDT = 1_000_000;

/** Digits, an optional point and up to six more; or a point and one to six digits. */
const AMOUNT = /^(?:(\d+)(?:\.(\d{0,6}))?|\.(\d{1,6}))$/;

/**
 * "100.5" -> 100_500_000.
 *
 * Accepts plain decimal digits only: "100", "100.5", ".5", "5." (as typed,
 * mid-entry), leading zeros. Returns null for anything else - more than six
 * decimals, a sign, exponents ("1e3"), thousands separators ("1,000" is
 * refused rather than guessed at, since a comma is the decimal point in much
 * of the world), spaces inside the number, or a value too large to hold
 * exactly. Surrounding whitespace is ignored. Zero parses to 0; whether zero
 * is an acceptable amount is the caller's decision.
 */
export function parseUsdt(input: string): number | null {
  const match = AMOUNT.exec(input.trim());
  if (!match) return null;
  const whole = match[1] ?? '';
  const fraction = match[2] ?? match[3] ?? '';
  const digits = (whole + fraction.padEnd(USDT_DECIMALS, '0')).replace(/^0+(?=\d)/, '');
  // An integer string converts to Number exactly while it stays within the
  // safe range; past it the last digits would silently change.
  if (digits.length > 16) return null;
  const micro = Number(digits);
  return Number.isSafeInteger(micro) ? micro : null;
}

function groupThousands(digits: string): string {
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/** Whole part and six-digit fraction of a micro amount, both as strings, plus the sign. */
function split(micro: number): { negative: boolean; whole: string; fraction: string } {
  const value = Math.round(micro);
  const negative = value < 0;
  const digits = String(Math.abs(value)).padStart(USDT_DECIMALS + 1, '0');
  return {
    negative,
    whole: digits.slice(0, -USDT_DECIMALS),
    fraction: digits.slice(-USDT_DECIMALS),
  };
}

export interface FormatUsdtOptions {
  /**
   * Drop trailing zeros, keeping at least two decimals: "100.50 USDT",
   * "0.000001 USDT". For compact places - list rows, chips. Default false.
   */
  trim?: boolean;
  /** Append " USDT". Default true. */
  unit?: boolean;
  /** Prefix "+" on a positive amount (a negative one always shows "-"). */
  signed?: boolean;
}

/**
 * 1_250_500_000 -> "1,250.500000 USDT".
 *
 * Six decimals by default, so the figure on screen is exactly the figure in
 * the ledger. A non-finite value (a field the server did not send) prints as
 * "--" rather than "NaN USDT".
 */
export function formatUsdt(micro: number, options: FormatUsdtOptions = {}): string {
  const { trim = false, unit = true, signed = false } = options;
  if (!Number.isFinite(micro)) return '--';
  const { negative, whole, fraction } = split(micro);
  const shown = trim ? fraction.replace(/0+$/, '').padEnd(2, '0') : fraction;
  const isZero = /^0+$/.test(whole + fraction);
  const sign = negative && !isZero ? '-' : signed && !isZero ? '+' : '';
  return sign + groupThousands(whole) + '.' + shown + (unit ? ' USDT' : '');
}

/**
 * 1_250_500_000 -> "1250.5": what to put back into an amount field (the Max
 * button), so that `parseUsdt` reads it as exactly the same amount. No
 * grouping, no unit, no trailing zeros.
 */
export function toUsdtInput(micro: number): string {
  if (!Number.isFinite(micro) || micro <= 0) return '0';
  const { whole, fraction } = split(micro);
  const trimmed = fraction.replace(/0+$/, '');
  return trimmed ? whole + '.' + trimmed : whole;
}
