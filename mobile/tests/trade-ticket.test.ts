import { beforeEach, describe, expect, it } from 'vitest';
import {
  accountOf,
  amountMessage,
  amountPresets,
  amountState,
  amountStep,
  amountText,
  contractTerms,
  durationChips,
  parseAmount,
  pickDuration,
  stakeLimits,
  stepAmount,
} from '../src/features/trade/ticketModel';
import { useTradeResultStore } from '../src/stores/tradeResultStore';
import type { Trade } from '../src/types/api';

/**
 * The ticket's rules. These decide which amounts and durations a trader can
 * pick and what the ticket says is wrong - get one wrong and the ticket either
 * blocks a valid trade or offers one the server will refuse.
 */
const limits = { min: 100_00, max: 10_000_00 };

describe('parseAmount', () => {
  it('reads plain, decimal and grouped amounts as minor units', () => {
    expect(parseAmount('500')).toBe(500_00);
    expect(parseAmount('250.5')).toBe(250_50);
    expect(parseAmount('1,000')).toBe(1000_00);
  });

  it('refuses text that is not money', () => {
    expect(parseAmount('')).toBeNull();
    expect(parseAmount('abc')).toBeNull();
    expect(parseAmount('1.2.3')).toBeNull();
    expect(parseAmount('10.555')).toBeNull();
    expect(parseAmount('.')).toBeNull();
    expect(parseAmount('-5')).toBeNull();
  });
});

describe('amountState', () => {
  it('names each state in the order a trader can fix them', () => {
    expect(amountState('', limits, 1000_00).kind).toBe('empty');
    expect(amountState('  ', limits, 1000_00).kind).toBe('empty');
    expect(amountState('x', limits, 1000_00).kind).toBe('invalid');
    expect(amountState('0', limits, 1000_00).kind).toBe('invalid');
    expect(amountState('50', limits, 1000_00)).toEqual({ kind: 'below-min', min: 100_00 });
    expect(amountState('20000', limits, 1_000_000_00)).toEqual({ kind: 'above-max', max: 10_000_00 });
    expect(amountState('500', limits, 200_00)).toEqual({ kind: 'insufficient', available: 200_00 });
    expect(amountState('500', limits, 500_00)).toEqual({ kind: 'valid', value: 500_00 });
  });

  it('reports the limits before the balance', () => {
    // Below the minimum and more than the balance: the minimum is the problem.
    expect(amountState('50', limits, 10_00).kind).toBe('below-min');
  });

  it('does not block on the balance before it has loaded', () => {
    expect(amountState('500', limits, null).kind).toBe('valid');
  });

  it('writes a message for every state but valid', () => {
    expect(amountMessage({ kind: 'empty' })).toBe('Enter an amount');
    expect(amountMessage({ kind: 'invalid' })).toMatch(/valid amount/);
    expect(amountMessage({ kind: 'below-min', min: 100_00 })).toMatch(/Minimum investment is ₹100\.00/);
    expect(amountMessage({ kind: 'above-max', max: 10_000_00 })).toMatch(/Maximum investment is/);
    expect(amountMessage({ kind: 'insufficient', available: 5_00 })).toMatch(/Not enough balance/);
    expect(amountMessage({ kind: 'valid', value: 1 })).toBeNull();
  });
});

describe('amountPresets', () => {
  it('offers only the presets inside the limits', () => {
    const presets = amountPresets({ min: 200_00, max: 2000_00 }, null);
    expect(presets.map((p) => p.value)).toEqual([500_00, 1000_00]);
  });

  it('adds Max as the smaller of the balance and the maximum stake', () => {
    expect(amountPresets(limits, 750_00).at(-1)).toEqual({ value: 750_00, isMax: true });
    expect(amountPresets(limits, 50_000_00).at(-1)).toEqual({ value: 10_000_00, isMax: true });
  });

  it('leaves Max out when the balance cannot cover the minimum', () => {
    expect(amountPresets(limits, 50_00).some((p) => p.isMax)).toBe(false);
  });

  it('does not repeat a preset as Max', () => {
    const presets = amountPresets(limits, 1000_00);
    expect(presets.filter((p) => p.value === 1000_00)).toHaveLength(1);
  });
});

