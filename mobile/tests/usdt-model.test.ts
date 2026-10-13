import { describe, expect, it } from 'vitest';
import { ApiRequestError } from '../src/services/http';
import {
  GENERIC_ERROR,
  KNOWN_ERROR_CODES,
  OFFLINE_ERROR,
  addressHint,
  amountProblem,
  createAttemptKey,
  depositNeedsPolling,
  depositStatusExplanation,
  depositStatusWord,
  explorerTxUrl,
  isDepositOpen,
  isWithdrawalFinal,
  isWithdrawalReturned,
  ledgerSign,
  networkLabel,
  safeExternalUrl,
  usdtErrorMessage,
  withdrawalLimitProblem,
  withdrawalStatusWord,
} from '../src/features/usdt/model';

const TRON = 'TQrY8tryqsYVCYS3MFbtffiPp2ccyn4STm';
const BSC = '0x8894E0a0c962CB723c1976a4421c95949bE2D4E3';

describe('networks', () => {
  it('labels a network code, a label, or an unknown value', () => {
    expect(networkLabel('USDTTRC20')).toBe('TRC20');
    expect(networkLabel('USDTBSC')).toBe('BSC');
    expect(networkLabel('TRC20')).toBe('TRC20');
    expect(networkLabel('USDTSOL')).toBe('USDTSOL');
    expect(networkLabel(null)).toBe('--');
  });

  it('links a transaction to the right explorer', () => {
    expect(explorerTxUrl('USDTTRC20', 'abc')).toBe('https://tronscan.org/#/transaction/abc');
    expect(explorerTxUrl('USDTBSC', '0xdef')).toBe('https://bscscan.com/tx/0xdef');
    expect(explorerTxUrl('USDTSOL', 'x')).toBeNull();
  });
});

describe('addressHint', () => {
  it('accepts well-formed addresses and an empty field', () => {
    expect(addressHint('USDTTRC20', TRON)).toBeNull();
    expect(addressHint('USDTBSC', BSC)).toBeNull();
    expect(addressHint('USDTBSC', BSC.toLowerCase())).toBeNull();
    expect(addressHint('USDTTRC20', '')).toBeNull();
    expect(addressHint('USDTTRC20', '  ' + TRON + '  ')).toBeNull();
  });

  it('spots an address from the other chain', () => {
    expect(addressHint('USDTTRC20', BSC)).toMatch(/looks like a BSC address/);
    expect(addressHint('USDTBSC', TRON)).toMatch(/looks like a TRC20 address/);
  });

  it('flags a short, long or mistyped address', () => {
    expect(addressHint('USDTTRC20', TRON.slice(0, -1))).toMatch(/start with T and are 34/);
    expect(addressHint('USDTTRC20', TRON + 'x')).toMatch(/start with T and are 34/);
    // 0, O, I and l are not base58.
    expect(addressHint('USDTTRC20', 'T0rY8tryqsYVCYS3MFbtffiPp2ccyn4STm')).not.toBeNull();
    expect(addressHint('USDTBSC', BSC.slice(0, -1))).toMatch(/0x followed by 40/);
    expect(addressHint('USDTBSC', BSC.replace('a', 'g'))).not.toBeNull();
  });
});

