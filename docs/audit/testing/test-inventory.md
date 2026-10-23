# Test inventory

Generated from the test files and the full test run in this commit on 23 October 2026 (Go 1.27.1, MongoDB replica set + Redis test containers, `go test -count=1 -p 1 -v -cover ./...`). It maps each test package to the audit findings it gives evidence for, and lists what is **not** tested.

**Totals:** 431 Go test functions in 39 packages; 429 passed in the full run. The 2 tests in `cmd/server` are opt-in system tests (build tag `loadtest`, `LOADTEST=1`) and ran separately - see `docs/audit/operations/load-and-failover.md`. Web client 354, admin console 35 and mobile app 369 unit tests pass. Statement coverage of the Go code: 45.9%.

## Summary by package

| Package | Findings | Tests | Result | Coverage |
|---|---|---|---|---|
| `cmd/server` | F-05, F-06, F-14 (opt-in, build tag loadtest) | 2 | opt-in | - |
| `internal/api/admin` | F-15, F-18, F-19 | 6 | ok | 25.7% |
| `internal/api/auth` | F-19 | 3 | ok | 18.7% |
| `internal/api/binary` | F-01, F-05 | 2 | ok | 45.6% |
| `internal/api/marketdata` | general | 2 | ok | 74.1% |
| `internal/api/transfers` | general | 3 | ok | 29.5% |
| `internal/api/usdt` | F-16 | 4 | ok | 69.4% |
| `internal/api/users` | general | 3 | ok | 27.1% |
| `internal/config` | F-16, F-19, F-21, hedging config | 14 | ok | 56.4% |
| `internal/models` | general | 4 | ok | 35.4% |
| `internal/platform/httpx` | F-19 | 7 | ok | 24.8% |
| `internal/platform/leader` | F-14 | 5 | ok | 76.8% |
| `internal/platform/redisx` | F-21 | 3 | ok | 9.9% |
| `internal/platform/util` | general | 2 | ok | 35.2% |
| `internal/service/admin` | F-09, F-18 | 1 | ok | 28.2% |
| `internal/service/alerts` | F-17, F-21 | 15 | ok | 80.5% |
| `internal/service/alerts/monitor` | F-21 | 4 | ok | 48.3% |
| `internal/service/assets` | F-01 (launch scope) | 1 | ok | 12.9% |
| `internal/service/auth` | F-19 | 15 | ok | 61.5% |
| `internal/service/beex` | F-16 | 2 | ok | 13.5% |
| `internal/service/binary` | F-01, F-05, F-11, F-12, F-15 | 54 | ok | 74.9% |
| `internal/service/expiry` | F-11, F-14 | 4 | ok | 64.7% |
| `internal/service/exposure` | F-04, F-05, F-06 | 18 | ok | 74.4% |
| `internal/service/hedging` | F-07, F-08, F-09, F-10 | 17 | ok | 59.7% |
| `internal/service/hedging/providers` | F-08 | 6 | ok | 57.2% |
| `internal/service/marketdata` | F-01 (normalisation), F-11, F-14, F-17 | 28 | ok | 65.4% |
| `internal/service/marketdata/providers` | F-01, F-17 | 5 | ok | 38.2% |
| `internal/service/payouts` | F-13 | 15 | ok | 76.5% |
| `internal/service/pricing` | F-01, F-02, F-03, F-10 | 24 | ok | 58.6% |
| `internal/service/pricinghedge` | F-10 | 4 | ok | 92.0% |
| `internal/service/reconciliation` | F-06, F-14, F-15, F-18 | 7 | ok | 64.0% |
| `internal/service/risk` | F-04, F-05, F-07 | 14 | ok | 47.6% |
| `internal/service/riskconfig` | F-11, F-17 | 5 | ok | 51.4% |
| `internal/service/settlement` | F-11, F-12, F-14 | 38 | ok | 71.0% |
| `internal/service/support` | general | 4 | ok | 6.3% |
| `internal/service/transfers` | F-15 | 21 | ok | 65.1% |
| `internal/service/usdt` | F-16 | 45 | ok | 60.3% |
| `internal/service/users` | F-19 | 6 | ok | 31.6% |
| `internal/service/wallets` | F-15 | 18 | ok | 49.7% |

## Every test

### `cmd/server` - F-05, F-06, F-14 (opt-in, build tag loadtest)

- `TestFailoverMultiProcess` (failover_test.go) - opt-in
- `TestLoadTradeStormAndBookRebuild` (loadtest_test.go) - opt-in

### `internal/api/admin` - F-15, F-18, F-19

- `TestAlertRoutesPermissionsAndAudit` (alerts_test.go) - PASS
- `TestAuditDoc` (audit_test.go) - PASS
- `TestSupportCannotReadExposureOrPnl` (security_test.go) - PASS
- `TestAdminChangesAreAudited` (security_test.go) - PASS
- `TestAdminSignInWithEnrolmentOverHTTP` (security_test.go) - PASS
- `TestWalletAdjustFundingRoute` (wallet_adjust_test.go) - PASS

### `internal/api/auth` - F-19

- `TestRegisterValidation` (validation_test.go) - PASS
- `TestLoginValidation` (validation_test.go) - PASS
- `TestZodEmail` (validation_test.go) - PASS

### `internal/api/binary` - F-01, F-05

- `TestParseCreateTrade` (routes_test.go) - PASS
- `TestTradeSummaryRoute` (summary_route_test.go) - PASS

### `internal/api/marketdata` - general

- `TestWindowValidation` (routes_test.go) - PASS
- `TestCandlesAndTicksRoutes` (routes_test.go) - PASS

### `internal/api/transfers` - general

- `TestJSNumber` (multipart_test.go) - PASS
- `TestMinorAmount` (multipart_test.go) - PASS
- `TestReadMultipartDeposit` (multipart_test.go) - PASS

