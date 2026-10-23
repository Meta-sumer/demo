# 04 - Client-side logic: web (`client/src`) and mobile (`mobile/src`)

This document describes the logic that runs in the two customer apps of the platform, and, briefly,
where the admin console mirrors it:

- **Web**: `client/src`, React + Vite + Tailwind, chart on `lightweight-charts`.
- **Mobile**: `mobile/src`, Expo SDK 54 / React Native 0.81 / expo-router 6 / NativeWind 4, chart on
  Skia + Reanimated, app version 1.0.4 (`mobile/app.json`). It is a port of the web client; the port
  contract lives in `mobile/PORTING.md` and the run/build notes in `mobile/README.md`.
- **Admin console**: `admin/src` (section 11 covers only the parts that show what the apps show).

All three talk to the same Go server (`server-go/`). Most non-UI modules are **identical copies** in
the web and mobile trees (see the table in section 12). When this document cites `client/src/...`
without saying otherwise, the same file exists at the same relative path under `mobile/src/`.

> **The governing rule.** The apps never decide a price, a time or a payout. Prices come from the
> server's ticks and candles, expiries from `entryTimestamp`/`expiryTimestamp` on the trade row, and
> payouts from the server's pricing engine: the catalogue's indicative figure (`asset.payouts`) and
> the live per-direction offers of `GET /binary/quotes`. The accepted trade's own `payoutPercent`,
> `profit`, `potentialReturn` and `settlementAmount` are what count. Everything the apps "compute" is
> either a validation that mirrors a server rule (so the trader gets a message before a round trip),
> a projection labelled as one ("If it ends now"), or a drawing aid (indicators, drawings) that is
> never sent anywhere. What an order carries besides its terms is `quotedPrice` (a slippage guard),
> `offerId` (the offer whose payout the server locked) and `displayedPayoutPercent` (the payout on
> screen, used only to refuse a trade priced below it). None of them can raise a payout or move a
> fill (`server-go/internal/service/binary/binary.go` `acceptTrade`; `service/pricing/offers.go`
> `TakeOffer`, `CheckDisplayed`).

Money is integer minor units throughout: INR in paise, USDT in micro-USDT (section 7).

---

## 1. Data layer

### 1.1 HTTP client - `services/http.ts`

An axios instance with `timeout: 15_000` and JSON content type.

- **Base URL**: web uses `import.meta.env.VITE_API_URL ?? '/api'` (Vite proxy in development, nginx
  in production); mobile uses `API_URL` from `mobile/src/config.ts` (absolute, see 1.3).
- **Request interceptor**: attaches `authorization: Bearer <accessToken>` from `loadTokens()`.
- **Success envelope**: the server answers `{ success: true, data }`. The helpers `apiGet`,
  `apiPost(url, body, headers?)`, `apiPatch`, `apiPut`, `postForm` all return `response.data.data`,
  so callers deal in plain data.
- **Error envelope -> `ApiRequestError`**: `{ success: false, error: { code, message, details } }`
  becomes `new ApiRequestError(code, message, status, details)`. Anything without that envelope
  becomes `ApiRequestError('NETWORK_ERROR', ...)`; axios' `"Network Error"` is reworded to
  `"Cannot reach the trading server"`. `status` is `0` when there was no HTTP response. The
  `details` object is what the trade ticket reads a re-quote from (section 4.7).

**Single-flight token refresh on 401.** The response interceptor treats a response as an auth
failure when `status === 401 && error.code !== 'INVALID_CREDENTIALS'` (a wrong password must not
trigger a refresh). If the original request has not been retried (`_retried` flag) and is not
itself `/auth/refresh`:

```
original._retried = true
refreshing = refreshing ?? refreshAccessToken().finally(() => refreshing = null)
token = await refreshing          // every concurrent 401 awaits the same promise
token ? retry original with new Bearer : onUnauthorised()
```

`refreshAccessToken()` posts `{ refreshToken }` to `baseURL + '/auth/refresh'` with a **bare
axios** call (so it cannot recurse into the interceptor), saves the new pair on success and calls
`clearTokens()` on failure. `setUnauthorisedHandler()` is wired at app start (web `App.tsx`, mobile
`app/_layout.tsx`) to drop the session: `useAuthStore.setState({ status: 'anonymous', user: null,
wallet: null })`, which the route guards turn into a redirect.

**Multipart (`postForm`)**: web deliberately sets `content-type: undefined` so the browser writes
the boundary; mobile sets `multipart/form-data` and `transformRequest: (d) => d`, because React
Native's networking layer writes the boundary for a `FormData` body. Mobile additionally exports
`authenticatedSource(path)` -> `{ uri, headers: { Authorization } }` for authenticated images
(deposit screenshots in `expo-image`).

### 1.2 Token storage

| | Web | Mobile |
|---|---|---|
| Store | `localStorage['bt.tokens']` (JSON `{accessToken, refreshToken}`) | `expo-secure-store` key `bt.tokens` (device keychain) |
| Read | synchronous, every call | from an in-memory mirror `cached` |
| Write | synchronous; ignored on failure (private browsing) | mirror updated synchronously, `setItemAsync` fire-and-forget |
| Init | none | `initTokens()` must run once before `hydrate()` |

On mobile, the request interceptor and the websocket need tokens synchronously, so `initTokens()`
reads SecureStore once into `cached` (guarded by an `initialised` flag) and `loadTokens()` returns
the mirror. `mobile/src/app/_layout.tsx` runs `initTokens().then(hydrate).finally(setBooted)` and
keeps the splash screen up (`SplashScreen.preventAutoHideAsync`) until `booted`, so a signed-in
trader never sees the welcome screen flash.

On the web the tokens sit in `localStorage`, readable by any script on the page. The production
nginx site sends a Content-Security-Policy with `script-src 'self'`
(`deploy/vps/nginx/wintratrade-security-headers.conf`); for that reason the pre-paint theme script
is a file, `client/public/theme-init.js`, not inline (section 9). See
[threat-model.md](../audit/security/threat-model.md) R3.

### 1.3 Mobile API URL and first request

`mobile/src/config.ts`:

- `API_URL = trimSlash(EXPO_PUBLIC_API_URL || 'https://wintratrade.online/api')` (production: nginx
  serves the API under `/api`).
- `WS_URL = trimSlash(EXPO_PUBLIC_WS_URL || (EXPO_PUBLIC_API_URL ? API_URL.replace(/^http/, 'ws') +
  '/ws' : 'wss://wintratrade.online/ws'))`: a custom API address without a custom websocket address
  is a local server with `/ws` beside the routes.
- `APP_SCHEME = 'binarytrading'` (deep links, matches `app.json`).

Two start-up measures for a slow or flaky first request:

1. **Warm-up ping**: at module load of `app/_layout.tsx`, `fetch(API_URL + '/health').catch(() =>
   undefined)` opens DNS/TLS while the splash screen is up. Nothing awaits it.
2. **Hydrate retry that keeps the session** (`mobile/src/stores/authStore.ts` `hydrate`): tries
   `api.auth.me()` up to **2 times**, waiting **1500 ms** after the first failure. A `401`/`403`
   clears the tokens (the session was refused). Any other failure (network, 5xx, timeout) leaves
   the tokens in SecureStore - "unreachable is not refused" - so the next launch can restore the
   session; the in-memory status still ends `anonymous` for this launch. The web `hydrate()`
   (`client/src/stores/authStore.ts`) has no retry and clears the tokens on **any** failure (see
   Known gaps).

### 1.4 Auth store and route gating

`stores/authStore.ts` - status machine `idle -> loading -> authenticated | anonymous`.
`login`/`register` save tokens then set `user` + `wallet`; on error they set `error` and rethrow.
`logout` posts the refresh token, ignores the server's answer, then always clears locally.

- Web: `ProtectedRoute` shows a spinner for `idle|loading`, redirects to `/login` with
  `state.from = pathname + search` otherwise; `PublicOnlyRoute` sends an authenticated session to
  `/home`. Legacy paths redirect (`/dashboard -> /home`, `/assets -> /markets`, `/profile ->
  /account`, `/support -> /account/support`).
- Mobile: `Stack.Protected guard={authenticated}` for `(app)` and `onboarding`, `guard={!authenticated}`
  for `welcome|login|register`; the router moves itself when the status flips. `app/index.tsx`
  redirects to `/home` or `/welcome`. After registration `continueAfterRegistration()`
  (`features/auth/afterRegister.ts`) replaces to `/onboarding` on the next tick unless the device's
  onboarding flag is set.

### 1.5 API map and React Query

`services/api.ts` is the single list of endpoints (`api.auth`, `profile`, `favourites`, `assets`,
`market`, `trades`, `wallet`, `transfers`, `usdt`, `notifications`, `support`). Notable shapes:

- `assets.list()` = `GET /assets` passed through `offerableAssets` (1.6); `assets.get(id)` is not
  filtered.
- `public.markets()` = `GET /public/markets`, the one call that needs no session: the signed-out
  landing page's market list and live payouts (1.7).
- `trades.entryQuote({ assetId, expirySeconds, amount?, accountType? })` = `GET /binary/quotes`:
  one offer per direction (section 4.2).
- `trades.create(input, idempotencyKey)` = `POST /binary/trades` with header `idempotency-key`;
  `input` is `{ assetId, direction, amount, expirySeconds, quotedPrice?, accountType?, offerId?,
  displayedPayoutPercent? }`. `accountType` defaults to LIVE server-side.
- `trades.quote(id)` = `GET /binary/trades/:id/quote` and `trades.closeEarly(id)` = `POST
  /binary/trades/:id/close` (Sell now, section 4.8).
- `market.candles(assetId, limit = 300, interval = 60, to?)`; `market.ticks(assetId, {from, to,
  windowMs})` - the `/ticks` window is capped at 15 minutes server-side
  (`server-go/internal/api/marketdata/routes.go`, `config.TickHistoryRetentionMs`).
- `usdt.*` = `/wallet/usdt/*` (section 8). Calls that move money take an `Idempotency-Key`;
  amounts are integer micro-USDT.
- Mobile `transfers.deposit` takes `proof: UploadFile { uri, name, type }` instead of a `File`
  (the only difference between the two `api.ts` files).

**QueryClient defaults** (identical in `client/src/main.tsx` and `mobile/src/app/_layout.tsx`):
`refetchOnWindowFocus: false` (live data comes over the socket), `staleTime: 5_000`, and
`retry: (n, err) => 4xx ? false : n < 2`.

**Query keys in use** (the invalidation targets matter, because the realtime bridge hits them):

| Key | Used for |
|---|---|
| `['assets']` | asset catalogue; refetched every 4 s on the trade and markets screens (`pages/Trade.tsx`, `pages/Markets.tsx`; mobile `TradeScreen.tsx`, and `MarketsScreen.tsx` while focused); also read by `useDrawPolicy` (`staleTime 60_000`) |
| `['entry-quote', assetId, expirySeconds, stake, account]` | the ticket's live offers, refetched every second (4.2) |
| `['public', 'markets']` | the landing page's market list (1.7), refetched every 12 s (mobile: while the welcome screen is in front) |
| `['wallet', 'accounts']` (`ACCOUNTS_KEY`) | both INR balances; any `['wallet']` invalidation reaches it |
| `['wallet', 'transactions', {...}]` | ledger pages |
| `['trades', 'open']` | open trades (nav count + trade screen) |
| `['trades', filters, page]`, `['trades', 'summary', filters]`, `['trades', 'recent-settled', account]` | history |
| `['trade', id]`, `['trade', id, 'quote']` | trade detail, Sell now quote |
| `['trade-window', id]` | trade detail chart data |
| `['candles', assetId, timeframe]` | main chart history |
| `['sparkline', assetId]` | web market cards (Home, Markets): the last 60 one-minute candles, refetched every 60 s |
| `['market', 'health']`, `['notifications']`, `['transfers']`, `['payment-methods']`, `['profile']` | misc |
| `['usdt', ...]` (`USDT_KEY`, `features/usdt/queries.ts`) | every USDT query under one root |

### 1.6 Asset catalogue and launch scope

Each catalogue row (`GET /assets`, `server-go/internal/api/assets/routes.go` `list`/`detail`)
carries the asset, its `feed` health, `payouts` (one row per expiry the engine can price now, with
`payoutPercent` = the engine's indicative payout: the lower of UP and DOWN at the smallest stake;
`configuredPayoutPercent`, `payoutPercentUp/Down` alongside), a `pricing` block, `drawPolicy` and
`sentiment` (`client/src/types/api.ts` `Asset`, `PayoutConfig`, `AssetPricing`).

The `pricing` block (`server-go/internal/service/pricing/board.go` `CustomerPayouts`) says:

- `priced` / `notPricedReason`: whether the instrument has an enabled pricing rule at all
  (`service/pricing/sigma.go` `SymbolPriced`). Launch scope: only BTC/USD and ETH/USD are priced
  (see [feed-rules.md](../audit/operations/feed-rules.md)).
- `halted` / `haltReason`: the symbol or platform is halted.
- `unavailable[]`: `{ expirySeconds, reason, message }` for each expiry the engine cannot price
  right now. Such an expiry is **left out of `payouts`**, so an older app shows it as unavailable.

