# How the platform works - logic reference

These documents describe the rules the code actually implements. They were
written by reading the code, not the brief: every statement cites the file it
comes from, and where the code and the root `README.md` disagree, the
documents say so.

| # | Document | What it covers |
|---|---|---|
| 1 | [Trading core](01-trading-core.md) | Trade lifecycle and statuses, the acceptance checks in the order they run (with error codes), how entry price and expiry are fixed, expiry detection and settlement, win/loss/draw and the draw policy, the single-settlement guarantee, early close pricing, assets and payout tables, DEMO vs LIVE, market sentiment, websocket events |
| 2 | [Money, wallets and payments](02-money-wallets-payments.md) | Minor units and micro-USDT, LIVE/DEMO/FUNDING wallets, the lock model, the ledger and its idempotency keys, manual INR deposits and withdrawals, the USDT/BeeX logic, reconciliation, audit, which permission gates which money action |
| 3 | [Risk, market data, auth, realtime](03-risk-marketdata-auth.md) | Exposure (net/gross), risk scoring and pre-trade limits, hedging, market data providers, feed health and candles, JWT sessions and refresh rotation, rate limits, RBAC, websocket protocol and events, workers and leader election, admin P&L |
| 4 | [Client and mobile](04-client-and-mobile.md) | HTTP and token handling, the realtime bridge, the stores, trade ticket rules, live result projection, chart logic and indicator formulas, formatting, the theme system, how the React Native app differs from the web client |

Read 1 → 2 → 3 for the server, 4 for the apps.

## The rules everything else follows

- **The server decides price, time and payout.** A trade request carries
  asset, direction, amount, expiry and account (plus an optional quoted price
  used only as a slippage guard). Nothing a client sends can change a result.
- **Money is an integer.** INR in paise, USDT in micro-USDT. No floating-point
  arithmetic on balances.
- **A trade settles once, a credit applies once.** Atomic status claims,
  unique indexes and ledger idempotency keys back each other up.
- **MongoDB is the record; Redis is a cache.** Exposure counters and the
  expiry queue are rebuilt from MongoDB at boot.
- **A stale feed stops new trades**, but open trades still settle on their
  published terms.
- **DEMO money never touches the house book** - no exposure, hedging, risk
  limits, P&L or platform reconciliation.

## Found while writing these (not fixed)

Each item is explained, with file references, in the document named.

**Things that do not work today**

- **USDT is not connected** (doc 2, §5). The `usdt` and `beex` services are
  complete, but no route, webhook or worker uses them. The clients call
  `/wallet/usdt/*`, which the deployed API answers with `NOT_FOUND` (checked
  against the live server on 2026-10-14).
- **Hedge realised P&L is always 0** (doc 3): nothing writes `realisedPnl`.

**Crash and concurrency edge cases**

- An early close that crashes between steps can leave the trade marked `DRAW`,
  settle a re-claimed trade before its expiry, or leave the ledger and the
  settlement row disagreeing (doc 1, §10).
- Early close prices off the last tick in Redis without the feed-health check
  (doc 1, §10).
- If no tick at or before expiry survives (ticks are kept 15 minutes), the
  expiry price falls back to a later tick (doc 1, §10).
- A manual deposit or withdrawal whose wallet step fails after APPROVED stays
  APPROVED with no way to retry or reject it; a withdrawal is saved and
  reserved in two separate steps; a retried admin adjustment is applied twice
  (doc 2).
- Every instance with workers enabled rebuilds exposure counters and the expiry
  queue at boot without a lock (doc 3, §8).
- Changing a payout closes the old row before inserting the new one; a failed
  insert leaves no active payout, and `minStake <= maxStake` is not checked
  (doc 1, §10).

**Security**

- The rate limiter fails open when Redis is down and trusts the first
  `X-Forwarded-For` value (doc 3, §5).
- Suspending a user revokes no tokens; access tokens and open sockets keep
  working until they expire (doc 3, §5).
- Any admin role can see platform exposure on the dashboard (doc 3, §9).
- The seed creates `risk@` and `finance@` accounts with passwords published in
  the root README; change them on any public deployment.

**Where the root README is out of date**

- Early close is not "one transaction": the Redis steps run after the MongoDB
  writes (docs 1 and 3).
- The tests it cites for concurrency and "hedged and unhedged settle the same"
  were Node suites that were never ported to Go (docs 1, 2, 3).
- `FEED_*_MS` only seed the first risk configuration; afterwards the admin
  console is the only control, and `feedStaleMaxMs` is never read (doc 3).
- Tick retention is a fixed 15-minute constant, not an env var; the server also
  accepts 3h and 1d chart frames (doc 3).
- Settlement runs on every instance, not only the leader (doc 3).
- It still says there is no payment provider and documents none of the
  `USDT_*` / `BEEX_*` variables (doc 2).
- "`utils/format.ts` is the only place that converts money" is not true: USDT
  has its own converters and a few screens divide by 100 by hand (doc 2).
