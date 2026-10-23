import { formatMoney, toMinor } from './format';
import { formatUsdt, parseUsdt } from './usdt';

/**
 * Manual wallet adjustments: which wallet, in which currency.
 *
 * LIVE and DEMO are INR wallets (amounts in paise); FUNDING is the customer's
 * USDT wallet (amounts in micro-USDT). The amount an operator types is read in
 * the selected wallet's currency, never converted.
 */
export type AdjustmentAccount = 'LIVE' | 'DEMO' | 'FUNDING';

export const ADJUSTMENT_ACCOUNTS: { value: AdjustmentAccount; label: string }[] = [
  { value: 'LIVE', label: 'Live (INR)' },
  { value: 'DEMO', label: 'Demo (INR)' },
  { value: 'FUNDING', label: 'USDT wallet' },
];

export function adjustmentCurrency(account: AdjustmentAccount): 'INR' | 'USDT' {
  return account === 'FUNDING' ? 'USDT' : 'INR';
}

/**
 * The typed amount in the wallet's minor unit - paise (up to 2 decimals) for
 * INR, micro-USDT (up to 6 decimals) for USDT - or null when it is not a
 * positive amount that currency can hold exactly.
 */
export function parseAdjustmentAmount(text: string, account: AdjustmentAccount): number | null {
  if (account === 'FUNDING') {
    const micro = parseUsdt(text);
    return micro !== null && micro > 0 ? micro : null;
  }
  const cleaned = text.replace(/[,\s_]/g, '');
  if (!/^(\d+(\.\d{0,2})?|\.\d{1,2})$/.test(cleaned)) return null;
  const minor = toMinor(Number(cleaned));
  return Number.isSafeInteger(minor) && minor > 0 ? minor : null;
}

/** An amount in the wallet's minor unit, formatted in its currency. */
export function formatAdjustment(minor: number, account: AdjustmentAccount): string {
  return account === 'FUNDING' ? formatUsdt(minor) : formatMoney(minor);
}