### `internal/api/usdt` - F-16

- `TestRoutesResolveMismatchedWithdrawal` (resolve_routes_test.go) - PASS
- `TestRoutesWebhookVerifiesTheRawBody` (routes_test.go) - PASS
- `TestRoutesCustomerWalletContract` (routes_test.go) - PASS
- `TestRoutesAdminPermissionsAndAudit` (routes_test.go) - PASS

### `internal/api/users` - general

- `TestIsPlausibleBirthDate` (birthdate_test.go) - PASS
- `TestAgeInYears` (birthdate_test.go) - PASS
- `TestUpdateProfileValidation` (birthdate_test.go) - PASS

### `internal/config` - F-16, F-19, F-21, hedging config

- `TestAlertEmailDisabledNeedsNothing` (alerts_env_test.go) - PASS
- `TestAlertEmailEnabledRequiresAWorkingSetup` (alerts_env_test.go) - PASS
- `TestAlertEmailValidSetup` (alerts_env_test.go) - PASS
- `TestHedgeDefaults` (hedge_env_test.go) - PASS
- `TestHedgeLiveRefusesToBootWithoutKeys` (hedge_env_test.go) - PASS
- `TestHedgeEnvironmentsCannotBeMixed` (hedge_env_test.go) - PASS
- `TestHedgeRateNeedsItsTime` (hedge_env_test.go) - PASS
- `TestRemovedHedgeAdaptersAreRefused` (hedge_env_test.go) - PASS
- `TestParseTrustedProxies` (security_test.go) - PASS
- `TestTrustedProxyDefaultIsLoopbackOnly` (security_test.go) - PASS
- `TestRemovedPermissionsAreDropped` (security_test.go) - PASS
- `TestUsdtLiveModeRefusesToBootWithoutKeys` (usdt_env_test.go) - PASS
- `TestUsdtProductionSandboxNeedsTheExplicitSwitch` (usdt_env_test.go) - PASS
- `TestUsdtDevelopmentMockNeedsNoKeys` (usdt_env_test.go) - PASS

### `internal/models` - general

- `TestAccountTypeDefaults` (account_test.go) - PASS
- `TestLegacyWalletIndexKey` (account_test.go) - PASS
- `TestViewsNullsAndSlices` (views_test.go) - PASS
- `TestAssetViewNextOpenAt` (views_test.go) - PASS

### `internal/platform/httpx` - F-19

- `TestClientIPIgnoresSpoofedForwardedForFromUntrustedPeer` (security_test.go) - PASS
- `TestClientIPTakesRightmostUntrustedHop` (security_test.go) - PASS
- `TestClientIPUsesConfiguredProxies` (security_test.go) - PASS
- `TestRateLimitRedisDownStillLimits` (security_test.go) - PASS
- `TestRateLimitSpoofedForwardedForDoesNotEscapeLimit` (security_test.go) - PASS
- `TestRateLimitFallsBackWhenRedisUnreachable` (security_test.go) - PASS
- `TestMemoryWindowExpires` (security_test.go) - PASS

### `internal/platform/leader` - F-14

- `TestTickRunsUnderAValidFence` (leader_test.go) - PASS
- `TestEpochIncreasesWithEveryAcquisition` (leader_test.go) - PASS
- `TestLeaderLossMidTickIsFenced` (leader_test.go) - PASS
- `TestBoundTickIsCancelledAtLeaseExpiry` (leader_test.go) - PASS
- `TestNoFenceOutsideALoop` (leader_test.go) - PASS

### `internal/platform/redisx` - F-21

- `TestClientOptionsEnableRetries` (redis_test.go) - PASS
- `TestDroppedConnectionIsRetried` (redis_test.go) - PASS
- `TestDroppedConnectionFailsWithRetriesDisabled` (redis_test.go) - PASS

### `internal/platform/util` - general

- `TestNextTradingOpen` (trading_hours_test.go) - PASS
- `TestNextTradingOpenMatchesMinuteByMinute` (trading_hours_test.go) - PASS

### `internal/service/admin` - F-09, F-18

- `TestPlatformPnlUsesHedgeLedger` (hedge_pnl_test.go) - PASS

### `internal/service/alerts` - F-17, F-21

- `TestRaiseDeduplicatesOpenAlerts` (alerts_test.go) - PASS
- `TestEscalationQueuesAnotherEmailButNeverDowngrades` (alerts_test.go) - PASS
- `TestBelowMinSeverityOrDisabledIsStoredNotEmailed` (alerts_test.go) - PASS
- `TestResolveByKeyEmailsRecoveryOnlyWhenNotified` (alerts_test.go) - PASS
- `TestOperatorResolveThenReopen` (alerts_test.go) - PASS
- `TestResolveStaleOnlyTouchesOldAlerts` (alerts_test.go) - PASS
- `TestDeliverWaitsToGatherThenSendsADigest` (alerts_test.go) - PASS
- `TestDeliverSendsIndividuallyBelowTheDigestThreshold` (alerts_test.go) - PASS
- `TestDeliverRetriesWithBackoffThenGivesUp` (alerts_test.go) - PASS
- `TestDeliverRespectsTheHourlyLimitAndDigestsTheBacklog` (alerts_test.go) - PASS
- `TestSMTPStartTLSAuthDelivers` (mailer_test.go) - PASS
- `TestSMTPImplicitTLSDelivers` (mailer_test.go) - PASS
- `TestSMTPRefusesServerWithoutStartTLS` (mailer_test.go) - PASS
- `TestSMTPWrongPasswordFails` (mailer_test.go) - PASS
- `TestMessageHeadersCannotBeInjected` (mailer_test.go) - PASS

### `internal/service/alerts/monitor` - F-21