describe('deposit status', () => {
  it('has a word for every status in the contract', () => {
    expect(
      [
        'created',
        'awaiting_payment',
        'partially_paid',
        'confirming',
        'paid',
        'overpaid',
        'underpaid',
        'expired',
        'cancelled',
        'failed',
      ].map(depositStatusWord),
    ).toEqual([
      'Created',
      'Awaiting payment',
      'Partially paid',
      'Confirming',
      'Paid',
      'Overpaid',
      'Underpaid',
      'Expired',
      'Cancelled',
      'Failed',
    ]);
    expect(depositStatusWord('on_hold')).toBe('On hold');
  });

  it('keeps asking while money may arrive or the credit has not landed', () => {
    expect(depositNeedsPolling({ status: 'awaiting_payment', credited: false })).toBe(true);
    expect(depositNeedsPolling({ status: 'confirming', credited: false })).toBe(true);
    expect(depositNeedsPolling({ status: 'paid', credited: false })).toBe(true);
    expect(depositNeedsPolling({ status: 'paid', credited: true })).toBe(false);
    expect(depositNeedsPolling({ status: 'overpaid', credited: true })).toBe(false);
    expect(depositNeedsPolling({ status: 'expired', credited: false })).toBe(false);
    expect(depositNeedsPolling({ status: 'underpaid', credited: false })).toBe(false);
  });

  it('treats only the waiting states as open', () => {
    expect(isDepositOpen('partially_paid')).toBe(true);
    expect(isDepositOpen('paid')).toBe(false);
  });

  it('tells a partial payer the rest can go to the same address, and an underpayer that support reviews it', () => {
    expect(depositStatusExplanation({ status: 'partially_paid', credited: false }, 'TRC20')).toMatch(
      /rest to the same address/,
    );
    expect(depositStatusExplanation({ status: 'underpaid', credited: false }, 'TRC20')).toMatch(/support team will review/);
  });

  it('does not say "credited" for a paid deposit the server has not credited', () => {
    expect(depositStatusExplanation({ status: 'paid', credited: false }, 'BSC')).toMatch(/being credited/);
    expect(depositStatusExplanation({ status: 'paid', credited: true }, 'BSC')).toMatch(/^Credited/);
  });
});

describe('withdrawal status', () => {
  it('collapses the provider states into words a customer can use', () => {
    expect(withdrawalStatusWord('pending_review')).toBe('Under review');
    expect(withdrawalStatusWord('requested')).toBe('Processing');
    expect(withdrawalStatusWord('awaiting_approval')).toBe('Processing');
    expect(withdrawalStatusWord('sending')).toBe('Processing');
    expect(withdrawalStatusWord('completed')).toBe('Completed');
    expect(withdrawalStatusWord('failed')).toBe('Failed');
    expect(withdrawalStatusWord('rejected')).toBe('Rejected');
  });

  it('knows which states are final and which returned the money', () => {
    expect(isWithdrawalFinal('sending')).toBe(false);
    expect(isWithdrawalFinal('completed')).toBe(true);
    expect(isWithdrawalReturned('completed')).toBe(false);
    expect(isWithdrawalReturned('failed')).toBe(true);
    expect(isWithdrawalReturned('rejected')).toBe(true);
  });
});

describe('withdrawalLimitProblem', () => {
  const quote = { minWithdrawal: 10_000_000, maxWithdrawal: 1_000_000_000, dailyCap: 2_000_000_000, dailyUsed: 0, netAmount: 1 };

  it('passes an amount inside every limit', () => {
    expect(withdrawalLimitProblem(50_000_000, 100_000_000, quote)).toBeNull();
  });

  it('refuses more than is available before anything else', () => {
    expect(withdrawalLimitProblem(200_000_000, 100_000_000, quote)).toMatch(/available balance of 100.00 USDT/);
  });

  it('applies min, max and the daily cap from the quote', () => {
    expect(withdrawalLimitProblem(5_000_000, null, quote)).toMatch(/minimum withdrawal is 10.00 USDT/);
    expect(withdrawalLimitProblem(1_500_000_000, null, quote)).toMatch(/maximum withdrawal is 1,000.00 USDT/);
    expect(withdrawalLimitProblem(600_000_000, null, { ...quote, dailyUsed: 1_500_000_000 })).toMatch(
      /up to 500.00 USDT more today/,
    );
  });

  it('treats a zero limit as no limit', () => {
    expect(withdrawalLimitProblem(5_000_000_000, null, { ...quote, maxWithdrawal: 0, dailyCap: 0 })).toBeNull();
  });

  it('refuses an amount the fee would swallow', () => {
    expect(withdrawalLimitProblem(10_000_000, null, { ...quote, netAmount: 0 })).toMatch(/does not cover/);
  });
});

