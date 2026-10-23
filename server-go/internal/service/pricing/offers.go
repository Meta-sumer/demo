package pricing

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/google/uuid"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/platform/util"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/riskconfig"
)

// Offers (spec section 8). GET /binary/quotes computes an offer for each
// direction of an (asset, expiry, stake): locked terms valid for
// pricingOfferTtlMs. A live offer is kept in Redis and is single use - the
// first lookup removes it, whatever the outcome - and every offer, issued or
// blocked, is recorded in pricing_offers (TTL OfferRetention) with all its
// payout components (spec 10: offer_created, offer_blocked).

func offerKey(id string) string { return "pricing:offer:" + mongox.DB().Name() + ":" + id }

// OfferView is one direction of a quote.
type OfferView struct {
	OfferID   *string          `json:"offerId"`
	Direction models.Direction `json:"direction"`
	Status    string           `json:"status"`
	Available bool             `json:"available"`
	// Profit percent (83 = 83%); profit and total return on a win, minor units.
	PayoutPercent   *float64 `json:"payoutPercent"`
	Profit          *int64   `json:"profit"`
	PotentialReturn *int64   `json:"potentialReturn"`
	// The mid the offer was computed on: acceptance requires the price not to
	// have moved more than maxSlippageBps from it.
	ReferencePrice *float64 `json:"referencePrice"`
	Code           *string  `json:"code"`
	Reason         *string  `json:"reason"`
}

// QuoteView is the GET /binary/quotes response.
type QuoteView struct {
	AssetID             string  `json:"assetId"`
	Symbol              string  `json:"symbol"`
	ExpirySeconds       int64   `json:"expirySeconds"`
	Amount              int64   `json:"amount"`
	AccountType         string  `json:"accountType"`
	Mode                string  `json:"mode"`
	RuleVersion         string  `json:"ruleVersion"`
	ModelVersion        int64   `json:"modelVersion"`
	RiskConfigVersion   int64   `json:"riskConfigVersion"`
	PayoutConfigVersion int64   `json:"payoutConfigVersion"`
	IssuedAt            int64   `json:"issuedAt"`
	ValidUntil          int64   `json:"validUntil"`
	ValidForMs          int64   `json:"validForMs"`
	MaxSlippageBps      float64 `json:"maxSlippageBps"`
	// The lower available payout (for a ticket that shows one figure); null
	// when neither direction is offered.
	PayoutPercent *float64  `json:"payoutPercent"`
	Up            OfferView `json:"up"`
	Down          OfferView `json:"down"`
}

// QuoteRequest asks for offers. Amount 0 quotes the reference stake.
type QuoteRequest struct {
	AssetID       string
	ExpirySeconds int64
	Amount        int64
	AccountType   string
}

// offerFrom turns a decision into the offer record.
func offerFrom(d *Decision, userID string, ttlMs int64, inline bool) models.PricingOffer {
	o := models.PricingOffer{
		ID: uuid.NewString(), Status: models.OfferStatusOK, UserID: userID, AssetID: d.Asset.ID.Hex(),
		Symbol: d.Asset.Symbol, Direction: d.Direction, ExpirySeconds: d.ExpirySeconds, Stake: d.Stake,
		AccountType: d.AccountType, Mode: d.Mode, Payout: d.PayoutFraction, PayoutPercent: d.PayoutPercent,
		Reference: d.Reference, CreatedAtMs: d.DecidedAt, ValidUntil: d.DecidedAt + ttlMs,
		RuleVersion: d.RuleVersion, RiskConfigVer: d.Config.Version, FeedProvider: d.FeedProvider, Inline: inline,
		Components: d.Components, Guard: d.Guard, CreatedAt: time.UnixMilli(d.DecidedAt).UTC(),
	}
	if d.Payout != nil {
		o.PayoutCfgID, o.PayoutCfgVer = d.Payout.ID.Hex(), d.Payout.Version
	}
	if !d.OK {
		o.Status, o.ReasonCode, o.Reason, o.ValidUntil = models.OfferStatusBlocked, d.Code, d.Reason, d.DecidedAt
	}
	return o
}

