import { createElement, Fragment } from 'react';
import { describe, expect, it } from 'vitest';
import { matchesQuery, nodeText, words } from '../src/utils/textSearch';

/**
 * Learn's search. The guides are JSX, so the words are read back out of the
 * same tree the page renders - and matched on word starts, so "draw" finds the
 * draw rule without also finding every mention of a withdrawal.
 */
describe('nodeText', () => {
  it('reads the words out of nested JSX', () => {
    const body = createElement(
      Fragment,
      null,
      createElement('p', null, 'Higher (', createElement('b', null, 'UP'), ') or lower.'),
      createElement('p', null, 'Return = investment + payout. ', 82, '%'),
    );
    const text = nodeText(body);
    expect(text).toContain('UP');
    expect(text).toContain('Return = investment + payout.');
    expect(text).toContain('82');
  });

  it('ignores what renders nothing', () => {
    expect(nodeText(null)).toBe('');
    expect(nodeText(false)).toBe('');
    expect(nodeText(undefined)).toBe('');
  });
});

describe('words', () => {
  it('folds case, accents and punctuation', () => {
    expect(words('Café’s UTR/reference: “one-tap”')).toEqual(['cafe', 's', 'utr', 'reference', 'one', 'tap']);
  });
});

describe('matchesQuery', () => {
  const draw = 'If the closing price is exactly equal to your entry price, the trade is a draw.';
  const withdrawals = 'Deposits and withdrawals are for your Live account only.';

  it('matches every word of the query, in any order', () => {
    expect(matchesQuery(draw, 'draw price')).toBe(true);
    expect(matchesQuery(draw, 'price draw')).toBe(true);
    expect(matchesQuery(draw, 'draw payout')).toBe(false);
  });

  it('matches word starts, not the middle of a word', () => {
    expect(matchesQuery(withdrawals, 'draw')).toBe(false);
    expect(matchesQuery(withdrawals, 'withdraw')).toBe(true);
    expect(matchesQuery(draw, 'clos')).toBe(true);
  });

  it('ignores case and extra spaces, and matches everything when empty', () => {
    expect(matchesQuery(draw, '  DRAW  ')).toBe(true);
    expect(matchesQuery(draw, '')).toBe(true);
    expect(matchesQuery(draw, '   ')).toBe(true);
  });
});
