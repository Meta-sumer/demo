# Load and failover evidence (F-05, F-06, F-14)

**Findings:** F-05 (atomic risk reservation under concurrent trades), F-06
(exposure-counter consistency and the boot rebuild race) and F-14 (worker
coordination and recovery across instances). The interim audit response
proposed a load test and a test with several real processes; this document
records both.

**Scope.** Two opt-in Go tests in `server-go/cmd/server`:

| Test | File | What it does |
|---|---|---|
| `TestLoadTradeStormAndBookRebuild` | `loadtest_test.go` | Thousands of concurrent LIVE trade requests through the real HTTP router, with client retries on the same idempotency key, a forced exposure-book rebuild and a safe-mode episode in the middle of the run |
| `TestFailoverMultiProcess` | `failover_test.go` | Three real server processes sharing one MongoDB database and one Redis database; the process holding the worker leaderships is killed while trades are open and expiring, then started again |

Shared helpers are in `loadtest_helpers_test.go`. All three files carry the
build tag `loadtest` and also skip unless `LOADTEST=1`, so `go test ./...` and
CI never run them.

**Code under test.** Branch `main` at `493abbd` plus the uncommitted work in
the tree on 22 Oct 2026 (the pricing and market-data rework in progress, and
the alerting, Redis retry and deployment changes described in
`production-readiness.md`). Trades were priced in `FIXED` mode (the payout
row as is): the dynamic engine is another workstream and is not what these
tests measure.

**Not a capacity claim.** The figures were measured on a developer PC
(Windows 11, 16 GB RAM with little free, test processes limited to
`GOMAXPROCS=2`), with MongoDB 7 (single-node replica set) and Redis 7 in
Docker Desktop and the load generator on the same machine. The production VPS
(Linux, 4 vCPU, MongoDB and Redis in Docker on the same host) has not been
measured; the figures show behaviour and orders of magnitude, not production
capacity.

---

## 1. How to run

Point the tests at **isolated** stores (never a database anyone uses):

```bash
cd server-go
export MONGO_URI="mongodb://127.0.0.1:27120/bt_test_ops?replicaSet=rs0"   # replica set (transactions)
export REDIS_URL="redis://localhost:6390"
export LOADTEST=1

# Load and retry storm (default profile: binding limits, 48 concurrent clients)
go test -tags loadtest -count=1 -timeout 14m -run TestLoadTradeStormAndBookRebuild -v ./cmd/server

# Capacity profile: limits that do not bind
LOADTEST_PROFILE=capacity LOADTEST_GROSS_STAKES=100000 LOADTEST_NET_STAKES=100000 LOADTEST_USER_STAKES=100 \
  go test -tags loadtest -count=1 -timeout 14m -run TestLoadTradeStormAndBookRebuild -v ./cmd/server

# Target profile: as capacity, 8 concurrent clients
LOADTEST_PROFILE=target LOADTEST_CONCURRENCY=8 LOADTEST_GROSS_STAKES=100000 LOADTEST_NET_STAKES=100000 LOADTEST_USER_STAKES=100 \
  go test -tags loadtest -count=1 -timeout 14m -run TestLoadTradeStormAndBookRebuild -v ./cmd/server

# Three processes on ports 4101-4103 (builds the server binary itself)
go test -tags loadtest -count=1 -timeout 18m -run TestFailoverMultiProcess -v ./cmd/server
```

| Variable | Default | Meaning |
|---|---|---|
| `LOADTEST_USERS` | 120 | Customers |
| `LOADTEST_TRADES_PER_USER` | 12 | Logical trades per customer |
| `LOADTEST_CONCURRENCY` | 48 | Concurrent clients |
| `LOADTEST_GROSS_STAKES`, `LOADTEST_NET_STAKES`, `LOADTEST_USER_STAKES` | 150, 40, 8 | Asset gross, asset net and per-customer exposure ceilings, in stakes of 100.00 |
| `LOADTEST_OUT` | - | File that receives one JSON line per result |
| `LOADTEST_REDIS_DB` | 9 | Redis database index for the failover test (must be empty) |
| `LOADTEST_LOGDIR` | temporary | Keeps the failover test's binary and the three server logs |

**Isolation and clean-up.**

