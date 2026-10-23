# Hedging specification: target, instrument, costs

Audit findings: **F-07** (hedge target logic and effectiveness) and the
hedging side of **F-10** (hedge costs connected to pricing).
Companion documents: [hedge-lifecycle.md](hedge-lifecycle.md) (orders, fills,
reconciliation, realised P&L - F-08, F-09) and
[../operations/metric-dictionary.md](../operations/metric-dictionary.md) (F-18).

Code: `server-go/internal/service/risk/hedgetarget.go` and
`risk.ComputeHedgeTarget` (target), `server-go/internal/service/hedging/`
(execution, costs, ledger, reconciliation), `.../hedging/providers/binance.go`
(venue adapter), `server-go/internal/config/hedge.go` (configuration).
Money in the platform is INR minor units (paise); venue quantities and USDT
amounts are stored as integers in units of 1e-8 (`models.QtyScale`).

## 0. Status

| Item | Status |
|---|---|
| Target as the delta-equivalent of the short binary book, with caps and a gamma window | Implemented |
| Binance USDⓈ-M perpetual adapter (signed REST, testnet and live, live switch) | Implemented, tested against an `httptest` fake of the Binance API |
| Executable quantity from the venue's `exchangeInfo` filters | Implemented |
| Cost model and availability for pricing (`hedging.CostEstimate`, `hedging.EstimateTradeHedgeCost`) | Implemented and installed in pricing at server start (`service/pricinghedge`, section 7.4) |
| INR per USDT with a timestamp and a staleness limit | Implemented (environment rate plus operator-entered rates); an automatic rate source is not connected |
| Real Binance keys | **Not available yet.** Nothing in this repository has talked to the real testnet or the live exchange |
| Calibration of h, the caps and the cost inputs on real fills | **Open** (needs testnet, then live, fills) |

## 1. Scope and owner decisions

- Hedging is in scope for launch, on **Binance only**.
- Launch assets: **BTC/USD** and **ETH/USD**. EUR/USD, GBP/USD and XAU/USD are
  off at launch and must never be hedged. This is enforced by configuration,
  not by convention: only assets listed in `HEDGE_SYMBOLS`
  (default `BTC/USD=BTCUSDT,ETH/USD=ETHUSDT`) have a venue symbol
  (`config.Env.HedgeVenueSymbol`); every other asset is "not hedgeable" in
  the target, the cost estimate (`NOT_HEDGEABLE`) and the worker, whatever its
  `hedgeEnabled` flag says (`TestHedgeDefaults`).
- The book is in INR; Binance trades in USDT. Conversion uses a configured
  INR-per-USDT rate with its observation time; a stale rate stops hedging
  (section 6).
- Live execution stays refused unless `HEDGE_LIVE_EXECUTION_ENABLED=true`
  (section 10).
- Hedging is an aggregate action on the book. Settlement never reads hedge
  data; nothing here can change a customer's result.

## 2. Instrument: Binance USDⓈ-M perpetual futures

The platform is short binary options to customers in both directions: an UP
trade loses the platform money when the price rises, a DOWN trade when it
falls. The hedge must therefore go long and short.

| Candidate | Verdict | Why |
|---|---|---|
| **USDⓈ-M perpetual (BTCUSDT, ETHUSDT)** | **Chosen** | Long and short in one account; USDT-margined and linear (P&L = quantity x price change, in USDT); the deepest crypto derivatives books; a testnet (`https://testnet.binancefuture.com`) with the same API as live (`https://fapi.binance.com`). |
| Spot BTCUSDT / ETHUSDT | Rejected | Cannot go short without margin borrowing; a book leaning DOWN could not be hedged. |
| COIN-M (inverse) futures | Rejected | Margined and settled in the coin; P&L is non-linear in USD; adds a coin balance to manage. |
| Binance options | Rejected | European, daily or longer expiries; they do not match 30 s - 30 min binaries and are far less liquid. |

**Basis against the platform's price source.** The platform prices and
settles on Binance **spot** trades (`btcusdt`, `ethusdt` streams). The perpetual
trades at its own price, anchored to an index of spot venues by the funding
mechanism. The difference (basis) is normally a few basis points and moves
slowly; the hedge is sized on the perpetual's mid
(`VenueSnapshot.Mid`) and the delta on the platform price, so a basis change
between sizing and settlement is a residual risk, small against the binary
payoffs (section 9).

**Funding.** Perpetual positions pay or receive funding every 8 hours
(`lastFundingRate` from `/fapi/v1/premiumIndex`, typically about ±0.01% per
8 h). The cost model charges `|rate| x hold / 8 h` as a cost (section 7); the
ledger records the actual payments from `/fapi/v1/income` (FUNDING_FEE).

**Liquidity.** At the time of writing BTCUSDT has a quantity step of 0.001
BTC, minimum quantity 0.001, a MARKET order maximum of 120 BTC and a minimum
notional of 100 USDT; ETHUSDT 0.001 ETH and 20 USDT. These values are read at
run time from `/fapi/v1/exchangeInfo` (`Binance.Rules`, cached one hour) and
never hard-coded, except in the in-process paper venue.

