package hedging

import (
	"context"
	"math"
	"strconv"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
)

// Hedge cost model (hedging-spec.md section 7; audit finding F-10, hedging
// side). Basis points of hedged notional:
//
//	perSide   = takerFee + halfSpread + slippage
//	roundTrip = 2 x perSide + funding
//	takerFee  = the account's rate from the venue (commissionRate), else HEDGE_TAKER_FEE_BPS
//	halfSpread= (ask - bid) / mid / 2, from the venue book (else HEDGE_FALLBACK_SPREAD_BPS / 2)
//	slippage  = HEDGE_SLIPPAGE_BPS
//	funding   = |lastFundingRate| x holdSeconds / 8h   (always charged as a cost)
//
// Availability, in this order: the asset has a venue symbol (HEDGE_SYMBOLS)
// and hedging enabled; the venue can trade (keys, live switch); the INR rate
// is fresh; the venue snapshot is fresh and healthy. A round trip above
// HEDGE_MAX_COST_BPS is "expensive". Pricing widens or refuses on either
// verdict, which is how the capacity for unhedged risk shrinks.

// Unavailability reasons.
const (
	ReasonNotHedgeable    = "NOT_HEDGEABLE"
	ReasonHedgeDisabled   = "HEDGE_DISABLED"
	ReasonVenueUnavail    = "VENUE_UNAVAILABLE"
	ReasonVenueStale      = "VENUE_STALE"
	ReasonCostLimit       = "COST_LIMIT"
	ReasonHedgeCapacity   = "HEDGE_CAPACITY"
	ReasonNoVolatility    = "NO_VOLATILITY"
	DefaultHoldSeconds    = 300
	fundingIntervalSecond = 8 * 3600
)

// HedgeCost is a cost estimate for hedging Notional (minor units) of an asset.
type HedgeCost struct {
	AssetID     string `json:"assetId"`
	Symbol      string `json:"symbol"`
	VenueSymbol string `json:"venueSymbol"`
	Provider    string `json:"provider"`
	Venue       string `json:"venue"`
	Notional    int64  `json:"notional"`
	HoldSeconds int64  `json:"holdSeconds"`
	// A hedge can be placed now. Reason says why not (or COST_LIMIT when
	// available but expensive).
	Available bool   `json:"available"`
	Expensive bool   `json:"expensive"`
	Reason    string `json:"reason,omitempty"`
	models.HedgeCostBps
	// Round-trip cost of Notional, minor units.
	CostInr        int64   `json:"costInr"`
	MaxCostBps     float64 `json:"maxCostBps"`
	InrPerUsdt     float64 `json:"inrPerUsdt"`
	FxAsOf         int64   `json:"fxAsOf"`
	FxAgeMs        int64   `json:"fxAgeMs"`
	VenueAgeMs     int64   `json:"venueAgeMs"`
	VenuePrice     float64 `json:"venuePrice"`
	MinNotionalInr int64   `json:"minNotionalInr"`
	FeeSource      string  `json:"feeSource"`
	SpreadSource   string  `json:"spreadSource"`
}

func envFloat(get func(*config.Env) float64, def float64) float64 {
	if config.E != nil {
		return get(config.E)
	}
	return def
}