- The load test uses a throw-away database (`internal/testsupport`), with
  transactions on as in production. Its Redis queues are renamed into a
  namespace of its own; every other key it writes carries an id it generated.
  At the end it waits for fire-and-forget writes, deletes its keys, drops the
  database and checks the database is gone.
- The failover test uses its own database (`binary_trading_failover_<id>`)
  and its own Redis database index, because the server's queue and leader
  keys are not namespaced. It refuses to start if that index is not empty,
  and flushes it and drops the database at the end. It kills every process it
  started.
- During this session two early runs of the load test left their throw-away
  databases and keys behind. They were found afterwards and removed by hand;
  the explicit clean-up and drop check above were added after that, and the
  later runs left nothing.

---

## 2. Load and retry storm (`TestLoadTradeStormAndBookRebuild`)

### 2.1 What the test does

1. Configures a `BTC/USD` asset with one expiry (300 s), a payout row of 85 %,
   and the exposure ceilings under test; `FIXED` pricing; relaxed feed
   thresholds for a 50 ms feed.
2. Creates 120 customers with a funded LIVE wallet (one in ten can afford only
   two trades) and signs their access tokens directly (the sign-in rate limit
   is per IP).
3. Feeds a random walk through the production ingest path
   (`marketdata.IngestTick`) every 50 ms.
4. Builds the real HTTP router (`newRouter`, the same function `main` uses)
   behind a loopback HTTP server, so every request passes the middleware,
   authentication, the per-user rate limit (60 trades a minute), validation
   and `POST /binary/trades`.
5. Sends 1,440 logical trades (12 per customer, 65 % UP), each with its own
   idempotency key:
   - every logical trade is sent 1 to 3 times **at once** with the same key
     (a client retrying a slow request);
   - one key in 25 also gets a concurrent request with the **same key and a
     different amount** (a client bug);
   - one key in 5 is sent again **after the storm**.
6. Mid-run book events:
   - at 35 % of the trades, a **forced rebuild** of the exposure book
     (`exposure.RebuildBook`, `Force`);
   - at 65 %, the book is **marked DIRTY** (safe mode), and 300 ms later
     rebuilt.

### 2.2 What it asserts

| Check | How |
|---|---|
| No limit is ever breached | From MongoDB (the record), over the open LIVE trades: asset gross ≤ limit, \|asset net\| ≤ limit, every customer ≤ the per-user limit |
| The Redis book equals MongoDB | `exposure.GetExposure` (UP, DOWN, gross, net, open trades) and `GetUserExposure` for every customer equal the MongoDB figures; no reservation left; book `READY`; `exposure.Reconcile` finds nothing to repair |
| One trade per idempotency key | MongoDB has at most one trade per (customer, key); every 200/201 answer for a key names that trade; at most one 201 per key, whichever payload got there first |
| Wallet locks reconcile | For every customer, `lockedBalance` equals the stakes of their open trades and `balance` equals the funding |
| Queue | Every open trade is on the expiry queue |
| Ledgers | `reconciliation.RunReconciliation` returns `OK` (wallet ledger vs balances, locked funds vs open positions, and the other checks) |
| No unexpected failure | No transport error; no 5xx other than `503` with `EXPOSURE_BOOK_UNAVAILABLE` / `SERVICE_UNAVAILABLE` (LIVE fails closed while the book is not exact) |

### 2.3 Results (22 Oct 2026) - all runs **PASS**

Each run: 120 customers, 1,440 logical trades, 3,239 HTTP requests.

| | Binding limits | Capacity | Target (run 1) | Target (run 2) |
|---|---|---|---|---|
| Concurrent clients | 48 | 48 | 8 | 8 |
| Limits (stakes of 100.00) | gross 150, net ±40, user 8 | not binding | not binding | not binding |
| Trades accepted | 146 | 1,319 | 1,320 | 1,320 |
| Wall time (all requests) | 20.5 s | 24.0 s | 30.5 s | 15.5 s |
| Requests per second | 158 | 135 | 106 | 209 |
| Accepted trades per second (storm) | 7.9 (limit reached early) | 59.5 | 45.3 | 91.1 |
| Latency, all requests p50 / p95 / p99 | 368 / 828 / 991 ms | 296 / 896 / 1,414 ms | 70 / 212 / 275 ms | 32 / 103 / 228 ms |
| Latency, accepted trades p50 / p95 / p99 | 839 / 1,048 / 1,259 ms | 696 / 993 / 1,768 ms | 157 / 246 / 290 ms | 68 / 134 / 334 ms |
| Forced rebuild (duration) | 694 ms | 503 ms | 168 ms | 47 ms |
| DIRTY, then rebuild (duration) | 1.27 s | 714 ms | 525 ms | 479 ms |
| Final book | gross 150/150, net +32/±40 | gross 1,345, net +381 | gross 1,341, net +373 | gross 1,344, net +380 |
| Reconciliation | OK | OK | OK | OK |

