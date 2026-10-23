# Metric dictionary: dashboard, exposure, hedging and P&L screens

Audit finding: **F-18** (risk dashboard and P&L metric definitions).

Every figure on the admin Dashboard, Exposure, Hedging and Reports (P&L)
pages: what it is, how it is computed, where the data comes from, how often
it refreshes, its unit, whether it is realised or unrealised, how working
(unfilled) hedge orders are treated, and how the screen shows stale or
missing data. Code references are to `server-go/internal/service/admin/admin.go`
(`getDashboard`, `GetPlatformPnl`), `.../service/exposure/`,
`.../service/risk/`, `.../service/hedging/` and the pages in `admin/src/pages/`.

Conventions:

- **Money** is INR minor units (paise) on the wire and in storage; the screens
  divide by 100. Every money figure excludes DEMO accounts; wallet totals
  also exclude the USDT (FUNDING) wallets.
- **Hedge notionals** are delta-equivalent INR notionals: the INR value of
  the underlying that would offset the book's sensitivity to a relative price
  move (docs/audit/hedging/hedging-spec.md section 3).
- **Working orders** (PENDING, ACCEPTED, PARTIALLY_FILLED) are contingent: no
  figure counts them as protection or P&L. Only fills do. The Hedging page
  shows them in their own column.
- **Freshness display** (`admin/src/utils/freshness.ts`,
  `components/freshness.tsx`): a value whose data is older than its allowed
  age carries a **"stale since hh:mm:ss (age)"** badge; a value that could
  not be loaded reads **"unavailable"**, never 0. For a polled query the
  allowed age is two refresh intervals plus one second (`queryMaxAge`).
  Tested in `admin/tests/freshness.test.ts`.
- **Permissions**: exposure and hedge figures need `exposure:read` /
  `hedge:read`, P&L figures `reports:read`; a figure the caller may not see is
  omitted by the server (not zeroed) and its card is not drawn
  (`admin.VisibilityFor`, docs/audit/security/permission-matrix.md).

## 1. Dashboard (`admin/src/pages/Dashboard.tsx`, `GET /admin/dashboard`)

Summary refresh: every **5 s**. Staleness: the "Total users" card carries the
summary's stale badge (it applies to every card of the summary); a failed
first load shows "unavailable" in every card. `generatedAt` is the server
time of the figures.

| Figure | Formula | Source | Unit | Realised / unrealised | Working orders | Staleness / reconciliation shown |
|---|---|---|---|---|---|---|
| Total users | count of users with role USER | `users` | count | - | - | summary badge |
| Active today (hint) | distinct `userId` of non-DEMO trades created since 00:00 UTC | `binary_trades` | count | - | - | summary badge |
| Open trades | count of non-DEMO trades OPEN or SETTLING | `binary_trades` | count | - | - | summary badge |
| Placed today (hint) | count of non-DEMO trades created since 00:00 UTC | `binary_trades` | count | - | - | summary badge |
| Today's volume | sum of `stake` of non-DEMO trades created since 00:00 UTC | `binary_trades` | INR | - | - | summary badge |
| Today's P&L | sum of `platformResult` of non-DEMO trades **created** since 00:00 UTC (open trades contribute 0 until settled) | `binary_trades` | INR | realised (settled trades only) | - | summary badge |
| Current exposure | sum over assets of `upStake + downStake` of the committed book (reservations excluded) | Redis book `exp:asset:{id}` (`exposure.GetPortfolioExposure`) | INR stake | open book (unrealised) | - | summary badge |
| Net (hint) | sum over assets of `upStake - downStake`; + = book leans UP | same | INR stake | open book | - | summary badge |
| Hedged exposure | sum over assets of `|filled hedge position| x mark x INR rate` | `hedge_positions.notionalInr` (`hedging.GetHedgeBookSummary`) | INR delta-equivalent | position at mark | **excluded** | reconciliation status badge when not OK; "mark stale since" when the oldest mark of an open position is older than 60 s (`hedgeMarkedAt`) |
| % of target (hint) | hedged exposure / hedge target x 100, capped at 100 | as above, target from the risk state | % | - | excluded | as above |
| Hedge target (hint value) | sum over assets of `|target|` (section 3 of the hedging spec) | Redis `risk:asset:{id}` field `hedgeTargetSigned`, written by the risk worker every 1 s; oldest evaluation in `hedgeTargetAsOf` | INR delta-equivalent | - | - | summary badge |
| Unhedged exposure | sum over assets of `|target - signed filled position|` (what the policy target still lacks) | risk state + `hedge_positions` | INR delta-equivalent | - | **excluded** (a working order does not reduce it) | summary badge |
| Liability (hint) | sum over assets of `max(0, upProfit - downStake, downProfit - upStake)` | Redis book | INR | open book | - | summary badge |
| Feed latency | p50 of the last 2,000 feed latency samples; p95 / p99 in the hint | Redis list `metrics:latency:feed.total` (`metrics.GetLatencySummary`) | ms | - | - | summary badge |
| Customer balances | sum of `balance` of INR LIVE wallets; locked in the hint (`lockedBalance`) | `wallets` | INR | - | - | summary badge |
| Pending deposits / withdrawals | count of manual INR transfers with status PENDING by kind | `transfers` | count | - | - | summary badge |
| Trade acceptance p95 | p95 of the last 2,000 trade acceptance latencies | Redis `metrics:latency:trade.acceptance` | ms | - | - | summary badge |
| Active assets (hint) | count of active assets | `assets` (cached 2 s) | count | - | - | summary badge |