**Near-expiry behaviour.** The perpetual has no expiry; the binaries do. A
binary's delta grows without bound as it nears expiry at the money (gamma),
which no linear instrument can follow. Section 3.3 explains how the target
treats trades near their expiry.

## 3. From the binary book to a hedge target (F-07)

### 3.1 Objective: liability sensitivity

Each open LIVE trade is a cash-or-nothing option the platform has sold. With
P = stake + profit (what the platform pays if the customer wins), strike
K = entry price, S = current platform price, tau = seconds to expiry,
sigma = volatility per sqrt(second), and no drift over seconds to minutes
(r = 0):

```
value of an UP contract     V = P x N(d2)
                            d2 = ln(S / K) / (sigma sqrt(tau)) - sigma sqrt(tau) / 2
sensitivity to S            dV / d ln S = P x phi(d2) / (sigma sqrt(tau))
```

A DOWN contract has the same magnitude and the opposite sign. The platform's
liability therefore rises with S by the **book delta-equivalent notional**

```
D = sum over open trades of  dir x P x phi(d2) / (sigma sqrt(tau))      (INR minor units)
dir = +1 for UP, -1 for DOWN
```

Holding D of the underlying (long when D > 0, short when D < 0) makes the
book's value insensitive to small price moves. This is the hedge objective:
**neutralise a chosen fraction of the book's first-order sensitivity, within
caps that keep the hedge from becoming the larger risk.**

Code: `risk.LegDelta`, `risk.BookDelta` (`hedgetarget.go`). Inputs are read in
`risk.AssessAssetDetailed`: the asset's open LIVE trades (`loadHedgeLegs`:
status OPEN, not DEMO; direction, stake + profit, entry price, expiry), the
platform's last tick, and sigma (section 3.4). A draw refund is ignored (the
probability of an exact tie is negligible for the delta).

### 3.2 Target

```
target = clamp( h x D , -cap, +cap )
h      = min( band.hedgeRatio + volatilityBoost , asset.targetHedgeRatio , 1 )
cap    = min( asset.maxHedgeNotional , HEDGE_MAX_LIABILITY_MULTIPLE x asset expiry liability )
```

- **Band** (`risk.SelectHedgeLevel` over risk config `hedgeLevels`, unchanged)
  is chosen by the asset's utilisation. Below the hedge threshold
  (`asset.hedgeThresholdPercent`, else `hedgeThresholdPercent`, default 30%)
  or in a band with ratio 0 the target is **0**, and an existing hedge is
  unwound.
- **Volatility boost**: `volatilityHedgeBoost` (default 0.1) when the per-tick
  realised volatility exceeds `risk.ElevatedVolatility`.
- **Asset ceiling** - this replaces the old `band x asset x 2` scaling. The
  asset's `targetHedgeRatio` is now the highest fraction of its delta the
  platform will hedge (`risk.HedgeRatioFor`). The old factor 2 existed only
  to make the default asset value 0.5 neutral; with the new rule the default
  0.5 caps h at 0.5, so **the launch configuration should set BTC/USD and
  ETH/USD `targetHedgeRatio` explicitly** (0.95 lets the top band apply).
- **Caps** (`risk.CapTarget`): the asset's `maxHedgeNotional` (default INR 10
  lakh) and a liability multiple (default 100): the hedge never exceeds 100
  times what the book can lose on the asset (`expiryLiability`, the sum over
  expiry buckets of the worst-case net payout, `exposure/liability.go`).
  Reading of the multiple: with M = 100, a 1% move changes the hedge's value
  by at most the book's whole liability. A book that cannot lose
  (liability 0) gets no hedge. The binding cap is recorded (`cappedBy`).

When the target is zero the reason is recorded (`HedgeTargetDetail.reason`:
disabled, below threshold, no price, no volatility, nothing outside the gamma
window, zero after caps).

### 3.3 Near expiry: the gamma window

Trades with fewer than `minSecondsToExpiryForHedge` seconds left (risk
config, default 10) contribute nothing to D (`LegDelta` returns not
eligible). Their outcome is effectively decided, and their delta swings
between 0 and P / (sigma sqrt(tau)) faster than an order can round-trip; a
hedge placed for them would buy the move after it happened. The window also
bounds the delta of every counted trade by P x phi(0) / (sigma sqrt(window)).

This replaces the previous rule "no hedge at all while the nearest expiry is
within N seconds", which on a busy book (a trade expiring every second)
would have frozen or unwound the hedge permanently.

### 3.4 Volatility

`risk.hedgeSigma`: the pricing engine's estimate - `risk.HedgeSigmaSource`
is installed at server start (`pricinghedge.Install`, section 7.4) and
returns `pricing.Sigma`: the EWMA (source `pricing-ewma`), else the
instrument's calibrated baseline (`pricing-baseline`). Only an asset with no
pricing rule falls back to the realised volatility of the tick history
(`marketdata.GetRealisedVolatility`, per tick) rescaled to per second by the
observed tick rate (source `realised`). The value and its source are stored
on every decision.

