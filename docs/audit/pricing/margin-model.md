# Margin model and trade economics (F-03)

This document answers audit finding F-03: what the platform expects to earn
on a trade, what each part of the payout deduction pays for, when the
expectation turns negative, and the back-test evidence that the engine's
payouts keep it positive. The engine itself is described in
[pricing-spec.md](pricing-spec.md); every number quoted from data comes from
[calibration-report.md](calibration-report.md) (Binance BTCUSDT and ETHUSDT,
1-second klines for the 14 days and 1-minute klines for the 2 years ending
2026-10-16 00:00 UTC).

## 1. One trade

A customer stakes S on UP or DOWN at payout r (the profit fraction: 0.765 =
76.5%). Let p be the probability the trade wins and q the probability of a
draw (expiry price equal to entry price). Under the platform's default draw
policy `REFUND` (`models.DrawPolicy`, the stake is returned on a draw):

| Outcome | Probability | Customer receives | Platform result |
|---|---|---|---|
| win | p | S (1 + r) | - r S |
| draw | q | S | 0 |
| loss | 1 - p - q | 0 | + S |

The platform's expected result per trade is

```
E = S [ (1 - p - q) - p r ]                       (draws refunded)
  = S p ( r* - r ),      r* = (1 - p - q) / p     (break-even payout)
```

Every payout point below break-even earns the platform p points of stake (about
half a point). The other draw policies change only k = P(platform keeps the
stake) and w = P(platform pays): `LOSS` (a draw is lost by the customer):
k = 1 - p, w = p; `WIN` (a draw pays): k = 1 - p - q, w = p + q. The guard
uses the same three cases (`service/pricing/engine.go` `Guard`).

For a fair market (p = (1 - q) / 2 for both directions) break-even is
r* = 1, that is 100%: any payout below 100% has a positive expectation
against a customer with no information. The risk is a customer (or a
situation) with p above one half: momentum, latency, or a regime the payout
does not reflect. The engine's deductions and the guard are there for that.

There is no trading fee on entry. The platform's revenue on a held-to-expiry
trade is E; Sell now (early close) has its own fee and quote model (F-12,
[../customer/disclosures.md](../customer/disclosures.md)).

## 2. What each deduction pays for

The payout is `base - vol_adj - spread_adj - hedge_adj - exposure_adj -
margin_adj` (plus `hedge_unavailable_adj` during a hedge outage), clamped to
[min, max] and rounded down to 0.5 points (`engine.go` `ComputeOffer`).

| Term | Default (BTC/USD, ETH/USD) | Pays for | Where set |
|---|---|---|---|
| `base` | the payout row: BTC 82 / 84 / 87 / 87 / 87, ETH 80 / 83 / 86 / 86 / 86 (30 s / 60 s / 5 / 15 / 30 min) | the starting price for the (asset, expiry) | payout rows, versioned |
| `margin_adj` | 3.5 points | the platform's business margin (operating cost, profit) | `pricingSymbolRules[].margin`, or `marginPercent` on the row |
| `hedge_adj` | 3.5 points x expiry factor (30 s 4.55, 60 s 3.5, 5 min 2.8) | the cost of hedging the book on Binance | `hedgeCost`, or the hedging service's live estimate (pricing-spec §9) |
| `hedge_unavailable_adj` | 3 points, only while a required hedge cannot be placed | carrying the unhedged risk | `pricingHedgeUnavailableAdj` (or `REFUSE`) |
| `vol_adj` | 0 in calm markets, up to 12 points | model and gap risk when volatility is above baseline | `pricingKVol`, `pricingMaxVolAdj`; block above 4x |
| `spread_adj` | about 0 for BTC and ETH (one-tick spreads), up to 5 points | a wide market: the entry mid is less certain relative to the move | `pricingKSpread`, `pricingMaxSpreadAdj` |
| `exposure_adj` | 0 on an empty book, up to 10 points | concentration: the side already loaded pays more | `pricingKExposure`, `pricingMaxExposureAdj` |
| rounding | 0 to 0.5 points | always in the platform's favour (floor) | `pricingPayoutStep` |
| guard edge | at least 1% of stake at the model's probabilities, or the offer is refused | estimation uncertainty of p | `pricingGuardMinEdgePercent`, `pricingGuardConservative` |
| capital | not a deduction: the worst-case loss per side is capped | the capital at risk if every open trade on one side wins | `pricingSymbolRules[].maxExposure`, bucket limits |

The deductions are not charged twice for the same cost: hedge cost is only
in `hedge_adj`; the business margin only in `margin_adj`; uncertainty in
`vol_adj` (volatility above normal) and the guard (probability estimate); the
min and max bounds are hard limits, not costs.

### 2.1 Hedge cost against the venue

