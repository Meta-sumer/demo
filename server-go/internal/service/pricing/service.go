package pricing

import (
	"context"
	"errors"
	"math"
	"slices"
	"strconv"
	"strings"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/riskconfig"
)

// Request is one pricing question.
type Request struct {
	AssetID string
	// Optional, when the caller already holds them.
	Asset         *models.Asset
	Payout        *models.PayoutConfig
	Direction     models.Direction
	ExpirySeconds int64
	// Minor units; 0 prices the reference stake (the smallest tradable).
	Stake       int64
	AccountType string
	// The tick to price on (trade acceptance passes the one its feed gate
	// returned); nil reads the last accepted quote.
	Tick *models.NormalisedTick
	// Epoch ms; 0 = now.
	Now int64
}

// Decision is an offer's terms (or the block) with everything they came from.
type Decision struct {
	OK     bool
	Code   string
	Reason string

	Mode        string
	RuleVersion string
	Config      *models.RiskConfig
	Rules       Rules
	Terms       Terms
	Payout      *models.PayoutConfig
	Asset       *models.Asset
	Model       *models.PricingModel

	// The payout as a fraction and as a percent (what trades store).
	PayoutFraction float64
	PayoutPercent  float64
	Components     models.PricingComponents
	Guard          models.PricingGuard

	Tick         *models.NormalisedTick
	Reference    float64
	FeedProvider string

	Direction     models.Direction
	ExpirySeconds int64
	Stake         int64
	AccountType   string
	Demo          bool
	DecidedAt     int64
}

// SourceAllowed reports whether the guard's model, calibrated on
// modelSource, may vouch for a feed from provider: the same source, or the
// simulated development feed where it may run at all (outside production, or
// with ALLOW_SIMULATED_FEED_IN_PRODUCTION) - prices there are generated, and
// the offer records the mismatch.
func SourceAllowed(modelSource, provider string) (ok, mismatch bool) {
	if strings.EqualFold(modelSource, provider) {
		return true, false
	}
	if provider == "simulated" && (!config.IsProduction() || (config.E != nil && config.E.AllowSimulatedFeedInProduction)) {
		return true, true
	}
	return false, true
}

func feedProviderOf(ctx context.Context, assetID string) string {
	if name, err := redisx.R().HGet(ctx, config.RedisKeys.LastTick(assetID), "provider").Result(); err == nil && name != "" {
		return name
	}
	if config.E != nil && config.E.MarketDataProvider != "" {
		return config.E.MarketDataProvider
	}
	return "unknown"
}

// ReferenceStake is the stake an indicative price is computed for.
func ReferenceStake(cfg *models.RiskConfig, asset *models.Asset, row *models.PayoutConfig) int64 {
	s := max(asset.MinStake, cfg.MinTradeAmount, 1)
	if row != nil && row.MinStake != nil {
		s = max(s, *row.MinStake)
	}
	return s
}

// StakeBounds are the effective stake limits of an (asset, expiry).
func StakeBounds(cfg *models.RiskConfig, asset *models.Asset, row *models.PayoutConfig) (int64, int64) {
	lo := max(asset.MinStake, cfg.MinTradeAmount)
	hi := min(asset.MaxStake, cfg.MaxTradeAmount)
	if row != nil && row.MinStake != nil {
		lo = max(lo, *row.MinStake)
	}
	if row != nil && row.MaxStake != nil {
		hi = min(hi, *row.MaxStake)
	}
	return lo, hi
}

// HaltFor is the halt in force for a symbol: the global kill switch first,
// then the symbol's own (spec 7.4: either one blocks).
func HaltFor(cfg *models.RiskConfig, symbol string) (code, reason string, halted bool) {
	if cfg.TradingHalted {
		r := cfg.TradingHaltReason
		if r == "" {
			r = "trading is halted"
		}
		return CodeTradingHalted, r, true
	}
	for _, h := range cfg.PricingHalts {
		if h.Symbol == symbol {
			return CodeSymbolHalted, h.Reason, true
		}
	}
	return "", "", false
}

func (d *Decision) refuse(code, reason string) *Decision {
	d.OK, d.Code, d.Reason, d.PayoutFraction, d.PayoutPercent = false, code, reason, 0, 0
	return d
}

