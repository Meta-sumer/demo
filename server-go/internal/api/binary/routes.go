// Package binary is the trade API (spec section 7), mounted at /binary.
package binary

import (
	"encoding/json"
	"math"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	bsvc "binarytrading/internal/service/binary"
	"binarytrading/internal/service/pricing"
)

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)

	r.With(httpx.RequireActiveUser, httpx.RateLimit(httpx.RateLimits.Trade)).Post("/trades", httpx.H(createTrade))
	read := r.With(httpx.RateLimit(httpx.RateLimits.Read))
	read.Get("/trades", httpx.H(listTrades))
	read.Get("/trades/summary", httpx.H(tradeSummary))
	read.Get("/trades/open", httpx.H(openTrades))
	read.Get("/trades/{id}", httpx.H(getTrade))
	read.Get("/trades/{id}/quote", httpx.H(quoteTrade))
	// Entry pricing quote: the engine's payout for both directions, with a
	// quote id per direction to trade on (docs/audit/pricing/pricing-spec.md).
	read.Get("/quotes", httpx.H(entryQuote))

	// Closing early moves money, so it is rate limited like opening a trade.
	r.With(httpx.RequireActiveUser, httpx.RateLimit(httpx.RateLimits.Trade)).Post("/trades/{id}/close", httpx.H(closeTrade))
}

var hexID = regexp.MustCompile(`^[a-fA-F0-9]{24}$`)

// ── body parsing (Zod createBinaryTradeSchema) ──────────────────────────

// jsonKind names a raw JSON value the way Zod's "received" does.
func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "undefined"
	}
	switch s[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	}
	return "number"
}

type bodyReader struct {
	fields map[string]json.RawMessage
	v      *httpx.V
}

// str reads a string field; expected is the Zod "Expected ..." label.
func (b *bodyReader) str(name string, required bool, expected string) (string, bool) {
	raw, ok := b.fields[name]
	if !ok {
		if required {
			b.v.Add(name, "Required")
		}
		return "", false
	}
	if kind := jsonKind(raw); kind != "string" {
		b.v.Add(name, "Expected "+expected+", received "+kind)
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		b.v.Add(name, "Expected "+expected+", received string")
		return "", false
	}
	return s, true
}

func (b *bodyReader) num(name string, required bool) (float64, bool) {
	raw, ok := b.fields[name]
	if !ok {
		if required {
			b.v.Add(name, "Required")
		}
		return 0, false
	}
	if kind := jsonKind(raw); kind != "number" {
		b.v.Add(name, "Expected number, received "+kind)
		return 0, false
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		b.v.Add(name, "Expected number, received number")
		return 0, false
	}
	return f, true
}

// boundedInt applies z.number().int().positive().max(max).
func (b *bodyReader) boundedInt(name string, f float64, max float64) {
	b.v.IsInt(name, f)
	b.v.Positive(name, f)
	b.v.Check(f <= max, name, "Number must be less than or equal to "+formatNum(max))
}

func formatNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

func parseCreateTrade(r *http.Request) (bsvc.CreateTradeInput, error) {
	fields := map[string]json.RawMessage{}
	if err := httpx.DecodeBody(r, &fields); err != nil {
		return bsvc.CreateTradeInput{}, err
	}
	v := httpx.NewV()
	b := &bodyReader{fields: fields, v: v}
	var in bsvc.CreateTradeInput

	if s, ok := b.str("assetId", true, "string"); ok {
		v.Match("assetId", s, hexID, "assetId must be a valid id")
		in.AssetID = s
	}
	if s, ok := b.str("direction", true, "'UP' | 'DOWN'"); ok {
		v.Enum("direction", s, models.DirectionValues...)
		in.Direction = models.Direction(s)
	}
	if f, ok := b.num("amount", true); ok {
		b.boundedInt("amount", f, 1_000_000_000)
		in.Amount = int64(f)
	}
	if f, ok := b.num("expirySeconds", true); ok {
		b.boundedInt("expirySeconds", f, 86_400)
		in.ExpirySeconds = int64(f)
	}
	if f, ok := b.num("quotedPrice", false); ok {
		v.Positive("quotedPrice", f)
		v.Check(!math.IsInf(f, 0), "quotedPrice", "Number must be finite")
		in.QuotedPrice = &f
	}
	if s, ok := b.str("idempotencyKey", false, "string"); ok {
		v.Str("idempotencyKey", s, 8, 128)
		in.IdempotencyKey = &s
	}
	in.AccountType = models.AccountLive
	if s, ok := b.str("accountType", false, "'LIVE' | 'DEMO'"); ok {
		v.Enum("accountType", s, models.AccountTypeValues...)
		in.AccountType = s
	}
	// Pricing: the offer accepted, and the payout on screen. Optional - apps
	// that predate offers send neither and are priced at acceptance.
	if s, ok := b.str("offerId", false, "string"); ok {
		v.Str("offerId", s, 8, 128)
		in.OfferID = &s
	}
	if f, ok := b.num("displayedPayoutPercent", false); ok {
		v.Check(f >= 0 && f <= 500, "displayedPayoutPercent", "Number must be between 0 and 500")
		in.DisplayedPayoutPercent = &f
	}
	if err := v.Err("request body"); err != nil {
		return bsvc.CreateTradeInput{}, err
	}
	return in, nil
}

