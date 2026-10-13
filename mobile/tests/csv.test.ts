import { describe, expect, it } from 'vitest';
import { escapeCsvValue, toCsv } from '../src/utils/csv';

/**
 * The failure modes of a CSV export are all silent: a stray comma shifts every
 * later column, an unescaped quote breaks the row, and a value starting with
 * `=` runs as a formula the moment someone opens the file. None of those raise
 * an error - they just produce a wrong statement of someone's trading history.
 */
describe('escapeCsvValue', () => {
  it('leaves an ordinary value alone', () => {
    expect(escapeCsvValue('BTCUSD')).toBe('BTCUSD');
    expect(escapeCsvValue(1234)).toBe('1234');
  });

  it('renders null and undefined as empty, not as the word', () => {
    expect(escapeCsvValue(null)).toBe('');
    expect(escapeCsvValue(undefined)).toBe('');
  });

  it('quotes a value containing the delimiter', () => {
    expect(escapeCsvValue('Mumbai, India')).toBe('"Mumbai, India"');
  });

  it('doubles embedded quotes', () => {
    expect(escapeCsvValue('He said "hi"')).toBe('"He said ""hi"""');
  });

  it('quotes a value spanning lines', () => {
    expect(escapeCsvValue('line one\nline two')).toBe('"line one\nline two"');
    expect(escapeCsvValue('line one\r\nline two')).toBe('"line one\r\nline two"');
  });

  it('neutralises a formula so a spreadsheet reads it as text', () => {
    // Without this, opening the export runs the cell.
    expect(escapeCsvValue('=1+1')).toBe("'=1+1");
    expect(escapeCsvValue('@SUM(A1:A9)')).toBe("'@SUM(A1:A9)");
    expect(escapeCsvValue('+44 20 7946')).toBe("'+44 20 7946");
  });

  it('neutralises a negative-looking value without losing it', () => {
    // A loss is written -500.00; it must survive as text, not become a formula.
    expect(escapeCsvValue('-500.00')).toBe("'-500.00");
  });

  it('quotes a formula that also contains a comma', () => {
    expect(escapeCsvValue('=A1,B1')).toBe('"\'=A1,B1"');
  });
});

describe('toCsv', () => {
  const columns = [
    { header: 'Symbol', value: (row: { symbol: string; stake: number }) => row.symbol },
    { header: 'Stake', value: (row: { symbol: string; stake: number }) => row.stake },
  ];

  it('writes a header even with no rows', () => {
    expect(toCsv([], columns)).toBe('Symbol,Stake');
  });

  it('uses CRLF between records', () => {
    const csv = toCsv([{ symbol: 'BTCUSD', stake: 500 }], columns);

    expect(csv).toBe('Symbol,Stake\r\nBTCUSD,500');
  });

  it('keeps columns aligned when a value contains a comma', () => {
    const csv = toCsv([{ symbol: 'EUR,USD', stake: 500 }], columns);
    const dataLine = csv.split('\r\n')[1]!;

    expect(dataLine).toBe('"EUR,USD",500');
    // Two columns still, despite the comma inside the first.
    expect(dataLine.match(/^("[^"]*"|[^,]*),([^,]*)$/)).not.toBeNull();
  });

  it('escapes a header the same way as a value', () => {
    const csv = toCsv([], [{ header: 'Profit, net', value: () => '' }]);

    expect(csv).toBe('"Profit, net"');
  });

  it('writes one line per row', () => {
    const rows = [
      { symbol: 'A', stake: 1 },
      { symbol: 'B', stake: 2 },
      { symbol: 'C', stake: 3 },
    ];

    expect(toCsv(rows, columns).split('\r\n')).toHaveLength(4);
  });
});
