package pricing

import (
	"context"
	"sort"
	"sync"
	"time"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/payouts"
	"binarytrading/internal/service/riskconfig"
)

// The indicative board: the engine's current offer terms for every active
// payout row of an asset, both directions, at the reference stake on the
// LIVE book. Nothing is issued (no offer ids). It feeds the existing asset
// endpoints (GET /assets, /assets/{id}), whose payoutPercent is now this
// figure, and the admin pricing view. Cached for boardTTL per process.

const boardTTL = time.Second

// lastGoodTTL: while market data is not fresh (trading is paused for it
// anyway) the catalogue keeps showing the last figure it had for this long,
// so the apps' duration choice does not flicker away during a feed blip.
const lastGoodTTL = time.Minute

// BoardRow is one payout row priced both ways.
type BoardRow struct {
	Payout *models.PayoutConfig
	Up     *Decision
	Down   *Decision
}

type boardEntry struct {
	at   time.Time
	rows []BoardRow
}

type lastGoodPrice struct {
	at  time.Time
	pct float64
}

var (
	boardMu  sync.Mutex
	boards   = map[string]*boardEntry{}
	lastGood = map[string]lastGoodPrice{}
)

// InvalidateBoard drops the cached boards.
func InvalidateBoard() {
	boardMu.Lock()
	boards = map[string]*boardEntry{}
	boardMu.Unlock()
}

// Board prices every active payout row of an asset.
func Board(ctx context.Context, asset *models.Asset) ([]BoardRow, error) {
	id := asset.ID.Hex()
	boardMu.Lock()
	if b := boards[id]; b != nil && time.Since(b.at) < boardTTL {
		rows := b.rows
		boardMu.Unlock()
		return rows, nil
	}
	boardMu.Unlock()

	views, err := payouts.ListActivePayouts(ctx, id)
	if err != nil {
		return nil, err
	}
	now := util.NowMs()
	rows := make([]BoardRow, 0, len(views))
	for _, v := range views {
		row, err := payouts.FindActivePayout(ctx, id, v.ExpirySeconds)
		if err != nil {
			return nil, err
		}
		if row == nil {
			continue
		}
		br := BoardRow{Payout: row}
		for _, dir := range []models.Direction{models.DirectionUp, models.DirectionDown} {
			d, err := Evaluate(ctx, Request{Asset: asset, Payout: row, Direction: dir, ExpirySeconds: v.ExpirySeconds,
				AccountType: models.AccountLive, Now: now})
			if err != nil {
				return nil, err
			}
			if dir == models.DirectionUp {
				br.Up = d
			} else {
				br.Down = d
			}
		}
		rows = append(rows, br)
	}
	boardMu.Lock()
	boards[id] = &boardEntry{at: time.Now(), rows: rows}
	boardMu.Unlock()
	return rows, nil
}

// CustomerPayout is a payout row as the customer catalogue shows it: the
// row with payoutPercent set to the engine's current figure (the lower of UP
// and DOWN), plus the per-direction figures. Additive to models.PayoutView,
// so apps that read payoutPercent keep working.
type CustomerPayout struct {
	models.PayoutView
	// The row's own percent (the formula's base payout in DYNAMIC mode).
	ConfiguredPayoutPercent float64  `json:"configuredPayoutPercent"`
	PayoutPercentUp         *float64 `json:"payoutPercentUp"`
	PayoutPercentDown       *float64 `json:"payoutPercentDown"`
	PricingMode             string   `json:"pricingMode"`
	RuleVersion             string   `json:"ruleVersion"`
}

// Unpriced is an expiry the engine cannot offer right now.
type Unpriced struct {
	ExpirySeconds int64  `json:"expirySeconds"`
	Reason        string `json:"reason"`
	Message       string `json:"message"`
}

// AssetPricing is the additive pricing block of an asset in the catalogue.
type AssetPricing struct {
	// The instrument has an enabled pricing rule: it can be offered at all.
	// The apps hide an asset with priced false (launch scope: only BTC/USD
	// and ETH/USD are priced).
	Priced          bool       `json:"priced"`
	NotPricedReason string     `json:"notPricedReason,omitempty"`
	Mode            string     `json:"mode"`
	RuleVersion     string     `json:"ruleVersion"`
	ModelVersion    int64      `json:"modelVersion"`
	ComputedAt      int64      `json:"computedAt"`
	Halted          bool       `json:"halted"`
	HaltReason      string     `json:"haltReason,omitempty"`
	Unavailable     []Unpriced `json:"unavailable"`
}

func sidePct(d *Decision) *float64 {
	if d == nil || !d.OK {
		return nil
	}
	p := d.PayoutPercent
	return &p
}