Charts (refresh **60 s**, loading skeleton while fetching; daily aggregates,
no stale badge):

| Chart | Formula | Source | Unit |
|---|---|---|---|
| Trading volume | per UTC day (14 days, every day emitted), sum of `stake` of non-DEMO trades by `createdAt` | `binary_trades` (`GetDailySeries`) | INR |
| Platform P&L | per UTC day, sum of trade `platformResult` (by trade `createdAt`) and sum of settlement `payoutAmount` (by settlement `createdAt`) | `binary_trades`, `settlements` | INR, realised |
| Volume by asset | last 7 UTC days, sum of `stake` per asset | `binary_trades` (`GetAssetVolume`) | INR |
| Trade outcomes | 14 days, counts of WON / LOST / DRAW (customer view) | `binary_trades` | count |

## 2. Exposure (`admin/src/pages/Exposure.tsx`, `GET /admin/exposure`)

Refresh: websocket `exposure.updated` on every trade and settlement, plus a
**15 s** poll. Staleness: "updated hh:mm:ss" uses the newer of the websocket
update and the last poll; a "stale since" badge appears when that is older
than 31 s; a failed load shows "unavailable".

| Figure | Formula | Source | Unit | Notes |
|---|---|---|---|---|
| Gross exposure | sum over assets of `upStake + downStake` | Redis book (`exposure.GetPortfolioExposure`) | INR stake | committed trades only; in-flight reservations are separate (`reservedStake`) |
| Net exposure | sum of `upStake - downStake`; + leans UP | same | INR stake | |
| Potential liability | sum over assets of `max(0, upProfit - downStake, downProfit - upStake)` | same | INR | worst case of the two outcomes per asset, no expiry netting; the liability model with expiry buckets is `expiryLiability` (hedging spec 3.2) |
| Open trades | sum of open trade counts | same | count | |
| Table UP / DOWN / NET / Gross | per asset as above | same | INR stake | |
| Table Liability | per asset `potentialLiability` | same | INR | |
| Open | per asset open trades | same | count | |
| Utilisation | `max(gross / min(asset limit, max asset exposure), |net| / net limit, platform gross / max gross, liability utilisations when configured)` | `risk.AssessRisk` | % | the binding constraint; 50% / 80% colour steps |
| Hedge target | the asset's signed delta-equivalent target, and the cap that binds | `RiskAssessment.hedgeTarget` (`risk.ComputeHedgeTarget`) | INR delta-equivalent | recomputed on every request; "--" when not computed |
| Risk | level from score: utilisation + feed penalty + volatility + near-expiry, bands from `criticalExposurePercent` | `risk.ClassifyRiskLevel` | LOW..CRITICAL | |

## 3. Hedging (`admin/src/pages/Hedging.tsx`)

Refresh: status and monitor **5 s**, alerts **15 s**, orders **10 s**.
Sources: `GET /admin/hedging/status` (`hedging.GetHedgeStatus`),
`GET /admin/hedging` (`hedging.GetHedgeMonitor`), `GET /admin/hedging/alerts`,
`GET /admin/hedging/orders`.