The default estimator charges the configured 3.5 points and reports the
Binance venue basis: per side taker fee 5 bps (USD-M futures, the hedging
venue) + half spread 1 bp + slippage 2 bps, round trip 0.16% of hedged
notional (`service/pricing/hedgecost.go` `defaultEstimate`). The 3.5-point
allowance therefore covers the venue cost of hedging up to about 22 times
the stake in notional per trade, round trip (0.035 / 0.0016). How much
notional is actually hedged is decided by the hedging service on the net
book (F-07), and its realised costs and P&L are reported there (F-09,
[../hedging/hedging-spec.md](../hedging/hedging-spec.md)). That document
estimates that delta-hedging a short binary trade by trade would cost more
than the platform margin (its §7.3: about 11.9 points for a 1,000 stake at
85% with its defaults), which is why hedging works on the net book and the
pricing charge starts from the configured allowance. When the adapter of
its §7.4 is installed (`pricing.SetHedgeCostEstimator`), availability comes
from the hedging service, and `hedge_adj` can follow its per-trade cost once
that model is calibrated on real fills.

### 2.2 Capital

The worst-case loss on one symbol is bounded per side: L(D) = profits on
side D - stakes on the other side <= `maxExposure` (1,250,000.00 per side for
BTC/USD and ETH/USD), checked at quote time and atomically at acceptance
(pricing-spec §5). The existing expiry-bucket, asset, platform and per-user
limits still apply. One trade's result has a standard deviation of about
S (1 + r) sqrt(p (1 - p)), roughly 0.88 S at r = 0.765, against an expected
+0.12 S: over many independent trades the edge dominates (after about 55
equal trades the expected total exceeds one standard deviation of it), but
a single burst of correlated trades on one side can lose up to the side
limit. That loss is what `maxExposure` sizes, and it should be set from the
capital the owner is willing to lose in that scenario.

## 3. Examples: when E is positive and when it is not

E per unit stake, `(1 - p - q) - p r`, with p and q measured on Binance data
(calibration report §5.3 and §6). "Top decile" is the 10% of entries where
the guard model's momentum signal most favours UP; a customer who could pick
exactly those entries is the strongest selection the data shows. Its draw
rate is not reported per decile, so the regime's draw rate is used (0.001 to
0.0013; it moves E by less than 0.1 point).

| # | Case | p | q | payout r | break-even r* | E | Note |
|---|---|---|---|---|---|---|---|
| A | BTC/USD 60 s, UP, normal volatility, engine's calm payout | 0.4911 | 0.0099 | 76.5% | 101.6% | **+12.3%** | report §6 gives 12.32% |
| B | Spec example, fair coin, no draws | 0.5 | 0 | 83.0% | 100% | **+8.5%** | pricing-spec §12.1 |
| C1 | BTC/USD 15 min, top momentum decile, pre-engine fixed 87% | 0.5379 | 0.001 | 87.0% | 85.7% | **-0.69%** | negative |
| C2 | same, engine's calm payout | 0.5379 | 0.001 | 79.5% | 85.7% | **+3.35%** | |
| D1 | ETH/USD 15 min, top momentum decile, pre-engine fixed 86% | 0.5559 | 0.0013 | 86.0% | 79.7% | **-3.53%** | negative |
| D2 | same, engine's calm payout | 0.5559 | 0.0013 | 78.5% | 79.7% | **+0.64%** | positive but thin (95% half-width about 2.6 points) |
| E1 | BTC/USD 30 min, UP, low volatility (the weakest regime) at the 88% maximum | 0.5306 | 0.0022 | 88.0% | 88.1% | **+0.03%** | break-even: the maximum bound is the edge |
| E2 | same, engine's calm payout | 0.5306 | 0.0022 | 79.5% | 88.1% | **+4.54%** | report §3: 4.53% |
| F1 | BTC/USD 30 s, UP, low volatility (many draws, refunded) | 0.4756 | 0.0793 | 73.5% | 93.6% | **+9.55%** | |
| F2 | same at a 98% fixed payout | 0.4756 | 0.0793 | 98.0% | 93.6% | **-2.10%** | negative |

What the examples show:

- Against an uninformed customer the margin is large (A, B): roughly half of
  the gap between the payout and 100%.
- A fixed payout near or above the break-even of a selective customer is
  negative (C1, D1, F2). The pre-engine fixed payouts (82 to 87% for BTC,
  80 to 86% for ETH) were negative against the top momentum decile at 15
  minutes. The engine's payouts (C2, D2) stay positive there, because the
  hedge and margin deductions take 7 points off the base before any risk
  adjustment.
- The maximum bound matters: 88% is break-even in the weakest regime (E1).
  The engine never reaches it with the default bases (82 to 87) and
  deductions (7 to 8 points); an operator who raised a base above 95 could.
  The guard would then refuse what falls below a 1% edge (section 4).