// recordOffers writes the audit records off the request path.
func recordOffers(offers []models.PricingOffer) {
	if len(offers) == 0 || mongox.DB() == nil {
		return
	}
	docs := make([]any, len(offers))
	for i := range offers {
		docs[i] = offers[i]
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := mongox.C(models.CollPricingOffers).InsertMany(ctx, docs); err != nil {
			logx.Warn("failed to record pricing offers", "err", err.Error())
		}
	}()
}

// RecordOffer records one offer synchronously (an offer priced at
// acceptance, whose record the trade refers to).
func RecordOffer(ctx context.Context, o models.PricingOffer) error {
	_, err := mongox.C(models.CollPricingOffers).InsertOne(ctx, o)
	return err
}

func viewOf(o models.PricingOffer) OfferView {
	v := OfferView{Direction: o.Direction, Status: o.Status, Available: o.Status == models.OfferStatusOK}
	if v.Available {
		id, pct, ref := o.ID, o.PayoutPercent, o.Reference
		profit := util.CalculateProfit(o.Stake, pct)
		ret := o.Stake + profit
		v.OfferID, v.PayoutPercent, v.Profit, v.PotentialReturn, v.ReferencePrice = &id, &pct, &profit, &ret, &ref
	} else {
		code, reason := o.ReasonCode, o.Reason
		v.Code, v.Reason = &code, &reason
	}
	return v
}

// IssueQuote prices both directions and issues an offer for each that can be
// offered.
func IssueQuote(ctx context.Context, userID string, in QuoteRequest) (QuoteView, error) {
	asset, err := assets.RequireAssetDoc(ctx, in.AssetID)
	if err != nil {
		return QuoteView{}, err
	}
	now := util.NowMs()
	if err := assets.AssertAssetTradable(asset, now); err != nil {
		return QuoteView{}, err
	}
	var sides [2]*Decision
	for i, dir := range []models.Direction{models.DirectionUp, models.DirectionDown} {
		d, err := Evaluate(ctx, Request{Asset: asset, Direction: dir, ExpirySeconds: in.ExpirySeconds,
			Stake: in.Amount, AccountType: in.AccountType, Now: now})
		if err != nil {
			return QuoteView{}, err
		}
		sides[i] = d
	}
	d0 := sides[0]
	ttl := max(d0.Rules.OfferTTLMs, 500)
	v := QuoteView{AssetID: asset.ID.Hex(), Symbol: asset.Symbol, ExpirySeconds: in.ExpirySeconds, Amount: d0.Stake,
		AccountType: d0.AccountType, Mode: d0.Mode, RuleVersion: d0.RuleVersion, RiskConfigVersion: d0.Config.Version,
		IssuedAt: now, ValidUntil: now + ttl, ValidForMs: ttl, MaxSlippageBps: d0.Rules.MaxSlippageBps}
	if d0.Payout != nil {
		v.PayoutConfigVersion = d0.Payout.Version
	}
	offers := make([]models.PricingOffer, 0, 2)
	pipe := redisx.R().Pipeline()
	for i, d := range sides {
		o := offerFrom(d, userID, ttl, false)
		offers = append(offers, o)
		if o.Status == models.OfferStatusOK {
			raw, _ := json.Marshal(o)
			// Kept a minute past validity, so a late acceptance is told
			// "expired" rather than "unknown".
			pipe.Set(ctx, offerKey(o.ID), raw, time.Duration(ttl)*time.Millisecond+time.Minute)
			if v.PayoutPercent == nil || o.PayoutPercent < *v.PayoutPercent {
				p := o.PayoutPercent
				v.PayoutPercent = &p
			}
		}
		if mv := d.ModelVersion(); mv > v.ModelVersion {
			v.ModelVersion = mv
		}
		if i == 0 {
			v.Up = viewOf(o)
		} else {
			v.Down = viewOf(o)
		}
	}
	if _, err := pipe.Exec(ctx); err != nil && !redisx.IsNil(err) {
		logx.Warn("failed to store offers", "err", err.Error())
		return QuoteView{}, apperr.Unavailable(apperr.ServiceUnavailable, "Quotes are temporarily unavailable. Please try again.")
	}
	recordOffers(offers)
	return v, nil
}

