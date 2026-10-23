// Package risk is the risk engine (spec section 13).
//
// AssessRisk is a pure function of its inputs: the same exposure, config,
// volatility and feed state always produce the same level, score and hedge
// target. That is what makes a risk decision auditable and replayable (rule 9).
package risk

import (
	"context"
	"math"
	"math/big"
	"sort"
	"strconv"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/sync/errgroup"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/riskconfig"
)

type RiskInputs struct {
	AssetID                    string
	Symbol                     string
	Exposure                   models.ExposureSnapshot
	AssetExposureLimit         int64
	AssetNetExposureLimit      int64
	HedgeEnabled               bool
	AssetTargetHedgeRatio      float64
	AssetHedgeThresholdPercent *float64
	MinHedgeNotional           int64
	MaxHedgeNotional           int64
	Config                     *models.RiskConfig
	GlobalGrossExposure        int64
	Volatility                 float64
	FeedStatus                 models.FeedStatus
	SecondsToNearestExpiry     *int64
	ExistingHedgeNotional      int64
	// Platform-wide expiry liability; read only with a liability limit set.
	PlatformLiability int64
	// Open trades, price and volatility for the delta-based hedge target
	// (hedgetarget.go). Nil: the target is zero.
	HedgeBook *HedgeBook
	// Non-empty when the hedge venue cannot take an order now (FX rate
	// stale, venue down, ...): a non-zero target then adds a reason.
	HedgeUnavailableReason string
}

var feedPenalty = map[models.FeedStatus]float64{
	models.FeedStatusHealthy:  0,
	models.FeedStatusDegraded: 8,
	models.FeedStatusStale:    20,
	models.FeedStatusOffline:  35,
}

// ElevatedVolatility: volatility above this (stddev of per-tick log returns)
// counts as elevated.
const ElevatedVolatility = 0.0015

// maxSafeInteger is JS Number.MAX_SAFE_INTEGER.
const maxSafeInteger = 9007199254740991