describe('amountProblem', () => {
  it('says nothing about an empty field', () => {
    expect(amountProblem('', null)).toBeNull();
  });
  it('explains an unreadable or zero amount', () => {
    expect(amountProblem('1,000', null)).toMatch(/up to 6 decimal places/);
    expect(amountProblem('0', 0)).toMatch(/greater than zero/);
    expect(amountProblem('1', 1_000_000)).toBeNull();
  });
});

describe('ledgerSign', () => {
  it('signs credits and debits, and leaves reservations unsigned', () => {
    expect(ledgerSign({ movement: 'CREDIT' })).toBe(1);
    expect(ledgerSign({ movement: 'DEBIT' })).toBe(-1);
    expect(ledgerSign({ movement: 'LOCK' })).toBe(0);
    expect(ledgerSign({ movement: 'UNLOCK' })).toBe(0);
  });
});

describe('usdtErrorMessage', () => {
  it('shows the server message for every known code', () => {
    for (const code of KNOWN_ERROR_CODES) {
      expect(usdtErrorMessage(new ApiRequestError(code, 'From the server: ' + code, 400))).toBe(
        'From the server: ' + code,
      );
    }
  });

  it('covers every code the contract names', () => {
    expect([...KNOWN_ERROR_CODES].sort()).toEqual(
      [
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
      ].sort(),
    );
  });

  it('falls back to its own words when a known code comes without a message', () => {
    expect(usdtErrorMessage(new ApiRequestError('INSUFFICIENT_BALANCE', '  ', 400))).toMatch(/enough available USDT/);
  });

  it('never shows the text of an unknown code', () => {
    expect(usdtErrorMessage(new ApiRequestError('INTERNAL_ERROR', 'mongo: connection refused at 10.0.0.3', 500))).toBe(
      GENERIC_ERROR,
    );
    expect(usdtErrorMessage(new Error('boom'))).toBe(GENERIC_ERROR);
    expect(usdtErrorMessage('nope')).toBe(GENERIC_ERROR);
  });

  it('says the server cannot be reached when it cannot', () => {
    expect(usdtErrorMessage(new ApiRequestError('NETWORK_ERROR', 'Network Error', 0))).toBe(OFFLINE_ERROR);
  });
});

describe('createAttemptKey', () => {
  function counter() {
    let n = 0;
    return () => 'key-' + ++n;
  }

  it('gives the same body the same key, so a retry or double click is one request', () => {
    const attempt = createAttemptKey(counter());
    const first = attempt.for({ network: 'USDTTRC20', amount: 1 });
    expect(attempt.for({ network: 'USDTTRC20', amount: 1 })).toBe(first);
  });

  it('starts a new attempt when the body changes', () => {
    const attempt = createAttemptKey(counter());
    const first = attempt.for({ amount: 1 });
    const second = attempt.for({ amount: 2 });
    expect(second).not.toBe(first);
    // Going back to the first body is a new attempt too, not the old key.
    expect(attempt.for({ amount: 1 })).not.toBe(first);
  });

  it('starts a new attempt after reset, even for an identical body', () => {
    const attempt = createAttemptKey(counter());
    const first = attempt.for({ amount: 1 });
    attempt.reset();
    expect(attempt.for({ amount: 1 })).not.toBe(first);
  });

  it('generates real random keys by default', () => {
    const a = createAttemptKey().for({ amount: 1 });
    const b = createAttemptKey().for({ amount: 1 });
    expect(a).toMatch(/^[0-9a-f-]{32,36}$/);
    expect(a).not.toBe(b);
  });
});

describe('safeExternalUrl', () => {
  it('passes ordinary web addresses', () => {
    expect(safeExternalUrl('https://pay.example.com/i/abc')).toBe('https://pay.example.com/i/abc');
  });
  it('refuses anything that is not http(s)', () => {
    expect(safeExternalUrl('javascript:alert(1)')).toBeNull();
    expect(safeExternalUrl('data:text/html,hi')).toBeNull();
    expect(safeExternalUrl('/relative')).toBeNull();
    expect(safeExternalUrl(null)).toBeNull();
  });
});
