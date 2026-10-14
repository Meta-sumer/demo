# Trading core: business logic

This document covers how the trading core decides things: how a binary trade
is accepted, priced, timed, expired, settled and closed early, and how assets,
payouts and the DEMO account fit in. Everything here comes from the Go backend
in `server-go/`. Where a statement depends on a specific function, the file is
cited. Where the code and the root `README.md` disagree, the code is treated as
the reference and the disagreement is listed in section 11.

Conventions used throughout:

- **Money is always integer minor units** (paise). `10000` = ₹100.00. Prices
  are `float64`. Timestamps called `*Timestamp` are epoch milliseconds.
- `RoundHalfUp(x)` = `floor(x + 0.5)` (JS `Math.round`), from
  `server-go/internal/platform/util/util.go`.
- Error responses carry an error `code` and an HTTP status. Codes are defined in
  `server-go/internal/platform/apperr/errors.go`. `apperr.Validation` is **422**,
  `Unavailable` is **503**, `ConflictErr` is **409**, `NotFoundErr` is **404**,
  `Unauth` is **401** (`UNAUTHENTICATED`).
- Runtime-tunable rules live in the versioned **risk config**
  (`server-go/internal/models/risk_config.go`, defaults in `DefaultRiskConfig()`).
  It is cached for 2 s per process (`server-go/internal/service/riskconfig/riskconfig.go`),
  so a change can take up to about 2 s to reach every instance.

---

## 1. Trade lifecycle and status state machine

Statuses (`server-go/internal/models/domain.go`): `OPEN`, `SETTLING`, `WON`,
`LOST`, `DRAW`, `CANCELLED`, `CLOSED_EARLY`. Results: `WIN`, `LOSS`, `DRAW`,
`EARLY_CLOSE`.

```
                    POST /binary/trades (acceptTrade)
                                 │
                                 ▼
   admin cancel  ┌──────────────── OPEN ───────────────┐  settlement worker claim
   (CancelTrade) │                 │                    │  (claimTrade)
                 ▼                 │ customer "Sell now"▼
            CANCELLED              │ (CloseTradeEarly)  SETTLING ──(stale >30 s)──► re-claimed
                                   ▼                    │   │                       by claimTrade
                              SETTLING ──► CLOSED_EARLY │   └─ no price: stays SETTLING (deferred)
                                                        ▼
                                              WON / LOST / DRAW
```

| Transition | Who | Guard | Code |
|---|---|---|---|
| (none) → `OPEN` | `binary.acceptTrade` from `POST /binary/trades` | full validation chain (section 2) | `server-go/internal/service/binary/binary.go` |
| `OPEN` → `SETTLING` (expiry) | settlement worker, `settlement.claimTrade` | conditional `FindOneAndUpdate` on `status: OPEN` | `server-go/internal/service/settlement/settlement.go` |
| `SETTLING` (stale) → `SETTLING` | settlement worker, `claimTrade` | `status: SETTLING` and `updatedAt < now − 30 000 ms` (`StaleSettlingMs`) | same |
| `SETTLING` → `WON`/`LOST`/`DRAW` | `settlement.applySettlement` | runs only after the claim, the wallet mutation and the settlement row insert | same |
| `OPEN` → `SETTLING` (early close) | `binary.CloseTradeEarly` | conditional update on `status: OPEN` | `server-go/internal/service/binary/earlyclose_service.go` |
| `SETTLING` → `CLOSED_EARLY` | `CloseTradeEarly` | follows its own claim | same |
| `OPEN` → `CANCELLED` | admin `POST /admin/binary/trades/{id}/cancel` (permission `PermTradesCancel`), `binary.CancelTrade` | conditional update on `status: OPEN`; a `SETTLING` trade cannot be cancelled | `server-go/internal/service/binary/binary.go`, `server-go/internal/api/admin/trading.go` |
| repair: `OPEN`/`SETTLING` → final | `settlement.ensureTradeFinalised` | a settlement row already exists but the trade status was never updated | `server-go/internal/service/settlement/settlement.go` |

"Live" statuses are `OPEN` and `SETTLING` (`binary.LiveStatuses`). Finished ones
are `WON`, `LOST`, `DRAW`, `CANCELLED`, `CLOSED_EARLY` (`binary.FinishedStatuses`).
List filters use `phase=OPEN|SETTLED` on top of these.

---

## 2. Trade acceptance

### 2.1 Route and request

`POST /binary/trades` (`server-go/internal/api/binary/routes.go`). Middleware runs
in this order:

1. `httpx.Authenticate`.
2. `httpx.RequireActiveUser`: user missing → 401 `UNAUTHENTICATED` "Account no
   longer exists". Status not `ACTIVE` → 403 `ACCOUNT_DISABLED`. Role changed
   since the token was issued → 401 `TOKEN_INVALID`.
3. `RateLimit(RateLimits.Trade)`: 60 requests per minute per user, shared with
   `POST /trades/{id}/close`. Over the limit → 429 `RATE_LIMITED`.

Body (`parseCreateTrade`). A malformed JSON body → 400 `VALIDATION_ERROR`. A
field failure → **422** `VALIDATION_ERROR` "Invalid request body" with
per-field issues.

| Field | Rule |
|---|---|
| `assetId` | required, 24-hex string |
| `direction` | required, `UP` \| `DOWN` |
| `amount` | required, integer, > 0, ≤ 1 000 000 000 (minor units) |
| `expirySeconds` | required, integer, > 0, ≤ 86 400 |
| `quotedPrice` | optional, positive, finite. Used only by the slippage guard |
| `idempotencyKey` | optional, string of 8–128 chars |
| `accountType` | optional, `LIVE` \| `DEMO`, default `LIVE` |

The `Idempotency-Key` header is also accepted (first value, no length check).
**The body key wins over the header** (`CreateTrade`). An empty key means no
idempotency.