- Case D2 is the thinnest positive case found: a customer who systematically
  picks ETH/USD 15-minute entries in the top momentum decile. Its interval
  includes zero. The default guard (point estimate) lets it through (model
  edge 4.1% at 78.5%); `pricingGuardConservative` refuses it (edge -3.2%
  with the calibrated add-ons). See section 6.

### 3.1 FIXED mode

`pricingMode: FIXED` pays the payout row's percent (the base) with every
check except the formula and the guard (`pricing/service.go` `Evaluate`
returns before the guard in FIXED mode). Its economics are cases C1, D1 and
F2: positive against uninformed flow, negative against selective flow at the
higher bases. FIXED is kept as an operating fallback, not as the launch
configuration.

## 4. The guard: payout below calibrated break-even minus a margin

After the formula, every offer is checked against the calibrated
win-probability model (`engine.go` `Guard`; model and calibration in
pricing-spec §8 and the calibration report §5). With w = P(platform pays)
and k = P(platform keeps the stake) under the draw policy:

```
point edge        = k - w r            >= pricingGuardMinEdgePercent (1%)    default
conservative edge = k_lb - w_ub r      >= 1%                                 pricingGuardConservative
break-even payout = (k - 0.01) / w     (or (k_lb - 0.01) / w_ub)
```

k_lb and w_ub add the uncertainty add-ons calibrated on a holdout period per
volatility tercile (u for the win probability, dq for draws). So the engine
never offers a payout above the model's break-even minus 1% of stake; if the
model cannot vouch for the entry (no calibrated entry, other price source,
too little recent data, volatility far outside calibration) the offer is
refused, never priced on a default probability. Both edges and the
break-even payout are recorded on every offer and trade.

The guard does not set the payout; it refuses. With the default bases and
deductions it rarely binds on the point estimate (0 to 1% of entries, 2.6%
for BTC/USD 30 s); on the conservative bound it would refuse up to about
half of short-expiry entries (table below). That is the trade-off behind the
default: the point estimate blocks the entries where the model sees the
payout too close to break-even, and the conservative bound is available as
a switch for stressed periods.

## 5. Back-test reconciliation

Two independent checks, both in the calibration report:

- **§3, the formula as live**: every second of the 14-day 1-second window
  priced exactly as the engine would (EWMA, defaults, seeded bases, empty
  book, one-tick spread), then booked with its real outcome, draws refunded.
  E per regime (volatility terciles) and direction at the payouts actually
  quoted.
- **§6, the guard's test period**: for 5 to 30 minutes, the last 20% of two
  years of 1-minute data (about 4.8 months, 210,000 entries per expiry); for
  30 and 60 s the last 20% of the 1-second window. E at the formula's calm
  payout, which is its highest offer: E falls as the payout rises, so E at
  the calm payout bounds every offer from below.

| Asset | Expiry | Calm payout | Mean payout quoted (§3) | Blocked by volatility (§3) | Lowest E across regimes and directions (§3) | +/- (95%) | Lowest E at calm payout (§6) | +/- (95%) | §6 lower bound | Guard blocks, point / conservative (§6) |
|---|---|---|---|---|---|---|---|---|---|---|
| BTC/USD | 30 s | 73.5% | 72.74% | 0.49% | 8.34% | 1.28 | 9.55% | 3.28 | 6.27% | 2.59% / 55.2% |
| BTC/USD | 60 s | 76.5% | 75.85% | 0.49% | 8.14% | 1.94 | 7.90% | 4.72 | 3.18% | 0% / 32.6% |
| BTC/USD | 5 min | 80.5% | 79.92% | 0.50% | 6.66% | 4.79 | 8.65% | 1.49 | 7.16% | 0.12% / 3.8% |
| BTC/USD | 15 min | 79.5% | 78.85% | 0.52% | 5.96% | 8.31 | 8.80% | 2.57 | 6.23% | 0.31% / 20.6% |
| BTC/USD | 30 min | 79.5% | 78.85% | 0.52% | 4.53% | 11.76 | 8.58% | 3.64 | 4.94% | 0.26% / 43.2% |
| ETH/USD | 30 s | 71.5% | 70.90% | 0.58% | 11.03% | 1.33 | 11.67% | 3.29 | 8.38% | 0% / 10.3% |
| ETH/USD | 60 s | 75.5% | 74.95% | 0.58% | 9.54% | 2.02 | 10.21% | 4.76 | 5.45% | 0% / 0.02% |
| ETH/USD | 5 min | 79.5% | 79.00% | 0.59% | 8.28% | 4.80 | 9.46% | 1.49 | 7.97% | 0.88% / 6.5% |
| ETH/USD | 15 min | 78.5% | 77.96% | 0.60% | 8.39% | 8.28 | 9.88% | 2.56 | 7.32% | 0.97% / 17.3% |
| ETH/USD | 30 min | 78.5% | 77.96% | 0.60% | 4.64% | 11.70 | 9.32% | 3.62 | 5.70% | 0.71% / 23.4% |

