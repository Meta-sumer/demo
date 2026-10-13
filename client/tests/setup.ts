/**
 * Test environment shims.
 *
 * The stores run under zustand's `persist` middleware, which looks for
 * `localStorage`. The tests run in node, where there is none, so persist logs a
 * warning on every write. Left alone that noise buries the warnings worth
 * reading, so the tests get a real in-memory implementation instead.
 */
const store = new Map<string, string>();

const memoryStorage: Storage = {
  get length() {
    return store.size;
  },
  clear: () => store.clear(),
  getItem: (key) => store.get(key) ?? null,
  key: (index) => [...store.keys()][index] ?? null,
  removeItem: (key) => void store.delete(key),
  setItem: (key, value) => void store.set(key, String(value)),
};

// Node 22+ ships its own read-only `localStorage` binding, so a plain
// assignment can be silently ignored - define the property instead.
for (const name of ['localStorage', 'sessionStorage'] as const) {
  Object.defineProperty(globalThis, name, {
    value: memoryStorage,
    configurable: true,
    writable: true,
  });
}

// zustand's persist middleware probes for a browser-like global before it will
// use the storage at all.
if (!('window' in globalThis)) {
  Object.defineProperty(globalThis, 'window', {
    value: globalThis,
    configurable: true,
    writable: true,
  });
}
