// Package hedging is the hedging engine: Binance USDⓈ-M perpetual futures
// against the platform's short binary book (docs/audit/hedging/).
//
// Inputs are aggregate only - the open book of an asset, its price and
// volatility - and nothing here can change a customer's result: settlement
// never reads a hedge order (rule 8). Every order stores every input of the
// decision that produced it, so it can be replayed (ReplayDecision, rule 9).
//
//	risk.ComputeHedgeTarget   book delta -> capped INR target   (risk/hedgetarget.go)
//	PlanExecution             target -> venue quantity          (execution.go)
//	send / RefreshOrder       order state machine               (lifecycle.go)
//	recordFills / RefoldPosition  fills ledger -> position, P&L (ledger.go)
//	ReconcilePositions        ledger vs venue positions         (reconcile.go)
//	CostEstimate              cost and availability for pricing (cost.go)
package hedging

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
)

func init() {
	// The risk assessment names an unavailable hedge among its reasons.
	risk.HedgeAvailability = func(ctx context.Context, assetID string) (bool, string) {
		c, err := CostEstimate(ctx, assetID, 0)
		if err != nil {
			return false, ReasonVenueUnavail
		}
		return c.Available, c.Reason
	}
}

func maxOrderUSDT() float64 {
	if config.E != nil {
		return config.E.HedgeMaxOrderUSDT
	}
	return 50_000
}

// HedgeEvaluation is the outcome of one evaluation.
type HedgeEvaluation struct {
	AssetID       string                `json:"assetId"`
	Symbol        string                `json:"symbol"`
	VenueSymbol   string                `json:"venueSymbol"`
	Assessment    models.RiskAssessment `json:"assessment"`
	Plan          *Plan                 `json:"plan"`
	Order         *models.HedgeOrder    `json:"order"`
	SkippedReason string                `json:"skippedReason,omitempty"`
}

