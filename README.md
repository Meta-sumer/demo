# Binary Trading Platform

A binary options trading platform in three applications: a customer trading
client, an operations console, and the server that owns every decision.

```
binary-trading/
├── client/   React + TypeScript trading application   (port 5173)
├── admin/    React + TypeScript operations console    (port 5174)
└── server-go/ Go backend + workers                     (port 4000)
```

---

## The rules this platform is built around

Everything else is implementation detail. These are the properties that make it
trustworthy with money:

| Rule | Where it is enforced |
|---|---|
| The server decides the price, the times, and the payout | `internal/service/binary` reads the entry price from Redis; the request carries only asset, direction, amount and expiry |
| A client can never influence a result | Settlement compares the frozen entry price against the authoritative expiry price; nothing in the request is consulted |
| A trade settles exactly once | Atomic `OPEN → SETTLING` claim, a unique index on `settlements.tradeId`, and a wallet ledger keyed `trade:settle:<id>` |
| A wallet credit can never be applied twice | Partial unique index on `wallet_transactions.idempotencyKey` |
| Redis is never the accounting record | MongoDB owns every financial fact; exposure counters and the expiry queue are rebuilt from it at boot |
| A position cannot be closed early and settled at expiry | Early close takes the same `OPEN → SETTLING` claim and the same `trade:settle:<id>` ledger key as expiry settlement |
| A stale feed stops new trades | `assertFeedTradable` refuses the trade; open trades still settle under their published terms |
| Hedging cannot touch a customer | The settlement path never reads a hedge order — there is a test that proves the outcome is identical hedged and unhedged |
| Every financial change is auditable | Append-only `audit_logs`, with mutation blocked at the schema level |

---

## Money is stored in integer minor units

Every monetary value in the database, the API and the websocket payloads is an
**integer number of minor units** (paise/cents).

```
₹100.00  →  10000
$1.50    →    150
```

Floating point cannot represent 0.1 exactly, so a ledger that adds and
subtracts fractional currency in `number` fields drifts. Integers make every
wallet operation exact and reconcilable. `internal/platform/util` (money helpers) on the server and
`utils/format.ts` on both frontends are the only places that convert.

---

## Quick start

```bash
# 1. Infrastructure (MongoDB as a single-node replica set, plus Redis)
npm run infra:up

# 2. Install and configure
npm install
cp .env.example .env        # already present; edit MONGO_PORT if 27017 is taken

# 3. Seed the platform: admin accounts, assets, payout tables
npm run seed

# 4. Run everything
npm run dev
```

| Application | URL | Sign in |
|---|---|---|
| Trading client | http://localhost:5173 | `trader@binary.local` / `Trader@12345` |
| Admin console | http://localhost:5174/admin/login | `admin@binary.local` / `Admin@12345` |
| API | http://localhost:4000 | `GET /health`, `GET /ready` |

The seed also creates `risk@binary.local` / `Risk@12345` (RISK_MANAGER) and
`finance@binary.local` / `Finance@12345` (FINANCE) so the permission model can
be seen working.

**MongoDB needs a replica set** for multi-document transactions. `docker-compose.yml`
configures one and initialises it through the healthcheck. Against a standalone
`mongod`, set `ENABLE_MONGO_TRANSACTIONS=false`; settlement then relies on its
guarded atomic updates and unique-index idempotency, which is safe but does not
commit the balance and its ledger row in one atomic step.

---

## The trading workspace

The client is a single full-height workspace rather than a set of pages: an
icon rail on the left, instrument tabs across the top of the chart, the ticket
and the open positions on the right. A trader watching a 30-second expiry
should never have to navigate away from the chart to do anything.

### Instruments and tabs

Up to eight instruments stay open as tabs, persisted across reloads. Each tab
carries the symbol, the current payout percentage and the live price, and a dot
that goes amber when that instrument's feed is no longer tradable — the same
`assertFeedTradable` decision the server enforces, surfaced before the trader
places an order that would be refused.