- `TestBookAlertAfterThresholdAndAutoResolve` (monitor_test.go) - PASS
- `TestFeedStaleWarnsThenOfflineEscalatesAndClears` (monitor_test.go) - PASS
- `TestStuckSettlementsAlertAndClear` (monitor_test.go) - PASS
- `TestRateLimiterPerInstanceAlertResolvedWhenStale` (monitor_test.go) - PASS

### `internal/service/assets` - F-01 (launch scope)

- `TestApplyLaunchScopeIsIdempotentAndAudited` (launchscope_test.go) - PASS

### `internal/service/auth` - F-19

- `TestComparesBcryptjsHashes` (auth_test.go) - PASS
- `TestDummyHashIsWellFormed` (auth_test.go) - PASS
- `TestHashLongPassword` (auth_test.go) - PASS
- `TestAdminLoginEnrolmentAndTwoFactorFlow` (mfa_flow_test.go) - PASS
- `TestChallengeDiesAfterTooManyWrongCodes` (mfa_flow_test.go) - PASS
- `TestCustomerLoginUnaffectedAdminNeedsConsole` (mfa_flow_test.go) - PASS
- `TestTwoFactorNotRequiredWhenDisabledAndNotEnrolled` (mfa_flow_test.go) - PASS
- `TestRefreshRejectsPreEnrolmentAdminSessionWhenRequired` (mfa_flow_test.go) - PASS
- `TestChangePasswordEndsEverySessionAndReissues` (mfa_flow_test.go) - PASS
- `TestHOTPRFC4226Vectors` (totp_test.go) - PASS
- `TestTOTPRFC6238Vectors` (totp_test.go) - PASS
- `TestVerifyTOTPWindowAndFormat` (totp_test.go) - PASS
- `TestSecretEncodingAndURI` (totp_test.go) - PASS
- `TestSealedSecretIsBoundToItsUser` (totp_test.go) - PASS
- `TestRecoveryCodesFormat` (totp_test.go) - PASS

### `internal/service/beex` - F-16

- `TestVerifySignatureAcceptsOnlyTheSignedBytesInTime` (signature_test.go) - PASS
- `TestMockProviderNeedsTheSandboxSwitchInProduction` (signature_test.go) - PASS

### `internal/service/binary` - F-01, F-05, F-11, F-12, F-15

- `TestCancelTradeReleasesOnceAndIsIdempotent` (cancel_test.go) - PASS
- `TestCancelTradeWalletFailureIsFlaggedAndRepaired` (cancel_test.go) - PASS
- `TestRepairFinishesLegacyCancels` (cancel_test.go) - PASS
- `TestAcceptanceFreezesTheDrawPolicy` (drawpolicy_test.go) - PASS
- `TestDrawPolicyForFallsBackForOldTrades` (drawpolicy_test.go) - PASS
- `TestEarlyCloseCorrectionLookup` (earlyclose_calibration_test.go) - PASS
- `TestEarlyCloseCorrectionMatchesTheFit` (earlyclose_calibration_test.go) - PASS
- `TestEarlyCloseNoTableIsTheUncorrectedFormula` (earlyclose_calibration_test.go) - PASS
- `TestEarlyCloseBacktestVolatilityIsTheLiveOne` (earlyclose_calibration_test.go) - PASS
- `TestQuoteAppliesTheEarlyCloseCalibration` (earlyclose_calibration_test.go) - PASS
- `TestCloseEarlyBooksOneOutcomeWithEvidence` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyRefusesMissingOrStaleFeed` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyRefusesZeroVolatility` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyRequiresARecentQuote` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyBoundsTheRequoteAgainstTheShownQuote` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyImpossibleAtOrAfterExpiry` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyRefusedWhileTradingHalted` (earlyclose_service_test.go) - PASS
- `TestCloseEarlyRefusedWhileSymbolHalted` (earlyclose_service_test.go) - PASS
- `TestConcurrentDuplicateClosesMoveMoneyOnce` (earlyclose_service_test.go) - PASS
- `TestCheckQuoteValidity` (earlyclose_service_test.go) - PASS
- `TestStandardNormalCdf` (earlyclose_test.go) - PASS
- `TestCoinFlipAtEntry` (earlyclose_test.go) - PASS
- `TestPaysMoreInTheMoney` (earlyclose_test.go) - PASS
- `TestPaysLessOutOfTheMoney` (earlyclose_test.go) - PASS
- `TestDownMirrorsUp` (earlyclose_test.go) - PASS
- `TestNeverPaysFullPayout` (earlyclose_test.go) - PASS
- `TestNeverNegative` (earlyclose_test.go) - PASS
- `TestTimeDecay` (earlyclose_test.go) - PASS
- `TestAppliesFee` (earlyclose_test.go) - PASS
- `TestStillMarket` (earlyclose_test.go) - PASS
- `TestToFixedMatchesJS` (earlyclose_test.go) - PASS
- `TestFeedRuleBlockRefusesQuotesAndTrades` (feedrules_test.go) - PASS
- `TestTradeFingerprintIncludesAccount` (fingerprint_test.go) - PASS
- `TestOneFreshnessRuleForQuotesAndAcceptance` (freshness_accept_test.go) - PASS
- `TestPlatformRefusalOfAnOfferCarriesAFreshQuote` (freshness_accept_test.go) - PASS
- `TestRedisFailureAfterTheInsertReturnsTheTrade` (freshness_accept_test.go) - PASS
- `TestOfferLifecycle` (pricing_accept_test.go) - PASS
- `TestOfferAcceptedConcurrently` (pricing_accept_test.go) - PASS
- `TestOfferExpiryAndSlippage` (pricing_accept_test.go) - PASS
- `TestConfigChangeDuringAnOffer` (pricing_accept_test.go) - PASS
- `TestKillSwitch` (pricing_accept_test.go) - PASS
- `TestOldAppWithoutAnOffer` (pricing_accept_test.go) - PASS
- `TestRefusals` (pricing_accept_test.go) - PASS
- `TestExposureLimitPerSide` (pricing_accept_test.go) - PASS
- `TestFixedModePaysThePayoutRow` (pricing_accept_test.go) - PASS
- `TestHedgeOutageWidensOrRefuses` (pricing_accept_test.go) - PASS
- `TestSlippageUnchanged` (slippage_test.go) - PASS
- `TestSlippageUp` (slippage_test.go) - PASS
- `TestSlippageDown` (slippage_test.go) - PASS
- `TestSlippageBoundary` (slippage_test.go) - PASS
- `TestSlippageDisabledAndDegenerate` (slippage_test.go) - PASS
- `TestSlippageRealisticInstruments` (slippage_test.go) - PASS
- `TestSummariseStatusTotals` (summary_test.go) - PASS
- `TestTradeFilter` (summary_test.go) - PASS

