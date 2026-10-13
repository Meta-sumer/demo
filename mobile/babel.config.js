module.exports = function (api) {
  api.cache(true);
  return {
    // babel-preset-expo adds the worklets plugin Reanimated needs on its own.
    presets: [['babel-preset-expo', { jsxImportSource: 'nativewind' }], 'nativewind/babel'],
  };
};
