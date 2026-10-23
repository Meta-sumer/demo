package admin

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/platform/iso"
	"binarytrading/internal/service/audit"
	"binarytrading/internal/service/hedging"
	"binarytrading/internal/service/hedging/providers"
)

// Hedging routes beyond the monitor, the order list and cancel (which live
// in mountTrading): venue/rate/reconciliation status, the fills ledger,
// alerts, the INR rate, a manual reconciliation pass, P&L corrections and
// the replay of a stored decision. docs/audit/hedging/.

const (
	auditHedgeFxRateSet      = "hedge.fx_rate_set"
	auditHedgeAlertResolved  = "hedge.alert_resolved"
	auditHedgeReconcileRun   = "hedge.reconcile_run"
	auditHedgeAdjustmentMade = "hedge.adjustment_created"
)

func mountHedging(perm, write func(p string) chi.Router) {
	perm(config.PermHedgeRead).Get("/hedging/status", httpx.H(getHedgeStatus))
	perm(config.PermHedgeRead).Get("/hedging/fills", httpx.H(listHedgeFills))
	perm(config.PermHedgeRead).Get("/hedging/alerts", httpx.H(listHedgeAlerts))
	perm(config.PermHedgeRead).Get("/hedging/orders/{id}/replay", httpx.H(replayHedgeOrder))
	write(config.PermHedgeWrite).Post("/hedging/alerts/{id}/resolve", httpx.H(resolveHedgeAlert))
	write(config.PermHedgeWrite).Put("/hedging/fx-rate", httpx.H(setHedgeFxRate))
	write(config.PermHedgeWrite).Post("/hedging/reconcile", httpx.H(runHedgeReconcile))
	write(config.PermHedgeWrite).Post("/hedging/adjustments", httpx.H(createHedgeAdjustment))
}

func getHedgeStatus(w http.ResponseWriter, r *http.Request) error {
	s, err := hedging.GetHedgeStatus(r.Context())
	if err != nil {
		return err
	}
	return httpx.OK(w, s)
}

type hedgeFillView struct {
	ID            string   `json:"id"`
	Venue         string   `json:"venue"`
	VenueSymbol   string   `json:"venueSymbol"`
	TradeID       int64    `json:"tradeId"`
	VenueOrderID  string   `json:"venueOrderId"`
	ClientOrderID string   `json:"clientOrderId"`
	HedgeOrderID  *string  `json:"hedgeOrderId"`
	AssetID       string   `json:"assetId"`
	Symbol        string   `json:"symbol"`
	Side          string   `json:"side"`
	Qty           string   `json:"qty"`
	Price         float64  `json:"price"`
	Quote         string   `json:"quote"`
	Fee           string   `json:"fee"`
	FeeAsset      string   `json:"feeAsset"`
	FeeInr        int64    `json:"feeInr"`
	Realised      string   `json:"realised"`
	RealisedInr   int64    `json:"realisedInr"`
	PositionAfter string   `json:"positionAfter"`
	AvgPriceAfter float64  `json:"avgPriceAfter"`
	InrPerUsdt    float64  `json:"inrPerUsdt"`
	FxStale       bool     `json:"fxStale"`
	TradeTime     iso.Time `json:"tradeTime"`
}

func listHedgeFills(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	assetID := q.OptStr("assetId")
	page, limit := q.Page()
	if err := q.Err(); err != nil {
		return err
	}
	id := ""
	if assetID != nil {
		id = *assetID
	}
	items, total, err := hedging.ListFills(r.Context(), id, page, limit)
	if err != nil {
		return err
	}
	views := make([]hedgeFillView, 0, len(items))
	for _, f := range items {
		v := hedgeFillView{ID: f.ID.Hex(), Venue: f.Venue, VenueSymbol: f.VenueSymbol, TradeID: f.TradeID,
			VenueOrderID: f.VenueOrderID, ClientOrderID: f.ClientOrderID, AssetID: f.AssetID.Hex(), Symbol: f.Symbol,
			Side: f.Side, Qty: providers.FormatE8(f.QtyE8), Price: f.Price, Quote: providers.FormatE8(f.QuoteE8),
			Fee: providers.FormatE8(f.FeeE8), FeeAsset: f.FeeAsset, FeeInr: f.FeeInr, Realised: providers.FormatE8(f.RealisedE8),
			RealisedInr: f.RealisedInr, PositionAfter: providers.FormatE8(f.PositionAfterE8), AvgPriceAfter: f.AvgPriceAfter,
			InrPerUsdt: f.InrPerUsdt, FxStale: f.FxStale, TradeTime: iso.From(time.UnixMilli(f.TradeTime).UTC())}
		if f.HedgeOrderID != nil {
			h := f.HedgeOrderID.Hex()
			v.HedgeOrderID = &h
		}
		views = append(views, v)
	}
	return httpx.OK(w, httpx.Paginate(views, page, limit, total))
}

func listHedgeAlerts(w http.ResponseWriter, r *http.Request) error {
	q := httpx.Query(r)
	status := q.OptEnum("status", models.AlertOpen, models.AlertResolved)
	limit := q.Int("limit", 50, 1, 200)
	if err := q.Err(); err != nil {
		return err
	}
	s := ""
	if status != nil {
		s = *status
	}
	items, err := hedging.ListAlerts(r.Context(), s, limit)
	if err != nil {
		return err
	}
	return httpx.OK(w, nonNil(items))
}

