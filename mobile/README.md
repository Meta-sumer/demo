# Binary Trading - mobile app

The trading client as a native app for Android and iOS: Expo SDK 54, React
Native 0.81 (new architecture), expo-router, NativeWind and a Skia chart. It is
a port of `../client` (the web app) and talks to the same Go server in
`../server-go` - the server still decides every price, time and payout.

## Run it

```bash
cd mobile
npm install          # this folder is NOT part of the root npm workspace
npm start            # Metro; press `a` for Android, or scan the QR code with Expo Go (SDK 54)
```

By default the app talks to the production server at
`https://wintratrade.online/api` (websocket `wss://wintratrade.online/ws`).

To use a server on your own machine instead, create `mobile/.env.local`:

```bash
# Android emulator (run `adb reverse tcp:4000 tcp:4000` once per boot)
EXPO_PUBLIC_API_URL=http://localhost:4000
# or a phone on the same Wi-Fi
EXPO_PUBLIC_API_URL=http://192.168.x.x:4000
```

and start the backend from the repo root (`npm run infra:up`, `npm run seed`,
`npm run dev:server`). Restart Metro with `npx expo start -c` after changing env.

Demo sign-in (seeded): `trader@binary.local` / `Trader@12345`.

## Build an installable app

```bash
npx eas-cli@latest login
npx eas-cli@latest build -p android --profile preview      # APK to sideload
npx eas-cli@latest build -p android --profile production   # AAB for Play Store
```

Profiles live in `eas.json`; both point at the deployed API.

## Checks

```bash
npm run typecheck    # tsc --noEmit
npm test             # vitest: the web client's pure-logic suites, plus a check
                     # that the app's palette matches the web stylesheet
```

## Layout

```
src/app/            routes (expo-router) - thin files that render a screen
src/features/       screens and their parts, by area (trade, trades, home, usdt, ...)
src/components/     shared pieces; components/ui is the UI kit (Text, Button, Sheet, Field, ...)
src/stores/         zustand stores (persisted ones use AsyncStorage)
src/services/       HTTP client (tokens in SecureStore) and the API map
src/websocket/      live prices, trades, balances and support messages
src/theme/          the three themes' tokens - the same values as client/src/index.css
src/utils/          money, time, indicators, CSV ... carried over from the web
tests/              vitest suites (node), with small stand-ins for Expo modules
```

`PORTING.md` is the contract the port was written to: how web elements,
classes and APIs map to React Native here.

## Differences from the web client

- Phone layout only: desktop-only features (two charts side by side, the icon
  rail, wide tables) are not carried over.
- Deposit proof is an image from the photo library; the web also accepted PDF.
- Dates (date of birth, history filter) use in-app pickers, not a native one.
- CSV export opens the share sheet instead of downloading.