Reading the table:

- **E is positive in every regime and direction** in both checks, at the
  payouts the engine quoted (§3) and at its highest offer (§6). The lowest
  point estimate is 4.53% of stake (BTC/USD 30 min, low volatility, UP).
- With the longer history (§6) the whole 95% interval is above zero for every
  asset and expiry: the lowest lower bound is 3.18% (BTC/USD 60 s).
- The 14-day window (§3) alone is too short to fix the sign for 15 and 30
  minutes (overlapping outcomes leave 222 to 445 effective samples per
  regime, and the interval includes zero); §6 is the evidence for those
  expiries.
- Volatility blocks about half a percent of seconds (bursts), and the
  sensitivity table (report §4) shows the lowest regime E barely moves with
  `k_vol` or the block ratio, because `vol_adj` only ever lowers the payout.
- The back-test uses an empty book and the configured hedge allowance; with a
  loaded book `exposure_adj` lowers payouts further, so E only rises. The
  back-test E is before actual hedging costs and before operating costs:
  it is the budget those must fit in (section 2).

## 6. Residual risk and levers

| Risk | Size in the data | Lever (all versioned configuration) |
|---|---|---|
| Selective customers at 15 to 30 minutes (top momentum decile) | ETH/USD 15 min: E +0.64% +/- 2.6 at the calm payout (case D2) | `pricingGuardConservative: true` (refuses it), or a higher `marginPercent` on the 15 and 30 minute rows, or a higher `pricingGuardMinEdgePercent` |
| A regime not in the data (crash, exchange outage) | not represented | volatility block (4x baseline), stale-data block (2 s), the guard's out-of-calibration refusal, kill switch |
| Baseline volatility drifts | 2-year 1-minute data implies a higher baseline than the recent 14 days (report §2) | re-run `server-go/cmd/calibrate` regularly; change `baselineVol` |
| Actual hedge cost above the allowance | depends on the hedge ratio (F-07) | live estimator via `pricing.SetHedgeCostEstimator`; compare realised hedge cost per stake (F-09) with `hedge_adj` |
| Correlated burst on one side | up to `maxExposure` per symbol and side | `maxExposure`, bucket and platform limits; no cross-instrument limit (BTC and ETH are correlated) |
| Operator raises a base or max | E1: 88% is break-even in the weakest regime | the guard refuses offers below a 1% edge; payout rows and rules are audited |

The model's probabilities and add-ons are estimates on the periods named in
the calibration report, not guarantees; the report should be regenerated
and reviewed before each material change of bases, margins or bounds.

## 7. Code and tests

| What | Where |
|---|---|
| Payout formula | `server-go/internal/service/pricing/engine.go` `ComputeOffer` |
| Terms (base from the row, min, max, margin) | `engine.go` `TermsFor`; `models/risk_config.go` `DefaultPricingSymbolRules`; `models/payout_config.go` |
| Guard (break-even minus margin) | `engine.go` `Guard`; `service.go` `applyGuard` |
| Hedge cost | `hedgecost.go` `DefaultHedgeCostEstimator`, `SetHedgeCostEstimator` |
| Side liability and capital limit | `exposure/sideliability.go`, `exposure/lua.go` (`SIDE_LIABILITY`), `risk/reserve.go` |
| Back-test and evidence | `pricing/evidence.go` `Backtest`, `LongHorizonEvidence`, `CalmPayout`; `server-go/cmd/calibrate` |

Tests: `TestPayoutProperties` (payout within [min, max], on the grid, never
rounded up, lower with more volatility or liability), `TestGuard` (edges,
break-even, refusals), `TestSpecWorkedExample`, `TestVolatilitySpike`,
`TestExposureBlocksTheLoadedSideOnly`, `TestFinalExposureCheckUsesTheRealPayout`
(`go test ./internal/service/pricing/`), and the end-to-end acceptance tests
in `./internal/service/binary/` (`TestFixedModePaysThePayoutRow`,
`TestExposureLimitPerSide`, `TestHedgeOutageWidensOrRefuses`).

To regenerate the evidence:

```
cd server-go
go run ./cmd/calibrate -end 2026-10-16T00:00:00Z -out ../docs/audit/pricing
```

The command downloads the Binance klines (public REST, no key; cached in
`-cache`), and writes the report and also the embedded guard model
(`-model-out`, default `internal/service/pricing/default_model.json`) with
`-version`. A new model reaches running servers only with `-publish` (next
active version in `pricing_models`) or a new build.