### Early close ("Sell now")

An open position can be closed before expiry for a quoted value.

The quote is a **server** decision, computed in `server-go/internal/service/binary/earlyclose.go`
from the frozen trade terms and the current authoritative price. It values the
position as a digital option under a driftless random walk:

```
value = stake × (1 + payout%) × Φ(z) × (1 − fee)

z = signed move / (price × σ_per_second × √seconds_remaining)
```

`Φ` is the standard normal CDF, so a position deep in the money late in its
life approaches the full payout, and one deep out of the money approaches zero.
The win probability is clamped to [0.01, 0.99]: neither end is ever certain
while the market is still open, and a quote of exactly zero or exactly the full
payout would misrepresent that.

What makes it safe to expose:

- The quote endpoint **never mutates the trade**. Quoting is idempotent and can
  be polled; the ticket polls it every two seconds.
- Closing reuses the settlement machinery — the same `OPEN → SETTLING` claim,
  the same `trade:settle:<id>` ledger key as expiry settlement. A position can
  therefore never be both closed early and settled at expiry, and a double
  close is refused with `ALREADY_SETTLED`.
- The settlement row records `result: 'EARLY_CLOSE'` and the trade lands in
  `CLOSED_EARLY`, so reconciliation and the audit trail account for it
  distinctly from an expiry outcome rather than silently as a win or loss.
- Exposure is released with `sign: -1` and the trade is removed from the expiry
  queue in the same transaction.

The fee, the cutoff before expiry, and whether the feature is available at all
are risk-configuration fields (`earlyCloseFeePercent`,
`minSecondsRemainingForEarlyClose`, `earlyCloseEnabled`), versioned and audited
like every other risk parameter, and editable in the admin console.

### Market sentiment

The chart shows the UP/DOWN split of open interest on the instrument as
percentages only. `deriveSentiment` returns `null` rather than a fabricated
50/50 when there are no open trades, and the bar renders `--` — a made-up even
split would read as real information about the book.

**Only ratios are exposed, never amounts.** Publishing the actual staked
totals would tell a customer the size of the platform's position.

### Chart

History comes from the server's OHLC endpoint; the live tick stream folds into
the bucket in progress.

- **Timeframes** — 5s to 1h. Only 60-second candles are persisted, so coarser
  frames are folded from those and sub-minute frames are rebuilt from the
  retained tick history. That means the sub-minute frames reach back only
  `TICK_HISTORY_RETENTION_MS` (15 minutes), which the timeframe menu says
  plainly rather than showing a short chart with no explanation.
- **Chart types** — candles, bars, line, area. All four series are built once
  and toggled, so switching type keeps the pan and zoom.
- **Indicators** — SMA 20, EMA 50, Bollinger (20, 2σ), computed in
  `utils/indicators.ts` from the candles already on screen. They are display
  aids: nothing about them reaches the server or takes part in an order. Each
  returns points only where the window is fully populated, because padding the
  head with a partial average draws a line that looks like data and is not.
- **Price levels** — the draw tool places horizontal levels, stored per
  instrument in the browser. Local annotations, no server meaning.
- **Trade boundaries** — vertical "Beginning of trade" and "End of trade"
  markers for each open position. lightweight-charts has no vertical-line
  primitive, so these are DOM overlays positioned from the time scale and
  re-measured whenever it moves.
- **OHLC readout** — follows the crosshair, falling back to the newest bar.

Appearance is adjustable in Settings: grid opacity, auto-scrolling, the short
order label, hiding trade results entirely, and up/down colour overrides that
default to following the theme rather than freezing at whatever the theme was
when the drawer was first opened.

---

## Theme

Both apps share one palette and offer four templates: **Light Mode**,
**Twilight**, **Full Night** and **System**. Twilight sits between the two
dark ends - a softer ground for long sessions where full contrast is tiring.
The control sits in the header of each app and in Settings.

