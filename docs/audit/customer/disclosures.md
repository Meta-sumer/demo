# Customer disclosures: how your trade is settled and how "Sell now" works (F-11, F-12)

**Findings:** F-11 - the expiry-price fallback needs an explicit policy; F-12 -
the early-close model and lifecycle need remediation. This document is the
customer-facing text those findings ask for.

**How to use this document.** Part A is the plain-language text for the
apps, the help pages and the terms. Part B gives the source of every number,
so the text can be checked against the code (`server-go/internal/`, commit
`493abbd`). If a number in the code changes, Part A must change with it.

Payouts are being moved to dynamic pricing by separate work
(`docs/audit/pricing/`). This document therefore says only that the payout
shown when you trade is fixed for that trade.

---

## Part A - Text for customers

### Your trade's prices and times

- **The platform sets the prices, not your device.** When you place a trade,
  the platform takes its own latest market price at the moment it accepts
  your trade. That is your **entry price**. The expiry time is counted from
  the moment of acceptance.
- **Your payout is fixed when your trade is accepted.** The payout percentage
  shown when you trade is the one your trade is settled on, even if the
  payout shown in the app changes afterwards.
  - If your trade wins, you get your stake back plus that profit.
  - If it loses, you lose your stake.

### How the closing (expiry) price is chosen

- The closing price is the **last market price recorded at or before the
  exact expiry moment**. A price recorded after expiry is **never** used.
- That price must be **no more than 2 seconds older** than the expiry moment.
- To make sure no earlier price is still on its way, the platform waits a
  moment (at most 2 seconds after expiry) before it settles.
- Prices whose own timestamp is more than 2 seconds ahead of the time the
  platform received them are ignored.
- Your trade's history keeps the price used, its time and the rule applied, so
  any result can be checked later.

### If no valid closing price exists

- If no valid price was recorded at or within 2 seconds before expiry (for
  example during a market-data outage), the platform keeps checking for up to
  **60 seconds after expiry**.
- If there is still no valid price, **your trade is cancelled ("voided") and
  your full stake is returned**. There is no profit and no loss.
- You receive a notification titled **"Trade voided"**. In your trade history
  the trade is shown as **"Void · refunded"**, with the reason (no valid market
  price at expiry).

### If the closing price equals your entry price (a tie)

- If the closing price is exactly the same as your entry price, the trade is a
  **tie**.
- Under the platform's current rule, **a tie returns your stake** (no profit,
  no loss).
- The rule that applies to each asset is shown in the trade's contract
  information before you trade.
- The rule is **fixed when your trade is accepted**, like your payout. If the
  platform changes it later, the change applies only to trades placed after
  it.

### "Sell now" (closing a trade early)

- **What it is.** While a trade is open you can sell it back to the platform
  before expiry, for the amount shown on the **Sell now** button.
- **How the amount is worked out.** The amount is the platform's estimate of
  what your trade is worth at this moment. It starts from your full possible
  payout (stake plus profit) and multiplies it by the estimated chance that
  your trade will win. That estimate is based on:
  - the current price compared with your entry price;
  - the time left;
  - how much the price has been moving recently.

  The estimate is adjusted using how trades like yours actually ended on past
  market data (for example, close to expiry a price that has moved away from
  your entry price usually stays on that side). A **5% fee** is then
  deducted.
- **The estimate is never certain.** The estimated chance is always kept
  between 0.05% and 99%. So selling a trade that is winning always pays
  **less** than holding it to expiry and winning. Selling a losing trade may
  still return part of your stake.
- **A quote is valid for 10 seconds.** The amount on the button is a quote.
  When you press Sell, the platform prices your trade again on the latest
  market price:
  - if the new amount is **higher**, you receive the higher amount;
  - if the new amount is **more than 5% lower** than the quote you saw, the
    sale is refused and you are shown the new amount, so you can decide again;
  - if your quote is older than 10 seconds, you are asked to check the latest
    price and try again.
