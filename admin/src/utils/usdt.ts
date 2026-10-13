import type {
  UsdtDepositStatus,
  UsdtDepositView,
  UsdtWithdrawalStatus,
  WebhookProcessingStatus,
} from '../types/usdt';

/**
 * USDT money and wording helpers.
 *
 * Amounts arrive as integer micro-USDT. Formatting works on the digit string,
 * never on a float: 0.1 + 0.2 style drift is tolerable in a chart axis but not
 * in the figure an operator approves a payout against.
 */
export const MICRO_PER_USDT = 1_000_000;
const DECIMALS = 6;

/** Splits an integer amount into sign and digits; null for anything that is not an exact integer. */
function integerDigits(micro: number | bigint | string | null | undefined): { negative: boolean; digits: string } | null {
  if (micro == null) return null;
  let text: string;
  if (typeof micro === 'bigint') text = micro.toString();
  else if (typeof micro === 'number') {
    if (!Number.isSafeInteger(micro)) return null;
    text = String(micro);
  } else {
    text = micro.trim();
    if (!/^-?\d+$/.test(text)) return null;
  }
  const negative = text.startsWith('-');
  const digits = (negative ? text.slice(1) : text).replace(/^0+(?=\d)/, '');
  return { negative: negative && /[1-9]/.test(digits), digits };
}

function group(whole: string): string {
  return whole.replace(/\B(?=(\d{3})+(?!\d))/g, ',');
}

/**
 * 1250500000 -> "1,250.500000 USDT".
 *
 * Always six decimals so columns line up and nothing is silently rounded.
 * Anything that is not an exact integer renders as "--" rather than a guess.
 */
export function formatUsdt(
  micro: number | bigint | string | null | undefined,
  options: { unit?: boolean; sign?: boolean } = {},
): string {
  const parts = integerDigits(micro);
  if (!parts) return '--';
  const padded = parts.digits.padStart(DECIMALS + 1, '0');
  const whole = padded.slice(0, -DECIMALS);
  const fraction = padded.slice(-DECIMALS);
  const isZero = !/[1-9]/.test(parts.digits);
  const prefix = parts.negative ? '-' : options.sign && !isZero ? '+' : '';
  return prefix + group(whole) + '.' + fraction + (options.unit === false ? '' : ' USDT');
}

/** The plain decimal an operator types back into a form: 10500000 -> "10.5". */
export function toUsdtInput(micro: number | null | undefined): string {
  const formatted = formatUsdt(micro, { unit: false });
  if (formatted === '--') return '';
  return formatted.replace(/,/g, '').replace(/\.?0+$/, '');
}

/**
 * "1,250.5" -> 1250500000. Null for empty, malformed, negative, more than six
 * decimals, or beyond the safe integer range - the caller shows a field error.
 */
export function parseUsdt(text: string): number | null {
  const cleaned = text.replace(/[,\s_]/g, '');
  const match = /^(\d*)(?:\.(\d*))?$/.exec(cleaned);
  if (!match) return null;
  const whole = match[1] ?? '';
  const fraction = match[2] ?? '';
  if (whole === '' && fraction === '') return null;
  if (fraction.length > DECIMALS) return null;
  const micro = BigInt(whole || '0') * BigInt(MICRO_PER_USDT) + BigInt(fraction.padEnd(DECIMALS, '0') || '0');
  if (micro > BigInt(Number.MAX_SAFE_INTEGER)) return null;
  return Number(micro);
}

/**
 * Withdrawal fee estimate: fixed + amount x percent, rounded half up to the
 * micro. The percentage is scaled to an integer first so the multiplication
 * itself is exact.
 */
export function estimateFee(amountMicro: number, feePercent: number, feeFixedMicro: number): number {
  const fixed = Number.isSafeInteger(feeFixedMicro) ? feeFixedMicro : 0;
  if (!Number.isSafeInteger(amountMicro) || amountMicro <= 0) return fixed;
  if (!Number.isFinite(feePercent) || feePercent <= 0) return fixed;
  // The percentage as whole millionths of a percent: 1.5 % -> 1_500_000.
  const scaledPercent = BigInt(Math.round(feePercent * 1_000_000));
  const divisor = BigInt(100 * 1_000_000);
  const variable = (BigInt(amountMicro) * scaledPercent + divisor / BigInt(2)) / divisor;
  return Number(variable) + fixed;
}

