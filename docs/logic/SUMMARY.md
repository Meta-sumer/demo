# Platform Logic - Summary

A short version of the four full logic documents in `docs/logic/`. Each section
names the full document for the detail.

## Ground rules

- **The server decides everything that matters:** entry price, start and end
  time, payout and result. A trade request carries only asset, direction,
  amount, duration and account (plus an optional quoted price that is used
  only to refuse a fill that moved too far).
- **Money is an integer:** INR in paise (₹1 = 100), USDT in micro-USDT.
- **A trade settles once, money moves once:** atomic status changes, unique
  database indexes and ledger idempotency keys back each other up.
- **MongoDB is the record, Redis is a cache:** counters and the expiry queue
  are rebuilt from MongoDB when the server starts.
- **Demo money never touches the house book:** no exposure, hedging, risk
  limits or P&L.

## 1. A trade, start to finish ([01-trading-core](01-trading-core.md))

**Placing.** The checks run in this order, and the first failure stops the
trade:

1. idempotency key (a repeated request returns the same trade)
2. trading not halted
3. user active
4. asset exists, is active and is inside its trading hours
5. duration is one the asset supports
6. the price feed is healthy enough to trade
7. price has not moved too far from the quoted price
8. a payout is configured for that duration
9. stake and exposure limits
10. available balance

Then the stake is **locked** in the wallet, the trade is saved as `OPEN`, and
it joins the expiry queue.

**Settling.** At expiry the server compares the expiry price with the entry
price:

| Outcome | Balance change |
|---|---|
| Win | + stake × payout% (profit) |
| Loss | − stake |
| Draw (prices equal) | refund by default; the platform can set Loss or Win instead |

The trade moves `OPEN → SETTLING → WON / LOST / DRAW`. The `SETTLING` claim is
atomic, so two workers can never settle the same trade.

**Sell now (early close).** The offer is a server price:

```
value = stake × (1 + payout%) × Φ(z) × (1 − fee)
z     = price move ÷ (price × volatility per second × √seconds left)
```

The win probability is clamped to 1–99%. The fee defaults to 5%, and Sell now
is closed in the last 10 seconds by default. The result is recorded as
`CLOSED_EARLY` ("Sold early").

## 2. Money ([02-money-wallets-payments](02-money-wallets-payments.md))

**Wallets.** Each user has a LIVE wallet and a DEMO wallet (₹10,000 virtual,
resettable when no demo trade is open). A running trade only **locks** its
stake:

```
open:  locked += stake
win:   locked −= stake,  balance += profit
loss:  locked −= stake,  balance −= stake
draw:  locked −= stake
```

Every change writes a ledger row with an idempotency key, so a retry never
pays twice.

**Deposits (manual).**

1. The customer pays to the UPI/bank details shown.
2. They submit the UTR and/or a screenshot.
3. The claim stays `PENDING`.
4. An operator approves it, and only that credits the wallet.

The same UTR can be claimed only once.

**Withdrawals (manual).** The amount is reserved immediately. The operator
pays out and approves with the payout UTR. A rejection returns the money.

**USDT.** The service logic exists, but its routes are not connected yet.

## 3. Risk, prices and access ([03-risk-marketdata-auth](03-risk-marketdata-auth.md))

**Exposure.** Per asset, the server keeps the UP total and the DOWN total.
Net = UP − DOWN; gross = UP + DOWN (UP 10,000 / DOWN 4,000 → net 6,000,
gross 14,000).

**Limits.** Defaults below; all are editable in the admin console.

| Limit | Default |
|---|---|
| Stake per trade | ₹10 – ₹1,00,000 |
| Open stake per user | ₹5,00,000 |
| Gross per asset | ₹50,00,000 |
| Gross platform-wide | ₹2,00,00,000 |
| User share of one asset's book | 40%, only once the book is large |

**Feed health.** The status comes from the age of the newest tick:

| Status | Newest tick | New trades |
|---|---|---|
| HEALTHY | ≤ 100 ms | allowed |
| DEGRADED | ≤ 300 ms | allowed |
| STALE | older than that | refused |
| OFFLINE | none, or older than 5 s | refused |

Open trades still settle whatever the status.

**Hedging.** Sized from the book. The paper provider is the default, and live
orders need explicit flags. Settlement never reads hedges.

**Access.**
- JWT access tokens last 15 minutes and refresh tokens 30 days.
- Refresh tokens rotate, and a replayed one is detected.
- Rate limits apply.
- Roles: Super Admin, Admin, Risk, Finance, Support, each with its own
  permissions.

**Realtime.** One websocket: `market:<asset>` channels carry prices and the
`user:<id>` channel carries trades, balances and support messages.

## 4. The apps ([04-client-and-mobile](04-client-and-mobile.md))

- **The apps never decide a price.** The web client and the mobile app only
  show what the server sends.
- **Trade ticket.**
  - Durations and payouts come from the asset.
  - The amount must stay within the stake limits.
  - Each attempt has its own idempotency key.
  - Confirm-before-trade or one-tap.
- **"If it ends now".** A local estimate from the live price; the server's
  settlement is the result.
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

- USDT routes are not connected.
- Some crash-recovery paths around early close and manual approvals need
  hardening.
- A few security settings need tightening before real money: suspension does
  not revoke sessions immediately, and the rate limiter fails open when
  Redis is down.
- Prices are simulated (demo) until a real market data provider is
  configured.

Details, with file references, are in the four full documents.