// toFixed reproduces JS Number.prototype.toFixed: rounds the exact binary
// value, ties away from zero (Go's strconv rounds ties to even).
func toFixed(x float64, digits int) string {
	if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 1e21 {
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	neg := x < 0
	if neg {
		x = -x
	}
	scaled := new(big.Float).SetPrec(256).SetFloat64(x)
	scaled.Mul(scaled, new(big.Float).SetPrec(256).SetFloat64(math.Pow10(digits)))
	scaled.Add(scaled, big.NewFloat(0.5).SetPrec(256))
	n, _ := scaled.Int(nil) // truncates toward zero = floor for positives
	s := n.String()
	if digits > 0 {
		for len(s) <= digits {
			s = "0" + s
		}
		s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// roundTo is JS Number(x.toFixed(digits)).
func roundTo(x float64, digits int) float64 {
	f, _ := strconv.ParseFloat(toFixed(x, digits), 64)
	return f
}

func AssessRisk(in RiskInputs) models.RiskAssessment {
	reasons := []string{}
	cfg := in.Config

	assetGrossLimit := min(in.AssetExposureLimit, cfg.MaxAssetExposure)
	grossUtilisation := util.SafePercent(float64(in.Exposure.GrossExposure), float64(assetGrossLimit))
	netAbs := in.Exposure.NetExposure
	if netAbs < 0 {
		netAbs = -netAbs
	}
	netUtilisation := util.SafePercent(float64(netAbs), float64(in.AssetNetExposureLimit))
	globalUtilisation := util.SafePercent(float64(in.GlobalGrossExposure), float64(cfg.MaxGrossExposure))

	// The binding constraint decides: whichever ceiling is closest to being hit.
	utilisationPercent := max(grossUtilisation, netUtilisation, globalUtilisation)

	// Liability ceilings count only when configured (0 = off), and only win
	// when strictly tighter, so the stake-based reasons stay as they were.
	liabilityUtilisation, liabilityReason := 0.0, ""
	if cfg.MaxAssetLiability > 0 {
		liabilityUtilisation = util.SafePercent(float64(in.Exposure.ExpiryLiability), float64(cfg.MaxAssetLiability))
		liabilityReason = "liability at " + toFixed(liabilityUtilisation, 1) + "% of the asset liability limit"
	}
	if cfg.MaxPlatformLiability > 0 {
		if u := util.SafePercent(float64(in.PlatformLiability), float64(cfg.MaxPlatformLiability)); u > liabilityUtilisation {
			liabilityUtilisation = u
			liabilityReason = "platform liability at " + toFixed(u, 1) + "% of the global liability limit"
		}
	}

	switch {
	case liabilityUtilisation > utilisationPercent:
		utilisationPercent = liabilityUtilisation
		reasons = append(reasons, liabilityReason)
	case grossUtilisation >= netUtilisation && grossUtilisation >= globalUtilisation:
		reasons = append(reasons, "gross exposure at "+toFixed(grossUtilisation, 1)+"% of the asset limit")
	case netUtilisation >= globalUtilisation:
		reasons = append(reasons, "net exposure at "+toFixed(netUtilisation, 1)+"% of the asset limit")
	default:
		reasons = append(reasons, "platform gross exposure at "+toFixed(globalUtilisation, 1)+"% of the global limit")
	}

	score := utilisationPercent

	if p := feedPenalty[in.FeedStatus]; p > 0 {
		score += p
		reasons = append(reasons, "market data feed is "+string(in.FeedStatus))
	}

	if in.Volatility > ElevatedVolatility {
		score += math.Min(15, (in.Volatility/ElevatedVolatility)*5)
		reasons = append(reasons, "realised volatility is elevated")
	}

	if in.SecondsToNearestExpiry != nil && *in.SecondsToNearestExpiry <= 10 && in.Exposure.OpenTrades > 0 {
		// Concentrated expiries carry outcome risk that cannot be hedged away.
		score += 5
		reasons = append(reasons, "positions expiring within 10s")
	}

	level := ClassifyRiskLevel(score, cfg)
	hedge := ComputeHedgeTarget(HedgeTargetInput{RiskInputs: in, UtilisationPercent: utilisationPercent, Level: level})
	if hedge.Notional != 0 && in.HedgeUnavailableReason != "" {
		reasons = append(reasons, "hedge required but unavailable: "+in.HedgeUnavailableReason)
	}
	var detail *models.HedgeTargetDetail
	if in.HedgeBook != nil {
		d := hedge.Detail
		detail = &d
	}

	return models.RiskAssessment{
		AssetID:             in.AssetID,
		Symbol:              in.Symbol,
		Level:               level,
		Score:               roundTo(score, 2),
		UtilisationPercent:  roundTo(utilisationPercent, 2),
		NetExposure:         in.Exposure.NetExposure,
		GrossExposure:       in.Exposure.GrossExposure,
		PotentialLiability:  in.Exposure.PotentialLiability,
		ExpiryLiability:     in.Exposure.ExpiryLiability,
		Reasons:             reasons,
		HedgeDecision:       hedge.Decision,
		TargetHedgeRatio:    hedge.Ratio,
		TargetHedgeNotional: hedge.Notional,
		EvaluatedAt:         util.NowMs(),
		HedgeTarget:         detail,
	}
}

// ClassifyRiskLevel: bands are derived from the configured critical
// threshold, never hardcoded. Only CriticalExposurePercent and
// HedgeThresholdPercent are read.
func ClassifyRiskLevel(score float64, cfg *models.RiskConfig) models.RiskLevel {
	critical := cfg.CriticalExposurePercent
	high := critical * 0.7
	medium := math.Min(cfg.HedgeThresholdPercent, high)
	switch {
	case score >= critical:
		return models.RiskLevelCritical
	case score >= high:
		return models.RiskLevelHigh
	case score >= medium:
		return models.RiskLevelMedium
	}
	return models.RiskLevelLow
}

// SelectHedgeLevel returns nil when levels is empty.
func SelectHedgeLevel(utilisationPercent float64, levels []models.HedgeLevel) *models.HedgeLevel {
	sorted := append([]models.HedgeLevel(nil), levels...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].FromPercent < sorted[j].FromPercent })
	for i := range sorted {
		if utilisationPercent >= sorted[i].FromPercent && utilisationPercent < sorted[i].ToPercent {
			return &sorted[i]
		}
	}
	if len(sorted) == 0 {
		return nil
	}
	return &sorted[len(sorted)-1]
}

type HedgeTargetInput struct {
	RiskInputs
	UtilisationPercent float64
	Level              models.RiskLevel
}

type HedgeTarget struct {
	Decision models.HedgeDecision `json:"decision"`
	// h, the fraction of the book delta hedged.
	Ratio float64 `json:"ratio"`
	// |target|, minor units.
	Notional  int64                    `json:"notional"`
	LevelName string                   `json:"levelName"`
	Detail    models.HedgeTargetDetail `json:"detail"`
}

// ComputeHedgeTarget is the hedge sizing (docs/audit/hedging/hedging-spec.md
// section 3; hedgetarget.go has the formulas):
//
//	target = clamp(h x bookDelta, +-min(maxHedgeNotional, multiple x expiryLiability))
//
// The utilisation band (cfg.hedgeLevels, threshold) decides h, i.e. HOW MUCH
// of the book's sensitivity is hedged; the delta decides the size and the
// direction. Hedging is an aggregate, book-level action: nothing here reads
// or can affect an individual customer's trade (rule 8).
func ComputeHedgeTarget(in HedgeTargetInput) HedgeTarget {
	cfg := in.Config
	level := SelectHedgeLevel(in.UtilisationPercent, cfg.HedgeLevels)
	levelName := "NO_HEDGE"
	if level != nil {
		levelName = level.Name
	}
	out := HedgeTarget{Decision: models.HedgeDecisionNoHedge, LevelName: levelName}
	d := &out.Detail
	d.GammaWindowSeconds = cfg.MinSecondsToExpiryForHedge
	d.MaxHedgeNotional = in.MaxHedgeNotional
	d.ExpiryLiability = in.Exposure.ExpiryLiability
	d.AssetCeiling = in.AssetTargetHedgeRatio
	d.TickVolatility = in.Volatility
	if level != nil {
		d.BandRatio = level.HedgeRatio
	}
	if in.Volatility > ElevatedVolatility {
		d.VolatilityBoost = cfg.VolatilityHedgeBoost
	}

	threshold := cfg.HedgeThresholdPercent
	if in.AssetHedgeThresholdPercent != nil {
		threshold = *in.AssetHedgeThresholdPercent
	}

	book := in.HedgeBook
	if book != nil {
		d.Sigma, d.SigmaSource, d.ReferencePrice, d.DecidedAt = book.Sigma, book.SigmaSource, book.Price, book.NowMs
		d.LiabilityMultiple = book.LiabilityMultiple
		delta, eligible, excluded := BookDelta(book.Legs, book.Price, book.Sigma, book.NowMs, cfg.MinSecondsToExpiryForHedge)
		d.BookDelta = int64(util.RoundHalfUp(delta))
		d.EligibleTrades, d.ExcludedTrades = eligible, excluded
	}

	switch {
	case !in.HedgeEnabled:
		d.Reason = "hedging disabled for this asset"
	case book == nil:
		d.Reason = "no book data"
	case level == nil || level.HedgeRatio <= 0:
		d.Reason = "utilisation in a band with no hedge"
	case in.UtilisationPercent < threshold:
		d.Reason = "utilisation below the hedge threshold"
	case !(book.Price > 0):
		d.Reason = "no reference price"
	case !(book.Sigma > 0):
		d.Reason = "no volatility estimate"
	case d.EligibleTrades == 0:
		d.Reason = "no open trade outside the gamma window"
	}
	if d.Reason != "" {
		return out
	}

	h := HedgeRatioFor(level, d.VolatilityBoost, in.AssetTargetHedgeRatio)
	d.HedgeRatio = h
	raw := h * float64(d.BookDelta)
	d.RawTarget = int64(util.RoundHalfUp(raw))
	d.TargetSigned, d.Cap, d.CappedBy = CapTarget(raw, in.MaxHedgeNotional, book.LiabilityMultiple, in.Exposure.ExpiryLiability)
	if d.TargetSigned == 0 {
		d.Reason = "book delta is zero after caps"
		return out
	}
	out.Ratio = roundTo(h, 4)
	out.Notional = absInt64(d.TargetSigned)
	out.Decision = toHedgeDecision(levelName)
	return out
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func toHedgeDecision(levelName string) models.HedgeDecision {
	switch d := models.HedgeDecision(levelName); d {
	case models.HedgeDecisionPartialHedge, models.HedgeDecisionHighHedge, models.HedgeDecisionMaxAllowedHedge:
		return d
	}
	return models.HedgeDecisionNoHedge
}

// AssessAsset gathers the live inputs for one asset and runs the pure
// assessment, caching the result in Redis.
func AssessAsset(ctx context.Context, assetID string) (models.RiskAssessment, error) {
	a, _, err := AssessAssetDetailed(ctx, assetID)
	return a, err
}

// AssessAssetDetailed is AssessAsset that also returns the inputs it used,
// so a hedge decision can store them and be replayed (rule 9).
func AssessAssetDetailed(ctx context.Context, assetID string) (models.RiskAssessment, RiskInputs, error) {
	asset, err := assets.RequireAssetDoc(ctx, assetID)
	if err != nil {
		return models.RiskAssessment{}, RiskInputs{}, err
	}

	var (
		snap       models.ExposureSnapshot
		cfg        *models.RiskConfig
		portfolio  exposure.PortfolioExposure
		volatility float64
		health     models.FeedHealthSnapshot
		hedged     int64
		legs       []models.HedgeLeg
		tick       *models.NormalisedTick
	)
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() (err error) { snap, err = exposure.GetExposure(gctx, assetID, asset.Symbol); return })
	g.Go(func() (err error) { cfg, err = riskconfig.GetRiskConfig(gctx); return })
	g.Go(func() (err error) { portfolio, err = exposure.GetPortfolioExposure(gctx); return })
	g.Go(func() (err error) { volatility, err = marketdata.GetRealisedVolatility(gctx, assetID, 0); return })
	g.Go(func() (err error) { health, err = marketdata.GetFeedHealth(gctx, assetID, asset.Symbol); return })
	g.Go(func() (err error) { hedged, err = getHedgedNotional(gctx, asset.ID); return })
	g.Go(func() (err error) { legs, err = loadHedgeLegs(gctx, asset.ID); return })
	g.Go(func() (err error) { tick, err = marketdata.GetLastTick(gctx, assetID); return })
	if err := g.Wait(); err != nil {
		return models.RiskAssessment{}, RiskInputs{}, err
	}
	now := util.NowMs()
	nearestExpry := secondsToNearestExpiry(legs, now)
	sigma, sigmaSource, err := hedgeSigma(ctx, assetID, volatility)
	if err != nil {
		return models.RiskAssessment{}, RiskInputs{}, err
	}
	book := &HedgeBook{Legs: legs, Sigma: sigma, SigmaSource: sigmaSource, NowMs: now, LiabilityMultiple: hedgeLiabilityMultiple()}
	if tick != nil {
		book.Price = tick.Price
	}
	unavailable := ""
	if HedgeAvailability != nil && asset.HedgeEnabled {
		if ok, why := HedgeAvailability(ctx, assetID); !ok {
			unavailable = why
		}
	}

	in := RiskInputs{
		AssetID:                    assetID,
		Symbol:                     asset.Symbol,
		Exposure:                   snap,
		AssetExposureLimit:         asset.ExposureLimit,
		AssetNetExposureLimit:      asset.NetExposureLimit,
		HedgeEnabled:               asset.HedgeEnabled,
		AssetTargetHedgeRatio:      asset.TargetHedgeRatio,
		AssetHedgeThresholdPercent: asset.HedgeThresholdPercent,
		MinHedgeNotional:           asset.MinHedgeNotional,
		MaxHedgeNotional:           asset.MaxHedgeNotional,
		Config:                     cfg,
		GlobalGrossExposure:        portfolio.TotalGrossExposure,
		Volatility:                 volatility,
		FeedStatus:                 health.Status,
		SecondsToNearestExpiry:     nearestExpry,
		ExistingHedgeNotional:      hedged,
		PlatformLiability:          portfolio.TotalExpiryLiability,
		HedgeBook:                  book,
		HedgeUnavailableReason:     unavailable,
	}
	assessment := AssessRisk(in)

	if err := cacheAssessment(ctx, assessment); err != nil {
		return models.RiskAssessment{}, RiskInputs{}, err
	}
	return assessment, in, nil
}

// AssessAllAssets assesses every asset concurrently and broadcasts the list
// on the admin risk channel.
func AssessAllAssets(ctx context.Context) ([]models.RiskAssessment, error) {
	list, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]models.RiskAssessment, len(list))
	g, gctx := errgroup.WithContext(ctx)
	for i, a := range list {
		g.Go(func() (err error) {
			out[i], err = AssessAsset(gctx, a.ID.Hex())
			return
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	ws.Emit([]string{config.ChAdminRisk}, config.EvRiskUpdated, out)
	return out, nil
}

func cacheAssessment(ctx context.Context, a models.RiskAssessment) error {
	fields := map[string]any{
		"level":               string(a.Level),
		"score":               jsNumber(a.Score),
		"utilisationPercent":  jsNumber(a.UtilisationPercent),
		"netExposure":         strconv.FormatInt(a.NetExposure, 10),
		"grossExposure":       strconv.FormatInt(a.GrossExposure, 10),
		"hedgeDecision":       string(a.HedgeDecision),
		"targetHedgeNotional": strconv.FormatInt(a.TargetHedgeNotional, 10),
		"evaluatedAt":         strconv.FormatInt(a.EvaluatedAt, 10),
	}
	if h := a.HedgeTarget; h != nil {
		fields["hedgeTargetSigned"] = strconv.FormatInt(h.TargetSigned, 10)
		fields["hedgeBookDelta"] = strconv.FormatInt(h.BookDelta, 10)
		fields["hedgeCappedBy"] = h.CappedBy
		fields["hedgeRatio"] = jsNumber(h.HedgeRatio)
		fields["hedgeSigma"] = strconv.FormatFloat(h.Sigma, 'g', -1, 64)
		fields["hedgeSigmaSource"] = h.SigmaSource
		fields["hedgeTickVolatility"] = strconv.FormatFloat(h.TickVolatility, 'g', -1, 64)
	}
	return redisx.R().HSet(ctx, config.RedisKeys.RiskState(a.AssetID), fields).Err()
}

// jsNumber is JS String(n) for the 2-decimal values stored in the cache.
func jsNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// GetCachedAssessment returns nil when the asset has never been assessed;
// otherwise it re-assesses (as the TS does).
func GetCachedAssessment(ctx context.Context, assetID string) (*models.RiskAssessment, error) {
	n, err := redisx.R().Exists(ctx, config.RedisKeys.RiskState(assetID)).Result()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, nil
	}
	a, err := AssessAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// secondsToNearestExpiry is the time to the soonest open trade's expiry
// (rounded seconds, >= 0), nil for an empty book.
func secondsToNearestExpiry(legs []models.HedgeLeg, nowMs int64) *int64 {
	if len(legs) == 0 {
		return nil
	}
	next := legs[0].ExpiryMs
	for _, l := range legs[1:] {
		next = min(next, l.ExpiryMs)
	}
	secs := max(0, int64(util.RoundHalfUp(float64(next-nowMs)/1000)))
	return &secs
}

// getHedgedNotional is the protection actually held on an asset: the INR
// notional of its filled hedge position at the last mark (hedge_positions,
// written by the hedging package). Working orders do not count.
func getHedgedNotional(ctx context.Context, assetID bson.ObjectID) (int64, error) {
	var p struct {
		NotionalInr int64 `bson:"notionalInr"`
	}
	err := mongox.C(models.CollHedgePositions).FindOne(ctx, bson.M{"assetId": assetID},
		options.FindOne().SetProjection(bson.M{"notionalInr": 1})).Decode(&p)
	if mongox.IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return p.NotionalInr, nil
}

// GetHedgedNotionalByAsset is the filled hedge notional per asset (minor
// units, absolute, at the last mark), placed here because the risk worker
// needs it and hedging imports risk. The hedging package calls this rather
// than re-implement it.
func GetHedgedNotionalByAsset(ctx context.Context) (map[string]int64, error) {
	cur, err := mongox.C(models.CollHedgePositions).Find(ctx, bson.M{},
		options.Find().SetProjection(bson.M{"assetId": 1, "notionalInr": 1}))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		AssetID     bson.ObjectID `bson:"assetId"`
		NotionalInr int64         `bson:"notionalInr"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		if r.NotionalInr != 0 {
			out[r.AssetID.Hex()] = r.NotionalInr
		}
	}
	return out, nil
}

type TradeLimitInput struct {
	Stake     int64
	Profit    int64
	Direction models.Direction
	// Reads Symbol, MinStake, MaxStake, ExposureLimit, NetExposureLimit.
	Asset *models.Asset
	// Reads MinTradeAmount, MaxTradeAmount, MaxAssetExposure, MaxGrossExposure,
	// MaxUserExposure, MaxOpenTradesPerUser, MaxUserAssetConcentrationPercent.
	Config              *models.RiskConfig
	Exposure            models.ExposureSnapshot
	GlobalGrossExposure int64
	UserTotalExposure   int64
	UserAssetExposure   int64
	UserOpenTrades      int64
	UserMaxTradeAmount  *int64
	UserMaxExposure     *int64
	// A DEMO trade is checked only against the stake bounds and the open
	// trade count: it adds nothing to the house book, so the exposure,
	// platform and concentration limits do not apply to it.
	Demo bool
	// Liability: the committed expiry liability of the asset and of the
	// platform, and the committed counters of the expiry bucket the trade
	// falls into. Read only when a liability limit is configured.
	AssetLiability    int64
	PlatformLiability int64
	Bucket            exposure.BucketCounters
}

type LimitViolation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func i64(n int64) string { return strconv.FormatInt(n, 10) }

// CheckTradeLimits is the pure pre-trade limit check (spec section 8).
//
// Returns every violation rather than the first, so the audit trail records
// the full reason a trade was refused.
func CheckTradeLimits(in TradeLimitInput) []LimitViolation {
	violations := []LimitViolation{}
	asset, cfg, exp, stake := in.Asset, in.Config, in.Exposure, in.Stake

	minStake := max(asset.MinStake, cfg.MinTradeAmount)
	userMaxTrade := int64(maxSafeInteger)
	if in.UserMaxTradeAmount != nil {
		userMaxTrade = *in.UserMaxTradeAmount
	}
	maxStake := min(asset.MaxStake, cfg.MaxTradeAmount, userMaxTrade)

	if stake < minStake {
		violations = append(violations, LimitViolation{apperr.StakeBelowMin, "Minimum stake for " + asset.Symbol + " is " + i64(minStake)})
	}
	if stake > maxStake {
		violations = append(violations, LimitViolation{apperr.StakeAboveMax, "Maximum stake for " + asset.Symbol + " is " + i64(maxStake)})
	}

	if in.UserOpenTrades >= cfg.MaxOpenTradesPerUser {
		violations = append(violations, LimitViolation{
			apperr.UserLimitExceeded,
			"You already have the maximum of " + i64(cfg.MaxOpenTradesPerUser) + " open trades",
		})
	}

	if in.Demo {
		return violations
	}

	userExposureLimit := cfg.MaxUserExposure
	if in.UserMaxExposure != nil {
		userExposureLimit = *in.UserMaxExposure
	}
	if in.UserTotalExposure+stake > userExposureLimit {
		violations = append(violations, violationFor(exposure.RuleUserExposure, asset.Symbol))
	}

	projectedGross := exp.GrossExposure + stake
	if projectedGross > min(asset.ExposureLimit, cfg.MaxAssetExposure) {
		violations = append(violations, violationFor(exposure.RuleAssetGross, asset.Symbol))
	}

	projectedNet := exp.NetExposure - stake
	if in.Direction == models.DirectionUp {
		projectedNet = exp.NetExposure + stake
	}
	if projectedNet < 0 {
		projectedNet = -projectedNet
	}
	if projectedNet > asset.NetExposureLimit {
		violations = append(violations, violationFor(exposure.RuleAssetNet, asset.Symbol))
	}

	if in.GlobalGrossExposure+stake > cfg.MaxGrossExposure {
		violations = append(violations, violationFor(exposure.RulePlatformGross, asset.Symbol))
	}

	// Concentration. A share of the book is only meaningful once the book is
	// bigger than any single customer is allowed to hold. Below that the
	// per-user exposure limit is already the binding constraint, and a
	// percentage rule would block the first customer to trade a newly listed
	// asset - who is unavoidably 100% of an empty book.
	if projectedGross > userExposureLimit {
		concentration := util.SafePercent(float64(in.UserAssetExposure+stake), float64(projectedGross))
		if concentration > cfg.MaxUserAssetConcentrationPercent {
			violations = append(violations, violationFor(exposure.RuleConcentration, asset.Symbol))
		}
	}

	// Liability: what the platform can lose, from the payout frozen on each
	// trade, with UP and DOWN offsetting only inside one expiry bucket. A
	// trade that does not raise the bucket's worst case is never refused here
	// - refusing it would keep the book exactly as exposed as it is.
	before := exposure.BucketLiability(in.Bucket)
	after := exposure.BucketLiability(in.Bucket.With(in.Direction, stake, in.Profit))
	if after > before {
		if cfg.MaxExpiryLiability > 0 && after > cfg.MaxExpiryLiability {
			violations = append(violations, violationFor(exposure.RuleExpiryLiability, asset.Symbol))
		}
		if cfg.MaxAssetLiability > 0 && in.AssetLiability-before+after > cfg.MaxAssetLiability {
			violations = append(violations, violationFor(exposure.RuleAssetLiability, asset.Symbol))
		}
		if cfg.MaxPlatformLiability > 0 && in.PlatformLiability-before+after > cfg.MaxPlatformLiability {
			violations = append(violations, violationFor(exposure.RulePlatformLiability, asset.Symbol))
		}
	}

	return violations
}

// violationFor is the code and message of an exposure rule, shared by the
// pure check and the atomic reservation so both refuse in the same words.
func violationFor(rule, symbol string) LimitViolation {
	switch rule {
	case exposure.RuleUserExposure:
		return LimitViolation{apperr.UserLimitExceeded, "This trade would exceed your open exposure limit"}
	case exposure.RuleAssetGross:
		return LimitViolation{apperr.ExposureLimitExceeded, "Exposure limit reached for " + symbol}
	case exposure.RuleAssetNet:
		return LimitViolation{apperr.ExposureLimitExceeded, "One-sided exposure limit reached for " + symbol}
	case exposure.RulePlatformGross:
		return LimitViolation{apperr.RiskLimitExceeded, "Platform exposure limit reached, please try again shortly"}
	case exposure.RuleConcentration:
		return LimitViolation{apperr.RiskLimitExceeded, "Your share of the open book on " + symbol + " is too large"}
	case exposure.RuleExpiryLiability:
		return LimitViolation{apperr.ExposureLimitExceeded, "Liability limit reached for " + symbol + " at this expiry"}
	case exposure.RuleAssetLiability:
		return LimitViolation{apperr.ExposureLimitExceeded, "Liability limit reached for " + symbol}
	case exposure.RulePlatformLiability:
		return LimitViolation{apperr.RiskLimitExceeded, "Platform liability limit reached, please try again shortly"}
	}
	return LimitViolation{apperr.RiskLimitExceeded, "Risk limit reached, please try again shortly"}
}

type TradeRiskContext struct {
	UserID             string
	Asset              *models.Asset
	Stake              int64
	Profit             int64
	Direction          models.Direction
	UserMaxTradeAmount *int64
	UserMaxExposure    *int64
	// DEMO account: house-level limits are skipped (see TradeLimitInput.Demo).
	Demo bool
	// The trade's expiry, for its liability bucket (0 = unknown: the bucket
	// is taken as empty).
	ExpirySeconds int64
}

// AssertTradeWithinRiskLimits gathers live state and returns the first
// violation as an AppError (400) with every violation in details.
func AssertTradeWithinRiskLimits(ctx context.Context, c TradeRiskContext) error {
	assetID := c.Asset.ID.Hex()
	userOID, ok := mongox.ParseID(c.UserID)
	if !ok {
		return apperr.Validation("Invalid user id", nil)
	}

	var (
		cfg        *models.RiskConfig
		snap       models.ExposureSnapshot
		portfolio  exposure.PortfolioExposure
		userExp    exposure.UserExposure
		openTrades int64
		bucket     exposure.BucketCounters
	)
	g, gctx := errgroup.WithContext(ctx)
	account := models.AccountLive
	if c.Demo {
		account = models.AccountDemo
	}
	g.Go(func() (err error) { cfg, err = riskconfig.GetRiskConfig(gctx); return })
	if !c.Demo {
		g.Go(func() (err error) { snap, err = exposure.GetExposure(gctx, assetID, c.Asset.Symbol); return })
		g.Go(func() (err error) { portfolio, err = exposure.GetPortfolioExposure(gctx); return })
		g.Go(func() (err error) { userExp, err = exposure.GetUserExposure(gctx, c.UserID); return })
		if c.ExpirySeconds > 0 {
			// The trade's own expiry is fixed a moment later; this pre-check
			// estimates its bucket, the atomic reservation uses the exact one.
			expiry := util.NowMs() + c.ExpirySeconds*1000
			g.Go(func() (err error) { bucket, err = exposure.GetBucketCounters(gctx, assetID, expiry); return })
		}
	}
	// The open trade limit is per account, so practising cannot block real trading.
	g.Go(func() (err error) {
		openTrades, err = mongox.C(models.CollBinaryTrades).CountDocuments(gctx,
			bson.M{"userId": userOID, "status": string(models.TradeStatusOpen), "accountType": models.AccountFilter(account)})
		return
	})
	if err := g.Wait(); err != nil {
		return err
	}

	violations := CheckTradeLimits(TradeLimitInput{
		Stake:               c.Stake,
		Profit:              c.Profit,
		Direction:           c.Direction,
		Asset:               c.Asset,
		Config:              cfg,
		Exposure:            snap,
		GlobalGrossExposure: portfolio.TotalGrossExposure,
		UserTotalExposure:   userExp.Total,
		UserAssetExposure:   userExp.PerAsset[assetID],
		UserOpenTrades:      openTrades,
		UserMaxTradeAmount:  c.UserMaxTradeAmount,
		UserMaxExposure:     c.UserMaxExposure,
		Demo:                c.Demo,
		AssetLiability:      snap.ExpiryLiability,
		PlatformLiability:   portfolio.TotalExpiryLiability,
		Bucket:              bucket,
	})
	if len(violations) > 0 {
		first := violations[0]
		return apperr.New(first.Code, first.Message, 400, map[string]any{"violations": violations})
	}
	return nil
}
