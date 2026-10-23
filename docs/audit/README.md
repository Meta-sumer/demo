# Audit evidence documents

This folder holds the documents written in answer to the independent audit
of the platform (21 findings, F-01 to F-21). Each document:

- is written for the auditor in plain English;
- cites the file and function behind every rule;
- shows its diagrams in Mermaid, each followed by a one-line plain
  description;
- lists the automated tests that prove each path.

Unless a document says otherwise, it describes branch `main` at commit
`493abbd`. The development team's interim response of 17 Oct 2026 gives the
status of each finding and the test results of that date.

## Documents

| Folder | Document | What it covers | Findings |
|---|---|---|---|
| `money/` | [funds-flows.md](money/funds-flows.md) | Funds-flow and state diagrams for manual INR deposits and withdrawals, trade stake lock, release and settle, and admin adjustments (Idempotency-Key, replay); transaction boundaries, idempotency keys, retries, repair endpoints, transfer and ledger reconciliation, tests | F-15 |
| `operations/` | [multi-instance.md](operations/multi-instance.md) | What runs on every instance and what runs on the leader; leader lock and fencing (token and epoch); settlement invariants; retry, backoff and poison entries; SETTLING recovery; queue rebuild from MongoDB; exposure book rebuild, reconcile and safe mode; tests | F-14 (also F-05, F-06) |
| `operations/` | [production-readiness.md](operations/production-readiness.md) | Topology (datastore authentication, log rotation, memory limits), capacity and load targets with the measured results, backups (authenticated, failure email), RPO/RTO, rollback (with the `493abbd` model diff), incident runbooks, SBOM, secret management, environment separation and CI, monitoring and the alert list (email alerting: what fires, thresholds, severity, where it goes), web security headers, on-call and go/no-go sign-off, the one-time rollout steps on the server | F-21 (also F-20) |
| `operations/` | [load-and-failover.md](operations/load-and-failover.md) | The load and retry-storm test through the HTTP API (limits, idempotency, mid-run book rebuild and safe mode, throughput and latency) and the three-process test with a leader crash and restart (settle-once, ledgers, wallets, book): commands, figures, pass/fail, findings | F-05, F-06, F-14 |
| `testing/` | [test-inventory.md](testing/test-inventory.md) | Every Go test function by package, mapped to the findings it gives evidence for, its result in the full run, coverage per package, and what is not tested or tested only against a simulation | F-20 |
| `security/` | [threat-model.md](security/threat-model.md) | Assets, trust boundaries, threats and controls with code references, residual risks | F-19 |
| `security/` | [permission-matrix.md](security/permission-matrix.md) | Roles × permissions, the permission each admin route needs, websocket channels, SUPER_ADMIN-only actions | F-19 |
| `security/` | [secret-scan.md](security/secret-scan.md) | Secret scan of the tracked files, the tracked archive and the git history: commands, findings and their classification | F-19 |
| `customer/` | [disclosures.md](customer/disclosures.md) | Customer-facing terms: how the expiry price is chosen, the void-and-refund rule, ties, Sell now (pricing, fee, cut-off, quote validity, requote limit, when it is refused) | F-11, F-12 |
| `pricing/` | [early-close.md](pricing/early-close.md), [early-close-backtest.md](pricing/early-close-backtest.md) (+ `.json`) | Sell now: price formula, market snapshot and freshness, volatility, fee basis, rounding, bounds, quote validity, cut-off and floor, refusals, disclosure; back-test on Binance data (reliability, Brier, log loss, ECE, platform edge by asset, expiry, volatility regime and probability bin, out of sample, two-year check); the calibrated correction (risk configuration) and its limits | F-12 |
| `risk/` | [liability-and-stress.md](risk/liability-and-stress.md) | Liability model: every term (gross and net stake, potential payout, per-side and per-bucket liability, asset and platform liability, max payout, stress loss, concentration, filled / working / residual hedge) with formula and code; what offsets and what does not; the limits and the atomic check; numerical books and stress scenarios from a test; capital and liquidity buffer with recommended settings; what is not modelled | F-04 |
| `pricing/` | [pricing-spec.md](pricing/pricing-spec.md), [margin-model.md](pricing/margin-model.md), [calibration-report.md](pricing/calibration-report.md) | Dynamic payout engine (market-data normalisation, EWMA volatility, payout formula, per-side liability, kill switch, offers and locked terms, settlement tolerance, audit trail, configuration, worked examples, deviations from the owner's specification); calibrated guard model; trade economics, expected margin and the back-test across volatility regimes; hedge-cost hook | F-01, F-02, F-03, F-10 (pricing side) |
| `usdt/` | [usdt-flows.md](usdt/usdt-flows.md) | USDT deposits and withdrawals through BeeX: routes and permissions, state diagrams, webhook signature and replay protection, provider reconciliation and alerts, limits, workers, configuration and the sandbox switch, sandbox test evidence, what live keys still need | F-16 |
| `hedging/` | [hedging-spec.md](hedging/hedging-spec.md) | Binance USDⓈ-M instrument choice, the book's delta-equivalent target and its caps, executable quantity, INR per USDT, cost model and availability for pricing, worked tables, configuration, going live | F-07, F-10 (hedging side) |
| `hedging/` | [hedge-lifecycle.md](hedging/hedge-lifecycle.md) | Order state machine, idempotency, recovery, venue reconciliation and alerts, fills ledger, execution-based realised P&L with a worked example | F-08, F-09 |
| `operations/` | [metric-dictionary.md](operations/metric-dictionary.md) | Every figure on the Dashboard, Exposure, Hedging and P&L screens: formula, source, refresh, unit, realised/unrealised, working orders, stale/unavailable display | F-18 |
| `operations/` | [feed-rules.md](operations/feed-rules.md) | Feed-health and historical-data rules: one authority for the thresholds (`feedStaleMaxMs` retired), gap detection that tells a gap from a quiet market, dropped and rejected tick counters, clock-sync check, tick-history retention, alerts, what settlement does without data, the launch scope (BTC/USD and ETH/USD) and the production Binance feed, tests | F-17 (also F-11) |

## Findings index

| ID | Finding (short title) | Where the evidence is | Document status |
|---|---|---|---|
| F-01 | Dynamic payout as a complete pricing engine | [pricing-spec.md](pricing/pricing-spec.md) | Written |
| F-02 | Calibrated entry win-probability model | [pricing-spec.md §8](pricing/pricing-spec.md), [calibration-report.md §5](pricing/calibration-report.md) | Written (BTC/USD and ETH/USD only; other instruments not priced) |
| F-03 | Expected margin and trade economics | [margin-model.md](pricing/margin-model.md) | Written |
| F-04 | Stake exposure is not a complete liability model | [liability-and-stress.md](risk/liability-and-stress.md); the book: [multi-instance.md §7](operations/multi-instance.md) | Written (23 Oct 2026); `TestLiabilityStressScenarios` passes: identical stakes priced at 65 / 76.5 / 85% give 25,000 / 36,500 / 45,000 of liability, identical books with different expiries 0 or 160,000, BTC and ETH never offset, every limit refuses the breaching trade atomically and accepts the reducing one. Open for the operator: the liability ceilings are off by default (recommended values and the capital rule in §6); the screens show the netted figure, not the limit measure (§7) |
| F-05 | Atomic risk reservation under concurrent trades | [multi-instance.md §7](operations/multi-instance.md), [funds-flows.md §3.1](money/funds-flows.md), [load-and-failover.md §2](operations/load-and-failover.md) | Written; load test with a retry storm passes (limits never breached, one trade per idempotency key) |
| F-06 | Exposure-counter consistency and boot rebuild race | [multi-instance.md §1, §7](operations/multi-instance.md), [load-and-failover.md §2, §3, §4.2](operations/load-and-failover.md) | Written; book exact after a forced rebuild and safe mode under load; since 23 Oct 2026 releases are by trade id and the book is exact when the last trade finishes after a crash and restart (finding §4.2 fixed, re-run §3.4) |
| F-07 | Hedge target logic and hedge effectiveness | [hedging-spec.md](hedging/hedging-spec.md) | Written (implemented and tested against a fake Binance; real Binance keys and calibration on real fills still needed) |
| F-08 | Pending and partial hedge lifecycle | [hedge-lifecycle.md](hedging/hedge-lifecycle.md) | Written |
| F-09 | Realised hedge P&L | [hedge-lifecycle.md §7](hedging/hedge-lifecycle.md) | Written |
| F-10 | Hedge costs connected to pricing | [hedging-spec.md §7](hedging/hedging-spec.md), [pricing-spec.md §9](pricing/pricing-spec.md) | Written; the estimator is installed at server start (`service/pricinghedge`) and the hedge target uses the pricing engine's sigma |
| F-11 | Expiry-price fallback needs an explicit policy | [disclosures.md](customer/disclosures.md); retries in [multi-instance.md §4](operations/multi-instance.md); missing data in [feed-rules.md §7](operations/feed-rules.md) | Written; the settlement policy is versioned risk configuration with hard limits, edited in the admin console; the tie rule is frozen on the trade at acceptance; voids show as "Void · refunded" in the apps |
| F-12 | Early-close model and lifecycle | [early-close.md](pricing/early-close.md) (rules, back-test, correction), [early-close-backtest.md](pricing/early-close-backtest.md); [disclosures.md](customer/disclosures.md); lifecycle in [multi-instance.md §3, §5](operations/multi-instance.md) and [funds-flows.md §3.2](money/funds-flows.md) | Written (23 Oct 2026). Back-test on Binance 1 s data: the platform edge is positive in every regime (+4.4% to +8.1% of stake), but the uncorrected `Phi(z)` overpaid selective sellers (69 of 718 probability bins significantly negative, worst -58% of stake). A calibrated correction (risk configuration, defaults fitted on the first 60%) leaves 2 of 629 bins significantly negative on the last 40% and cuts the 5-30 min calibration error from 0.037-0.061 to 0.005-0.013; lower clamp 0.05% (was 1%). Limits in §7 (klines not the live mid, 14 days, two residual BTC 30 s bins) |
| F-13 | Payout replacement must be failure-safe | Replacement protocol: `service/payouts/payouts.go` (package comment, tests in `payouts_test.go`); scheduled changes with an effective time: `service/payouts/schedule.go`; rollback note in [production-readiness.md §5.1](operations/production-readiness.md) | Implemented and tested (one active row per asset and expiry at any instant, scheduled activation, cancel); no dedicated document yet |
| F-14 | Worker coordination and recovery across instances | [multi-instance.md](operations/multi-instance.md), [load-and-failover.md §3](operations/load-and-failover.md) | Written; a test with three real processes, the leader killed and restarted, passes; since 23 Oct 2026 the feed is taken over in 2.6-2.9 s (was ~25 s), 0 trades voided in the re-runs (finding §4.1 fixed, re-run §3.4), and orphaned stake locks are released by a fenced leader loop |
| F-15 | Wallet, ledger and transfer workflows end to end | [funds-flows.md](money/funds-flows.md) | Written |
| F-16 | USDT integration reachable as documented | [usdt-flows.md](usdt/usdt-flows.md) | Written (implemented and sandbox-tested; live BeeX keys still needed for real money) |
| F-17 | Feed-health and historical-data rules | [feed-rules.md](operations/feed-rules.md); related: [production-readiness.md §6.2](operations/production-readiness.md), [disclosures.md](customer/disclosures.md) | Written |
| F-18 | Risk dashboard and P&L metric definitions | [metric-dictionary.md](operations/metric-dictionary.md); dashboard visibility in [permission-matrix.md §3.2](security/permission-matrix.md) | Written |
| F-19 | Security, session and admin hardening | [threat-model.md](security/threat-model.md), [permission-matrix.md](security/permission-matrix.md), [secret-scan.md](security/secret-scan.md) | Written |
| F-20 | Test evidence matches the README claims | Root `README.md` "Testing" (rewritten: the Go suites and CI instead of the former Node suites); `.github/workflows/ci.yml` (build, vet, gofmt, the full Go suite against a MongoDB replica set and Redis with a coverage report per commit, web and mobile type checks and tests); test lists in every document above; [test-inventory.md](testing/test-inventory.md); [load-and-failover.md](operations/load-and-failover.md) | Full Go suite on 23 Oct 2026: 429 passed, 0 failed, 0 skipped (38 packages), coverage 45.9%; web 354, admin 35, mobile 369 tests pass. CI runs from the next push |
| F-21 | Production readiness | [production-readiness.md](operations/production-readiness.md) | Written; alert list completed (§10.2), datastore authentication, log rotation, CSP, CI and load results added; one-time steps on the server in §13; owner items open: §11, §12 |

## Relationship to `docs/logic/`

The logic reference in `docs/logic/` was rewritten against the current code
(pricing engine, feed rules, settlement policy, USDT, Binance hedging, admin
security). Where a statement there and one here disagree, the code is the
reference; please report it.
