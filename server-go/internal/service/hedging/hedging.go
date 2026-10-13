// Package hedging is the dynamic hedging engine (spec sections 14, 15).
//
// The engine works on ONE input: the platform's aggregate net exposure per
// asset. It has no access to individual trades, and nothing it does can change
// a customer's result - settlement never reads a hedge order (rule 8).
//
// Every order records the decision inputs that produced it, so any hedge can be
// replayed against the risk configuration that was live at the time (rule 9).
package hedging

import (
	"context"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/metrics"
	"binarytrading/internal/service/risk"
	"binarytrading/internal/service/riskconfig"
)

type HedgeAdjustment struct {
	Side                  providers.HedgeSide `json:"side"`
	Notional              int64               `json:"notional"`
	TargetSignedNotional  int64               `json:"targetSignedNotional"`
	CurrentSignedNotional int64               `json:"currentSignedNotional"`
}

// ComputeHedgeAdjustment is the pure sizing step.
//
// The platform is short the market when the book leans UP (it pays out if the
// price rises), so it hedges by going long, and vice versa. The order is only
// the difference between where the hedge should be and where it already is.
func ComputeHedgeAdjustment(netExposure, targetHedgeNotional, currentSignedNotional, minHedgeNotional int64) *HedgeAdjustment {
	var direction int64
	switch {
	case netExposure > 0:
		direction = 1
	case netExposure < 0:
		direction = -1
	}
	targetSigned := direction * targetHedgeNotional
	delta := targetSigned - currentSignedNotional
	abs := delta
	if abs < 0 {
		abs = -abs
	}
	if abs < minHedgeNotional {
		return nil
	}
	side := providers.SideSell
	if delta > 0 {
		side = providers.SideBuy
	}
	return &HedgeAdjustment{
		Side:                  side,
		Notional:              abs,
		TargetSignedNotional:  targetSigned,
		CurrentSignedNotional: currentSignedNotional,
	}
}

type HedgeEvaluation struct {
	AssetID               string                `json:"assetId"`
	Symbol                string                `json:"symbol"`
	Assessment            models.RiskAssessment `json:"assessment"`
	CurrentSignedNotional int64                 `json:"currentSignedNotional"`
	Adjustment            *HedgeAdjustment      `json:"adjustment"`
	Order                 *models.HedgeOrder    `json:"order"`
	SkippedReason         string                `json:"skippedReason,omitempty"`
}

func EvaluateAndHedge(ctx context.Context, assetID string) (*HedgeEvaluation, error) {
	asset, err := assets.RequireAssetDoc(ctx, assetID)
	if err != nil {
		return nil, err
	}
	assessment, err := risk.AssessAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	currentSigned, err := GetSignedHedgeNotional(ctx, assetID)
	if err != nil {
		return nil, err
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}

	ev := &HedgeEvaluation{
		AssetID:               assetID,
		Symbol:                asset.Symbol,
		Assessment:            assessment,
		CurrentSignedNotional: currentSigned,
	}

	if !asset.HedgeEnabled {
		ev.SkippedReason = "hedging disabled for this asset"
		return ev, nil
	}

	adj := ComputeHedgeAdjustment(assessment.NetExposure, assessment.TargetHedgeNotional, currentSigned, asset.MinHedgeNotional)
	if adj == nil {
		ev.SkippedReason = "hedge already within tolerance"
		return ev, nil
	}
	ev.Adjustment = adj

	tick, err := marketdata.GetLastTick(ctx, assetID)
	if err != nil {
		return nil, err
	}
	if tick == nil {
		ev.SkippedReason = "no reference price available"
		return ev, nil
	}

	// One order per (asset, target, current) state: re-running the evaluation
	// on unchanged state returns the same order rather than stacking duplicates.
	idempotencyKey := util.SHA256(strings.Join([]string{
		assetID, string(adj.Side), strconv.FormatInt(adj.Notional, 10),
		strconv.FormatInt(adj.TargetSignedNotional, 10), strconv.FormatInt(currentSigned, 10),
	}, ":"))

	existing, err := FindByIdempotencyKey(ctx, idempotencyKey)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		ev.Order = existing
		ev.SkippedReason = "identical hedge already placed"
		return ev, nil
	}

	order, err := placeHedgeOrder(ctx, placeHedgeInput{
		asset:                 asset,
		adjustment:            adj,
		assessment:            assessment,
		referencePrice:        tick.Price,
		idempotencyKey:        idempotencyKey,
		existingHedgeNotional: absInt(currentSigned),
		riskConfigVersion:     cfg.Version,
	})
	if err != nil {
		return nil, err
	}
	ev.Order = order
	return ev, nil
}