**Unpriced assets are never offered.** `components/assetList.ts` `isOfferable(asset)` is
`asset.pricing?.priced !== false` (a server that predates the flag offers everything), and
`offerableAssets` filters a list with it. `services/api.ts` applies it inside `assets.list`, so
every screen that reads `['assets']` - Markets, the Trade screen's asset picker and tabs, Home, the
history filters - sees only offered instruments. Because the workspace prunes ids the catalogue no
longer lists (`workspaceStore.ensureOpen`, 3.6), a tab left open on an unpriced asset disappears on
the next load. Tests: `client|mobile/tests/void-and-scope.test.ts` ("launch scope").

The server applies the same scope independently: the pricing engine refuses an unpriced symbol
with `NOT_PRICED` (`service/pricing/service.go`), so it cannot be quoted or traded, and
`APPLY_LAUNCH_SCOPE=true` switches EUR/USD, GBP/USD and XAU/USD off
(`service/assets/launchscope.go`); the app-side filter only keeps them off the screen.

### 1.7 Landing page market list

The signed-out landing page (web `pages/Landing.tsx`, mobile `features/landing/LandingScreen.tsx`)
has no session, so it cannot read `GET /assets`. It reads `GET /public/markets`
(`server-go/internal/api/public/routes.go`, `service/pricing/public.go`), which needs none:

```json
{ "markets": [ { "symbol": "BTC/USD", "name": "Bitcoin / US Dollar", "category": "CRYPTO",
                 "payouts": [ { "expirySeconds": 30, "payoutPercent": 76.5 },
                              { "expirySeconds": 60, "payoutPercent": 78 } ],
                 "minPayoutPercent": 76.5, "maxPayoutPercent": 78 } ],
  "minPayoutPercent": 76.5, "maxPayoutPercent": 78, "payoutBasis": "LOWER_OF_UP_AND_DOWN",
  "generatedAt": "2026-10-23T10:15:02.123Z", "maxAgeSeconds": 5 }
```

Only active instruments the engine can price are listed (the same `SymbolPriced` rule as 1.6), and
per expiry the catalogue's own indicative figure: the lower of UP and DOWN at the reference stake on
the LIVE book (`CustomerPayouts`). An instrument with nothing priceable right now (a halt, a feed
outage) is listed with `payouts: []` and null min/max. No ids, stakes, exposure, per-direction
figures or reasons. The server rebuilds one snapshot at most every 5 s from the cached indicative
board (`Cache-Control: public, max-age=5`) and limits each address to 300 requests a minute.

`features/landing/useLandingBoard.ts` polls it every 12 s (`LANDING_REFRESH_MS`; mobile only while
the screen is focused) under the key `['public', 'markets']`, and `features/landing/markets.ts`
`landingBoard` turns it into rows (short name, kind, percent by expiry), the expiry columns (the
union of offered expiries) and `best` (the highest figure). The page shows:

