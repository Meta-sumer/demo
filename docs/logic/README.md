# How the platform works - logic reference

These documents describe the rules the code implements now. They were
written by reading the code, not the brief: every statement cites the file it
comes from. They were brought up to date with the audit fixes (pricing
engine, feed data rules, settlement policy, USDT, Binance hedging, admin
security); where a rule has its own audit document, the logic document
summarises it and links to `docs/audit/`, which holds the evidence and the
tests.

| # | Document | What it covers |
|---|---|---|
| 1 | [Trading core](01-trading-core.md) | Trade lifecycle and statuses, the acceptance checks in the order they run (with error codes), offers and the pricing engine, how entry price and expiry are fixed, expiry detection and the settlement state machine, the expiry-price policy and voids, win/loss/draw and the tie rule frozen at acceptance, early close, administrative cancel and its repair, assets and versioned (and scheduled) payout tables, the launch scope, DEMO vs LIVE, websocket events |
| 2 | [Money, wallets and payments](02-money-wallets-payments.md) | Minor units and micro-USDT, LIVE/DEMO/FUNDING wallets, the lock model, the ledger and its idempotency keys, manual INR deposits and withdrawals and their repair, admin adjustments, USDT through BeeX, transfers between accounts, reconciliation, audit, which permission gates which money action |
| 3 | [Risk, market data, auth, realtime](03-risk-marketdata-auth.md) | Exposure book, atomic reservation and liability limits, risk scoring, the versioned risk configuration, Binance hedging, market data providers, normalisation, feed health and the feed data rules, candles, sessions, admin two-factor, rate limits, RBAC, websocket protocol, workers and leader election, alerts, admin P&L |
| 4 | [Client and mobile](04-client-and-mobile.md) | HTTP and token handling, the realtime bridge, the stores, the asset list and launch scope, the trade ticket with live offers, live result projection, trade status words (voids), chart logic and indicator formulas, formatting, the theme system, how the React Native app differs from the web client, and the admin console where it mirrors the apps |

Read 1 -> 2 -> 3 for the server, 4 for the apps. [SUMMARY.md](SUMMARY.md) is
the short version of all four.

## The rules everything else follows

- **The server decides price, time and payout.** A trade request carries
  asset, direction, amount, expiry and account, plus the id of the offer the
  trader accepted (`GET /binary/quotes`) and the payout the app displayed.
  The offer is single-use and re-checked at acceptance; the entry price is the
  platform's mid at acceptance. Nothing a client sends can change a result.
- **The payout is a price.** The dynamic pricing engine computes it from the
  market (volatility, spread), the book (each side's worst-case liability),
  the hedge (cost and availability) and the margin, inside each payout row's
  bounds, and freezes it on the trade with every input
  ([../audit/pricing/pricing-spec.md](../audit/pricing/pricing-spec.md)).
- **Only priced instruments are offered.** At launch that is BTC/USD and
  ETH/USD on the real Binance feed; EUR/USD, GBP/USD and XAU/USD are switched
  off.
- **Money is an integer.** INR in paise, USDT in micro-USDT. No floating-point
  arithmetic on balances.
- **A trade settles once, a credit applies once.** One conditional claim with
  an immutable intent, unique indexes and ledger idempotency keys back each
  other up; every intermediate state (settling, cancelling, payout
  replacement, transfer approval) can be finished by a repair.
- **The terms are frozen at acceptance**: payout, entry price, tie rule and
  the configuration versions that produced them.
- **MongoDB is the record; Redis is a cache.** The exposure book and the
  expiry queue are rebuilt from MongoDB and reconciled against it.
- **Untrustworthy data stops new trades, not settlement.** A stale feed, a
  gap, dropped ticks or a drifting clock refuse quotes and trades; open
  trades still settle on their published terms, or are voided and refunded
  when no valid price exists.
- **Every rule that can change is versioned configuration**, edited in the
  admin console and audited (risk configuration, payout rows).
- **DEMO money never touches the house book** - no exposure, hedging, risk
  limits, P&L or platform reconciliation.

## Open items

The items that still hold after the fixes are listed at the end of each
document ("Known gaps"). The audit status of every finding is in
[../audit/README.md](../audit/README.md).