Colours are CSS custom properties holding RGB channels, which is what lets
Tailwind's opacity modifiers keep working (`bg-accent/15`) while the value
underneath swaps. The two ramps are semantic rather than literal, so no
component carries a `dark:` variant:

```
surface-950 … surface-500   page, panels, inputs, borders   (950 = furthest back)
content-100 … content-400   text                            (100 = strongest)
```

Both ramps invert between themes, so `bg-surface-950` is near-black in dark
and near-white in light.

Details that matter more than the palette:

- **No flash of the wrong theme.** An inline script in `index.html` resolves
  the stored preference before first paint. Its storage key must stay in step
  with `THEME_STORAGE_KEY` in `stores/themeStore.ts`.
- **"System" stays live.** A `prefers-color-scheme` listener follows the OS
  while the tab is open, rather than freezing at whatever it was on load.
- **Charts read the same tokens.** lightweight-charts and Recharts take colour
  strings, so they read the custom properties through `utils/theme-colors.ts`
  and recolour in place — the price chart keeps your pan and zoom across a
  theme change.
- **Filled buttons have their own colours.** White on the vibrant `up` green is
  about 2.2:1. That green is right for text and chart series on a dark ground
  (8.3:1), so the roles are split: `up` for text and accents, `up-solid` for
  backgrounds white text sits on.
- **The scrim darkens in both themes.** Every other surface inverts; a light
  veil over a light page would not read as a veil.

- **Surfaces have to be distinguishable, not just readable.** Contrast ratios
  against text do not catch a washed-out theme: a page at `#f6f7fb` behind
  white panels passes every text check and still renders as one flat white
  sheet, because the step between them is 1.07:1. Both themes now hold a
  page-to-panel step of ~1.17:1 and a panel border ~1.5:1 clear of the panel,
  and the two are mirror images of each other.

`client/tests/theme-contrast.test.ts` computes the real ratios from the
stylesheet on every run:

- 4.5:1 for text, 3:1 for the large button labels
- page/panel, panel/border and panel/input separation, so a flat theme fails
- the border ramp opens progressively away from the panel
- the admin console has not drifted from the same palette

A colour change that quietly breaks readability — or flattens the hierarchy —
fails the build rather than shipping.

---

## Manual deposits and withdrawals

No payment provider is connected. Money moves outside the platform over UPI
or a bank transfer, and an operator confirms it here.

### Deposit — the customer pays first

1. The deposit screen shows the account to pay, from the operator-managed
   payment methods (`/admin/payment-methods`). Details are copy buttons, never
   retyped — a mistyped UPI id loses real money.
2. The customer pays, then submits the **UTR and/or a screenshot**. One or the
   other is required; there is nothing to verify without it.
3. The claim sits as `PENDING`. **Nothing is credited.** A balance that appears
   instantly and vanishes on rejection is worse than a wait.
4. An operator with `deposits:approve` opens the screenshot, matches it against
   the bank statement, and approves — which is what credits the wallet.

### Withdrawal — the platform pays second

1. The customer submits an amount and a destination (UPI id, or account number
   + IFSC). Both are format-validated.
2. The amount is **reserved immediately**, so the same balance cannot fund two
   requests while they queue for review.
3. The operator pays out, then approves **with the UTR of the payment they
   made**. That reference is mandatory and lands on the wallet ledger, so
   "did we actually pay this?" is answerable months later.
4. Rejecting instead releases the reservation and shows the customer the reason.

### What stops the obvious abuses