### `internal/service/expiry` - F-11, F-14

- `TestProcessExpiriesWaitsForPostExpiryTick` (expiry_test.go) - PASS
- `TestLeaderLossMidTickHandOffIsFenced` (expiry_test.go) - PASS
- `TestHandOffSkipsEntryRescheduledMeanwhile` (expiry_test.go) - PASS
- `TestQueueLossRebuiltFromDurableState` (expiry_test.go) - PASS

### `internal/service/exposure` - F-04, F-05, F-06

- `TestSpecifiedNetAndGross` (exposure_test.go) - PASS
- `TestNegativeNetWhenBookLeansDown` (exposure_test.go) - PASS
- `TestBalancedBook` (exposure_test.go) - PASS
- `TestLiabilityIsWorstCase` (exposure_test.go) - PASS
- `TestNeverNegative` (exposure_test.go) - PASS
- `TestDeriveSentiment` (exposure_test.go) - PASS
- `TestSafeModeWhenCountersAreDeleted` (rebuild_test.go) - PASS
- `TestRebuildDuringConcurrentTradingIsExact` (rebuild_test.go) - PASS
- `TestReconcileRepairsDrift` (rebuild_test.go) - PASS
- `TestSupersededAndAbandonedRebuilds` (rebuild_test.go) - PASS
- `TestBootCheckKeepsAConsistentBook` (rebuild_test.go) - PASS
- `TestReleaseIsByTradeID` (release_test.go) - PASS
- `TestReleaseOfALegacyTrade` (release_test.go) - PASS
- `TestReserveConcurrentNeverBreachesLimits` (reserve_test.go) - PASS
- `TestReservationsSurviveRebuild` (reserve_test.go) - PASS
- `TestBookOperationsAreIdempotent` (reserve_test.go) - PASS
- `TestLapsedReservationsAreSweptAgainstMongo` (reserve_test.go) - PASS
- `TestSideLiabilityIsAtomic` (sideliability_test.go) - PASS

### `internal/service/hedging` - F-07, F-08, F-09, F-10

- `TestEvaluatePlacesFillsAndReplays` (lifecycle_test.go) - PASS
- `TestPartialFillThenRemainder` (lifecycle_test.go) - PASS
- `TestTimeoutAfterAcceptanceIsQueriedNotResent` (lifecycle_test.go) - PASS
- `TestLostRequestFailsAfterRecvWindow` (lifecycle_test.go) - PASS
- `TestDuplicateFillReportsCountOnce` (lifecycle_test.go) - PASS
- `TestCancelRacingAFill` (lifecycle_test.go) - PASS
- `TestOutageAndStaleRateMakeHedgingUnavailable` (lifecycle_test.go) - PASS
- `TestRestartRecovery` (lifecycle_test.go) - PASS
- `TestReconcileDriftRaisesAlert` (lifecycle_test.go) - PASS
- `TestFundingIsRecordedOnceAndInPnl` (lifecycle_test.go) - PASS
- `TestTradeHedgeCostForPricing` (lifecycle_test.go) - PASS
- `TestPlanExecution` (pure_test.go) - PASS
- `TestRealisedPnlWorkedExample` (pure_test.go) - PASS
- `TestComputeCostBps` (pure_test.go) - PASS
- `TestSelectFxRate` (pure_test.go) - PASS
- `TestEntryDelta` (pure_test.go) - PASS
- `TestWorkedHedgeTable` (worked_table_test.go) - PASS

### `internal/service/hedging/providers` - F-08

- `TestSignMatchesBinanceDocumentedExample` (binance_test.go) - PASS
- `TestDecimalE8` (binance_test.go) - PASS
- `TestBinanceAdapterAgainstFake` (binance_test.go) - PASS
- `TestBadSecretIsRejected` (binance_test.go) - PASS
- `TestLiveVenueRefusesWithoutSwitchOrKeys` (binance_test.go) - PASS
- `TestUnknownOutcomes` (binance_test.go) - PASS

### `internal/service/marketdata` - F-01 (normalisation), F-11, F-14, F-17

