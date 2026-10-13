import { isValidElement, type ReactNode } from 'react';

/**
 * Plain-text search over short help content (Learn, reference record 19 -
 * Olymptrade's "Find the help you need").
 *
 * The guides are written as JSX so they can carry emphasis; searching them
 * reads the words back out of the same tree rather than keeping a second,
 * plain copy that would drift from what the page says.
 */

/** The words a piece of JSX says, in order. */
export function nodeText(node: ReactNode): string {
  if (node === null || node === undefined || typeof node === 'boolean') return '';
  if (typeof node === 'string' || typeof node === 'number') return String(node);
  if (Array.isArray(node)) return node.map(nodeText).join(' ');
  if (isValidElement(node)) return nodeText((node.props as { children?: ReactNode }).children);
  return '';
}

/** Lower case, accents and curly quotes folded, split into words. */
export function words(text: string): string[] {
  return text
    .toLowerCase()
    .normalize('NFKD')
    .replace(/[̀-ͯ]/g, '')
    .replace(/[‘’]/g, "'")
    .split(/[^\p{L}\p{N}]+/u)
    .filter(Boolean);
}

/**
 * Whether every word of the query starts a word in the text, in any order.
 *
 * Word starts rather than any substring: "draw" should find the draw rule, not
 * every guide that mentions a withdrawal. A prefix still lets a half-typed
 * word match ("refun" finds "refunded"). An empty query matches everything.
 */
export function matchesQuery(text: string, query: string): boolean {
  const terms = words(query);
  if (terms.length === 0) return true;
  const haystack = words(text);
  return terms.every((term) => haystack.some((word) => word.startsWith(term)));
}
