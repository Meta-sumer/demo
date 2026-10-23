package pricing

import (
	"context"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/riskconfig"
)

// The public market board (GET /public/markets): what a signed-out landing
// page may show. Only active instruments the engine can price, and for each
// expiry the catalogue's own indicative payout - the lower of UP and DOWN at
// the reference stake on the LIVE book (CustomerPayouts) - plus the range
// across them. Nothing else: no ids, no stake or exposure figures, no halt or
// refusal reasons, and no per-direction split, which would let anyone without
// an account read the book's skew. It is built from the cached indicative
// board and kept for PublicMarketsTTL per process, so a request costs a map
// read; concurrent rebuilds collapse into one.

// PublicMarketsTTL is how long one snapshot is served.
const PublicMarketsTTL = 5 * time.Second

// PublicPayoutBasis names the figure published per expiry.
const PublicPayoutBasis = "LOWER_OF_UP_AND_DOWN"

// publicBuildTimeout bounds one rebuild; it is detached from the request
// that triggered it, so a caller that hangs up does not fail the others.
const publicBuildTimeout = 5 * time.Second

// PublicPayout is one offered expiry of a public market.
type PublicPayout struct {
	ExpirySeconds int64 `json:"expirySeconds"`
	// The lower of the UP and DOWN indicative payouts, in percent.
	PayoutPercent float64 `json:"payoutPercent"`
}

// PublicMarket is one instrument of the public board.
type PublicMarket struct {
	Symbol   string               `json:"symbol"`
	Name     string               `json:"name"`
	Category models.AssetCategory `json:"category"`
	// The expiries the engine can price right now, shortest first. Empty
	// while none can be (a halt, a feed outage): the instrument is still
	// listed, without figures.
	Payouts []PublicPayout `json:"payouts"`
	// Range of Payouts; null when it is empty.
	MinPayoutPercent *float64 `json:"minPayoutPercent"`
	MaxPayoutPercent *float64 `json:"maxPayoutPercent"`
}

// PublicMarkets is the GET /public/markets payload.
type PublicMarkets struct {
	Markets []PublicMarket `json:"markets"`
	// Range across every market's payouts; null when none is offered.
	MinPayoutPercent *float64 `json:"minPayoutPercent"`
	MaxPayoutPercent *float64 `json:"maxPayoutPercent"`
	PayoutBasis      string   `json:"payoutBasis"`
	GeneratedAt      iso.Time `json:"generatedAt"`
	// How long the server serves one snapshot.
	MaxAgeSeconds int64 `json:"maxAgeSeconds"`
}

var (
	publicMu     sync.Mutex
	publicAt     time.Time
	publicCached *PublicMarkets
	publicFlight singleflight.Group
)

// InvalidatePublicMarkets drops the cached snapshot.
func InvalidatePublicMarkets() {
	publicMu.Lock()
	publicCached = nil
	publicMu.Unlock()
}

// GetPublicMarkets returns the current public board. The result is shared:
// callers must treat it as read-only.
func GetPublicMarkets(ctx context.Context) (*PublicMarkets, error) {
	publicMu.Lock()
	if publicCached != nil && time.Since(publicAt) < PublicMarketsTTL {
		m := publicCached
		publicMu.Unlock()
		return m, nil
	}
	publicMu.Unlock()

	v, err, _ := publicFlight.Do("markets", func() (any, error) {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), publicBuildTimeout)
		defer cancel()
		m, err := buildPublicMarkets(c)
		if err != nil {
			return nil, err
		}
		publicMu.Lock()
		publicCached, publicAt = m, time.Now()
		publicMu.Unlock()
		return m, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*PublicMarkets), nil
}

func buildPublicMarkets(ctx context.Context) (*PublicMarkets, error) {
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	docs, err := assets.ListAssetDocs(ctx, true)
	if err != nil {
		return nil, err
	}
	out := &PublicMarkets{Markets: []PublicMarket{}, PayoutBasis: PublicPayoutBasis,
		GeneratedAt: iso.From(time.Now().UTC()), MaxAgeSeconds: int64(PublicMarketsTTL / time.Second)}
	all := []PublicPayout{}
	for _, a := range docs {
		// The apps never offer an instrument without an enabled pricing
		// rule; neither does the landing page.
		if priced, _ := SymbolPriced(cfg, a.Symbol); !priced {
			continue
		}
		rows, _, err := CustomerPayouts(ctx, a.ID.Hex())
		if err != nil {
			return nil, err
		}
		m := PublicMarket{Symbol: a.Symbol, Name: a.Name, Category: a.Category, Payouts: publicPayouts(rows)}
		m.MinPayoutPercent, m.MaxPayoutPercent = payoutRange(m.Payouts)
		all = append(all, m.Payouts...)
		out.Markets = append(out.Markets, m)
	}
	out.MinPayoutPercent, out.MaxPayoutPercent = payoutRange(all)
	return out, nil
}

// publicPayouts keeps only the expiry and the published figure of each
// catalogue row, shortest expiry first.
func publicPayouts(rows []CustomerPayout) []PublicPayout {
	out := make([]PublicPayout, 0, len(rows))
	for _, r := range rows {
		if r.PayoutPercent <= 0 {
			continue
		}
		out = append(out, PublicPayout{ExpirySeconds: r.ExpirySeconds, PayoutPercent: r.PayoutPercent})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ExpirySeconds < out[j].ExpirySeconds })
	return out
}

func payoutRange(ps []PublicPayout) (lo, hi *float64) {
	for _, p := range ps {
		v := p.PayoutPercent
		if lo == nil || v < *lo {
			lo = &v
		}
		v2 := p.PayoutPercent
		if hi == nil || v2 > *hi {
			hi = &v2
		}
	}
	return lo, hi
}
