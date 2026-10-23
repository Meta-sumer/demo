package hedging

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/marketdata"
)

// Venue snapshot: the leader's hedge worker reads the venue's book, mark,
// funding rate, fee rate and order filters for every hedgeable symbol and
// stores them in Redis, so any instance can answer a cost estimate (it runs
// on every price quote) without calling the venue. A snapshot older than
// HEDGE_VENUE_MAX_AGE_MS, or one that recorded a failure, means the venue is
// unavailable.

// VenueSnapshot is one symbol's venue state.
type VenueSnapshot struct {
	Venue           string            `json:"venue"`
	Symbol          string            `json:"symbol"`
	OK              bool              `json:"ok"`
	Error           string            `json:"error,omitempty"`
	Bid             float64           `json:"bid"`
	Ask             float64           `json:"ask"`
	Mark            float64           `json:"mark"`
	FundingRate     float64           `json:"fundingRate"`
	NextFundingTime int64             `json:"nextFundingTime"`
	TakerRate       float64           `json:"takerRate"`
	TakerRateSource string            `json:"takerRateSource"`
	Rules           models.HedgeRules `json:"rules"`
	UpdatedAt       int64             `json:"updatedAt"`
}

// Mid of the book (0 without both sides).
func (s VenueSnapshot) Mid() float64 {
	if s.Bid <= 0 || s.Ask <= 0 {
		return 0
	}
	return (s.Bid + s.Ask) / 2
}

func venueKey(symbol string) string { return "hedge:venue:" + mongox.DB().Name() + ":" + symbol }

func venueMaxAge() int64 {
	if config.E != nil && config.E.HedgeVenueMaxAgeMs > 0 {
		return config.E.HedgeVenueMaxAgeMs
	}
	return 15_000
}

// RefreshVenueSnapshot reads the venue and stores the result (a failure is
// stored too, so readers learn of it at once).
func RefreshVenueSnapshot(ctx context.Context, v providers.Venue, symbol string) VenueSnapshot {
	snap := VenueSnapshot{Venue: v.ID(), Symbol: symbol, UpdatedAt: util.NowMs()}
	err := func() error {
		rules, err := v.Rules(ctx, symbol)
		if err != nil {
			return err
		}
		q, err := v.Quote(ctx, symbol)
		if err != nil {
			return err
		}
		m, err := v.Mark(ctx, symbol)
		if err != nil {
			return err
		}
		snap.Rules, snap.Bid, snap.Ask = rules, q.Bid, q.Ask
		snap.Mark, snap.FundingRate, snap.NextFundingTime = m.MarkPrice, m.FundingRate, m.NextFundingTime
		if snap.Mid() <= 0 {
			return errors.New("venue book has no bid or ask")
		}
		// The account's own taker fee when the venue reports it.
		if c, err := v.Commission(ctx, symbol); err == nil && c.TakerRate > 0 {
			snap.TakerRate, snap.TakerRateSource = c.TakerRate, "venue"
		}
		return nil
	}()
	if err != nil {
		snap.OK, snap.Error = false, err.Error()
	} else {
		snap.OK = true
	}
	if raw, err := json.Marshal(snap); err == nil {
		_ = redisx.R().Set(ctx, venueKey(symbol), raw, time.Hour).Err()
	}
	return snap
}

// GetVenueSnapshot returns the stored snapshot (nil when there is none).
func GetVenueSnapshot(ctx context.Context, symbol string) (*VenueSnapshot, error) {
	raw, err := redisx.R().Get(ctx, venueKey(symbol)).Result()
	if redisx.IsNil(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s VenueSnapshot
	if json.Unmarshal([]byte(raw), &s) != nil {
		return nil, nil
	}
	return &s, nil
}

// paperPrices maps venue symbols to platform assets for the paper venue,
// which fills against the platform's own last tick.
var (
	paperMu     sync.Mutex
	paperAssets = map[string]string{}
)

func rememberVenueSymbol(venueSymbol, assetID string) {
	paperMu.Lock()
	paperAssets[venueSymbol] = assetID
	paperMu.Unlock()
}

func paperPrice(ctx context.Context, symbol string) (float64, float64, bool) {
	paperMu.Lock()
	assetID := paperAssets[symbol]
	paperMu.Unlock()
	if assetID == "" {
		return 0, 0, false
	}
	tick, err := marketdata.GetLastTick(ctx, assetID)
	if err != nil || tick == nil || tick.Price <= 0 {
		return 0, 0, false
	}
	if tick.Bid != nil && tick.Ask != nil && *tick.Bid > 0 && *tick.Ask >= *tick.Bid {
		return *tick.Bid, *tick.Ask, true
	}
	half := 0.5
	if config.E != nil {
		half = config.E.HedgeFallbackSpreadBps / 2
	}
	return tick.Price * (1 - half/10_000), tick.Price * (1 + half/10_000), true
}

// venue returns the configured venue, wiring the paper venue's price source.
func venue() providers.Venue {
	v := providers.GetVenue()
	if p, ok := v.(*providers.PaperVenue); ok {
		p.SetPrice(paperPrice)
	}
	return v
}