// AcceptTerms identifies the trade that accepts an offer.
type AcceptTerms struct {
	UserID        string
	OfferID       string
	AssetID       string
	Direction     models.Direction
	ExpirySeconds int64
	Stake         int64
	AccountType   string
}

// requote adds a fresh quote to an error's details, so the app can show the
// new terms without another round trip.
func requote(ctx context.Context, t AcceptTerms, details map[string]any) map[string]any {
	if q, err := IssueQuote(ctx, t.UserID, QuoteRequest{AssetID: t.AssetID, ExpirySeconds: t.ExpirySeconds,
		Amount: t.Stake, AccountType: t.AccountType}); err == nil {
		details["quote"] = q
	}
	return details
}

// WithRequote gives a refusal that is not the customer's - a halt, market
// data, the book's limits - a fresh quote in its details when it has none:
// an app that sent an offer is shown what is offered now (or, in the quote,
// why nothing is) instead of being left holding an offer it cannot use.
// Other errors are returned unchanged.
func WithRequote(ctx context.Context, t AcceptTerms, err error) error {
	ae, ok := apperr.As(err)
	if !ok {
		return err
	}
	switch ae.Code {
	case apperr.TradingHalted, apperr.FeedUnhealthy, apperr.NoPriceAvailable, apperr.ExposureLimitExceeded:
	default:
		return err
	}
	details := map[string]any{}
	switch d := ae.Details.(type) {
	case nil:
	case map[string]any:
		if _, has := d["quote"]; has {
			return err
		}
		for k, v := range d {
			details[k] = v
		}
	default:
		return err
	}
	if t.OfferID != "" {
		if _, has := details["offerId"]; !has {
			details["offerId"] = t.OfferID
		}
	}
	return apperr.New(ae.Code, ae.Message, ae.StatusCode, requote(ctx, t, details))
}

// TakeOffer consumes an offer and re-validates it (spec 8.2): the offer
// exists and is unused (removed on this first lookup, even if a check below
// fails), it is still valid, its symbol is not halted, market data is fresh,
// the mid has not moved more than maxSlippageBps from the offer's reference
// and the side's exposure still holds with the locked payout. tick is the
// quote the trade will enter at (its mid).
func TakeOffer(ctx context.Context, t AcceptTerms, tick *models.NormalisedTick, now int64) (*models.PricingOffer, error) {
	raw, err := redisx.R().GetDel(ctx, offerKey(t.OfferID)).Result()
	if err != nil && !redisx.IsNil(err) {
		return nil, err
	}
	invalid := func(msg string) error {
		return apperr.New(apperr.OfferInvalid, msg, 409, requote(ctx, t, map[string]any{"offerId": t.OfferID}))
	}
	if redisx.IsNil(err) || raw == "" {
		return nil, invalid("This offer is unknown or was already used. Check the new payout and confirm again.")
	}
	var o models.PricingOffer
	if json.Unmarshal([]byte(raw), &o) != nil {
		return nil, invalid("This offer is unknown or was already used. Check the new payout and confirm again.")
	}
	if o.UserID != t.UserID || o.AssetID != t.AssetID || o.Direction != t.Direction || o.ExpirySeconds != t.ExpirySeconds ||
		models.NormAccountType(o.AccountType) != models.NormAccountType(t.AccountType) || o.Stake != t.Stake ||
		o.Status != models.OfferStatusOK {
		return nil, invalid("This offer was made for a different trade. Check the new payout and confirm again.")
	}
	if now > o.ValidUntil {
		return nil, apperr.New(apperr.OfferExpired, "Your offer has expired. Check the latest payout and confirm again.", 409,
			requote(ctx, t, map[string]any{"offerId": o.ID, "validUntil": o.ValidUntil}))
	}
	cfg, err := riskconfig.GetRiskConfig(ctx)
	if err != nil {
		return nil, err
	}
	// Refusals that are not the customer's (halt, feed, the book) consume the
	// offer too, so they also carry a fresh quote: the app shows what is
	// offered now (or why nothing is) instead of holding a used offer.
	if code, reason, halted := HaltFor(cfg, o.Symbol); halted {
		return nil, apperr.New(apperr.TradingHalted, "Trading on "+o.Symbol+" is halted: "+reason, 503,
			requote(ctx, t, map[string]any{"reason": code, "symbol": o.Symbol, "offerId": o.ID}))
	}
	// Re-validated under the same freshness rule the offer was issued under.
	if fresh, err := marketdata.CheckFreshness(ctx, o.AssetID, tick, now); err != nil {
		return nil, err
	} else if !fresh.OK {
		return nil, apperr.New(apperr.FeedUnhealthy, "Market data for "+o.Symbol+" is not fresh. New trades are paused.", 503,
			requote(ctx, t, map[string]any{"reason": CodeStaleMarketData, "detail": fresh.Reason, "offerId": o.ID}))
	}
	if move := slippageBps(tick.Price, o.Reference); move > cfg.PricingMaxSlippageBps {
		return nil, apperr.New(apperr.PriceMoved,
			"The price moved while your order was in flight. Review the new price and confirm again.", 409,
			requote(ctx, t, map[string]any{
				"serverPrice": tick.Price, "quotedPrice": o.Reference, "movePercent": roundTo(move/100, 4),
				"tolerancePercent": cfg.PricingMaxSlippageBps / 100, "moveBps": roundTo(move, 4),
				"maxSlippageBps": cfg.PricingMaxSlippageBps,
			}))
	}
	if models.NormAccountType(o.AccountType) != models.AccountDemo {
		terms, _ := TermsFor(cfg, o.Symbol, nil)
		sl, err := exposure.GetSideLiability(ctx, o.AssetID)
		if err != nil {
			return nil, err
		}
		l := sl.Down
		if o.Direction == models.DirectionUp {
			l = sl.Up
		}
		if terms.MaxExposure > 0 && max(0, float64(l)+float64(o.Stake)*o.Payout) > float64(terms.MaxExposure) {
			return nil, apperr.New(apperr.ExposureLimitExceeded,
				"Exposure limit reached on the "+string(o.Direction)+" side for "+o.Symbol, 400,
				requote(ctx, t, map[string]any{"reason": CodeExposureLimit, "liability": l, "maxExposure": terms.MaxExposure, "offerId": o.ID}))
		}
	}
	return &o, nil
}