| Risk | Control |
|---|---|
| The same payment claimed twice | Partial unique index on `transfers.utr` for deposits; UTRs are upper-cased first, so changing the case does not evade it |
| A claim credited twice by two operators | The `PENDING → APPROVED` claim is a conditional atomic update, and the wallet credit is keyed `deposit:credit:<id>` |
| An unverified claim becoming a balance | Approval is the only path that credits; the request handler never touches the wallet |
| A renamed executable uploaded as a screenshot | The type is read from the file's own magic bytes, not its declared content-type or extension |
| One customer reading another's bank details | Screenshots are streamed through an authenticated route — never a static mount — and are readable only by the owner or a reviewer. A stranger gets 404, not 403 |
| A path-traversal filename | The stored name is a generated id; the uploaded name is display-only |
| A payout with no record | The payout UTR is required to approve a withdrawal |

`UPLOAD_DIR` and `MAX_UPLOAD_BYTES` control proof storage. **The seeded payment
details are placeholders** — replace them in the admin console before taking a
real deposit, or customers will pay into an account that is not yours.

---

## Architecture

```
Market provider ──► Market worker ──► normalise ──► Redis ──► WebSocket ──► clients
                                          │
                                          ▼
                                  feed health gate
                                          │
   POST /binary/trades ──► validation chain ──► wallet lock ──► trade ──► exposure
                                                                             │
                                                                             ▼
                                                              risk engine ──► hedge worker ──► provider
                                          ┌──────────────────────────────────┘
   expiry worker ──► settlement queue ──► settlement engine ──► wallet ──► ledger
```

### Server layout

The backend is a single Go module in `server-go/` (see `server-go/README.md`).
`npm run dev` runs it through `scripts/go.mjs`, which locates the Go toolchain.

```
server-go/
├── cmd/server/          process bootstrap, router, startup migrations
├── cmd/seed/            development seed
└── internal/
    ├── config/          env, RBAC permissions, Redis keys and WS events
    ├── models/          MongoDB documents, indexes, JSON views, migrations
    ├── platform/        httpx (middleware, validation, errors), mongo, redis, websocket hub, leader election
    ├── service/         one package per domain
    │   ├── auth/ users/            registration, sessions, profiles, favourites, per-user limits
    │   ├── wallets/                the only code that may change a balance (LIVE and DEMO accounts)
    │   ├── assets/ payouts/        instrument catalogue, trading hours, versioned payout tables
    │   ├── marketdata/             provider adapters, tick normalisation, feed health, candles
    │   ├── binary/                 trade acceptance, the validation chain, early close
    │   ├── expiry/ settlement/     due-trade detection and the single-settlement guarantee
    │   ├── exposure/ risk/         live book per asset, risk scoring and pre-trade limits
    │   ├── hedging/                hedge sizing and the external execution adapters
    │   ├── transfers/              deposits and withdrawals
    │   ├── admin/ audit/           dashboard analytics, platform P&L, append-only trail
    │   └── reconciliation/         cross-ledger checks
    ├── api/             HTTP handlers, one package per route group
    └── workers/         market, expiry, settlement, risk, hedge, reconciliation
```

Each user has two wallets: **LIVE** (real money; deposits and withdrawals) and
**DEMO** (practice money, opened lazily with ₹10,000 and resettable). Trades
carry the account they were placed on; DEMO trades use the same wallet ledger
but never enter the house book - no exposure, hedging, house risk limits,
admin P&L or platform-result reconciliation.

Workers are leader-elected through a renewable Redis lock, so several server
instances can run while only one owns the market feed, the expiry scan and the
hedge book. Settlement is the deliberate exception: it is idempotent by
construction, so every instance consumes the queue and the platform settles
faster under load.

### Pluggable providers

Market data and hedge execution are both behind interfaces, so a real vendor
replaces a class and nothing else changes.

| Market data | |
|---|---|
| `simulated` | Local random walk. Development and tests only; `authoritative: false`, and the server refuses to boot with it in production unless explicitly overridden. |
| `binance` | Real public trade stream. No credentials needed, and a genuine authoritative feed for crypto. |
| `generic-ws` | Any vendor publishing JSON ticks over a websocket. |