### 3.5 Decision record and replay

Every order stores every input of its decision (`models.HedgeDecisionSnapshot`):
the open trades (legs, up to 2,000 with a truncation flag), price, sigma and
its source, decision time, gamma window, band ratio, boost, asset ceiling,
caps and liability, the target, the venue mid, the INR rate and its time, the
venue filters, the position and order quantities, the per-order clip, the
rebalance minimum and the estimated cost. Volatility and seconds-to-expiry,
previously saved as 0 / empty, are filled.

`hedging.ReplayDecision(snapshot)` recomputes the book delta, the target and
the executable quantity from the stored row alone (no live data);
`GET /admin/hedging/orders/{id}/replay` returns the replay, the stored values
and whether they match. `TestEvaluatePlacesFillsAndReplays` replays a real
decision to the same delta, target and quantity.

## 4. Executable quantity

`hedging.PlanExecution` (`execution.go`):

```
targetQty = target / (100 x inrPerUsdt x venueMid)               base asset (BTC, ETH)
orderQty  = targetQty - filled position                            working orders are not counted
no order  when |orderQty| x mid x rate < asset.minHedgeNotional   rebalance band (default INR 1,000)
qty       = floor(|orderQty| / step) x step                        never rounded up
qty       = min(qty, MARKET maxQty, HEDGE_MAX_ORDER_USDT / mid)     the rest follows on the next pass
no order  when qty < minQty, or qty x mid < minNotional            (a reduce-only order is exempt from minNotional)
reduceOnly when the order only shrinks the position
residual  = targetQty - (position + signed qty)
```

One working order per asset at a time (hedge-lifecycle.md).

## 5. Worked tables (F-07 evidence)

Produced by `TestWorkedHedgeTable`
(`go test ./internal/service/hedging/ -run TestWorkedHedgeTable -v -timeout 5m`).
The test computes the target with the production functions
(`risk.BookDelta`, `risk.CapTarget`, `PlanExecution`, `ComputeCostBps`),
sends each executable order through the real Binance adapter to the fake
venue (`binancefake`: book 60,000.0 / 60,000.1, each order filled in two
fills walking the book one tick, taker fee 0.05%), and reads the fills back.
Fixed inputs: h = 0.95, cap = min(INR 10 lakh, 100 x liability), 88 INR per
USDT, gamma window 10 s, sigma = annual volatility / sqrt(31,536,000).

**Table 1 - one UP trade, stake INR 1,000**

| Scenario | S | Delta-equiv. D (INR) | Liability L (INR) | h x D (INR) | Target (INR) | Capped by | Target qty | Executable qty | Est. cost (INR, bps/side) | Fill avg | Actual cost (INR, bps/side) | Residual delta (INR) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| A1 30 s, 85%, 50%/yr, at the money | 60000.0 | 1,513,403 | 850 | 1,437,732 | 85,000 | LIABILITY_MULTIPLE | 0.01609847 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 1,428,922 |
| A2 60 s, 85%, 50%/yr, at the money | 60000.0 | 1,070,137 | 850 | 1,016,630 | 85,000 | LIABILITY_MULTIPLE | 0.01609847 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 985,657 |
| A3 5 min, 85%, 50%/yr, at the money | 60000.0 | 478,580 | 850 | 454,651 | 85,000 | LIABILITY_MULTIPLE | 0.01609847 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 394,100 |
| A4 30 min, 85%, 50%/yr, at the money | 60000.0 | 195,379 | 850 | 185,610 | 85,000 | LIABILITY_MULTIPLE | 0.01609847 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 110,899 |
| B1 60 s, 85%, calm 30%/yr | 60000.0 | 1,783,562 | 850 | 1,694,384 | 85,000 | LIABILITY_MULTIPLE | 0.01609847 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 1,699,082 |
| B2 60 s, 85%, stressed 100%/yr | 60000.0 | 535,068 | 850 | 508,315 | 85,000 | LIABILITY_MULTIPLE | 0.01609847 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 450,588 |
| C1 60 s, payout 80% | 60000.0 | 1,041,215 | 800 | 989,154 | 80,000 | LIABILITY_MULTIPLE | 0.0151515 | 0.015 | 56 (7.01) | 60000.15 | 40 (5.02) | 962,014 |
| C2 60 s, payout 87% | 60000.0 | 1,081,706 | 870 | 1,027,621 | 87,000 | LIABILITY_MULTIPLE | 0.01647726 | 0.016 | 59 (7.01) | 60000.15 | 42 (5.02) | 997,226 |
| D1 60 s, price 0.1% below strike | 59940.0 | 373,452 | 850 | 354,779 | 85,000 | LIABILITY_MULTIPLE | 0.01611459 | 0.016 | 59 (7.01) | 59940.15 | 42 (5.02) | 289,056 |
| D2 60 s, price 0.1% above strike | 60060.0 | 374,612 | 850 | 355,882 | 85,000 | LIABILITY_MULTIPLE | 0.01608239 | 0.016 | 59 (7.01) | 60060.15 | 42 (5.02) | 290,048 |
| E1 8 s to expiry (gamma window) | 60000.0 | 0 | 850 | 0 | 0 | - | 0 | 0 | 0 (7.01) | - | - | 0 |