- `TestChange24hPercent` (change24h_test.go) - PASS
- `TestFeedLeaseGatesIngestion` (feedlease_test.go) - PASS
- `TestGapTrackerTellsAGapFromAQuietMarket` (feedrules_test.go) - PASS
- `TestGapReasonsDisconnectAndRestart` (feedrules_test.go) - PASS
- `TestGapThresholdFollowsTheExpectedRate` (feedrules_test.go) - PASS
- `TestClockVerdict` (feedrules_test.go) - PASS
- `TestBufferSaturationIsCountedAndRefusesTrading` (feedrules_test.go) - PASS
- `TestDisconnectGapIsRecordedExactlyAndTradingStops` (feedrules_test.go) - PASS
- `TestRestartGapFoundWhenDataResumes` (feedrules_test.go) - PASS
- `TestClockDriftRefusesTradingAndClears` (feedrules_test.go) - PASS
- `TestOutOfOrderTicksAreRejectedAndCounted` (feedrules_test.go) - PASS
- `TestTickHistoryRetentionIsConfigurable` (feedrules_test.go) - PASS
- `TestTickHistoryDefaultRetention` (feedrules_test.go) - PASS
- `TestAssessFreshness` (freshness_test.go) - PASS
- `TestIngestTicksWritesABatchInOrder` (ingest_batch_test.go) - PASS
- `TestFeedHealthClassification` (marketdata_test.go) - PASS
- `TestJSString` (marketdata_test.go) - PASS
- `TestTickJSONKeyOrder` (marketdata_test.go) - PASS
- `TestFoldCandles` (marketdata_test.go) - PASS
- `TestRealisedVolatility` (marketdata_test.go) - PASS
- `TestNormalizationChecksInOrder` (normalize_test.go) - PASS
- `TestRejectedQuoteNeverChangesState` (normalize_test.go) - PASS
- `TestOutlierFilter` (normalize_test.go) - PASS
- `TestPendingOutliersAgeOut` (normalize_test.go) - PASS
- `TestDuplicateAndOutOfOrderFloods` (normalize_test.go) - PASS
- `TestProviderOutage` (normalize_test.go) - PASS
- `TestEWMAVolatility` (normalize_test.go) - PASS
- `TestIngestionNormalisesAndPersists` (normstate_test.go) - PASS

### `internal/service/marketdata/providers` - F-01, F-17

- `TestBinanceServerTime` (clock_test.go) - PASS
- `TestBinanceBookTicker` (providers_test.go) - PASS
- `TestGenericParsesBothShapes` (providers_test.go) - PASS
- `TestSimulatedWalk` (providers_test.go) - PASS
- `TestSimulatedWalkStaysNearSeedOverADay` (providers_test.go) - PASS

### `internal/service/payouts` - F-13

- `TestValidatePayoutInput` (payouts_test.go) - PASS
- `TestUpsertPayoutVersionsAndKeepsOldTerms` (payouts_test.go) - PASS
- `TestUpsertPayoutFailureMidUpdateKeepsPreviousVersion` (payouts_test.go) - PASS
- `TestUpsertPayoutRollbackFailureBlocksTradesUntilRepaired` (payouts_test.go) - PASS
- `TestUpsertPayoutRepairsLeftoverReplacementFirst` (payouts_test.go) - PASS
- `TestConcurrentUpsertsLeaveOneActiveRow` (payouts_test.go) - PASS
- `TestRacingReplacementsWithoutTheLockStillLeaveOneActiveRow` (payouts_test.go) - PASS
- `TestInFlightReadDoesNotCacheSupersededTerms` (payouts_test.go) - PASS
- `TestOtherInstanceChangeIsSeenWithoutWaitingForTheTTL` (payouts_test.go) - PASS
- `TestDeactivatePayoutBlocksNewTrades` (payouts_test.go) - PASS
- `TestUpsertPayoutRejectsInvalidInputAgainstTheStoredAsset` (payouts_test.go) - PASS
- `TestScheduledPayoutTakesEffectAtItsTime` (schedule_test.go) - PASS
- `TestScheduledActivationFailureIsRetried` (schedule_test.go) - PASS
- `TestScheduledPayoutCanBeCancelled` (schedule_test.go) - PASS
- `TestEffectiveFromValidation` (schedule_test.go) - PASS

### `internal/service/pricing` - F-01, F-02, F-03, F-10

- `TestFitLogisticRecoversParameters` (calibrate_test.go) - PASS
- `TestCalibrateEntryOnSyntheticRandomWalk` (calibrate_test.go) - PASS
- `TestCalibrateEntryRefusesShortHistory` (calibrate_test.go) - PASS
- `TestWilsonAndQuantile` (calibrate_test.go) - PASS
- `TestEarlyCloseVolatility` (earlyclose_eval_test.go) - PASS
- `TestEarlyCloseBacktestOnARandomWalk` (earlyclose_eval_test.go) - PASS
- `TestFitEarlyCloseCalibrationRecoversParameters` (earlyclose_eval_test.go) - PASS
- `TestEarlyCloseWinProbability` (earlyclose_eval_test.go) - PASS
- `TestSpecWorkedExample` (engine_test.go) - PASS
- `TestVolatilitySpike` (engine_test.go) - PASS
- `TestExposureBlocksTheLoadedSideOnly` (engine_test.go) - PASS
- `TestFinalExposureCheckUsesTheRealPayout` (engine_test.go) - PASS
- `TestClampAndFloor` (engine_test.go) - PASS
- `TestHedgeUnavailableWidensOrRefuses` (engine_test.go) - PASS
- `TestPayoutProperties` (engine_test.go) - PASS
- `TestRulesFromDefaults` (engine_test.go) - PASS
- `TestHaltFor` (engine_test.go) - PASS
- `TestGuard` (engine_test.go) - PASS
- `TestDefaultHedgeEstimate` (engine_test.go) - PASS
- `TestSourceAllowed` (engine_test.go) - PASS
- `TestEmbeddedModel` (engine_test.go) - PASS
- `TestSymbolPricedLaunchScope` (engine_test.go) - PASS
- `TestComputeFeatures` (features_test.go) - PASS
- `TestComputeFeaturesMomentumSign` (features_test.go) - PASS

### `internal/service/pricinghedge` - F-10

- `TestEstimatorMapsHedgingVerdicts` (pricinghedge_test.go) - PASS
- `TestInstallWiresBothHooks` (pricinghedge_test.go) - PASS
- `TestHedgeStateDrivesThePayout` (pricinghedge_test.go) - PASS
- `TestHedgeSigmaIsThePricingVolatility` (pricinghedge_test.go) - PASS