// EvaluateAndHedge decides and, when the plan has an executable quantity,
// places one order.
func EvaluateAndHedge(ctx context.Context, assetID string) (*HedgeEvaluation, error) {
	asset, err := assets.RequireAssetDoc(ctx, assetID)
	if err != nil {
		return nil, err
	}
	ev := &HedgeEvaluation{AssetID: assetID, Symbol: asset.Symbol}
	venueSymbol := config.E.HedgeVenueSymbol(asset.Symbol)
	ev.VenueSymbol = venueSymbol
	if venueSymbol == "" {
		ev.SkippedReason = "not a hedgeable asset (HEDGE_SYMBOLS)"
		return ev, nil
	}
	rememberVenueSymbol(venueSymbol, assetID)
	v := venue()

	// One working order per asset: settle it before deciding again.
	if w, err := findWorkingOrderFor(ctx, asset.ID); err != nil {
		return nil, err
	} else if w != nil {
		if w, err = RefreshOrder(ctx, v, w); err != nil {
			logx.Warn("could not refresh working hedge order", "reference", w.Reference, "err", err.Error())
		}
		if w != nil && w.Working() {
			ev.Order, ev.SkippedReason = w, "an order is still working"
			return ev, nil
		}
	}

	assessment, inputs, err := risk.AssessAssetDetailed(ctx, assetID)
	if err != nil {
		return nil, err
	}
	ev.Assessment = assessment
	switch {
	case !asset.HedgeEnabled:
		ev.SkippedReason = "hedging disabled for this asset"
		return ev, nil
	case inputs.FeedStatus == models.FeedStatusStale || inputs.FeedStatus == models.FeedStatusOffline:
		ev.SkippedReason = "market data feed is " + string(inputs.FeedStatus)
		return ev, nil
	}
	if err := v.CanTrade(); err != nil {
		ev.SkippedReason = "venue cannot trade: " + errMessage(err)
		return ev, nil
	}
	fx, err := CurrentFX(ctx)
	if err != nil {
		return nil, err
	}
	target := int64(0)
	if assessment.HedgeTarget != nil {
		target = assessment.HedgeTarget.TargetSigned
	}
	if !fx.Usable() {
		if target != 0 {
			raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertFxStale, Subject: "rate", Severity: models.AlertCritical,
				Message: "Hedging stopped: the INR per USDT rate is " + fx.Reason() + "; set a fresh rate on the Hedging page",
				Details: map[string]any{"rate": fx.Rate, "asOf": fx.AsOf, "ageMs": fx.AgeMs, "maxAgeMs": fx.MaxAgeMs}})
		}
		ev.SkippedReason = "INR rate unusable: " + fx.Reason()
		return ev, nil
	}
	clearAlert(ctx, models.HedgeAlertFxStale, "rate")

	snap, err := GetVenueSnapshot(ctx, venueSymbol)
	if err != nil {
		return nil, err
	}
	if snap == nil || !snap.OK || util.NowMs()-snap.UpdatedAt > venueMaxAge() {
		fresh := RefreshVenueSnapshot(ctx, v, venueSymbol)
		snap = &fresh
	}
	if !snap.OK {
		ev.SkippedReason = "venue unavailable: " + snap.Error
		return ev, nil
	}

	pos, err := GetPosition(ctx, asset.ID)
	if err != nil {
		return nil, err
	}
	var posQty int64
	if pos != nil {
		posQty = pos.QtyE8
	}
	plan := PlanExecution(target, posQty, snap.Mid(), fx.Rate, snap.Rules, maxOrderUSDT(), asset.MinHedgeNotional)
	ev.Plan = &plan
	if plan.ExecutableQtyE8 == 0 {
		ev.SkippedReason = plan.Reason
		return ev, nil
	}

	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	cost, _, _ := ComputeCostBps(*snap, DefaultHoldSeconds,
		envFloat(func(e *config.Env) float64 { return e.HedgeTakerFeeBps }, 5),
		envFloat(func(e *config.Env) float64 { return e.HedgeSlippageBps }, 2),
		envFloat(func(e *config.Env) float64 { return e.HedgeFallbackSpreadBps }, 1))
	decision := buildDecision(assessment, inputs, cfg.Version, *snap, fx, plan, asset.MinHedgeNotional, cost, pos)

	side := models.DirectionUp
	if plan.Side == providers.SideSell {
		side = models.DirectionDown
	}
	id := bson.NewObjectID()
	clientID := newClientOrderID(id)
	mid := snap.Mid()
	slot := asset.ID.Hex()
	intent := &models.HedgeOrder{
		ID: id, Reference: util.Reference("HDG"), AssetID: asset.ID, Symbol: asset.Symbol,
		Provider: v.Name(), Venue: v.ID(), VenueSymbol: venueSymbol, ClientOrderID: clientID,
		Side: side, ReduceOnly: plan.ReduceOnly, TargetQtyE8: plan.ExecutableQtyE8,
		RequestedNotional: NotionalForQty(plan.ExecutableQtyE8, mid, fx.Rate), RequestPrice: &mid,
		Status: models.HedgeOrderStatusPending, Decision: decision, WorkingSlot: &slot, IdempotencyKey: clientID,
	}
	order, err := CreateHedgeOrder(ctx, intent)
	if err != nil {
		if mongox.IsDuplicateKey(err) {
			ev.SkippedReason = "an order is still working"
			return ev, nil
		}
		return nil, err
	}
	emitHedge(config.EvHedgeCreated, order)
	if order, err = send(ctx, v, order); err != nil {
		return nil, err
	}
	ev.Order = order
	logx.Info("hedge order processed", "reference", order.Reference, "symbol", venueSymbol, "side", string(plan.Side),
		"qty", providers.FormatE8(plan.ExecutableQtyE8), "filled", providers.FormatE8(order.FilledQtyE8),
		"status", string(order.Status), "venue", v.ID())
	return ev, nil
}

