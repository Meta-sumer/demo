import { ApiRequestError } from '../../services/http';
import { formatUsdt } from '../../utils/usdt';
import type {
  UsdtDepositStatus,
  UsdtDepositView,
  UsdtNetworkView,
  UsdtTransaction,
  UsdtWithdrawalQuote,
  UsdtWithdrawalStatus,
  UsdtWithdrawalView,
} from '../../types/api';

/**
 * Everything about the USDT wallet that is a rule rather than a picture: the
 * words a status is shown in, which states are still moving, what an address
 * looks like, which server messages may be shown as they are. Kept free of
 * React so the tests can pin it down.
 */

export type BadgeTone = 'neutral' | 'up' | 'down' | 'warn' | 'accent';

/* ---------------------------------------------------------------- networks */

const NETWORK_LABELS: Record<string, string> = { USDTTRC20: 'TRC20', USDTBSC: 'BSC', TRC20: 'TRC20', BSC: 'BSC' };

/**
 * "USDTTRC20" -> "TRC20". Records name the network by code; a label is
 * accepted too, and anything unknown is shown as it came rather than hidden.
 */
export function networkLabel(network: string | null | undefined, networks?: UsdtNetworkView[]): string {
  if (!network) return '--';
  const known = networks?.find((n) => n.code === network || n.label === network);
  return known?.label ?? NETWORK_LABELS[network] ?? network;
}

/** The chain's own explorer page for a transaction, when the network is known. */
export function explorerTxUrl(network: string, txHash: string): string | null {
  const label = networkLabel(network);
  if (!txHash) return null;
  if (label === 'TRC20') return 'https://tronscan.org/#/transaction/' + encodeURIComponent(txHash);
  if (label === 'BSC') return 'https://bscscan.com/tx/' + encodeURIComponent(txHash);
  return null;
}

const BASE58 = /^T[1-9A-HJ-NP-Za-km-z]{33}$/;
const EVM = /^0x[0-9a-fA-F]{40}$/;

/**
 * A hint about a destination address, or null when it looks right (or is
 * still empty).
 *
 * Help only: it catches a pasted address from the wrong chain or a dropped
 * character before the customer gets as far as confirming. The server checks
 * the address itself and its verdict is the one that counts.
 */
export function addressHint(network: string, address: string): string | null {
  const value = address.trim();
  if (!value) return null;
  const label = networkLabel(network);
  if (label === 'TRC20' && !BASE58.test(value)) {
    return EVM.test(value)
      ? 'This looks like a BSC address. TRC20 addresses start with T.'
      : 'TRC20 addresses start with T and are 34 characters long.';
  }
  if (label === 'BSC' && !EVM.test(value)) {
    return BASE58.test(value)
      ? 'This looks like a TRC20 address. BSC addresses start with 0x.'
      : 'BSC addresses start with 0x followed by 40 letters a-f and digits.';
  }
  return null;
}

/* ---------------------------------------------------------------- deposits */

export const DEPOSIT_STATUS_WORDS: Record<UsdtDepositStatus, string> = {
  created: 'Created',
  awaiting_payment: 'Awaiting payment',
  partially_paid: 'Partially paid',
  confirming: 'Confirming',
  paid: 'Paid',
  overpaid: 'Overpaid',
  underpaid: 'Underpaid',
  expired: 'Expired',
  cancelled: 'Cancelled',
  failed: 'Failed',
};

export function depositStatusWord(status: string): string {
  return DEPOSIT_STATUS_WORDS[status as UsdtDepositStatus] ?? humanise(status);
}

export function depositStatusTone(status: string): BadgeTone {
  switch (status) {
    case 'paid':
    case 'overpaid':
      return 'up';
    case 'failed':
      return 'down';
    case 'expired':
    case 'cancelled':
      return 'neutral';
    default:
      return 'warn';
  }
}

/** Statuses in which money may still arrive. */
const DEPOSIT_OPEN = new Set<string>(['created', 'awaiting_payment', 'partially_paid', 'confirming']);