// ComputeCostBps is the pure cost formula.
func ComputeCostBps(snap VenueSnapshot, holdSeconds int64, takerFeeBps, slippageBps, fallbackSpreadBps float64) (models.HedgeCostBps, string, string) {
	var c models.HedgeCostBps
	feeSource, spreadSource := "config", "config"
	c.TakerFeeBps = takerFeeBps
	if snap.TakerRate > 0 {
		c.TakerFeeBps, feeSource = snap.TakerRate*10_000, "venue"
	}
	c.HalfSpreadBps = fallbackSpreadBps / 2
	if mid := snap.Mid(); mid > 0 && snap.Ask >= snap.Bid {
		c.HalfSpreadBps, spreadSource = (snap.Ask-snap.Bid)/mid/2*10_000, "venue"
	}
	c.SlippageBps = slippageBps
	c.FundingBps = math.Abs(snap.FundingRate) * float64(max(0, holdSeconds)) / fundingIntervalSecond * 10_000
	c.PerSideBps = c.TakerFeeBps + c.HalfSpreadBps + c.SlippageBps
	c.RoundTripBps = 2*c.PerSideBps + c.FundingBps
	for _, p := range []*float64{&c.TakerFeeBps, &c.HalfSpreadBps, &c.SlippageBps, &c.FundingBps, &c.PerSideBps, &c.RoundTripBps} {
		*p = math.Round(*p*10_000) / 10_000
	}
	return c, feeSource, spreadSource
}

// CostQuery asks for the cost of hedging Notional for HoldSeconds.
type CostQuery struct {
	AssetID     string
	Notional    int64
	HoldSeconds int64
}

// CostEstimate is the cost of hedging notional (minor units, INR) of an
// asset, held DefaultHoldSeconds. Safe for concurrent use; reads Redis and a
// cached FX row only, never the venue.
func CostEstimate(ctx context.Context, assetID string, notional int64) (HedgeCost, error) {
	return CostEstimateFor(ctx, CostQuery{AssetID: assetID, Notional: notional, HoldSeconds: DefaultHoldSeconds})
}

// CostEstimateFor is CostEstimate with an explicit holding period.
func CostEstimateFor(ctx context.Context, q CostQuery) (HedgeCost, error) {
	asset, err := assets.RequireAssetDoc(ctx, q.AssetID)
	if err != nil {
		return HedgeCost{}, err
	}
	v := venue()
	out := HedgeCost{AssetID: q.AssetID, Symbol: asset.Symbol, Notional: q.Notional, HoldSeconds: q.HoldSeconds,
		Provider: v.Name(), Venue: v.ID(), MaxCostBps: envFloat(func(e *config.Env) float64 { return e.HedgeMaxCostBps }, 30)}
	out.VenueSymbol = config.E.HedgeVenueSymbol(asset.Symbol)
	unavailable := func(reason string) (HedgeCost, error) {
		out.Available, out.Reason = false, reason
		return out, nil
	}
	if out.VenueSymbol == "" {
		return unavailable(ReasonNotHedgeable)
	}
	if !asset.HedgeEnabled {
		return unavailable(ReasonHedgeDisabled)
	}
	if err := v.CanTrade(); err != nil {
		code := ReasonVenueUnavail
		if ae, ok := apperr.As(err); ok {
			code = ae.Code
		}
		return unavailable(code)
	}
	fx, err := CurrentFX(ctx)
	if err != nil {
		return HedgeCost{}, err
	}
	out.InrPerUsdt, out.FxAsOf, out.FxAgeMs = fx.Rate, fx.AsOf, fx.AgeMs
	if !fx.Usable() {
		return unavailable(fx.Reason())
	}
	snap, err := GetVenueSnapshot(ctx, out.VenueSymbol)
	if err != nil {
		return HedgeCost{}, err
	}
	if snap == nil || !snap.OK {
		return unavailable(ReasonVenueUnavail)
	}
	out.VenueAgeMs = util.NowMs() - snap.UpdatedAt
	if out.VenueAgeMs > venueMaxAge() {
		return unavailable(ReasonVenueStale)
	}
	out.VenuePrice = snap.Mid()
	out.HedgeCostBps, out.FeeSource, out.SpreadSource = ComputeCostBps(*snap, q.HoldSeconds,
		envFloat(func(e *config.Env) float64 { return e.HedgeTakerFeeBps }, 5),
		envFloat(func(e *config.Env) float64 { return e.HedgeSlippageBps }, 2),
		envFloat(func(e *config.Env) float64 { return e.HedgeFallbackSpreadBps }, 1))
	out.CostInr = int64(util.RoundHalfUp(float64(absI(q.Notional)) * out.RoundTripBps / 10_000))
	out.MinNotionalInr = int64(util.RoundHalfUp(snap.Rules.MinNotional * fx.Rate * 100))
	out.Available = true
	if out.RoundTripBps > out.MaxCostBps {
		out.Expensive, out.Reason = true, ReasonCostLimit
	}
	return out, nil
}

