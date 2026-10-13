const path = require('path');
const { getDefaultConfig } = require('expo/metro-config');
const { withNativeWind } = require('nativewind/metro');

const config = getDefaultConfig(__dirname);

/*
 * The repository root is an npm workspace (client, admin) with its own
 * node_modules holding React 18 for the web apps. Metro walks up directories
 * when a package is missing here, and a module picked up from there would load
 * a second React. Everything in the parent's node_modules is blocked, so a
 * missing dependency fails loudly instead.
 */
const parentModules = path.resolve(__dirname, '..', 'node_modules');
const pattern = parentModules
  .split(/[\\/]/)
  .map((part) => part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))
  .join('[\\\\/]');
const existing = config.resolver.blockList;
config.resolver.blockList = [
  ...(Array.isArray(existing) ? existing : existing ? [existing] : []),
  new RegExp('^' + pattern + '[\\\\/].*'),
];
config.watchFolders = [__dirname];

module.exports = withNativeWind(config, { input: './global.css' });