export function isDepositOpen(status: string): boolean {
  return DEPOSIT_OPEN.has(status);
}

/**
 * Whether paying (more) is still the customer's move: the checkout link and
 * the pay address are offered only then. Once the payment is confirming the
 * money is on its way, and pointing at the checkout again invites a second
 * payment.
 */
export function canStillPay(status: string): boolean {
  return status === 'created' || status === 'awaiting_payment' || status === 'partially_paid';
}

/**
 * Whether a deposit is worth asking about again: money may still arrive, or
 * it has been paid but the wallet has not been credited yet.
 */
export function depositNeedsPolling(deposit: Pick<UsdtDepositView, 'status' | 'credited'>): boolean {
  if (deposit.credited) return false;
  return DEPOSIT_OPEN.has(deposit.status) || deposit.status === 'paid' || deposit.status === 'overpaid';
}

/** One or two sentences on what a deposit's status means for the customer. */
export function depositStatusExplanation(
  deposit: Pick<UsdtDepositView, 'status' | 'credited'>,
  network: string,
): string {
  switch (deposit.status) {
    case 'created':
      return 'The deposit is ready. Open the payment page to pay.';
    case 'awaiting_payment':
      return 'Waiting for your payment. Send USDT on the ' + network + ' network to the address shown.';
    case 'partially_paid':
      return (
        'We have received part of the amount. You can still send the rest to the same address, on the ' +
        network +
        ' network, until the deposit expires.'
      );
    case 'confirming':
      return 'Your payment has been seen on the network and is waiting for confirmations.';
    case 'paid':
    case 'overpaid':
      if (!deposit.credited) return 'Your payment is confirmed and is being credited to your USDT wallet.';
      return deposit.status === 'overpaid'
        ? 'You sent more than the requested amount. What was credited is shown below.'
        : 'Credited to your USDT wallet.';
    case 'underpaid':
      return (
        'Less than the requested amount arrived before the deposit closed. ' +
        'Our support team will review it and contact you.'
      );
    case 'expired':
      return (
        'The deposit expired before a payment arrived. ' +
        'If you already sent USDT, contact support with the reference.'
      );
    case 'cancelled':
      return 'This deposit was cancelled. Nothing was credited.';
    case 'failed':
      return 'This deposit could not be completed. If you sent USDT, contact support with the reference.';
    default:
      return '';
  }
}

/* ------------------------------------------------------------- withdrawals */

const WITHDRAWAL_STATUS_WORDS: Record<UsdtWithdrawalStatus, string> = {
  pending_review: 'Under review',
  requested: 'Processing',
  awaiting_approval: 'Processing',
  sending: 'Processing',
  completed: 'Completed',
  failed: 'Failed',
  rejected: 'Rejected',
};

export function withdrawalStatusWord(status: string): string {
  return WITHDRAWAL_STATUS_WORDS[status as UsdtWithdrawalStatus] ?? humanise(status);
}

export function withdrawalStatusTone(status: string): BadgeTone {
  if (status === 'completed') return 'up';
  if (status === 'failed' || status === 'rejected') return 'down';
  return 'warn';
}

export function isWithdrawalFinal(status: string): boolean {
  return status === 'completed' || status === 'failed' || status === 'rejected';
}

/** Failed or rejected: the reserved amount went back to the wallet. */
export function isWithdrawalReturned(status: string): boolean {
  return status === 'failed' || status === 'rejected';
}

export function withdrawalStatusExplanation(withdrawal: Pick<UsdtWithdrawalView, 'status'>, network: string): string {
  switch (withdrawal.status) {
    case 'pending_review':
      return 'Our team checks this withdrawal before it is sent. The amount stays reserved in your wallet meanwhile.';
    case 'requested':
    case 'awaiting_approval':
    case 'sending':
      return 'Your withdrawal is being sent on the ' + network + ' network.';
    case 'completed':
      return 'Sent. It can take a few minutes to show in the receiving wallet.';
    // Where the money went is said beside the reason, once.
    case 'failed':
      return 'The withdrawal could not be sent.';
    case 'rejected':
      return 'Our team did not approve this withdrawal.';
    default:
      return '';
  }
}