describe('stepping', () => {
  it('clamps to the limits', () => {
    expect(stepAmount(100_00, -100_00, limits)).toBe(100_00);
    expect(stepAmount(9_950_00, 100_00, limits)).toBe(10_000_00);
    expect(stepAmount(null, 100_00, limits)).toBe(200_00);
  });

  it('takes bigger steps for bigger amounts', () => {
    expect(amountStep(500_00)).toBe(100_00);
    expect(amountStep(2000_00)).toBe(500_00);
    expect(amountStep(20_000_00)).toBe(1000_00);
  });

  it('writes amounts back without trailing zeros', () => {
    expect(amountText(500_00)).toBe('500');
    expect(amountText(250_50)).toBe('250.50');
  });
});

describe('durations', () => {
  const asset = {
    supportedExpiries: [300, 30, 60],
    payouts: [
      { id: 'a', assetId: 'x', expirySeconds: 30, payoutPercent: 80, minStake: null, maxStake: null, version: 1 },
      { id: 'b', assetId: 'x', expirySeconds: 60, payoutPercent: 85, minStake: 200_00, maxStake: null, version: 1 },
    ],
  };

  it('lists every supported duration, shortest first, with its payout', () => {
    const chips = durationChips(asset);
    expect(chips.map((c) => c.label)).toEqual(['30s', '1m', '5m']);
    expect(chips.map((c) => c.payoutPercent)).toEqual([80, 85, null]);
  });

  it('disables a duration without a payout, with the reason', () => {
    const chips = durationChips(asset);
    expect(chips[2]!.unavailableReason).toBe('No payout for 5m');
    expect(chips[0]!.unavailableReason).toBeNull();
  });

  it('keeps a usable choice and replaces an unusable one', () => {
    const chips = durationChips(asset);
    expect(pickDuration(chips, 60)).toBe(60);
    expect(pickDuration(chips, 300)).toBe(30);
    expect(pickDuration(chips, 900)).toBe(30);
  });

  it('takes the tighter of the asset and payout limits', () => {
    expect(stakeLimits({ minStake: 100_00, maxStake: 5000_00 }, asset.payouts[1]!)).toEqual({
      min: 200_00,
      max: 5000_00,
    });
  });
});

describe('contractTerms', () => {
  it('separates profit, total return and the maximum loss', () => {
    expect(contractTerms(500_00, 84)).toEqual({ profit: 420_00, totalReturn: 920_00, maxLoss: 500_00 });
    expect(contractTerms(null, 84)).toBeNull();
    expect(contractTerms(500_00, null)).toBeNull();
  });
});

function trade(id: string, extra: Partial<Trade> = {}): Trade {
  return {
    id,
    reference: 'BT_' + id,
    userId: 'u',
    accountType: 'LIVE',
    assetId: 'btc',
    symbol: 'BTC/USD',
    direction: 'UP',
    stake: 100_00,
    currency: 'INR',
    payoutPercent: 82,
    profit: 82_00,
    potentialReturn: 182_00,
    entryPrice: 1,
    expiryPrice: 2,
    entryTimestamp: 0,
    expiryTimestamp: 30_000,
    status: 'WON',
    result: 'WIN',
    settlementAmount: 82_00,
    ip: null,
    settledAt: null,
    createdAt: '',
    ...extra,
  };
}

describe('accountOf', () => {
  it('treats a trade without an account as live', () => {
    expect(accountOf(trade('a', { accountType: undefined as unknown as Trade['accountType'] }))).toBe('LIVE');
    expect(accountOf(trade('a', { accountType: 'DEMO' }))).toBe('DEMO');
  });
});

describe('tradeResultStore', () => {
  beforeEach(() => useTradeResultStore.getState().reset());

  it('keeps results newest first and never shows one twice', () => {
    const store = useTradeResultStore.getState();
    store.announce(trade('1'));
    store.announce(trade('2'));
    store.announce(trade('1'));
    expect(useTradeResultStore.getState().results.map((t) => t.id)).toEqual(['2', '1']);
  });

  it('does not bring back a dismissed result when the event replays', () => {
    const store = useTradeResultStore.getState();
    store.announce(trade('1'));
    store.dismiss('1');
    store.announce(trade('1'));
    expect(useTradeResultStore.getState().results).toHaveLength(0);
  });
});
