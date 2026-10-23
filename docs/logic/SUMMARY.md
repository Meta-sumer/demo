  # Platform Logic - Summary

A short version of the four full logic documents in `docs/logic/`. Each section
names the full document for the detail; the audit evidence is in
`docs/audit/`.

## Ground rules

- **The server decides everything that matters:** entry price, start and end
  time, payout and result. The app asks for a quote, the trader accepts one
  offer, and the server re-checks it when the trade arrives. Nothing the app
  sends can change a result.
- **The payout is priced, not fixed.** The pricing engine sets it from the
  market's volatility and spread, how much the platform already stands to
  lose on that side, the hedge, and the margin, inside the limits set for each
  asset and duration.
- **Only BTC/USD and ETH/USD trade at launch**, on the real Binance price
  feed. EUR/USD, GBP/USD and XAU/USD are switched off and the apps do not show
  them.
- **Money is an integer:** INR in paise (₹1 = 100), USDT in micro-USDT.
- **A trade settles once, money moves once:** one atomic claim with its
  outcome recorded, unique database indexes and ledger idempotency keys back
  each other up. Anything interrupted half way can be finished by a repair.
- **A trade's terms are fixed when it is accepted:** payout, entry price and
  the tie rule.
- **MongoDB is the record, Redis is a cache:** the exposure book and the
  expiry queue are rebuilt from MongoDB and checked against it continuously.
- **Demo money never touches the house book:** no exposure, hedging, risk
  limits or P&L.

## 1. A trade, start to finish ([01-trading-core](01-trading-core.md))

**Quoting.** While the ticket is open the app fetches a quote every second:
one offer for UP and one for DOWN, each with its payout, valid for 3 seconds
and usable once.

**Placing.** The checks run in this order, and the first failure stops the
trade:

1. idempotency key (a repeated request returns the same trade)
2. trading not halted
3. user active
4. asset exists, is active and is inside its trading hours
5. duration is one the asset supports
6. the price feed is healthy enough to trade (and no feed rule blocks it:
   dropped ticks, a drifting clock)
7. price has not moved too far from the quoted price
8. a payout row is configured for that duration, and the stake is within its
   bounds
9. the offer is still valid, unused and for this trade, and the price has not
   moved more than 2 bps since it (an old app without an offer is priced now)
10. stake and exposure limits
11. the tie rule in force is frozen on the trade
12. exposure is reserved atomically (LIVE only), including the side's
    worst-case loss
13. available balance (the stake is locked)

Then the trade is saved as `OPEN`, its reservation committed, and it joins the
expiry queue.

**Settling.** The expiry price is the last price recorded at or before the
expiry moment, no more than 2 seconds older than it. A price recorded after
expiry is never used. Then:

| Outcome | Balance change |
|---|---|
| Win | + stake × payout% (profit) |
| Loss | − stake |
| Tie (prices equal) | refund, under the tie rule frozen on the trade (the platform can set Loss or Win for new trades) |
| No valid price | the platform keeps looking for 60 seconds, then voids the trade and returns the stake; the apps show "Void · refunded" with the reason |

The trade moves `OPEN → SETTLING → WON / LOST / DRAW`. The `SETTLING` claim
records the outcome with it, so two workers can never settle the same trade
and a crash is finished with the same result. These timings are versioned
settings in the admin console, with hard limits nobody can configure away.

**Sell now (early close).** The offer is a server price:

```
value = stake × (1 + payout%) × Φ(z) × (1 − fee)
z     = price move ÷ (price × volatility per second × √seconds left)
```

The win probability is clamped to 1–99%. The fee defaults to 5%, a quote is
valid for 10 seconds, and Sell now is closed in the last 10 seconds by default
(never under 1 second). It is refused when the feed is not trustworthy and
while trading is halted, globally or on the trade's symbol (a Sell-now is a
new acceptance of a price; the trade itself still settles at expiry). The
result is recorded as `CLOSED_EARLY` ("Sold early").

**Cancelling (admin).** Only an open trade; the stake is returned in full. A
cancel interrupted before the money moved is finished by repeating it or by
the repair, and reconciliation flags it until then.

