import type { PublicMarkets } from '../../types/api';

/**
 * The landing page's market list, read off the platform.
 *
 * `GET /public/markets` is the one view of the pricing engine a signed-out
 * visitor can have: the active instruments it prices and, for each expiry,
 * its current indicative payout - the lower of UP and DOWN, the same figure
 * the catalogue shows a trader. Payouts move with the market, so the page
 * polls it rather than printing a rate card.
 *
 * While the first answer is on its way, or when the server cannot be
 * reached, the instruments are listed without a single figure. An invented
 * fallback percentage on a landing page is not a placeholder, it is a claim.
 *
 * Kept free of React and of the HTTP client so the mapping can be tested on
 * its own; the same file is in the web and the mobile app.
 */

export interface LandingMarket {
  symbol: string;
  /** Short display name: "Bitcoin" for "Bitcoin / US Dollar". */
  name: string;
  /** "Crypto", "Forex", ... */
  kind: string;
  /** Percent by expiry seconds; empty when there is no figure to show. */
  payouts: Record<number, number>;
}

export interface LandingBoard {
  markets: LandingMarket[];
  /** The expiry columns, shortest first. */
  expiries: number[];
  /** The highest payout on the board right now; null when there is none. */
  best: number | null;
}

/** How often the page asks again. Figures move with the market. */
export const LANDING_REFRESH_MS = 12_000;

/** The launch instruments, listed while there are no figures to show. */
export const LAUNCH_MARKETS: LandingMarket[] = [
  { symbol: 'BTC/USD', name: 'Bitcoin', kind: 'Crypto', payouts: {} },
  { symbol: 'ETH/USD', name: 'Ethereum', kind: 'Crypto', payouts: {} },
];

/** The expiry columns while there are no figures: the platform's shortest durations. */
export const DEFAULT_EXPIRIES = [30, 60, 300];

const KINDS: Record<string, string> = {
  CRYPTO: 'Crypto',
  FOREX: 'Forex',
  COMMODITY: 'Commodities',
  STOCK: 'Stocks',
  INDEX: 'Indices',
};

function kindOf(category: string): string {
  return KINDS[category] ?? category.charAt(0).toUpperCase() + category.slice(1).toLowerCase();
}

/** "Bitcoin / US Dollar" reads as "Bitcoin" next to its symbol. */
function shortName(name: string, symbol: string): string {
  const head = name.split(' / ')[0]?.trim();
  return head || symbol;
}

/**
 * The board the page draws. `data` is the latest answer, or nothing while it
 * is loading or after a failed refresh - then the launch instruments are
 * listed without figures.
 */
export function landingBoard(data: PublicMarkets | null | undefined): LandingBoard {
  if (!data || !Array.isArray(data.markets) || data.markets.length === 0) {
    return { markets: LAUNCH_MARKETS, expiries: DEFAULT_EXPIRIES, best: null };
  }
  const expiries = new Set<number>();
  const markets: LandingMarket[] = [];
  let best: number | null = null;
  for (const market of data.markets) {
    const payouts: Record<number, number> = {};
    for (const row of market.payouts ?? []) {
      if (!Number.isFinite(row.payoutPercent) || row.payoutPercent <= 0) continue;
      payouts[row.expirySeconds] = row.payoutPercent;
      expiries.add(row.expirySeconds);
      if (best === null || row.payoutPercent > best) best = row.payoutPercent;
    }
    markets.push({
      symbol: market.symbol,
      name: shortName(market.name, market.symbol),
      kind: kindOf(market.category),
      payouts,
    });
  }
  return {
    markets,
    expiries: expiries.size > 0 ? [...expiries].sort((a, b) => a - b) : DEFAULT_EXPIRIES,
    best,
  };
}

/**
 * The one figure the ticker shows for an instrument: its 1 minute payout, or
 * the shortest expiry it is offered at.
 */
export function tickerPayout(market: LandingMarket): number | undefined {
  if (market.payouts[60] !== undefined) return market.payouts[60];
  const offered = Object.keys(market.payouts)
    .map(Number)
    .sort((a, b) => a - b);
  return offered.length > 0 ? market.payouts[offered[0]!] : undefined;
}

/**
 * A payout as text. Rounded down to one decimal: the page must never show a
 * figure higher than the engine's.
 */
export function payoutText(percent: number): string {
  const shown = Math.floor(percent * 10 + 1e-9) / 10;
  return (Number.isInteger(shown) ? String(shown) : shown.toFixed(1)) + '%';
}

/** A column heading: "30 sec", "1 min", "15 min", "1 hour". */
export function expiryLabel(seconds: number): string {
  if (seconds < 60) return seconds + ' sec';
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60);
    const rest = seconds % 60;
    return rest ? minutes + ' min ' + rest + ' sec' : minutes + ' min';
  }
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.round((seconds % 3600) / 60);
  return hours + (hours === 1 ? ' hour' : ' hours') + (minutes ? ' ' + minutes + ' min' : '');
}