### 3.1 Status cards and venue table

| Figure | Formula / meaning | Source | Refresh of the data behind it | Unit | Staleness shown |
|---|---|---|---|---|---|
| Hedge venue | `paper`, `binance-testnet` or `binance-live`; LIVE / TESTNET / PAPER badge; "Orders allowed" or the reason they are not (missing keys, live switch off) | configuration (`providers.Venue.CanTrade`) | - | - | status query stale badge; "unavailable" on error |
| INR per USDT | rate in force: newest of the environment rate and operator rates | `hedging.CurrentFX` (`hedge_fx_rates`, env) | when set | INR per USDT | "rate stale since" when older than `HEDGE_FX_MAX_AGE_MS` (24 h); "unavailable" when missing - hedging then stops |
| Reconciliation | worst position status: DRIFT > UNKNOWN > NEVER > OK / NOT_APPLICABLE | `hedge_positions.reconcileStatus` | every `HEDGE_RECONCILE_INTERVAL_MS` (60 s) | status | "Last reconciled" = oldest successful reconciliation; stale badge when older than 2 x interval + 60 s |
| Open hedge alerts | count of open `hedge_alerts` | `hedge_alerts` | on event | count | - |
| Cost model error (hint) | mean of (realised per-side cost - estimated per-side cost) over the last 50 orders with fills | `hedge_orders.realisedCostBps`, `decision.estimatedCost.perSideBps` | on fills | bps per side | "no filled orders" when none |
| Venue: state, mid, mark, funding rate, taker fee | the venue snapshot per symbol: book mid, mark price, `lastFundingRate`, the account taker fee (or "config") | Redis `hedge:venue:{db}:{symbol}` (`RefreshVenueSnapshot`) | every 3 s on the hedge leader | USDT, %, bps | "stale since" when older than `HEDGE_VENUE_MAX_AGE_MS` (15 s); state FAILED with the error on a failed read |

### 3.2 Hedge coverage (per asset)

| Column | Formula | Source | Unit | Realised / unrealised | Working orders |
|---|---|---|---|---|---|
| Asset | symbol; venue symbol, "not hedgeable" (not in `HEDGE_SYMBOLS`) or "hedging disabled" | assets, configuration | - | - | - |
| Book delta | `D = sum dir x P x phi(d2) / (sigma sqrt(tau))` over open trades outside the gamma window | `risk.BookDelta` via `risk.AssessAsset` (computed for the request) | INR delta-equivalent | - | - |
| Target | `clamp(h x D, +-cap)`; below it h and the binding cap, or why it is zero | `RiskAssessment.hedgeTarget` | INR delta-equivalent, signed | - | - |
| Held (filled) | `sign(position) x |position| x mark x rate`; quantity and % of target below | `hedge_positions` (`qtyE8`, `notionalInr`) | INR, base-asset quantity | position at mark | **excluded** |
| Working | remaining quantity of the asset's working order x its sizing price x rate, signed; "contingent" | latest `hedge_orders` row when working | INR, quantity | - | **only here** |
| Residual | `target - held` | as above | INR, signed | - | excluded |
| Risk | the asset's risk level | `risk.AssessAsset` | level | - | - |
| Reconciliation | position status, last reconciled time; on DRIFT the venue quantity and the drift | `hedge_positions` | status, time | - | - |
| Cost / availability | "available" or "expensive" with the round-trip cost of the target, or the unavailability reason (`NOT_HEDGEABLE`, `HEDGE_DISABLED`, `HEDGE_NOT_CONFIGURED`, `LIVE_EXECUTION_DISABLED`, `FX_STALE`, `FX_MISSING`, `VENUE_UNAVAILABLE`, `VENUE_STALE`) | `hedging.CostEstimate` | bps round trip | - | - |
| Realised (net) | `trading P&L - fees + funding` since inception; fees and funding in the hint | `hedge_positions` (fold of `hedge_fills`, sums of `hedge_funding`) | INR at stored rates | **realised** | excluded |
| Unrealised | `(mark - average entry) x position x rate` | `hedge_positions` | INR | **unrealised** | excluded |
| Last order | status of the asset's latest order | `hedge_orders` | status | - | - |

