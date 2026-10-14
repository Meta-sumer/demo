/**
 * Where the trading server lives.
 *
 * The web client reaches it through the Vite proxy at `/api`; a phone has no
 * such origin, so the address is absolute. By default the app talks to the
 * deployed API on Render. To develop against a server on your own machine,
 * set `EXPO_PUBLIC_API_URL` in `mobile/.env.local` (see .env.example) -
 * with an emulator and `adb reverse tcp:4000 tcp:4000` that is
 * `http://localhost:4000`.
 */
const DEPLOYED_API_URL = 'https://binary-trading-api-ih4v.onrender.com';

function trimSlash(value: string): string {
  return value.replace(/\/+$/, '');
}

export const API_URL = trimSlash(process.env.EXPO_PUBLIC_API_URL || DEPLOYED_API_URL);

/** Defaults to the API's own host with ws(s):// and the `/ws` path. */
export const WS_URL = trimSlash(process.env.EXPO_PUBLIC_WS_URL || API_URL.replace(/^http/, 'ws') + '/ws');

/** The app's own deep-link scheme (app.json `scheme`). */
export const APP_SCHEME = 'binarytrading';