Answers, binding run: 146 × 201 Created, 34 × 200 (replayed: same key, same
trade), 1,575 × 400 `EXPOSURE_LIMIT_EXCEEDED`, 1,399 × 409
`IDEMPOTENCY_IN_PROGRESS` (a concurrent copy while the first was being
processed), 85 × 422 `IDEMPOTENCY_PAYLOAD_MISMATCH`. Capacity run: 1,319 ×
201, 293 × 200, 146 × 400 `INSUFFICIENT_BALANCE` (the customers funded for two
trades), 1,391 × 409, 89 × 422, and **1 × 503 `SERVICE_UNAVAILABLE`** (a LIVE
request during the safe-mode episode that could not wait for the rebuild:
refused, as designed).

**What the runs show.**

- **Limits hold exactly under contention.** In the binding run the asset's
  gross exposure filled to exactly its ceiling (150 of 150 stakes) and never
  above, while 48 clients raced for the last headroom; the net and per-user
  ceilings also held. All three ceilings answer `EXPOSURE_LIMIT_EXCEEDED`.
- **Retry storms place one trade per key.** About 1,400 concurrent duplicates
  per run were answered `409 IDEMPOTENCY_IN_PROGRESS`; late retries were
  answered `200` with the original trade; a different payload under a used key
  was refused (`422`), or, when it arrived first, was the one trade for that
  key. No key ever produced two trades.
- **A rebuild in the middle of trading is exact.** After the forced rebuild
  and the DIRTY episode, the Redis book equalled MongoDB to the unit, no
  reservation was left, and `Reconcile` found nothing to repair. LIVE trading
  failed closed for at most one request per run.
- **Latency depends on concurrency on this machine.** With 8 concurrent
  clients, trades were accepted at 45-91 per second with an accepted-trade
  p95 of 134-246 ms, inside the proposed target (`production-readiness.md`
  §2.2: 20 trades/s, p95 under 300 ms). With 48 concurrent clients the p95 was
  about 1 s: requests queue for the two CPUs given to the test process, for
  Docker Desktop's MongoDB and Redis, and for the per-wallet lock. The
  variation between the two target runs is the load on the shared machine.

---

## 3. Several real processes and a leader crash (`TestFailoverMultiProcess`)

### 3.1 What the test does

1. Creates its own database and uses its own Redis database index; configures
   `BTC/USD` with a **30 s** expiry, `FIXED` pricing and limits that do not
   bind; 20 customers with funded LIVE wallets.
2. Builds the server binary and starts **process A on port 4101**, waits until
   it is ready and leads the market feed (it then holds every worker
   leadership: market feed, expiry, risk, hedge, reconciliation, exposure
   reconcile, alert mailer and monitor), then starts **B on 4102 and C on
   4103**. All three run with `WORKERS_ENABLED=true`, the simulated feed
   (ticks every 50 ms from whichever process leads the feed), transactions on.
3. Each customer places a trade every 1-2 s on a random live instance; one
   trade in four is **retried on another instance with the same idempotency
   key**.
4. At 15 s, A is **killed** (`TerminateProcess`, the Windows equivalent of
   `SIGKILL`: no graceful shutdown, no lock released). Trading continues on B
   and C.
5. At 50 s, A is **started again** (its boot tasks run: book check, add-only
   expiry-queue rebuild; since 23 Oct 2026 the orphaned-lock release is the
   leader loop `stake-lock-orphans`, not a boot task). Trading continues on
   all three for 15 s, then stops.
6. The test waits until no trade is `OPEN` or `SETTLING`.

### 3.2 What it asserts