func buildDecision(a models.RiskAssessment, in risk.RiskInputs, cfgVersion int64, snap VenueSnapshot, fx FxRate, plan Plan,
	minHedge int64, cost models.HedgeCostBps, pos *models.HedgePosition) models.HedgeDecisionSnapshot {
	d := models.HedgeDecisionSnapshot{
		NetExposureAtDecision:   a.NetExposure,
		GrossExposureAtDecision: a.GrossExposure,
		UtilisationPercent:      a.UtilisationPercent,
		RiskLevel:               a.Level,
		HedgeLevel:              string(a.HedgeDecision),
		TargetHedgeRatio:        a.TargetHedgeRatio,
		TargetHedgeNotional:     a.TargetHedgeNotional,
		RiskConfigVersion:       cfgVersion,
		Volatility:              in.Volatility,
		VenuePrice:              snap.Mid(),
		InrPerUsdt:              fx.Rate,
		FxAsOf:                  fx.AsOf,
		TargetQtyE8:             plan.TargetQtyE8,
		PositionQtyE8:           plan.PositionQtyE8,
		OrderQtyE8:              plan.OrderQtyE8,
		ExecutableQtyE8:         plan.ExecutableQtyE8,
		ResidualQtyE8:           plan.ResidualQtyE8,
		Rules:                   snap.Rules,
		MinHedgeNotional:        minHedge,
		MaxOrderUSDT:            maxOrderUSDT(),
		EstimatedCost:           cost,
		Legs:                    []models.HedgeLeg{},
	}
	if in.SecondsToNearestExpiry != nil {
		s := float64(*in.SecondsToNearestExpiry)
		d.SecondsToNearestExpiry = &s
	}
	if a.HedgeTarget != nil {
		d.Target = *a.HedgeTarget
	}
	if pos != nil {
		d.ExistingHedgeNotional = NotionalForQty(absI(pos.QtyE8), snap.Mid(), fx.Rate)
	}
	if in.HedgeBook != nil {
		legs := in.HedgeBook.Legs
		d.LegsCount = int64(len(legs))
		if len(legs) > models.MaxDecisionLegs {
			legs, d.LegsTruncated = legs[:models.MaxDecisionLegs], true
		}
		d.Legs = append(d.Legs, legs...)
	}
	return d
}

// Replay is a decision recomputed from its stored inputs.
type Replay struct {
	BookDelta    int64 `json:"bookDelta"`
	TargetSigned int64 `json:"targetSigned"`
	Plan         Plan  `json:"plan"`
	// Exact is false when the stored legs were truncated.
	Exact bool `json:"exact"`
}

// ReplayDecision recomputes the target and the order from a stored decision
// alone (no live data): the audit check that a hedge is reproducible.
func ReplayDecision(d models.HedgeDecisionSnapshot) Replay {
	t := d.Target
	delta, _, _ := risk.BookDelta(d.Legs, t.ReferencePrice, t.Sigma, t.DecidedAt, t.GammaWindowSeconds)
	bookDelta := int64(util.RoundHalfUp(delta))
	h := risk.HedgeRatioFor(&models.HedgeLevel{HedgeRatio: t.BandRatio}, t.VolatilityBoost, t.AssetCeiling)
	target, _, _ := risk.CapTarget(h*float64(bookDelta), t.MaxHedgeNotional, t.LiabilityMultiple, t.ExpiryLiability)
	if t.Reason != "" {
		target = 0
	}
	plan := PlanExecution(target, d.PositionQtyE8, d.VenuePrice, d.InrPerUsdt, d.Rules, d.MaxOrderUSDT, d.MinHedgeNotional)
	return Replay{BookDelta: bookDelta, TargetSigned: target, Plan: plan, Exact: !d.LegsTruncated}
}

// MarkHedgePositions values every position at the venue mark (paper: the
// platform price) and the current INR rate.
func MarkHedgePositions(ctx context.Context) error {
	cur, err := positions().Find(ctx, bson.M{})
	if err != nil {
		return err
	}
	var ps []models.HedgePosition
	if err := cur.All(ctx, &ps); err != nil {
		return err
	}
	fx, err := CurrentFX(ctx)
	if err != nil {
		return err
	}
	if fx.Missing || !(fx.Rate > 0) {
		return nil
	}
	for i := range ps {
		snap, err := GetVenueSnapshot(ctx, ps[i].VenueSymbol)
		if err != nil {
			return err
		}
		if snap == nil || !snap.OK {
			continue
		}
		mark := snap.Mark
		if !(mark > 0) {
			mark = snap.Mid()
		}
		if _, err := markPosition(ctx, &ps[i], mark, fx.Rate); err != nil {
			return err
		}
	}
	return nil
}

// ToHedgeView is models.ToHedgeView, re-exported for route handlers.
func ToHedgeView(o *models.HedgeOrder) models.HedgeOrderView { return models.ToHedgeView(o) }