- **Live**: the table, the ticker (each instrument's 1-minute figure, else its shortest expiry), "up
  to NN% right now" in the hero and the highlights card. `payoutText` rounds down to one decimal, so
  the page never shows more than the engine quoted.
- **Loading, failed, or no figure at all**: `LAUNCH_MARKETS` (BTC/USD, ETH/USD) or the listed
  instruments with dashes, the ticker without percentages, the hero without "up to", and a note that
  live payouts cannot be shown right now. No fallback percentage exists anywhere in the page. A
  failed refresh hides figures that were already on the page until the next success.

The workspace illustrations (`WorkspaceMock.tsx`) are labelled as illustrations and use example
figures inside the live range (78% on BTC/USD). Test: `client|mobile/tests/landing-markets.test.ts`
(`markets.ts` is the same file in both apps); server: `internal/api/public/routes_test.go`,
`internal/service/pricing/public_test.go`.

---

## 2. Realtime bridge - `hooks/useRealtime.ts` + `websocket/client.ts`

### 2.1 The socket (`websocket/client.ts`)

A singleton `TradingSocket`. URL: web `VITE_WS_URL ?? (wss|ws)://<host>/ws`, mobile `WS_URL`; the
access token goes in the query string (`?token=`). `connect()` is a no-op without a token or when a
socket already exists. The production nginx logs `/ws` without its query string
(`deploy/vps/nginx/wintratrade-logformat.conf` `wt_noquery`), so the token does not reach the
access log ([threat-model.md](../audit/security/threat-model.md) R4).

- **State**: `connecting | open | closed`, derived from `readyState`; `onStateChange` subscribers.
  `features/trade/useConnectionState.ts` reads it through `useSyncExternalStore` so every component
  agrees on the state in the same render (the ticket's banner and its disabled buttons cannot
  disagree).
- **Heartbeat**: on open, `{type:'ping'}` every **25 s**.
- **Reconnect/backoff**: `onclose` (which always follows `onerror`) tears down and, unless
  `closedByUser`, schedules `delay = min(1000 * 2^attempts, 15_000)` ms (`scheduleReconnect`);
  `attempts` resets to 0 on a successful open.
- **Resubscribe**: the set of channels survives a drop; on open the whole set is re-sent as one
  `{type:'subscribe', channels:[...]}`. `subscribe()` only sends channels not already in the set;
  `unsubscribe()` always sends. The set is **not reference-counted** (see 2.5).
- `disconnect()` sets `closedByUser`, cancels the timer, clears the channel set; a socket still
  `CONNECTING` is closed in its `onopen` (avoids a StrictMode console warning on web).
- Frames are `{event, payload}`; handlers are per event plus a `'*'` wildcard.
- Channels: `market:<assetId>`, `user:<userId>`.

### 2.2 What each event updates

`useRealtime()` is mounted once in the signed-in shell (web `components/Layout.tsx`, mobile
`app/(app)/_layout.tsx`). While `status === 'authenticated' && userId`, it connects, subscribes
`user:<id>`, loads favourites from the server, and pre-loads the support thread (best effort).

| Event | Effect |
|---|---|
| `market.tick` | web: `useMarketStore.applyTick(tick)` at once; mobile: batched (2.3) |
| `wallet.updated` | invalidate `USDT_KEY`; if `currency === 'USDT'` **stop** (never file a USDT balance in an INR slot); else default missing `accountType` to `LIVE`, `useAccountStore.upsertWallet`, and patch `ACCOUNTS_KEY` in place (`mergeWallet` replaces the entry of the same type) |
| `usdt.updated` | invalidate `USDT_KEY` (record, lists and balances refetch together) |
| `trade.created` | `useTradeStore.upsertTrade`, invalidate `['trades']` |
| `trade.updated` | `useTradeStore.upsertTrade` |
| `trade.settled` | `useTradeStore.settleTrade`, `announceResult(trade)`, `setQueryData(['trade', id], trade)`, invalidate `['trades']` and `['wallet','transactions']` |
| `support.message` | `useSupportStore.addMessage` |
| `support.conversation` | `useSupportStore.setConversation` |
| `support.typing` | only when `side === 'AGENT'`: `setAgentTyping(name ?? 'Support')` |
| `notification.created` | invalidate `['notifications']` |

The `trade.settled` payload is the server's full trade view
(`server-go/internal/service/settlement/execute.go` emits `models.ToTradeView(trade)`), so it
carries `voided`, `voidReason`, `expiryPriceSource` and `drawPolicy` (section 5.4) and the result
card can name a void at once.

On cleanup (sign-out or user change): unsubscribe handlers, `socket.disconnect()`, reset the
support, account-balance and legacy wallet stores. The **active account choice survives** sign-out
(it is a device preference).

A second effect keeps the legacy single-wallet store (`stores/walletStore.ts`) pointed at the
**active** account's wallet, so older readers follow a DEMO/LIVE switch without knowing about
accounts.

### 2.3 Mobile tick batching (250 ms)

The feed sends up to 20 ticks a second per asset. On a phone, applying each one re-rendered the
ticket, the header and the chart that often; taps then queued behind renders and landed late,
several at once. `mobile/src/hooks/useRealtime.ts` therefore keeps only the newest tick per asset in
a `pendingTicks` map and flushes it at most every `TICK_FLUSH_MS = 250` ms through
`useMarketStore.getState().applyTicks(batch)` (`mobile/src/stores/marketStore.ts` `applyTicks`:
one store update for the whole batch, recording each asset's previous price for the up/down
flash). The pending timer and map are cleared on cleanup. Nothing here decides a price: the server
fills and settles on its own ticks, and the ticket's `quotedPrice` is only a slippage guard. The
web client applies every tick (`applyTick`).

### 2.4 Account loading fallback (`useAccounts`)

Query `ACCOUNTS_KEY`, `staleTime 10_000`, enabled when signed in:

1. `api.wallet.accounts()`, filtered to non-USDT wallets, `accountType ?? 'LIVE'`.
2. On error (older server without `/wallet/accounts`): `api.wallet.get('LIVE')` as a one-element list.

When data lands: `setWallets`, and if the persisted `active` account is not among the returned
types (e.g. DEMO on an old server), `setActive(types[0])` so balances are never blank.

`features/trade/useTradingWallet.ts` resolves the wallet the ticket validates against: account
store -> `['wallet','accounts']` list entry of the active type -> legacy wallet **only if its type
matches**. A live balance must never stand in for a demo one.

`features/trade/useActiveTrades.ts` loads `['trades','open']` from any screen (`staleTime 0`,
`refetchInterval 30_000` as a safety net) and writes it to the trade store only when
`isFetchedAfterMount`, so a previous session's cached list never reaches the nav count.
`countOpenFor(trades, account)` counts with `accountOf(trade)` (`accountType ?? 'LIVE'`).

### 2.5 `useLiveFeeds` subscription rules

`features/trade/useLiveFeeds.ts` subscribes `market:<id>` for every asset with a running trade
while a list of running trades is on screen, so a live result is never computed from a frozen
price. Because socket subscriptions are a plain set (the chart's unmount drops a channel whoever
else wanted it):

- With `keepWorkspace: true` (the trade-screen panel), channels for the workspace's
  `activeAssetId` and `openAssetIds` are **never released** from here.
- Wanted channels are re-asserted every **5 s** (`REASSERT_MS`) and immediately after the chart
  changes `activeAssetId` (that effect runs after the chart's unsubscribe in the same commit).
  Re-asserting is free because `subscribe` only sends missing channels.
- The key is the sorted, de-duplicated asset id list, so re-renders do not churn subscriptions.

`useMarketSubscription(assetId)` (in `useRealtime.ts`) is the chart's own subscribe/unsubscribe.

On the web, the market cards on Home and Markets (`components/MarketCard.tsx` `MarketCardGrid`) use
`useLiveFeeds(ids, { keepWorkspace: false })` for the cards on screen, so their prices stream off the
trade screen too. Those pages are never mounted together with the trade screen or the Trades page,
and React runs the leaving page's unsubscribe before the arriving page subscribes.

---

## 3. Stores (zustand)

All persisted stores use `zustand/middleware` `persist`. Web storage is `localStorage`; mobile
passes `storage: persistStorage` (`mobile/src/stores/storage.ts`, `createJSONStorage(() =>
AsyncStorage)`). AsyncStorage is asynchronous, so a mobile persisted store renders its defaults
for a frame and then rehydrates; nothing persisted gates a server decision.

| Store | Persist key | What persists |
|---|---|---|
| `accountStore` | `bt.account` | `active` only (balances are session data) |
| `settingsStore` | `bt.settings` (version 5) | everything |
| `workspaceStore` | `bt.workspace` | `openAssetIds`, `activeAssetId` |
| `drawingsStore` | `bt.drawings` (version 1) | `drawings` only (mobile's undo history is session-only) |
| `favouritesStore` | `bt.favourites` | `favourites`, `recent` |
| `onboardingStore` | `bt.onboarding` | `completed` |
| `themeStore` | `bt.theme` | web: raw string in localStorage; mobile: `{preference}` via persist |
| `authStore`, `tradeStore`, `tradeResultStore`, `orderPreviewStore`, `marketStore`, `walletStore`, `supportStore`, `ticketStore` | - | memory only |

### 3.1 `accountStore` - active DEMO/LIVE

`active: AccountType` defaults to **`'DEMO'`**: "nobody should learn the product with real money".
`wallets: Partial<Record<AccountType, Wallet>>` keyed by `accountType ?? 'LIVE'`. Every trade is
placed on `active` (read at send time, see 4.7). `features/account/funds.ts`:
`openFunds(kind)` switches to LIVE first (with an info toast) because money only moves in and out
of LIVE; `useResetDemo()` calls `/wallet/demo/reset`, upserts the wallet and shows the server's
409 message verbatim when demo trades are still open.

### 3.2 `tradeStore`

`openTrades` holds only statuses `OPEN | SETTLING`, sorted by `expiryTimestamp` ascending;
`upsertTrade` removes a trade that left those statuses. `settleTrade` removes it from open and
prepends it to `recentlySettled` (de-duplicated by id, capped at **20**). A module-level
`useAuthStore.subscribe` resets the store whenever `user.id` changes.

### 3.3 `tradeResultStore` - dedupe

`results` newest first, capped at `MAX_RESULTS = 10`; `seen: Set<tradeId>`. `announce(trade)` is a
no-op for an id already seen, because a settlement can arrive twice (socket `trade.settled` and a
Sell now resolving from its own response). `announceResult()` (`features/trade/announceResult.ts`)
is the only entry point. Results do not time out; the newest stays until dismissed, older ones fold
as "+N more" (`TradeResultToasts.tsx`). The card's status word comes from `tradeLabel` (5.4), so a
void reads "Void · refunded".

### 3.4 `orderPreviewStore` - INTENT / PLACING / REJECTED

The only order state the client owns outright; it has `id: 'preview'`, never a trade id.

```
           showIntent(draft)                commit(draft, entryPrice)
  (none) ------------------> INTENT ------------------------------> PLACING
     ^   <------------------   |  entryPrice/entry/expiry = null      |  entryPrice frozen,
     |      clearIntent()      |  (tracks market + clock)             |  entry = now,
     |                         |                                       |  expiry = now + expirySeconds*1000
     |                         +--------------- commit --------------->|
     |                                                                 |
     +---------------------- resolve() / clear() <---------------------+ (server accepted)
     |                                                                 |
     +--- auto after 1800 ms <--- REJECTED <------- reject() ----------+ (server refused)
```

Rules: `showIntent` is ignored while a non-INTENT preview exists (a hover cannot unfreeze an
in-flight order) and when the draft is unchanged (same asset, direction, stake, expiry).
`clearIntent(assetId?)` only clears an INTENT. The REJECTED timer is module-scoped and only clears
the preview if it is **still** REJECTED, so a new order placed meanwhile survives. The draft's
`payoutPercent` and `potentialReturn` use the ticket's live payout (4.2).

### 3.5 `settingsStore` and migrations

Defaults: `defaultStake 500_00` (minor units), `defaultExpirySeconds 60`, `soundEnabled true`,
`confirmBeforeTrade true`, `tradeMode 'beginner'`, `chartType 'candles'`, `timeframe 60`, plus
`APPEARANCE_DEFAULTS` (`gridOpacity 7` on a 0-10 dial, `timezone SYSTEM`, `performanceMode false`,
`autoScroll true`, `showOrderLabels true`, `hideTradeResults false`, colour overrides `null`,
`indicators []`, `showExpiryGuide true`). `setGridOpacity` clamps to `[0, GRID_OPACITY_MAX=10]`.

`TIMEFRAMES = [5, 15, 30, 60, 300, 900, 1800, 3600, 10800, 86400]` seconds - "must match the
server's set". `timeframeLabel` -> `5s/1m/3h/1d`; `timeframeName` -> `"15 minutes"`, singular for 1.

`migrate(persisted, from)` (current `version: 5`):

1. Always merge `APPEARANCE_DEFAULTS` under the stored state (v1 had no appearance keys).
2. `from < 3` and `gridOpacity > 10`: rescale a stored percentage, `round(g / 100 * 10)`.
3. Any stored `timeframe` not in `TIMEFRAMES` (e.g. the removed 10 s) becomes **15**.
4. `from < 5`: force `confirmBeforeTrade = true` and `tradeMode = 'advanced'` (existing users had
   every tool on screen).

### 3.6 `workspaceStore`

`openAssetIds` (tabs, `MAX_TABS = 8`, oldest drops when full), `activeAssetId`, and transient UI
(`settingsOpen`, `transferModal`, `assetPickerOpen`, `chartLayout 1|2`). `closeAsset` on the
active tab moves to the last remaining tab. `ensureOpen(assetIds)` prunes ids the server no longer
lists (including assets hidden by the launch scope, 1.6), seeds the first two assets when nothing
valid remains, and fixes an invalid active id.

### 3.7 `drawingsStore`

`drawings: Record<assetId, Drawing[]>`; `Drawing = { id, kind, points: ChartPoint[] }` with
`ChartPoint = { time: epoch ms, price }` - **chart terms, never pixels**, so a drawing stays on its
candles through pan, zoom, resize and timeframe changes. Kinds and points needed:
`trend 2, ray 2, hline 1, vline 1, rect 2, fib 2` (`hline` ignores `time`, `vline` ignores
`price`). `MAX_DRAWINGS_PER_ASSET = 50` (oldest fall off). `armed: { kind, assetId }` scopes a
picked tool to one chart (two charts can be on screen on web). `add` validates completeness and
finite numbers, stores, selects the new drawing and disarms. `selectDrawings(state, assetId)`
returns a frozen shared `NO_DRAWINGS` array for empty assets - inline `[]` in a selector would
loop renders under `Object.is`. Ids come from `crypto.randomUUID()` (web) / `expo-crypto` (mobile).

**Mobile undo/redo** (`mobile/src/stores/drawingsStore.ts` `undo`, `redo`, `change`): adding,
removing and clearing are undoable per asset. Each change pushes the asset's previous list onto
`history[assetId].undo` (at most `MAX_HISTORY = 50` steps) and drops the redo trail; `undo`/`redo`
swap lists between the trails. A selection survives only while its drawing still exists
(`keepSelection`). The history is not persisted (`partialize` keeps `drawings` only).
`selectCanUndo`/`selectCanRedo` drive the toolbar buttons. Test: `mobile/tests/drawings-history.test.ts`.

**Migration v0 -> v1** (`migrateDrawings`): v0 stored `levels: { [assetId]: [{id, price}] }`.
Each finite price becomes `{ id (kept or new), kind: 'hline', points: [{ time: 0, price }] }`,
capped at 50 per asset; unreadable entries are dropped.

### 3.8 `favouritesStore`

Optimistic: `toggle` updates locally, then `PUT /users/me/favourites` fire-and-forget. `load()`
replaces the local list with the server's (called by `useRealtime` on sign-in); a failed load keeps
the local list. `recent` is device-only, most recent first, `MAX_RECENT = 8`.

### 3.9 `supportStore`

Held in a store because messages arrive from three directions (own send, server echo, agent reply).
`merge(existing, incoming)` keys by `id` and sorts by `createdAt` then `id`, so an echo never
duplicates. `pending: { tempId, body, createdAt, failed }[]` for unsent messages;
`resolvePending(tempId, message?)` removes the pending entry and merges the confirmed message;
`failPending` flags it. Typing: `setAgentTyping(name)` sets `agentTypingUntil = now + 5000`
(`TYPING_TTL_MS`, the server repeats the signal while typing continues); an `AGENT` message clears
it immediately. `reset()` on sign-out.

### 3.10 Others

- `onboardingStore`: `completed` per **device**, not per account (it is an explanation, not a
  consent record). Replaying from Learn does not reset it.
- `marketStore`: `ticks[assetId]`, `previous[assetId]` (for the up/down flash), `feeds`. Mobile adds
  `applyTicks(batch)` (2.3).
- `walletStore`: legacy single wallet + `lastChangeAt`.
- `themeStore`: section 9. `ticketStore`: section 4.5.

---

## 4. Trade ticket - `features/trade/ticketModel.ts`, `livePayout.ts`, `ticketStore.ts`, `TradeTicket.tsx`

`ticketModel.ts` and `livePayout.ts` are pure and shared (mobile's `ticketModel.ts` adds two
helpers for its Time sheet); the component only renders their answers. All money is integer
**minor units** (paise). The two `TradeTicket.tsx` files share the same rules and differ in layout
and in the mobile send lock (4.7).

### 4.1 Durations

- `durationChips(asset)`: one chip per `asset.supportedExpiries`, sorted ascending; `payoutPercent`
  from `asset.payouts.find(p => p.expirySeconds === s)` (the catalogue's indicative payout). A
  duration without a payout row stays visible but disabled; its `unavailableReason` is the pricing
  engine's message for that expiry (`livePayout.ts` `unpricedReason`, from
  `asset.pricing.unavailable`), else `'No payout for <label>'`.
- `durationLabel(s)`: `formatExpiry` below an hour (`30s`, `1m`, `1.5m`), `"1h"` / `"1h 30m"` above.
- `pickDuration(chips, wanted)`: `wanted` if usable, else the first usable, else the first chip.
  The ticket replaces an unusable duration in `ticketStore` **without** touching the saved default
  (another asset may offer it).
- **Web layout**: `durationRow(chips, selected, max = 4)` shows up to 4 chips; when more exist, the
  rest go behind "More" (`hidden` count, `DurationSheet.tsx`), and a selected long duration takes
  the last slot so the selection is always visible.
- **Mobile layout** (`mobile/src/features/trade/TradeTicket.tsx`): one row of an **Amount** box, a
  trade-history button (opens the positions sheet, carries the open-trade badge) and a **Time** box
  (`TimeBox`) showing the clock time a trade placed now would end at, to the second, recomputed
  every second in the chart's time zone. The Time box opens `TimeSheet.tsx`: `expiryGroups(chips)`
  sorts every chip into exactly one section (`Up to 5 min.` <= 300 s, `15–60 min.` or `5–60 min.`
  up to 3600 s, `Over 1 hour`), each choice written as its end time with `durationWords(s)`
  (`"1 min 30 sec"`, `"1 hour"`) and its payout under it. The sheet notes that the expiry is
  counted from acceptance. Test: `mobile/tests/time-sheet.test.ts`.

### 4.2 Live offers - `features/trade/livePayout.ts`

The payout on the UP and DOWN buttons is the pricing engine's live offer, not the catalogue
figure. While the ticket shows an asset with a payout row for the chosen duration and its market is
open, it queries `['entry-quote', assetId, expirySeconds, stake, account]` ->
`api.trades.entryQuote({ assetId, expirySeconds, amount: stake ?? undefined, accountType })`
**every second** (`refetchInterval: 1000`, `retry: false`). Across a change of amount the previous
quote for the same asset, duration and account stays on screen (`placeholderData`).

An `EntryQuote` (`types/api.ts`) holds one `EntryOffer` per direction: `offerId` (single use),
`available`, `payoutPercent`, `profit`, `potentialReturn`, `referencePrice`, and `code`/`reason`
when that side is not offered (e.g. `SYMBOL_HALTED`, `VOLATILITY_BLOCK`, `EXPOSURE_LIMIT`). The
server keeps each offer for `pricingOfferTtlMs` (3 s by default), bound to the user, asset,
direction, duration, stake and account (`service/pricing/offers.go` `IssueQuote`, `TakeOffer`;
[pricing-spec.md](../audit/pricing/pricing-spec.md)).

| Helper | Rule |
|---|---|
| `sidePayout(quote, dir, fallback)` | the direction's offer payout; `null` when that side is not offered; the catalogue figure until the first quote lands |
| `shownPayout(quote, fallback)` | `quote.payoutPercent` (the lower available payout), or the catalogue figure; used for the contract information, the preview draft and the guide's example |
| `quoteBlockedReason(quote)` | when neither side is offered: `up.reason ?? down.reason ?? 'This duration cannot be priced right now'` |
| `shownTerms(quote, dir, fallback)` | what the order carries: `offerId` (only when that side's offer is available) and `displayedPayoutPercent` |
| `isRequote(code)` | `OFFER_EXPIRED`, `OFFER_INVALID`, `PAYOUT_CHANGED` |
| `isPaused(code)` | `TRADING_HALTED`, `FEED_UNHEALTHY`, `NO_PRICE_AVAILABLE`, `EXPOSURE_LIMIT_EXCEEDED`: refusals that are not about the trader's terms; for an order that carried an offer the server answers these with a fresh quote too |
| `requoteFrom(details)` | the fresh `EntryQuote` the server puts in a refusal's `details.quote` |

Each direction button shows its own payout and is disabled when that side's payout is `null`, so
one side can trade while the other is blocked. While the confirmation is open, the contract
information shows the confirming direction's payout. After a successful order the ticket refetches
at once (the offer is used up). Test: `client|mobile/tests/live-payout.test.ts`.

### 4.3 Stake limits and contract terms

`payout = asset.payouts.find(p => p.expirySeconds === expirySeconds)`.
`stakeLimits(asset, payout) = { min: max(asset.minStake, payout.minStake), max: min(asset.maxStake,
payout.maxStake) }` (missing values default to 0 / `MAX_SAFE_INTEGER`).

`contractTerms(stake, payoutPercent)` (shown by `ContractInfo.tsx` with the live payout):

```
profit      = round(stake * payoutPercent / 100)
totalReturn = stake + profit
maxLoss     = stake
```

The preview draft uses the same formula for `potentialReturn`. These are displays; the accepted
trade's own `payoutPercent`/`profit`/`potentialReturn` (the offer's locked payout) are what count.
`ContractInfo.tsx` also states the tie rule (5.3), "Expires at" as now + duration (recomputed every
second), and the fee note (no fee to open; Sell now is priced live and includes a fee).

### 4.4 Amount: presets, Max, parsing, validation

- `PRESET_AMOUNTS_MAJOR = [100, 500, 1000, 5000]` -> minor, **filtered** to `[min, max]` (a preset
  the server would refuse is hidden, not disabled).
- **Max** = `min(available, limits.max)`, added only when `>= limits.min`, `> 0` and not equal to an
  existing preset.
- `parseAmount(text)`: strips grouping commas; accepts `^\d*(\.\d{0,2})?$` (not a lone `.`);
  returns `toMinor(value)` or `null`.
- `amountState(text, limits, available)` - checked in the order a trader can fix them:
  `empty -> invalid (null or <= 0) -> below-min -> above-max -> insufficient -> valid`.
  Insufficient balance is reported last on purpose: telling someone to deposit for an amount the
  platform would refuse anyway sends them to the deposit screen for nothing. On `insufficient` the
  message offers **Deposit** (LIVE, `openFunds('DEPOSIT')`) or **Reset demo balance** (DEMO).
- `amountMessage(state, currency)`: the copy, e.g. `"Not enough balance. Available: ₹1,234.00"`.
- Stepper: `amountStep(v)` = 1000 major when `v >= 10_000`, 500 when `>= 1000`, else 100;
  `stepAmount` clamps to `[min, max]`. `amountText(minor)` writes back without trailing `.00`.
  Typing a valid amount, a preset or a step also saves it as `settings.defaultStake`.
- Mobile types the amount in `AmountSheet.tsx` (decimal pad, focused after 320 ms with the text
  selected); the sheet only draws - limits, presets and messages come from the ticket.

### 4.5 `ticketStore`

Holds `amountText` (the raw field text, so empty/half-typed is a nameable state), `expirySeconds`
and `focusRequest`. Initialised from `settings.defaultStake` / `defaultExpirySeconds`. `setExpiry`
also saves the default; `prefill(stake, expiry)` ("Trade Again" from a result card) saves both
defaults and bumps `focusRequest`: web scrolls the ticket into view and focuses and selects the
amount; mobile scrolls the ticket to the top and opens the Amount sheet.

### 4.6 Blocking

```
feedBlocked   = !asset || feed.status in {OFFLINE, STALE}
blockedReason = !asset             -> 'Choose an asset'
              : !asset.marketOpen  -> 'Market closed'
              : connection != open -> 'Live prices disconnected — reconnecting…' (also a banner)
              : feedBlocked        -> 'Price feed <status> - trading paused'
              : !payout            -> 'This duration is not available right now'
              : quoteBlockedReason(quote)        // neither side offered: the engine's reason
cannotTrade   = !!blockedReason || stake === null
UP disabled   = cannotTrade || upPayout === null || pending      (DOWN alike)
```

`asset.feed.status` comes from the catalogue (refetched every 4 s on the trade screen). The
server's feed status already folds in its data rules: dropped ticks or clock drift make
`GetFeedHealth` report STALE with a `blockReason` (see [feed-rules.md](../audit/operations/feed-rules.md));
the apps show the status, not the `blockReason`. `components/assetList.ts#assetAvailability`
applies the market/feed part of the rule to list rows (`closed | paused | open`). Every check is
repeated by the server at acceptance; the ticket only says it first. On mobile the blocked reason
and the re-quote banners are pinned with the UP/DOWN buttons, below the scrolling set-up, so a
disabled button always comes with its reason.

### 4.7 Placing: confirm or one-tap, idempotency, offers, re-quotes, locks

```
press(dir):   if pending || cannotTrade: return            // mobile: also if sendingRef
              confirmBeforeTrade ? open ConfirmTradeSheet(dir) : send(dir, livePrice)
confirm():    "don't ask again" -> toggleConfirm();  send(dir, reQuote?.serverPrice ?? livePrice)
send(dir,p):  setPending(dir); orderPreview.commit(draft, p);
              mutate({dir, quotedPrice: p ?? undefined})   // + shownTerms(latest quote, dir)
```

- **What is sent**: `{ assetId, direction, amount: stake, expirySeconds, quotedPrice, offerId,
  displayedPayoutPercent, accountType }`. `offerId`/`displayedPayoutPercent` come from
  `shownTerms(quoteRef.current, ...)` - the latest quote at the moment of sending, not the render's.
  `accountType` is read from `useAccountStore.getState().active` at send time.
- **`quotedPrice`** = `marketStore.ticks[asset.id].price ?? asset.feed.lastPrice ?? null`, the price
  on screen. With no tick at all it is omitted and the server applies no slippage guard. It is never
  the fill price.
- **Idempotency**: a fresh `crypto.randomUUID()` (mobile: `expo-crypto` `randomUUID`) per attempt,
  sent as `idempotency-key`. The server collapses retries of the same attempt; a deliberate second
  trade gets a new key.
- **Success**: clear re-quote state and sheet error, refetch the quote (the offer is used up),
  `orderPreview.resolve()`, `tradeStore.upsertTrade`, invalidate `['wallet']` and `['trades']`,
  toast with the **server's** `entryPrice` and `potentialReturn`. Nothing is shown as accepted
  before this.
- **Re-quote, the terms moved** (`OFFER_EXPIRED`, `OFFER_INVALID`, `PAYOUT_CHANGED`;
  `service/pricing/offers.go` `TakeOffer`, `CheckDisplayed`). Nothing was placed. The fresh quote
  from `details.quote` replaces the cached one (or the ticket refetches). One-tap: a banner "The
  payout changed before your order landed" offers **Place UP at N%** (re-sent on the new offer at
  the price on screen) or says that side cannot be traded now. With confirmation on, the refusal
  message shows in the sheet, whose payout already reads the fresh offer; pressing Confirm again
  sends on it. An order sent after an amount change but before that amount's quote arrived
  carries the previous stake's offer and comes back as `OFFER_INVALID` this way.
- **Re-quote, the price moved** (`PRICE_MOVED`, from the `quotedPrice` slippage guard in
  `service/binary/binary.go` `acceptTrade` or from the offer's reference price in `TakeOffer`):
  the fresh quote is taken from `details.quote` when present, and `details.serverPrice` is offered
  back - "Price moved before your order landed ... Place UP at new price" (one-tap) or the sheet's
  re-quote price (confirm mode) - and re-sent with `quotedPrice = serverPrice`.
- **A refusal that is not the trader's** (`isPaused`: a halt, market data not fresh, the book's
  limit): the fresh quote from `details.quote` replaces the cached one (or the ticket refetches),
  so the buttons show at once what is offered now - typically each side "not offered" with the
  engine's reason - instead of keeping the refused offer; the message then goes to the sheet or
  the toast as below. The server sends that quote for every such refusal of an order that carried
  an offer ([01-trading-core.md](01-trading-core.md) 2.2).
- **Any other refusal** goes into the sheet (confirm mode) or a "Trade rejected" toast (one-tap).
  The preview is marked rejected (`orderPreview.reject()`). Re-quote state is cleared when the
  asset, duration or amount changes.
- **Double-send locks**: both buttons are disabled from the press until **350 ms** after the round
  trip ends (`onSettled` cooldown), so a double click is never two trades. Mobile adds a
  **synchronous lock**: `sendingRef` is set in `send()` and cleared with the cooldown, and both
  `press()` and `send()` return while it is set. State alone was not enough on a busy phone:
  several queued taps could run before the re-render that disables the buttons, each reading
  `pending` as null (`mobile/src/features/trade/TradeTicket.tsx`; see also 2.3).
- **Preview hooks**: web shows an INTENT preview on mouse hover or keyboard `:focus-visible` only
  (touch emits no matching leave). Mobile shows it on `onPressIn` and clears it on release or
  slide-off one tick later (`leave` defers with `setTimeout(0)`, and `press` sets `confirmingRef`
  before the render), unless the confirmation opened. While the confirmation is open the preview is
  re-shown. Changing asset or account clears the intent and closes the confirmation; unmount
  clears it.
- Mobile dismisses the keyboard on press and gives a light haptic when an order is sent
  (`feelPlaced`, `Haptics.impactAsync(Light)`, errors swallowed).

### 4.8 Sell now (early close)

`PositionsPanel.tsx#OpenPosition`: query `['trade', id, 'quote']` -> `api.trades.quote(id)`,
`enabled: running && status === 'OPEN'`, `refetchInterval: 2000`, `staleTime: 1000`. The button
reads `Sell now <quote.value>` when `quote.available`; otherwise the row shows
`quote.unavailableReason ?? 'Pricing...'`, or `'Settling…'` once the countdown is over (a row whose
countdown hit zero while still `OPEN` is displayed as `SETTLING`).

The server decides availability (`server-go/internal/service/binary/earlyclose_service.go`
`priceEarlyClose`; the first reason found is the one shown):

| Reason shown | Rule |
|---|---|
| Closing early is currently disabled | `earlyCloseEnabled` off |
| This trade is already ... | status is not `OPEN` |
| Trading is currently halted | `tradingHalted` |
| Too close to expiry to close early | time left < `EarlyCloseFloorMsFor` = max(`earlyCloseFloorMs`, at least 1 s; `minSecondsRemainingForEarlyClose`, 10 s) (`service/settlement/policy.go`) |
| No live price is available for this asset | no last tick |
| Market data for X is OFFLINE / STALE / DEGRADED. Closing early is paused. | feed classified from that tick, per the halt switches; a data-rule block counts as STALE |
| Market data for X cannot be trusted right now (code) | `marketdata.FeedBlockFor` (dropped ticks, clock drift) |
| The live price for X is stale | tick older than `earlyCloseMaxTickAgeMs` (1 s) |
| Not enough recent market data to value this position | no realised volatility |

An available quote is recorded server-side as the one the customer was shown (Redis, kept for
`earlyCloseQuoteMaxAgeMs`, 10 s; `QuoteTrade`). `POST /binary/trades/:id/close` re-prices on the
latest price and pays the fresh value, but refuses with `PRICE_MOVED` when no quote was shown in
the last 10 s or the fresh value is more than `earlyCloseQuoteTolerancePercent` (5 %) below it; a
higher value is simply paid (`checkQuoteValidity`). The app shows a refusal as a "Could not sell"
toast with the server's message; the button already shows the newer amount from its 2 s poll. On
success: `settleTrade`, `announceResult`, invalidate wallet and trades, toast
`"<value> returned (<signed customerResult>)"`. Customer wording:
[disclosures.md](../audit/customer/disclosures.md) ("Sell now").

---

## 5. Results: projection, settlement, tie rule, status words

### 5.1 Live projection ("If it ends now") - `features/trade/liveResult.ts`

What a running trade **would** pay if it ended at the price on screen. Worded as a projection ("If
it ends now"); never a result.

```
liveResult(trade, price, drawPolicy = 'REFUND'):
  price not finite             -> null
  price == entryPrice          -> policy = trade.drawPolicy ?? drawPolicy
                                  REFUND: {draw, 0} | LOSS: {losing, -stake} | WIN: {winning, profit}
  winning = UP ? price > entry : price < entry
  winning ? {winning, +trade.profit} : {losing, -trade.stake}
```

The amounts are the ones the server wrote on the trade (`profit` from the payout locked at
acceptance, `stake`). `liveTotal(trades, priceOf, now, drawPolicy)` sums over trades with
`expiryTimestamp > now` and reports `running` and `priced` counts separately, so a total never
silently covers fewer trades than the line claims. Expired-but-unsettled trades are excluded (their
outcome depends on a price the screen may not have seen). `amountTone(a)` -> `up | down | neutral`.
`STANDING_WORDS`: `winning`, `losing`, `draw, refunded`. The projection does not anticipate a void:
whether a valid expiry price exists is known only at settlement. Callers pass the tie rule from
`useDrawPolicy` (5.3): per trade in `PositionsPanel.tsx` `OpenPosition` and the Trades pages, one
rule for the whole total in `liveTotal`.

### 5.2 Settlement display - `features/trade/settlement.ts`

- `settlementOf(trade)`: `trade.settlementAmount` when present - already **net and signed**
  (`payout - stake` on a win, `-stake` on a loss, `0` on a draw or void, the customer's result on a
  Sell now). Fallbacks only for a terminal row missing the field: WON -> `profit`, LOST -> `-stake`,
  DRAW/CANCELLED -> `0`, else `null`.
- `payoutOf(trade) = stake + (settlementOf(trade) ?? -stake)`: what came back to the wallet (full
  payout on a win, `0` on a loss, the stake on a draw or void). The result popup shows this, so a
  loss reads as zero, not negative.

Both the chart overlay and the result popup use these, so they cannot disagree.

### 5.3 The tie rule - `features/trade/drawRule.ts`

What an exact match of entry and expiry price settles as is the platform's `drawPolicy` (risk
configuration: `REFUND` by default, or `LOSS`/`WIN`). The server sends the policy **in force now**
with every catalogue row; it is platform-wide, the same on every asset
(`server-go/internal/api/assets/routes.go` `assetDetail.DrawPolicy` = `cfg.DrawPolicy`).

- `drawPolicyOf(asset) = asset.drawPolicy ?? 'REFUND'`.
- `TIE_SENTENCE`, `TIE_CLAUSE`, `TIE_LABEL` give the copy per policy.
- `useDrawPolicy(assetId?)` reads the shared `['assets']` query (no extra request) and falls back
  to the first asset's policy. `ContractInfo.tsx` (the "?" on "Expires at" and "Maximum loss"),
  `HowItWorks.tsx`, Learn and Onboarding call it without an id; the positions panel and the Trades
  pages pass the trade's asset.
- A tie is `DRAW` only under REFUND; under LOSS/WIN it settles as LOST/WON. A void (5.4) is
  `DRAW` under any policy.

**Frozen on the trade.** The server stores the policy on each trade at acceptance
(`service/binary/binary.go` `acceptTrade`; `Trade.drawPolicy` in the trade view) and settles with it (`service/settlement/settlement.go` `DrawPolicyFor`, falling back to the config for
older trades). The projection of an open trade uses it (`liveResult`: `trade.drawPolicy` first, the
policy in force now only for a trade that does not carry one; test `tests/void-and-scope.test.ts`).
The admin trade detail shows it ("Tie rule (frozen)", section 11).

### 5.4 Status words, voids and tones - `utils/tradeStatus.ts`

One terminology everywhere (`statusLabel`): `OPEN 'Active'`, `SETTLING 'Settling'`, `WON 'Won'`,
`LOST 'Lost'`, `DRAW 'Draw · refunded'`, `CANCELLED 'Cancelled'`, `CLOSED_EARLY 'Sold early'`.

**Voids.** When no valid market price exists at expiry the server voids the trade and returns the
stake. The status stays `DRAW` for older readers; the trade view adds `voided: true`,
`voidReason` ("No valid market price at expiry; stake returned") and `expiryPriceSource:
'NO_PRICE_VOID_REFUND'` (`server-go/internal/models/binary_trade.go` `ToTradeView`,
`TradeVoided`). The apps tell the two apart:

| Helper | Rule |
|---|---|
| `isVoided(trade)` | `status === 'DRAW' && (voided === true \|\| expiryPriceSource === 'NO_PRICE_VOID_REFUND')` - the second test covers a server that sends only the rule; only a DRAW can be a void |
| `tradeLabel(trade)` | `VOID_LABEL` = `'Void · refunded'` for a void, else `statusLabel(status)`; a real tie still reads `'Draw · refunded'` |
| `voidReason(trade)` | the server's `voidReason`, else `'No valid market price at expiry; your stake was returned.'`; `null` for any other trade |

Every place that names a trade's state uses `tradeLabel`: `StatusPill` (`features/trade/tradeBits.tsx`),
the result card and its older rows (`TradeResultToasts.tsx`), the Trades page and its CSV export
(`pages/Trades.tsx`; mobile `features/trades/TradesScreen.tsx`) and the trade detail, which adds a
"Why voided" row with `voidReason` (`pages/TradeDetail.tsx`; mobile
`features/trades/TradeDetailScreen.tsx`). The history result filter's DRAW option reads "Draw /
void", because the server filters by status. Test: `client|mobile/tests/void-and-scope.test.ts`.

`statusTone`: OPEN/SETTLING `live`, WON `up`, LOST `down`, CLOSED_EARLY by the sign of
`settlementAmount`, else `neutral` (a void is neutral, like a draw). `isActive(status)` = OPEN or
SETTLING. `durationOf(trade) = round((expiry - entry) / 1000)`.

---

## 6. Chart logic

### 6.1 History, timeframes and live tick folding

`PriceChart.tsx` (both platforms) loads `['candles', assetId, timeframe]` =
`api.market.candles(id, 300, timeframe)` with `refetchInterval: max(15_000, timeframe * 1000)`.
Bars for every timeframe, including the sub-minute ones (5/15/30 s), come from the server; the
client does not aggregate history. `bucketMs = timeframe * 1000`. In beginner mode the chart type
is restricted to `BEGINNER_CHART_TYPES = ['candles', 'line', 'area']` (`ChartToolbar.tsx`) and
indicators/drawings are hidden (but kept).

When a candle response lands: the bars are copied into `barsRef`, painted, `lastBucketRef` = last
bar, and `seriesFrameRef = bucketMs` ("the series now holds this frame"). The view jumps to the
live edge only when the dataset changed (asset or timeframe) or the view was already following
(`isFollowingRealTime`: `|scrollPosition - rightOffset| < 1.5` bars), and only with `autoScroll`.
A background refetch never drags a trader out of history.

**Live tick folding** into the open bucket:

```
if seriesFrameRef != bucketMs: skip          // timeframe changed, refetch in flight
openTime = floor(tick.timestamp / bucketMs) * bucketMs
if previous && openTime < previous.time: skip   // never fold backwards
bucket = previous.time == openTime
  ? { open: prev.open, high: max(prev.high, p), low: min(prev.low, p), close: p }
  : { open: p, high: p, low: p, close: p }      // a new bar
replace last bar or push
```

The guard exists because a tick bucketed with a new, wider frame (`floor(now / 3h)`) before the
refetch lands would be older than the newest bar, which makes `lightweight-charts` throw "Cannot
update oldest data" and takes the chart down. Skipping is safe: the in-flight refetch contains that
bucket. Times are in ms in the client and divided by 1000 for `lightweight-charts`' `UTCTimestamp`.
On mobile the ticks reach the chart through the 250 ms batch (2.3), so a bar moves at most four
times a second there.

Other display rules: while the active account is DEMO the chart says so - on the web as a badge in
its top-left corner ("Demo account · virtual money", "Demo · virtual money" below `sm`, in the warn
tone, above the OHLC readout), on mobile as a DEMO watermark under the series (font size
`clamp(round(width * 0.17), 44, 150)`, per-theme alpha 0.06/0.075/0.055); axis and crosshair labels
use the chosen time zone (formatting only); grid colour alpha = `gridOpacity / 10`.

### 6.2 Indicators - `utils/indicators.ts`, `features/trade/chart/indicatorCatalog.ts`

Display aids from the loaded bars only; never sent to the server. Every function returns points
only where its window is fully populated (no padded heads).

| Indicator | Params (fixed) | Formula |
|---|---|---|
| SMA | 20 | `SMA_i = (1/n) * sum(close_{i-n+1..i})`, first point at bar `n-1` (running sum) |
| EMA | 50 | `k = 2/(n+1)`; seed = SMA of first `n`; `EMA_i = (close_i - EMA_{i-1})*k + EMA_{i-1}` |
| Bollinger | 20, 2 | middle = SMA20; `sigma` = **population** std-dev (divide by n) of the window; bands = `middle +/- 2*sigma` |
| RSI | 14 | Wilder: seed avg gain/loss over changes 1..n (first value on bar `n`); then `avg = (avg*(n-1) + x)/n`; `RSI = 100 - 100/(1 + G/L)`; `L=0`: 100, both 0: **50** |
| MACD | 12, 26, 9 | `macd = EMA12 - EMA26` from bar 25 (offset `slow-fast` aligns the arrays); `signal = EMA9(macd)`; `histogram = macd - signal` |
| Stochastic (slow) | 14, 3, 3 | raw %K = `(close - LL14)/(HH14 - LL14) * 100` (range 0 -> **50**); `%K = SMA3(raw)`; `%D = SMA3(%K)` |

`INDICATORS = ['sma','ema','bollinger','rsi','macd','stochastic']` (`settingsStore`); stored lists
are filtered with `isIndicator` (a list from another build may name an unknown one). SMA, EMA and
Bollinger are `overlay` placement (on price; colours: warn, 60% strong text, crosshair grey);
RSI/MACD/Stochastic are `pane` oscillators drawn in bands under the price in `INDICATOR_ORDER`
(guide lines RSI 70/30, Stochastic 80/20). `searchIndicators(query)` requires every typed word to
appear in name, short name, params, category label or keywords. Recomputation is throttled by
`shouldRun` (6.8) with key `indicators | theme | candles.dataUpdatedAt`.

Oscillator bands are separate charts that follow the main chart's visible logical range; every
price axis is widened to the widest one so bar `i` sits at the same x in every band (web:
`syncAxisWidths`; mobile: shared `engine.axisW`).

### 6.3 Logical-index time projection - `features/trade/chart/timeProjection.ts`

Both chart engines lay the time axis out **by bar index**, not by time. Anything drawn at an
instant (entries inside a bar, future expiries, drawing points) goes through a fractional logical
index:

```
timeToLogical(ms, bars, bucketMs):
  ms >= last.time  -> (count-1) + (ms - last)/bucketMs        // extrapolate into the future
  ms <  first.time -> (ms - first)/bucketMs                   // extrapolate into the past
  else i = lastAtOrBefore(bars, ms)                           // binary search
       -> i + min(1, (ms - bars[i].time)/bucketMs)            // gaps (closed market) held at next slot
logicalToTime = inverse (linear extrapolation outside the data)
x = x0 + logical * spacing
```

On web, `readLogicalAxis(chart)` recovers `x0` and `spacing` from
`timeScale().logicalToCoordinate(0)` and `(1)`, because the library answers 0 for non-integer
indices. On mobile the engine's `View` already carries `x0` and `s`. Mobile duplicates every
function over flat time arrays (`*Times`) and marks them `'worklet'`, so the UI thread and the JS
tap handlers run the same code. The orders, expiry guide and drawings all project through this, so
a trend line drawn to an order's expiry sits exactly on it.

### 6.4 Drawings geometry - `features/trade/chart/drawingGeometry.ts`

Pure pixel geometry after projection:

- `FIB_LEVELS = [0, 0.236, 0.382, 0.5, 0.618, 1]`; `fibPrice(start, end, l) = end - (end - start) * l`
  (0 at the end of the move, 1 at its start; first click is the start).
- `distanceToSegment` (clamped projection), `rayEnd(a, b, plot)` (a point beyond the plot along
  a->b: max corner distance + plot diagonal), `boxOf`.
- `hitsDrawing(shape, p, plot, tolerance)`: hline by `|dy|`, vline by `|dx|`, trend/ray by segment
  distance, rect/fib anywhere inside the box +/- tolerance. `topmostHit` scans from the last
  (top-most) drawing. Tolerances (`DrawingLayer.tsx`): web `MOUSE_REACH 6`, `TOUCH_REACH 14`, slop
  4/10 px, click <= 700 ms; mobile `TOUCH_REACH 14`.
- `actionAnchor`: where the delete button hangs (right end of hline, top of vline, the higher end
  of a line, top-right of a box).

### 6.5 Expiry guide - `features/trade/chart/ExpiryGuide.tsx`

A vertical, grey, dotted line at `Date.now() + expirySeconds * 1000`, labelled with the clock time
on the time axis. Shown only when `showExpiryGuide`, the market is open, the chart is the one the
ticket trades (`activeAssetId`), and the asset supports the ticket's duration; hidden while an
order preview for that asset is on the chart. Visually distinct from orders (no price, no amount).
Web moves DOM nodes from a rAF loop (`useFrameLoop`, 30 fps in performance mode); mobile records a
Skia picture driven by `useFrameClock`. The mobile ticket's Time box (4.1) shows the same instant.

### 6.6 Order layer - `features/trade/orders/orderModel.ts`, `ChartOrderLayer.tsx`, `useOrderProjection.ts`

`buildOverlayOrders({assetId, preview, openTrades, settled, seenAt, now})` merges, in paint order:
recently settled trades (each lingers `SETTLED_LINGER_MS = 6000` from when **this client first saw**
it settled - not from `settledAt`, which a lagging clock could make already expired), open trades,
then the preview last (on top).

Visual states: `PREVIEW | ACTIVE | EXPIRED | WON | LOST | CANCELLED`. Mapping: DRAW (a tie or a
void) and SETTLING -> EXPIRED; OPEN past expiry -> EXPIRED with `awaitingSettlement` ("Settling");
CLOSED_EARLY -> WON if `settlementAmount >= 0` else LOST; REJECTED preview -> CANCELLED (lingers
`CANCELLED_LINGER_MS = 1800`). An INTENT preview `tracksClock` (entry = now, expiry = now +
duration, price = live, all recomputed per frame so it glides); a PLACING preview is pinned.
`entryPrice`, once non-null, is never overwritten. Colours (`orderTone`): preview green/red dashed;
placed orders **brand yellow** regardless of direction; outcome colour once settled;
expired/cancelled grey and faded. `needsHeartbeat` keeps the 1 s heartbeat only while something is
ACTIVE, PREVIEW or fading. `isInTheMoney` is indicative only.

Right-offset room-making (web `ChartOrderLayer.tsx`), only when the view is at the live edge and
`autoScroll` is on:

```
needed = 6                                                      // BASE_RIGHT_OFFSET
guide:   needed = max(needed, ceil(guideSeconds*1000 / bucketMs) + 2)
pending orders: needed = max(needed, applied, 18)               // ORDER_RIGHT_OFFSET, hold what was made
                for each: needed = max(needed, ceil((expiry - lastBar.time)/bucketMs) + 2)
needed = min(needed, 42, max(6, floor(plotWidth * 0.5 / barSpacing)))
```

Projection: web `useOrderProjection` runs a rAF loop (60 fps, 30 in performance mode) writing DOM
styles only on a change > 0.5 px, because the price scale autoscales on every tick without an
event. Mobile computes the same geometry as a Reanimated derived value on the UI thread.

### 6.7 Trade-window chart data - `features/trade/chart/tradeWindow.ts`

The trade detail chart (`TradeWindowChart.tsx`, query `['trade-window', id]`, `staleTime
Infinity`, refetch every 15 s only for a running trade drawn from candles, `retry 1`):

1. `tickWindow(trade)`: `pad = clamp(0.25 * duration, 10 s, 120 s)`; `stepMs` = 500 (<= 1 min),
   1000 (<= 5 min), else 2000; `from = entry - pad`, `to = expiry + pad`.
2. If `ticksRetained(from, now)` (`now - from < TICK_RETENTION_MS - 45 s`, with
   `TICK_RETENTION_MS = 15 min`), fetch ticks (`windowMs = clamp(now - from + 5 s, 1 s, 15 min)` for
   older servers); use them only if `ticksReachEntry`. The server's tick history defaults to 15
   minutes (`feedTickHistoryRetentionMs`, risk config) and `/ticks` serves at most a 15-minute
   window; a shorter retention simply fails `ticksReachEntry` and falls through to candles.
3. `resampleTicks`: a regular grid anchored at the entry (`time = entry + k*step`), each point takes
   the last tick at or before it (the server's expiry-price rule), future points and points before
   the first tick are whitespace, and on-grid `pins` force the recorded entry/exit prices.
4. Otherwise one-minute candles: `candleWindow` = 20 min before entry to `min(now, expiry + 5 min)`,
   `limit = clamp(minutes + 2, 10, 1000)`; `candlesForWindow` returns null unless a candle contains
   the entry (covers a server that ignored `to`). Fallback retry with `MAX_CANDLES = 1000` latest.
5. `tradeMoments`: exit = sold-at time for CLOSED_EARLY, else expiry; null while running or for
   CANCELLED.

### 6.8 Redraw throttling - `utils/redraw.ts`

`shouldRun(ref, key, performanceMode, now)`: always runs when `key` changed (a structural change
the trader caused); a repeat under the same key is skipped only when performance mode is on and
less than `PERFORMANCE_REDRAW_MS = 1000` passed. Performance mode never delays input.

### 6.9 The mobile Skia engine - `mobile/src/features/trade/chart/engine/`

The engine replaces `lightweight-charts` but keeps its model, so everything projected on web
projects identically.

- **Threads** (`types.ts`): the JS thread owns data (history, ticks, indicators, orders) and writes
  Reanimated shared values; the UI thread owns the viewport and repaints in worklets. A pan or pinch
  never costs a React render.
- **Series as flat arrays** (`SeriesData { t, o, h, l, c, bucket }`), so a tick updates the last
  element in place (`series.modify` worklet) instead of copying a thousand objects across threads.
  `NaN` in `c` is whitespace. The tick fold on mobile also checks `data.bucket === bucketMs`, and a
  new bar bumps `rightOffset` by 1 when the newest bar is not visible, so a view parked in history
  stays put.
- **Viewport** (`useChartEngine.ts`): shared values `size`, `axisW` (56 initially), `spacing`,
  `rightOffset`, `restingOffset` (6), `series`, `extents` (overlay indicator values the price scale
  fits to), `closeOnly` (line/area), `minMove`, `crosshair`. The derived `view` =
  `fitPrices(horizontal(...))`.
- **Horizontal** (`scale.ts`): `x0 = pw - (count - 1 + rightOffset + 0.5) * s` - the
  lightweight-charts placement; visible `first/last` from the left/right logical edges.
- **Price autoscale**: min/max of visible highs/lows (closes for line/area) and visible overlay
  values; empty view holds the newest bar; flat range padded by `max(|hi|*0.0005, 5*minMove,
  1e-9)`; margins applied as `lo - span*bottom/inner`, `hi + span*top/inner`, `inner = max(0.05, 1 -
  top - bottom)`. Oscillators can pass a `FixedRange`.
- **Gestures**: `Gesture.Race(longPress-crosshair (260 ms), Simultaneous(pan, pinch), tap (<= 700
  ms, <= 10 px))`. Pan is horizontal only (`activeOffsetX +/-6`, `failOffsetY +/-12`, so the page
  can still scroll vertically); dragging the time axis scales spacing by `exp(dx / 220)`; a fling
  uses `withDecay` (deceleration 0.997) clamped by `clampOffset` (at least 2 bars stay on screen).
  Pinch zooms about the fingers (`offsetKeeping`) or keeps the newest bar fixed when following.
  `isFollowing` = `|rightOffset - restingOffset| < 1.5`; `scrollToRealTime` animates 260 ms;
  `fitContent` waits until there are bars and a size.
- **Tick marks**: `priceMarks` picks a nice step from `[1, 2, 2.5, 5, 10] * 10^k` about 34 px apart
  (`PRICE_GAP`), never finer than the instrument precision, and flags the rounder labels as
  `major`; `timeMarks` picks from a step table (1 s ... 30 d) so labels are about 50 px apart
  (`TIME_GAP`), labels a day change with the date, seconds below a minute.
- **Time zones on the UI thread**: `Intl` is too slow per label per frame, so `buildZoneInfo`
  computes piecewise UTC offsets on the JS thread (sampled every 3 days, each switch narrowed by
  bisection to 1 s) and the painter formats from that table.
- **Look** (`engine/colors.ts` `chartLook`): the theme palette dressed like the TradingView mobile
  app - a black plot in dark, white in light, twilight keeps its navy ground; candles
  `TV_UP #089981` / `TV_DOWN #F23645` unless the trader set colour overrides in Settings; a dotted
  grid (`DOTTED`, still faded by `gridOpacity`); `major` price labels bold and brighter; the
  last-price line dotted in the newest bar's colour; and, while the market is open, the last-price
  tag carries the time left in the bar being built (`format.ts` `barCountdownText`, driven by a
  seconds-only `useFrameClock`). `contrastText` picks black or white tag text from the tag's
  brightness. These colours live outside the theme tokens, which mirror the web stylesheet.
- **Painting**: `mainPainter.ts#paintMain` records grid, series (`paintCandles` draws wick-only below
  3 px spacing, body `floor(0.8 * s)` forced odd), overlays, last-price line/tag, axes, crosshair
  (magnet to close on the price chart) and the DEMO watermark into one `SkPicture` via
  `useDerivedValue(createPicture(...))`. Layers (`host.ts`: `drawings`, `guide`, `orders`) record
  their own pictures and are drawn into the same `Canvas` in that order. Taps are offered to layers
  by priority (`orders 30`, `drawings 10`) before falling through to the crosshair.
- **Clock** (`useFrameLoop.ts#useFrameClock`): a `useFrameCallback` clock that writes `now` only when
  it would move something by half a pixel or change a second label (half rate in performance mode);
  with `secondsOnly` it writes once per second, for the bar countdown.
- **Around the plot** (mobile only): `chart/ChartLegend.tsx` writes the asset, whether it trades
  now, the price and its 24-hour move (the OHLC of the bar under the crosshair while it is out),
  with further rows for indicators and expiries; `ChartToolbar.tsx` is a bar under the chart for
  symbol, timeframe, drawing tools, chart options, drawing undo/redo (3.7) and fullscreen. Beginner
  mode leaves out the drawing tools, undo/redo and the indicators.

---

## 7. Formatting, time, export, search, asset list

### 7.1 Money - `utils/format.ts`

`MINOR = 100`; `toMinor(major) = round(major * 100)`, `fromMinor(minor) = minor / 100` - the only
conversion points for INR, so a display value cannot leak into a request. `formatMoney` uses
`en-IN` currency formatting with exactly 2 decimals (default INR); `formatSignedMoney` prefixes `+`
for positive; `formatMoneyCompact` drops `.00` only for whole amounts (chart tags, presets);
`formatPrice(p, precision)` uses `en-US` with fixed precision; `formatExpiry` (`30s`, `1m`, `1.5m`,
`1.0h`); `formatCountdown` `mm:ss` with `ceil` and clamped at `00:00`. `currencySymbolFor` asks
`Intl.NumberFormat#formatToParts`; mobile adds a `KNOWN_SYMBOLS` fallback (`INR ₹, USD $, EUR €, GBP
£`) for engines without `formatToParts`.

### 7.2 Time - `utils/time.ts`

The server speaks epoch ms; a zone only changes how an instant is written. `SYSTEM_ZONE = 'SYSTEM'`
resolves via `Intl...resolvedOptions().timeZone || 'UTC'`. `TIME_ZONES` lists 11 zones plus system.
`zoneOffsetMinutes` formats the instant in the zone and rebuilds it as UTC (hour 24 normalised),
per instant so DST is correct on both sides. Labels: `zoneOffsetLabel` `UTC+05:30`, `zoneChip`
`UTC+5:30`, `formatClock` `HH:mm:ss`, `formatHourMinute`, `formatDayTime` `5 Sep 12:30`.
`hooks/useCountdown.ts`: `useCountdown(target, 250ms)` recomputes from the wall clock each tick
(correct after backgrounding); `useNow(1000)` aligns ticks to whole seconds so every clock on screen
turns over together (the contract information's "Expires at", the mobile Time box and Time sheet).

### 7.3 USDT amounts - `utils/usdt.ts`

USDT amounts are integer **micro-USDT** (`MICRO_PER_USDT = 1_000_000`, `USDT_DECIMALS = 6`), both
in requests and in responses. No float arithmetic: `parseUsdt` matches
`^(?:(\d+)(?:\.(\d{0,6}))?|\.(\d{1,6}))$`, pads the fraction to 6 digits and converts the digit
string (max 16 digits, must be a safe integer); commas, signs and exponents are refused.
`formatUsdt(micro, {trim, unit, signed})` prints 6 decimals by default (`trim` keeps at least 2),
groups thousands, `--` for non-finite. `toUsdtInput` writes an amount back into a field so it
parses to the same value. INR helpers (`toMinor`, `formatMoney`) are never used on USDT.

### 7.4 CSV - `utils/csv.ts`

`escapeCsvValue`: values starting with `= + - @ \t \r` get a leading `'` (formula injection);
fields with `" , \n \r` are quoted with doubled quotes. `toCsv(rows, columns)` uses CRLF. Web
`downloadCsv` prepends a UTF-8 BOM and clicks a Blob link (revoked after 1 s). Mobile
`shareCsv(filename, csv)` writes the BOM-prefixed file to `Paths.cache` with `expo-file-system` and
opens the share sheet (`expo-sharing`, `text/csv`). The trade history export's Status column uses
`tradeLabel` (5.4).

### 7.5 Day grouping - `utils/dayGroups.ts`

Days are counted in the **chosen display zone**, on calendar keys (`"2026-09-30"`) so DST never
produces a 23/25-hour day. `dayKey`, `shiftDay`, `daysBetween`, `weekdayOf`, `dayLabel` (`Today`,
`Yesterday`, `Mon 28 Sep`, year added when different). `groupByDay(items, timeOf, now, zone)` keeps
input order; an out-of-order item joins its existing group.

### 7.6 Text search - `utils/textSearch.ts`

`nodeText` extracts the words from JSX (Learn guides are searched from the rendered tree, not a
copy). `words` lower-cases, strips accents (NFKD), normalises curly quotes, splits on non
letters/digits. `matchesQuery`: every query word must be a **prefix** of some word in the text
("draw" does not match "withdrawal"; "refun" matches "refunded"); empty query matches all.

### 7.7 Asset list rules - `components/assetList.ts`

- `isOfferable` / `offerableAssets`: launch-scope filter on `pricing.priced` (1.6), applied in
  `api.assets.list`.
- `assetAvailability`: `!marketOpen -> closed`, feed STALE/OFFLINE -> `paused`, else `open`.
  `openCount` counts only `open`. It does not read `pricing.halted` (see Known gaps).
- `bestPayout` = max `payoutPercent` over the asset's payout rows (0 when none can be priced).
  `categoriesOf` in first-seen order. `catalogueOrder` by `displayOrder`.
- `assetsInFilter`: `all | favourites | recent | category:<name>`; Recent keeps recency order.
- `matchesSearch`: case-insensitive substring of symbol or name.
- `sortAssets`: `default` (chip order), `payout` (desc, tie -> `displayOrder`), `name` (numeric,
  base-sensitivity compare, tie -> `displayOrder`), so rows do not reshuffle between refreshes.
- `formatNextOpen(nextOpenAt, now, zone)`: `Opens 14:30` (today), `Opens Mon 09:00` (< 7 days),
  `Opens 12 Oct 09:00`; `null` when unknown or already past.

---

## 8. USDT wallet screens - `features/usdt/*`, `/wallet/usdt/*`

The USDT wallet is a balance of its own (the FUNDING wallet), apart from the INR Live and Demo
accounts in the top bar. The screens are connected: the server mounts the customer routes at
`/wallet/usdt` (`server-go/cmd/server/main.go` `r.Route("/wallet/usdt", usdtapi.Mount)`;
`server-go/internal/api/usdt/routes.go` `Mount`: authenticated, rate-limited; the money-moving
`POST /deposit`, `/withdraw` and `/transfer-to-trading` also require an active user). Server side
flows, webhooks, workers and limits: [usdt-flows.md](../audit/usdt/usdt-flows.md).

**Screens.** Web `pages/UsdtWallet.tsx` (`/wallet/usdt`) and `pages/UsdtDepositStatus.tsx`
(`/wallet/usdt/success?deposit=<id>`); mobile `features/usdt/UsdtWalletScreen.tsx` and
`UsdtDepositStatusScreen.tsx` under `app/(app)/wallet/usdt/`. The wallet page shows the balances,
the history (`History.tsx`: Deposits, Withdrawals and Ledger tabs, 15 rows a page), the networks,
and opens `DepositFlow`, `WithdrawFlow` or `TransferFlow` over the page (a sheet on a phone);
`?flow=deposit|withdraw|transfer` opens one directly once the overview allows it, then drops the
parameter.

**Gates from the overview** (`GET /wallet/usdt`, `UsdtOverview { wallet, trading, networks,
enabled, transferToTradingEnabled }`): `enabled: false` (or a `USDT_DISABLED` error) shows an
"unavailable" panel instead of the wallet; Deposit/Withdraw are disabled when no network has
`depositEnabled`/`withdrawEnabled`; Transfer to Trading is disabled with "Trading with USDT is not
available yet" unless `transferToTradingEnabled` (server `USDT_TRANSFER_TO_TRADING_ENABLED`,
default `false`).

**Queries** (`features/usdt/queries.ts`, identical on both platforms). Everything lives under
`USDT_KEY = ['usdt']`, so a `usdt.updated` or `wallet.updated` event refreshes it all with one
invalidation (2.2). A deposit is re-read every 5 s while `depositNeedsPolling` (money may still
arrive, or it is paid but not yet credited); a withdrawal every 5 s until `isWithdrawalFinal`
(completed, failed, rejected); lists every 12 s while any row is still moving. The overview has
`staleTime 15_000`.

**Moving money once.** Every money call carries an `Idempotency-Key` from `createAttemptKey()`
(`features/usdt/model.ts`): the same request body gets the same key however often it is sent (a
double click, a retry after a timeout), a changed body gets a new key (reusing the old one would be
refused as `IDEMPOTENCY_PAYLOAD_MISMATCH`), and `reset()` after a success makes the next identical
request a new attempt. Mobile builds the key with `expo-crypto` (`checkout.ts`
`createDeviceAttemptKey`; `model.ts` `newIdempotencyKey`). `useSendOnce` additionally blocks a
second send synchronously while one is in flight.

**Flows.**

- **Deposit** (`DepositFlow.tsx`): network (only `depositEnabled` ones) and amount (`parseUsdt`,
  checked against the network's `minDeposit`) -> `POST /wallet/usdt/deposit` -> the provider's
  checkout link and pay address. The link is used only if it is `http(s)` (`safeExternalUrl`) and
  only while `canStillPay` (created, awaiting payment, partially paid). Web opens the checkout in the
  same tab; mobile opens it in the in-app browser (`checkout.ts` `useOpenCheckout`,
  `expo-web-browser`, falling back to `Linking.openURL`), then invalidates `USDT_KEY` and pushes the
  status screen.
- **Deposit status**: arriving there proves nothing. The page asks the server about the deposit
  every 4 s for up to 3 minutes, then offers a button that calls `POST /deposits/:id/refresh` (the
  server answers from the stored record unless 30 s have passed). Only the server's
  `credited === true` reads as completed. Mobile also re-checks when the app returns to the front
  (`AppState`).
- **Withdraw** (`WithdrawFlow.tsx`): network, address and amount, then a review step. The quote
  (`GET /withdrawal-quote`) is fetched for the amount after a 400 ms debounce and counts only for
  exactly the amount on screen. `addressHint` flags a pasted address from the wrong chain (TRC20
  `T...` base58, 34 characters; BSC `0x` + 40 hex) and `withdrawalLimitProblem` checks balance,
  min/max, the daily cap and that the net amount covers the fee - help only; the server's checks
  count. Withdrawals above the quote's `reviewedAbove` are reviewed by staff first.
- **Transfer to Trading** (`TransferFlow.tsx`): an amount up to the available USDT balance, a review
  step, then `POST /transfer-to-trading`; on success both `USDT_KEY` and `['wallet']` are
  invalidated.

**Words and errors** (`model.ts`): deposit and withdrawal statuses have fixed words, tones and
one-line explanations (`depositStatusWord`, `withdrawalStatusExplanation`, ...); ledger rows have
labels and a sign (`ledgerLabel`, `ledgerSign`: CREDIT +1, DEBIT -1, LOCK/UNLOCK 0). Server
messages are shown as they come only for known codes (`KNOWN_ERROR_CODES`); anything else reads
"Something went wrong. Please try again." (`usdtErrorMessage`). Tests:
`client|mobile/tests/usdt-model.test.ts`, `usdt-money.test.ts`.

---

## 9. Theme system

**Tokens.** The palette is a set of named tokens stored as space-separated RGB channels (`"246 216
74"`) so Tailwind/NativeWind can apply alpha (`bg-accent/60`). Groups: `surface-950..500`,
`content-100..400`, `accent(-soft|-solid)`, `up/down(-soft|-solid|-bright)`, `warn`, `critical`,
`brand`, `scrim`, `chart-grid|axis|crosshair|bg|bull|bear|price-line`.

**Three themes + system.** `ThemePreference = 'light' | 'twilight' | 'dark' | 'system'`; `THEMES`
is that list, and `cycle()` steps through it in that order (`light -> twilight -> dark -> system`).
"System" follows the OS live (`watchSystemTheme`) and resolves only to `light` or `dark`.

**Default.** Web: a visitor with no stored preference (or an unknown value) gets `dark`
(`DEFAULT_THEME`, `preferenceFrom()`), the same black-and-yellow as the landing page, whatever the
OS prefers; a stored choice always wins, and a stored `system` still follows the OS. The store and
`public/theme-init.js` apply the same rule, so there is no flash of another theme; `index.css` has
no `prefers-color-scheme` fallback (an attribute-less `:root` is dark), and `index.html` has one
dark `theme-color`. Test: `client/tests/theme-default.test.ts` (runs `theme-init.js` against the
store). The admin console applies the same rule - no stored choice is Full night (`dark`) - in
`admin/src/stores/themeStore.ts` and `admin/public/theme-init.js` (test
`admin/tests/theme-default.test.ts`). Mobile still defaults to `system`.

- **Web** (`client/src/stores/themeStore.ts`): tokens are CSS custom properties in
  `client/src/index.css` (`:root` = dark, `:root[data-theme='twilight']`, `:root[data-theme='light']`).
  The resolved theme is written to `<html data-theme>` and `color-scheme`; the preference to
  `localStorage['bt.theme']`. A blocking script, `client/public/theme-init.js` (loaded by
  `client/index.html` as `/theme-init.js`), applies the same key before first paint - a file, not
  inline, so the site's CSP can stay `script-src 'self'`. Change the key in both places together.
  `utils/theme-colors.ts` reads tokens with `getComputedStyle` and converts to hex/`rgba(a, b, c,
  x)` because chart libraries cannot parse `rgb(1 2 3)` (lightweight-charts throws "Cannot parse
  color").
- **Admin console** (`admin/src/index.css`, same key `bt.theme` on its own origin): the same token
  names and the same brand yellow, but its own, deeper ramp for dense operations screens - a
  near-black page (`surface-950` `8 9 11`) under lighter panels, inputs (`850`) set apart from
  panels, borders (`700`) that outline. One extra token, `on-accent` (near-black), is the text on
  the yellow fill (`accent-solid`: primary buttons, the selected nav item, the active theme
  segment). In Light, `accent-solid` stays yellow while `accent` (text, rings) is a dark gold, so
  all three themes are one brand. Checked by `admin/tests/theme-contrast.test.ts` (WCAG 4.5:1 for
  text on page, panel and input, badge text on its tint, `on-accent` on `accent-solid`, focus ring
  3:1, page/panel/input/divider/border separation); `client/tests/theme-contrast.test.ts` asserts
  that `--brand` (all themes) and the dark themes' `--accent-solid` match the client.
- **Mobile** (`mobile/src/theme/tokens.ts`): `THEME_TOKENS: Record<'dark'|'twilight'|'light',
  TokenTable>` with the same values; `ThemeRoot.tsx` sets them as NativeWind `vars()` on the root
  view (they cascade through context, so sheets/modals get them), sets the status-bar style and the
  system window background. `tokenColor(theme, name, alpha)` serves Skia and other colour props;
  `readChartPalette(theme)` builds the chart palette, which the price chart then dresses with its
  own TradingView look (6.9). `SCRIM_ALPHA = { dark 0.72, twilight 0.7, light 0.45 }`. The store
  persists only `preference` and recomputes `resolved` on rehydrate; system theme comes from
  `Appearance`.

**Parity and contrast tests** (`mobile/tests/theme-contrast.test.ts`, web counterpart
`client/tests/theme-contrast.test.ts` reading `index.css`):

- Parity: parses `client/src/index.css` token blocks (`:root {`, `:root[data-theme='twilight'] {`,
  `:root[data-theme='light'] {`) and asserts every mobile token exists there with the same value.
- WCAG contrast per theme: text pairs >= 4.5:1 (content-100/200/300 on surface-950, content-400 on
  surface-900 and 850, accent/up/down/warn/critical on surface-900); primary button label
  (`surface-950` on `accent-solid`) >= 4.5; white on `down-solid` >= 4.5; white on `up-solid` >= 3
  (large bold text).
- Surface hierarchy: page vs panel >= 1.15, panel border (`surface-700`) vs panel >= 1.4, input
  (`surface-850`) vs panel >= 1.03, border ramp `800 < 700 < 600 < 500` increasingly distinct from
  the panel, scrim luminance < 0.1.

---

## 10. How the React Native app differs, and test setup

**Behaviour that differs from the web client** (verified in code; README "Differences from the web
client" and `PORTING.md` list the layout ones):

| Area | Mobile | Web | Where |
|---|---|---|---|
| Token storage | device keychain (`expo-secure-store`) with an in-memory mirror, loaded before `hydrate` | `localStorage` | 1.2, `services/http.ts` |
| Session restore | `hydrate` retries once after 1.5 s and keeps the tokens unless the server refused them (401/403) | one try; any failure clears the tokens | 1.3, `stores/authStore.ts` |
| Server address | absolute `https://wintratrade.online/api` and `wss://wintratrade.online/ws` unless `EXPO_PUBLIC_*` is set; `/health` warm-up at start | relative `/api` and `/ws` on the page's host (the VPS nginx serves both), unless `VITE_API_URL`/`VITE_WS_URL` are set at build (`render.yaml`) | 1.3, `mobile/src/config.ts` |
| Live prices | newest tick per asset, flushed at most every 250 ms (`applyTicks`) | every tick (`applyTick`) | 2.3 |
| Send lock | `sendingRef` checked in `press` and `send`, cleared with the 350 ms cooldown | `pending` state and disabled buttons | 4.7 |
| Ticket layout | Amount box, trade-history button and Time box (live end time), each opening a sheet; Time sheet grouped by length; blocked reason and UP/DOWN pinned under a scrolling set-up | duration chips (4 + More), amount field with stepper and presets in the ticket | 4.1, 4.4 |
| Preview | on press-in, cleared on release/slide-off one tick later | mouse hover or keyboard focus only | 4.7 |
| Haptics | light impact when an order is sent; success/error/warning notification on a result (`TradeResultToasts.tsx`, skipped when results are hidden); always `.catch`-guarded | none | 4.7 |
| Chart | Skia engine with TradingView look, legend, bottom toolbar, fullscreen, drawing undo/redo | `lightweight-charts`, two-chart layout | 6.9, 3.7 |
| USDT checkout | in-app browser, then the status screen; re-checks when the app returns to the front | same-tab redirect, provider returns to the status page | 8 |
| Random ids | `expo-crypto` `randomUUID` (trade idempotency keys, drawing ids, USDT attempt keys) | `crypto.randomUUID` | 4.7, 8 |

Layout and platform differences:

- **Phone layout only**: the < 640 px variant of every responsive web layout; no split two-chart
  view, no icon rail, no wide tables. Tabs (`app/(app)/(tabs)`): Home, Markets, Trade, Trades,
  Account; other screens are pushed. Overlays mounted once in `app/(app)/_layout.tsx`:
  `SettingsDrawer`, `TransferModal`, `TradeResultToasts`, `Toaster`.
- **Sheets** (`components/ui/Sheet.tsx`) replace web popovers, dropdowns and modals.
- **Deposit proof is image-only**: `expo-image-picker` from the photo library, single image,
  `Compatible` representation (HEIC -> JPEG); types PNG/JPEG/WebP, max 5 MB
  (`features/transfer/Transfer.tsx` `MAX_PROOF_BYTES`, `PROOF_TYPES`). Web also accepts PDF.
- **In-app date pickers**: `features/trades/DateRangeField.tsx` opens a month calendar in a sheet,
  values are calendar keys (`YYYY-MM-DD`), start <= end enforced.
- **CSV** opens the share sheet (`shareCsv`) instead of downloading.
- Tab screens stay mounted, so polling screens use `hooks/useScreenFocused.ts` to pause when another
  tab is in front; `hooks/usePullRefresh.ts` shows a spinner only for a pull, not for polls.
- Drawings and order markers are hit by touch only.
- AsyncStorage for persisted stores (3).

**Test setup.** Both apps run vitest over pure-logic suites (`client/tests/*.test.ts`,
`mobile/tests/*.test.ts`; shared suite names: active-trades, asset-list, auth-schemas, chart-colors,
chart-geometry, csv, day-groups, duration-row, indicators, landing-markets, live-payout, live-result, redraw,
store-selectors, text-search, theme-contrast, time, trade-ticket, trade-window, usdt-model,
usdt-money, void-and-scope; mobile adds drawings-history and time-sheet). `mobile/vitest.config.ts`
runs in `node` and aliases native packages to in-memory stand-ins under `mobile/tests/mocks/`:
`@react-native-async-storage/async-storage`, `expo-secure-store`, `expo-crypto` (Node
`randomUUID`), `expo-constants`, `expo-file-system`, `expo-sharing` (`isAvailableAsync -> false`)
and `react-native` (`Appearance` fixed to dark, `Platform`). Commands: `npm test` and `npm run
typecheck` in `mobile/` (which is not part of the root npm workspace), the same in `client/`.
The admin console has its own suites (`admin/tests/`: adjustment, freshness, money-format,
theme-contrast, theme-default, usdt, void-status).

---

## 11. Admin console: where it mirrors the apps

Only the parts that show operators what customers see, or that set the rules the apps display.
Roles and permissions: [permission-matrix.md](../audit/security/permission-matrix.md).

**Voids** (`admin/src/utils/tradeStatus.ts`). `isVoided(trade)` is the apps' rule (status `DRAW`
and `voided === true` or `expiryPriceSource === 'NO_PRICE_VOID_REFUND'`); `VOID_LABEL =
'VOID · REFUNDED'`. Test: `admin/tests/void-status.test.ts`.

- `pages/Trades.tsx`: a voided row shows a warn badge with the `voidReason` as its tooltip instead
  of the DRAW status badge; the status filter's DRAW option reads "Draw / void".
- `pages/TradeDetail.tsx`: the void badge; "Result" reads "Void · stake refunded"; "Why voided"
  (`voidReason`, fallback "No valid market price at expiry"); "Settlement rule"
  (`expiryPriceSource`); "Tie rule (frozen)" (`trade.drawPolicy`, or "policy at settlement" for a
  trade accepted before the policy was stored). A **Pricing** panel shows the offer the trade was
  accepted on (or "Priced at acceptance (the app sent no offer)"), the locked and the displayed
  payout, the offer's reference price, the mid at acceptance, the slippage and the engine's
  components and guard.

**Not priced** (`pages/Assets.tsx`, `pages/AssetDetail.tsx`). An asset with `priced === false`
carries a "Not priced" warn badge with `notPricedReason` as its tooltip - the assets the apps hide
(1.6).

**Payout changes, now or scheduled** (`pages/AssetDetail.tsx`). The payout table shows each
expiry's base payout and, from the live pricing board (`GET /admin/pricing?assetId=`, every 2 s,
permission `payouts:read`), its min/max, margin and the live UP/DOWN payout or refusal code. The
form takes the base payout, optional min/max/margin (empty = the instrument rule's default) and
"Takes effect" (a local date-time; empty = now), sent as `effectiveFrom` to `POST
/admin/assets/:id/payouts`. A future time creates a `SCHEDULED` row ("Payout change scheduled"
toast), shown with a badge and a **Cancel** button (permission `payouts:write`; `DELETE
/admin/assets/payouts/:id`); cancelled and abandoned rows say so; an activated row that was
scheduled shows "scheduled for ...". The server activates due rows from the leader loop
`payout-scheduler` every second and refuses a second change for the same slot while one is
scheduled (409) (`server-go/internal/service/payouts/schedule.go` `ActivateDuePayouts`). The
apps see a change only when it becomes the active row (1.6).

**Settlement policy and feed data rules** (`pages/Settings.tsx`, `PUT /admin/settings/risk`). The
risk settings page edits, as new versions of the risk configuration:

- *Early close (Sell now)*: among its fields `earlyCloseFloorMs` (min 1000),
  `earlyCloseMaxTickAgeMs`, `earlyCloseQuoteMaxAgeMs`, `earlyCloseQuoteTolerancePercent` - the Sell
  now rules of 4.8.
- *Settlement policy*: `settlementMaxTickAgeMs`, `settlementConfirmationWindowMs`,
  `settlementDeferWindowMs`, `settlementRetryDelayMs`, `settlementMaxClockSkewMs`, and
  `settlementNoPriceRule` (`VOID_REFUND`, or `STALE_TICK_THEN_VOID`) - what decides a void (5.4).
- *Market data thresholds* (healthy/degraded/stale/offline bands; "the FEED_* environment values
  only seed the first version") and *Feed data rules*: `feedTickHistoryRetentionMs`,
  `feedGapMinMs`, `feedGapIntervalMultiple`, `feedSaturationHoldMs`, `feedMaxClockDriftMs`,
  `feedClockCheckIntervalMs`.
- The tie rule `drawPolicy` (*Trade limits*; the apps state it, 5.3).

Money fields are typed in rupees and sent in paise (`toMinor`), as the API stores them; the field
shows a `₹` prefix and, under it, the amount with Indian grouping and in lakh/crore
(`utils/format.ts` `formatRupees`, `rupeesInWords`). Other fields carry their unit inside the
input (`%`, `s`, `ms` - read out as s/min/h by `formatMs` - and `×`); a "0 = off" field at 0 shows
an "Off" badge. Each group is a card with a one-line description, and a sticky bar counts the
fields that differ from the published version (each field carries it as `data-published`), with
**Discard** and **Publish new version**. Display only: the request body is unchanged.

Fields an older server does not send are neither rendered nor sent. The server enforces hard limits
(`server-go/internal/service/riskconfig/policy_validate.go`); see
[disclosures.md](../audit/customer/disclosures.md) and [feed-rules.md](../audit/operations/feed-rules.md).

**Money corrections** (operator side; [02-money-wallets-payments.md](02-money-wallets-payments.md)
sections 7 and 9.6).

- `pages/UserDetail.tsx` *Manual wallet adjustment*: the wallet comes first - Live (INR), Demo
  (INR) or USDT wallet (FUNDING) - and the amount is typed in that wallet's currency and never
  converted (`utils/adjustment.ts` `parseAdjustmentAmount`: up to 2 decimals for INR, up to 6 for
  USDT read as a digit string, so `0.1` USDT is exactly 100,000 micro-USDT); the form shows that
  wallet's balance and what the adjustment adds or removes, and sends `accountType` with the
  usual per-attempt `Idempotency-Key`. Test: `admin/tests/adjustment.test.ts`.
- `pages/usdt/UsdtWithdrawals.tsx`: a withdrawal whose BeeX answer did not match
  (`operatorResolvable`) shows the mismatch in a warning and **Mark completed** / **Mark failed**
  (permission `withdrawals:approve`); `ResolutionDialog` restates the amount, the user and what
  happens to the reserved funds, requires a note of what was checked with BeeX and, for
  completed, takes an optional transaction hash. A resolved withdrawal shows who resolved it, when
  and the note.

**Data quality** (`pages/MarketData.tsx` `FeedQualityPanel`, from the `quality` block of `GET
/admin/market-data`): the clock check (state, offset, drift, source), the ingestion buffer's
high-water mark against its cap, and the total of dropped ticks; per asset the state (`LIVE`,
`QUIET`, `GAP`, `DISCONNECTED`, `NO_DATA`, `NOT_EVALUATED`; an open gap with its start), expected
interval, gap threshold, updates/heartbeats, dropped, rejected and the active block (code and
detail - the reason the apps' feed reads STALE, 4.6); and the recent gaps with reason, last data
before, first data after and duration.

---

## 12. Where to look

"Same" means the mobile file is byte-identical; "platform" means it differs only in timers,
storage or the random-id source.

| Topic | Web | Mobile | Identical? |
|---|---|---|---|
| HTTP client, tokens, refresh | `client/src/services/http.ts` | `mobile/src/services/http.ts` | no (storage, multipart) |
| Endpoints | `client/src/services/api.ts` | `mobile/src/services/api.ts` | no (`UploadFile`) |
| API URL / WS URL | `VITE_API_URL`, `VITE_WS_URL` | `mobile/src/config.ts` | - |
| App boot, QueryClient | `client/src/main.tsx`, `client/src/App.tsx` | `mobile/src/app/_layout.tsx` | - |
| Auth store | `client/src/stores/authStore.ts` | `mobile/src/stores/authStore.ts` | no (hydrate retry) |
| Websocket | `client/src/websocket/client.ts` | `mobile/src/websocket/client.ts` | no (URL, timers) |
| Realtime bridge | `client/src/hooks/useRealtime.ts` | `mobile/src/hooks/useRealtime.ts` | no (250 ms tick batching) |
| Live feeds, connection, open trades, trading wallet | `client/src/features/trade/use{LiveFeeds,ConnectionState,ActiveTrades,TradingWallet}.ts` | same paths | same (`useLiveFeeds`: platform) |
| Stores | `client/src/stores/*.ts` | `mobile/src/stores/*.ts` (+ `storage.ts`) | logic same except `marketStore.applyTicks` and drawing undo/redo; storage/theme differ |
| Catalogue, launch scope, list rules | `client/src/components/assetList.ts` | same | same |
| Landing market list | `client/src/features/landing/{markets.ts,useLandingBoard.ts}`, `client/src/pages/Landing.tsx` | `mobile/src/features/landing/{markets.ts,useLandingBoard.ts}`, `LandingScreen.tsx` | `markets.ts`, `useLandingBoard.ts` same |
| Ticket rules | `client/src/features/trade/ticketModel.ts`, `ticketStore.ts` | same | `ticketStore` same; `ticketModel` + `durationWords`/`expiryGroups` |
| Live offers | `client/src/features/trade/livePayout.ts` | same | same |
| Ticket component | `client/src/features/trade/TradeTicket.tsx`, `ContractInfo.tsx`, `ConfirmTradeSheet.tsx`, `DurationSheet.tsx` | `mobile/src/features/trade/TradeTicket.tsx`, `AmountSheet.tsx`, `TimeSheet.tsx`, ... | no |
| Sell now | `client/src/features/trade/PositionsPanel.tsx` | same path | no (UI) |
| Projection / settlement / tie rule | `client/src/features/trade/{liveResult,settlement,drawRule,announceResult}.ts` | same | same |
| Status words, voids | `client/src/utils/tradeStatus.ts` | same | same |
| Main chart | `client/src/features/trade/PriceChart.tsx`, `ChartToolbar.tsx` | `mobile/src/features/trade/PriceChart.tsx`, `ChartToolbar.tsx`, `chart/ChartLegend.tsx` | no |
| Indicators | `client/src/utils/indicators.ts`, `features/trade/chart/indicatorCatalog.ts` | same | same |
| Time projection | `client/src/features/trade/chart/timeProjection.ts` | same path (worklets + `*Times`) | no |
| Drawings | `chart/drawingGeometry.ts`, `chart/DrawingLayer.tsx`, `stores/drawingsStore.ts` | same | geometry same; store + undo/redo |
| Expiry guide | `chart/ExpiryGuide.tsx`, `chart/useFrameLoop.ts` | same paths (`useFrameClock`) | no |
| Orders overlay | `orders/orderModel.ts`, `orders/ChartOrderLayer.tsx`, `orders/useOrderProjection.ts` | same | model same |
| Trade detail chart | `chart/tradeWindow.ts`, `chart/TradeWindowChart.tsx` | same | model same |
| Redraw throttle | `client/src/utils/redraw.ts` | same | same |
| Skia engine, chart look | - | `mobile/src/features/trade/chart/engine/*` (`colors.ts`, `mainPainter.ts`, `scale.ts`, `format.ts`) | mobile only |
| Formatting | `client/src/utils/{format,time,usdt}.ts` | same | `format` differs (symbol fallback) |
| CSV, days, search | `client/src/utils/{csv,dayGroups,textSearch}.ts` | same | `csv` differs (share sheet) |
| USDT screens | `client/src/pages/{UsdtWallet,UsdtDepositStatus}.tsx`, `client/src/features/usdt/*` | `mobile/src/features/usdt/*` (+ `checkout.ts`), `mobile/src/app/(app)/wallet/usdt/*` | `queries.ts` same; `model.ts` platform |
| Theme | `client/src/stores/themeStore.ts`, `client/src/index.css`, `client/public/theme-init.js`, `client/src/utils/theme-colors.ts` | `mobile/src/stores/themeStore.ts`, `mobile/src/theme/{tokens.ts,ThemeRoot.tsx}`, `mobile/src/utils/theme-colors.ts` | no |
| Admin mirrors | `admin/src/utils/tradeStatus.ts`, `admin/src/pages/{Trades,TradeDetail,Assets,AssetDetail,Settings,MarketData}.tsx` | - | - |
| Tests | `client/tests/*.test.ts` | `mobile/tests/*.test.ts`, `mobile/tests/mocks/*`, `mobile/vitest.config.ts` | suites mirrored |
| Port contract | - | `mobile/PORTING.md`, `mobile/README.md` | - |

---

## 13. Known gaps

Checked against the code in the working tree. None of them lets an app change a price, a payout
or a result - the server decides all three - but each can make a screen say something less exact
than it could.

1. **History of instruments no longer offered loses its precision.** `api.assets.list` hides
   unpriced assets and the server lists only active ones, so a past trade on EUR/USD, GBP/USD or
   XAU/USD finds no catalogue row: prices fall back to 2 decimals (`pages/Trades.tsx` `precisionOf`,
   `pages/TradeDetail.tsx` `asset?.pricePrecision ?? 2`; mobile `TradesScreen.tsx`,
   `TradeDetailScreen.tsx`) and the history's asset filter cannot select them.
2. **Lists do not say why an instrument cannot trade.** `assetAvailability` reads only
   `marketOpen` and the feed status, so a halted instrument (`pricing.halted`) reads "open" on the
   asset rows (`components/AssetRow.tsx`: Markets, Home, the asset browser) until the ticket shows
   its durations as unavailable with the engine's reason; and a feed blocked by the data rules
   shows only "Price feed stale - trading paused", without the server's `blockReason` (dropped
   ticks, clock drift).
3. **Mobile keeps polling the trade screen in the background tabs.** Tab screens stay mounted, and
   the Trade screen does not use `useScreenFocused`: once visited, its catalogue query (every 4 s)
   and the ticket's offer query (`GET /binary/quotes`, every second) keep running while another tab
   is in front (`mobile/src/features/trade/TradeScreen.tsx`, `TradeTicket.tsx`). The Home, Markets
   and Trades screens do pause.
4. **The web signs out on a failed page load.** `client/src/stores/authStore.ts` `hydrate` clears
   the stored tokens on any failure of `/auth/me`, a network error or a 5xx included; the mobile app
   keeps them unless the server refused them (1.3).
5. **Web tokens are readable by page scripts and the websocket token travels in the URL.** The web
   client keeps both tokens in `localStorage` and every app sends the access token as `?token=` on
   `/ws`. Mitigations in place: the site's CSP (`script-src 'self'`) and an nginx log format that
   drops the query string for `/ws`; see [threat-model.md](../audit/security/threat-model.md) R3, R4.
6. **The feed indicator is stricter than the server.** The ticket greys out trading while the
   asset's feed status (from the catalogue, every 4 s) is `STALE` or `OFFLINE`, and `STALE` starts
   at 300 ms of effective age; the server accepts a price up to 2 s old under the one freshness
   rule it applies to quotes and trades ([01-trading-core.md](01-trading-core.md) 2.4). The quote
   the ticket polls every second is authoritative; the indicator can briefly say "trading paused"
   while an offer is available.
7. **No USDT trading in the apps.** The ticket trades only the INR Live and Demo accounts
   (`useAccounts` drops USDT wallets, `useTradingWallet`), and the USDT screens keep Transfer to
   Trading disabled unless the server sets `USDT_TRANSFER_TO_TRADING_ENABLED` (default `false`);
   a USDT trading balance, if one exists, is shown on the USDT page only.