A request carries no entry price, no timestamps and no payout. The server
decides all of them.

### 2.2 Order of operations

`binary.CreateTrade` → `acceptTrade` (`server-go/internal/service/binary/binary.go`).
The whole flow runs on `context.WithoutCancel`, so it is not aborted if the
client disconnects. Steps in order. The first failure stops the flow.

| # | Step | Refusal | Status |
|---|---|---|---|
| 0a | `accountType == FUNDING` (USDT wallet) is refused. The API enum already excludes it, so this is a defensive check | `VALIDATION_ERROR` | 422 |
| 0b | **Idempotency resolution**, if a key is present (2.3). Runs **before** any business validation | `IDEMPOTENCY_PAYLOAD_MISMATCH` / `IDEMPOTENCY_IN_PROGRESS` | 422 / 409 |
| 1 | Global kill switch `riskConfig.tradingHalted` (`riskconfig.AssertTradingNotHalted`) | `TRADING_HALTED` | 503 |
| 2 | User still exists and is `ACTIVE`. This re-check reads `maxTradeAmount` and `maxUserExposure` | `UNAUTHENTICATED` / `ACCOUNT_DISABLED` | 401 / 403 |
| 3a | Asset exists (`assets.RequireAssetDoc`, from the 2 s in-process cache) | `ASSET_NOT_FOUND` | 404 |
| 3b | Asset `active` (`assets.AssertAssetTradable`) | `ASSET_INACTIVE` | 409 |
| 3c | Inside trading hours (`util.IsWithinTradingHours`, section 6.2) | `MARKET_CLOSED` | 409 |
| 4 | `expirySeconds` ∈ `asset.supportedExpiries` | `EXPIRY_NOT_SUPPORTED` | 400 |
| 5 | **Feed gate** `marketdata.AssertFeedTradable` (2.4). Returns the tick that will price the trade | `FEED_UNHEALTHY` / `NO_PRICE_AVAILABLE` | 503 |
| 5b | **Slippage guard**, only when `quotedPrice` was sent (2.5) | `PRICE_MOVED` (details: `serverPrice`, `quotedPrice`, `movePercent`, `tolerancePercent`) | 409 |
| 6 | Active payout row for (asset, expiry) (`payouts.RequireActivePayout`) | `PAYOUT_NOT_CONFIGURED` | 409 |
| 7a | Per-expiry stake bounds from the payout row, when set: `stake < payout.minStake` / `stake > payout.maxStake` | `STAKE_BELOW_MIN` / `STAKE_ABOVE_MAX` ("…for this expiry is N") | 400 |
| 7b | **Risk limits** `risk.AssertTradeWithinRiskLimits` (2.6) | first violation's code (`STAKE_BELOW_MIN`, `STAKE_ABOVE_MAX`, `USER_LIMIT_EXCEEDED`, `EXPOSURE_LIMIT_EXCEEDED`, `RISK_LIMIT_EXCEEDED`); `details.violations` lists all of them | 400 |
| 8 | Server fixes `tradeId`, `entryTimestamp`, `expiryTimestamp` (section 3) | — | — |
| 9a | DEMO only: `wallets.EnsureDemoWallet` creates and funds the DEMO wallet on first use | — | — |
| 9b | **Reserve the stake**: `wallets.LockTradeFunds` (ledger key `trade:lock:<tradeId>`; guard `balance − lockedBalance ≥ stake`) | `INSUFFICIENT_BALANCE` (400); `WALLET_NOT_FOUND` (404) if the wallet row is missing; `SERVICE_UNAVAILABLE` (503) if the per-wallet Redis lock is not obtained within 15 s | |
| 10 | Insert the `binary_trades` document. On a duplicate key, release the stake (`ReleaseCancelled`) and, when the clash was on the idempotency key, return the winning trade as a replay | — | — |
| 11 | `ZADD expiry:queue <expiryTimestamp> <tradeId>`. For LIVE only, `exposure.ApplyExposureDelta(sign +1)`. Mark idempotency committed. Emit `trade.created` | — | — |

Any refusal increments the `trades.rejected` metric and **deletes the Redis
idempotency reservation**, so a retry with the same key is evaluated again
from scratch. The handler writes an audit row: `TradeRejected` (failed, with
the error message) or `TradeCreated` (with `replayed`). The response is
**201** for a new trade and **200** with `meta.replayed = true` for a replay.

Steps 1–7 read caches: the risk config, the asset table and the payout table
each have a 2 s TTL per process. Note that the stake is reserved (9b) *before*
the trade is written (10). If the process crashes between the two, the
customer's funds stay locked against a trade that does not exist. At worker
boot, `wallets.ReleaseOrphanedTradeLocks` releases any `TRADE_LOCK` older than
60 s whose trade document does not exist, up to 500 per run
(`server-go/internal/service/wallets/orphan_locks.go`, called from
`server-go/internal/workers/workers.go`).

### 2.3 Idempotency

Two layers:

- **MongoDB**: a partial unique index on `{userId, idempotencyKey}` for string
  keys (`server-go/internal/models/binary_trade.go`). The trade stores
  `idempotencyFingerprint`.
- **Redis**: the key `idem:<userId>:<key>`, TTL 24 h. Its value is
  `in-progress:<fp>` while the request runs and `committed:<fp>:<tradeId>`
  after it succeeds.

The fingerprint (`TradeFingerprint`) is the SHA-256 of a canonical JSON of
`{assetId, direction, amount, expirySeconds}`, plus `accountType` only when it
is not LIVE (so fingerprints written before accounts existed still match).
`quotedPrice` is **not** part of the fingerprint, so a retry with a fresh quote
counts as the same request.

`resolveIdempotency`:

1. If a trade with (user, key) exists in Mongo: when its stored fingerprint is
   non-empty and differs → 422 `IDEMPOTENCY_PAYLOAD_MISMATCH`. Otherwise the
   stored trade is returned (`replayed: true`, no side effects).
2. Otherwise `SETNX` the Redis key with `in-progress:<fp>`. If the set
   succeeds, the request proceeds.
3. If the Redis key already holds `in-progress:` with another fingerprint →
   422 `IDEMPOTENCY_PAYLOAD_MISMATCH`. With the same fingerprint → 409
   `IDEMPOTENCY_IN_PROGRESS`. Any other value lets the request proceed, and
   the unique index catches a true duplicate at insert (step 10).

### 2.4 Feed gate (`assertFeedTradable`)

`server-go/internal/service/marketdata/marketdata.go`. Feed health comes from
the last tick in `md:tick:<assetId>`:

```
no tick                      → OFFLINE
ageMs = now − tick.receivedAt
ageMs > feedTickMaxAgeMs     → OFFLINE                       (default 5000)
effective = max(tick.totalLatencyMs, ageMs)
effective ≤ feedHealthyMaxMs → HEALTHY                       (default 100)
effective ≤ feedDegradedMaxMs→ DEGRADED                      (default 300)
otherwise                    → STALE                         (feedStaleMaxMs default 1000; only used for config validation)
```

A trade is refused with 503 `FEED_UNHEALTHY` when the status is `OFFLINE`, or
`STALE` with `haltTradingOnStaleFeed` (default `true`), or `DEGRADED` with
`haltTradingOnDegradedFeed` (default `false`). With the defaults, `HEALTHY` and
`DEGRADED` feeds can trade. If the last tick disappears between the health
check and the re-read → 503 `NO_PRICE_AVAILABLE`. On first boot the feed
thresholds are copied from the env (`FEED_*_MS`) into risk config version 1.

### 2.5 Slippage guard (`quotedPrice`)

`server-go/internal/service/binary/slippage.go`. The quote is only ever used to
decide whether to accept. It is never used as the entry price.

```
if quotedPrice is not finite or ≤ 0     → accept (no opinion)
movePercent = (serverPrice − quotedPrice) / quotedPrice × 100
adverse     = movePercent > 0 for UP,  movePercent < 0 for DOWN
if slippageTolerancePercent ≤ 0         → accept (guard disabled)
if !adverse                             → accept (a favourable move is never refused)
accept iff |movePercent| ≤ slippageTolerancePercent   (default 0.1; boundary accepted)
```

`serverPrice` is the tick returned by the feed gate. On refusal, `movePercent`
is rounded to 4 decimals (JS `toFixed` semantics) in the error details.

### 2.6 Risk limits

`server-go/internal/service/risk/risk.go`, `CheckTradeLimits` (pure) fed by
`AssertTradeWithinRiskLimits`. The function collects **every** violation, in
this order:

| Check | Formula | Code |
|---|---|---|
| min stake | `stake < max(asset.minStake, cfg.minTradeAmount)` | `STAKE_BELOW_MIN` |
| max stake | `stake > min(asset.maxStake, cfg.maxTradeAmount, user.maxTradeAmount ?? ∞)` | `STAKE_ABOVE_MAX` |
| open trades | count of the user's trades with `status: OPEN` **on the same account** `≥ cfg.maxOpenTradesPerUser` (SETTLING not counted) | `USER_LIMIT_EXCEEDED` |
| *DEMO stops here* | | |
| user exposure | `userTotalExposure + stake > (user.maxUserExposure ?? cfg.maxUserExposure)` | `USER_LIMIT_EXCEEDED` |
| asset gross | `asset.gross + stake > min(asset.exposureLimit, cfg.maxAssetExposure)` | `EXPOSURE_LIMIT_EXCEEDED` |
| asset net | `|asset.net ± stake| > asset.netExposureLimit` (+ for UP, − for DOWN) | `EXPOSURE_LIMIT_EXCEEDED` |
| platform gross | `portfolio.totalGross + stake > cfg.maxGrossExposure` | `RISK_LIMIT_EXCEEDED` |
| concentration | only if `projectedGross > userExposureLimit`: `(userAssetExposure + stake) / projectedGross × 100 > cfg.maxUserAssetConcentrationPercent` | `RISK_LIMIT_EXCEEDED` |

Exposure figures come from the Redis counters (section 7) and are
stake-based: `net = upStake − downStake`, `gross = upStake + downStake`.

---

## 3. What the server fixes on the trade

All values are set in `acceptTrade` and frozen on the document. Later changes
to the payout table or the risk config never touch an existing trade.