- **When Sell now is not available:**
  - in the **last 10 seconds before expiry** (the current setting; it can never
    be shorter than 1 second). Sell now is never possible at or after expiry:
    the trade then settles on its own;
  - when the market data for the asset is not healthy (offline or delayed), or
    the latest price is more than 1 second old;
  - when there is not enough recent market data to value your trade;
  - when trading is paused on the platform or on this instrument, or Sell now
    has been switched off (your trade still settles at expiry as usual);
  - when the trade is no longer open.

  The button tells you the reason when it is unavailable.
- **One result per trade.** A trade can be sold early or settled at expiry,
  never both, and its money moves exactly once.

### Practice (DEMO) account

All of the rules above apply in the same way to the practice account, with
practice money.

---

## Part B - Where each number comes from

| Statement in Part A | Value | Code |
|---|---|---|
| Entry price is the platform's latest price at acceptance; times are set by the server | - | `service/binary/binary.go` `acceptTrade` (feed gate, `entryTimestamp`, `expiryTimestamp`) |
| The payout is fixed for the trade | `payoutPercent` and `profit` stored on the trade | `acceptTrade`; `models/binary_trade.go` |
| Closing price = latest tick at or before expiry; never a tick after expiry | Rule `TICK_AT_OR_BEFORE_EXPIRY` | `service/settlement/price.go` `DecideExpiryPrice`; test `TestDecideExpiryPrice_NeverUsesPostExpiryTick` |
| No more than 2 s older than expiry | `MaxTickAgeMs = 2000` (5000 at `493abbd`; lowered to the pricing specification's 2 s settlement tolerance, see `pricing/pricing-spec.md` §9) | `service/settlement/policy.go` `DefaultPolicy` |
| Wait at most 2 s after expiry for confirmation | `ConfirmationWindowMs = 2000` | `DefaultPolicy`; `price.go` `ExpiryConfirmed` |
| Ignore prices stamped more than 2 s ahead of receipt | `MaxClockSkewMs = 2000` | `DefaultPolicy`; `price.go` `clockTrusted` |
| Keep checking up to 60 s after expiry | `DeferWindowMs = 60000`, retried every `RetryDelayMs = 1000` | `DefaultPolicy`; `service/settlement/settlement.go` `settleOpen` |
| Then void and return the stake | `NoPriceRule = VOID_REFUND` (default); rule `NO_PRICE_VOID_REFUND`; status `DRAW` (kept for older apps); ledger `TRADE_REFUND`; the evidence records why no price existed (`noPriceReason`: `FEED_GAP`, `HISTORY_UNAVAILABLE`, `NO_TICK_IN_WINDOW`) | `models/settlement_evidence.go` (`DefaultSettlementNoPriceRule`); `settlement.go` `settleOpen`; `nopricereason.go`; `execute.go` `expiryIntent` |
| "Trade voided" notification | Title "Trade voided" | `service/settlement/execute.go` `afterFinal` |
| Shown as "Void · refunded" with the reason | trade view `voided: true`, `voidReason`, `expiryPriceSource` (additive) | `models/binary_trade.go` `ToTradeView`; `client|mobile/src/utils/tradeStatus.ts` `tradeLabel`, `voidReason`; `admin/src/utils/tradeStatus.ts`; tests `tests/void-and-scope.test.ts` (client, mobile), `admin/tests/void-status.test.ts`, `TestPolicyComesFromTheVersionedRiskConfig` |
| Price, time and rule kept for checking | `settlementIntent.evidence`, `settlements.evidence` | `models/settlement_evidence.go` `SettlementEvidence`; `price.go` `ReplayExpiryResult` |
| Tie returns the stake | `drawPolicy = REFUND` (default; `LOSS` and `WIN` are configurable) | `models/risk_config.go` `DefaultRiskConfig`; `settlement.go` `ApplyDrawPolicy` |
| The tie rule is shown per asset before trading | `drawPolicy` on every asset in `GET /assets` | `api/assets/routes.go`; `client/src/features/trade/ContractInfo.tsx`, `drawRule.ts` |
| The tie rule is fixed at acceptance | `drawPolicy` stored on the trade by `acceptTrade` (step 7c) and used at settlement; trades accepted before it was stored use the policy in force at settlement | `service/binary/binary.go` `acceptTrade`; `service/settlement/settlement.go` `DrawPolicyFor`, `settleOpen`; recorded as `evidence.drawPolicy`; tests `TestAcceptanceFreezesTheDrawPolicy`, `TestDrawPolicyForFallsBackForOldTrades` (`internal/service/binary`) |
| Sell-now amount = full payout × estimated chance × (1 − fee) | `value = round(stake × (1 + payout%) × P(win) × (1 − fee))` | `service/binary/earlyclose.go` `QuoteEarlyClose` |
| Chance kept between 0.05% and 99% | lower clamp `earlyCloseMinProbabilityPercent` = 0.05 (risk configuration; was a fixed 1% before F-12, chosen from the evidence in `pricing/early-close.md` §5.5), upper `maxProbability = 0.99` | `earlyclose.go` `QuoteEarlyClose`; `models/risk_config.go` `DefaultEarlyCloseMinProbabilityPercent` |
| Estimate uses price vs entry, time left and recent movement | Realised volatility over the last 60 s, rescaled per second | `service/binary/earlyclose_service.go` `volatilityPerSecond`, `priceEarlyClose` |
| Estimate adjusted on how past trades ended | Calibrated correction per instrument, time left and volatility (volatility scale and floor, "stickiness"), fitted on Binance data | `earlyclose.go` `EarlyCloseCorrection`; `models.DefaultEarlyCloseCalibration` (risk configuration `earlyCloseCalibration`); `pricing/early-close.md` |
| 5% fee | `earlyCloseFeePercent = 5` (default; admins can set 0-50) | `models/risk_config.go` `DefaultRiskConfig`; `api/admin/core.go` `parseRiskPatch` |
| Quote valid 10 s | `EarlyCloseQuoteMaxAgeMs = 10000` | `policy.go` `DefaultPolicy`; `earlyclose_service.go` `checkQuoteValidity`; test `TestCloseEarlyRequiresARecentQuote` |
| Refused if more than 5% below the quote; a higher value is paid | `EarlyCloseQuoteTolerancePercent = 5` | `checkQuoteValidity`; test `TestCloseEarlyBoundsTheRequoteAgainstTheShownQuote` |
| Not in the last 10 s; never under 1 s; never at or after expiry | `minSecondsRemainingForEarlyClose = 10` (default; admins can set 0-3600); hard floor `EarlyCloseHardFloorMs = 1000`; the claim is conditional on the **database clock** | `models/risk_config.go`; `policy.go` `EarlyCloseFloorMsFor`; `execute.go` `ClaimEarlyClose`; test `TestCloseEarlyImpossibleAtOrAfterExpiry` |
| Refused when the feed is not healthy | Same feed gate as new trades (OFFLINE always; STALE when `haltTradingOnStaleFeed`, default on; DEGRADED when `haltTradingOnDegradedFeed`, default off) | `earlyclose_service.go` `priceEarlyClose`; test `TestCloseEarlyRefusesMissingOrStaleFeed` |
| Refused when the latest price is more than 1 s old | `EarlyCloseMaxTickAgeMs = 1000` | `policy.go`; `priceEarlyClose` |
| Refused without enough recent data | Volatility of 0 (fewer than 3 ticks in 60 s, or no movement) | `priceEarlyClose`; `service/marketdata/marketdata.go`; test `TestCloseEarlyRefusesZeroVolatility` |
| Refused while trading is halted, globally or on the trade's symbol, or Sell now is switched off | `tradingHalted`, `pricingHalts` (`pricing.HaltFor`), `earlyCloseEnabled` | `priceEarlyClose`, `CloseTradeEarly`; tests `TestCloseEarlyRefusedWhileTradingHalted`, `TestCloseEarlyRefusedWhileSymbolHalted` |
| One result per trade; money moves once | One conditional claim per trade; ledger key `trade:settle:<id>` | `execute.go`; tests `TestEarlyCloseAndExpiryAreMutuallyExclusive`, `TestConcurrentDuplicateClosesMoveMoneyOnce` |
| Same rules for DEMO | - | DEMO trades go through the same paths (`TestDemoTradeSettlesWithoutExposure`) |

### Notes for the product team

1. **Settlement values are versioned risk configuration.** The defaults are
   the values in Part A:
   - expiry price: `settlementMaxTickAgeMs` 2,000, `settlementConfirmationWindowMs`
     2,000, `settlementDeferWindowMs` 60,000, `settlementRetryDelayMs` 1,000,
     `settlementMaxClockSkewMs` 2,000, `settlementNoPriceRule` `VOID_REFUND`;
   - Sell now: `earlyCloseMaxTickAgeMs` 1,000, `earlyCloseQuoteMaxAgeMs`
     10,000, `earlyCloseQuoteTolerancePercent` 5, `earlyCloseFloorMs` 1,000,
     plus the fee (5%) and the cut-off seconds (10 s).

   They are fields of `models.RiskConfig`, edited on the admin **Settings**
   page (*Settlement policy*, *Early close*) through `PUT /admin/settings/risk`;
   each change is a new configuration version and an audit entry, and every
   settlement stores the version and the effective values it used
   (`evidence.policy`, `evidence.riskConfigVersion`). `settlement.PolicyFor`
   still reads them by field name from the version in force, and a stored
   version without them uses the defaults. Hard limits are enforced
   (`service/riskconfig/policy_validate.go`): the clock-skew guard cannot be
   switched off, the Sell-now floor cannot go under 1 s, the no-price rule is
   one of the two above (neither ever uses a post-expiry tick), the defer
   window is at least the confirmation window, and the tick history must
   cover the defer window plus 5 minutes. Tests:
   `TestSettlementPolicyAndFeedRulesValidation`, `TestPolicyPatchApply`
   (`internal/service/riskconfig`), `TestPolicyComesFromTheVersionedRiskConfig`
   (`internal/service/settlement`). **If an admin changes any of them, update
   Part A.**
2. **The cut-off is 10 s, not 1 s.** The interim audit response says Sell now
   is "impossible within 1 second of expiry". That 1 s is the hard floor; with
   the default setting, the cut-off customers experience is **10 seconds**.
   Part A states both.
3. **The apps show voids as voids.** The web app, the mobile app and the
   admin console show a trade voided for want of a price as "Void ·
   refunded" with the reason (the trade view's `voided`, `voidReason`, or for
   an older server `expiryPriceSource = NO_PRICE_VOID_REFUND`); a real tie
   still reads "Draw · refunded", and trades settled before the change read as
   before.
4. **The tie rule is frozen on the trade at acceptance**
   (`acceptTrade` stores `drawPolicy`; `settlement.DrawPolicyFor` settles
   with it). A change of `drawPolicy` applies only to trades accepted after
   it. Trades accepted before the policy was stored settle under the policy
   in force at settlement (there are none left open once the release has
   been live for the longest expiry).
5. **The Sell-now amount is a model estimate, not a market price.** Since
   F-12 its probability is calibrated on Binance history
   ([../pricing/early-close.md](../pricing/early-close.md)), but it remains
   an estimate; avoid wording that presents it as a market price.
6. **The early-close formula may change.** The correction table and the lower
   clamp are risk configuration and are re-fitted from time to time
   (`cmd/calibrate -early-close`). Part A describes the method at a high level
   only; if the lower clamp or the fee changes, update Part A.
