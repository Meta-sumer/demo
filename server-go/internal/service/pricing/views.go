package pricing

import (
	"context"
	"fmt"
	"strings"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/service/riskconfig"
)

// ModelView is a published guard model as the console shows it.
type ModelView struct {
	ID          string                     `json:"id"`
	Version     int64                      `json:"version"`
	Active      bool                       `json:"active"`
	Name        string                     `json:"name"`
	Method      string                     `json:"method"`
	GeneratedAt iso.Time                   `json:"generatedAt"`
	DataFrom    int64                      `json:"dataFrom"`
	DataTo      int64                      `json:"dataTo"`
	Source      string                     `json:"source"`
	Hash        string                     `json:"hash"`
	Report      string                     `json:"report"`
	PublishedBy string                     `json:"publishedBy"`
	CreatedAt   iso.Time                   `json:"createdAt"`
	Entries     []models.PricingModelEntry `json:"entries,omitempty"`
}

// ToModelView maps a model; withEntries includes the parameters.
func ToModelView(m *models.PricingModel, withEntries bool) ModelView {
	v := ModelView{
		ID: m.ID.Hex(), Version: m.Version, Active: m.Active, Name: m.Name, Method: m.Method,
		GeneratedAt: iso.From(m.GeneratedAt), DataFrom: m.DataFrom, DataTo: m.DataTo, Source: m.Source,
		Hash: m.Hash, Report: m.Report, PublishedBy: m.PublishedBy, CreatedAt: iso.From(m.CreatedAt),
	}
	if withEntries {
		v.Entries = m.Entries
		if v.Entries == nil {
			v.Entries = []models.PricingModelEntry{}
		}
	}
	return v
}

// EstimatorName names the installed hedge cost estimator.
func EstimatorName() string { return fmt.Sprintf("%T", CurrentHedgeCostEstimator()) }

// ── kill switch (spec 7.4) ──────────────────────────────────────────────

// Halt stops new offers and acceptances, for one symbol or (symbol "")
// everything. Open trades keep settling. It is a risk-configuration change:
// versioned, and audited by the caller. A symbol halt does not override the
// global one; either blocks.
func Halt(ctx context.Context, symbol, reason, actorID string) (*models.RiskConfig, *models.RiskConfig, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, nil, apperr.Validation("A halt needs a reason", nil)
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	var patch riskconfig.RiskConfigPatch
	note := "halt " + nonEmpty(symbol, "ALL") + ": " + reason
	if symbol == "" {
		t := true
		patch.TradingHalted, patch.TradingHaltReason = &t, &reason
	} else {
		halts := make([]models.PricingHalt, 0, len(cfg.PricingHalts)+1)
		for _, h := range cfg.PricingHalts {
			if h.Symbol != symbol {
				halts = append(halts, h)
			}
		}
		halts = append(halts, models.PricingHalt{Symbol: symbol, Reason: reason, At: time.Now().UTC().Truncate(time.Millisecond), By: actorID})
		patch.PricingHalts = halts
	}
	InvalidateBoard()
	return riskconfig.UpdateRiskConfig(ctx, patch, actorID, &note)
}

// Resume lifts a halt: one symbol's, or (symbol "") the global one.
func Resume(ctx context.Context, symbol, actorID string) (*models.RiskConfig, *models.RiskConfig, error) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, nil, err
	}
	var patch riskconfig.RiskConfigPatch
	note := "resume " + nonEmpty(symbol, "ALL")
	if symbol == "" {
		f, empty := false, ""
		patch.TradingHalted, patch.TradingHaltReason = &f, &empty
	} else {
		halts := []models.PricingHalt{}
		found := false
		for _, h := range cfg.PricingHalts {
			if h.Symbol == symbol {
				found = true
				continue
			}
			halts = append(halts, h)
		}
		if !found {
			return nil, nil, apperr.ConflictErr(apperr.Conflict, symbol+" is not halted")
		}
		patch.PricingHalts = halts
	}
	InvalidateBoard()
	return riskconfig.UpdateRiskConfig(ctx, patch, actorID, &note)
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
