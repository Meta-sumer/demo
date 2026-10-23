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

/**
 * Rupees (major units, as typed into a form) with the ₹ sign and Indian digit
 * grouping: 5000000 -> "₹50,00,000". Paise are kept when present.
 */
export function formatRupees(rupees: number): string {
  const sign = rupees < 0 ? '-' : '';
  return sign + '₹' + Math.abs(rupees).toLocaleString('en-IN', { maximumFractionDigits: 2 });
}

/**
 * The size of a rupee amount the way it is said in India, so a ceiling can be
 * read at a glance: 5000000 -> "50 lakh", 20000000 -> "2 crore". Empty below
 * a thousand, where the figure already says it.
 */
export function rupeesInWords(rupees: number): string {
  const abs = Math.abs(rupees);
  const scaled = (value: number, unit: string) =>
    value.toLocaleString('en-IN', { maximumFractionDigits: 2 }) + ' ' + unit;
  if (abs >= 10_000_000) return scaled(abs / 10_000_000, 'crore');
  if (abs >= 100_000) return scaled(abs / 100_000, 'lakh');
  if (abs >= 1_000) return scaled(abs / 1_000, 'thousand');
  return '';
}

/**
 * A millisecond setting in a readable unit: 60000 -> "1 min", 1500 -> "1.5 s",
 * 604800000 -> "7 days". Empty under a second, where "ms" already reads well.
 */
export function formatMs(ms: number): string {
  if (!Number.isFinite(ms) || ms < 1000) return '';
  const scaled = (value: number, unit: string) => Number(value.toFixed(2)).toString() + ' ' + unit;
  if (ms < 60_000) return scaled(ms / 1000, 's');
  if (ms < 3_600_000) return scaled(ms / 60_000, 'min');
  if (ms < 172_800_000) return scaled(ms / 3_600_000, 'h');
  return scaled(ms / 86_400_000, 'days');
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