| Check | How |
|---|---|
| Every accepted trade exists and finished | Every 201 answer has its trade; none `OPEN` or `SETTLING` at the end. MongoDB may hold more trades than 201 answers, but no more than the requests that lost their connection: the kill can land between a trade's insert and its response (bound changed on 23 Oct 2026, see §3.4) |
| Nothing settled twice, nothing missing | Exactly one `settlements` row per trade; exactly one `trade:settle:<id>` and one `trade:lock:<id>` ledger movement per trade |
| Cross-instance idempotency | A retry on another instance is answered `200` with the original trade (a retry sent to A at the instant it was killed gets a transport error, which is counted, not failed) |
| Wallets | No customer has funds locked; `reconciliation.RunReconciliation` returns `OK` (ledger vs balances, locked vs open positions, settled trades vs settlement rows vs ledger) |
| Exposure book | It must never show **less** exposure than MongoDB (that could let a trade through a limit), and the running cluster must bring it back to exactly MongoDB by itself (polled for up to 90 s); afterwards `Reconcile` finds nothing to repair and the book is `READY` with no reservation |
| Leader takeover | Time from the kill until B or C reports market-feed leadership on `/ready`; the longest gap between two accepted ticks |

### 3.3 Results (22 Oct 2026) - final run **PASS**

| | Run 1 | Run 2 (final) |
|---|---|---|
| Trades accepted | 520 | 502 |
| Outcomes | 209 WON, 199 LOST, 112 DRAW (voided) | 206 WON, 184 LOST, 112 DRAW (voided) |
| Settlement rows / settle movements / lock movements per trade | exactly 1 / 1 / 1 | exactly 1 / 1 / 1 |
| Cross-instance retries replayed the original trade | 152 of 152 that reached a live instance | 152 (1 sent to A as it died) |
| Requests refused `FEED_UNHEALTHY` (no fresh tick) | 304 | 302 |
| Market feed taken over after the kill | 25.1 s (by B) | 24.7 s (by B) |
| Longest gap between accepted ticks | 25.0 s | 24.8 s |
| Settlement latency after expiry, all trades: p50 / p95 / max | 0.2 s / 60.2 s / 60.7 s | 0.7 s / 60.6 s / 61.5 s |
| Trades that expired in the leaderless gap | 182, settled p50 60.1 s after expiry (voided) | 179, p50 60.1 s (voided) |
| Locked funds left | 0 | 0 |
| Reconciliation | OK | OK |
| Exposure book when the last trade finished | 36 stale open records: gross over-stated by 36 stakes | 60 stale open records: gross over-stated by 60 stakes, never under-stated |
| Book back to exactly MongoDB | not measured (see below) | **31.6 s**, by the cluster's own exposure-reconcile loop |

Run 1 **failed** on two assertions, both kept as findings:

- It required the book to equal MongoDB the moment the last trade finished;
  it held 36 stale records. That led to §4.2. A diagnostic run in between
  (524 trades, 48 stale records) showed one `Reconcile` 12 s later closing all
  48; the assertion was then changed to the safety property in §3.2 (never
  under-stated, healed by the cluster), which the final run passes.
- A retry sent to A at the instant it was killed was counted as a wrong
  answer; it is a transport error and is now counted as one.

### 3.4 Results after the fixes (23 Oct 2026)

Re-run after the fixes for §4.1 (a short, fenced market-feed lease) and §4.2
(exposure released by trade id), with the same test and the same machine.
Command as in §1, with `REDIS_URL=redis://127.0.0.1:6390` (resolving
`localhost` timed out on this PC) and `-vet=off`.

| | Run A | Run B | Run C (final) |
|---|---|---|---|
| Result | **PASS** | FAIL (2 assertions, below) | **PASS** |
| Trades accepted (201) | 789 | 800 | 760 |
| Trades in MongoDB | 789 | 801 (one written by A as it was killed, no 201) | 761 (the same) |
| Outcomes | 386 WON, 403 LOST, **0 voided** | 401 WON, 400 LOST, **0 voided** | 382 WON, 379 LOST, **0 voided** |
| Market feed taken over after the kill | **2.6 s** (by C) | **2.7 s** | **2.9 s** (by B) |
| Longest gap between accepted ticks | **2.63 s** | **2.59 s** | **2.88 s** |
| Requests refused `FEED_UNHEALTHY` | 7 | 4 | 8 |
| Settlement latency after expiry: p50 / p95 / max | 0.16 / 0.32 / 0.79 s | 0.16 / 0.35 / 6.1 s | 0.31 / 2.9 / 9.6 s |
| Settlement rows / settle / lock movements per trade | exactly 1 / 1 / 1 | exactly 1 / 1 / 1 | exactly 1 / 1 / 1 |
| Exposure book when the last trade finished | **exact** (0 stale records) | **exact** | **exact** |
| Reconciliation | OK | ALERT (one orphaned stake lock) | OK |

