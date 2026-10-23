import type { Asset, Direction, EntryOffer, EntryQuote } from '../../types/api';

/**
 * The live payout on the ticket (the server's pricing engine).
 *
 * The ticket fetches offers - one per direction, each locking a payout for a
 * few seconds and usable once - every second while it is open, shows them,
 * and sends the chosen direction's offer with the order. If the offer has
 * expired, was used, or the price moved too far since, the server refuses
 * with a fresh quote in the error, which the ticket shows for the trader to
 * accept or not.
 */

/** Errors that mean "the terms moved: show the new ones and ask again". */
export const REQUOTE_CODES: readonly string[] = ['OFFER_EXPIRED', 'OFFER_INVALID', 'PAYOUT_CHANGED'];

export function isRequote(code: string | null | undefined): boolean {
  return !!code && REQUOTE_CODES.includes(code);
}

/**
 * Refusals that are not about the trader's terms: a halt, market data that
 * is not fresh, the book's limits. An order that carried an offer is answered
 * with a fresh quote for these too, so the ticket shows what is offered now
 * (or why nothing is) instead of keeping the offer that was refused.
 */
export const PAUSED_CODES: readonly string[] = [
  'TRADING_HALTED',
  'FEED_UNHEALTHY',
  'NO_PRICE_AVAILABLE',
  'EXPOSURE_LIMIT_EXCEEDED',
];

export function isPaused(code: string | null | undefined): boolean {
  return !!code && PAUSED_CODES.includes(code);
}

export function offerFor(quote: EntryQuote | null | undefined, direction: Direction): EntryOffer | null {
  if (!quote) return null;
  return direction === 'UP' ? quote.up : quote.down;
}

/**
 * The payout a direction is offered now: the offer's, or the catalogue's
 * figure until the first quote lands. Null when that side is not offered.
 */
export function sidePayout(
  quote: EntryQuote | null | undefined,
  direction: Direction,
  fallback: number | null,
): number | null {
  const offer = offerFor(quote, direction);
  if (!offer) return fallback;
  return offer.available ? offer.payoutPercent : null;
}

/** One figure for a ticket that shows one: the lower available payout. */
export function shownPayout(quote: EntryQuote | null | undefined, fallback: number | null): number | null {
  if (!quote) return fallback;
  return quote.payoutPercent;
}

/** Why neither direction is offered, or null when one is. */
export function quoteBlockedReason(quote: EntryQuote | null | undefined): string | null {
  if (!quote || quote.up.available || quote.down.available) return null;
  return quote.up.reason ?? quote.down.reason ?? 'This duration cannot be priced right now';
}

/** What the order carries: the offer to accept, and the payout on screen. */
export function shownTerms(
  quote: EntryQuote | null | undefined,
  direction: Direction,
  fallback: number | null,
): { offerId?: string; displayedPayoutPercent?: number } {
  const offer = offerFor(quote, direction);
  const payout = sidePayout(quote, direction, fallback);
  return {
    offerId: offer?.available && offer.offerId ? offer.offerId : undefined,
    displayedPayoutPercent: payout ?? undefined,
  };
}

/** The fresh quote a refused order carries, when it has one. */
export function requoteFrom(details: unknown): EntryQuote | null {
  if (!details || typeof details !== 'object') return null;
  const quote = (details as { quote?: unknown }).quote;
  if (!quote || typeof quote !== 'object') return null;
  const q = quote as Partial<EntryQuote>;
  return q.up && q.down && typeof q.assetId === 'string' ? (q as EntryQuote) : null;
}

/** The catalogue's reason a duration cannot be offered, when it gives one. */
export function unpricedReason(asset: Pick<Asset, 'pricing'> | null, seconds: number): string | null {
  return asset?.pricing?.unavailable.find((entry) => entry.expirySeconds === seconds)?.message ?? null;
}