| Hedge execution | |
|---|---|
| `paper` | In-process sandbox. Exercises the whole pipeline — decision, order, fill, position, P&L — with nothing leaving the server. Supports simulated partial fills, slippage and rejections. |
| `broker` / `lp` | REST adapters for a real venue. Both **fail closed**: without `HEDGE_LIVE_EXECUTION_ENABLED=true` and credentials, no order is transmitted. |

---

## Testing

```bash
npm test                       # go test ./... in server-go, then the client tests
npm run typecheck              # go vet ./..., then strict TypeScript in client and admin
npm run build                  # server binary and production frontend builds
```

The Go tests are unit tests of the pure logic (money, risk scoring and limits,
settlement rules, early-close pricing, exposure derivation, validation) and need
neither MongoDB nor Redis.

The table below lists the concurrency and integration suites of the former
Node/TypeScript server. They described the behaviour the Go server was ported
to preserve; they have not been ported to Go (`indicators.test.ts` is a
client suite and still runs):

| Test | What it proves |
|---|---|
| `exposure.test.ts` | The specification's worked example: UP 10,000 / DOWN 4,000 → net 6,000, gross 14,000 |
| `wallet.test.ts` | 25 concurrent locks against a 1,000 balance yield exactly 10 successes and never oversell |
| `wallet.test.ts` | Ten simultaneous identical settlements credit the customer once |
| `trade.test.ts` | 100 concurrent requests create 100 distinct trades; 20 sharing an idempotency key create one |
| `trade.test.ts` | A stale feed and a missing price both refuse new trades |
| `settlement.test.ts` | Ten workers racing on one trade produce one settlement row and one wallet movement |
| `settlement.test.ts` | 25 trades expiring at the same instant, each settled twice, settle exactly once each |
| `settlement.test.ts` | A trade orphaned in `SETTLING` by a crashed worker is recovered |
| `settlement.test.ts` | With no price available, settlement defers rather than guessing |
| `hedging.test.ts` | A customer settles identically whether or not the book was hedged |
| `api.test.ts` | RBAC, token rotation with replay detection, rate limiting, and that one user cannot read another's trade |
| `early-close.test.ts` | A quote never mutates the position; closing twice is refused; a winning position is quoted above a losing one; the value stays under the full payout |
| `candles.test.ts` | Folding minutes into wider frames preserves the first open, the last close and both extremes, and every frame comes back aligned and in order |
| `indicators.test.ts` | The overlays are correct at the boundaries: no partial windows, no drift over a long series, population deviation for the bands |
| `transfers.test.ts` | A deposit claim credits nothing until approved; the same UTR cannot be claimed twice, even across customers or by changing case; two operators approving together credit once; a withdrawal cannot be approved without its payout UTR; rejecting releases the reservation |

### Load test

The load-test harness belonged to the former Node server and has not been
ported to Go. The figures below were measured with it, against the Node server:

```
Orders      1921 sent, 1921 accepted, 0 rejected, 96 orders/sec sustained
Acceptance  p50 210ms   p95 457ms   p99 660ms
WebSockets  500/500 connected, delivery p50 12ms  p95 27ms  p99 42ms
Feed        provider→server p99 1ms, processing p99 7ms
```

**This is not a capacity claim.** It was measured on one developer laptop:
a single Node process, MongoDB and Redis in Docker Desktop on Windows, and the
load generator on the same host. Acceptance latency is dominated by the wallet
transaction committing with majority write concern on a single-node replica
set. Per spec rule 15, production capacity should not be claimed until the same
test is run against production-shaped infrastructure with several server
instances and a real replica set.

---

## Where this implementation departs from the brief

Three deliberate decisions, each because following the text literally would
have produced something incorrect or unusable.

**1. Wallet arithmetic on settlement.** The brief specifies that opening a trade
leaves `balance` unchanged and only raises `lockedBalance`, but then says a win
credits `balance += stake + profit` and a loss only does `lockedBalance -= stake`.
Those belong to two different models: under the stated open behaviour, a loss
that only released the lock would hand the stake back, and a win that credited
the stake again would pay it twice. This implementation uses the lock model
consistently:

```
open   lockedBalance += stake                    (balance unchanged)
lose   lockedBalance -= stake, balance -= stake
win    lockedBalance -= stake, balance += profit
draw   lockedBalance -= stake
```

Both models settle on the same final balance; this one matches the stated open
behaviour and is internally consistent.

**2. Money as integer minor units.** The brief's interfaces type amounts as
`number`. Storing fractional currency in floating point drifts, which a ledger
that must reconcile cannot tolerate. Field names follow the brief; the values
are integers.

**3. The user concentration limit engages only on a material book.** Read
literally, "a single user may not hold more than 40% of one asset's book"
blocks the first customer to trade a newly listed asset — they are unavoidably
100% of an empty book. The rule now applies only once the book is larger than
any single customer is permitted to hold, below which the per-user exposure
limit is already the binding constraint.

---

## What is deliberately not built

Being explicit about this matters more than the feature list.

- **No payment provider.** Deposits and withdrawals are manual: the customer
  submits a UTR or screenshot and an operator confirms it against the bank.
  `provider` / `providerReference` on the transfer record are the seam for a PSP
  should one be connected. Nothing moves real money by itself.
- **No KYC/AML.** There is no identity verification, sanctions screening,
  source-of-funds checking or transaction monitoring. A regulated deployment
  needs all of it.
- **No live hedge venue.** The paper provider is the default; the broker and LP
  adapters are the shape a real integration takes, not a working one.
- **Not regulated.** Binary options are restricted or prohibited for retail
  customers in many jurisdictions. Operating this requires the appropriate
  licence, client-money segregation, and the disclosures your regulator
  mandates.
- **Single region.** No multi-region failover or disaster recovery drill.
- **Email and 2FA.** No verification emails, password reset, or second factor.

## Production checklist

Before this handles real customer money:

- [ ] Replace every secret in `.env`; `JWT_SECRET` and `JWT_REFRESH_SECRET` must differ
- [ ] Configure an authoritative market data provider (the server refuses the simulated feed in production)
- [ ] Run the load test against production-shaped infrastructure and set capacity from the result
- [ ] Put MongoDB on a real replica set with authentication and TLS enabled
- [ ] Enable Redis authentication and TLS
- [ ] Terminate TLS in front of the server and set `CLIENT_URL` / `ADMIN_URL` to the real origins
- [ ] Ship structured logs and the latency metrics to a monitoring system with alerting on feed health, reconciliation alerts and CRITICAL risk levels
- [ ] Review `maxAssetExposure`, `maxGrossExposure` and the hedge levels against your actual risk appetite
- [ ] Rehearse a market-feed outage and a settlement-worker crash against a staging copy
- [ ] Have the payout terms, the draw policy and the feed-halt behaviour reviewed and disclosed to customers
- [ ] Replace the placeholder deposit payment details, and put proof uploads on durable, backed-up storage rather than a local directory

---

## Environment

See `.env.example` — every variable is documented there. The ones that change
behaviour most:

| Variable | Effect |
|---|---|
| `MARKET_DATA_PROVIDER` | `simulated`, `binance` or `generic-ws`. Production refuses `simulated`. |
| `HEDGE_PROVIDER` + `HEDGE_LIVE_EXECUTION_ENABLED` | Live orders require both. |
| `FEED_HEALTHY_MAX_MS` / `FEED_DEGRADED_MAX_MS` / `FEED_STALE_MAX_MS` | Feed classification bands; also editable at runtime from the admin console. |
| `ENABLE_MONGO_TRANSACTIONS` | Set false only for a standalone `mongod`. |
| `WORKERS_ENABLED` | Set false on API-only replicas. |
| `MONGO_PORT` | Host port for the dockerised MongoDB, if 27017 is taken. |
#   b i n n a r y  
 