Before the fixes (§3.3): takeover ~25 s, gap ~25 s, 112 of ~510 trades
voided, ~300 requests refused, settlement p95 ~60 s, and 36-60 stale records
in the book at drain that the reconcile loop removed ~30 s later.

What the figures show:

- **Feed gap: ~25 s -> 2.6-2.9 s.** The market-feed lease is renewed every
  500 ms and lapses 2.5 s after the last renewal; a follower retries every
  500 ms and connects and subscribes in the same step
  (`service/marketdata/worker.go`). Ticks are ingested only under a valid
  local lease, and a new leader continues from the normaliser and volatility
  state in Redis, so the tick history stays single-writer and in order.
- **Voids: 112 -> 0.** In this test the first trades expire about 31 s after
  the start and the kill comes at 15 s, so a 3 s gap now ends long before any
  expiry; the old 25 s gap reached into the expiries (31-40 s). The 0 does
  not mean a trade can never be voided by a failover: one expiring inside a
  gap more than 2 s after the last tick still is (about 0.6-0.9 s of expiries
  per failover at these gaps, roughly 8-12 trades at this test's ~13 trades a
  second, against ~300 for a 25 s gap).
- **Exposure book exact at drain** in every run (0 stale records, nothing for
  the reconcile loop to heal): settlement and Sell now release by trade id
  (`exposure.ReleaseTrade`), which removes the ~30 s over-statement of §4.2.
- **Run B** failed two assertions, both caused by the SIGKILL landing inside
  a trade acceptance, not by a wrong result:
  - a trade was inserted by A just before it was killed and its 201 never
    reached the client. The assertion "trades = 201 answers" was too strict
    for a kill; it now requires every 201 to have its trade and allows extra
    trades only up to the requests that lost their connection
    (`failover_test.go`, the one threshold changed);
  - a stake was locked by A just before the kill and its trade never written
    (an orphan). The orphan sweep then ran every 5 minutes on a 10-minute
    lease still held by the dead process, so nothing released it before the
    test ended (the old boot-time sweep would not have either: the lock was
    under the 60 s grace when A restarted). The sweep now runs every 15 s
    over the last 30 minutes of locks on a 45 s lease (every lock on taking
    the lead and every 30 minutes; `service/wallets/orphan_worker.go`), so
    such an orphan is released about a minute after the crash.
  Run C exercised the first case again (761 trades, 760 answers) and passed.
- **Settlement latency** in run C (p95 2.9 s, max 9.6 s) reflects a busy
  machine: Docker Desktop had just restarted. No trade waited for the 60 s
  defer window, as all did in the leaderless gap before.
- Two further attempts could not start: the test's own server could not bind
  127.0.0.1:4101 at start (nothing was listening there afterwards), and the
  Go toolchain failed to launch `vet.exe` while Docker Desktop was
  restarting. They produced no result and left nothing behind.

Every run's processes were stopped, its database dropped and its Redis
database flushed (checked after the final run: no listener on 4101-4103, no
`server.exe`, Redis database 9 empty, no `binary_trading_failover_*`
database).

---

## 4. Findings for the code owners

These were found by the tests above and reproduced with
`TestFailoverMultiProcess`. §4.1 and §4.2 were **fixed on 23 Oct 2026**; the
re-run is in §3.4.

### 4.1 A leader crash voids every trade expiring in the next ~25 s - fixed

**Fixed 23 Oct 2026** (`service/marketdata/worker.go`): the market-feed lease
is renewed every 500 ms and lapses after 2.5 s, a follower takes over within
about 3 s and resubscribes at once, ticks are ingested only under a valid
lease, and a new leader continues from the state in Redis. Measured gap
2.6-2.9 s, 0 voided, 4-8 requests refused (§3.4). A trade expiring inside
the remaining gap more than 2 s after the last tick is still voided by the
settlement policy. The original finding:


