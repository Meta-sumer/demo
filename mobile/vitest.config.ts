import path from 'node:path';
import { defineConfig } from 'vitest/config';

/*
 * The pure-logic suites carried over from the web client, run in node.
 *
 * Modules under test are plain TypeScript, but some import an Expo or React
 * Native package at the top (a persisted store reaches AsyncStorage, the HTTP
 * client reaches SecureStore). Those packages ship untranspiled native code,
 * so they are swapped for small in-memory stand-ins here.
 */
const mock = (name: string) => path.resolve(__dirname, 'tests/mocks', name);

export default defineConfig({
  resolve: {
    alias: {
      '@react-native-async-storage/async-storage': mock('async-storage.ts'),
      'expo-secure-store': mock('secure-store.ts'),
      'expo-crypto': mock('crypto.ts'),
      'expo-constants': mock('constants.ts'),
      'expo-file-system': mock('file-system.ts'),
      'expo-sharing': mock('sharing.ts'),
      'react-native': mock('react-native.ts'),
    },
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
});
