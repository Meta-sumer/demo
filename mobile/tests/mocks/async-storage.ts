/** In-memory AsyncStorage for the node test run. */
const store = new Map<string, string>();

const AsyncStorage = {
  getItem: async (key: string) => store.get(key) ?? null,
  setItem: async (key: string, value: string) => void store.set(key, value),
  removeItem: async (key: string) => void store.delete(key),
  clear: async () => store.clear(),
  getAllKeys: async () => [...store.keys()],
};

export default AsyncStorage;
