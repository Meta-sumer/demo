/** A fresh random key; falls back to getRandomValues where randomUUID is missing (plain http). */
export function newIdempotencyKey(): string {
  const c = globalThis.crypto;
  if (typeof c?.randomUUID === 'function') return c.randomUUID();
  const bytes = new Uint8Array(16);
  c.getRandomValues(bytes);
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

/**
 * The idempotency key of one attempt.
 *
 * The same request body gets the same key however many times it is sent - a
 * double click, a retry after a timeout - so the server acts on it once.
 * Changing anything in the body starts a new attempt with a new key, and so
 * does `reset`, called once an attempt has succeeded so the next identical
 * request is a new one rather than a replay of the last.
 */
export function createAttemptKey(generate: () => string = newIdempotencyKey) {
  let current: { payload: string; key: string } | null = null;
  return {
    for(body: unknown): string {
      const payload = JSON.stringify(body);
      if (!current || current.payload !== payload) current = { payload, key: generate() };
      return current.key;
    },
    reset(): void {
      current = null;
    },
  };
}