// TradeCostInput describes a trade being priced (what pricing's
// HedgeCostRequest carries). Money is minor units.
type TradeCostInput struct {
	AssetID   string
	Direction models.Direction
	Stake     int64
	// Profit percent the trade could be paid (0 = assume 100, the
	// conservative bound).
	PayoutPercent float64
	// How much the trade grows |net exposure| (0 = it does not).
	NetIncrease        int64
	UtilisationPercent float64
	ExpirySeconds      int64
}

// TradeHedgeCost is the hedge a trade causes and what it costs.
type TradeHedgeCost struct {
	Required  bool   `json:"required"`
	Available bool   `json:"available"`
	Expensive bool   `json:"expensive"`
	Reason    string `json:"reason,omitempty"`
	// h, and the trade's entry delta-equivalent and hedged notional.
	HedgeRatio    float64 `json:"hedgeRatio"`
	DeltaNotional int64   `json:"deltaNotional"`
	HedgeNotional int64   `json:"hedgeNotional"`
	Sigma         float64 `json:"sigma"`
	// Expected round-trip hedge cost as a fraction of the stake.
	CostPerStake float64   `json:"costPerStake"`
	Cost         HedgeCost `json:"cost"`
	// Ready-made for pricing.HedgeCostEstimate: their product is
	// CostPerStake (HedgeRatio x CostFraction).
	PricingHedgeRatio   float64 `json:"pricingHedgeRatio"`
	PricingCostFraction float64 `json:"pricingCostFraction"`
	Basis               string  `json:"basis"`
	Provider            string  `json:"provider"`
}

// cachedHedgeState is what the risk worker last stored for the asset.
type cachedHedgeState struct {
	sigma, tickVol float64
	targetSigned   int64
	cappedBy       string
	evaluatedAt    int64
}

func readCachedHedgeState(ctx context.Context, assetID string) (cachedHedgeState, error) {
	m, err := redisx.R().HGetAll(ctx, config.RedisKeys.RiskState(assetID)).Result()
	if err != nil {
		return cachedHedgeState{}, err
	}
	var s cachedHedgeState
	s.sigma, _ = strconv.ParseFloat(m["hedgeSigma"], 64)
	s.tickVol, _ = strconv.ParseFloat(m["hedgeTickVolatility"], 64)
	s.targetSigned, _ = strconv.ParseInt(m["hedgeTargetSigned"], 10, 64)
	s.evaluatedAt, _ = strconv.ParseInt(m["evaluatedAt"], 10, 64)
	s.cappedBy = m["hedgeCappedBy"]
	return s, nil
}

// EntryDelta is the delta-equivalent notional (minor units) of a new trade
// at the money: payout x phi(d2) / (sigma sqrt(tau)), d2 = -sigma sqrt(tau)/2,
// tau floored at the gamma window.
func EntryDelta(payout int64, sigma float64, expirySeconds, gammaWindowSec int64) int64 {
	tau := float64(max(expirySeconds, gammaWindowSec, 1))
	if !(sigma > 0) || payout <= 0 {
		return 0
	}
	v := sigma * math.Sqrt(tau)
	return int64(util.RoundHalfUp(float64(payout) * risk.NormPDF(-v/2) / v))
}