// HedgeMonitorRow is one line of the live hedging screen. Money is minor
// units (INR); signed figures are + long / - short. The first block keeps
// its earlier meaning where it still has one (see the metric dictionary).
type HedgeMonitorRow struct {
	AssetID           string                  `json:"assetId"`
	Symbol            string                  `json:"symbol"`
	NetExposure       int64                   `json:"netExposure"`
	TargetHedge       int64                   `json:"targetHedge"`
	CurrentHedge      int64                   `json:"currentHedge"`
	RemainingExposure int64                   `json:"remainingExposure"`
	RiskLevel         models.RiskLevel        `json:"riskLevel"`
	HedgeDecision     models.HedgeDecision    `json:"hedgeDecision"`
	Provider          string                  `json:"provider"`
	OrderStatus       models.HedgeOrderStatus `json:"orderStatus"`
	FillPrice         *float64                `json:"fillPrice"`
	HedgePnl          int64                   `json:"hedgePnl"`
	HedgeEnabled      bool                    `json:"hedgeEnabled"`

	Hedgeable    bool    `json:"hedgeable"`
	Venue        string  `json:"venue"`
	VenueSymbol  string  `json:"venueSymbol"`
	BookDelta    int64   `json:"bookDelta"`
	TargetSigned int64   `json:"targetSigned"`
	HedgeRatio   float64 `json:"hedgeRatio"`
	CappedBy     string  `json:"cappedBy"`
	TargetReason string  `json:"targetReason"`
	Sigma        float64 `json:"sigma"`
	SigmaSource  string  `json:"sigmaSource"`
	// Filled position (protection held).
	PositionQty      float64   `json:"positionQty"`
	PositionNotional int64     `json:"positionNotional"`
	MarkPrice        float64   `json:"markPrice"`
	MarkedAt         *iso.Time `json:"markedAt"`
	// Working orders: contingent, not protection.
	WorkingQty      float64 `json:"workingQty"`
	WorkingNotional int64   `json:"workingNotional"`
	// target - filled position, signed.
	ResidualNotional int64 `json:"residualNotional"`
	// Execution-based P&L (INR minor units at stored rates).
	RealisedPnl   int64 `json:"realisedPnl"`
	Fees          int64 `json:"fees"`
	Funding       int64 `json:"funding"`
	UnrealisedPnl int64 `json:"unrealisedPnl"`
	// Venue reconciliation.
	ReconcileStatus string    `json:"reconcileStatus"`
	ReconciledAt    *iso.Time `json:"reconciledAt"`
	VenueQty        *float64  `json:"venueQty"`
	DriftQty        float64   `json:"driftQty"`
	// Cost estimate for the target (round trip) and availability.
	Available         bool    `json:"available"`
	UnavailableReason string  `json:"unavailableReason"`
	Expensive         bool    `json:"expensive"`
	RoundTripBps      float64 `json:"roundTripBps"`
	EvaluatedAt       int64   `json:"evaluatedAt"`
}