/* ------------------------------------------------------------------ ledger */

const LEDGER_LABELS: Record<string, string> = {
  DEPOSIT: 'Deposit',
  WITHDRAWAL: 'Withdrawal',
  WITHDRAWAL_LOCK: 'Withdrawal reserved',
  WITHDRAWAL_RELEASE: 'Withdrawal returned',
  WITHDRAWAL_REFUND: 'Withdrawal returned',
  TRANSFER_TO_TRADING: 'Transfer to Trading',
  TRANSFER_OUT: 'Transfer to Trading',
  FEE: 'Network fee',
  ADJUSTMENT_CREDIT: 'Adjustment',
  ADJUSTMENT_DEBIT: 'Adjustment',
};

export function ledgerLabel(type: string): string {
  return LEDGER_LABELS[type] ?? humanise(type);
}

/**
 * +1 for money in, -1 for money out, 0 for a reservation being placed or
 * lifted (the balance is unchanged, only what is available moves).
 */
export function ledgerSign(row: Pick<UsdtTransaction, 'movement'>): 1 | -1 | 0 {
  if (row.movement === 'CREDIT') return 1;
  if (row.movement === 'DEBIT') return -1;
  return 0;
}

export function ledgerMovementWord(movement: UsdtTransaction['movement']): string {
  switch (movement) {
    case 'CREDIT':
      return 'Credited';
    case 'DEBIT':
      return 'Debited';
    case 'LOCK':
      return 'Reserved';
    case 'UNLOCK':
      return 'Released';
    default:
      return humanise(String(movement));
  }
}

/* ------------------------------------------------------------------ errors */

/**
 * Server codes whose message is written for the customer and is shown as it
 * comes. Anything else - a stack trace, a provider's raw text, a code this
 * build has never heard of - gets a generic line instead.
 */
export const KNOWN_ERROR_CODES = new Set([
  'INVALID_NETWORK',
  'INVALID_ADDRESS',
  'INVALID_AMOUNT',
  'INSUFFICIENT_BALANCE',
  'MIN_WITHDRAWAL_NOT_MET',
  'MAX_WITHDRAWAL_EXCEEDED',
  'DAILY_CAP_EXCEEDED',
  'USDT_DISABLED',
  'USDT_TRANSFER_DISABLED',
  'PROVIDER_ERROR',
  'PROVIDER_UNAVAILABLE',
  'RATE_LIMITED',
  'IDEMPOTENCY_PAYLOAD_MISMATCH',
]);

/** Used when a known code arrives without a message of its own. */
const FALLBACK_MESSAGES: Record<string, string> = {
  INVALID_NETWORK: 'That network is not available.',
  INVALID_ADDRESS: 'That address is not valid for the selected network.',
  INVALID_AMOUNT: 'Enter a valid amount.',
  INSUFFICIENT_BALANCE: 'You do not have enough available USDT.',
  MIN_WITHDRAWAL_NOT_MET: 'The amount is below the minimum withdrawal.',
  MAX_WITHDRAWAL_EXCEEDED: 'The amount is above the maximum withdrawal.',
  DAILY_CAP_EXCEEDED: 'This would go over your daily withdrawal limit.',
  USDT_DISABLED: 'The USDT wallet is not available right now.',
  USDT_TRANSFER_DISABLED: 'Trading with USDT is not available yet.',
  PROVIDER_ERROR: 'The payment provider returned an error. Please try again.',
  PROVIDER_UNAVAILABLE: 'The payment provider is not reachable right now. Please try again shortly.',
  RATE_LIMITED: 'Too many requests. Wait a moment and try again.',
  IDEMPOTENCY_PAYLOAD_MISMATCH: 'This request changed while it was being sent. Check the details and try again.',
};

export const GENERIC_ERROR = 'Something went wrong. Please try again.';
export const OFFLINE_ERROR = 'Cannot reach the server. Check your connection and try again.';

