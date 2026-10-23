import { describe, expect, it } from 'vitest';
import {
  DEFAULT_EXPIRIES,
  LAUNCH_MARKETS,
  expiryLabel,
  landingBoard,
  payoutText,
  tickerPayout,
} from '../src/features/landing/markets';
import type { PublicMarkets } from '../src/types/api';

/**
 * The landing page's market list: what `GET /public/markets` becomes on the
 * page, and what the page shows while it has no answer - the instruments,
 * and not one invented figure.
 */
function board(markets: PublicMarkets['markets']): PublicMarkets {
  return {
    markets,
    minPayoutPercent: null,
    maxPayoutPercent: null,
    payoutBasis: 'LOWER_OF_UP_AND_DOWN',
    generatedAt: '2026-10-23T10:00:00.000Z',
    maxAgeSeconds: 5,
  };
}

const BTC = {
  symbol: 'BTC/USD',
  name: 'Bitcoin / US Dollar',
  category: 'CRYPTO',
  payouts: [
    { expirySeconds: 300, payoutPercent: 80.5 },
    { expirySeconds: 30, payoutPercent: 74 },
    { expirySeconds: 60, payoutPercent: 77.5 },
  ],
  minPayoutPercent: 74,
  maxPayoutPercent: 80.5,
};

const ETH = {
  symbol: 'ETH/USD',
  name: 'Ethereum / US Dollar',
  category: 'CRYPTO',
  payouts: [
    { expirySeconds: 60, payoutPercent: 76 },
    { expirySeconds: 900, payoutPercent: 81 },
  ],
  minPayoutPercent: 76,
  maxPayoutPercent: 81,
};

describe('landing market list', () => {
  it('maps the public board to rows, columns and the best figure', () => {
    const view = landingBoard(board([BTC, ETH]));
    expect(view.markets).toEqual([
      { symbol: 'BTC/USD', name: 'Bitcoin', kind: 'Crypto', payouts: { 30: 74, 60: 77.5, 300: 80.5 } },
      { symbol: 'ETH/USD', name: 'Ethereum', kind: 'Crypto', payouts: { 60: 76, 900: 81 } },
    ]);
    expect(view.expiries).toEqual([30, 60, 300, 900]);
    expect(view.best).toBe(81);
  });

  it('lists the launch instruments without figures while loading or after a failure', () => {
    for (const data of [undefined, null, board([])]) {
      const view = landingBoard(data);
      expect(view.markets).toBe(LAUNCH_MARKETS);
      expect(view.markets.map((m) => m.symbol)).toEqual(['BTC/USD', 'ETH/USD']);
      expect(view.markets.every((m) => Object.keys(m.payouts).length === 0)).toBe(true);
      expect(view.expiries).toEqual(DEFAULT_EXPIRIES);
      expect(view.best).toBeNull();
    }
  });

  it('shows an instrument the engine cannot price right now without figures', () => {
    const halted = { ...ETH, payouts: [], minPayoutPercent: null, maxPayoutPercent: null };
    const view = landingBoard(board([BTC, halted]));
    expect(view.markets[1]).toEqual({ symbol: 'ETH/USD', name: 'Ethereum', kind: 'Crypto', payouts: {} });
    expect(view.best).toBe(80.5);

    const none = landingBoard(board([halted]));
    expect(none.markets.map((m) => m.symbol)).toEqual(['ETH/USD']);
    expect(none.expiries).toEqual(DEFAULT_EXPIRIES);
    expect(none.best).toBeNull();
  });

  it('drops a figure that is not a positive number', () => {
    const odd = {
      ...BTC,
      payouts: [
        { expirySeconds: 60, payoutPercent: 0 },
        { expirySeconds: 30, payoutPercent: Number.NaN },
      ],
    };
    const view = landingBoard(board([odd]));
    expect(view.markets[0]!.payouts).toEqual({});
    expect(view.best).toBeNull();
  });

  it('keeps names and kinds readable', () => {
    const view = landingBoard(
      board([
        { ...BTC, name: 'Bitcoin', category: 'CRYPTO' },
        { ...ETH, symbol: 'XAU/USD', name: '', category: 'COMMODITY' },
        { ...ETH, symbol: 'ABC/USD', name: 'Abc / US Dollar', category: 'WIDGET' },
      ]),
    );
    expect(view.markets.map((m) => [m.name, m.kind])).toEqual([
      ['Bitcoin', 'Crypto'],
      ['XAU/USD', 'Commodities'],
      ['Abc', 'Widget'],
    ]);
  });

  it('puts the 1 minute payout on the ticker, else the shortest expiry', () => {
    const longOnly = {
      ...ETH,
      payouts: [
        { expirySeconds: 900, payoutPercent: 81 },
        { expirySeconds: 300, payoutPercent: 79 },
      ],
    };
    const [btc, eth] = landingBoard(board([BTC, longOnly])).markets;
    expect(tickerPayout(btc!)).toBe(77.5);
    expect(tickerPayout(eth!)).toBe(79);
    expect(tickerPayout(LAUNCH_MARKETS[0]!)).toBeUndefined();
  });

  it('never shows a payout higher than the engine quoted', () => {
    expect(payoutText(78)).toBe('78%');
    expect(payoutText(78.5)).toBe('78.5%');
    expect(payoutText(78.25)).toBe('78.2%');
    expect(payoutText(80.99)).toBe('80.9%');
    expect(payoutText(72.000001)).toBe('72%');
  });

  it('labels expiry columns', () => {
    expect([30, 60, 90, 300, 900, 1800, 3600, 5400].map(expiryLabel)).toEqual([
      '30 sec',
      '1 min',
      '1 min 30 sec',
      '5 min',
      '15 min',
      '30 min',
      '1 hour',
      '1 hour 30 min',
    ]);
  });
});