| Field | Value |
|---|---|
| `_id` | new ObjectID, created before the wallet lock (the lock's ledger key uses it) |
| `reference` | `"BT_" + base64url(9 random bytes)` (`util.Reference`), unique index |
| `entryPrice` | `tick.price` of the last stored tick returned by `AssertFeedTradable` (step 5) |
| `entryTickSequence`, `entryProviderTimestamp`, `entryFeedLatencyMs` | provenance of that tick (`entryFeedLatencyMs = tick.totalLatencyMs`) |
| `entryTimestamp` | `util.NowMs()` at step 8, i.e. server time *after* validation, not the tick's time |
| `expiryTimestamp` | `entryTimestamp + expirySeconds × 1000` |
| `payoutPercent`, `payoutConfigId`, `payoutConfigVersion` | from the active payout row at step 6 |
| `profit` | `RoundHalfUp(stake × payoutPercent / 100)` (`util.CalculateProfit`) |
| `currency` | always `"INR"` (`BinaryTradeDefaultCurrency`) |
| `accountType` | `LIVE` or `DEMO` (missing on old documents = LIVE) |
| `status` | `OPEN` |
| `ip` | client IP from the request |

`potentialReturn` is not stored. The view computes it as `stake + profit`
(`models.ToTradeView`). Example: stake 10 000 at 82 % → profit 8 200, potential
return 18 200.

---

## 4. Expiry detection and settlement

### 4.1 Queues

| Redis key | Type | Producer | Consumer |
|---|---|---|---|
| `expiry:queue` | ZSET, score = `expiryTimestamp`, member = tradeId | `acceptTrade`; `RebuildExpiryQueue` at boot | expiry worker |
| `settlement:queue` | LIST (`LPUSH` / `BRPOP`+`RPOP` = FIFO) | expiry worker, sweeper, settlement worker (re-queue) | settlement worker |

Redis is a cache here. At worker start (`server-go/internal/workers/workers.go`)
the exposure counters (`RebuildExposureFromDatabase`) and the expiry queue
(`binary.RebuildExpiryQueue`, all `OPEN`+`SETTLING` trades) are rebuilt from
MongoDB before any loop starts.

### 4.2 Expiry worker (leader-elected)

`server-go/internal/service/expiry/worker.go`, `expiry.go`.

- Runs through `leader.New{Name: "expiry", Interval: EXPIRY_TICK_MS (default 100, min 10), LockTTL: 15 s}`.
  Leadership is a Redis `SETNX` lock `lock:worker:expiry` that is renewed on
  every tick (`server-go/internal/platform/leader/leader.go`). If the leader
  dies, another instance takes over within one TTL. Ticks never overlap.
  `WORKERS_ENABLED=false` disables the loop.
- Each tick, `ProcessExpiries(now, 200)`:
  1. `FindDueTrades`: `ZRANGEBYSCORE expiry:queue -inf now LIMIT 0 200`, then
     loads those trades with status `OPEN`/`SETTLING`. Queue ids that don't
     match a live trade are `ZREM`ed as orphans.
  2. For each due trade, with `elapsed = now − expiryTimestamp`: if
     `elapsed < 2000 ms` (`ExpiryTickGraceMs`) **and** no tick with provider
     timestamp ≥ `expiryTimestamp` exists (`HasTickAtOrAfter`), the trade
     waits for the next tick. This makes sure a tick still in flight is not
     missed.
  3. Otherwise: emit `trade.expiring`, `LPUSH settlement:queue`,
     `ZREM expiry:queue`, record `expiry.detection` latency.
     **Nothing is settled here.**
- Every `round(30 000 / interval)` ticks (about 30 s), `SweepMissedExpiries(now, 500)`
  pushes back onto `settlement:queue` any trade that is `OPEN` with
  `expiryTimestamp ≤ now − 2000`, or `SETTLING` with `updatedAt < now − 30 s`.

### 4.3 Settlement worker (not leader-elected)

`server-go/internal/service/settlement/worker.go`. Every instance with workers
enabled consumes the queue. It does a `BRPOP settlement:queue` with a 1 s
timeout on a dedicated connection, then drains up to `SETTLEMENT_BATCH_SIZE`
(default 200) ids with `RPOP`. Each id runs `SettleTrade` on a fresh
background context with a 60 s timeout. Errors are logged and the trade is
left for the sweeper. A `SKIPPED_NO_PRICE` outcome is re-`LPUSH`ed. On a
consumer error the loop backs off 250 ms.

### 4.4 `SettleTrade` step by step

`server-go/internal/service/settlement/settlement.go`:

1. If a `settlements` row exists for the trade, run `ensureTradeFinalised`
   (repair the trade status if needed) and return `ALREADY_SETTLED`.
2. `claimTrade`: atomic `FindOneAndUpdate` with
   `{_id, $or: [{status: OPEN}, {status: SETTLING, updatedAt < now−30s}]}` →
   `{status: SETTLING, updatedAt: now}`. No match → `CLAIMED_ELSEWHERE`.
3. **Expiry price**, `marketdata.GetPriceAtOrBefore(assetId, expiryTimestamp)`:
   - the newest tick in `md:hist:<assetId>` (ZSET scored by **provider
     timestamp**, i.e. market time) with score ≤ `expiryTimestamp` → source
     `TICK_AT_OR_BEFORE_EXPIRY`;
   - otherwise the current last tick → source `LAST_KNOWN_TICK`;
   - otherwise `nil` → return `SKIPPED_NO_PRICE` and leave the trade in
     `SETTLING` (**deferred, never guessed**).
4. `DetermineResult(direction, entryPrice, expiryPrice)`: exact float equality
   → `DRAW`. Otherwise UP wins if `expiry > entry`, DOWN wins if
   `expiry < entry`. The smallest move decides.
5. `ApplyDrawPolicy(result, cfg.drawPolicy)`: `REFUND` (default) keeps `DRAW`,
   `LOSS` turns a draw into `LOSS`, `WIN` turns it into `WIN`. The policy never
   changes a decided result. The policy-adjusted result is what gets stored;
   the raw draw is not recorded. `GET /assets` discloses the policy as
   `drawPolicy` on every asset.
6. `applySettlement`:
   1. **Wallet first**, keyed `trade:settle:<tradeId>` (amounts below).
   2. Insert a `settlements` row (unique on `tradeId`). A duplicate key returns
      `ALREADY_SETTLED`.
   3. Update the trade: `status`, `result`, `expiryPrice`, `expiryPriceSource`,
      `settlementAmount`, `platformResult`, `settledAt`,
      `settlementLatencyMs = now − expiryTimestamp`.
   4. `ZREM expiry:queue`. For LIVE only, `ApplyExposureDelta(sign −1)`.
   5. Emit `trade.settled`. Create a `TRADE_SETTLED` notification (fire and
      forget, emits `notification.created`). Update metrics.

### 4.5 Settlement amounts

| Result | Final status | Wallet op (type / movement) | Balance Δ | Locked Δ | `payoutAmount` (row) | `platformResult` | `settlementAmount` (trade) |
|---|---|---|---|---|---|---|---|
| WIN | `WON` | `TRADE_WIN` / CREDIT, amount = profit | +profit | −stake | stake + profit | −profit | +profit |
| LOSS | `LOST` | `TRADE_LOSS` / DEBIT, amount = stake | −stake | −stake | 0 | +stake | −stake |
| DRAW (REFUND) | `DRAW` | `TRADE_REFUND` / UNLOCK | 0 | −stake | stake | 0 | 0 |
| EARLY_CLOSE | `CLOSED_EARLY` | `TRADE_EARLY_CLOSE` / CREDIT or DEBIT, amount = \|value − stake\| | value − stake | −stake | value | stake − value | value − stake |
| (cancel) | `CANCELLED` | `TRADE_CANCEL` / UNLOCK | 0 | −stake | *no row* | 0 | 0 |

At open, `TRADE_LOCK` / LOCK has balance Δ 0 and locked Δ +stake. Every wallet
mutation goes through `applyMutation` (`server-go/internal/service/wallets/wallet.go`):
a Redis lock per (user, account, currency) (TTL 10 s, wait 15 s), a replay check
on `idempotencyKey`, a guarded `$inc` with `$expr` pre-conditions (locked ≥ stake
for releases, otherwise `LOCKED_FUNDS_MISMATCH` 409) and the ledger row, all in
one Mongo transaction (when `ENABLE_MONGO_TRANSACTIONS=true`).

### 4.6 Single-settlement guarantee

Three independent guards. Each alone stops a double payout:

1. **Claim**: only one caller can move `OPEN → SETTLING`. Early close uses the
   same conditional update.
2. **Wallet key** `trade:settle:<id>`: partial unique index on
   `wallet_transactions.idempotencyKey`. Win, loss, refund, cancel *and* early
   close all use this key, so money moves at most once per trade by any path.
   A replay returns the existing transaction id without moving money.
3. **`settlements.tradeId` unique index**: at most one ledger row per trade.

### 4.7 Recovery paths and deferral

- **Orphaned SETTLING** (a worker crashed after the claim): `claimTrade` itself
  re-claims a `SETTLING` trade whose `updatedAt` is more than 30 s old, and the
  sweeper re-queues such trades about every 30 s.
- **No price**: the trade stays `SETTLING` with a fresh `updatedAt`. The
  worker re-queues it right away, but that retry hits `claimTrade` within
  30 s and returns `CLAIMED_ELSEWHERE`, which is not re-queued. **In practice
  the retry is the sweeper**, about 30–60 s later.
- **Row exists but trade not finalised**: `ensureTradeFinalised` sets the final
  status from the row (`settlementAmount = payoutAmount − stake` for WIN,
  `−stake` for LOSS, otherwise 0).
- **Queue lost**: `RebuildExpiryQueue` at boot, plus the DB sweep.

---

## 5. Early close ("Sell now")

### 5.1 Quote formula

`server-go/internal/service/binary/earlyclose.go`, `QuoteEarlyClose` (pure):

```
move        = currentPrice − entryPrice
signedMove  = move  (UP)   |  −move (DOWN)
inTheMoney  = signedMove > 0

P(win):
  secondsRemaining ≤ 0              → 1 if signedMove > 0 else 0
  expectedMove = currentPrice × σ_perSecond × √secondsRemaining
  expectedMove ≤ 0 / NaN / Inf      → 0.5 if signedMove == 0, 1 if > 0, 0 if < 0
  otherwise                         → Φ(signedMove / expectedMove)
P(win) is then clamped to [0.01, 0.99]   (always, including the cases above)

fullPayout = stake × (1 + payoutPercent/100)
fee        = clamp(earlyCloseFeePercent, 0, 100) / 100
value      = max(0, RoundHalfUp(fullPayout × P(win) × (1 − fee)))      minor units
customerResult = value − stake
winProbability = P(win) rounded to 4 dp
```

`Φ` is `0.5 × (1 + erf(z/√2))`, with erf from Abramowitz–Stegun 7.1.26.

**σ per second** (`volatilityPerSecond`, `earlyclose_service.go`):
`σ_tick = stddev of log returns` over the ticks in the last 60 s
(`GetRealisedVolatility`, sample stddev, needs ≥ 3 ticks, otherwise 0), then
`σ_perSecond = σ_tick × √max(0.1, ticksInWindow / 60)`. When σ is 0 the
"still market" branch applies.

Because P(win) ≤ 0.99 and fee ≥ 0, the quote is always below the full payout.
Selling a winning position early pays less than holding it to expiry.

### 5.2 Availability

Risk-config fields: `earlyCloseEnabled` (default `true`),
`earlyCloseFeePercent` (default 5, admin range 0–50),
`minSecondsRemainingForEarlyClose` (default 10, admin range 0–3600).

`GET /binary/trades/{id}/quote` (`QuoteTrade`) **never mutates anything** and
never fails for an un-closeable trade. It returns
`{value, customerResult, winProbability, inTheMoney, tradeId, currentPrice, secondsRemaining (rounded), available, unavailableReason}`.
`unavailableReason` is the first that applies:

1. `!earlyCloseEnabled` → "Closing early is currently disabled"
2. `status != OPEN` → "This trade is already <status lowercased, first `_` → space>"
3. no last tick → "No live price is available for this asset" (the quote then uses `entryPrice` as the current price)
4. `secondsRemaining < minSecondsRemainingForEarlyClose` → "Too close to expiry to close early"

Someone else's trade, or an unknown id, gives 404 `TRADE_NOT_FOUND`. It is
never 403, so the response does not reveal that the trade exists.

### 5.3 Close path

`POST /binary/trades/{id}/close` → `CloseTradeEarly` (requires an active user;
same 60/min bucket as opening). The checks run in this order:

| # | Check | Error |
|---|---|---|
| 1 | `earlyCloseEnabled` | 503 `SERVICE_UNAVAILABLE` |
| 2 | trade exists and is the caller's | 404 `TRADE_NOT_FOUND` |
| 3 | `status == OPEN` | 409 `ALREADY_SETTLED` "This trade is no longer open" |
| 4 | `secondsRemaining ≥ minSecondsRemainingForEarlyClose` | 409 `CONFLICT` "Too close to expiry…" |
| 5 | last tick exists | 503 `NO_PRICE_AVAILABLE` |
| 6 | compute the quote from that tick (**price first, then claim**) | — |
| 7 | claim `OPEN → SETTLING` | lost race → 409 `ALREADY_SETTLED` |
| 8 | `wallets.SettleTradeEarlyClose(value)`: one guarded op, key `trade:settle:<id>` | wallet errors |
| 9 | insert a `settlements` row with `result: EARLY_CLOSE`, `expiryPrice = tick.price`, `payoutAmount = value`, `platformResult = stake − value`, `settledBy: "customer.early_close"` (a duplicate key is only logged) | — |
| 10 | trade → `CLOSED_EARLY`, `result: EARLY_CLOSE`, `expiryPriceSource: "EARLY_CLOSE_AT_MARKET"`, `settlementAmount = customerResult`, `platformResult`, `settledAt` | — |
| 11 | `ZREM expiry:queue`; for LIVE, `ApplyExposureDelta(sign −1)`; emit `trade.settled`; audit `trade.closed_early` | — |

The close path does **not** check feed health, the global halt or trading
hours. It uses whatever last tick is in Redis (section 10). It creates no
settlement notification.

---

## 6. Assets and payouts

### 6.1 Asset fields and defaults

`server-go/internal/models/asset.go`. Defaults are applied by
`assets.CreateAsset` (`server-go/internal/service/assets/assets.go`):

| Field | Meaning | Default |
|---|---|---|
| `symbol` | upper-cased, trimmed, unique | — |
| `name`, `category` (`CRYPTO`/`FOREX`/`STOCK`/`COMMODITY`/`INDEX`) | | — |
| `active` | trading enabled | `true` |
| `providerSymbol`, `provider` | feed mapping (provider symbol matched case-insensitively) | provider `simulated` |
| `pricePrecision` | display decimals | 2 |
| `minStake` / `maxStake` | asset stake bounds | 1 000 / 10 000 000 |
| `supportedExpiries` | seconds | `[30, 60, 300]` |
| `exposureLimit` | max gross exposure | 500 000 000 |
| `netExposureLimit` | max one-sided exposure | 200 000 000 |
| `hedgeEnabled`, `targetHedgeRatio`, `hedgeThresholdPercent` (null = global), `minHedgeNotional`, `maxHedgeNotional` | hedging only | `false`, 0.5, null, 100 000, 100 000 000 |
| `tradingWindows` | `[{days: [ISO 1..7], startMinuteUtc, endMinuteUtc}]` | `[]` (always open) |
| `displayOrder` | | 0 |

The asset table is cached in-process for 2 s and invalidated on write by the
writing instance. Other instances converge within the TTL.

### 6.2 Trading hours, `marketOpen`, `nextOpenAt`

`util.IsWithinTradingHours` (`server-go/internal/platform/util/util.go`):

- No windows means always open.
- A window matches when today's ISO weekday (UTC) is in `days` and the UTC
  minute is in `[start, end)`.
- If `start > end`, the window wraps **within the listed day**: it is open
  `00:00–end` and `start–24:00` on that same day. It does not spill into the
  next day.

`AssetView` (`ToAssetView`) adds `marketOpen = active && withinHours` and
`nextOpenAt` (epoch ms). `nextOpenAt` is `null` when the market is open, the
asset is inactive, or there are no windows. Otherwise it is the start of the
next open span, searched over today plus 7 days (`util.NextTradingOpen`). It
can also be null if the windows never open.

### 6.3 Payout tables and versioning

`server-go/internal/service/payouts/payouts.go`, `server-go/internal/models/payout_config.go`.

- Row fields: `assetId`, `expirySeconds`, `payoutPercent` (82 ⇒ 82 % profit),
  optional `minStake`/`maxStake`, `version`, `active`, `effectiveFrom`,
  `effectiveTo`, `createdBy`, `note`.
- Indexes: unique `{assetId, expirySeconds, version}`, and a partial unique
  `{assetId, expirySeconds}` where `active: true`, so at most one active row
  per (asset, expiry).
- **Upsert** (admin `POST /admin/assets/{id}/payouts`, `PermPayoutsWrite`;
  synchronous audit `PayoutUpdated`): the expiry must be in
  `asset.supportedExpiries` and the percent in 0–500 (otherwise 422). The
  current active row is closed (`active=false, effectiveTo=now`) *before* the
  new row is inserted with `version = latest + 1`. Rows are never edited in
  place.
- **Deactivate** (`DELETE /admin/assets/payouts/{id}`): sets `active=false`,
  `effectiveTo=now`. New trades on that expiry then fail with
  `PAYOUT_NOT_CONFIGURED`.
- Active rows are cached for 2 s per process. Trades record
  `payoutConfigId`/`payoutConfigVersion`, so a trade can always be traced back
  to the exact terms it was accepted under.

### 6.4 Stake bounds, effective resolution

```
effectiveMin = max(payout.minStake (if set), asset.minStake, cfg.minTradeAmount)
effectiveMax = min(payout.maxStake (if set), asset.maxStake, cfg.maxTradeAmount,
                   user.maxTradeAmount (if set), 1 000 000 000 API cap)
```

The payout bounds are checked first (step 7a, message "…for this expiry…").
The asset, config and user bounds come next, inside the risk check (step 7b,
message "…for <SYMBOL>…"). Both use `STAKE_BELOW_MIN` / `STAKE_ABOVE_MAX`.
Defaults: `cfg.minTradeAmount = 1 000`, `cfg.maxTradeAmount = 10 000 000`.

---

## 7. DEMO vs LIVE

Each user has a LIVE wallet and a DEMO wallet. The DEMO wallet is opened
lazily by `EnsureDemoWallet` and funded once with `DemoOpeningBalance =
1 000 000` (₹10 000), keyed `demo:open:<userId>`
(`server-go/internal/service/wallets/demo.go`). It can be reset only when no
DEMO trade is `OPEN`/`SETTLING` and nothing is locked. The USDT `FUNDING`
wallet cannot trade.

| Aspect | LIVE | DEMO |
|---|---|---|
| Kill switch, user status, asset, hours, expiry, feed gate, slippage, payout | yes | yes, identical |
| Stake bounds, open-trade count | yes | yes (open-trade count is **per account**) |
| User/asset/net/platform exposure, concentration limits | yes | **skipped** (`CheckTradeLimits` returns early) |
| Exposure counters (`exp:asset:*`, `exp:user:*`), sentiment | `ApplyExposureDelta` ±1 | never touched; excluded from the rebuild (`NotDemo()`) |
| Hedge evaluation | queued via `hedge:queue` on each exposure delta | none (no exposure delta) |
| Wallet, ledger keys, settlement path, draw policy, early close | same | same (DEMO wallet) |
| Metrics `trades.volume`, `platform.gross_result`, `customer.payout` | counted | not counted |
| Admin P&L / dashboards (`server-go/internal/service/admin/admin.go`) | included | excluded via `models.NotDemo()` |
| Reconciliation `platform_result_consistency` | included | excluded |
| Reconciliation `locked_vs_open_positions`, `settled_trades_vs_settlements` | included | included (per wallet / overall counts) |
| Idempotency fingerprint | no `accountType` | includes `accountType` |

`accountType` is stored on the trade and on the settlement row. A missing value
is read as LIVE (`models.NormAccountType`). The list filter `accountType=LIVE`
also matches old documents without the field.

---

## 8. Market sentiment

`server-go/internal/service/exposure/sentiment.go`, served only on
`GET /assets` (the list, not `GET /assets/{id}`) as `sentiment` per asset
(`server-go/internal/api/assets/routes.go`).

```
DeriveSentiment(upStake, downStake):
  total = upStake + downStake
  total ≤ 0  → nil                      → upPercent = downPercent = null
  up   = RoundHalfUp(upStake / total × 100)
  down = 100 − up
```

Inputs are the LIVE exposure counters (stake-weighted, not trade-count
weighted). They cover trades from acceptance until their exposure is released
at settlement, cancellation or early close, so `SETTLING` trades still count.
`openTrades` is also returned. **Amounts are never exposed**, only ratios,
because the book size would tell a trader how close an exposure limit is.

The result is `null`, not 50/50, for an empty book. A made-up even split would
read as real crowd information, so the client renders `--` instead.

---

## 9. Websocket events

Events are published through Redis pub/sub (`pubsub:broadcast`, fire and forget,
`server-go/internal/platform/ws/events.go`). Names are in
`server-go/internal/config/constants.go`. Channels: `user:<id>`, `admin:ops`,
`admin:risk`.

| Event | Channels | Payload | Emitted by |
|---|---|---|---|
| `wallet.updated` | `user:<id>` | `WalletView` (incl. `accountType`, `availableBalance`) | every applied wallet mutation (`performMutation`): stake lock at acceptance, settlement, refund, cancel, early close. Not sent when a replay is detected before the transaction |
| `trade.created` | `user:<id>`, `admin:ops` | `BinaryTradeView` | end of `acceptTrade` (not on replays) |
| `exposure.updated` | `admin:risk` | `ExposureSnapshot` | each LIVE `ApplyExposureDelta` |
| `trade.expiring` | `user:<id>` | `{tradeId, symbol, expiryTimestamp}` | expiry worker, when the trade is handed to the settlement queue (not by the sweeper) |
| `trade.settled` | `user:<id>`, `admin:ops` | `BinaryTradeView` (final) | `applySettlement`; `CloseTradeEarly` |
| `trade.updated` | `user:<id>`, `admin:ops` | `BinaryTradeView` | admin `CancelTrade` |
| `notification.created` | `user:<id>` | notification view | `TRADE_SETTLED` notification after expiry settlement |

Typical LIVE sequence: `wallet.updated` (lock) → `exposure.updated` →
`trade.created` → … → `trade.expiring` → `wallet.updated` (settle) →
`exposure.updated` → `trade.settled` → `notification.created`. Each event is
published from its own goroutine, so the order clients receive them in is not
guaranteed.

---

## 10. Edge cases worth knowing

- **Entry timestamp vs entry tick.** `entryPrice` comes from a tick read at
  step 5, while `entryTimestamp` is taken at step 8. The tick can be up to the
  accepted feed age old (≤ 300 ms with default thresholds).
- **Draw equality is exact `float64` equality** on stored prices.
- **The expiry price can come from after expiry.** The `LAST_KNOWN_TICK`
  fallback is used when no tick ≤ expiry is left in `md:hist`. Tick history
  and the last-tick hash are kept only 15 minutes (`TickHistoryRetentionMs`),
  and only in Redis. After a long outage or a Redis loss, a trade can settle
  on a tick published after its expiry.
- **The 2 s grace is not a freshness check.** If no post-expiry tick arrives,
  the trade settles on the newest tick ≤ expiry, however old it is.
- **Cancelling** first sets `CANCELLED`, then releases funds. If the release
  fails, the trade is cancelled but the stake stays locked
  (`locked_vs_open_positions` reconciliation flags it). Cancelled trades have
  no settlement row.
- **Early close and crashes.** A crash after the early-close claim leaves a
  `SETTLING` trade that the sweeper re-claims after 30 s. If its expiry has
  not passed yet, `GetPriceAtOrBefore` returns the newest tick, so it settles
  *before* expiry at the current price. A crash after the early-close wallet
  op but before the row makes expiry settlement replay the wallet key (no
  money moves) and write a WIN/LOSS/DRAW row whose amounts differ from what
  was paid. A crash after the row but before the trade update makes
  `ensureTradeFinalised` map `EARLY_CLOSE` to status **`DRAW`**
  (`finalStatusFor` has no `EARLY_CLOSE` case) with `settlementAmount 0`.
- **Early close uses any last tick.** It does not check feed health, so the
  tick can be up to 15 min old. With `minSecondsRemainingForEarlyClose = 0`
  (allowed by the admin API), a trade still `OPEN` during the 2 s post-expiry
  grace can be closed for `0.01 × fullPayout × (1−fee)` even when it has
  already lost.
- **Payout upsert is not atomic.** The old row is closed, then the new one is
  inserted. A failed insert leaves no active row. The risk config, by
  contrast, reinstates the previous version. There is no `minStake ≤ maxStake`
  check.
- **Removing an expiry** from `supportedExpiries` does not deactivate its payout
  rows. Acceptance refuses it at step 4 anyway.
- `ExpiryTickSequence` / `ExpiryProviderTimestamp` exist on the trade model
  but are never written. `RedisKeys.ExpiryClaim` is defined but unused.

---

## 11. Code vs README contradictions

1. **Early close "in the same transaction".** The README says exposure is
   released and the trade removed from the expiry queue "in the same
   transaction". The code (`earlyclose_service.go`) does these as separate
   Redis calls *after* the Mongo writes, with no transaction.
2. **"The request carries only asset, direction, amount and expiry."** The body
   also accepts `quotedPrice`, `idempotencyKey` and `accountType`. None of
   them can influence the price or the outcome, but they exist.
3. **"There is a test that proves the outcome is identical hedged and
   unhedged."** No such Go test exists. It was a Node suite that the README
   itself later lists as not ported. The Go tests cover only pure functions
   (`DetermineResult`, `ApplyDrawPolicy`, `QuoteEarlyClose`, `AssessSlippage`,
   summaries, risk limits).
4. **Payout `minStake`/`maxStake` "null falls back to the asset bounds"**
   (comment in `server-go/internal/models/payout_config.go`). The asset bounds
   always apply in the risk check, whether or not the payout bounds are set.
   This is a code-comment inaccuracy, not a README one.
5. **No-price retry** (code comment, not the README). The comment in
   `server-go/internal/service/settlement/worker.go` says the `SKIPPED_NO_PRICE`
   re-queue lets "the next pass try again once a price arrives". That re-queue
   always ends in `CLAIMED_ELSEWHERE`, because the claim is fresh. The real
   retry is the stale-SETTLING sweep (30–60 s), which the comment in
   `settlement.go` describes correctly. The README only says settlement
   "defers rather than guessing", which is accurate.

Points that do match the README: the wallet lock model (open: locked += stake;
win: balance += profit; loss: balance −= stake; draw: release), the
concentration rule only applying to a material book, settlement not being
leader-elected, DEMO excluded from exposure/hedging/P&L, the ₹10 000 DEMO
balance, and the quote formula and clamp.

---

## 12. Where to look

| Topic | Files |
|---|---|
| Trade API, body validation, audit | `server-go/internal/api/binary/routes.go` |
| Acceptance chain, idempotency, cancel, queue rebuild | `server-go/internal/service/binary/binary.go` |
| Slippage guard | `server-go/internal/service/binary/slippage.go` |
| Early-close pricing / service | `server-go/internal/service/binary/earlyclose.go`, `earlyclose_service.go` |
| Trade summary totals | `server-go/internal/service/binary/summary.go` |
| Expiry detection, sweeper, worker | `server-go/internal/service/expiry/expiry.go`, `worker.go` |
| Settlement, draw policy, consumer | `server-go/internal/service/settlement/settlement.go`, `worker.go` |
| Feed health, expiry price, volatility | `server-go/internal/service/marketdata/marketdata.go` |
| Risk limits | `server-go/internal/service/risk/risk.go` |
| Risk config and defaults | `server-go/internal/service/riskconfig/riskconfig.go`, `server-go/internal/models/risk_config.go`, `server-go/internal/api/admin/core.go` |
| Wallet mutations, ledger keys | `server-go/internal/service/wallets/wallet.go`, `demo.go`, `orphan_locks.go` |
| Exposure counters, sentiment | `server-go/internal/service/exposure/exposure.go`, `sentiment.go` |
| Assets, trading hours | `server-go/internal/service/assets/assets.go`, `server-go/internal/models/asset.go`, `server-go/internal/platform/util/util.go` |
| Payouts | `server-go/internal/service/payouts/payouts.go`, `server-go/internal/models/payout_config.go`, `server-go/internal/api/admin/assets.go` |
| Asset catalogue API (`drawPolicy`, `sentiment`) | `server-go/internal/api/assets/routes.go` |
| Admin trade routes (cancel) | `server-go/internal/api/admin/trading.go` |
| Models, statuses, indexes | `server-go/internal/models/binary_trade.go`, `settlement.go`, `domain.go`, `wallet.go` |
| Redis keys, WS events, channels | `server-go/internal/config/constants.go` |
| Error codes | `server-go/internal/platform/apperr/errors.go` |
| Worker boot, leader election | `server-go/internal/workers/workers.go`, `server-go/internal/platform/leader/leader.go` |
| Reconciliation | `server-go/internal/service/reconciliation/reconciliation.go` |
