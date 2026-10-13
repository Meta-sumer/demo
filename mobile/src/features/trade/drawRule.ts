import { useQuery } from '@tanstack/react-query';
import { api } from '../../services/api';
import type { Asset, DrawPolicy } from '../../types/api';

/**
 * What happens when the price at expiry is exactly the entry price.
 *
 * The platform's risk configuration decides it: by default the trade is a draw
 * and the investment is refunded, and an operator can make a tie count as a
 * loss or as a win instead. The server applies whichever is configured when it
 * settles, and sends it with every asset, so everything the app says about a
 * tie - the guide, the "?" on the contract terms, "If it ends now" - states
 * the rule the trade will actually be settled by rather than assuming one.
 *
 * The DRAW status itself only exists under REFUND: under the other two rules a
 * tie settles as WON or LOST.
 */
export function drawPolicyOf(asset: Pick<Asset, 'drawPolicy'> | null | undefined): DrawPolicy {
  return asset?.drawPolicy ?? 'REFUND';
}

/** The rule as a sentence of its own. */
export const TIE_SENTENCE: Record<DrawPolicy, string> = {
  REFUND: 'The same price is a draw, and your investment is refunded.',
  LOSS: 'The same price counts as a loss, whichever way you traded.',
  WIN: 'The same price counts as a win, whichever way you traded.',
};

/** The rule to end a sentence: "If it is exactly the same, …". */
export const TIE_CLAUSE: Record<DrawPolicy, string> = {
  REFUND: 'it is a draw and your investment is refunded',
  LOSS: 'it counts as a loss',
  WIN: 'it counts as a win',
};

/** Short label for a diagram: "same price = draw". */
export const TIE_LABEL: Record<DrawPolicy, string> = {
  REFUND: 'draw',
  LOSS: 'loss',
  WIN: 'win',
};

/**
 * The rule for an asset, or the platform's when none is named. It shares the
 * ['assets'] query every trading screen already runs, so it costs no request.
 */
export function useDrawPolicy(assetId?: string): DrawPolicy {
  const { data } = useQuery({ queryKey: ['assets'], queryFn: api.assets.list, staleTime: 60_000 });
  const asset = (assetId ? data?.find((a) => a.id === assetId) : undefined) ?? data?.[0];
  return drawPolicyOf(asset);
}