type placeHedgeInput struct {
	asset                 *models.Asset
	adjustment            *HedgeAdjustment
	assessment            models.RiskAssessment
	referencePrice        float64
	idempotencyKey        string
	existingHedgeNotional int64
	riskConfigVersion     int64
}

func emitHedge(event string, o *models.HedgeOrder) {
	ws.Emit([]string{config.ChAdminRisk}, event, models.ToHedgeView(o))
}

func placeHedgeOrder(ctx context.Context, in placeHedgeInput) (*models.HedgeOrder, error) {
	provider := providers.GetHedgeProvider()
	orderReference := util.Reference("HDG")
	side := models.DirectionDown
	if in.adjustment.Side == providers.SideBuy {
		side = models.DirectionUp
	}
	requestPrice := in.referencePrice

	// Persist the intent before the network call, so an order that is sent but
	// whose response is lost is still visible and reconcilable.
	order, err := CreateHedgeOrder(ctx, &models.HedgeOrder{
		Reference:         orderReference,
		AssetID:           in.asset.ID,
		Symbol:            in.asset.Symbol,
		Provider:          provider.Name(),
		Side:              side,
		RequestedNotional: in.adjustment.Notional,
		FilledNotional:    0,
		RequestPrice:      &requestPrice,
		Status:            models.HedgeOrderStatusPending,
		Decision: models.HedgeDecisionSnapshot{
			NetExposureAtDecision:   in.assessment.NetExposure,
			GrossExposureAtDecision: in.assessment.GrossExposure,
			UtilisationPercent:      in.assessment.UtilisationPercent,
			RiskLevel:               in.assessment.Level,
			HedgeLevel:              string(in.assessment.HedgeDecision),
			TargetHedgeRatio:        in.assessment.TargetHedgeRatio,
			TargetHedgeNotional:     in.assessment.TargetHedgeNotional,
			ExistingHedgeNotional:   in.existingHedgeNotional,
			RiskConfigVersion:       in.riskConfigVersion,
			Volatility:              0,
			SecondsToNearestExpiry:  nil,
		},
		RealisedPnl:    0,
		UnrealisedPnl:  0,
		IdempotencyKey: in.idempotencyKey,
	})
	if err != nil {
		return nil, err
	}

	emitHedge(config.EvHedgeCreated, order)

	startedAt := time.Now()
	elapsedMs := func() int64 {
		return int64(util.RoundHalfUp(float64(time.Since(startedAt).Microseconds()) / 1000))
	}
	result, err := provider.PlaceOrder(ctx, providers.OrderRequest{
		Reference:      orderReference,
		Symbol:         in.asset.Symbol,
		Side:           in.adjustment.Side,
		Notional:       in.adjustment.Notional,
		ReferencePrice: in.referencePrice,
		IdempotencyKey: in.idempotencyKey,
	})
	if err != nil {
		message := errMessage(err)
		logx.Error("hedge order failed", "err", err.Error(), "symbol", in.asset.Symbol, "reference", orderReference)
		updated, uerr := UpdateHedgeOrder(ctx, order.ID, bson.M{
			"status":           models.HedgeOrderStatusFailed,
			"error":            message,
			"requestLatencyMs": elapsedMs(),
		})
		if uerr != nil {
			return nil, uerr
		}
		if updated != nil {
			order = updated
		}
		emitHedge(config.EvHedgeUpdated, order)
		return order, nil
	}

	requestLatencyMs := elapsedMs()
	metrics.RecordLatency(config.LatHedgeRequest, float64(requestLatencyMs))
	if result.Status == providers.StateFilled || result.Status == providers.StatePartiallyFilled {
		metrics.RecordLatency(config.LatHedgeFill, float64(requestLatencyMs))
	}

	filled := int64(util.RoundHalfUp(result.FilledNotional))
	patch := resultPatch(result, filled)
	patch["requestLatencyMs"] = requestLatencyMs
	if result.FilledNotional > 0 {
		patch["fillLatencyMs"] = requestLatencyMs
	} else {
		patch["fillLatencyMs"] = nil
	}
	updated, err := UpdateHedgeOrder(ctx, order.ID, patch)
	if err != nil {
		return nil, err
	}
	if updated != nil {
		order = updated
	}

	emitHedge(config.EvHedgeUpdated, order)

	logx.Info("hedge order processed",
		"reference", orderReference, "symbol", in.asset.Symbol, "side", string(in.adjustment.Side),
		"requested", in.adjustment.Notional, "filled", result.FilledNotional, "status", string(result.Status),
		"provider", provider.Name())

	return order, nil
}

