/**
 * Money crosses the wire as integer minor units. These two helpers are the only
 * place that converts, so a display value can never leak back into a request.
 */
export const MINOR = 100;

export function toMinor(major: number): number {
  return Math.round(major * MINOR);
}

export function fromMinor(minor: number): number {
  return minor / MINOR;
}

export function formatMoney(minor: number, currency = 'INR', withSymbol = true): string {
  const value = fromMinor(minor);
  if (!withSymbol) {
    return value.toLocaleString('en-IN', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
  }
  return value.toLocaleString('en-IN', {
    style: 'currency',
    currency,
    minimumFractionDigits: 2,
    maximumFractionDigits: 2,
  });
}

export function formatSignedMoney(minor: number, currency = 'INR'): string {
  const sign = minor > 0 ? '+' : '';
  return sign + formatMoney(minor, currency);
}

export function formatPrice(price: number, precision = 2): string {
  return price.toLocaleString('en-US', {
    minimumFractionDigits: precision,
    maximumFractionDigits: precision,
  });
}

export function formatExpiry(seconds: number): string {
  if (seconds < 60) return seconds + 's';
  if (seconds < 3600) {
    const minutes = seconds / 60;
    return (Number.isInteger(minutes) ? minutes : minutes.toFixed(1)) + 'm';
  }
  return (seconds / 3600).toFixed(1) + 'h';
}

/** mm:ss for a countdown; clamped so an overdue trade shows 00:00. */
export function formatCountdown(msRemaining: number): string {
  const total = Math.max(0, Math.ceil(msRemaining / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return String(minutes).padStart(2, '0') + ':' + String(seconds).padStart(2, '0');
}

export function formatTime(value: number | string | Date): string {
  return new Date(value).toLocaleTimeString('en-GB', { hour12: false });
}

export function formatDateTime(value: number | string | Date): string {
  return new Date(value).toLocaleString('en-GB', {
    day: '2-digit',
    month: 'short',
    hour: '2-digit',
    minute: '2-digit',
    hour12: false,
  });
}

/**
 * Money for a chart tag: "₹100", "₹1,250.50".
 *
 * The trailing ".00" is dropped because the tag sits on top of the price
 * series, where every character competes with a candle for space. Anything
 * with real paise still shows them - a rounded stake on an order marker would
 * misreport what was risked.
 */
export function formatMoneyCompact(minor: number, currency = 'INR'): string {
  const value = fromMinor(minor);
  const whole = Number.isInteger(value);
  return value.toLocaleString('en-IN', {
    style: 'currency',
    currency,
    minimumFractionDigits: whole ? 0 : 2,
    maximumFractionDigits: 2,
  });
}

/**
 * The symbol a currency is written with, e.g. "INR" -> "₹".
 *
 * Asked of the formatter rather than stripped out of a formatted string:
 * currencies differ in where the symbol sits, whether a space follows it and
 * which digit and grouping characters the locale uses, so any pattern that
 * removes "the numeric parts" is wrong for some of them. `formatToParts` says
 * which part is the currency, so there is nothing to guess.
 */
export function currencySymbolFor(currency = 'INR'): string {
  try {
    const parts = new Intl.NumberFormat('en-IN', { style: 'currency', currency }).formatToParts(0);
    return parts.find((part) => part.type === 'currency')?.value ?? currency;
  } catch {
    // An unknown code makes Intl throw; the code itself is a usable label.
    return currency;
  }
}