/** "TXyz…ab9F" - the full value stays in the title and in the copy button. */
export function shortenMiddle(value: string | null | undefined, head = 6, tail = 4): string {
  if (!value) return '--';
  if (value.length <= head + tail + 1) return value;
  return value.slice(0, head) + '…' + value.slice(-tail);
}

/** Network codes and labels both appear on the wire; reduce either to the short label. */
export function networkLabel(network: string | null | undefined): string {
  if (!network) return '--';
  const upper = network.toUpperCase();
  if (upper.includes('TRC20') || upper.includes('TRON')) return 'TRC20';
  if (upper.includes('BSC') || upper.includes('BEP20')) return 'BSC';
  return network;
}

export function explorerTxUrl(network: string | null | undefined, txHash: string | null | undefined): string | null {
  if (!txHash) return null;
  const label = networkLabel(network);
  if (label === 'TRC20') return 'https://tronscan.org/#/transaction/' + encodeURIComponent(txHash);
  if (label === 'BSC') return 'https://bscscan.com/tx/' + encodeURIComponent(txHash);
  return null;
}

export function explorerAddressUrl(network: string | null | undefined, address: string | null | undefined): string | null {
  if (!address) return null;
  const label = networkLabel(network);
  if (label === 'TRC20') return 'https://tronscan.org/#/address/' + encodeURIComponent(address);
  if (label === 'BSC') return 'https://bscscan.com/address/' + encodeURIComponent(address);
  return null;
}

/** Only http(s) links leave the console; anything else (javascript:, data:) is shown as text. */
export function safeExternalUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  return /^https?:\/\/[^\s]+$/i.test(url.trim()) ? url.trim() : null;
}

/* ---------------------------------------------------------------- statuses */

export type Tone = 'neutral' | 'up' | 'down' | 'warn' | 'accent' | 'critical';

export interface StatusMeta {
  label: string;
  tone: Tone;
  description: string;
}

export const DEPOSIT_STATUSES: Record<UsdtDepositStatus, StatusMeta> = {
  created: { label: 'Created', tone: 'neutral', description: 'Invoice requested from BeeX' },
  awaiting_payment: { label: 'Awaiting payment', tone: 'accent', description: 'Invoice open; nothing received yet' },
  partially_paid: { label: 'Partially paid', tone: 'warn', description: 'Less than the expected amount has arrived' },
  confirming: { label: 'Confirming', tone: 'accent', description: 'Payment seen on chain, waiting for confirmations' },
  paid: { label: 'Paid', tone: 'up', description: 'Paid in full' },
  overpaid: { label: 'Overpaid', tone: 'up', description: 'More than the expected amount arrived' },
  underpaid: { label: 'Underpaid', tone: 'warn', description: 'Invoice closed with less than the expected amount' },
  expired: { label: 'Expired', tone: 'neutral', description: 'Invoice expired without payment' },
  cancelled: { label: 'Cancelled', tone: 'neutral', description: 'Invoice cancelled' },
  failed: { label: 'Failed', tone: 'down', description: 'BeeX reported a failure' },
};

export const WITHDRAWAL_STATUSES: Record<UsdtWithdrawalStatus, StatusMeta> = {
  pending_review: { label: 'Under review', tone: 'warn', description: 'Waiting for an operator to approve or reject' },
  requested: { label: 'Requested', tone: 'accent', description: 'Approved and submitted to BeeX' },
  awaiting_approval: { label: 'BeeX review', tone: 'accent', description: 'Held by BeeX for its own review' },
  sending: { label: 'Sending', tone: 'accent', description: 'Broadcast on chain, not yet final' },
  completed: { label: 'Completed', tone: 'up', description: 'Delivered on chain' },
  failed: { label: 'Failed', tone: 'down', description: 'The payout failed; funds return to the user' },
  rejected: { label: 'Rejected', tone: 'down', description: 'Rejected by an operator; funds returned to the user' },
};