// Evaluate prices one request in the specification's order (8.1): symbol
// known, not halted, expiry offered, stake in range, fresh market data, then
// volatility, exposure, payout and the final exposure check (ComputeOffer),
// and the calibrated guard. A block is a Decision with OK false; an error is
// a failure of the platform itself (a store unreachable).
func Evaluate(ctx context.Context, req Request) (*Decision, error) {
	now := req.Now
	if now == 0 {
		now = util.NowMs()
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	asset := req.Asset
	if asset == nil {
		if asset, err = assets.RequireAssetDoc(ctx, req.AssetID); err != nil {
			return nil, err
		}
	}
	assetID := asset.ID.Hex()
	d := &Decision{Mode: cfg.PricingMode, Config: cfg, Rules: RulesFrom(cfg), Asset: asset, Direction: req.Direction,
		ExpirySeconds: req.ExpirySeconds, AccountType: models.NormAccountType(req.AccountType), DecidedAt: now}
	d.Demo = d.AccountType == models.AccountDemo
	if d.Mode != models.PricingModeFixed {
		d.Mode = models.PricingModeDynamic
	}

	// 1. Symbol known (and in scope).
	terms, known := TermsFor(cfg, asset.Symbol, nil)
	d.Terms = terms
	if !known || !terms.Enabled {
		return d.refuse(CodeNotPriced, asset.Symbol+" is not priced: "+terms.DisabledReason), nil
	}
	// 2. Not halted.
	if code, reason, halted := HaltFor(cfg, asset.Symbol); halted {
		return d.refuse(code, reason), nil
	}
	// 3. Expiry offered.
	row := req.Payout
	if row == nil {
		if row, err = payouts.FindActivePayout(ctx, assetID, req.ExpirySeconds); err != nil {
			return nil, err
		}
	}
	if row == nil || !slices.Contains(asset.SupportedExpiries, req.ExpirySeconds) {
		return d.refuse(CodeExpiryNotOffered, strconv.FormatInt(req.ExpirySeconds, 10)+"s expiry is not offered on "+asset.Symbol), nil
	}
	d.Payout = row
	d.Terms, _ = TermsFor(cfg, asset.Symbol, row)
	d.RuleVersion = RuleVersion(cfg.PricingRuleLabel, cfg.Version, row.Version)
	// 4. Stake within bounds.
	d.Stake = req.Stake
	if d.Stake <= 0 {
		d.Stake = ReferenceStake(cfg, asset, row)
	}
	if lo, hi := StakeBounds(cfg, asset, row); d.Stake < lo || d.Stake > hi {
		return d.refuse(CodeStakeOutOfRange, "stake must be between "+strconv.FormatInt(lo, 10)+" and "+strconv.FormatInt(hi, 10)), nil
	}
	// 5. Fresh market data (the last accepted quote, by server receipt): the
	//    freshness rule shared with trade acceptance (marketdata.AssessFreshness:
	//    received within pricingStaleAfterMs, no feed data rule block - ticks
	//    dropped by a saturated buffer, a drifting clock - and delivered within
	//    the feed bands where the halt switches ask for it).
	tick := req.Tick
	if tick == nil {
		if tick, err = marketdata.GetLastTick(ctx, assetID); err != nil {
			return nil, err
		}
	}
	d.Tick = tick
	d.FeedProvider = feedProviderOf(ctx, assetID)
	if fresh, err := marketdata.CheckFreshness(ctx, assetID, tick, now); err != nil {
		return nil, err
	} else if !fresh.OK {
		return d.refuse(CodeStaleMarketData, "market data for "+asset.Symbol+" is not usable: "+fresh.Reason), nil
	}
	d.Reference = tick.Price
	spread := 0.0
	switch {
	case tick.SpreadBps != nil:
		spread = *tick.SpreadBps
	case tick.Bid != nil && tick.Ask != nil && tick.Price > 0:
		spread = (*tick.Ask - *tick.Bid) / tick.Price * 1e4
	}

	// Volatility: the shared EWMA (spec 5); a missing state is the baseline.
	sigma, _, err := sigmaFrom(ctx, assetID, d.Terms.BaselineVol)
	if err != nil {
		return nil, err
	}

	// Exposure: L(D) of the LIVE book (DEMO never touches it).
	var liability int64
	if !d.Demo {
		sl, err := exposure.GetSideLiability(ctx, assetID)
		if err != nil {
			return nil, err
		}
		liability = sl.Down
		if req.Direction == models.DirectionUp {
			liability = sl.Up
		}
	}
	hedge := HedgeCostEstimate{Available: true}
	if !d.Demo {
		est, herr := CurrentHedgeCostEstimator().EstimateHedgeCost(ctx, HedgeCostRequest{
			Asset: asset, Direction: req.Direction, Stake: d.Stake, ExpirySeconds: req.ExpirySeconds,
			Liability: liability, Price: tick.Price, Config: cfg,
		})
		if herr != nil {
			est = HedgeCostEstimate{Required: asset.HedgeEnabled, Available: false, Reason: "ESTIMATOR_ERROR", Basis: herr.Error()}
		}
		hedge = est
	}

	res := ComputeOffer(d.Rules, d.Terms, OfferInput{Direction: req.Direction, ExpirySeconds: req.ExpirySeconds,
		Stake: d.Stake, Liability: liability, Sigma: sigma, SpreadBps: spread, Hedge: hedge})
	d.Components = res.Components
	d.Components.QuoteTimestamp, d.Components.QuoteReceivedAt = tick.Timestamp, tick.ReceivedAt

	if d.Mode == models.PricingModeFixed {
		// The pre-engine behaviour: the row's percent. The checks above
		// (scope, halts, expiry, stake, fresh data) still apply; the
		// formula's terms are recorded for comparison only.
		d.OK, d.PayoutFraction, d.PayoutPercent = true, row.PayoutPercent/100, row.PayoutPercent
		return d, nil
	}
	if !res.OK {
		return d.refuse(res.Code, res.Reason), nil
	}
	d.PayoutFraction = res.Components.Payout
	d.PayoutPercent = PayoutPercent(res.Components.Payout)

	// The calibrated guard (audit F-02, F-03).
	if d.Rules.GuardEnabled {
		if err := d.applyGuard(ctx, assetID, now); err != nil {
			return nil, err
		}
		if !d.Guard.OK {
			return d.refuse(CodeGuard, "payout above the calibrated break-even ("+d.Guard.Reason+")"), nil
		}
	}
	d.OK = true
	return d, nil
}

func (d *Decision) applyGuard(ctx context.Context, assetID string, now int64) error {
	model, err := ActiveModel(ctx)
	if err != nil && !errors.Is(err, ErrNoModel) {
		return err
	}
	if err != nil {
		logx.Warn("no pricing guard model available", "err", err.Error())
	}
	d.Model = model
	in := GuardInput{Model: model, Direction: d.Direction, Payout: d.PayoutFraction, DrawPolicy: d.Rules.DrawPolicy,
		MinEdge: d.Rules.GuardMinEdge, Conservative: d.Rules.GuardConservative}
	in.Entry = FindEntry(model, d.Asset.ProviderSymbol, d.ExpirySeconds)
	if in.Entry != nil && in.Entry.Status == models.PricingEntryCalibrated {
		in.SourceOK, in.Mismatch = SourceAllowed(in.Entry.PriceSource, d.FeedProvider)
		in.Features, in.FeaturesOK, err = LiveFeatures(ctx, assetID, in.Entry.Features, d.Tick.Price, now)
		if err != nil {
			return err
		}
	}
	d.Guard = Guard(in)
	return nil
}

// RefusalError is the API error for a block.
func (d *Decision) RefusalError() error {
	if d.OK {
		return nil
	}
	details := map[string]any{"reason": d.Code, "detail": d.Reason, "symbol": d.Asset.Symbol,
		"expirySeconds": d.ExpirySeconds, "direction": d.Direction}
	switch d.Code {
	case CodeTradingHalted, CodeSymbolHalted:
		return apperr.New(apperr.TradingHalted, "Trading on "+d.Asset.Symbol+" is halted: "+d.Reason, 503, details)
	case CodeStaleMarketData:
		return apperr.New(apperr.FeedUnhealthy, "Market data for "+d.Asset.Symbol+" is not fresh. New trades are paused.", 503, details)
	case CodeExposureLimit:
		return apperr.New(apperr.ExposureLimitExceeded, "Exposure limit reached for "+d.Asset.Symbol+" on this side", 400, details)
	case CodeExpiryNotOffered:
		return apperr.New(apperr.ExpiryNotSupported, d.Reason, 400, details)
	}
	return apperr.New(apperr.PricingUnavailable, "This trade cannot be priced right now: "+d.Reason, 503, details)
}

// ModelVersion is the guard model's version (0 = none).
func (d *Decision) ModelVersion() int64 {
	if d.Model == nil {
		return d.Guard.ModelVersion
	}
	return d.Model.Version
}

// Record is the pricing record frozen on a trade priced at acceptance.
func (d *Decision) Record() *models.TradePricing {
	r := &models.TradePricing{
		Mode: d.Mode, RuleVersion: d.RuleVersion, RiskConfigVer: d.Config.Version, PayoutPercent: d.PayoutPercent,
		ReferencePrice: d.Reference, AcceptMid: d.Reference, FeedProvider: d.FeedProvider,
		Components: d.Components, Guard: d.Guard, Inline: true, OfferCreatedAt: d.DecidedAt, OfferValidUntil: d.DecidedAt,
	}
	if d.Payout != nil {
		r.PayoutCfgVer = d.Payout.Version
	}
	return r
}

// slippageBps is |mid / reference - 1| x 10^4.
func slippageBps(mid, reference float64) float64 {
	if !(reference > 0) {
		return math.Inf(1)
	}
	return math.Abs(mid/reference-1) * 1e4
}