// resultPatch maps a venue result onto stored fields. Undefined TS keys are
// dropped by Mongoose, so an empty providerOrderId is not written.
func resultPatch(r providers.OrderResult, filled int64) bson.M {
	patch := bson.M{
		"status":           models.HedgeOrderStatus(r.Status),
		"filledNotional":   filled,
		"averageFillPrice": r.AverageFillPrice,
		"error":            nil,
	}
	if r.ProviderOrderID != "" {
		patch["providerOrderId"] = r.ProviderOrderID
	}
	if r.Error != "" {
		patch["error"] = r.Error
	}
	return patch
}

func errMessage(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}

// ReconcilePendingHedgeOrders polls the venue for orders that were not
// immediately final.
func ReconcilePendingHedgeOrders(ctx context.Context) (int, error) {
	provider := providers.GetHedgeProvider()
	pending, err := FindPendingOrders(ctx, 200)
	if err != nil {
		return 0, err
	}
	updated := 0
	for i := range pending {
		order := &pending[i]
		if order.ProviderOrderID == nil || *order.ProviderOrderID == "" {
			continue
		}
		result, err := provider.GetOrder(ctx, *order.ProviderOrderID)
		if err != nil {
			logx.Warn("could not refresh hedge order state", "err", err.Error(), "reference", order.Reference)
			continue
		}
		filled := int64(util.RoundHalfUp(result.FilledNotional))
		if models.HedgeOrderStatus(result.Status) == order.Status && filled == order.FilledNotional {
			continue
		}
		patch := bson.M{
			"status":           models.HedgeOrderStatus(result.Status),
			"filledNotional":   filled,
			"averageFillPrice": result.AverageFillPrice,
			"error":            nil,
		}
		if result.Error != "" {
			patch["error"] = result.Error
		}
		next, err := UpdateHedgeOrder(ctx, order.ID, patch)
		if err != nil {
			logx.Warn("could not refresh hedge order state", "err", err.Error(), "reference", order.Reference)
			continue
		}
		if next != nil {
			emitHedge(config.EvHedgeUpdated, next)
			updated++
		}
	}
	return updated, nil
}