**Table 2 - a 60-trade book** (60% UP, stakes INR 500 - 5,000, strikes within
±0.15% of 60,000, expiries 30 s - 30 min, payout 85%; seeded, reproducible)

| Scenario | S | Delta-equiv. D (INR) | Liability L (INR) | h x D (INR) | Target (INR) | Capped by | Target qty | Executable qty | Est. cost (INR, bps/side) | Fill avg | Actual cost (INR, bps/side) | Residual delta (INR) |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| F1 book, price -0.2%, 50%/yr | 59880.0 | 11,938,442 | 129,435 | 11,341,520 | 1,000,000 | MAX_HEDGE_NOTIONAL | 0.18977333 | 0.189 | 698 (7.01) | 59880.15 | 500 (5.02) | 10,942,517 |
| F2 book, at 60,000, 50%/yr | 60000.0 | 26,491,163 | 129,435 | 25,166,605 | 1,000,000 | MAX_HEDGE_NOTIONAL | 0.18939378 | 0.189 | 699 (7.01) | 60000.15 | 501 (5.02) | 25,493,242 |
| F3 book, price +0.2%, 50%/yr | 60120.0 | 7,415,467 | 129,435 | 7,044,694 | 1,000,000 | MAX_HEDGE_NOTIONAL | 0.18901575 | 0.189 | 701 (7.01) | 60120.15 | 502 (5.02) | 6,415,551 |
| F4 book, at 60,000, stressed 100%/yr | 60000.0 | 14,779,751 | 129,435 | 14,040,763 | 1,000,000 | MAX_HEDGE_NOTIONAL | 0.18939378 | 0.189 | 699 (7.01) | 60000.15 | 501 (5.02) | 13,781,830 |
| F5 book, at 60,000, calm 30%/yr | 60000.0 | 38,056,633 | 129,435 | 36,153,802 | 1,000,000 | MAX_HEDGE_NOTIONAL | 0.18939378 | 0.189 | 699 (7.01) | 60000.15 | 501 (5.02) | 37,058,713 |

Residual delta = D - filled hedge notional: what stays unhedged by policy
(1 - h, the caps) and by the venue's quantity step.

**Table 3 - platform result at expiry if the price moves to S_T** (INR; book
= the binary payoffs at S_T, hedge = filled notional x move)

| Case | Move | Book | Hedge (default caps) | Net (default caps) | Hedge (uncapped h x D) | Net (uncapped) |
|---|---|---|---|---|---|---|
| A2 one 60 s trade | -1.0% | 1,000 | -845 | 155 | -10,166 | -9,166 |
| A2 one 60 s trade | -0.5% | 1,000 | -422 | 578 | -5,083 | -4,083 |
| A2 one 60 s trade | -0.1% | 1,000 | -84 | 916 | -1,017 | -17 |
| A2 one 60 s trade | +0.1% | -850 | 84 | -766 | 1,017 | 167 |
| A2 one 60 s trade | +0.5% | -850 | 422 | -428 | 5,083 | 4,233 |
| A2 one 60 s trade | +1.0% | -850 | 845 | -5 | 10,166 | 9,316 |
| F2 60-trade book | -1.0% | 76,890 | -9,979 | 66,911 | -251,666 | -174,776 |
| F2 60-trade book | -0.5% | 76,890 | -4,990 | 71,900 | -125,833 | -48,943 |
| F2 60-trade book | -0.1% | 65,050 | -998 | 64,052 | -25,167 | 39,883 |
| F2 60-trade book | +0.1% | -46,690 | 998 | -45,692 | 25,167 | -21,523 |
| F2 60-trade book | +0.5% | -51,315 | 4,990 | -46,325 | 125,833 | 74,518 |
| F2 60-trade book | +1.0% | -51,315 | 9,979 | -41,336 | 251,666 | 200,351 |

What the tables show:

1. **Short binaries have very large deltas.** One INR 1,000 trade at 60 s is
   worth about INR 10.7 lakh of BTC sensitivity at the money (A2); shorter
   expiries and calmer markets make it larger (A1, B1), moving away from the
   strike makes it smaller (D1, D2). A book of 60 such trades reaches INR 2.6
   crore (F2) against a liability of INR 1.3 lakh.
2. **The caps bind almost always** with the defaults: the single trade is
   limited to 100 x its INR 850 liability, the book to the INR 10 lakh asset
   maximum. The hedge is therefore a deliberately partial, tail-oriented
   hedge of short-dated books; the exposure and liability limits and the
   pricing margin remain the primary controls.