func resolveHedgeAlert(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	var body struct {
		Note *string `json:"note"`
	}
	if err := httpx.DecodeBody(r, &body); err != nil {
		return err
	}
	v := httpx.NewV()
	note := ""
	if v.Required("note", body.Note != nil) {
		note = strings.TrimSpace(*body.Note)
		v.Str("note", note, 3, 1000)
	}
	if err := v.Err("request body"); err != nil {
		return err
	}
	by := actor.ObjectID()
	view, err := hedging.ResolveAlert(r.Context(), id, &by, note)
	if err != nil {
		return err
	}
	e := entry(r, actor, auditHedgeAlertResolved, "HedgeAlert", id.Hex())
	e.After, e.Reason = auditDoc(view), note
	audit.Write(r.Context(), e)
	return httpx.OK(w, view)
}

func setHedgeFxRate(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	var body struct {
		InrPerUsdt *float64 `json:"inrPerUsdt"`
		Source     *string  `json:"source"`
		AsOf       *string  `json:"asOf"`
		Note       *string  `json:"note"`
	}
	if err := httpx.DecodeBody(r, &body); err != nil {
		return err
	}
	v := httpx.NewV()
	in := hedging.SetFxRateInput{}
	if v.Required("inrPerUsdt", body.InrPerUsdt != nil) {
		in.Rate = *body.InrPerUsdt
		v.Float("inrPerUsdt", in.Rate, 1, 1000)
	}
	if body.Source != nil {
		in.Source = strings.TrimSpace(*body.Source)
		v.Str("source", in.Source, 1, 100)
	}
	if body.AsOf != nil {
		t, perr := time.Parse(time.RFC3339, *body.AsOf)
		if v.Check(perr == nil, "asOf", "Invalid datetime") {
			in.AsOf = t
		}
	}
	if body.Note != nil {
		n := strings.TrimSpace(*body.Note)
		v.Str("note", n, 0, 500)
		in.Note = &n
	}
	if err := v.Err("request body"); err != nil {
		return err
	}
	before, err := hedging.CurrentFX(r.Context())
	if err != nil {
		return err
	}
	by := actor.ObjectID()
	in.SetBy = &by
	row, err := hedging.SetFxRate(r.Context(), in)
	if err != nil {
		return err
	}
	after, err := hedging.CurrentFX(r.Context())
	if err != nil {
		return err
	}
	e := entry(r, actor, auditHedgeFxRateSet, "HedgeFxRate", row.ID.Hex())
	e.Before, e.After = auditDoc(before), auditDoc(after)
	audit.Write(r.Context(), e)
	return httpx.OK(w, after)
}

func runHedgeReconcile(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	res, err := hedging.ReconcilePositions(r.Context())
	if err != nil {
		return err
	}
	e := entry(r, actor, auditHedgeReconcileRun, "HedgePosition", "")
	e.After = auditDoc(res)
	audit.Write(r.Context(), e)
	return httpx.OK(w, nonNil(res))
}

func createHedgeAdjustment(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	var body struct {
		AssetID *string  `json:"assetId"`
		Amount  *float64 `json:"amount"`
		Reason  *string  `json:"reason"`
	}
	if err := httpx.DecodeBody(r, &body); err != nil {
		return err
	}
	v := httpx.NewV()
	in := hedging.AddAdjustmentInput{}
	if v.Required("assetId", body.AssetID != nil) {
		in.AssetID = v.ObjectID("assetId", *body.AssetID)
	}
	if v.Required("amount", body.Amount != nil) && v.IsInt("amount", *body.Amount) {
		v.Check(*body.Amount != 0, "amount", "Amount cannot be 0")
		in.AmountInr = int64(*body.Amount)
	}
	if v.Required("reason", body.Reason != nil) {
		in.Reason = strings.TrimSpace(*body.Reason)
		v.Str("reason", in.Reason, 5, 500)
	}
	if err := v.Err("request body"); err != nil {
		return err
	}
	by := actor.ObjectID()
	in.CreatedBy = &by
	row, err := hedging.AddAdjustment(r.Context(), in)
	if err != nil {
		return err
	}
	e := entry(r, actor, auditHedgeAdjustmentMade, "HedgeAdjustment", row.ID.Hex())
	e.After, e.Reason = auditDoc(map[string]any{"reference": row.Reference, "assetId": row.AssetID.Hex(), "amountInr": row.AmountInr}), in.Reason
	audit.Write(r.Context(), e)
	return httpx.Created(w, map[string]any{"id": row.ID.Hex(), "reference": row.Reference, "assetId": row.AssetID.Hex(),
		"amountInr": row.AmountInr, "reason": row.Reason, "createdAt": iso.From(row.CreatedAt)})
}

func replayHedgeOrder(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	o, err := hedging.FindHedgeOrderByID(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	if o == nil {
		return apperr.NotFoundErr(apperr.NotFound, "Hedge order not found")
	}
	rep := hedging.ReplayDecision(o.Decision)
	return httpx.OK(w, map[string]any{
		"replay": rep, "stored": map[string]any{
			"bookDelta": o.Decision.Target.BookDelta, "targetSigned": o.Decision.Target.TargetSigned,
			"executableQtyE8": o.Decision.ExecutableQtyE8, "residualQtyE8": o.Decision.ResidualQtyE8,
		},
		"matches": rep.BookDelta == o.Decision.Target.BookDelta && rep.TargetSigned == o.Decision.Target.TargetSigned &&
			rep.Plan.ExecutableQtyE8 == o.Decision.ExecutableQtyE8,
	})
}