### `internal/service/reconciliation` - F-06, F-14, F-15, F-18

- `TestStuckSettlementsCheck` (settlements_test.go) - PASS
- `TestRunAlertBecomesAnOperationalAlert` (settlements_test.go) - PASS
- `TestStakeLocksHaveOpenTradesCheck` (stakelocks_test.go) - PASS
- `TestTransfersAgainstLedgerPassesWhenConsistent` (transfers_test.go) - PASS
- `TestTransfersAgainstLedgerFlagsMismatches` (transfers_test.go) - PASS
- `TestStuckApprovedTransfersAreReported` (transfers_test.go) - PASS
- `TestLockedFundsCountApprovedWithdrawals` (transfers_test.go) - PASS

### `internal/service/risk` - F-04, F-05, F-07

- `TestDemoTradeSkipsHouseLimits` (demo_limits_test.go) - PASS
- `TestLegDeltaMatchesClosedForm` (hedgetarget_test.go) - PASS
- `TestGammaWindowExcludesNearExpiry` (hedgetarget_test.go) - PASS
- `TestOffsettingBookHasNoDelta` (hedgetarget_test.go) - PASS
- `TestHedgeRatioReplacesTimesTwo` (hedgetarget_test.go) - PASS
- `TestCapTarget` (hedgetarget_test.go) - PASS
- `TestAssessRiskHedgeSizing` (hedgetarget_test.go) - PASS
- `TestBelowThresholdTargetsFlat` (hedgetarget_test.go) - PASS
- `TestUnavailableHedgeAddsReason` (hedgetarget_test.go) - PASS
- `TestLiabilityStressScenarios` (liability_stress_test.go) - PASS
- `TestTradeLimits` (risk_test.go) - PASS
- `TestClassifyRiskLevel` (risk_test.go) - PASS
- `TestSelectHedgeLevel` (risk_test.go) - PASS
- `TestToFixed` (risk_test.go) - PASS

### `internal/service/riskconfig` - F-11, F-17

- `TestEarlyCloseCalibrationConfig` (earlyclose_config_test.go) - PASS
- `TestSettlementPolicyAndFeedRulesValidation` (policy_validate_test.go) - PASS
- `TestPolicyPatchApply` (policy_validate_test.go) - PASS
- `TestValidateConfig` (riskconfig_test.go) - PASS
- `TestPatchApply` (riskconfig_test.go) - PASS

### `internal/service/settlement` - F-11, F-12, F-14

- `TestVoidRecordsWhyNoPriceExisted` (noprice_test.go) - PASS
- `TestPolicyComesFromTheVersionedRiskConfig` (policy_config_test.go) - PASS
- `TestDecideExpiryPrice_LatestTickAtOrBeforeExpiryRegardlessOfArrivalOrder` (price_test.go) - PASS
- `TestDecideExpiryPrice_TickExactlyAtExpiryQualifies` (price_test.go) - PASS
- `TestDecideExpiryPrice_NeverUsesPostExpiryTick` (price_test.go) - PASS
- `TestDecideExpiryPrice_AwaitsConfirmationWithinWindow` (price_test.go) - PASS
- `TestDecideExpiryPrice_StaleTickDefersThenVoids` (price_test.go) - PASS
- `TestDecideExpiryPrice_MissingTicksDeferThenVoid` (price_test.go) - PASS
- `TestDecideExpiryPrice_StaleTickRuleUsesOldTickButNeverPostExpiry` (price_test.go) - PASS
- `TestDecideExpiryPrice_ClockSkewedTickIgnored` (price_test.go) - PASS
- `TestDecideExpiryPrice_TiesAreDeterministic` (price_test.go) - PASS
- `TestDecideExpiryPrice_IgnoresUnusablePrices` (price_test.go) - PASS
- `TestEqualityGoesThroughDrawPolicy` (price_test.go) - PASS
- `TestNormalisePolicyKeepsHardRules` (price_test.go) - PASS
- `TestFinalStatusForEarlyClose` (price_test.go) - PASS
- `TestDetermineResult` (settlement_test.go) - PASS
- `TestApplyDrawPolicy` (settlement_test.go) - PASS
- `TestSettleWinPersistsReproducibleEvidence` (statemachine_test.go) - PASS
- `TestSettleEqualityAppliesDrawPolicy` (statemachine_test.go) - PASS
- `TestSettleUsesLateAndOutOfOrderTicksStampedBeforeExpiry` (statemachine_test.go) - PASS
- `TestSettleIgnoresClockSkewedTick` (statemachine_test.go) - PASS
- `TestSettleAwaitsPostExpiryTickThenSettles` (statemachine_test.go) - PASS
- `TestSettlePostExpiryOnlyDefersThenVoidsAndRefunds` (statemachine_test.go) - PASS
- `TestSettleStaleTickRuleIsExplicit` (statemachine_test.go) - PASS
- `TestSettleNeverBeforeExpiryRequeues` (statemachine_test.go) - PASS
- `TestTwoSettlementWorkersOnSameTrade` (statemachine_test.go) - PASS
- `TestDuplicateQueueEntriesSettleOnce` (statemachine_test.go) - PASS
- `TestCrashAfterWalletMutationRecoversToOneOutcome` (statemachine_test.go) - PASS
- `TestCrashAfterSettlementRowRecovers` (statemachine_test.go) - PASS
- `TestEarlyCloseCrashAfterWalletRecoversAsClosedEarly` (statemachine_test.go) - PASS
- `TestEarlyCloseAndExpiryAreMutuallyExclusive` (statemachine_test.go) - PASS
- `TestLegacySettlingBeforeExpiryReturnsToOpen` (statemachine_test.go) - PASS
- `TestLegacySettlingAfterExpirySettlesOnPolicyPrice` (statemachine_test.go) - PASS
- `TestLegacyEarlyCloseLedgerWithoutRowFinishesAsClosedEarly` (statemachine_test.go) - PASS
- `TestLegacyEarlyCloseRowFinishesAsClosedEarlyNotDraw` (statemachine_test.go) - PASS
- `TestIntentDisagreeingWithLedgerAdoptsLedger` (statemachine_test.go) - PASS
- `TestDemoTradeSettlesWithoutExposure` (statemachine_test.go) - PASS
- `TestTransactionalSettlementConcurrentAndCrash` (statemachine_test.go) - PASS

