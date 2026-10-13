import Constants from 'expo-constants';

/**
 * Where the trading server lives.
 *
 * The web client reaches it through the Vite proxy at `/api`; a phone has no
 * such origin, so the address is absolute. Set `EXPO_PUBLIC_API_URL` and
 * `EXPO_PUBLIC_WS_URL` (see .env.example) for anything other than local
 * development.
 *
 * Without them, development falls back to the machine running Metro: the
 * Expo dev server's own host (e.g. 192.168.1.5:8081) with the API's port.
 * That one address works from a phone on the same Wi-Fi and from an emulator,
 * where `localhost` would be the device itself.
 */
const API_PORT = 4000;

function devHost(): string {
  const hostUri = Constants.expoConfig?.hostUri ?? '';
  const host = hostUri.split(':')[0];
  return host || 'localhost';
}

function trimSlash(value: string): string {
  return value.replace(/\/+$/, '');
}

export const API_URL = trimSlash(process.env.EXPO_PUBLIC_API_URL || 'http://' + devHost() + ':' + API_PORT);

export const WS_URL = trimSlash(
  process.env.EXPO_PUBLIC_WS_URL || API_URL.replace(/^http/, 'ws') + '/ws',
);

/** The app's own deep-link scheme (app.json `scheme`). */
export const APP_SCHEME = 'binarytrading';