// CustomerPayouts is the catalogue's payout list for an asset. An expiry
// with neither direction offerable is left out (old apps then show it as
// unavailable) and listed under AssetPricing.Unavailable with the reason.
func CustomerPayouts(ctx context.Context, assetID string) ([]CustomerPayout, AssetPricing, error) {
	info := AssetPricing{Unavailable: []Unpriced{}, ComputedAt: util.NowMs(), Mode: models.PricingModeDynamic}
	asset, err := assets.RequireAssetDoc(ctx, assetID)
	if err != nil {
		return nil, info, err
	}
	if cfg, err := riskconfig.GetRiskConfig(ctx); err == nil {
		info.Priced, info.NotPricedReason = SymbolPriced(cfg, asset.Symbol)
	}
	rows, err := Board(ctx, asset)
	if err != nil {
		return nil, info, err
	}
	out := make([]CustomerPayout, 0, len(rows))
	for _, r := range rows {
		v := models.ToPayoutView(r.Payout)
		cp := CustomerPayout{PayoutView: v, ConfiguredPayoutPercent: r.Payout.PayoutPercent,
			PayoutPercentUp: sidePct(r.Up), PayoutPercentDown: sidePct(r.Down), PricingMode: r.Up.Mode,
			RuleVersion: r.Up.RuleVersion}
		info.Mode, info.RuleVersion = r.Up.Mode, r.Up.RuleVersion
		if mv := r.Up.ModelVersion(); mv > info.ModelVersion {
			info.ModelVersion = mv
		}
		if r.Up.Code == CodeTradingHalted || r.Up.Code == CodeSymbolHalted {
			info.Halted, info.HaltReason = true, r.Up.Reason
		}
		key := assetID + ":" + v.ID
		var best *float64
		for _, p := range []*float64{cp.PayoutPercentUp, cp.PayoutPercentDown} {
			if p != nil && (best == nil || *p < *best) {
				best = p
			}
		}
		if best == nil {
			boardMu.Lock()
			lg, ok := lastGood[key]
			boardMu.Unlock()
			if r.Up.Code == CodeStaleMarketData && ok && time.Since(lg.at) < lastGoodTTL {
				cp.PayoutPercent = lg.pct
				out = append(out, cp)
				continue
			}
			info.Unavailable = append(info.Unavailable, Unpriced{ExpirySeconds: v.ExpirySeconds, Reason: r.Up.Code, Message: r.Up.Reason})
			continue
		}
		cp.PayoutPercent = *best
		boardMu.Lock()
		lastGood[key] = lastGoodPrice{at: time.Now(), pct: *best}
		boardMu.Unlock()
		out = append(out, cp)
	}
	return out, info, nil
}

// AdminSide is one direction of the admin pricing view.
type AdminSide struct {
	OK            bool                     `json:"ok"`
	PayoutPercent *float64                 `json:"payoutPercent"`
	Code          string                   `json:"code,omitempty"`
	Reason        string                   `json:"reason,omitempty"`
	Components    models.PricingComponents `json:"components"`
	Guard         models.PricingGuard      `json:"guard"`
}

func adminSide(d *Decision) AdminSide {
	return AdminSide{OK: d.OK, PayoutPercent: sidePct(d), Code: d.Code, Reason: d.Reason, Components: d.Components, Guard: d.Guard}
}

// AdminRow is one (asset, expiry) of the admin pricing view.
type AdminRow struct {
	AssetID             string    `json:"assetId"`
	Symbol              string    `json:"symbol"`
	ExpirySeconds       int64     `json:"expirySeconds"`
	Mode                string    `json:"mode"`
	RuleVersion         string    `json:"ruleVersion"`
	PayoutConfigID      string    `json:"payoutConfigId"`
	PayoutConfigVersion int64     `json:"payoutConfigVersion"`
	BasePercent         float64   `json:"basePayoutPercent"`
	MinPercent          float64   `json:"minPayoutPercent"`
	MaxPercent          float64   `json:"maxPayoutPercent"`
	MarginPercent       float64   `json:"marginPercent"`
	MinStake            int64     `json:"minStake"`
	MaxStake            int64     `json:"maxStake"`
	ReferenceStake      int64     `json:"referenceStake"`
	SymbolEnabled       bool      `json:"symbolEnabled"`
	DisabledReason      string    `json:"disabledReason,omitempty"`
	FeedProvider        string    `json:"feedProvider"`
	Reference           float64   `json:"referencePrice"`
	RiskConfigVersion   int64     `json:"riskConfigVersion"`
	ModelVersion        int64     `json:"modelVersion"`
	Up                  AdminSide `json:"up"`
	Down                AdminSide `json:"down"`
}

// AdminBoard prices every asset's payout rows for the console.
func AdminBoard(ctx context.Context, assetID string) ([]AdminRow, error) {
	docs, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return nil, err
	}
	out := []AdminRow{}
	for _, a := range docs {
		if assetID != "" && a.ID.Hex() != assetID {
			continue
		}
		rows, err := Board(ctx, a)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			d := r.Up
			lo, hi := StakeBounds(d.Config, a, r.Payout)
			out = append(out, AdminRow{
				AssetID: a.ID.Hex(), Symbol: a.Symbol, ExpirySeconds: r.Payout.ExpirySeconds, Mode: d.Mode,
				RuleVersion: d.RuleVersion, PayoutConfigID: r.Payout.ID.Hex(), PayoutConfigVersion: r.Payout.Version,
				BasePercent: PayoutPercent(d.Terms.Base), MinPercent: PayoutPercent(d.Terms.Min),
				MaxPercent: PayoutPercent(d.Terms.Max), MarginPercent: PayoutPercent(d.Terms.Margin),
				MinStake: lo, MaxStake: hi, ReferenceStake: d.Stake, SymbolEnabled: d.Terms.Enabled,
				DisabledReason: d.Terms.DisabledReason, FeedProvider: d.FeedProvider, Reference: d.Reference,
				RiskConfigVersion: d.Config.Version, ModelVersion: max(r.Up.ModelVersion(), r.Down.ModelVersion()),
				Up: adminSide(r.Up), Down: adminSide(r.Down),
			})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Symbol != out[j].Symbol {
			return out[i].Symbol < out[j].Symbol
		}
		return out[i].ExpirySeconds < out[j].ExpirySeconds
	})
	return out, nil
}