// CheckDisplayed applies to a trade sent without an offer: refused for a
// re-quote when the payout the app displayed is more than the tolerance
// above the price now; otherwise the trade is accepted at the price now.
func CheckDisplayed(ctx context.Context, d *Decision, displayed *float64, t AcceptTerms) error {
	if displayed == nil || d.PayoutPercent >= *displayed-d.Rules.DisplayTolerancePercent-1e-9 {
		return nil
	}
	return apperr.New(apperr.PayoutChanged,
		"The payout changed to "+strconv.FormatFloat(d.PayoutPercent, 'f', -1, 64)+"%. Check the new payout and confirm again.", 409,
		requote(ctx, t, map[string]any{"displayedPayoutPercent": *displayed, "currentPayoutPercent": d.PayoutPercent}))
}

// InlineOffer records the offer a trade without an offer id was priced at.
func InlineOffer(d *Decision, userID string) models.PricingOffer {
	return offerFrom(d, userID, 0, true)
}

// RecordOfferRecord is the trade's copy of an accepted offer.
func RecordOfferRecord(o *models.PricingOffer, acceptMid float64) *models.TradePricing {
	return &models.TradePricing{
		Mode: o.Mode, RuleVersion: o.RuleVersion, RiskConfigVer: o.RiskConfigVer, PayoutCfgVer: o.PayoutCfgVer,
		PayoutPercent: o.PayoutPercent, OfferID: o.ID, Inline: o.Inline, OfferCreatedAt: o.CreatedAtMs,
		OfferValidUntil: o.ValidUntil, ReferencePrice: o.Reference, AcceptMid: acceptMid,
		SlippageBps: roundTo(slippageBps(acceptMid, o.Reference), 6), FeedProvider: o.FeedProvider,
		Components: o.Components, Guard: o.Guard,
	}
}

// AuditOffer records an offer (typically a blocked inline one) off the
// request path.
func AuditOffer(o models.PricingOffer) { recordOffers([]models.PricingOffer{o}) }