// GetHedgeMonitor builds the per-asset monitor.
func GetHedgeMonitor(ctx context.Context) ([]HedgeMonitorRow, error) {
	v := venue()
	docs, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return nil, err
	}
	rows := make([]HedgeMonitorRow, 0, len(docs))
	for _, asset := range docs {
		assetID := asset.ID.Hex()
		assessment, err := risk.AssessAsset(ctx, assetID)
		if err != nil {
			return nil, err
		}
		row := HedgeMonitorRow{
			AssetID: assetID, Symbol: asset.Symbol, NetExposure: assessment.NetExposure,
			TargetHedge: assessment.TargetHedgeNotional, RiskLevel: assessment.Level, HedgeDecision: assessment.HedgeDecision,
			Provider: v.Name(), Venue: v.ID(), OrderStatus: models.HedgeOrderStatusNotRequired, HedgeEnabled: asset.HedgeEnabled,
			VenueSymbol: config.E.HedgeVenueSymbol(asset.Symbol), ReconcileStatus: models.HedgeReconNever,
			EvaluatedAt: assessment.EvaluatedAt,
		}
		row.Hedgeable = row.VenueSymbol != ""
		if h := assessment.HedgeTarget; h != nil {
			row.BookDelta, row.TargetSigned, row.HedgeRatio = h.BookDelta, h.TargetSigned, h.HedgeRatio
			row.CappedBy, row.TargetReason, row.Sigma, row.SigmaSource = h.CappedBy, h.Reason, h.Sigma, h.SigmaSource
		}
		pos, err := GetPosition(ctx, asset.ID)
		if err != nil {
			return nil, err
		}
		var signedPos int64
		if pos != nil {
			signedPos = sgn(pos.QtyE8) * pos.NotionalInr
			row.PositionQty, row.PositionNotional, row.CurrentHedge = models.QtyFloat(pos.QtyE8), signedPos, pos.NotionalInr
			row.MarkPrice, row.MarkedAt = pos.MarkPrice, iso.Ptr(pos.MarkedAt)
			row.RealisedPnl, row.Fees, row.Funding, row.UnrealisedPnl = pos.RealisedInr, pos.FeesInr, pos.FundingInr, pos.UnrealisedInr
			row.ReconcileStatus, row.ReconciledAt = pos.ReconcileStatus, iso.Ptr(pos.ReconciledAt)
			row.DriftQty = models.QtyFloat(pos.DriftE8)
			if pos.VenueQtyE8 != nil {
				q := models.QtyFloat(*pos.VenueQtyE8)
				row.VenueQty = &q
			}
			if pos.QtyE8 != 0 {
				avg := pos.AvgPrice
				row.FillPrice = &avg
			}
			row.HedgePnl = pos.RealisedInr - pos.FeesInr + pos.FundingInr + pos.UnrealisedInr
		}
		row.ResidualNotional = row.TargetSigned - signedPos
		row.RemainingExposure = absI(row.ResidualNotional)
		latest, _, err := ListHedgeOrders(ctx, HedgeOrderQuery{AssetID: assetID, Page: 1, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(latest) > 0 {
			last := &latest[0]
			row.OrderStatus = last.Status
			if last.Working() {
				rem := max(0, last.TargetQtyE8-last.FilledQtyE8)
				if last.Side == models.DirectionDown {
					rem = -rem
				}
				row.WorkingQty = models.QtyFloat(rem)
				row.WorkingNotional = NotionalForQty(rem, last.Decision.VenuePrice, last.Decision.InrPerUsdt)
			}
		}
		if row.Hedgeable {
			c, err := CostEstimate(ctx, assetID, row.TargetHedge)
			if err != nil {
				return nil, err
			}
			row.Available, row.UnavailableReason, row.Expensive, row.RoundTripBps = c.Available, c.Reason, c.Expensive, c.RoundTripBps
		} else {
			row.UnavailableReason = ReasonNotHedgeable
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// VenueStatusView is one symbol's venue snapshot for the admin screen.
type VenueStatusView struct {
	Symbol      string  `json:"symbol"`
	OK          bool    `json:"ok"`
	Error       string  `json:"error,omitempty"`
	Mid         float64 `json:"mid"`
	Mark        float64 `json:"mark"`
	FundingRate float64 `json:"fundingRate"`
	TakerFeeBps float64 `json:"takerFeeBps"`
	UpdatedAt   int64   `json:"updatedAt"`
	AgeMs       int64   `json:"ageMs"`
	Stale       bool    `json:"stale"`
}

// HedgeStatus is the venue, rate and reconciliation state of hedging.
type HedgeStatus struct {
	Provider             string            `json:"provider"`
	Venue                string            `json:"venue"`
	Live                 bool              `json:"live"`
	LiveExecutionEnabled bool              `json:"liveExecutionEnabled"`
	CanTrade             bool              `json:"canTrade"`
	CanTradeReason       string            `json:"canTradeReason,omitempty"`
	Fx                   FxRate            `json:"fx"`
	Venues               []VenueStatusView `json:"venues"`
	ReconcileStatus      string            `json:"reconcileStatus"`
	LastReconciledAt     *iso.Time         `json:"lastReconciledAt"`
	LastCheckedAt        *iso.Time         `json:"lastCheckedAt"`
	OpenAlerts           int64             `json:"openAlerts"`
	// Average realised minus estimated per-side cost over recent filled
	// orders (model error), bps; nil without fills.
	CostModelErrorBps *float64 `json:"costModelErrorBps"`
	CostModelSamples  int64    `json:"costModelSamples"`
	// Refresh intervals the screen compares ages against, ms.
	HedgeIntervalMs     int64    `json:"hedgeIntervalMs"`
	ReconcileIntervalMs int64    `json:"reconcileIntervalMs"`
	VenueMaxAgeMs       int64    `json:"venueMaxAgeMs"`
	GeneratedAt         iso.Time `json:"generatedAt"`
}

// GetHedgeStatus builds the status panel.
func GetHedgeStatus(ctx context.Context) (HedgeStatus, error) {
	v := venue()
	s := HedgeStatus{Provider: v.Name(), Venue: v.ID(), Live: v.Live(), Venues: []VenueStatusView{},
		HedgeIntervalMs: hedgeIntervalMs(), ReconcileIntervalMs: reconcileIntervalMs(), VenueMaxAgeMs: venueMaxAge(),
		GeneratedAt: iso.From(mongox.Now())}
	s.LiveExecutionEnabled = config.E != nil && config.E.HedgeLiveExecutionEnabled
	if err := v.CanTrade(); err != nil {
		s.CanTradeReason = errMessage(err)
		if ae, ok := apperr.As(err); ok {
			s.CanTradeReason = ae.Code + ": " + ae.Message
		}
	} else {
		s.CanTrade = true
	}
	fx, err := CurrentFX(ctx)
	if err != nil {
		return s, err
	}
	s.Fx = fx
	if config.E != nil {
		seen := map[string]bool{}
		for _, sym := range config.E.HedgeSymbols {
			if seen[sym] {
				continue
			}
			seen[sym] = true
			snap, err := GetVenueSnapshot(ctx, sym)
			if err != nil {
				return s, err
			}
			view := VenueStatusView{Symbol: sym, Error: "no snapshot yet"}
			if snap != nil {
				view = VenueStatusView{Symbol: sym, OK: snap.OK, Error: snap.Error, Mid: snap.Mid(), Mark: snap.Mark,
					FundingRate: snap.FundingRate, TakerFeeBps: snap.TakerRate * 10_000, UpdatedAt: snap.UpdatedAt}
				view.AgeMs = util.NowMs() - snap.UpdatedAt
				view.Stale = view.AgeMs > venueMaxAge()
			}
			s.Venues = append(s.Venues, view)
		}
	}
	pnl, err := GetHedgePnlSummary(ctx, nil)
	if err != nil {
		return s, err
	}
	s.ReconcileStatus, s.LastReconciledAt = pnl.ReconcileStatus, iso.Ptr(pnl.ReconciledAt)
	var checked *time.Time
	cur, err := positions().Find(ctx, bson.M{})
	if err != nil {
		return s, err
	}
	var ps []models.HedgePosition
	if err := cur.All(ctx, &ps); err != nil {
		return s, err
	}
	if len(ps) == 0 {
		s.ReconcileStatus = models.HedgeReconNever
	}
	for i := range ps {
		if c := ps[i].ReconcileCheckedAt; c != nil && (checked == nil || c.Before(*checked)) {
			checked = c
		}
	}
	s.LastCheckedAt = iso.Ptr(checked)
	if s.OpenAlerts, err = CountOpenAlerts(ctx); err != nil {
		return s, err
	}
	s.CostModelErrorBps, s.CostModelSamples, err = costModelError(ctx, 50)
	return s, err
}

// costModelError compares realised per-side cost with the estimate over
// the last n orders with fills.
func costModelError(ctx context.Context, n int64) (*float64, int64, error) {
	cur, err := orders().Find(ctx, bson.M{"realisedCostBps": bson.M{"$ne": nil}},
		optionsFindRecent(n))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.HedgeOrder
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	if len(rows) == 0 {
		return nil, 0, nil
	}
	var sum float64
	for _, o := range rows {
		sum += *o.RealisedCostBps - o.Decision.EstimatedCost.PerSideBps
	}
	avg := sum / float64(len(rows))
	avg = float64(int64(avg*100)) / 100
	return &avg, int64(len(rows)), nil
}

// ProviderInfo reports the active venue for the health endpoint: its id
// and whether orders move real money.
func ProviderInfo() (name string, liveHedging bool) {
	v := providers.GetVenue()
	return v.ID(), v.Live() && v.CanTrade() == nil
}
