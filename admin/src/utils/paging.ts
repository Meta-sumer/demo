import type { Paginated } from '../types/api';

/**
 * Normalises a list response into the console's Paginated shape.
 *
 * Older endpoints put `{items, page, limit, total, totalPages}` inside `data`;
 * newer ones return the rows as `data` and the page figures in `meta`. Both are
 * accepted so a screen does not care which style a route uses.
 */
export function toPaginated<T>(
  data: unknown,
  meta?: Record<string, unknown> | null,
  requested?: { page?: number; limit?: number },
): Paginated<T> {
  const num = (value: unknown, fallback: number) =>
    typeof value === 'number' && Number.isFinite(value) ? value : fallback;

  if (data && !Array.isArray(data) && typeof data === 'object' && Array.isArray((data as { items?: unknown }).items)) {
    const page = data as Partial<Paginated<T>> & { items: T[] };
    const limit = num(page.limit, requested?.limit ?? page.items.length);
    const total = num(page.total, page.items.length);
    return {
      items: page.items,
      page: num(page.page, requested?.page ?? 1),
      limit,
      total,
      totalPages: num(page.totalPages, Math.max(1, Math.ceil(total / Math.max(1, limit)))),
    };
  }

  const items = Array.isArray(data) ? (data as T[]) : [];
  const source = (meta && typeof meta.pagination === 'object' && meta.pagination ? meta.pagination : meta ?? {}) as Record<
    string,
    unknown
  >;
  const limit = num(source.limit, requested?.limit ?? items.length);
  const total = num(source.total, items.length);
  return {
    items,
    page: num(source.page, requested?.page ?? 1),
    limit,
    total,
    totalPages: num(source.totalPages, Math.max(1, Math.ceil(total / Math.max(1, limit)))),
  };
}