// EstimateTradeHedgeCost is the hedge cost of one trade, for pricing. Not
// required (cost 0) when the asset is not hedged, the trade does not grow
// the book's imbalance, the post-trade utilisation is below the hedge
// threshold, or the expiry is inside the gamma window (never hedged).
func EstimateTradeHedgeCost(ctx context.Context, in TradeCostInput) (TradeHedgeCost, error) {
	asset, err := assets.RequireAssetDoc(ctx, in.AssetID)
	if err != nil {
		return TradeHedgeCost{}, err
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return TradeHedgeCost{}, err
	}
	v := venue()
	out := TradeHedgeCost{Available: true, Provider: v.ID(),
		Basis: "Binance USDⓈ-M perpetual: entry delta x h, round trip (fee + half spread + slippage per side) + funding over the expiry"}
	if !asset.HedgeEnabled {
		out.Basis = "asset not hedged"
		return out, nil
	}
	if in.NetIncrease <= 0 {
		out.Basis = "trade reduces net exposure"
		return out, nil
	}
	threshold := cfg.HedgeThresholdPercent
	if asset.HedgeThresholdPercent != nil {
		threshold = *asset.HedgeThresholdPercent
	}
	band := risk.SelectHedgeLevel(in.UtilisationPercent, cfg.HedgeLevels)
	if band == nil || band.HedgeRatio <= 0 || in.UtilisationPercent < threshold || in.ExpirySeconds < cfg.MinSecondsToExpiryForHedge {
		out.Basis = "below the hedge threshold or inside the gamma window"
		return out, nil
	}
	out.Required = true

	cached, err := readCachedHedgeState(ctx, in.AssetID)
	if err != nil {
		return TradeHedgeCost{}, err
	}
	boost := 0.0
	if cached.tickVol > risk.ElevatedVolatility {
		boost = cfg.VolatilityHedgeBoost
	}
	out.HedgeRatio = risk.HedgeRatioFor(band, boost, asset.TargetHedgeRatio)
	out.Sigma = cached.sigma
	pct := in.PayoutPercent
	if pct <= 0 {
		pct = 100
	}
	payout := in.Stake + util.CalculateProfit(in.Stake, pct)
	out.DeltaNotional = EntryDelta(payout, cached.sigma, in.ExpirySeconds, cfg.MinSecondsToExpiryForHedge)
	out.HedgeNotional = int64(util.RoundHalfUp(out.HedgeRatio * float64(out.DeltaNotional)))
	// The target never exceeds the liability multiple x liability, and one
	// trade raises the liability by at most its profit: that bounds the
	// hedge this trade can add.
	if m := envFloat(func(e *config.Env) float64 { return e.HedgeMaxLiabilityMultiple }, 0); m > 0 {
		out.HedgeNotional = min(out.HedgeNotional, int64(util.RoundHalfUp(m*float64(payout-in.Stake))))
	}

	cost, err := CostEstimateFor(ctx, CostQuery{AssetID: in.AssetID, Notional: out.HedgeNotional, HoldSeconds: in.ExpirySeconds})
	if err != nil {
		return TradeHedgeCost{}, err
	}
	out.Cost = cost
	out.Available, out.Expensive, out.Reason = cost.Available, cost.Expensive, cost.Reason
	if out.Available && !(cached.sigma > 0) {
		out.Available, out.Reason = false, ReasonNoVolatility
	}
	// At the asset's maximum hedge notional the hedge cannot grow: this
	// trade's delta stays unhedged when it pushes the same way. (A target
	// capped by the liability multiple grows with the trade's liability.)
	tradeSign := int64(1)
	if in.Direction != models.DirectionUp {
		tradeSign = -1
	}
	if out.Available && cached.cappedBy == "MAX_HEDGE_NOTIONAL" && sgn(cached.targetSigned) == tradeSign {
		out.Available, out.Reason = false, ReasonHedgeCapacity
	}
	if in.Stake > 0 {
		out.CostPerStake = float64(out.HedgeNotional) * cost.RoundTripBps / 10_000 / float64(in.Stake)
	}
	out.PricingHedgeRatio = out.HedgeRatio
	if out.HedgeRatio > 0 {
		out.PricingCostFraction = out.CostPerStake / out.HedgeRatio
	}
	return out, nil
}