// MarkHedgePositions marks every open hedge leg to market against the latest tick.
func MarkHedgePositions(ctx context.Context) error {
	items, _, err := ListHedgeOrders(ctx, HedgeOrderQuery{Page: 1, Limit: 500, Status: string(models.HedgeOrderStatusFilled)})
	if err != nil {
		return err
	}
	for i := range items {
		o := &items[i]
		if o.ClosedAt != nil || o.AverageFillPrice == nil || *o.AverageFillPrice == 0 || o.FilledNotional <= 0 {
			continue
		}
		tick, err := marketdata.GetLastTick(ctx, o.AssetID.Hex())
		if err != nil {
			return err
		}
		if tick == nil {
			continue
		}
		avg := *o.AverageFillPrice
		priceMove := (tick.Price - avg) / avg
		direction := -1.0
		if o.Side == models.DirectionUp {
			direction = 1
		}
		pnl := int64(util.RoundHalfUp(float64(o.FilledNotional) * priceMove * direction))
		if pnl != o.UnrealisedPnl {
			if _, err := UpdateHedgeOrder(ctx, o.ID, bson.M{"unrealisedPnl": pnl}); err != nil {
				return err
			}
		}
	}
	return nil
}

// ToHedgeView is models.ToHedgeView, re-exported for route handlers.
func ToHedgeView(o *models.HedgeOrder) models.HedgeOrderView { return models.ToHedgeView(o) }

// HedgeMonitorRow is one line of the live hedging screen (spec section 22).
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
}

func GetHedgeMonitor(ctx context.Context) ([]HedgeMonitorRow, error) {
	provider := providers.GetHedgeProvider()
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
		signed, err := GetSignedHedgeNotional(ctx, assetID)
		if err != nil {
			return nil, err
		}
		latest, _, err := ListHedgeOrders(ctx, HedgeOrderQuery{AssetID: assetID, Page: 1, Limit: 1})
		if err != nil {
			return nil, err
		}
		row := HedgeMonitorRow{
			AssetID:           assetID,
			Symbol:            asset.Symbol,
			NetExposure:       assessment.NetExposure,
			TargetHedge:       assessment.TargetHedgeNotional,
			CurrentHedge:      absInt(signed),
			RemainingExposure: max(0, absInt(assessment.NetExposure)-absInt(signed)),
			RiskLevel:         assessment.Level,
			HedgeDecision:     assessment.HedgeDecision,
			Provider:          provider.Name(),
			OrderStatus:       models.HedgeOrderStatusNotRequired,
			HedgeEnabled:      asset.HedgeEnabled,
		}
		if len(latest) > 0 {
			last := latest[0]
			row.OrderStatus = last.Status
			row.FillPrice = last.AverageFillPrice
			row.HedgePnl = last.RealisedPnl + last.UnrealisedPnl
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func CancelHedgeOrder(ctx context.Context, orderID string) (models.HedgeOrderView, error) {
	provider := providers.GetHedgeProvider()
	target, err := FindHedgeOrderByID(ctx, orderID)
	if err != nil {
		return models.HedgeOrderView{}, err
	}
	if target == nil {
		return models.HedgeOrderView{}, apperr.NotFoundErr(apperr.NotFound, "Hedge order not found")
	}
	if target.Status == models.HedgeOrderStatusFilled || target.Status == models.HedgeOrderStatusCancelled {
		return models.HedgeOrderView{}, apperr.ConflictErr(apperr.Conflict, "Order is already "+string(target.Status))
	}

	if target.ProviderOrderID != nil && *target.ProviderOrderID != "" {
		if err := provider.CancelOrder(ctx, *target.ProviderOrderID); err != nil {
			return models.HedgeOrderView{}, err
		}
	}
	updated, err := UpdateHedgeOrder(ctx, target.ID, bson.M{
		"status":   models.HedgeOrderStatusCancelled,
		"closedAt": mongox.Now(),
	})
	if err != nil {
		return models.HedgeOrderView{}, err
	}
	if updated == nil {
		updated = target
	}
	emitHedge(config.EvHedgeUpdated, updated)
	return models.ToHedgeView(updated), nil
}

// ProviderInfo reports the active provider for the health endpoint:
// name and whether orders really reach a venue.
func ProviderInfo() (name string, liveHedging bool) {
	p := providers.GetHedgeProvider()
	return p.Name(), p.Live() && config.E != nil && config.E.HedgeLiveExecutionEnabled
}

func absInt(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