// inputDoc is the parsed input as the Node audit row stores it.
func inputDoc(in bsvc.CreateTradeInput) bson.D {
	d := bson.D{
		{Key: "assetId", Value: in.AssetID},
		{Key: "direction", Value: string(in.Direction)},
		{Key: "amount", Value: in.Amount},
		{Key: "expirySeconds", Value: in.ExpirySeconds},
	}
	if in.QuotedPrice != nil {
		d = append(d, bson.E{Key: "quotedPrice", Value: *in.QuotedPrice})
	}
	if in.IdempotencyKey != nil {
		d = append(d, bson.E{Key: "idempotencyKey", Value: *in.IdempotencyKey})
	}
	d = append(d, bson.E{Key: "accountType", Value: in.AccountType})
	if in.OfferID != nil {
		d = append(d, bson.E{Key: "offerId", Value: *in.OfferID})
	}
	if in.DisplayedPayoutPercent != nil {
		d = append(d, bson.E{Key: "displayedPayoutPercent", Value: *in.DisplayedPayoutPercent})
	}
	return d
}

// ── handlers ────────────────────────────────────────────────────────────

func createTrade(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	input, err := parseCreateTrade(r)
	if err != nil {
		return err
	}

	var headerKey *string
	if vals := r.Header.Values("Idempotency-Key"); len(vals) > 0 {
		k := vals[0]
		headerKey = &k
	}
	ip := httpx.ClientIP(r)

	result, err := bsvc.CreateTrade(r.Context(), input, bsvc.TradeContext{
		UserID:         actor.UserID,
		IP:             &ip,
		IdempotencyKey: headerKey,
	})
	if err != nil {
		e := audit.FromRequest(actor, httpx.AuditMeta(r))
		e.Action = audit.TradeRejected
		e.Resource = "BinaryTrade"
		e.Before = inputDoc(input)
		e.Failed = true
		e.Reason = errorMessage(err)
		audit.Log(e)
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.TradeCreated
	e.Resource = "BinaryTrade"
	e.ResourceID = result.Trade.ID
	after := bson.D{
		{Key: "symbol", Value: result.Trade.Symbol},
		{Key: "direction", Value: string(result.Trade.Direction)},
		{Key: "accountType", Value: result.Trade.AccountType},
		{Key: "stake", Value: result.Trade.Stake},
		{Key: "entryPrice", Value: result.Trade.EntryPrice},
		{Key: "payoutPercent", Value: result.Trade.PayoutPercent},
		{Key: "offerId", Value: result.Trade.OfferID},
		{Key: "expiryTimestamp", Value: result.Trade.ExpiryTimestamp},
		{Key: "replayed", Value: result.Replayed},
	}
	if len(result.Degraded) > 0 {
		// Accepted; these Redis steps are left to the repairs from MongoDB.
		after = append(after, bson.E{Key: "degraded", Value: result.Degraded})
	}
	e.After = after
	audit.Log(e)

	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	return httpx.Send(w, status, result.Trade, map[string]any{"replayed": result.Replayed})
}

// errorMessage is JS `(err as Error).message`: the AppError message, not the
// Go "CODE: message" form.
func errorMessage(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}

func listTrades(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q, err := ParseListTradesQuery(r, false)
	if err != nil {
		return err
	}
	q.UserID = actor.UserID
	items, total, err := bsvc.ListTrades(r.Context(), q)
	if err != nil {
		return err
	}
	return httpx.OK(w, httpx.Paginate(items, q.Page, q.Limit, total))
}

// tradeSummary totals the caller's trades for the list's filters, across
// every page.
func tradeSummary(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q, err := ParseTradeSummaryQuery(r)
	if err != nil {
		return err
	}
	q.UserID = actor.UserID
	summary, err := bsvc.SummariseTrades(r.Context(), q)
	if err != nil {
		return err
	}
	return httpx.OK(w, summary)
}

func openTrades(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	items, err := bsvc.GetOpenTrades(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, items)
}

func getTrade(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := ParseTradeIDParam(r)
	if err != nil {
		return err
	}
	view, err := bsvc.GetTrade(r.Context(), id, actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, view)
}

// entryQuote issues offers for a prospective trade (pricing specification
// 8): GET /binary/quotes?assetId=&expirySeconds=&amount=&accountType=. One
// offer per direction, valid for pricingOfferTtlMs and usable once.
func entryQuote(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	assetID := q.Str("assetId")
	q.V.Match("assetId", assetID, hexID, "assetId must be a valid id")
	expiry := q.Int("expirySeconds", 0, 1, 86_400)
	if !q.Has("expirySeconds") {
		q.V.Add("expirySeconds", "Required")
	}
	amount := q.OptInt("amount", 1, 1_000_000_000)
	account := q.Enum("accountType", models.AccountLive, models.AccountTypeValues...)
	if err := q.Err(); err != nil {
		return err
	}
	in := pricing.QuoteRequest{AssetID: assetID, ExpirySeconds: expiry, AccountType: account}
	if amount != nil {
		in.Amount = *amount
	}
	view, err := pricing.IssueQuote(r.Context(), actor.UserID, in)
	if err != nil {
		return err
	}
	return httpx.OK(w, view)
}

// quoteTrade is the live "Sell now" price for one open position.
func quoteTrade(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := ParseTradeIDParam(r)
	if err != nil {
		return err
	}
	quote, err := bsvc.QuoteTrade(r.Context(), id, actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, quote)
}

func closeTrade(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := ParseTradeIDParam(r)
	if err != nil {
		return err
	}
	result, err := bsvc.CloseTradeEarly(r.Context(), id, actor.UserID)
	if err != nil {
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = "trade.closed_early"
	e.Resource = "BinaryTrade"
	e.ResourceID = id
	e.After = bson.D{
		{Key: "closeValue", Value: result.Quote.Value},
		{Key: "stake", Value: result.Trade.Stake},
		{Key: "winProbability", Value: result.Quote.WinProbability},
		{Key: "customerResult", Value: result.Quote.CustomerResult},
	}
	audit.Log(e)

	return httpx.OK(w, result)
}

// ── shared parsers (also used by the admin trading routes) ──────────────

// ParseTradeIDParam validates the {id} path parameter (tradeIdParamSchema).
func ParseTradeIDParam(r *http.Request) (string, error) {
	v := httpx.NewV()
	id := chi.URLParam(r, "id")
	v.Match("id", id, hexID, "Invalid trade id")
	if err := v.Err("path parameters"); err != nil {
		return "", err
	}
	return id, nil
}

// ParseListTradesQuery parses listTradesQuerySchema, or
// adminListTradesQuerySchema (adds userId) when admin is true. Both accept
// direction and accountType; without accountType both accounts are listed.
func ParseListTradesQuery(r *http.Request, admin bool) (bsvc.TradeListQuery, error) {
	q := httpx.Query(r)
	out := parseTradeFilters(q, admin, true)
	if err := q.Err(); err != nil {
		return bsvc.TradeListQuery{}, err
	}
	return out, nil
}

// ParseTradeSummaryQuery parses the customer list's filters without paging
// (page and limit are ignored): the summary covers every page.
func ParseTradeSummaryQuery(r *http.Request) (bsvc.TradeListQuery, error) {
	q := httpx.Query(r)
	out := parseTradeFilters(q, false, false)
	if err := q.Err(); err != nil {
		return bsvc.TradeListQuery{}, err
	}
	return out, nil
}

// parseTradeFilters reads the list filters, and page/limit when paged, in
// schema order.
func parseTradeFilters(q *httpx.Q, admin, paged bool) bsvc.TradeListQuery {
	var out bsvc.TradeListQuery
	if s := q.OptEnum("status", models.TradeStatusValues...); s != nil {
		out.Status = *s
	}
	if s := q.OptEnum("phase", bsvc.PhaseOpen, bsvc.PhaseSettled); s != nil {
		out.Phase = *s
	}
	if s := q.OptStr("assetId"); s != nil {
		q.V.Match("assetId", *s, hexID, "")
		out.AssetID = *s
	}
	out.From = q.OptDate("from")
	out.To = q.OptDate("to")
	if paged {
		out.Page, out.Limit = q.Page()
	}
	if s := q.OptEnum("direction", models.DirectionValues...); s != nil {
		out.Direction = *s
	}
	if s := q.OptEnum("accountType", models.AccountTypeValues...); s != nil {
		out.AccountType = *s
	}
	if admin {
		if s := q.OptStr("userId"); s != nil {
			q.V.Match("userId", *s, hexID, "")
			out.UserID = *s
		}
	}
	return out
}

// ParseCancelTradeBody parses cancelTradeSchema ({reason: 3..500 chars}).
func ParseCancelTradeBody(r *http.Request) (string, error) {
	fields := map[string]json.RawMessage{}
	if err := httpx.DecodeBody(r, &fields); err != nil {
		return "", err
	}
	v := httpx.NewV()
	b := &bodyReader{fields: fields, v: v}
	reason, ok := b.str("reason", true, "string")
	if ok {
		v.Str("reason", reason, 3, 500)
	}
	if err := v.Err("request body"); err != nil {
		return "", err
	}
	return reason, nil
}
