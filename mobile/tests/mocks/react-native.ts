/** The few React Native APIs that store and util modules touch at import time. */
export const Appearance = {
  getColorScheme: () => 'dark' as const,
  addChangeListener: () => ({ remove: () => undefined }),
};
export const Platform = { OS: 'android', select: <T,>(spec: { default?: T; android?: T }) => spec.android ?? spec.default };
