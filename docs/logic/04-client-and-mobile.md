# 04 - Client-side logic: web (`client/src`) and mobile (`mobile/src`)

This document describes the logic that runs in the two clients of the platform:

- **Web**: `client/src`, React + Vite + Tailwind, chart on `lightweight-charts`.
- **Mobile**: `mobile/src`, Expo SDK 54 / React Native 0.81 / expo-router 6 / NativeWind 4, chart on
  Skia + Reanimated. It is a feature-for-feature port of the web client; the port contract lives in
  `mobile/PORTING.md` and the run/build notes in `mobile/README.md`.

Both clients talk to the same Go server (`server-go/`). Most non-UI modules are **byte-identical
copies** in both trees (see the table in section 10). When this document cites `client/src/...`
without saying otherwise, the same file exists at the same relative path under `mobile/src/`.

> **The governing rule.** The client never decides a price, a time or a payout. Prices come from
> the server's ticks and candles, expiries from `entryTimestamp`/`expiryTimestamp` on the trade
> row, payouts from `asset.payouts` and the trade's own `profit`/`potentialReturn`/`settlementAmount`.
> Everything the client "computes" is either a validation that mirrors a server rule (so the trader
> gets a message before a round trip), a projection that is explicitly labelled as one ("If it ends
> now"), or a drawing aid (indicators, drawings) that is never sent anywhere. The only price the
> client ever sends is `quotedPrice`, and it is a slippage guard, not a fill price.

---

## 1. Data layer

### 1.1 HTTP client - `services/http.ts`

An axios instance with `timeout: 15_000` and JSON content type.

- **Base URL**: web uses `import.meta.env.VITE_API_URL ?? '/api'` (Vite proxy); mobile uses
  `API_URL` from `mobile/src/config.ts` (absolute, see 1.3).
- **Request interceptor**: attaches `authorization: Bearer <accessToken>` from `loadTokens()`.
- **Success envelope**: the server answers `{ success: true, data }`. The helpers `apiGet`,
  `apiPost(url, body, headers?)`, `apiPatch`, `apiPut`, `postForm` all return `response.data.data`,
  so callers deal in plain data.
- **Error envelope -> `ApiRequestError`**: `{ success: false, error: { code, message, details } }`
  becomes `new ApiRequestError(code, message, status, details)`. Anything without that envelope
  becomes `ApiRequestError('NETWORK_ERROR', ...)`; axios' `"Network Error"` is reworded to
  `"Cannot reach the trading server"`. `status` is `0` when there was no HTTP response.

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

### 1.3 Mobile API URL and Render cold start

`mobile/src/config.ts`:

- `API_URL = trimSlash(EXPO_PUBLIC_API_URL || 'https://binary-trading-api-ih4v.onrender.com')`.
- `WS_URL = trimSlash(EXPO_PUBLIC_WS_URL || API_URL.replace(/^http/, 'ws') + '/ws')`.
- `APP_SCHEME = 'binarytrading'` (deep links, matches `app.json`).

The free Render instance sleeps when idle (README: first request 30-60 s). Two mitigations:

1. **Wake-up ping**: at module load of `app/_layout.tsx`, `fetch(API_URL + '/health').catch(() =>
   undefined)`. Nothing awaits it; it just makes the server likely awake by the time the user has
   typed a password.
2. **Hydrate retry that keeps the session** (`mobile/src/stores/authStore.ts`): `hydrate()` tries
   `api.auth.me()` up to **2 times**, waiting **1500 ms** after the first failure. A `401`/`403`
   clears the tokens (the session was refused). Any other failure (network, 5xx, timeout) leaves
   the tokens in SecureStore - "unreachable is not refused" - so the next launch can restore the
   session; the in-memory status still ends `anonymous` for this launch. The web `hydrate()` has no
   retry and clears tokens on **any** failure.

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

- `trades.create(input, idempotencyKey)` sends header `idempotency-key`; `input.quotedPrice` is the
  slippage guard; `accountType` defaults to LIVE server-side.
- `market.candles(assetId, limit = 300, interval = 60, to?)`; `market.ticks(assetId, {from, to,
  windowMs})` - the server keeps only 15 minutes of raw ticks.
- USDT money calls take an `Idempotency-Key`; amounts are integer micro-USDT.
- Mobile `transfers.deposit` takes `proof: UploadFile { uri, name, type }` instead of a `File`.

**QueryClient defaults** (identical in `client/src/main.tsx` and `mobile/src/app/_layout.tsx`):
`refetchOnWindowFocus: false` (live data comes over the socket), `staleTime: 5_000`, and
`retry: (n, err) => 4xx ? false : n < 2`.

**Query keys in use** (the invalidation targets matter, because the realtime bridge hits them):

| Key | Used for |
|---|---|
| `['assets']` | asset list (also read by `useDrawPolicy`, `staleTime 60_000`) |
| `['wallet', 'accounts']` (`ACCOUNTS_KEY`) | both INR balances; any `['wallet']` invalidation reaches it |
| `['wallet', 'transactions', {...}]` | ledger pages |
| `['trades', 'open']` | open trades (nav count + trade screen) |
| `['trades', filters, page]`, `['trades', 'summary', filters]`, `['trades', 'recent-settled', account]` | history |
| `['trade', id]`, `['trade', id, 'quote']` | trade detail, early-close quote |
| `['trade-window', id]` | trade detail chart data |
| `['candles', assetId, timeframe]` | main chart history |
| `['market', 'health']`, `['notifications']`, `['transfers']`, `['payment-methods']`, `['profile']` | misc |
| `['usdt', ...]` (`USDT_KEY`, `features/usdt/queries.ts`) | every USDT query under one root |

---

## 2. Realtime bridge - `hooks/useRealtime.ts` + `websocket/client.ts`

### 2.1 The socket (`websocket/client.ts`)

A singleton `TradingSocket`. URL: web `VITE_WS_URL ?? (wss|ws)://<host>/ws`, mobile `WS_URL`; the
access token goes in the query string (`?token=`). `connect()` is a no-op without a token or when a
socket already exists.

- **State**: `connecting | open | closed`, derived from `readyState`; `onStateChange` subscribers.
  `features/trade/useConnectionState.ts` reads it through `useSyncExternalStore` so every component
  agrees on the state in the same render (the ticket's banner and its disabled buttons cannot
  disagree).
- **Heartbeat**: on open, `{type:'ping'}` every **25 s**.
- **Reconnect/backoff**: `onclose` (which always follows `onerror`) tears down and, unless
  `closedByUser`, schedules `delay = min(1000 * 2^attempts, 15_000)` ms (2 s, 4 s, 8 s, 15 s, ...);
  `attempts` resets to 0 on a successful open.
- **Resubscribe**: the set of channels survives a drop; on open the whole set is re-sent as one
  `{type:'subscribe', channels:[...]}`. `subscribe()` only sends channels not already in the set;
  `unsubscribe()` always sends. The set is **not reference-counted** (see 2.4).
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
| `market.tick` | `useMarketStore.applyTick(tick)` (latest tick + previous price per asset) |
| `wallet.updated` | invalidate `USDT_KEY`; if `currency === 'USDT'` **stop** (never file a USDT balance in an INR slot); else default missing `accountType` to `LIVE`, `useAccountStore.upsertWallet`, and patch `ACCOUNTS_KEY` in place (`mergeWallet` replaces the entry of the same type) |
| `usdt.updated` | invalidate `USDT_KEY` (record, lists and balances refetch together) |
| `trade.created` | `useTradeStore.upsertTrade`, invalidate `['trades']` |
| `trade.updated` | `useTradeStore.upsertTrade` |
| `trade.settled` | `useTradeStore.settleTrade`, `announceResult(trade)`, `setQueryData(['trade', id], trade)`, invalidate `['trades']` and `['wallet','transactions']` |
| `support.message` | `useSupportStore.addMessage` |
| `support.conversation` | `useSupportStore.setConversation` |
| `support.typing` | only when `side === 'AGENT'`: `setAgentTyping(name ?? 'Support')` |
| `notification.created` | invalidate `['notifications']` |

On cleanup (sign-out or user change): unsubscribe handlers, `socket.disconnect()`, reset the
support, account-balance and legacy wallet stores. The **active account choice survives** sign-out
(it is a device preference).

A second effect keeps the legacy single-wallet store (`stores/walletStore.ts`) pointed at the
**active** account's wallet, so older readers follow a DEMO/LIVE switch without knowing about
accounts.

### 2.3 Account loading fallback (`useAccounts`)

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

### 2.4 `useLiveFeeds` subscription rules

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
| `drawingsStore` | `bt.drawings` (version 1) | `drawings` only |
| `favouritesStore` | `bt.favourites` | `favourites`, `recent` |
| `onboardingStore` | `bt.onboarding` | `completed` |
| `themeStore` | `bt.theme` | web: raw string in localStorage; mobile: `{preference}` via persist |
| `authStore`, `tradeStore`, `tradeResultStore`, `orderPreviewStore`, `marketStore`, `walletStore`, `supportStore`, `ticketStore` | - | memory only |

### 3.1 `accountStore` - active DEMO/LIVE

`active: AccountType` defaults to **`'DEMO'`**: "nobody should learn the product with real money".
`wallets: Partial<Record<AccountType, Wallet>>` keyed by `accountType ?? 'LIVE'`. Every trade is
placed on `active` (read at send time, see 4.6). `features/account/funds.ts`:
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
no-op for an id already seen, because a settlement can arrive twice (socket `trade.settled` and an
early close resolving from its own response). `announceResult()` (`features/trade/announceResult.ts`)
is the only entry point. Results do not time out; the newest stays until dismissed, older ones fold
as "+N more" (`TradeResultToasts.tsx`).

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
the preview if it is **still** REJECTED, so a new order placed meanwhile survives.

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
lists, seeds the first two assets when nothing valid remains, and fixes an invalid active id.

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

**Migration v0 -> v1** (`migrateDrawings`): v0 stored `levels: { [assetId]: [{id, price}] }`.
Each finite price becomes `{ id (kept or new), kind: 'hline', points: [{ time: 0, price }] }`,
capped at 50 per asset; unreadable entries are dropped.

### 3.8 `favouritesStore`

Optimistic: `toggle` updates locally, then `PUT /users/me/favourites` fire-and-forget. `load()`
replaces the local list with the server's (called by `useRealtime` on sign-in); a failed load keeps
the local list. `recent` is device-only, most recent first, `MAX_RECENT = 8`.

### 3.9 `themeStore`

See section 8.

### 3.10 `supportStore`

Held in a store because messages arrive from three directions (own send, server echo, agent reply).
`merge(existing, incoming)` keys by `id` and sorts by `createdAt` then `id`, so an echo never
duplicates. `pending: { tempId, body, createdAt, failed }[]` for unsent messages;
`resolvePending(tempId, message?)` removes the pending entry and merges the confirmed message;
`failPending` flags it. Typing: `setAgentTyping(name)` sets `agentTypingUntil = now + 5000`
(`TYPING_TTL_MS`, the server repeats the signal while typing continues); an `AGENT` message clears
it immediately. `reset()` on sign-out.

### 3.11 `onboardingStore`

`completed` per **device**, not per account (it is an explanation, not a consent record). Replaying
from Learn does not reset it.

### 3.12 Others

`marketStore`: `ticks[assetId]`, `previous[assetId]` (for up/down flash), `feeds`. `walletStore`:
legacy single wallet + `lastChangeAt`. `ticketStore`: section 4.

---

## 4. Trade ticket - `features/trade/ticketModel.ts`, `ticketStore.ts`, `TradeTicket.tsx`

`ticketModel.ts` is pure and identical on both platforms; the component only renders its answers.
All money is integer **minor units** (paise).

### 4.1 Durations

- `durationChips(asset)`: one chip per `asset.supportedExpiries`, sorted ascending; `payoutPercent`
  from `asset.payouts.find(p => p.expirySeconds === s)`; a duration without a payout stays visible
  but disabled with `unavailableReason = 'No payout for <label>'`.
- `durationLabel(s)`: `formatExpiry` below an hour (`30s`, `1m`, `1.5m`), `"1h"` / `"1h 30m"` above.
- `pickDuration(chips, wanted)`: `wanted` if usable, else the first usable, else the first chip.
  The ticket replaces an unusable duration in `ticketStore` **without** touching the saved default
  (another asset may offer it).
- `durationRow(chips, selected, max = 4)`: up to 4 chips; when more exist, the rest go behind
  "More" (`hidden` count), and a selected long duration takes the last slot so the selection is
  always visible.

### 4.2 Payout and stake limits

`payout = asset.payouts.find(p => p.expirySeconds === expirySeconds)`.
`stakeLimits(asset, payout) = { min: max(asset.minStake, payout.minStake), max: min(asset.maxStake,
payout.maxStake) }` (missing values default to 0 / `MAX_SAFE_INTEGER`).

`contractTerms(stake, payoutPercent)`:

```
profit      = round(stake * payoutPercent / 100)
totalReturn = stake + profit
maxLoss     = stake
```

The ticket's preview draft uses the same formula for `potentialReturn`. These are displays of the
server's configured rate; the accepted trade's own `profit`/`potentialReturn` are what count.

### 4.3 Amount: presets, Max, parsing, validation

- `PRESET_AMOUNTS_MAJOR = [100, 500, 1000, 5000]` -> minor, **filtered** to `[min, max]` (a preset
  the server would refuse is hidden, not disabled).
- **Max** = `min(available, limits.max)`, added only when `>= limits.min`, `> 0` and not equal to an
  existing preset.
- `parseAmount(text)`: strips grouping commas; accepts `^\d*(\.\d{0,2})?$` (not a lone `.`);
  returns `toMinor(value)` or `null`.
- `amountState(text, limits, available)` - checked in the order a trader can fix them:
  `empty -> invalid (null or <= 0) -> below-min -> above-max -> insufficient -> valid`.
  Insufficient balance is reported last on purpose: telling someone to deposit for an amount the
  platform would refuse anyway sends them to the deposit screen for nothing.
- `amountMessage(state, currency)`: the copy, e.g. `"Not enough balance. Available: ₹1,234.00"`.
- Stepper: `amountStep(v)` = 1000 major when `v >= 10_000`, 500 when `>= 1000`, else 100;
  `stepAmount` clamps to `[min, max]`. `amountText(minor)` writes back without trailing `.00`.
  Writing an amount via preset/stepper also saves it as `settings.defaultStake`.

### 4.4 `ticketStore`

Holds `amountText` (the raw field text, so empty/half-typed is a nameable state), `expirySeconds`
and `focusRequest`. Initialised from `settings.defaultStake` / `defaultExpirySeconds`. `setExpiry`
also saves the default; `prefill(stake, expiry)` ("Trade Again" from a result card in the shell)
saves both defaults and bumps `focusRequest`, which makes the ticket scroll into view and focus the
amount.

### 4.5 Blocking

```
feedBlocked   = !asset || feed.status in {OFFLINE, STALE}
blockedReason = !asset             -> 'Choose an asset'
              : !asset.marketOpen  -> 'Market closed'
              : connection != open -> 'Live prices disconnected — reconnecting…' (also a banner)
              : feedBlocked        -> 'Price feed <status> - trading paused'
              : !payout            -> 'This duration is not available right now'
cannotTrade   = !!blockedReason || stake === null
```

`components/assetList.ts#assetAvailability` applies the same rule to list rows
(`closed | paused | open`).

### 4.6 Placing: confirm vs one-tap, idempotency, quotedPrice, cooldown

```
press(dir):   if pending || cannotTrade: return
              confirmBeforeTrade ? open ConfirmTradeSheet(dir) : send(dir, livePrice)
confirm():    "don't ask again" -> toggleConfirm();  send(dir, reQuote?.serverPrice ?? livePrice)
send(dir,p):  setPending(dir); orderPreview.commit(draft, p); mutate({dir, quotedPrice: p ?? undefined})
```

- `livePrice = marketStore.ticks[asset.id].price ?? asset.feed.lastPrice ?? null` - the price on
  screen. With no tick at all, `quotedPrice` is omitted and the server applies no guard.
- **Idempotency**: a fresh `crypto.randomUUID()` (mobile: `expo-crypto`) per attempt. The server
  collapses retries of the same attempt; a deliberate second trade gets a new key.
- `accountType` is read from `useAccountStore.getState().active` at send time, not from the render
  closure.
- **Success**: clear re-quote/sheet error, `orderPreview.resolve()`, `tradeStore.upsertTrade`,
  invalidate `['wallet']` and `['trades']`, toast with the **server's** `entryPrice` and
  `potentialReturn`. Nothing is shown as accepted before this.
- **Failure**: `orderPreview.reject()` (cancelled marker). `PRICE_MOVED` with
  `details.serverPrice` sets a re-quote offered back to the trader at the new price; any other error
  goes into the sheet (confirm mode) or a toast (one-tap). The re-quote is cleared when asset,
  expiry or amount changes.
- **Cooldown**: `onSettled` keeps `pending` set for **350 ms** more; the buttons stay disabled from
  the press until then, so a double tap is never two trades.
- **Preview hooks**: web shows an INTENT preview on mouse hover or keyboard `:focus-visible` only
  (touch emits no matching leave). Mobile shows it on `onPressIn` and clears on release/slide-off
  unless the confirmation opened. While the confirmation is open the preview is re-shown.
  Changing asset or account clears the intent and closes the confirmation; unmount clears it.
- Mobile adds a light haptic on placement (`Haptics.impactAsync(Light)`, errors swallowed).

### 4.7 Early close ("Sell now")

`PositionsPanel.tsx#OpenPosition`: query `['trade', id, 'quote']` -> `api.trades.quote(id)`,
`enabled: running && status === 'OPEN'`, `refetchInterval: 2000`, `staleTime: 1000`. The quote is
display-only; `POST /binary/trades/:id/close` re-prices server-side, so a stale on-screen quote
cannot be traded against. Button text `Sell now <quote.value>` when `quote.available`, otherwise
`quote.unavailableReason ?? 'Pricing...'`, or `'Settling…'` when the countdown is over. On success:
`settleTrade`, `announceResult`, invalidate wallet and trades, toast
`"<value> returned (<signed customerResult>)"`. A row whose countdown hit zero while still `OPEN`
is displayed as `SETTLING`.

---

## 5. Results: projection, settlement, draw rule, status words

### 5.1 Live projection - `features/trade/liveResult.ts`

What a running trade **would** pay if it ended at the price on screen. Worded as a projection ("If
it ends now"); never a result.

```
liveResult(trade, price, drawPolicy = 'REFUND'):
  price not finite             -> null
  price == entryPrice          -> REFUND: {draw, 0} | LOSS: {losing, -stake} | WIN: {winning, profit}
  winning = UP ? price > entry : price < entry
  winning ? {winning, +trade.profit} : {losing, -trade.stake}
```

The amounts are the ones the server wrote on the trade (`profit`, `stake`). `liveTotal(trades,
priceOf, now, drawPolicy)` sums over trades with `expiryTimestamp > now` and reports `running` and
`priced` counts separately, so a total never silently covers fewer trades than the line claims.
Expired-but-unsettled trades are excluded (their outcome depends on a price the screen may not have
seen). `amountTone(a)` -> `up | down | neutral`. `STANDING_WORDS`: `winning`, `losing`,
`draw, refunded`.

### 5.2 Settlement display - `features/trade/settlement.ts`

- `settlementOf(trade)`: `trade.settlementAmount` when present - already **net and signed**
  (`payout - stake` on a win, `-stake` on a loss, `0` on a draw, the customer's result on an early
  close). Fallbacks only for a terminal row missing the field: WON -> `profit`, LOST -> `-stake`,
  DRAW/CANCELLED -> `0`, else `null`.
- `payoutOf(trade) = stake + (settlementOf(trade) ?? -stake)`: what came back to the wallet (full
  payout on a win, `0` on a loss, the stake on a draw). The result popup shows this, so a loss reads
  as zero, not negative.

Both the chart overlay and the result popup use these, so they cannot disagree.

### 5.3 Draw rule wording - `features/trade/drawRule.ts`

`drawPolicyOf(asset) = asset.drawPolicy ?? 'REFUND'` (sent by the server with each asset).
`TIE_SENTENCE`, `TIE_CLAUSE`, `TIE_LABEL` give the copy per policy (`REFUND/LOSS/WIN`).
`useDrawPolicy(assetId?)` reads the shared `['assets']` query (no extra request) and falls back to
the first asset's policy. The `DRAW` status only exists under REFUND; under LOSS/WIN a tie settles
as LOST/WON.

### 5.4 Status words and tones - `utils/tradeStatus.ts`

One terminology everywhere: `OPEN 'Active'`, `SETTLING 'Settling'`, `WON 'Won'`, `LOST 'Lost'`,
`DRAW 'Draw · refunded'`, `CANCELLED 'Cancelled'`, `CLOSED_EARLY 'Sold early'`. `statusTone`:
OPEN/SETTLING `live`, WON `up`, LOST `down`, CLOSED_EARLY by the sign of `settlementAmount`, else
`neutral`. `isActive(status)` = OPEN or SETTLING. `durationOf(trade) = round((expiry - entry) /
1000)`.

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

Other display rules: a DEMO watermark under the series when the active account is DEMO (font size
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
Skia picture driven by `useFrameClock`.

### 6.6 Order layer - `features/trade/orders/orderModel.ts`, `ChartOrderLayer.tsx`, `useOrderProjection.ts`

`buildOverlayOrders({assetId, preview, openTrades, settled, seenAt, now})` merges, in paint order:
recently settled trades (each lingers `SETTLED_LINGER_MS = 6000` from when **this client first saw**
it settled - not from `settledAt`, which a lagging clock could make already expired), open trades,
then the preview last (on top).

Visual states: `PREVIEW | ACTIVE | EXPIRED | WON | LOST | CANCELLED`. Mapping: DRAW and SETTLING ->
EXPIRED; OPEN past expiry -> EXPIRED with `awaitingSettlement` ("Settling"); CLOSED_EARLY -> WON if
`settlementAmount >= 0` else LOST; REJECTED preview -> CANCELLED (lingers `CANCELLED_LINGER_MS =
1800`). An INTENT preview `tracksClock` (entry = now, expiry = now + duration, price = live, all
recomputed per frame so it glides); a PLACING preview is pinned. `entryPrice`, once non-null, is
never overwritten. Colours (`orderTone`): preview green/red dashed; placed orders **brand yellow**
regardless of direction; outcome colour once settled; expired/cancelled grey and faded.
`needsHeartbeat` keeps the 1 s heartbeat only while something is ACTIVE, PREVIEW or fading.
`isInTheMoney` is indicative only.

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
2. If `ticksRetained(from, now)` (`now - from < 15 min - 45 s`), fetch ticks
   (`windowMs = clamp(now - from + 5 s, 1 s, 15 min)` for older servers); use them only if
   `ticksReachEntry`.
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
- **Viewport** (`useChartEngine.ts`): shared values `size`, `axisW` (56 initially), `spacing`
  (initial 10, min 0.5, max 50), `rightOffset`, `restingOffset` (6), `series`, `extents` (overlay
  indicator values the price scale fits to), `closeOnly` (line/area), `minMove`, `crosshair`. The
  derived `view` = `fitPrices(horizontal(...))`.
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
- **Tick marks**: `priceMarks` picks a nice step from `[1, 2, 2.5, 5, 10] * 10^k` about 34 px apart,
  never finer than the instrument precision; `timeMarks` picks from a step table (1 s ... 30 d) so
  labels are about 64 px apart, labels a day change with the date, seconds below a minute.
- **Time zones on the UI thread**: `Intl` is too slow per label per frame, so `buildZoneInfo`
  computes piecewise UTC offsets on the JS thread (sampled every 3 days, each switch narrowed by
  bisection to 1 s) and the painter formats from that table.
- **Painting**: `mainPainter.ts#paintMain` records grid, series (`paintCandles` draws wick-only below
  3 px spacing, body `floor(0.8 * s)` forced odd), overlays, last-price line/tag, axes, crosshair
  (magnet to close on the price chart) and the DEMO watermark into one `SkPicture` via
  `useDerivedValue(createPicture(...))`. Layers (`host.ts`: `drawings`, `guide`, `orders`) record
  their own pictures and are drawn into the same `Canvas` in that order. Taps are offered to layers
  by priority (`orders 30`, `drawings 10`) before falling through to the crosshair.
- **Clock** (`useFrameLoop.ts#useFrameClock`): a `useFrameCallback` clock that writes `now` only when
  it would move something by half a pixel or change a second label (half rate in performance mode).

---

## 7. Formatting, time, export, search, asset list

### 7.1 Money - `utils/format.ts`

`MINOR = 100`; `toMinor(major) = round(major * 100)`, `fromMinor(minor) = minor / 100` - the only
conversion points, so a display value cannot leak into a request. `formatMoney` uses `en-IN`
currency formatting with exactly 2 decimals (default INR); `formatSignedMoney` prefixes `+` for
positive; `formatMoneyCompact` drops `.00` only for whole amounts (chart tags); `formatPrice(p,
precision)` uses `en-US` with fixed precision; `formatExpiry` (`30s`, `1m`, `1.5m`, `1.0h`);
`formatCountdown` `mm:ss` with `ceil` and clamped at `00:00`. `currencySymbolFor` asks
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
turns over together.

### 7.3 USDT - `utils/usdt.ts`

`MICRO_PER_USDT = 1_000_000`, `USDT_DECIMALS = 6`. No float arithmetic: `parseUsdt` matches
`^(?:(\d+)(?:\.(\d{0,6}))?|\.(\d{1,6}))$`, pads the fraction to 6 digits and converts the digit
string (max 16 digits, must be a safe integer); commas, signs, exponents are refused.
`formatUsdt(micro, {trim, unit, signed})` prints 6 decimals by default (`trim` keeps at least 2),
groups thousands, `--` for non-finite. `toUsdtInput` writes an amount back into a field so it parses
to the same value.

### 7.4 CSV - `utils/csv.ts`

`escapeCsvValue`: values starting with `= + - @ \t \r` get a leading `'` (formula injection);
fields with `" , \n \r` are quoted with doubled quotes. `toCsv(rows, columns)` uses CRLF. Web
`downloadCsv` prepends a UTF-8 BOM and clicks a Blob link (revoked after 1 s). Mobile
`shareCsv(filename, csv)` writes the BOM-prefixed file to `Paths.cache` with `expo-file-system` and
opens the share sheet (`expo-sharing`, `text/csv`).

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

- `assetAvailability`: `!marketOpen -> closed`, feed STALE/OFFLINE -> `paused`, else `open`.
  `openCount` counts only `open`.
- `bestPayout` = max `payoutPercent`. `categoriesOf` in first-seen order. `catalogueOrder` by
  `displayOrder`.
- `assetsInFilter`: `all | favourites | recent | category:<name>`; Recent keeps recency order.
- `matchesSearch`: case-insensitive substring of symbol or name.
- `sortAssets`: `default` (chip order), `payout` (desc, tie -> `displayOrder`), `name` (numeric,
  base-sensitivity compare, tie -> `displayOrder`), so rows do not reshuffle between refreshes.
- `formatNextOpen(nextOpenAt, now, zone)`: `Opens 14:30` (today), `Opens Mon 09:00` (< 7 days),
  `Opens 12 Oct 09:00`; `null` when unknown or already past.

---

## 8. Theme system

**Tokens.** The palette is a set of named tokens stored as space-separated RGB channels (`"246 216
74"`) so Tailwind/NativeWind can apply alpha (`bg-accent/60`). Groups: `surface-950..500`,
`content-100..400`, `accent(-soft|-solid)`, `up/down(-soft|-solid|-bright)`, `warn`, `critical`,
`brand`, `scrim`, `chart-grid|axis|crosshair|bg|bull|bear|price-line`.

**Three themes + system.** `ThemePreference = 'light' | 'twilight' | 'dark' | 'system'`; `THEMES`
is that list, and `cycle()` steps through it in that order (`light -> twilight -> dark -> system`;
the web doc comment still says "light -> dark -> system"). "System" follows the OS live
(`watchSystemTheme`) and resolves only to `light` or `dark`.

- **Web** (`client/src/stores/themeStore.ts`): tokens are CSS custom properties in
  `client/src/index.css` (`:root` = dark, `:root[data-theme='twilight']`, `:root[data-theme='light']`).
  The resolved theme is written to `<html data-theme>` and `color-scheme`; the preference to
  `localStorage['bt.theme']`. An inline script in `client/index.html` applies the same key before
  first paint (change both together). `utils/theme-colors.ts` reads tokens with
  `getComputedStyle` and converts to hex/`rgba(a, b, c, x)` because chart libraries cannot parse
  `rgb(1 2 3)` (lightweight-charts throws "Cannot parse color").
- **Mobile** (`mobile/src/theme/tokens.ts`): `THEME_TOKENS: Record<'dark'|'twilight'|'light',
  TokenTable>` with the same values; `ThemeRoot.tsx` sets them as NativeWind `vars()` on the root
  view (they cascade through context, so sheets/modals get them), sets the status-bar style and the
  system window background. `tokenColor(theme, name, alpha)` serves Skia and other colour props;
  `readChartPalette(theme)` builds the chart palette (e.g. grid = `chart-grid` at 0.55 alpha).
  `SCRIM_ALPHA = { dark 0.72, twilight 0.7, light 0.45 }`. The store persists only `preference`
  and recomputes `resolved` on rehydrate; system theme comes from `Appearance`.

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

## 9. Mobile-specific differences and test setup

**Differences from web** (README "Differences from the web client", `PORTING.md`):

- **Phone layout only**: the < 640 px variant of every responsive web layout; no split two-chart
  view, no icon rail, no wide tables. Tabs (`app/(app)/(tabs)`): Home, Markets, Trade, Trades,
  Account; other screens are pushed. Overlays mounted once in `app/(app)/_layout.tsx`:
  `SettingsDrawer`, `TransferModal`, `TradeResultToasts`, `Toaster`.
- **Sheets** (`components/ui/Sheet.tsx`) replace web popovers, dropdowns and modals.
- **Deposit proof is image-only**: `expo-image-picker` from the photo library, single image,
  `Compatible` representation (HEIC -> JPEG); types PNG/JPEG/WebP, max 5 MB
  (`features/transfer/Transfer.tsx`). Web also accepts PDF.
- **In-app date pickers**: `features/trades/DateRangeField.tsx` opens a month calendar in a sheet,
  values are calendar keys (`YYYY-MM-DD`), start <= end enforced.
- **CSV** opens the share sheet (`shareCsv`) instead of downloading.
- **Haptics** (`expo-haptics`, always `.catch`-guarded): light impact on trade placement, a
  success/error/warning notification on a result (`TradeResultToasts.tsx`).
- Tab screens stay mounted, so polling screens use `hooks/useScreenFocused.ts` to pause when another
  tab is in front; `hooks/usePullRefresh.ts` shows a spinner only for a pull, not for polls.
- Chart preview on press-in instead of hover; drawings and order markers are hit by touch only.
- Platform substitutions: `expo-crypto` `randomUUID`, `setTimeout` without `window.`, SecureStore
  for tokens, AsyncStorage for persisted stores.

**Test setup.** Both apps run vitest over pure-logic suites (`client/tests/*.test.ts`,
`mobile/tests/*.test.ts`, same suite names: active-trades, asset-list, auth-schemas, chart-colors,
chart-geometry, csv, day-groups, duration-row, indicators, live-result, redraw, store-selectors,
text-search, theme-contrast, time, trade-ticket, trade-window, usdt-model, usdt-money).
`mobile/vitest.config.ts` runs in `node` and aliases native packages to in-memory stand-ins under
`mobile/tests/mocks/`: `@react-native-async-storage/async-storage`, `expo-secure-store`,
`expo-crypto` (Node `randomUUID`), `expo-constants`, `expo-file-system`, `expo-sharing`
(`isAvailableAsync -> false`) and `react-native` (`Appearance` fixed to dark, `Platform`). Commands:
`npm test` and `npm run typecheck` in `mobile/` (which is not part of the root npm workspace),
`npm test` in `client/`.

---

## 10. Where to look

| Topic | Web | Mobile | Identical? |
|---|---|---|---|
| HTTP client, tokens, refresh | `client/src/services/http.ts` | `mobile/src/services/http.ts` | no (storage, multipart) |
| Endpoints | `client/src/services/api.ts` | `mobile/src/services/api.ts` | no (`UploadFile`) |
| API URL / WS URL | `VITE_API_URL`, `VITE_WS_URL` | `mobile/src/config.ts` | - |
| App boot, QueryClient | `client/src/main.tsx`, `client/src/App.tsx` | `mobile/src/app/_layout.tsx` | - |
| Auth store | `client/src/stores/authStore.ts` | `mobile/src/stores/authStore.ts` | no (hydrate retry) |
| Websocket | `client/src/websocket/client.ts` | `mobile/src/websocket/client.ts` | no (URL, timers) |
| Realtime bridge | `client/src/hooks/useRealtime.ts` | `mobile/src/hooks/useRealtime.ts` | yes |
| Live feeds, connection, open trades, trading wallet | `client/src/features/trade/use{LiveFeeds,ConnectionState,ActiveTrades,TradingWallet}.ts` | same paths | yes |
| Stores | `client/src/stores/*.ts` | `mobile/src/stores/*.ts` (+ `storage.ts`) | logic yes, storage/theme no |
| Ticket rules | `client/src/features/trade/ticketModel.ts`, `ticketStore.ts` | same | yes |
| Ticket component | `client/src/features/trade/TradeTicket.tsx` | `mobile/src/features/trade/TradeTicket.tsx` | no |
| Early close | `client/src/features/trade/PositionsPanel.tsx` | same path | no (UI) |
| Projection / settlement / draw rule | `client/src/features/trade/{liveResult,settlement,drawRule,announceResult}.ts` | same | yes |
| Status words | `client/src/utils/tradeStatus.ts` | same | yes |
| Main chart | `client/src/features/trade/PriceChart.tsx` | `mobile/src/features/trade/PriceChart.tsx` | no |
| Indicators | `client/src/utils/indicators.ts`, `features/trade/chart/indicatorCatalog.ts` | same | yes |
| Time projection | `client/src/features/trade/chart/timeProjection.ts` | same path (worklets + `*Times`) | no |
| Drawings | `chart/drawingGeometry.ts`, `chart/DrawingLayer.tsx`, `stores/drawingsStore.ts` | same | geometry yes |
| Expiry guide | `chart/ExpiryGuide.tsx`, `chart/useFrameLoop.ts` | same paths (`useFrameClock`) | no |
| Orders overlay | `orders/orderModel.ts`, `orders/ChartOrderLayer.tsx`, `orders/useOrderProjection.ts` | same | model yes |
| Trade detail chart | `chart/tradeWindow.ts`, `chart/TradeWindowChart.tsx` | same | model yes |
| Redraw throttle | `client/src/utils/redraw.ts` | same | yes |
| Skia engine | - | `mobile/src/features/trade/chart/engine/*` | mobile only |
| Formatting | `client/src/utils/{format,time,usdt}.ts` | same | format differs (symbol fallback) |
| CSV, days, search | `client/src/utils/{csv,dayGroups,textSearch}.ts` | same | csv differs (share sheet) |
| Asset list rules | `client/src/components/assetList.ts` | same | yes |
| USDT queries/keys | `client/src/features/usdt/queries.ts` | same | yes |
| Theme | `client/src/stores/themeStore.ts`, `client/src/index.css`, `client/index.html`, `client/src/utils/theme-colors.ts` | `mobile/src/stores/themeStore.ts`, `mobile/src/theme/{tokens.ts,ThemeRoot.tsx}`, `mobile/src/utils/theme-colors.ts` | no |
| Tests | `client/tests/*.test.ts` | `mobile/tests/*.test.ts`, `mobile/tests/mocks/*`, `mobile/vitest.config.ts` | suites mirrored |
| Port contract | - | `mobile/PORTING.md`, `mobile/README.md` | - |
