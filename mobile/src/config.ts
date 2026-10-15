/**
 * Where the trading server lives.
 *
 * The web client reaches it through the Vite proxy at `/api`; a phone has no
 * such origin, so the address is absolute. By default the app talks to the
 * production server at wintratrade.online, where nginx serves the API under
 * `/api` and the websocket at `/ws`. To develop against a server on your own
 * machine, set `EXPO_PUBLIC_API_URL` (and `EXPO_PUBLIC_WS_URL`) in
 * `mobile/.env.local` (see .env.example) - with an emulator and
 * `adb reverse tcp:4000 tcp:4000` that is `http://localhost:4000`.
 */
const PRODUCTION_API_URL = 'https://wintratrade.online/api';
const PRODUCTION_WS_URL = 'wss://wintratrade.online/ws';

function trimSlash(value: string): string {
  return value.replace(/\/+$/, '');
}

export const API_URL = trimSlash(process.env.EXPO_PUBLIC_API_URL || PRODUCTION_API_URL);

/**
 * The websocket. A custom API address without a custom websocket address is a
 * server on its own (local development), where `/ws` sits beside the routes.
 */
export const WS_URL = trimSlash(
  process.env.EXPO_PUBLIC_WS_URL ||
    (process.env.EXPO_PUBLIC_API_URL ? API_URL.replace(/^http/, 'ws') + '/ws' : PRODUCTION_WS_URL),
);

/** The app's own deep-link scheme (app.json `scheme`). */
export const APP_SCHEME = 'binarytrading';