**Payout changes.** Each change is a new version; a trade keeps the terms it
was accepted on. A change can be scheduled for a later time, and there is
always exactly one active version per asset and duration.

## 2. Money ([02-money-wallets-payments](02-money-wallets-payments.md))

**Wallets.** Each user has a LIVE wallet, a DEMO wallet (₹10,000 virtual,
resettable when no demo trade is open) and, for USDT, a FUNDING wallet. A
running trade only **locks** its stake:

```
open:   locked += stake
win:    locked −= stake,  balance += profit
loss:   locked −= stake,  balance −= stake
draw / void / cancel:  locked −= stake
```

Every change writes a ledger row with an idempotency key (`trade:lock:<id>`,
`trade:settle:<id>`, ...), so a retry never pays twice. Admin adjustments (of
the LIVE, DEMO or USDT FUNDING wallet, each in its own currency) need an
Idempotency-Key and replay the first result. A stake locked by a trade that
was never written (a crash in between) is released by a leader sweep that
goes through every lock.

**Deposits (manual INR).**

1. The customer pays to the UPI/bank details shown.
2. They submit the UTR and/or a screenshot.
3. The claim stays `PENDING`.
4. An operator approves it, and only that credits the wallet.

The same UTR can be claimed only once. A step that fails after approval can be
retried or repaired, and reconciliation flags it.

**Withdrawals (manual INR).** The amount is reserved immediately. The operator
pays out and approves with the payout UTR. A rejection returns the money.

**USDT (BeeX).** Deposits to a TRC20 or BSC address and withdrawals go through
BeeX; webhooks are signed and replay-protected, the provider is reconciled
regularly, and limits apply. USDT lands in the FUNDING wallet; moving it to
trading is a separate, switchable step. A withdrawal whose provider answer
does not match stays reserved until an operator, having checked it with
BeeX, marks it completed (the amount leaves once) or failed (it returns once).

**Reconciliation.** Wallets against the ledger, locks against open trades and
withdrawals, settlements against trades and ledger rows, transfers, USDT
deposits and withdrawals against the provider, hedge orders, stuck
settlements, cancelled trades whose stake is still locked, and stake locks
without an open trade. A mismatch is
an alert for an operator; nothing is "fixed" silently.

## 3. Risk, prices and access ([03-risk-marketdata-auth](03-risk-marketdata-auth.md))

**Exposure.** Per asset the server keeps UP and DOWN stakes and profits, and
each side's worst-case loss (what the platform pays if that side wins, minus
the other side's stakes). Every new LIVE trade reserves its place atomically
against all the limits at once, so two trades can never both use the last of
a limit. A finished trade leaves the book by its own trade id, exactly once.
If Redis fails just after a trade is written, the trade is still confirmed to
the app and the book and expiry queue are repaired from MongoDB.

**Limits.** Defaults below; all are versioned settings in the admin console.

| Limit | Default |
|---|---|
| Stake per trade | ₹10 – ₹1,00,000 |
| Open stake per user | ₹5,00,000 |
| Gross per asset | ₹50,00,000 |
| Gross platform-wide | ₹2,00,00,000 |
| Worst-case loss per side (BTC/USD, ETH/USD) | ₹12,50,000 |
| User share of one asset's book | 40%, only once the book is large |