When the process that leads the market feed dies, no instance publishes ticks
until its lock lapses: the market-feed lock TTL is 25 s (`max(5 s, 5 ×
interval)` for a 5 s interval, `platform/leader`, `service/marketdata/worker.go`).
Measured: 24.7-25.1 s without a tick. Consequences:

- every trade that expires in that window has no tick close enough to its
  expiry (the settlement price rule allows 2 s on this build), so after the
  60 s defer window it is **voided and the stake refunded** (112 of ~510
  trades in each run, all of them "DRAW / voided"); this is the documented
  policy (`customer/disclosures.md`), but a single process crash triggers it
  for a quarter of a minute of business;
- new trades are refused `FEED_UNHEALTHY` meanwhile (about 300 requests per
  run).

Options: a shorter market-feed lock TTL and interval, a standby feed
connection on the other instances that starts publishing as soon as the lock
is lost, or both. On today's single-instance production, a crash of the one
process has the same effect until Docker restarts it.

### 4.2 The exposure book can over-state open exposure after a failover - fixed

**Fixed 23 Oct 2026**: settlement, Sell now and admin cancel release a trade's
exposure by its trade id (`service/settlement/execute.go` `afterFinal`,
`service/binary/cancel.go` -> `exposure.ReleaseTrade`), exactly once; a trade
still counted under a legacy anonymous record is released through it. The
book was exact when the last trade finished in every re-run (§3.4); tests
`TestReleaseIsByTradeID`, `TestReleaseOfALegacyTrade`. The original finding:


Settlement and admin cancel release a trade's exposure **by matching fields**
(asset, user, direction, stake, profit) instead of the trade id
(`service/settlement/execute.go` `afterFinal` → `exposure.ApplyExposureDelta`
without `TradeID` → `closeByMatch`). With several identical open trades a
release can close a sibling's record; `multi-instance.md` §7 documents this
and relies on the reconciler. Across a crash and restart, the boot reconcile
of the restarted instance re-opened records for still-open trades whose
records had been closed that way, while the matching stale records of
finished trades stayed open. Measured: 36-60 open records (36-60 stakes of
gross exposure) left in the book after every trade had finished; the
cluster's exposure-reconcile loop removed them about 30 s later.

The error is **conservative**: the book shows more exposure than really
exists, so limits are never breached, but trades that would fit can be
refused, and the Exposure screen over-states the book for that time.
Suggested fix: pass the trade id on release (`ExposureDelta.TradeID` in
`afterFinal` and `CancelTrade`), so `CloseTrade` closes the exact record and
the match path is no longer used.

### 4.3 Other observations

- Settlement of trades that expired in the gap took about 60 s (the defer
  window), well under the "settlement stuck" alert threshold (10 min,
  `production-readiness.md` §10.2).
- A feed gap after a failover (now about 3 s) is far shorter than the
  monitor's feed threshold (`ALERT_FEED_STALE_SECONDS`, default 60 s), so the
  monitor does not email it; the market-data code records gaps longer than
  its gap threshold itself (`market_data_gaps`, `feed.gap` alerts).
- A SIGKILL can land inside a trade acceptance: between the stake lock and
  the insert (an orphaned stake lock, released by the `stake-lock-orphans`
  loop about a minute later and flagged by the reconciliation check
  `stake_locks_have_open_trades` until then), or between the insert and the
  response (the trade exists although the client saw a transport error; a
  retry with the same idempotency key returns it). Both happened in the
  re-runs (§3.4).
- Cross-instance idempotency held in every case: a retry on another instance
  always returned the original trade.
- Nothing was settled twice and no money moved twice in any run.

---

## 5. Not covered yet

| Item | Why it matters |
|---|---|
| A run on production-shaped infrastructure (Linux, the VPS's MongoDB and Redis containers) | The figures above are from a developer PC |
| Websocket load (1,000 connections, delivery latency) | Not part of these tests |
| A real Redis restart or data loss during trading | The load test simulates safe mode (book marked DIRTY) and a forced rebuild; it does not stop Redis |
| An expiry burst (500 trades expiring in the same second) | The settlement consumer is sequential per instance; the failover test settled up to ~20 trades a second |
| Dynamic pricing under load | These tests used `FIXED` pricing |
