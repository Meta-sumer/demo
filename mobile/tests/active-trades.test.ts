import { beforeEach, describe, expect, it } from 'vitest';
import { useTradeStore } from '../src/stores/tradeStore';
import { useAuthStore } from '../src/stores/authStore';
import { countOpenFor } from '../src/features/trade/useActiveTrades';
import type { Trade, User } from '../src/types/api';

/**
 * The navigation's count of running trades, and the store behind it - which
 * is now read on every screen, so it must never carry one person's positions
 * into another person's session.
 */
function trade(id: string, extra: Partial<Trade> = {}): Trade {
  return {
    id,
    reference: 'BT_' + id,
    userId: 'u',
    accountType: 'DEMO',
    assetId: 'btc',
    symbol: 'BTC/USD',
    direction: 'UP',
    stake: 100_00,
    currency: 'INR',
    payoutPercent: 82,
    profit: 82_00,
    potentialReturn: 182_00,
    entryPrice: 1,
    expiryPrice: null,
    entryTimestamp: 0,
    expiryTimestamp: 60_000,
    status: 'OPEN',
    result: null,
    settlementAmount: null,
    ip: null,
    settledAt: null,
    createdAt: '',
    ...extra,
  };
}

const user = (id: string) => ({ id }) as User;

describe('countOpenFor', () => {
  it('counts the running trades of one account only', () => {
    const trades = [trade('a'), trade('b', { accountType: 'LIVE' }), trade('c')];
    expect(countOpenFor(trades, 'DEMO')).toBe(2);
    expect(countOpenFor(trades, 'LIVE')).toBe(1);
  });

  it('treats a trade from before accounts existed as live', () => {
    const legacy = trade('a', { accountType: undefined as unknown as Trade['accountType'] });
    expect(countOpenFor([legacy], 'LIVE')).toBe(1);
    expect(countOpenFor([legacy], 'DEMO')).toBe(0);
  });
});

describe('trade store and the session', () => {
  beforeEach(() => {
    useAuthStore.setState({ status: 'authenticated', user: user('one') });
    useTradeStore.getState().setOpenTrades([trade('a'), trade('b')]);
  });

  it('keeps only running trades', () => {
    useTradeStore.getState().setOpenTrades([trade('a'), trade('b', { status: 'WON' })]);
    expect(useTradeStore.getState().openTrades.map((t) => t.id)).toEqual(['a']);
  });

  it('empties when the person signs out', () => {
    useAuthStore.setState({ status: 'anonymous', user: null });
    expect(useTradeStore.getState().openTrades).toEqual([]);
  });

  it('empties when someone else signs in', () => {
    useAuthStore.setState({ user: user('two') });
    expect(useTradeStore.getState().openTrades).toEqual([]);
  });

  it('survives an update to the same person, such as a profile edit', () => {
    useAuthStore.setState({ user: { ...user('one'), fullName: 'New Name' } });
    expect(useTradeStore.getState().openTrades).toHaveLength(2);
  });
});
