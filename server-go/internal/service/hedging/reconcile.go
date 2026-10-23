package hedging

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/hedging/providers"
)

// Position reconciliation (hedge-lifecycle.md section 6). On a schedule
// (HEDGE_RECONCILE_INTERVAL_MS) and for every hedgeable asset:
//
//  1. refold the position from the fills ledger; a cached position that
//     disagreed with its own ledger is repaired and raises ledger_mismatch;
//  2. compare it with the venue's position (positionRisk). A difference of
//     half a quantity step or more is DRIFT: alert position_drift (critical),
//     an error log line, and the position shows DRIFT with the venue
//     quantity until they agree again. Nothing is corrected automatically -
//     the venue may hold trades this ledger never saw (a manual trade, a
//     liquidation) and an operator must decide;
//  3. read funding payments since the last cursor;
//  4. mark the position.
//
// The paper venue has no independent position: NOT_APPLICABLE.

// ReconcileResult is one asset's outcome.
type ReconcileResult struct {
	AssetID     string `json:"assetId"`
	VenueSymbol string `json:"venueSymbol"`
	Status      string `json:"status"`
	LedgerQty   string `json:"ledgerQty"`
	VenueQty    string `json:"venueQty"`
	Drift       string `json:"drift"`
	Funding     int    `json:"fundingAdded"`
	Error       string `json:"error,omitempty"`
}

// ReconcilePositions runs one pass over the hedgeable assets.
func ReconcilePositions(ctx context.Context) ([]ReconcileResult, error) {
	v := venue()
	docs, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []ReconcileResult{}
	for _, a := range docs {
		sym := config.E.HedgeVenueSymbol(a.Symbol)
		if sym == "" {
			continue
		}
		existing, err := GetPosition(ctx, a.ID)
		if err != nil {
			return out, err
		}
		if existing == nil && !a.HedgeEnabled {
			continue
		}
		r, err := reconcileOne(ctx, v, a, sym, existing)
		if err != nil {
			return out, err
		}
		out = append(out, r)
	}
	return out, nil
}

func reconcileOne(ctx context.Context, v providers.Venue, a *models.Asset, sym string, before *models.HedgePosition) (ReconcileResult, error) {
	res := ReconcileResult{AssetID: a.ID.Hex(), VenueSymbol: sym}
	p, err := RefoldPosition(ctx, a.ID, a.Symbol, v.ID(), sym)
	if err != nil {
		return res, err
	}
	if before != nil && (before.QtyE8 != p.QtyE8 || before.RealisedE8 != p.RealisedE8) {
		raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertLedgerMismatch, Subject: sym, AssetID: &a.ID,
			Message: "Cached hedge position disagreed with its fills ledger and was rebuilt from it",
			Details: map[string]any{"cachedQty": providers.FormatE8(before.QtyE8), "ledgerQty": providers.FormatE8(p.QtyE8),
				"cachedRealisedE8": before.RealisedE8, "ledgerRealisedE8": p.RealisedE8}})
	}
	res.LedgerQty = providers.FormatE8(p.QtyE8)
	now := mongox.Now()
	set := bson.M{"reconcileCheckedAt": now, "updatedAt": now}

	vp, err := v.Position(ctx, sym)
	switch {
	case errors.Is(err, providers.ErrUnsupported):
		res.Status = models.HedgeReconNotApplicable
		set["reconcileStatus"], set["reconcileError"] = res.Status, nil
	case err != nil:
		res.Status, res.Error = models.HedgeReconUnknown, err.Error()
		set["reconcileStatus"], set["reconcileError"] = res.Status, err.Error()
		raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertVenueUnreachable, Subject: sym, AssetID: &a.ID,
			Message: "Hedge venue position could not be read", Details: map[string]any{"error": err.Error()}})
	default:
		clearAlert(ctx, models.HedgeAlertVenueUnreachable, sym)
		drift := vp.QtyE8 - p.QtyE8
		res.VenueQty, res.Drift = providers.FormatE8(vp.QtyE8), providers.FormatE8(drift)
		tolerance := int64(0)
		if snap, _ := GetVenueSnapshot(ctx, sym); snap != nil {
			tolerance = snap.Rules.StepE8 / 2
		}
		set["venueQtyE8"], set["driftE8"], set["reconcileError"] = vp.QtyE8, drift, nil
		if absI(drift) > tolerance {
			res.Status = models.HedgeReconDrift
			set["reconcileStatus"] = res.Status
			raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertPositionDrift, Subject: sym, AssetID: &a.ID, Severity: models.AlertCritical,
				Message: "Hedge position at the venue differs from the fills ledger",
				Details: map[string]any{"ledgerQty": res.LedgerQty, "venueQty": res.VenueQty, "drift": res.Drift}})
			logx.Error("HEDGE_POSITION_DRIFT", "symbol", sym, "ledgerQty", res.LedgerQty, "venueQty", res.VenueQty)
		} else {
			res.Status = models.HedgeReconOK
			set["reconcileStatus"], set["reconciledAt"] = res.Status, now
			clearAlert(ctx, models.HedgeAlertPositionDrift, sym)
		}
	}
	if _, err := positions().UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": set}); err != nil {
		return res, err
	}

	if res.Status != models.HedgeReconUnknown {
		added, err := SyncFunding(ctx, v, p)
		if err != nil {
			logx.Warn("could not read hedge funding", "symbol", sym, "err", err.Error())
		} else if added > 0 {
			res.Funding = added
			if _, err := RefoldPosition(ctx, a.ID, a.Symbol, v.ID(), sym); err != nil {
				return res, err
			}
		}
	}
	return res, nil
}

func optionsFindRecent(n int64) *options.FindOptionsBuilder {
	return options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(n)
}