3. **Why the caps exist (Table 3).** A binary's payoff is a step; a linear
   hedge sized to the full delta matches it only for small moves. Uncapped,
   a 1% move against the hedge turns the single trade's INR 1,000 win into a
   INR 9,166 loss, and the book's INR 76,890 win into a INR 1.75 lakh loss -
   the hedge becomes the larger risk. With the default caps the hedge reduces
   the loss when the book loses (+1%: INR -5 instead of -850) and never
   loses more on a 1% move than the book's liability.
4. **Costs.** The estimate (7.01 bps per side: 5 bps fee, 0.008 bps half
   spread, 2 bps slippage) exceeds what the fake venue charged (5.02 bps:
   fee plus one tick); this difference is the cost model error the
   Hedging page tracks on real fills. At the money, hedging the full entry
   delta of a 60 s trade for one round trip would cost about
   1,016,630 x 14 bps = INR 1,423 on an INR 1,000 stake: delta-hedging
   short binaries trade by trade is uneconomic, which is what the pricing
   integration reports (section 7).

The owner decides h per band, `targetHedgeRatio`, `maxHedgeNotional` and the
liability multiple; the tables can be regenerated for any proposed values by
editing the constants at the top of `worked_table_test.go`.

## 6. INR per USDT

`hedging.CurrentFX` (`fx.go`), pure core `SelectFxRate`:

- Sources: `HEDGE_INR_PER_USDT` with `HEDGE_INR_PER_USDT_AS_OF` (the
  observation time is required when a rate is set - boot fails otherwise),
  and rates entered on the Hedging page (`PUT /admin/hedging/fx-rate`,
  `hedge:write`, audited as `hedge.fx_rate_set`, stored append-only in
  `hedge_fx_rates`). The newest observation wins.
- **Stale** when older than `HEDGE_FX_MAX_AGE_MS` (default 24 h); **missing**
  when neither source has a rate. Either way: no hedge order is placed, the
  cost estimate says `FX_STALE` / `FX_MISSING` (pricing widens or refuses),
  the risk assessment names the reason, and a critical alert `fx_stale` is
  raised when a hedge is required. Nothing guesses a rate
  (`TestOutageAndStaleRateMakeHedgingUnavailable`).