export const PROCESSING_STATUSES: Record<WebhookProcessingStatus, StatusMeta> = {
  processed: { label: 'Processed', tone: 'up', description: 'Applied to the deposit or withdrawal' },
  duplicate: { label: 'Duplicate', tone: 'neutral', description: 'Already received; ignored safely' },
  ignored: { label: 'Ignored', tone: 'neutral', description: 'Not relevant to any record' },
  failed: { label: 'Failed', tone: 'down', description: 'Processing raised an error' },
  pending: { label: 'Pending', tone: 'warn', description: 'Received, not processed yet' },
};

export const DEPOSIT_STATUS_ORDER = Object.keys(DEPOSIT_STATUSES) as UsdtDepositStatus[];
export const WITHDRAWAL_STATUS_ORDER = Object.keys(WITHDRAWAL_STATUSES) as UsdtWithdrawalStatus[];
export const PROCESSING_STATUS_ORDER = Object.keys(PROCESSING_STATUSES) as WebhookProcessingStatus[];

/** "awaiting_payment" -> "Awaiting payment", for a status this console does not know yet. */
export function humanize(value: string): string {
  const spaced = value.replace(/[_.-]+/g, ' ').trim().toLowerCase();
  return spaced ? spaced.charAt(0).toUpperCase() + spaced.slice(1) : '--';
}

function lookup(table: Record<string, StatusMeta>, status: string | null | undefined): StatusMeta {
  if (!status) return { label: '--', tone: 'neutral', description: '' };
  return table[status] ?? table[status.toLowerCase()] ?? { label: humanize(status), tone: 'neutral', description: '' };
}

export const depositStatusMeta = (status: string | null | undefined) => lookup(DEPOSIT_STATUSES, status);
export const withdrawalStatusMeta = (status: string | null | undefined) => lookup(WITHDRAWAL_STATUSES, status);
export const processingStatusMeta = (status: string | null | undefined) => lookup(PROCESSING_STATUSES, status);

/* ------------------------------------------------------------------ rules */

/**
 * Whether "Credit received amount" applies: an underpaid invoice, or a
 * partially paid one whose invoice has expired (so nothing more can arrive),
 * that has not been credited and actually received something.
 * The server applies the same rule; this only decides whether to offer it.
 */
export function creditEligibility(
  deposit: Pick<UsdtDepositView, 'status' | 'credited' | 'amountReceived' | 'expiresAt'>,
  now = Date.now(),
): { eligible: boolean; reason: string } {
  if (deposit.credited) return { eligible: false, reason: 'Already credited' };
  if (deposit.status !== 'underpaid' && deposit.status !== 'partially_paid') {
    return { eligible: false, reason: 'Only underpaid or partially paid deposits can be credited manually' };
  }
  if (!(deposit.amountReceived > 0)) return { eligible: false, reason: 'Nothing has been received' };
  if (deposit.status === 'partially_paid') {
    const expires = deposit.expiresAt ? Date.parse(deposit.expiresAt) : NaN;
    if (!Number.isFinite(expires) || expires > now) {
      return { eligible: false, reason: 'Still open - more may arrive. Available once the invoice expires.' };
    }
  }
  return { eligible: true, reason: '' };
}

/** Local calendar days from <input type="date"> as an inclusive ISO range. */
export function dayRangeToIso(from: string, to: string): { from?: string; to?: string } {
  const toDate = (value: string, end: boolean) => {
    const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
    if (!match) return undefined;
    const date = end
      ? new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 23, 59, 59, 999)
      : new Date(Number(match[1]), Number(match[2]) - 1, Number(match[3]), 0, 0, 0, 0);
    return Number.isNaN(date.getTime()) ? undefined : date.toISOString();
  };
  return { from: toDate(from, false), to: toDate(to, true) };
}