**Feed health and freshness.** One freshness rule decides both whether a
price can be quoted and whether a trade can be accepted: the newest tick
must be under 2 s old (the owner's `stale_after`), arrive without unusual
delay, and no data rule may be blocking the feed. An offer is therefore never
issued on a price that acceptance would refuse; if the market goes stale or a
halt lands before the offer is used, the refusal carries a fresh quote so the
app shows the new state. For monitoring, each feed is also classed by the age
and latency of its newest tick:

| Status | Newest tick (age or latency) |
|---|---|
| HEALTHY | ≤ 100 ms |
| DEGRADED | ≤ 300 ms |
| STALE | older or later than that |
| OFFLINE | none, or older than 5 s |

On top of that: a gap in the data (nothing at all from the provider, as
opposed to a quiet market) is recorded with its exact start and end and
raises an alert; ticks dropped by an overloaded buffer, or a server clock
that drifts from Binance's, refuse new trades until resolved. Open trades
still settle whatever the status, or are voided if no valid price exists.
If the server holding the market feed crashes, another takes over within
about 3 seconds.

**Hedging.** On Binance USDⓈ-M perpetual futures, sized from the book's
sensitivity to the price and capped; a paper venue is the default and live
orders need explicit switches. When a hedge cannot be placed, or costs too
much, new trades on that asset get a lower payout or are refused. Settlement
never reads hedges.

**Access.**
- JWT access tokens last 15 minutes and refresh tokens 30 days; refresh tokens
  rotate, and a replayed one is detected.
- Suspending a user ends their sessions.
- Admin roles sign in with an authenticator code (two-factor).
- Rate limits apply, and keep applying when Redis is down.
- Roles: Super Admin, Admin, Risk, Finance, Support, each with its own
  permissions; each admin page shows only what the role may see.

**Realtime.** One websocket: `market:<asset>` channels carry prices and the
`user:<id>` channel carries trades, balances and support messages.

**Alerts.** Operational problems (feed gaps, a drifting clock, reconciliation
mismatches, hedge and USDT provider issues, stuck settlements) open one alert
each, shown in the admin console and emailed to operators.

## 4. The apps ([04-client-and-mobile](04-client-and-mobile.md))

- **The apps never decide a price.** The web client and the mobile app only
  show what the server sends.
- **Only priced instruments are shown** (BTC/USD and ETH/USD at launch).
- **Trade ticket.**
  - Durations come from the asset; the payout for each direction comes from a
    live quote refreshed every second.
  - The amount must stay within the stake limits.
  - The chosen offer is sent with the order; if the terms changed, the app
    shows the new ones for the trader to confirm.
  - Each attempt has its own idempotency key.
  - Confirm-before-trade or one-tap.
- **"If it ends now".** A local estimate from the live price; the server's
  settlement is the result.
- **Results.** Won, Lost, "Draw · refunded", "Sold early", and "Void ·
  refunded" with the reason when no valid price existed.
- **Chart.**
  - Candles come from the server, and live ticks update the newest one.
  - Indicators (SMA, EMA, Bollinger, RSI, MACD, Stochastic) are display only.
  - Drawings are saved per asset on the device.
- **Mobile.**
  - Same logic as the web client.
  - Tokens are kept in the phone's secure storage.
  - Live prices are applied at most every 250 ms, so the phone stays
    responsive.
  - The send button is locked while an order is in flight.

## Known gaps

Still open after the fixes (each is explained, with file references, in the
document named). Closed on 23 Oct 2026: orphaned stake locks (now swept
through every lock on a leader loop, with a reconciliation check), the USDT
withdrawal whose provider answer did not match (now marked completed or
failed by an operator, audited) and FUNDING balance corrections (now an
audited admin adjustment in USDT), the two freshness rules for quotes and
trades (now one), Sell now under a per-symbol halt (now refused), the
failure reported for a trade already written (now the trade is returned),
exposure released by matching fields (now by trade id), and the 25 s feed gap
after a feed-leader crash (now about 3 s).

- **USDT:** no live BeeX run has happened yet, and resolving a mismatched
  withdrawal relies on one operator checking BeeX (doc 2).
- **Freshness:** an accepted price may be up to 2 s old (the owner's rule);
  the apps' feed indicator still uses the stricter 300 ms monitoring band
  (docs 1 and 4).
- **Feed failover** still leaves about 3 s without prices; a trade expiring
  in it more than 2 s after the last price is voided and refunded (docs 1
  and 3).
- **Part of the Redis book is not namespaced by database** (doc 3).
- **Hedging** has only been tested against a fake Binance; real keys and
  calibration on real fills are still needed (doc 3).
- **Apps:** past trades on instruments no longer offered show prices with 2
  decimals, and a halted instrument is not labelled in the lists (doc 4).

Details, with file references, are in the four full documents.