### `internal/service/support` - general

- `TestPreview` (support_test.go) - PASS
- `TestValidateBody` (support_test.go) - PASS
- `TestDisplayName` (support_test.go) - PASS
- `TestTypingPayload` (support_test.go) - PASS

### `internal/service/transfers` - F-15

- `TestDepositWithoutMethodIsHeldToDefaultLimits` (transfer_flow_test.go) - PASS
- `TestConcurrentDepositApprovalsCreditOnce` (transfer_flow_test.go) - PASS
- `TestDepositWalletFailureAfterApprovalRecovers` (transfer_flow_test.go) - PASS
- `TestApprovedDepositWithoutCreditIsReportedAndCanBeCompletedOrRejected` (transfer_flow_test.go) - PASS
- `TestApproveAgainCompletesAStuckApproval` (transfer_flow_test.go) - PASS
- `TestWithdrawalRequestReservesWithTheTransfer` (transfer_flow_test.go) - PASS
- `TestWithdrawalReservationFailureLeavesNothingBehind` (transfer_flow_test.go) - PASS
- `TestOrphanedWithdrawalReservationIsReleasedByRepair` (transfer_flow_test.go) - PASS
- `TestRacingWithdrawalsCannotOverReserve` (transfer_flow_test.go) - PASS
- `TestConcurrentWithdrawalApprovalsSettleOnce` (transfer_flow_test.go) - PASS
- `TestWithdrawalSettleFailureAfterApprovalRecovers` (transfer_flow_test.go) - PASS
- `TestRejectWithdrawalReleasesReservationOnce` (transfer_flow_test.go) - PASS
- `TestRejectedWithdrawalWhoseReleaseFailedIsRepaired` (transfer_flow_test.go) - PASS
- `TestLegacyWithdrawalWithoutReservationIsNotSettled` (transfer_flow_test.go) - PASS
- `TestApproveAndRejectRaceHasOneOutcome` (transfer_flow_test.go) - PASS
- `TestDetectFileType` (transfer_test.go) - PASS
- `TestStoreAndResolveAttachment` (transfer_test.go) - PASS
- `TestNormaliseUtr` (transfer_test.go) - PASS
- `TestValidateDestination` (transfer_test.go) - PASS
- `TestSanitiseFilename` (transfer_test.go) - PASS
- `TestValidatePaymentMethod` (transfer_test.go) - PASS

### `internal/service/usdt` - F-16

