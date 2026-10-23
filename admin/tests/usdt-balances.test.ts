import { describe, expect, it } from 'vitest';
import { normaliseBalances } from '../src/pages/usdt/UsdtDashboard';

describe('normaliseBalances', () => {
  it('reads a {balances: [...]} wrapper as one row per currency (the BeeX shape)', () => {
    const rows = normaliseBalances({
      balances: [
        { currency: 'USDTTRC20', available: '100000.000000', pending: '0.000000', reserve: '0.000000' },
        { currency: 'USDTBSC', available: '250.500000', pending: '1.000000', reserve: '0.000000' },
      ],
    });
    expect(rows.map((row) => row.currency)).toEqual(['USDTTRC20', 'USDTBSC']);
    expect(rows[0].figures).toEqual([
      { label: 'available', text: '100000.000000' },
      { label: 'pending', text: '0.000000' },
      { label: 'reserve', text: '0.000000' },
    ]);
    expect(JSON.stringify(rows)).not.toContain('[object Object]');
  });

  it('still reads a plain map of currency to amount', () => {
    expect(normaliseBalances({ USDTTRC20: '12.5' })).toEqual([
      { currency: 'USDTTRC20', figures: [{ label: '', text: '12.5' }] },
    ]);
  });

  it('reads a bare list of rows', () => {
    expect(normaliseBalances([{ coin: 'USDTBSC', available: '3' }])).toEqual([
      { currency: 'USDTBSC', figures: [{ label: 'available', text: '3' }] },
    ]);
  });
});