- Every order stores the rate that sized it; every fill is converted at the
  rate stored on it (its order's decision rate); funding at the rate in force
  when the payment was recorded (flagged `fxStale` if that rate was stale).

Production needs a rate source: until an automatic feed (for example the
USDT/INR rate the USDT wallet's provider quotes, or a bank reference rate) is
connected, an operator must enter a rate at least daily.

## 7. Cost model and availability (F-10, hedging side)

### 7.1 Formula

`hedging.ComputeCostBps` (pure), basis points of hedged notional:

```
perSide    = takerFee + halfSpread + slippage
roundTrip  = 2 x perSide + funding
takerFee   = the account's taker rate from /fapi/v1/commissionRate, else HEDGE_TAKER_FEE_BPS (5)
halfSpread = (ask - bid) / mid / 2 from the venue book, else HEDGE_FALLBACK_SPREAD_BPS / 2
slippage   = HEDGE_SLIPPAGE_BPS (2)
funding    = |lastFundingRate| x holdSeconds / 28,800            charged as a cost, never as income
```

### 7.2 Availability

`hedging.CostEstimate(ctx, assetID, notional)` returns `HedgeCost`
(`available`, `expensive`, `reason`, the bps components, `costInr`, the rate
and venue ages). It reads only Redis and a five-second cache, never the
venue, so it is safe on every price quote. In order:

| Check | Reason when it fails |
|---|---|
| Asset has a venue symbol (`HEDGE_SYMBOLS`) | `NOT_HEDGEABLE` |
| `hedgeEnabled` | `HEDGE_DISABLED` |
| Venue can trade: keys set; live needs `HEDGE_LIVE_EXECUTION_ENABLED` | `HEDGE_NOT_CONFIGURED`, `LIVE_EXECUTION_DISABLED` |
| INR rate present and fresh | `FX_MISSING`, `FX_STALE` |
| Venue snapshot healthy and younger than `HEDGE_VENUE_MAX_AGE_MS` (15 s) | `VENUE_UNAVAILABLE`, `VENUE_STALE` |
| Round trip within `HEDGE_MAX_COST_BPS` (30) | available but `expensive`, reason `COST_LIMIT` |

The venue snapshot (book, mark, funding rate, fee rate, filters) is written to
Redis by the hedge leader every 3 s (`RefreshVenueSnapshot`); a failed read is
stored as a failure, so all instances see an outage at once.

### 7.3 Per-trade estimate and model error

`hedging.EstimateTradeHedgeCost(ctx, TradeCostInput)` is the hedge a trade
causes: required only when the asset is hedged, the trade grows the book's
imbalance (`NetIncrease > 0`), the post-trade utilisation is in a band above
the threshold and the expiry is outside the gamma window. Then

```
entryDelta    = P x phi(-v/2) / v,   v = sigma sqrt(max(expiry, gamma window))
hedgeNotional = min( h x entryDelta , HEDGE_MAX_LIABILITY_MULTIPLE x profit )
costPerStake  = hedgeNotional x roundTrip / 10,000 / stake       (round trip held for the expiry)
```

The second bound follows from the target cap: one trade raises the asset's
liability by at most its profit, so it can raise a liability-capped target by
at most the multiple times that. It is **unavailable** (`HEDGE_CAPACITY`) when
the asset's target is at its maximum hedge notional and the trade pushes the
same way - its delta would stay unhedged - and `NO_VOLATILITY` without a
sigma. The estimate is a lower bound: re-hedging as the delta changes costs
more. With the defaults (M = 100, 14 bps round trip) a INR 1,000 trade at
85% adds at most INR 85,000 of hedge, about INR 119 of cost: 11.9 payout
points, more than the platform margin - delta-hedging short binaries trade by
trade is uneconomic, which is why section 7.4 starts with the configured
hedge cost.

Realised cost per order is measured from its fills (`realisedCostBps` = fill
price vs the sizing mid, plus fees, per side). The Hedging page shows the
average of realised minus estimated per-side cost over the last 50 filled
orders (`costModelErrorBps`, `GetHedgeStatus`): the evidence for tuning
`HEDGE_SLIPPAGE_BPS`.

### 7.4 Plugging into pricing (installed)

**Installed.** `server-go/internal/service/pricinghedge/pricinghedge.go`
implements the adapter below (`Estimator`, `Sigma`) and
`server-go/cmd/server/main.go` calls `pricinghedge.Install()` at start, before
the workers. Tests: `TestEstimatorMapsHedgingVerdicts`,
`TestHedgeStateDrivesThePayout` (venue outage, stale INR rate and a cost over
`HEDGE_MAX_COST_BPS` each make pricing widen by 3 points or refuse per
`pricingHedgeUnavailableAction`; a healthy venue leaves the payout unchanged),
`TestHedgeSigmaIsThePricingVolatility`, `TestInstallWiresBothHooks`
(`internal/service/pricinghedge`). The text below is the design it follows.

The pricing engine's hook is `pricing.HedgeCostEstimator`
(`server-go/internal/service/pricing/hedgecost.go`, owned by the pricing work
and not edited here). As of this writing it receives `HedgeCostRequest{Asset,
Direction, Stake, ExpirySeconds, Liability, Price, Config}` and returns
`HedgeCostEstimate{Required, Available, CostPoints, VenueCostFraction,
Reason, Basis, Provider}`; the engine deducts `hedge_adj = hedge_cost x
expiry_factor`, where `hedge_cost` is `CostPoints` when positive and the
instrument's configured value otherwise, and widens or refuses when a
required hedge is unavailable.

Install an adapter at startup (for example in `cmd/server/main.go` after
`config.Load`, or in a small package importing both `pricing` and
`hedging`). For launch it takes **availability** from the hedging service and
keeps the **configured** hedge cost, reporting the live venue cost next to it:

```go
type hedgeCost struct{}

func (hedgeCost) EstimateHedgeCost(ctx context.Context, r pricing.HedgeCostRequest) (pricing.HedgeCostEstimate, error) {
	out := pricing.HedgeCostEstimate{Available: true, Basis: "configured hedge_cost; availability and venue cost from the hedging service"}
	if r.Asset == nil || !r.Asset.HedgeEnabled {
		return out, nil // not hedged: nothing required
	}
	out.Required = true
	c, err := hedging.CostEstimateFor(ctx, hedging.CostQuery{AssetID: r.Asset.ID.Hex(), HoldSeconds: r.ExpirySeconds})
	if err != nil {
		return pricing.HedgeCostEstimate{}, err // pricing treats an error as "hedge unavailable"
	}
	out.Available = c.Available && !c.Expensive // NOT_HEDGEABLE, FX_STALE, VENUE_*, COST_LIMIT ... -> unavailable
	out.Reason, out.Provider = c.Reason, c.Venue
	out.VenueCostFraction = c.RoundTripBps / 10_000
	// CostPoints 0 keeps the configured hedge_cost. Once calibrated on real
	// fills, the model's figure can replace it (section 7.3):
	//   e, _ := hedging.EstimateTradeHedgeCost(ctx, hedging.TradeCostInput{AssetID: ..., Direction: r.Direction,
	//       Stake: r.Stake, PayoutPercent: <payout cap %>, NetIncrease: <from the book impact>,
	//       UtilisationPercent: <post-trade hedge utilisation>, ExpirySeconds: r.ExpirySeconds})
	//   if e.Required { out.CostPoints = e.CostPerStake }
	return out, nil
}

// at startup:
pricing.SetHedgeCostEstimator(hedgeCost{})
// optional: the delta uses the pricing engine's volatility instead of the realised one
risk.HedgeSigmaSource = func(ctx context.Context, assetID string) (float64, string, bool) { /* sigma per sqrt(second) */ }
```

`hedging.CostEstimate(ctx, assetID, notional)` (hold 300 s) and
`CostEstimateFor` read Redis and a five-second cache only, so they are safe
on every quote. When the hedge is unavailable or expensive, pricing widens
(`pricingHedgeUnavailableAdj`) or refuses (`pricingHedgeUnavailableAction =
REFUSE`) - this is the "risk side reduces capacity": while the venue, the rate
or the cost is not acceptable, trades on hedged assets carry a wider margin
or are not taken. The risk assessment also names the reason ("hedge required
but unavailable: ...") through `risk.HedgeAvailability`, which the hedging
package installs itself.

## 8. Unavailable or expensive hedging

| Condition | Hedge worker | Cost estimate (pricing) | Risk assessment | Alert |
|---|---|---|---|---|
| Venue down / snapshot stale | no new order; working orders queried until reachable | `VENUE_UNAVAILABLE` / `VENUE_STALE` | reason "hedge required but unavailable" | `venue_unreachable` from reconciliation |
| INR rate stale or missing | no order | `FX_STALE` / `FX_MISSING` | reason | `fx_stale` (critical) |
| No keys / live switch off | no order | `HEDGE_NOT_CONFIGURED` / `LIVE_EXECUTION_DISABLED` | reason | boot warning |
| Cost above `HEDGE_MAX_COST_BPS` | orders still placed (the hedge is a risk decision) | `expensive`, `COST_LIMIT` | - | - |
| Target at a cap | order up to the cap | `HEDGE_CAPACITY` for trades pushing the same way | `cappedBy` shown | - |
| Market data feed STALE / OFFLINE | no order | - | existing feed reasons | - |

## 9. Residual risks

- **Gamma and jumps.** A linear hedge cannot replicate a step payoff; between
  rebalances (worker tick 1 s, full pass 15 s) and inside the gamma window the
  book is effectively unhedged. The caps keep the hedge's own loss bounded
  (Table 3).
- **Partial hedge by design.** With the default caps most of a short-dated
  book's delta stays unhedged (Tables 1-2, "Residual delta"); exposure and
  liability limits remain the primary control.
- **Basis.** Settlement uses Binance spot, the hedge is the perpetual; a basis
  move between sizing and settlement is unhedged (typically a few bps).
- **Correlation.** BTC and ETH are hedged separately; no cross-asset netting.
- **Liquidity.** Orders are MARKET; the slippage input is a configured
  estimate until calibrated on real fills; very large targets are worked in
  clips of `HEDGE_MAX_ORDER_USDT`.
- **Churn at the threshold.** The target drops to 0 below the hedge threshold;
  a book oscillating around it would open and close hedges. The rebalance band
  damps small changes; a hysteresis band is a candidate improvement.
- **INR rate.** Manual until an automatic source is connected.
- **Disabled hedging** stops new orders but does not flatten an existing
  position; an operator closes it deliberately.

## 10. Configuration

| Setting | Default | Meaning | Production |
|---|---|---|---|
| `HEDGE_PROVIDER` | `paper` | `paper` (in-process) or `binance`; `broker`/`lp` were removed and fail at boot | `binance` |
| `HEDGE_BINANCE_ENV` | `testnet` | `testnet` or `live`; picks the base URL | `testnet`, then `live` |
| `HEDGE_API_URL` | (empty) | base URL override; boot fails if it contradicts the environment, live requires https | empty |
| `HEDGE_API_KEY`, `HEDGE_API_SECRET` | (empty) | futures API key and HMAC secret of the hedge sub-account | from the secret store |
| `HEDGE_LIVE_EXECUTION_ENABLED` | `false` | orders reach the **live** exchange only when true; live + true without keys fails at boot | `true` only at go-live |
| `HEDGE_SYMBOLS` | `BTC/USD=BTCUSDT,ETH/USD=ETHUSDT` | the only hedgeable assets | unchanged |
| `HEDGE_INR_PER_USDT`, `HEDGE_INR_PER_USDT_AS_OF` | (empty) | configured rate and its observation time | a fresh rate |
| `HEDGE_FX_MAX_AGE_MS` | 86,400,000 | rate staleness limit | 86,400,000 or less |
| `HEDGE_TAKER_FEE_BPS` | 5 | fee when the venue does not report it | 5 |
| `HEDGE_SLIPPAGE_BPS` | 2 | slippage per side | calibrate on fills |
| `HEDGE_FALLBACK_SPREAD_BPS` | 1 | spread when the book is unknown | 1 |
| `HEDGE_MAX_COST_BPS` | 30 | round trip above it = expensive | owner decision |
| `HEDGE_MAX_LIABILITY_MULTIPLE` | 100 | cap: multiple of expiry liability (0 = off) | owner decision |
| `HEDGE_MAX_ORDER_USDT` | 50,000 | largest single order | owner decision |
| `HEDGE_RECONCILE_INTERVAL_MS` | 60,000 | position and funding reconciliation | 60,000 |
| `HEDGE_RECV_WINDOW_MS` | 5,000 | Binance recvWindow (max 60,000) | 5,000 |
| `HEDGE_REQUEST_TIMEOUT_MS` | 10,000 | HTTP timeout per venue call | 10,000 |
| `HEDGE_VENUE_MAX_AGE_MS` | 15,000 | venue snapshot staleness limit | 15,000 |
| `HEDGE_INTERVAL_MS` | 1,000 | hedge worker tick | 1,000 |
| risk config `hedgeLevels`, `hedgeThresholdPercent`, `volatilityHedgeBoost`, `minSecondsToExpiryForHedge` | spec defaults | bands, threshold, boost, gamma window | owner decision |
| asset `hedgeEnabled`, `targetHedgeRatio`, `maxHedgeNotional`, `minHedgeNotional` | false, 0.5, INR 10 lakh, INR 1,000 | per-asset switch, h ceiling, cap, rebalance band | BTC/ETH: enable, ceiling 0.95 |

Validation: `config.loadHedge` (`server-go/internal/config/hedge.go`), tests in
`hedge_env_test.go`.

## 11. Going live

1. **Testnet.** Create a Binance futures testnet account, generate an API key
   with futures trading permission, set `HEDGE_PROVIDER=binance`,
   `HEDGE_BINANCE_ENV=testnet`, the key and secret. Set the account to
   one-way position mode and turn "BNB for fees" off (fees must be charged in
   USDT; otherwise alert `fee_not_usdt`).
2. Enter an INR rate (Hedging page) or set `HEDGE_INR_PER_USDT` with its time.
3. Enable hedging on BTC/USD and ETH/USD (asset page) with the agreed
   `targetHedgeRatio`, `maxHedgeNotional`, `minHedgeNotional`.
4. Run on testnet: the Hedging page must show the venue OK, reconciliation OK
   (venue position = ledger position), fills, fees and funding; compare the
   cost model error; replay a few decisions (`/replay`).
5. **Live.** Create a dedicated live sub-account with only futures trading
   permission, no withdrawals, IP allow-listed to the VPS; fund it with the
   agreed USDT margin; set `HEDGE_BINANCE_ENV=live`, the live key and secret
   (from the secret store, never committed) and, last,
   `HEDGE_LIVE_EXECUTION_ENABLED=true`. The server refuses to boot in live
   mode with the switch on and no keys.
6. Watch the first orders: reconciliation OK, no open alerts, realised costs
   near the estimate.

Rollback: `HEDGE_LIVE_EXECUTION_ENABLED=false` (orders refused at once;
working orders are still queried and cancellable) or disable hedging per
asset.

## 12. Tests

| Test | Package | Proves |
|---|---|---|
| `TestLegDeltaMatchesClosedForm`, `TestGammaWindowExcludesNearExpiry`, `TestOffsettingBookHasNoDelta`, `TestHedgeRatioReplacesTimesTwo`, `TestCapTarget`, `TestAssessRiskHedgeSizing`, `TestBelowThresholdTargetsFlat`, `TestUnavailableHedgeAddsReason` | `internal/service/risk` | delta formula, gamma window, h without the x2, caps, reasons |
| `TestWorkedHedgeTable` | `internal/service/hedging` | the tables in section 5 |
| `TestPlanExecution` | `internal/service/hedging` | quantity step, rebalance band, reduce-only, minimum notional, clips |
| `TestComputeCostBps`, `TestSelectFxRate`, `TestEntryDelta` | `internal/service/hedging` | cost formula, rate choice and staleness |
| `TestEvaluatePlacesFillsAndReplays` | `internal/service/hedging` | decision inputs stored, replay matches |
| `TestOutageAndStaleRateMakeHedgingUnavailable`, `TestTradeHedgeCostForPricing` | `internal/service/hedging` | availability and per-trade cost for pricing |
| `TestEstimatorMapsHedgingVerdicts`, `TestHedgeStateDrivesThePayout`, `TestHedgeSigmaIsThePricingVolatility`, `TestInstallWiresBothHooks` | `internal/service/pricinghedge` | the installed adapter: hedging state -> pricing widens or refuses; one sigma for hedge and payout |
| `TestSignMatchesBinanceDocumentedExample`, `TestBinanceAdapterAgainstFake`, `TestLiveVenueRefusesWithoutSwitchOrKeys`, `TestUnknownOutcomes`, `TestBadSecretIsRejected`, `TestDecimalE8` | `internal/service/hedging/providers` | HMAC-SHA256 signing (Binance's documented vector), adapter, live switch |
| `TestHedgeDefaults`, `TestHedgeLiveRefusesToBootWithoutKeys`, `TestHedgeEnvironmentsCannotBeMixed`, `TestHedgeRateNeedsItsTime`, `TestRemovedHedgeAdaptersAreRefused` | `internal/config` | configuration validation |

Commands (no real network; Mongo and Redis are the isolated test stores):

```
export MONGO_URI="mongodb://127.0.0.1:27120/bt_test_hedge?replicaSet=rs0" REDIS_URL="redis://localhost:6390"
go test ./internal/service/hedging/... -timeout 5m
go test ./internal/service/risk/ ./internal/config/ -timeout 5m
```