- `TestSandboxDepositPaidCreditsOnceAcrossDuplicateDeliveries` (deposit_flow_test.go) - PASS
- `TestSandboxDepositReplayedRequestIsRecognised` (deposit_flow_test.go) - PASS
- `TestSandboxDepositOverpaidCreditsWhatArrived` (deposit_flow_test.go) - PASS
- `TestSandboxDepositUnderpaidWaitsForAnOperator` (deposit_flow_test.go) - PASS
- `TestSandboxDepositPartialThenPaid` (deposit_flow_test.go) - PASS
- `TestSandboxDepositExpiredThenLatePaymentStillCredits` (deposit_flow_test.go) - PASS
- `TestSandboxWebhookBadSignatureIsRefusedAndNothingStored` (deposit_flow_test.go) - PASS
- `TestSandboxWebhookOldOrFutureTimestampIsRefused` (deposit_flow_test.go) - PASS
- `TestSandboxWebhookWithoutSecretIsUnavailable` (deposit_flow_test.go) - PASS
- `TestSandboxWebhookForUnknownInvoiceIsStoredForReviewNeverCredited` (deposit_flow_test.go) - PASS
- `TestSandboxWebhookReferenceOrModeMismatchIsNotCredited` (deposit_flow_test.go) - PASS
- `TestSandboxDepositPaidWithoutAmountsAsksBeexFirst` (deposit_flow_test.go) - PASS
- `TestSandboxDepositProviderTimeoutThenRetryReusesTheInvoice` (deposit_flow_test.go) - PASS
- `TestSandboxDepositProviderOutageThenRecovery` (deposit_flow_test.go) - PASS
- `TestSandboxConcurrentCreditPathsCreditOnce` (deposit_flow_test.go) - PASS
- `TestSandboxWebhookStoredButUnprocessedIsRecoveredByRetryLoop` (deposit_flow_test.go) - PASS
- `TestSandboxDepositBelowNetworkMinimumIsRefused` (deposit_flow_test.go) - PASS
- `TestSandboxReconciliationCreditsAMissedWebhookOnce` (reconcile_flow_test.go) - PASS
- `TestSandboxReconciliationFlagsCreditedDepositBeexCallsUnpaid` (reconcile_flow_test.go) - PASS
- `TestSandboxReconciliationFlagsSettledWithdrawalBeexCallsFailed` (reconcile_flow_test.go) - PASS
- `TestSandboxReconciliationFlagsLedgerMismatch` (reconcile_flow_test.go) - PASS
- `TestSandboxReconciliationProviderOutageRaisesAlertThenClears` (reconcile_flow_test.go) - PASS
- `TestSandboxInvoiceExpiryAsksBeex` (reconcile_flow_test.go) - PASS
- `TestSandboxInvoiceExpiryDuringOutageThenLatePaymentStillCounts` (reconcile_flow_test.go) - PASS
- `TestSandboxPayoutPollingCompletesWithoutWebhook` (reconcile_flow_test.go) - PASS
- `TestSandboxWorkerStopsWhenItsLeaseIsGone` (reconcile_flow_test.go) - PASS
- `TestSandboxSummaryShowsAlertsAndSandbox` (reconcile_flow_test.go) - PASS
- `TestSandboxProviderMismatchResolvedByAnOperator` (resolve_flow_test.go) - PASS
- `TestSandboxProviderMismatchConcurrentResolutionsMoveMoneyOnce` (resolve_flow_test.go) - PASS
- `TestSandboxWithdrawalApproveThenPaidSettlesOnce` (withdrawal_flow_test.go) - PASS
- `TestSandboxWithdrawalFailedReturnsFundsOnce` (withdrawal_flow_test.go) - PASS
- `TestSandboxWithdrawalRejectReturnsFundsOnce` (withdrawal_flow_test.go) - PASS
- `TestSandboxConcurrentApprovalsSendOnePayout` (withdrawal_flow_test.go) - PASS
- `TestSandboxConcurrentApproveAndRejectOneWins` (withdrawal_flow_test.go) - PASS
- `TestSandboxPayoutTimeoutAfterBeexAcceptedIsResentWithSameReference` (withdrawal_flow_test.go) - PASS
- `TestSandboxPayoutOutageKeepsFundsReservedAndRetries` (withdrawal_flow_test.go) - PASS
- `TestSandboxPayoutDefiniteRefusalReturnsFundsOnce` (withdrawal_flow_test.go) - PASS
- `TestSandboxPayoutRefusedAfterUnknownAttemptChecksBeexFirst` (withdrawal_flow_test.go) - PASS
- `TestSandboxApprovedButNotSubmittedIsRecoveredByRetryLoop` (withdrawal_flow_test.go) - PASS
- `TestSandboxWithdrawalAddressIsValidatedPerNetwork` (withdrawal_flow_test.go) - PASS
- `TestSandboxWithdrawalLimitsAndBalance` (withdrawal_flow_test.go) - PASS
- `TestSandboxWithdrawalSameKeyIsOneWithdrawal` (withdrawal_flow_test.go) - PASS
- `TestSandboxLiveProviderOnlyPaysOutLiveDeposits` (withdrawal_flow_test.go) - PASS
- `TestSandboxPayoutAmountMismatchIsNotSettled` (withdrawal_flow_test.go) - PASS
- `TestSandboxPayoutCompletedAfterReleaseRaisesCriticalAlert` (withdrawal_flow_test.go) - PASS

### `internal/service/users` - F-19

- `TestNormaliseFavouriteIDs` (favourites_test.go) - PASS
- `TestSuspensionKillsAccessTokenImmediately` (revocation_test.go) - PASS
- `TestAdminUpdateLeastPrivilege` (revocation_test.go) - PASS
- `TestSuspensionClosesWebsocketImmediately` (revocation_test.go) - PASS
- `TestWebsocketRevalidatesOnSubscribe` (revocation_test.go) - PASS
- `TestEscapeRegex` (users_test.go) - PASS

### `internal/service/wallets` - F-15

- `TestValidateClientIdempotencyKey` (adjust_test.go) - PASS
- `TestAdjustmentLedgerKeyIsScopedToTheOperator` (adjust_test.go) - PASS
- `TestAdminAdjustWalletRepeatedRequestAppliesOnce` (adjust_test.go) - PASS
- `TestAdminAdjustWalletConcurrentDuplicatesApplyOnce` (adjust_test.go) - PASS
- `TestAdminAdjustWalletKeyReusedForAnotherBodyIsRefused` (adjust_test.go) - PASS
- `TestAdminAdjustWalletFailedDebitCanBeRetried` (adjust_test.go) - PASS
- `TestAdminAdjustWalletRequiresAKey` (adjust_test.go) - PASS
- `TestAdminAdjustFundingWallet` (adjust_test.go) - PASS
- `TestDemoResetDelta` (demo_test.go) - PASS
- `TestMutationsCarryAccount` (demo_test.go) - PASS
- `TestOrphanSweepReachesEveryLock` (orphan_locks_test.go) - PASS
- `TestOrphanSweepTicks` (orphan_locks_test.go) - PASS
- `TestTradeMutationsKeepTheLifecycle` (wallet_test.go) - PASS
- `TestSettlementPathsShareOneKey` (wallet_test.go) - PASS
- `TestAdjustmentMutations` (wallet_test.go) - PASS
- `TestCreditDebitRejectNonPositive` (wallet_test.go) - PASS
- `TestGuardFilter` (wallet_test.go) - PASS
- `TestNormCurrency` (wallet_test.go) - PASS

## Not tested, or tested only against a simulation

- Real Binance exchange: hedging is tested only against a simulated venue (httptest fake of the Binance API).
- Real BeeX: USDT is tested only against the BeeX mock / sandbox simulation.
- Real SMTP provider: alert e-mail is tested against a fake SMTP server (STARTTLS and implicit TLS).
- Production deploy scripts (deploy.sh, first-setup.sh, mongo-auth.sh) on the real VPS: tested in local Linux containers and a separate Docker Compose project, not on the server itself.
- Race detector (go test -race): not run on Windows; the CI workflow runs on Linux.
- Websocket load and a Redis restart under load: not tested.
- Mobile app on a real device: unit tests and type-check only; the owner tests the APK on a phone.
- Browser end-to-end: only the admin two-factor sign-in was driven in a real browser.
- Market regimes outside the calibration data window (crash, exchange outage).
