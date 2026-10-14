# 03 - Platform logic: risk, market data, auth, realtime

Audience: engineers joining the project. This document describes how the Go
backend (`server-go/`) manages the house book, prices, sessions and live
updates. Everything here comes from the code. File paths are relative to the
repo root. Where the code and the READMEs disagree, the code wins, and the
disagreement is listed in [section 11](#11-code-vs-documentation-disagreements).

Conventions used throughout:

- **Money is integer minor units** (paise/cents) everywhere: Mongo, API and
  websocket payloads. "₹100.00" is `10000`.
- **Times are epoch milliseconds**, and the server stamps them itself
  (`util.NowMs()`). Client clocks and client prices are never trusted.
- **MongoDB is the record and Redis is a cache.** Exposure counters, the
  expiry queue, ticks and rate-limit counters live in Redis. Anything
  financial can be rebuilt from Mongo.
- **DEMO trades never enter the house book.** That means no exposure, no
  hedging, no house limits and no admin P&L. Only LIVE trades are covered by
  sections 1 to 3.

---

## 1. Exposure engine

Code: `server-go/internal/service/exposure/exposure.go`, `sentiment.go`,
`server-go/internal/models/exposure.go`.

### 1.1 Storage

| Store | Key / collection | Contents |
|---|---|---|
| Redis hash | `exp:asset:<assetId>` | `upStake`, `downStake`, `upProfit`, `downProfit`, `openTrades`, `symbol`, `updatedAt` |
| Redis set | `exp:assets` | asset ids that have counters |
| Redis hash | `exp:user:<userId>` | one field per `<assetId>` (open stake on that asset) plus `total` |
| Mongo | `exposures` (unique `assetId`) | durable snapshot of the derived figures plus `hedgedNotional` |

### 1.2 Derived figures (`DeriveExposure`, pure)

Each raw counter is clamped with `max(0, ·)` first. Then:

```
netExposure          = upStake - downStake        (signed: + means the book leans UP)
grossExposure        = upStake + downStake
upPotentialPayout    = upStake + upProfit         (paid out if every UP trade wins)
downPotentialPayout  = downStake + downProfit
potentialLiability   = max(0, upProfit - downStake, downProfit - upStake)
openTrades           = max(0, openTrades)
```

`potentialLiability` is the platform's worst-case **net** loss. The winning
side's profits cost the house only the part that the losing side's forfeited
stakes do not cover.

**Worked example** (the spec example, every trade on an 85% payout):

| | UP | DOWN |
|---|---|---|
| stake | 10,000 | 4,000 |
| profit (85%) | 8,500 | 3,400 |

- net = 10,000 - 4,000 = **6,000** (leans UP)
- gross = 10,000 + 4,000 = **14,000**
- upPotentialPayout = 18,500 and downPotentialPayout = 7,400
- potentialLiability = max(0, 8,500 - 4,000, 3,400 - 10,000) = **4,500**.
  If UP wins, the house pays 8,500 in profit and keeps 4,000 of DOWN stakes.

Net drives hedging, which offsets the direction the book leans. Gross drives
the asset and platform ceilings, because it is the total stake at risk.

### 1.3 Updates (`ApplyExposureDelta`)

One Redis pipeline (not MULTI) does the following:
`SADD exp:assets`, `HINCRBY` stake/profit/openTrades by `sign`, `HSET symbol,
updatedAt`, and `HINCRBY` the user hash for the asset field and `total`. After
the pipeline it re-reads the snapshot and fire-and-forgets
`exposure.updated` to `admin:risk`. It also `LPUSH`es the asset id onto
`hedge:queue` (in a goroutine), so the hedge worker re-evaluates that asset.

| Event | Caller | sign |
|---|---|---|
| Trade accepted (LIVE only) | `service/binary/binary.go` `acceptTrade` step 11, after the Mongo insert and `ZADD expiry:queue` | `+1` |
| Expiry settlement | `service/settlement/settlement.go` after the trade's final `FindOneAndUpdate` | `-1` |
| Early close ("Sell now") | `service/binary/earlyclose_service.go` after the `CLOSED_EARLY` update | `-1` |
| Admin cancel | `service/binary/binary.go` (cancel path) after funds are released | `-1` |

The delta always uses the **frozen** `stake` and `profit` stored on the trade,
so the `+1` and `-1` cancel exactly. The Redis write happens *after* the Mongo
write and outside any Mongo transaction. If the process crashes between the
two, the counters drift until the next rebuild.

### 1.4 Rebuild at boot (`RebuildExposureFromDatabase`)

`internal/workers/workers.go` runs the rebuild before any worker starts, on
every instance with `WORKERS_ENABLED=true`:

1. It aggregates `binary_trades` with `status ∈ {OPEN, SETTLING}` and a
   non-DEMO account, grouped by `(assetId, direction)`: Σstake, Σprofit and a
   count.
2. For **every** asset in the registry it runs `DEL` and then `HSET`, writing
   zeros for assets with no open trades.
3. For users it deletes every `exp:user:*` key (found with `KEYS`) and
   rewrites them from a `(userId, assetId)` aggregation.

Edge case: the rebuild is not leader-gated or atomic. Suppose a second worker
instance boots while the first is accepting trades. Any increments that land
between the aggregation and the rewrite are lost or doubled until the next
boot.

The durable `exposures` snapshot is rewritten every risk-worker tick by
`PersistAllExposures`. That is an upsert per asset that adds `hedgedNotional`.
Failures are logged, never returned.

### 1.5 Sentiment (customer-facing)

`GetSentiment` exposes only the UP/DOWN **ratio**:
`up = round(upStake / (up+down) × 100)` and `down = 100 - up`. When the book
is empty both are `null`, never an invented 50/50. The endpoint never returns
stake amounts. It is served from `server-go/internal/api/assets/routes.go`.

---

## 2. Risk engine

Code: `server-go/internal/service/risk/risk.go`, `worker.go`,
`server-go/internal/service/riskconfig/riskconfig.go`,
`server-go/internal/models/risk_config.go`.

### 2.1 Risk configuration: versioned and append-only

The `risk_configs` collection has `scope = "GLOBAL"`. Its indexes are unique
`(scope, version desc)` and a **partial unique** `scope` where `active: true`,
so only one row can be active.

- **Read:** `GetRiskConfig` keeps an in-process cache for **2 s**, uses
  `singleflight` so concurrent cold reads share one query, and returns a
  private clone. The row is decoded over `DefaultRiskConfig()`, so a field
  added to the schema after a row was stored gets its default rather than the
  Go zero value. Config changes therefore reach other instances within about
  2 s, and that includes the kill switch.
- **Bootstrap:** on an empty collection, version 1 is inserted from the
  defaults below. Only the four feed thresholds are copied from env. If two
  processes race to insert, the loser reads the winner's row.
  `cmd/server/main.go` forces this at boot and fails fast on error.
- **Update** (`UpdateRiskConfig`), called from `PUT /admin/settings/risk`,
  which needs `risk:write` and the AdminWrite rate limit:
  1. Clone the active row, apply the patch and run `ValidateConfig`.
  2. Set the active row to `active=false`, then insert `version+1` with
     `updatedBy` and `note`. If the insert fails, the old row is re-activated.
  3. Invalidate the cache. The handler then writes an audit row synchronously
     (`audit.RiskConfigUpdated`, holding the full before/after) before it
     responds.
- **History:** `ListRiskConfigVersions` returns the newest versions first,
  50 by default.
- `ValidateConfig` checks:
  - `feedHealthy < feedDegraded < feedStale`
  - `minTradeAmount ≤ maxTradeAmount`
  - `0 ≤ hedgeThresholdPercent ≤ 100`
  - hedge levels sorted by `fromPercent` must not be empty (`from < to`) and
    must not overlap.

Field defaults (`DefaultRiskConfig`) and the bounds that
`server-go/internal/api/admin/core.go` `parseRiskPatch` accepts:

| Field | Default | Admin bounds | Used by |
|---|---|---|---|
| `maxAssetExposure` | 5,000,000_00 | ≥0 | gross limit (with asset `exposureLimit`) |
| `maxUserExposure` | 500,000_00 | ≥0 | per-user open stake |
| `maxGrossExposure` | 20,000,000_00 | ≥0 | platform-wide gross |
| `minTradeAmount` / `maxTradeAmount` | 10_00 / 100,000_00 | ≥0 | stake bounds |
| `maxOpenTradesPerUser` | 50 | ≥1 | per-account open count |
| `maxUserAssetConcentrationPercent` | 40 | 1..100 | concentration rule |
| `hedgeThresholdPercent` | 30 | 0..100 | MEDIUM band and hedge trigger |
| `criticalExposurePercent` | 90 | 1..100 | CRITICAL band |
| `hedgeLevels` | see 3.1 | ≥1 item; name 2..40 chars, from/to ≥0, ratio 0..1 | hedge sizing |
| `minSecondsToExpiryForHedge` | 10 | ≥0 | hedge suppression |
| `volatilityHedgeBoost` | 0.1 | 0..1 | hedge ratio add-on |
| `slippageTolerancePercent` | 0.1 | 0..100 (0 disables) | trade price-moved guard |
| `earlyCloseEnabled` / `earlyCloseFeePercent` / `minSecondsRemainingForEarlyClose` | true / 5 / 10 | - / 0..50 / 0..3600 | early close |
| `drawPolicy` | `REFUND` | REFUND, LOSS or WIN | settlement |
| `feedHealthyMaxMs` / `feedDegradedMaxMs` / `feedStaleMaxMs` / `feedTickMaxAgeMs` | 100 / 300 / 1000 / 5000 (bootstrapped from env) | ≥1 | feed health (4.4) |
| `haltTradingOnStaleFeed` | true | bool | feed gate |
| `haltTradingOnDegradedFeed` | false | bool | feed gate |
| `tradingHalted` | false | bool | global kill switch |
| `note` | - | ≤500 chars | stored on the version |

### 2.2 Risk scoring (`AssessRisk`, pure)

Inputs: the exposure snapshot, the asset limits, the config, global gross,
realised volatility, feed status and the seconds to the nearest open LIVE
expiry.

```
assetGrossLimit    = min(asset.exposureLimit, cfg.maxAssetExposure)
grossUtilisation   = gross / assetGrossLimit × 100
netUtilisation     = |net| / asset.netExposureLimit × 100
globalUtilisation  = platformGross / cfg.maxGrossExposure × 100
utilisation        = max(gross, net, global)          (the binding constraint)

score = utilisation
      + feedPenalty  {HEALTHY 0, DEGRADED 8, STALE 20, OFFLINE 35}
      + (vol > 0.0015 ? min(15, vol / 0.0015 × 5) : 0)
      + (secondsToNearestExpiry ≤ 10 and openTrades > 0 ? 5 : 0)
```

A zero denominator gives 0% (`util.SafePercent`). Volatility is the sample
standard deviation of per-tick log returns over the last 60 s of tick history.
It returns 0 when there are fewer than 3 ticks.
`ElevatedVolatility = 0.0015`. Each component adds a human-readable string to
`reasons[]`. `score` and `utilisation` are rounded to 2 dp and the hedge ratio
to 4 dp, using JS `toFixed` semantics.

**Levels** (`ClassifyRiskLevel`) come from the config, not from constants:

```
critical = cfg.criticalExposurePercent        (default 90)
high     = critical × 0.7                     (63)
medium   = min(cfg.hedgeThresholdPercent, high)  (30)
score ≥ critical → CRITICAL;  ≥ high → HIGH;  ≥ medium → MEDIUM;  else LOW
```

The result is cached in the Redis hash `risk:asset:<id>`.
`GetCachedAssessment` re-runs the assessment whenever that key exists, so it
is not really a cache read. `AssessAllAssets` broadcasts the full list as
`risk.updated` on `admin:risk`.

**Risk worker** (leader-only, `RISK_INTERVAL_MS`, default 1000): each tick it
runs `AssessAllAssets`, then `PersistAllExposures(hedgedByAsset)`, and logs a
warning listing every CRITICAL asset.

### 2.3 Pre-trade limits (`CheckTradeLimits`, pure; `AssertTradeWithinRiskLimits`)

The checks run inside `acceptTrade`, after the halt, account, asset, expiry,
feed, slippage and per-expiry payout stake checks. Every violation is
collected. The **first** one becomes the error (HTTP 400) and all of them go
in `details.violations`.

| # | Rule | Error code | Applies to DEMO |
|---|---|---|---|
| 1 | `stake < max(asset.minStake, cfg.minTradeAmount)` | `STAKE_BELOW_MIN` | yes |
| 2 | `stake > min(asset.maxStake, cfg.maxTradeAmount, user.maxTradeAmount?)` | `STAKE_ABOVE_MAX` | yes |
| 3 | open trades on this account ≥ `cfg.maxOpenTradesPerUser` (counted per LIVE/DEMO account) | `USER_LIMIT_EXCEEDED` | yes |
| 4 | `userTotal + stake > (user.maxUserExposure ?? cfg.maxUserExposure)` | `USER_LIMIT_EXCEEDED` | no |
| 5 | `gross + stake > min(asset.exposureLimit, cfg.maxAssetExposure)` | `EXPOSURE_LIMIT_EXCEEDED` | no |
| 6 | `|net ± stake| > asset.netExposureLimit` (+ for UP, - for DOWN) | `EXPOSURE_LIMIT_EXCEEDED` | no |
| 7 | `platformGross + stake > cfg.maxGrossExposure` | `RISK_LIMIT_EXCEEDED` | no |
| 8 | concentration (below) | `RISK_LIMIT_EXCEEDED` | no |

**The concentration rule only engages on a material book.** It is evaluated
only when `projectedGross (gross + stake) > userExposureLimit`. In that case it
fails when `(userAssetStake + stake) / projectedGross × 100 >
maxUserAssetConcentrationPercent`. Below that size the per-user limit (rule 4)
already binds. The guard exists because the first customer on a newly listed
asset is unavoidably 100% of the book.

Per-user overrides `maxTradeAmount` and `maxUserExposure` live on the user
document (`null` means use the config value). They are set by
`PATCH /admin/users/{id}`, which needs `users:write`.

Related gates in the same chain: `TRADING_HALTED` (503, from
`cfg.tradingHalted`), `FEED_UNHEALTHY` and `NO_PRICE_AVAILABLE` (503, see 4.5),
and `PRICE_MOVED` (409, slippage beyond `slippageTolerancePercent`; the
details carry `serverPrice`, `quotedPrice`, `movePercent` and
`tolerancePercent`).

---

## 3. Hedging

Code: `server-go/internal/service/hedging/hedging.go`, `worker.go`,
`repository.go`, `providers/{provider,paper,rest}.go`.

### 3.1 Hedge target (`risk.ComputeHedgeTarget`)

Default `hedgeLevels` (bands are percentages of utilisation):

| Name | from | to | hedgeRatio |
|---|---|---|---|
| NO_HEDGE | 0 | 30 | 0 |
| PARTIAL_HEDGE | 30 | 60 | 0.4 |
| HIGH_HEDGE | 60 | 80 | 0.7 |
| MAX_ALLOWED_HEDGE | 80 | 100,000 | 0.95 |

`SelectHedgeLevel` picks the band with `from ≤ utilisation < to`, falling back
to the highest band. The decision is `NO_HEDGE` in each of these cases:

- `!asset.hedgeEnabled`
- no band was found, or the band's ratio is ≤ 0
- `utilisation < (asset.hedgeThresholdPercent ?? cfg.hedgeThresholdPercent)`
- `secondsToNearestExpiry < cfg.minSecondsToExpiryForHedge`

Otherwise:

```
ratio    = clamp01(band.hedgeRatio × asset.targetHedgeRatio × 2 + (vol > 0.0015 ? cfg.volatilityHedgeBoost : 0))
notional = min(round(|net| × ratio), asset.maxHedgeNotional)
notional < asset.minHedgeNotional → NO_HEDGE
```

The asset default `targetHedgeRatio` is 0.5, so `× 2` makes the default
neutral (`models/asset.go`). Other asset defaults are
`minHedgeNotional = 1,000_00`, `maxHedgeNotional = 1,000,000_00` and
`netExposureLimit = 2,000,000_00`.

Worked example. Utilisation is 60% and is binding, net is +120,000,000,
volatility is normal, and the asset uses the defaults.
The band is HIGH_HEDGE, so ratio = 0.7 × 0.5 × 2 = 0.7 and
target = 84,000,000. The **risk level** is still MEDIUM (60 < 63). The hedge
band and the risk level are independent scales.

### 3.2 Order sizing and placement (`EvaluateAndHedge`)

1. `currentSigned` is the sum of `filledNotional` over orders in
   `{PENDING, PARTIALLY_FILLED, FILLED}` with `closedAt = null`, counting
   UP-side orders as + and DOWN-side orders as -.
2. `ComputeHedgeAdjustment`: `targetSigned = sign(net) × target` and
   `delta = targetSigned - currentSigned`. If `|delta| < asset.minHedgeNotional`
   nothing is placed. Otherwise the side is BUY when delta > 0 and SELL when it
   is not. A book leaning UP means the house is short the market, so it hedges
   long. When net goes back to 0 the target is 0 and the existing hedge is
   unwound with an opposite order.
3. If there is no last tick, the evaluation is skipped with "no reference
   price available".
4. The idempotency key is `sha256(assetId:side:notional:targetSigned:currentSigned)`.
   If an order already has that key, it is returned instead of placing a
   duplicate.
5. A `PENDING` order is persisted **before** the venue call, so a lost
   response is still reconcilable. It records a decision snapshot: net/gross
   at decision, utilisation, risk level, hedge level, target, existing hedge
   and `riskConfigVersion`. `hedge.created` is emitted. A BUY is stored with
   `side = UP` and a SELL with `side = DOWN`.
6. `provider.PlaceOrder` runs. On error the order becomes `FAILED` with the
   error message. On success the status, rounded `filledNotional`,
   `averageFillPrice` and latencies are stored. `hedge.updated` is emitted
   either way.

**Worker** (leader-only, `HEDGE_INTERVAL_MS`, default 1000):

- Each tick drains up to 200 entries of `hedge:queue`, deduplicated, and
  evaluates those assets.
- Every `round(15000 / interval)` ticks (about 15 s) it does a full pass over
  active `hedgeEnabled` assets that were not just evaluated. On the same pass
  it runs `ReconcilePendingHedgeOrders`, which polls the venue for up to 200
  PENDING or PARTIALLY_FILLED orders, and `MarkHedgePositions`:
  `unrealisedPnl = round(filled × (tick - avgFill) / avgFill × (UP ? +1 : -1))`,
  applied only to FILLED orders.
- An evaluation error is logged and never stops the loop.

Admin cancel: `POST /admin/hedging/orders/{id}/cancel` needs `hedge:write`.
FILLED and CANCELLED orders are refused with `CONFLICT`. Otherwise the order
is cancelled at the venue if it has a provider id, then set to `CANCELLED` and
`closedAt` is stamped.

### 3.3 Providers (`HEDGE_PROVIDER`)

| Provider | `Live()` | Behaviour |
|---|---|---|
| `paper` (default) | false | In-process ledger. Fill = `notional × partialFillRatio` (default 1). Fill price = `ref × (1 ± 2 bps)` (+ for BUY). Optional `failureRate` and `latencyMs`. Idempotent on its key. Nothing leaves the process. |
| `broker` | true | REST: `POST /orders`, `DELETE /orders/{id}` and `GET /orders/{id}`. Statuses FILLED and COMPLETE count as filled. |
| `lp` | true | REST: `POST /quotes` (key `<idem>:quote`), then `POST /executions` for `min(notional, quote.maxNotional)`. An expired quote is FAILED. Statuses FILLED and EXECUTED count as filled. |

**Live execution fails closed.** `restBase.assertExecutable` runs before every
request and returns:

- `LIVE_EXECUTION_DISABLED` (503) unless `HEDGE_LIVE_EXECUTION_ENABLED=true`
- `HEDGE_NOT_CONFIGURED` (503) unless `HEDGE_API_URL`, `HEDGE_API_KEY` and
  `HEDGE_API_SECRET` are all set.

Requests send `x-api-key` and `idempotency-key` with a 30 s timeout. At first
use, `GetHedgeProvider` warns when a live provider is selected but disabled,
and when production is running on paper. `GET /ready` and the boot log report
`liveHedging = provider.Live() && HEDGE_LIVE_EXECUTION_ENABLED`.

### 3.4 Settlement never reads hedges

The hedging engine takes only aggregate per-asset exposure as input. The
service import DAG (`server-go/CONVENTIONS.md`) is
`settlement -> exposure, marketdata, riskconfig, metrics, wallets,
notifications`. It contains no `hedging`, and
`server-go/internal/service/settlement/settlement.go` contains no reference to
hedge orders. A customer's result therefore depends only on entry price,
expiry price, payout terms and draw policy. The guarantee holds by
construction, through package dependencies. The Go suite has no test that
proves it (see section 11).

---

## 4. Market data

Code: `server-go/internal/service/marketdata/{marketdata,worker,candles,change24h}.go`,
`providers/{provider,factory,simulated,binance,genericws,wsclient}.go`,
`server-go/internal/api/marketdata/routes.go`.

### 4.1 Providers (`MARKET_DATA_PROVIDER`)

| Provider | Authoritative | Notes |
|---|---|---|
| `simulated` (default) | **false** | Ticks every 50 ms. Mean-reverting log random walk (OU, half-life 4 h) with momentum 0.6. Per-symbol minute volatility and seed prices for btcusdt, ethusdt, xauusd, eurusd, gbpusd and usdjpy. |
| `binance` | true | `wss://stream.binance.com:9443/stream?streams=<sym>@trade/...`. No key needed. Timestamp is `T`, falling back to `E` and then now. Sequence is the trade id. |
| `generic-ws` | true | Requires `MARKET_DATA_WS_URL` (validated at boot). Optional `Authorization: Bearer MARKET_DATA_API_KEY`. Sends `{action:"subscribe"/"unsubscribe", symbols}`. Accepts objects or arrays with `symbol|s`, `price|p`, `bid|b`, `ask|a`, `timestamp|t` and `sequence|seq`. |

An unknown name falls back to simulated. Websocket providers reconnect with
backoff `1000 × 2^min(attempt,5)` ms, capped at 30 s, so 2 s, 4 s, 8 s,
16 s, 30 s. Handshake timeout is 15 s. Connection states are CONNECTING,
CONNECTED, RECONNECTING, DISCONNECTED and FAILED. Each one is recorded in
memory, mirrored to the `md:provider:status` hash, and emitted as
`market.status` on `admin:market`.

**Production refuses a non-authoritative feed** at two layers:

1. `config.Load` fails when `NODE_ENV=production` and
   `MARKET_DATA_PROVIDER=simulated`, unless
   `ALLOW_SIMULATED_FEED_IN_PRODUCTION=true`.
2. `providers.GetMarketDataProvider` refuses any `!Authoritative()` provider in
   production unless that flag is set, and logs a warning for a
   non-authoritative feed in any environment.

`MARKET_DATA_API_SECRET` is loaded but not used by any provider.

### 4.2 Ingestion and normalisation

The market-feed worker is a leader loop: it runs every 5 s with a default lock
TTL of 25 s. It owns the provider connection, and on each tick it brings
subscriptions into line with the lower-cased `providerSymbol` of the active
assets. It reconnects if the provider drops.

Ticks go into an in-process channel with a buffer of 8192. When the buffer is
full, ticks are dropped with a warning. `receivedAt` is stamped on arrival. A
single goroutine drains the channel in batches of up to 512, which keeps
arrival order.

`normaliseTick`:

- Matches the provider symbol to an asset (`FindAssetByProviderSymbol`).
  Unknown symbols are ignored.
- Discards NaN, ±Inf and prices ≤ 0.
- Sets `timestamp = providerTimestamp` (or `receivedAt` when the provider
  gives none).
- Sets `feedLatencyMs = max(0, receivedAt - providerTimestamp)`,
  `processingLatencyMs = processedAt - receivedAt` and
  `totalLatencyMs = max(0, processedAt - providerTimestamp)`.

`writeTicks` makes **one Redis pipeline per batch**:

- `HSET md:tick:<id>` with the last tick plus the provider name, and a 15 min
  `PEXPIRE`.
- `ZADD md:hist:<id>` scored by the **provider timestamp**, then
  `ZREMRANGEBYSCORE` for anything older than `receivedAt - 15 min`, and a
  15 min `PEXPIRE`.
- `PUBLISH` of a `market.tick` envelope to `market:<id>` and `admin:market`.
- Latency samples.

Tick history retention is the constant
`config.TickHistoryRetentionMs = 15 min`. It is not an env var.

### 4.3 Expiry price

`GetPriceAtOrBefore` returns the newest history entry with `score ≤ expiry`
(source `TICK_AT_OR_BEFORE_EXPIRY`), falling back to the last tick
(`LAST_KNOWN_TICK`). The expiry worker waits until `HasTickAtOrAfter(expiry)`
is true, or for `ExpiryTickGraceMs = 2000`, before it queues a trade for
settlement (`service/expiry/expiry.go`).

### 4.4 Feed health

`ClassifyFeed` (pure) combines freshness and transport latency, and the worse
of the two wins:

```
ageMs = now - lastTick.receivedAt
if no tick                 → OFFLINE
if ageMs > tickMaxAgeMs    → OFFLINE                (default 5000)
effective = max(totalLatencyMs, ageMs)
effective ≤ healthyMaxMs   → HEALTHY                (default 100)
effective ≤ degradedMaxMs  → DEGRADED               (default 300)
otherwise                  → STALE                  (stale band 300..5000 by default)
```

The thresholds are read from the **active risk configuration**, not from env.
`FEED_HEALTHY_MAX_MS`, `FEED_DEGRADED_MAX_MS`, `FEED_STALE_MAX_MS` and
`FEED_TICK_MAX_AGE_MS` are validated at boot (`healthy < degraded < stale`),
but they only seed version 1 of the config. After that, the runtime override
is `PUT /admin/settings/risk`. `feedStaleMaxMs` is validated and stored, but
`ClassifyFeed` never reads it. Anything above degraded is STALE until the tick
max age is exceeded.

The simulated feed ticks every 50 ms because a slower generator would read as
DEGRADED against the 100 ms healthy band. The `md:health:<id>` key is declared
but unused: health is computed on demand.

### 4.5 Trading gate (`AssertFeedTradable`)

| Feed status | New trades |
|---|---|
| OFFLINE | always refused |
| STALE | refused if `haltTradingOnStaleFeed` (default true) |
| DEGRADED | refused if `haltTradingOnDegradedFeed` (default false) |
| no last tick | refused |

A refused feed status returns `FEED_UNHEALTHY` (503). A missing last tick
returns `NO_PRICE_AVAILABLE` (503). When the gate passes, it returns the tick
that becomes the trade's entry price. Open trades still settle.

### 4.6 Candles

- **Persisted:** only 60 s candles go to the Mongo `candles` collection, which
  is unique on `(assetId, interval, openTime)`. Each tick folds into an
  in-memory bucket keyed by `floor(timestamp / 60000)`. When the next bucket
  opens, the previous one is upserted asynchronously. Open buckets are flushed
  on leadership release and shutdown. Only the leader builds candles.
- **Supported intervals** (s): 5, 15, 30, 60, 300, 900, 1800, 3600, 10800 and
  86400.
- **Over 60 s:** reads `limit × (interval/60)` stored minutes (capped at
  5000), appends the open in-memory bucket, and folds with `foldCandles`. Open
  is the first open, close the last close, and high/low the extremes.
- **Under 60 s:** rebuilt from tick history only, so these frames reach back
  at most 15 minutes.
- **`to` parameter:** limits the series to candles with `openTime ≤ to`.
- **24h change** (`change24h.go`): `(last - firstOpenInLast24h) / firstOpen × 100`,
  rounded to 2 dp. It is cached for 60 s with a 3 s compute timeout, and any
  error degrades to `null`.

### 4.7 Endpoints (`/market-data`, authenticated, `Read` rate limit 600/min)

| Route | Returns |
|---|---|
| `GET /market-data/health` | feed health for every asset |
| `GET /market-data/{id}/price` | `{tick, feed}` |
| `GET /market-data/{id}/candles?limit=10..1000 (300)&interval=<supported> (60)&to=` | `[{time,open,high,low,close}]` |
| `GET /market-data/{id}/ticks?windowMs=1000..900000 (300000)&from=&to=` | raw ticks. `from < to` and `to - from ≤ 15 min`. A window older than retention returns an empty list, not an error. |

Admin: `GET /admin/market-data` needs `market-data:read` and returns the
provider status, feeds and latency report.

---

## 5. Authentication and sessions

Code: `server-go/internal/service/auth/auth.go`,
`server-go/internal/platform/authtoken/token.go`,
`server-go/internal/api/auth/routes.go`,
`server-go/internal/platform/httpx/middleware.go`,
`server-go/internal/models/refresh_token.go`.

### 5.1 Endpoints

| Route | Rate limit | Notes |
|---|---|---|
| `POST /auth/register` | 5/min per IP | Validation below. Creates the LIVE wallet and issues a session. Returns 201. |
| `POST /auth/login` | 10/min per IP | |
| `POST /auth/refresh` | 60/min per IP | body `{refreshToken}` (min 10 chars) |
| `POST /auth/logout` | auth | `{refreshToken?, allSessions?}` |
| `GET /auth/me` | auth | user plus LIVE wallet |
| `POST /auth/change-password` | auth | |
| `POST /admin/login` | 10/min per IP | Ordinary login, then a role gate. A non-admin's new refresh token is revoked immediately and the call returns `FORBIDDEN`. |

Validation:

- email: valid, ≤254 chars, lower-cased and trimmed.
- username: 3..24 chars, `[A-Za-z0-9_]`.
- password: 8..128 chars with at least one lowercase letter, one uppercase
  letter and one digit.
- fullName: 2..120 chars.
- country: exactly 2 letters, upper-cased.

Duplicates return `EMAIL_TAKEN` or `USERNAME_TAKEN` (409).

### 5.2 Passwords and login

- bcrypt with cost `BCRYPT_ROUNDS` (default 12, allowed 8..15). Input is
  truncated to 72 bytes so hashes stay compatible with bcryptjs.
- An unknown email is compared against a dummy cost-12 hash, so timing does
  not reveal whether the account exists.
- **Lockout:** each wrong password increments `failedLoginAttempts`. When it
  reaches **8**, `lockedUntil = now + 15 min` is set and the counter resets.
- The order of checks matters. Credentials are verified *before* the lock is
  checked, so during a lock:
  - a wrong password still returns `INVALID_CREDENTIALS` (401) and keeps
    counting;
  - a correct password returns `ACCOUNT_DISABLED` (403), "Too many failed
    attempts. Try again after <ISO>".
- A non-ACTIVE status returns `ACCOUNT_DISABLED` (403, "Account is
  suspended/closed").
- A successful login resets the counter and stamps `lastLoginAt` and
  `lastLoginIp`.

Every login, failed login, register, refresh, logout and password change
writes an audit row.

### 5.3 Tokens

- HS256, issuer `binary-trading`.
- **Access token:** `{sub, role, permissions, sid, typ:"access"}`, signed with
  `JWT_SECRET`, TTL `JWT_ACCESS_TTL` (default `15m`).
- **Refresh token:** `{sub, sid, fid, typ:"refresh"}`, signed with
  `JWT_REFRESH_SECRET`, TTL `JWT_REFRESH_TTL` (default `30d`).
- The duration format is `^\d+(ms|s|m|h|d)$`. Expiry is computed in whole
  seconds, so a sub-second TTL produces an already-expired token.
- The `permissions` claim holds the **effective** set at issue time (role
  permissions plus the user's extras). `HasPermission` also re-adds the role's
  own permissions on every check.
- Verify errors: an expired token returns `TOKEN_EXPIRED`. A bad token, wrong
  issuer or algorithm, or wrong `typ` returns `TOKEN_INVALID`. All are 401.
- Response body: `{accessToken, refreshToken, accessTokenExpiresIn,
  refreshTokenExpiresIn (seconds), tokenType:"Bearer"}`, plus `sessionId`.

### 5.4 Refresh rotation and replay detection

Refresh tokens are stored in the `refresh_tokens` collection. Each row holds
`tokenHash` (sha256 hex, unique), `familyId`, `sessionId`, `expiresAt` (TTL
index, so Mongo deletes expired rows), `revokedAt` and `replacedBy`.

State machine for one refresh row:

```
issued (revokedAt=null) --refresh--> rotated (revokedAt=now, replacedBy=<new sid>)
issued --logout / change-password / admin-gate--> revoked
issued --expiresAt passed--> expired (TTL-deleted later)
```

`Refresh(token)`:

1. Verify the JWT signature.
2. Look the row up by hash. If no row exists although the signature is valid,
   treat it as **replay**: revoke every row with `familyId = claims.fid` and
   return `TOKEN_INVALID`.
3. If the row has `revokedAt` set, the token was already rotated or revoked.
   Revoke the whole family and return `TOKEN_INVALID`, "Refresh token has been
   revoked". This is the main replay defence: a stolen token used after the
   real client rotated it kills every session in the family.
4. If `expiresAt` has passed, return `TOKEN_EXPIRED`.
5. If the user is missing, return `UNAUTHENTICATED`. If the user is not
   ACTIVE, return `ACCOUNT_DISABLED`.
6. Issue a new pair in the **same family**, then mark the presented row
   rotated.

Logout:

- `allSessions: true` revokes every row for the user.
- Otherwise it revokes the row matching the presented token.
- With neither it does nothing, and the access token stays valid until it
  expires.

Change password: verifies the current password (`INVALID_CREDENTIALS`, "Current
password is incorrect"), rehashes, then **revokes all of the user's refresh
tokens**, the current session included.

### 5.5 Account status enforcement

- `httpx.Authenticate` only verifies the JWT. It does not read the database.
- `httpx.RequireActiveUser` re-reads the user. It returns `ACCOUNT_DISABLED`
  (403) when the status is not ACTIVE, and `TOKEN_INVALID`, "Session is stale",
  when the stored role differs from the token's role. It is attached only to
  `POST /binary/trades`, `POST /binary/trades/{id}/close` and transfer writes.
- `acceptTrade` re-checks the status as well.
- Suspending a user (`PATCH /admin/users/{id}` with `status`) revokes no
  tokens. Read endpoints and existing websockets keep working until the access
  token expires (at most 15 min by default). The next refresh then fails with
  `ACCOUNT_DISABLED`.

### 5.6 Rate limiting

A fixed-window counter in Redis (`rl:<bucket>:<identity>:<windowIndex>`,
`INCR` plus `PEXPIRE`), shared across instances. Responses carry
`X-Ratelimit-Limit`, `X-Ratelimit-Remaining` and `X-Ratelimit-Reset`. Over the
limit, the response is 429 `RATE_LIMITED` with `Retry-After` and
`details.retryAfterMs`. **The limiter fails open** when Redis is unreachable
and logs an error.

| Bucket | Limit/min | Identity |
|---|---|---|
| auth:login | 10 | IP |
| auth:register | 5 | IP |
| auth:refresh | 60 | IP |
| binary:trade | 60 | user |
| wallet | 120 | user |
| wallet:transfer | 10 | user |
| support:chat | 60 | user |
| read | 600 | user, else IP |
| admin | 600 | user |
| admin:write | 120 | user |

The client IP is the first `X-Forwarded-For` entry when the header is present.
It is trusted unconditionally, so without a proxy that overwrites the header,
the IP-keyed limits can be bypassed.

### 5.7 Production secret checks (`config.Load`)

Every problem is collected and the process exits:

- `JWT_SECRET` and `JWT_REFRESH_SECRET` must each be ≥32 chars (in every
  environment).
- In production:
  - the two secrets must differ;
  - `JWT_SECRET` must not start with `change-me`;
  - the simulated feed is refused (see 4.1).
- `BCRYPT_ROUNDS` must be between 8 and 15.

Note that `JWT_REFRESH_SECRET` is not checked for the `change-me` prefix.

Other HTTP hardening:

- helmet-style security headers, with a CSP in production only;
- a CORS allow-list of `CLIENT_URL` and `ADMIN_URL`, plus any loopback origin
  outside production;
- a JSON body cap of `MAX_REQUEST_BODY_BYTES` (default 262,144), which returns
  `PAYLOAD_TOO_LARGE`.

---

## 6. RBAC

Code: `server-go/internal/config/permissions.go`. Roles are labels. The server
checks **permissions**. `SUPER_ADMIN` passes every check.
`HasPermission(role, granted, p)` is true when `p` is in the role's built-in
set **or** in the user's explicitly granted extras. Admin roles are SUPPORT,
FINANCE, RISK_MANAGER, ADMIN and SUPER_ADMIN. USER has no permissions.

| Role | Permissions |
|---|---|
| USER | none |
| SUPPORT | users:read, trades:read, wallets:read, deposits:read, withdrawals:read, assets:read, market-data:read, support:read, support:write |
| FINANCE | users:read, wallets:read, wallets:adjust, deposits:read, deposits:approve, withdrawals:read, withdrawals:approve, trades:read, reports:read, reconciliation:read |
| RISK_MANAGER | exposure:read, risk:read, risk:write, hedge:read, hedge:write, assets:read, payouts:read, trades:read, market-data:read, reports:read |
| ADMIN | SUPPORT ∪ RISK_MANAGER ∪ {users:write, users:suspend, trades:cancel, assets:write, payouts:write, market-data:write, reports:read, audit:read, settings:read, reconciliation:read} |
| SUPER_ADMIN | all 31 permissions |

What each permission gates (routes under `/admin` also need an admin role and
the Admin rate limit):

| Permission | Gates |
|---|---|
| users:read | `GET /admin/users`, `GET /admin/users/{id}` |
| users:write | `PATCH /admin/users/{id}` (status, overrides, emailVerified). Role and permission changes are additionally limited to SUPER_ADMIN. |
| trades:read / trades:cancel | `GET /admin/binary/trades[/{id}]` / `POST /admin/binary/trades/{id}/cancel` |
| assets:read / assets:write | `GET /admin/assets[/{id}]` / `POST`, `PATCH /admin/assets` |
| payouts:read / payouts:write | `GET /admin/assets/{id}/payouts` / `POST /admin/assets/{id}/payouts`, `DELETE /admin/assets/payouts/{id}` |
| risk:read | `GET /admin/settings/risk`, `GET /admin/risk`, `GET /risk/assessments`, `GET /risk/config`, ws `admin:risk` |
| risk:write | `PUT /admin/settings/risk` |
| exposure:read | `GET /admin/exposure`, `GET /risk/exposure[/{id}]`, ws `admin:risk` |
| hedge:read / hedge:write | `GET /admin/hedging`, `GET /admin/hedging/orders` / `POST /admin/hedging/orders/{id}/cancel` |
| wallets:read | `GET /admin/wallets`, `GET /admin/users/{id}/transactions` |
| wallets:adjust | `POST /admin/users/{id}/wallet/adjust` |
| deposits:read / withdrawals:read | `GET /admin/deposits` / `GET /admin/withdrawals`. Either one makes the caller a proof "reviewer" in `/transfers`. deposits:read also covers `GET /admin/payment-methods`. |
| deposits:approve / withdrawals:approve | the matching `/approve` route. `POST /admin/transfers/{id}/reject` needs **both**. |
| market-data:read | `GET /admin/market-data`, ws `admin:market` |
| reports:read | `GET /admin/reports/pnl`, `GET /admin/reports/counters` |
| audit:read | `GET /admin/audit-logs` |
| reconciliation:read | `GET /admin/reconciliation`, `POST /admin/reconciliation/run` |
| reconciliation:resolve | `POST /admin/reconciliation/{id}/resolve` |
| settings:write | `PUT /admin/payment-methods`, `POST /admin/payment-methods/{key}/active` |
| support:read / support:write | admin support desk routes, ws `admin:support` |
| **users:suspend, market-data:write, settings:read, admin:manage** | **declared but gate nothing** |

Any admin role, with no permission check, can reach `GET /admin/dashboard`,
`/dashboard/series`, `/dashboard/assets` and `/metrics/latency`. The dashboard
includes platform exposure and liability totals, so a SUPPORT user sees
figures that `/risk/exposure` would refuse them. Websocket `admin:ops` is also
open to any admin role.

---

## 7. Realtime (websocket)

Code: `server-go/internal/platform/ws/{hub,events}.go`,
`server-go/internal/config/constants.go`, `cmd/server/main.go`.

### 7.1 Connection

- `GET /ws?token=<access token>` (or `Authorization: Bearer`) is handled
  beside the router, and only when the request is an upgrade.
- The handshake checks, in order:
  1. total connections on this instance ≥ `WS_MAX_CONNECTIONS_TOTAL`
     (10,000): HTTP 503;
  2. missing token: 401;
  3. invalid or expired access token: 401;
  4. this user's sockets on this instance ≥ `WS_MAX_CONNECTIONS_PER_USER`
     (5): 429.
- `CheckOrigin` always passes, because the token is the authentication.
- Both limits count **per instance** (in memory). The `ws:connections` Redis
  key is declared but unused.
- The token is checked only at handshake. The socket outlives token expiry and
  account suspension.
- Read limit 64 KiB. The server pings every 30 s and closes a socket that has
  not ponged or sent a frame since the last ping. Write timeout 10 s. Send
  buffer 256 frames: a client that falls behind is disconnected.
- On connect, the socket is auto-subscribed to `user:<ownId>` and receives
  `connection.ready`
  `{socketId, userId, role, serverTime, channels}`.

### 7.2 Client protocol

Frames are JSON `{type, channels?}`. `channels` is an array of strings, each
≤128 chars, at most 100 per frame.

| type | Server reply |
|---|---|
| `ping` | `connection.pong {serverTime}` |
| `subscribe` | `subscription.updated {channels, accepted, rejected}` |
| `unsubscribe` | `subscription.updated {channels}`. The own `user:` channel cannot be removed. |
| other | `connection.error {code:"UNKNOWN_TYPE"}` |
| non-JSON | `connection.error {code:"BAD_FRAME"}` |

Channel authorisation (`canSubscribe`):

| Channel | Who may subscribe |
|---|---|
| `market:<assetId>` | anyone authenticated. The asset id is not validated. |
| `user:<id>` | only the socket's own user |
| `admin:risk` | risk:read or exposure:read |
| `admin:market` | market-data:read |
| `admin:support` | support:read |
| `admin:ops` | any admin role |

Server frames: `{event, payload, emittedAt, serverTime}`. Direct replies have
no `emittedAt`. Producers call `ws.Emit` (fire-and-forget) or publish a
pipelined envelope `{channels, event, payload, emittedAt}` to the Redis
channel `pubsub:broadcast`. Every instance consumes it and delivers to its own
sockets, so events cross instances.

### 7.3 Events

| Event | Channels | Payload | Producer |
|---|---|---|---|
| `market.tick` | `market:<id>`, `admin:market` | NormalisedTick: assetId, asset, symbol, price, bid?, ask?, timestamp, providerTimestamp, receivedAt, sequence?, processedAt, feedLatencyMs, processingLatencyMs, totalLatencyMs | marketdata `writeTicks` |
| `market.status` | `admin:market` | `{provider, state, at}` | provider status change |
| `trade.created` | `user:<id>`, `admin:ops` | BinaryTradeView | `acceptTrade` |
| `trade.updated` | `user:<id>`, `admin:ops` | BinaryTradeView | admin cancel |
| `trade.expiring` | `user:<id>` | `{tradeId, symbol, expiryTimestamp}` | expiry worker, when queued for settlement |
| `trade.settled` | `user:<id>`, `admin:ops` | BinaryTradeView | settlement, early close |
| `exposure.updated` | `admin:risk` | ExposureSnapshot (fields of 1.2 plus assetId, symbol, updatedAt) | `ApplyExposureDelta` |
| `risk.updated` | `admin:risk` | RiskAssessment[] (assetId, symbol, level, score, utilisationPercent, net, gross, potentialLiability, reasons, hedgeDecision, targetHedgeRatio, targetHedgeNotional, evaluatedAt) | `AssessAllAssets` (risk worker every tick, and `GET /risk/assessments`) |
| `hedge.created` / `hedge.updated` | `admin:risk` | HedgeOrderView (id, reference, assetId, symbol, provider, side, requested, filled, remaining notional, averageFillPrice, requestPrice, status, providerOrderId, riskLevel, hedgeLevel, targetHedgeNotional, realised/unrealised PnL, error, requestLatencyMs, timestamps) | hedging |
| `wallet.updated` | `user:<id>` | WalletView | wallets (mutations, transfers) |
| `notification.created` | `user:<id>` | `{id, type, title, body, data, read, createdAt}` | notifications |
| `usdt.updated` | `user:<id>` | `{kind, id, status}` | usdt (not wired, see section 8) |
| `support.message` | `user:<id>`, `admin:support` | message view | support |
| `support.conversation` | `user:<id>` or `admin:support` | conversation view (separate user and desk shapes) | support |
| `support.typing` | `admin:support` or `user:<id>` | `{conversationId, side, name?}` | support |
| `connection.ready` / `connection.pong` / `connection.error` / `subscription.updated` | the socket only | see 7.1 and 7.2 | hub |

---

## 8. Workers and leader election

Code: `server-go/internal/platform/leader/leader.go`,
`server-go/internal/platform/redisx/lock.go`, `internal/workers/workers.go`.

**Leader election.** `leader.Loop` tries `SET lock:worker:<name> <uuid> NX PX
ttl` on every tick, and the leader renews it with a compare-and-`PEXPIRE` Lua
script. The default TTL is `max(5 s, 5 × interval)`. A failed renew triggers
`OnRelease` and leadership is lost. If the leader dies, another instance takes
over within one TTL. Ticks never overlap. A panic is recovered and logged.
Stopping runs `OnRelease` and releases the lock with a compare-and-delete.
If Redis errors during a renew, the tick is skipped without releasing, so a
partitioned former leader can briefly overlap a new one.

**Boot sequence** (`cmd/server/main.go`):

1. Load config.
2. Connect to Mongo, run `models.Migrate`, ensure indexes.
3. Connect to Redis.
4. Load (or bootstrap) the risk config.
5. Resolve the market provider. This fails in production with a
   non-authoritative feed.
6. Start HTTP, then the ws hub.
7. `workers.Start`: rebuild exposure, rebuild the expiry queue from
   OPEN/SETTLING trades, release orphaned trade locks, then start the workers.

Shutdown (SIGINT/SIGTERM, 15 s hard limit): workers stop in reverse order and
concurrently, then the ws hub, HTTP, Redis and Mongo.

| Worker | Mode | Interval / lock TTL | Env |
|---|---|---|---|
| market-feed | leader | 5 s / 25 s | - |
| expiry | leader | `EXPIRY_TICK_MS` (100, min 10) / 15 s. A DB sweep runs about every 30 s. | `EXPIRY_TICK_MS` |
| settlement | **all instances** (BRPOP on `settlement:queue`, dedicated connection, then drains up to `SETTLEMENT_BATCH_SIZE`) | blocking, 1 s timeout | `SETTLEMENT_BATCH_SIZE` (200) |
| risk | leader | `RISK_INTERVAL_MS` (1000, min 100) / max(5 s, 5×) | `RISK_INTERVAL_MS` |
| hedge | leader | `HEDGE_INTERVAL_MS` (1000, min 100), full pass about every 15 s | `HEDGE_INTERVAL_MS` |
| reconciliation | leader | `RECONCILIATION_INTERVAL_MS` (300,000, min 10,000) / max(60 s, interval) | `RECONCILIATION_INTERVAL_MS` |

Settlement can run on every instance because it is idempotent: an atomic
`OPEN→SETTLING` claim, a unique `settlements.tradeId` and a keyed wallet
ledger. A trade skipped for lack of a price is re-pushed onto the queue.

`WORKERS_ENABLED=false` (default true) makes an instance API-only. It skips
the boot rebuilds and starts no workers. The websocket hub still runs, because
event delivery goes through Redis pub/sub.

`server-go/internal/service/usdt/worker.go` defines three more leader loops
(`usdt-webhook-retry` and `usdt-payout-submit` every 30 s,
`usdt-reconciliation` every 10 min). Nothing in `workers.Start` or `cmd/server`
calls them, and no `/usdt` HTTP routes are mounted.

`GET /ready` reports Mongo (including transaction support), Redis,
`marketFeed {provider, state, healthyAssets, totalAssets}`,
`workers {enabled, started, marketFeedLeader}` and ws stats. It returns 503
only when Mongo or Redis is down. The provider state comes from in-process
memory, so on an instance that is not the feed leader it reads `UNKNOWN`. The
`md:provider:status` hash is written but never read back.

---

## 9. Admin analytics and platform P&L

Code: `server-go/internal/service/admin/admin.go`. Every figure excludes DEMO.

- **Dashboard** (`GET /admin/dashboard`):
  - users: total and active today (distinct traders since UTC midnight);
  - trades: open, today's count, volume and Σ`platformResult`;
  - exposure: current gross, net and potential liability;
  - hedging: hedged exposure (Σ active filled hedge notional) and unhedged
    exposure (`max(0, |net| - hedged)`);
  - assets: active count;
  - latency: feed p50/p95/p99 and trade-acceptance p95, from the Redis samples
    `metrics:latency:*`, capped at 2000 per metric;
  - balances: INR LIVE wallet balance and locked totals;
  - transfers: pending deposits and withdrawals.
- **Series** (`/dashboard/series`, default 14 UTC days, every day emitted):
  trades, volume, platformResult and wins/losses/draws by trade `createdAt`,
  plus customerPayout from `settlements`. **Asset volume** (`/dashboard/assets`,
  default 7 days) sorts by volume.
- **P&L** (`GET /admin/reports/pnl`, needs reports:read). Each component comes
  from its own ledger, so they can be cross-checked:
  - `customerPayout`, `customerStakesCollected` and `platformGrossResult`
    come from `settlements` in the window;
  - `customerLiability` = Σ(stake + profit) of open trades now;
  - `hedgeRealisedPnl` and `hedgeUnrealisedPnl` come from `hedge_orders`
    created since `from`;
  - `fees` = Σ`FEE` wallet transactions;
  - `adjustments` = `ADJUSTMENT_DEBIT - ADJUSTMENT_CREDIT`.
  - `netResult = platformGrossResult + hedgeRealisedPnl + fees + adjustments`.
    Unrealised hedge P&L is reported but not included.

  `realisedPnl` is initialised to 0 and nothing in the code ever updates it, so
  `hedgeRealisedPnl` is currently always 0.

---

## 10. Where to look

| Topic | Files |
|---|---|
| Env, defaults, boot validation | `server-go/internal/config/env.go`, `.env.example` |
| Redis keys, ws events, channels, latency metrics | `server-go/internal/config/constants.go` |
| Roles and permissions | `server-go/internal/config/permissions.go` |
| Exposure counters, rebuild, sentiment | `server-go/internal/service/exposure/exposure.go`, `sentiment.go` |
| Risk scoring, pre-trade limits, risk worker | `server-go/internal/service/risk/risk.go`, `worker.go` |
| Risk config versioning, kill switch | `server-go/internal/service/riskconfig/riskconfig.go`, `server-go/internal/models/risk_config.go` |
| Risk settings admin API (bounds, audit) | `server-go/internal/api/admin/core.go` |
| Read-only risk API | `server-go/internal/api/risk/routes.go` |
| Hedging decision, worker, repository | `server-go/internal/service/hedging/{hedging,worker,repository}.go` |
| Hedge venues | `server-go/internal/service/hedging/providers/{provider,paper,rest}.go` |
| Trade acceptance chain | `server-go/internal/service/binary/binary.go` (`acceptTrade`) |
| Feed providers | `server-go/internal/service/marketdata/providers/*.go` |
| Tick pipeline, feed health, expiry price, volatility | `server-go/internal/service/marketdata/marketdata.go` |
| Feed worker | `server-go/internal/service/marketdata/worker.go` |
| Candles, 24h change | `server-go/internal/service/marketdata/candles.go`, `change24h.go` |
| Market data API | `server-go/internal/api/marketdata/routes.go` |
| Auth service, JWT | `server-go/internal/service/auth/auth.go`, `server-go/internal/platform/authtoken/token.go` |
| Auth API and validation | `server-go/internal/api/auth/routes.go` |
| Middleware: auth, RBAC, rate limit, CORS | `server-go/internal/platform/httpx/middleware.go` |
| Error codes | `server-go/internal/platform/apperr/errors.go` |
| Websocket hub and bus | `server-go/internal/platform/ws/hub.go`, `events.go` |
| Leader election, locks | `server-go/internal/platform/leader/leader.go`, `server-go/internal/platform/redisx/lock.go` |
| Worker lifecycle | `server-go/internal/workers/workers.go`, `server-go/cmd/server/main.go` |
| Expiry and settlement workers | `server-go/internal/service/expiry/`, `server-go/internal/service/settlement/` |
| Admin analytics and P&L | `server-go/internal/service/admin/admin.go` |
| Readiness | `server-go/internal/api/health/routes.go` |

---

## 11. Code vs documentation disagreements

1. **Feed thresholds are not live env settings.** The root README says the
   `FEED_*_MS` variables are "also editable at runtime". In code they only
   seed version 1 of the risk config. Once that row exists, changing the env
   has no effect, and the admin console is the only control.
   `feedStaleMaxMs` is never read by `ClassifyFeed`.
2. **Early close is not a single transaction.** The root README says exposure
   is released and the trade is dropped from the expiry queue "in the same
   transaction". The code runs sequential steps: claim, wallet, settlement
   row, trade update, then the Redis `ZREM` and exposure delta. Safety comes
   from idempotency keys, not from a transaction.
3. **"There is a test that proves" hedging cannot affect settlement.** That
   test (`hedging.test.ts`) was a Node suite and was not ported. The Go
   guarantee is structural (the import DAG), and `hedging_test.go` tests only
   sizing.
4. **Tick retention name.** The README calls it `TICK_HISTORY_RETENTION_MS`.
   It is the Go constant `config.TickHistoryRetentionMs` (15 min), not an env
   var.
5. **Chart timeframes.** The README says "5s to 1h". The server also accepts
   3h (10800) and 1d (86400).
6. **"Workers are leader-elected either way"** (`server-go/README.md`).
   Settlement is not leader-elected, and every worker-enabled instance runs
   the exposure and expiry-queue rebuild at boot without a lock.
7. **Hedge decisions are not fully replayable.** The package doc says every
   order records its decision inputs, but `placeHedgeOrder` always stores
   `volatility: 0` and `secondsToNearestExpiry: null`. `RiskInputs.ExistingHedgeNotional`
   is gathered but never used by `AssessRisk`.
8. **Hedge realised P&L.** P&L reports include `hedgeRealisedPnl`, but nothing
   ever writes `realisedPnl`. Cancelling a PARTIALLY_FILLED order stamps
   `closedAt`, which drops its filled part from the signed hedge position.
9. **Unused declarations.**
   - Permissions `users:suspend`, `market-data:write`, `settings:read` and
     `admin:manage` gate nothing. Suspension is controlled by `users:write`.
   - Redis keys `md:health:*`, `md:subscribed`, `ws:connections`, `cfg:risk`
     (risk config is cached in process instead) and `expiry:claim:*` are
     never used.
   - `MARKET_DATA_API_SECRET` is unused.
10. **Candle model comment.** `models/candle.go` says candles are what "the
    volatility estimate reads from", with intervals like "1 or 60". Volatility
    actually reads Redis tick history, and only 60 s candles are persisted.
11. **USDT is not wired.** The env, the `usdt.updated` event and the service
    exist, but `cmd/server` mounts no USDT routes and `workers.Start` does not
    start the USDT loops.