export function usdtErrorMessage(error: unknown): string {
  if (!(error instanceof ApiRequestError)) return GENERIC_ERROR;
  if (error.code === 'NETWORK_ERROR') return OFFLINE_ERROR;
  if (!KNOWN_ERROR_CODES.has(error.code)) return GENERIC_ERROR;
  return error.message.trim() || FALLBACK_MESSAGES[error.code] || GENERIC_ERROR;
}

export function errorCode(error: unknown): string | null {
  return error instanceof ApiRequestError ? error.code : null;
}

/* ------------------------------------------------------------- idempotency */

/** A fresh random key; falls back to getRandomValues where randomUUID is missing (plain http). */
export function newIdempotencyKey(): string {
  const c = globalThis.crypto;
  if (typeof c?.randomUUID === 'function') return c.randomUUID();
  const bytes = new Uint8Array(16);
  c.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/**
 * The idempotency key of one attempt.
 *
 * The same request body gets the same key, however many times it is sent - a
 * double click, a retry after a timeout - so the server acts on it once.
 * Changing anything in the body starts a new attempt with a new key (reusing
 * the old one would be refused as a payload mismatch), and so does `reset`,
 * called once an attempt has succeeded so the next identical request is a new
 * one rather than a replay of the last.
 */
export function createAttemptKey(generate: () => string = newIdempotencyKey) {
  let current: { payload: string; key: string } | null = null;
  return {
    for(body: unknown): string {
      const payload = JSON.stringify(body);
      if (!current || current.payload !== payload) current = { payload, key: generate() };
      return current.key;
    },
    reset(): void {
      current = null;
    },
  };
}

/* ------------------------------------------------------------------- misc */

/** Why a typed amount cannot be sent as it is, or null. An empty field is not an error yet. */
export function amountProblem(text: string, amount: number | null): string | null {
  if (!text.trim()) return null;
  if (amount === null) return 'Enter digits with up to 6 decimal places, using a point: 100 or 100.5.';
  if (amount <= 0) return 'Enter an amount greater than zero.';
  return null;
}

/**
 * Why a withdrawal of `amount` would be refused, judged against the balance
 * and the latest quote's limits; null when nothing stands out. Help for the
 * customer before they confirm - the server applies the same rules itself.
 * A limit of 0 means there is none.
 */
export function withdrawalLimitProblem(
  amount: number,
  available: number | null,
  quote: Pick<UsdtWithdrawalQuote, 'minWithdrawal' | 'maxWithdrawal' | 'dailyCap' | 'dailyUsed' | 'netAmount'> | null,
): string | null {
  if (available !== null && amount > available) {
    return 'That is more than your available balance of ' + formatUsdt(available, { trim: true }) + '.';
  }
  if (!quote) return null;
  if (quote.minWithdrawal > 0 && amount < quote.minWithdrawal) {
    return 'The minimum withdrawal is ' + formatUsdt(quote.minWithdrawal, { trim: true }) + '.';
  }
  if (quote.maxWithdrawal > 0 && amount > quote.maxWithdrawal) {
    return 'The maximum withdrawal is ' + formatUsdt(quote.maxWithdrawal, { trim: true }) + '.';
  }
  if (quote.dailyCap > 0 && quote.dailyUsed + amount > quote.dailyCap) {
    const left = Math.max(0, quote.dailyCap - quote.dailyUsed);
    return 'That is over your daily limit. You can withdraw up to ' + formatUsdt(left, { trim: true }) + ' more today.';
  }
  if (quote.netAmount <= 0) return 'The amount does not cover the network fee.';
  return null;
}

/**
 * The payment page address, only if it is an ordinary web address. It comes
 * from the server, but it is about to be navigated to; a `javascript:` URL
 * there must never run.
 */
export function safeExternalUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  try {
    const parsed = new URL(url);
    return parsed.protocol === 'https:' || parsed.protocol === 'http:' ? parsed.href : null;
  } catch {
    return null;
  }
}

function humanise(code: string): string {
  const words = code.replace(/[_-]+/g, ' ').trim().toLowerCase();
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : '--';
}