Table staleness: a "table stale since" badge when the last successful poll is
older than 11 s; per row a "mark stale since" badge when an open position's
mark (`markedAt`, refreshed every 15 s) is older than 60 s; "unavailable" when
the first load fails.

### 3.3 Alerts and orders

| Figure | Meaning | Source |
|---|---|---|
| Alert code, message, seen, last seen | one open problem (drift, unknown order, stale rate, venue unreachable, fee asset, missing fills, ledger mismatch); occurrences counts repeat sightings | `hedge_alerts` |
| Order target / filled / remaining qty | requested quantity, venue executed quantity, difference; fills count below | `hedge_orders.targetQtyE8`, `filledQtyE8` | base asset |
| Avg price | venue average fill price | `averageFillPrice` | USDT |
| Fee | commission of the order's recorded fills at the order's rate | `feeInr` | INR, realised |
| Cost bps/side est. / real | half of the estimated round trip at decision; realised per-side cost of the fills vs the sizing mid plus fees | `decision.estimatedCost`, `realisedCostBps` | bps |
| Status | lifecycle state (hedge-lifecycle.md section 2); "working - contingent" for working orders; error text | `status`, `error` | - |

## 4. Reports - P&L (`admin/src/pages/Reports.tsx`, `GET /admin/reports/pnl`)

Refresh: on load and when the date range changes (no polling); the footer
shows `generatedAt`. Window: `from` 00:00 to `to` 23:59:59.999 UTC.
"unavailable" when the report cannot be loaded.

| Figure | Formula | Source | Unit | Realised / unrealised |
|---|---|---|---|---|
| Customer stakes collected | sum of `stake` of non-DEMO settlements created in the window | `settlements` | INR | realised |
| Customer payouts | sum of `payoutAmount` of those settlements | `settlements` | INR | realised |
| Platform gross result (book) | sum of `platformResult` of those settlements | `settlements` | INR | realised |
| Open customer liability | sum of `stake + profit` of non-DEMO trades OPEN or SETTLING **now** | `binary_trades` | INR | point in time |
| Hedge realised result | `trading - fees + funding + corrections` | `hedging.GetHedgePnl` | INR at stored rates | realised |
| Hedge: trading result (average cost) | sum of `realisedInr` of fills with `tradeTime` in the window | `hedge_fills` | INR | realised |
| Hedge: fees | sum of `feeInr` of those fills (shown negative) | `hedge_fills` | INR | realised |
| Hedge: funding | sum of `amountInr` of funding payments with `time` in the window | `hedge_funding` | INR | realised |
| Hedge: corrections | sum of `amountInr` of corrections created in the window | `hedge_adjustments` | INR | realised |
| Hedge unrealised result | sum of `unrealisedInr` of the positions at their latest mark | `hedge_positions` | INR | **unrealised**, point in time, excluded from the net result |
| Fees | sum of `FEE` wallet transactions (INR, non-DEMO) in the window | `wallet_transactions` | INR | realised |
| Adjustments | `ADJUSTMENT_DEBIT - ADJUSTMENT_CREDIT` (INR, non-DEMO) in the window | `wallet_transactions` | INR | realised |
| Net result | `platform gross result + hedge realised result + fees + adjustments` | computed | INR | realised |
| Hedge reconciliation (footer) | worst position status and oldest reconciliation time; "hedge mark stale since" when the oldest mark of an open position is older than 60 s | `hedge_positions` | status, time | - |

Working hedge orders contribute nothing to any P&L line; only fills do.

## 5. Refresh intervals behind the figures

| Producer | Interval | Writes |
|---|---|---|
| Trade path / settlement | per event | Redis book counters, `binary_trades`, `settlements`, `wallets` |
| Risk worker (leader) | `RISK_INTERVAL_MS`, 1 s | risk assessments, Redis `risk:asset:{id}` (target, delta, sigma) |
| Hedge worker (leader) | `HEDGE_INTERVAL_MS`, 1 s; venue snapshot 3 s; full pass and marks 15 s; reconciliation `HEDGE_RECONCILE_INTERVAL_MS` 60 s | `hedge_orders`, `hedge_fills`, `hedge_positions`, `hedge_funding`, `hedge_alerts`, Redis `hedge:venue:*` |
| Market data | per tick | latency samples, last tick |
