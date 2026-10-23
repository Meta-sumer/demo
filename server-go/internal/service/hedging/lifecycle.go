package hedging

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/ws"
	"binarytrading/internal/service/hedging/providers"
	"binarytrading/internal/service/metrics"
)

// Hedge order lifecycle (hedge-lifecycle.md sections 2-5; audit F-08).
//
//	PENDING --sent, venue NEW--------------> ACCEPTED --fill--> PARTIALLY_FILLED --fill--> FILLED
//	   |  \--sent, filled at once------------------------------------------------------> FILLED
//	   |   \--venue EXPIRED/CANCELED (maybe partly filled) -----------------------------> CANCELLED
//	   |    \--definitive rejection / never sent / not found after recvWindow ----------> FAILED
//	   \--timeout or 5xx after sending: stays PENDING, queried by clientOrderId until known
//
// Rules that make it safe:
//   - The intent (with its deterministic clientOrderId) is written BEFORE the
//     venue call, and sendAttempts/sentAt are written before each send, so a
//     crash at any point leaves a row recovery can resolve.
//   - An order whose send outcome is unknown is never re-sent: it is looked
//     up by clientOrderId. "Not found" is only believed once recvWindow has
//     passed since the send (Binance refuses an older signed request), then
//     the order is FAILED and the next evaluation creates a fresh intent.
//   - One working order per asset (unique workingSlot).
//   - State moves forward only: executedQty never decreases and a final
//     status never changes, so duplicate or out-of-order venue answers are
//     harmless; fills go to the ledger once each (unique tradeId).
//   - Only fills count as protection. Working orders are contingent.

func mapVenueStatus(s string) models.HedgeOrderStatus {
	switch s {
	case providers.VenueNew:
		return models.HedgeOrderStatusAccepted
	case providers.VenuePartiallyFilled:
		return models.HedgeOrderStatusPartiallyFilled
	case providers.VenueFilled:
		return models.HedgeOrderStatusFilled
	case providers.VenueCanceled, providers.VenueExpired, providers.VenueExpiredInMatch:
		return models.HedgeOrderStatusCancelled
	case providers.VenueRejected:
		return models.HedgeOrderStatusFailed
	}
	return models.HedgeOrderStatusPending
}

func finalStatus(s models.HedgeOrderStatus) bool {
	return s == models.HedgeOrderStatusFilled || s == models.HedgeOrderStatusCancelled || s == models.HedgeOrderStatusFailed
}

func emitHedge(event string, o *models.HedgeOrder) {
	ws.Emit([]string{config.ChAdminRisk}, event, models.ToHedgeView(o))
}

// unknownGrace is how long after a send a "not found" answer can still be
// an order in flight: recvWindow + the request timeout + 2 s of clock skew.
func unknownGrace() time.Duration {
	rw, to := int64(5_000), int64(10_000)
	if config.E != nil {
		rw, to = config.E.HedgeRecvWindowMs, config.E.HedgeRequestTimeoutMs
	}
	return time.Duration(rw+to+2_000) * time.Millisecond
}

// staleWorkingAfter: a MARKET order still NEW after this is cancelled.
var staleWorkingAfter = 60 * time.Second

// unknownAlertAfter: an outcome still unknown after this raises an alert.
var unknownAlertAfter = 5 * time.Minute

// applyVenueState moves the order forward to what the venue reported.
func applyVenueState(ctx context.Context, o *models.HedgeOrder, st providers.OrderState) (*models.HedgeOrder, error) {
	for attempt := 0; attempt < 3; attempt++ {
		next := mapVenueStatus(st.Status)
		now := mongox.Now()
		if finalStatus(o.Status) && next != o.Status || st.ExecutedQtyE8 < o.FilledQtyE8 {
			// A stale or duplicate answer: keep the newer state, note the check.
			return UpdateHedgeOrder(ctx, o.ID, bson.M{"reconciledAt": now})
		}
		set := bson.M{
			"status": next, "venueStatus": st.Status, "filledQtyE8": st.ExecutedQtyE8, "cumQuoteE8": st.CumQuoteE8,
			"reconciledAt": now, "updatedAt": now,
		}
		if st.VenueOrderID != "" {
			set["providerOrderId"] = st.VenueOrderID
		}
		if o.AcceptedAt == nil && next != models.HedgeOrderStatusFailed {
			set["acceptedAt"] = now
		}
		if st.ExecutedQtyE8 > 0 {
			avg := st.AvgPrice
			if !(avg > 0) && st.CumQuoteE8 > 0 {
				avg = float64(st.CumQuoteE8) / float64(st.ExecutedQtyE8)
			}
			set["averageFillPrice"] = avg
			set["filledNotional"] = NotionalForQty(st.ExecutedQtyE8, avg, o.Decision.InrPerUsdt)
			if o.FillLatencyMs == nil && o.SentAt != nil {
				set["fillLatencyMs"] = time.Since(*o.SentAt).Milliseconds()
			}
		}
		update := bson.M{"$set": set}
		if finalStatus(next) {
			if o.FinalAt == nil {
				set["finalAt"] = now
			}
			if next != models.HedgeOrderStatusFilled {
				set["closedAt"] = now
			}
			if next == models.HedgeOrderStatusFailed && o.Error == nil {
				set["error"] = "rejected by the venue"
			} else if next != models.HedgeOrderStatusFailed {
				set["error"] = nil
			}
			update["$unset"] = bson.M{"workingSlot": ""}
		}
		var out models.HedgeOrder
		err := orders().FindOneAndUpdate(ctx,
			bson.M{"_id": o.ID, "status": o.Status, "filledQtyE8": bson.M{"$lte": st.ExecutedQtyE8}},
			update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
		if err == nil {
			if out.Status != o.Status || out.FilledQtyE8 != o.FilledQtyE8 {
				emitHedge(config.EvHedgeUpdated, &out)
			}
			return &out, nil
		}
		if !mongox.IsNotFound(err) {
			return nil, err
		}
		// Another writer moved it first: reload and re-apply.
		fresh, ferr := FindHedgeOrderByID(ctx, o.ID.Hex())
		if ferr != nil || fresh == nil {
			return fresh, ferr
		}
		o = fresh
	}
	return o, nil
}

// failOrder ends a working order as FAILED.
func failOrder(ctx context.Context, o *models.HedgeOrder, reason string) (*models.HedgeOrder, error) {
	now := mongox.Now()
	var out models.HedgeOrder
	err := orders().FindOneAndUpdate(ctx, bson.M{"_id": o.ID, "status": bson.M{"$in": ActiveHedgeStatuses}}, bson.M{
		"$set":   bson.M{"status": models.HedgeOrderStatusFailed, "error": reason, "finalAt": now, "closedAt": now, "updatedAt": now},
		"$unset": bson.M{"workingSlot": ""},
	}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
	if mongox.IsNotFound(err) {
		return FindHedgeOrderByID(ctx, o.ID.Hex())
	}
	if err != nil {
		return nil, err
	}
	emitHedge(config.EvHedgeUpdated, &out)
	logx.Warn("hedge order failed", "reference", out.Reference, "reason", reason)
	return &out, nil
}

// syncFills pulls the order's fills into the ledger, refolds the position
// and refreshes the order's fill totals.
func syncFills(ctx context.Context, v providers.Venue, o *models.HedgeOrder) (*models.HedgeOrder, error) {
	if o.ProviderOrderID == nil || *o.ProviderOrderID == "" || o.FilledQtyE8 <= 0 {
		return o, nil
	}
	list, err := v.Fills(ctx, o.VenueSymbol, *o.ProviderOrderID)
	if err != nil {
		return o, err
	}
	if _, err := recordFills(ctx, o, list); err != nil {
		return o, err
	}
	if _, err := RefoldPosition(ctx, o.AssetID, o.Symbol, o.Venue, o.VenueSymbol); err != nil {
		return o, err
	}
	t, err := sumOrderFills(ctx, o.ID)
	if err != nil {
		return o, err
	}
	patch := bson.M{"fillsQtyE8": t.QtyE8, "fillCount": t.Count, "feeE8": t.FeeE8, "feeAsset": "USDT", "feeInr": t.FeeInr}
	if t.QtyE8 > 0 && t.QuoteE8 > 0 && o.Decision.VenuePrice > 0 {
		avg := float64(t.QuoteE8) / float64(t.QtyE8)
		slip := (avg - o.Decision.VenuePrice) / o.Decision.VenuePrice * 10_000
		if o.Side == models.DirectionDown {
			slip = -slip
		}
		realised := slip + float64(t.FeeE8)/float64(t.QuoteE8)*10_000
		patch["realisedCostBps"] = float64(int64(realised*10_000)) / 10_000
	}
	out, err := UpdateHedgeOrder(ctx, o.ID, patch)
	if err != nil || out == nil {
		return o, err
	}
	if finalStatus(out.Status) && out.FillsQtyE8 >= out.FilledQtyE8 {
		clearAlert(ctx, models.HedgeAlertFillsIncomplete, out.Reference)
	}
	return out, nil
}

// send transmits a PENDING intent. sendAttempts and sentAt are persisted
// first: a crash after this point is resolved by querying, never by
// re-sending.
func send(ctx context.Context, v providers.Venue, o *models.HedgeOrder) (*models.HedgeOrder, error) {
	now := mongox.Now()
	var marked models.HedgeOrder
	err := orders().FindOneAndUpdate(ctx,
		bson.M{"_id": o.ID, "status": models.HedgeOrderStatusPending, "sendAttempts": 0},
		bson.M{"$set": bson.M{"sentAt": now, "updatedAt": now}, "$inc": bson.M{"sendAttempts": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&marked)
	if err != nil {
		if mongox.IsNotFound(err) {
			return FindHedgeOrderByID(ctx, o.ID.Hex())
		}
		return nil, err
	}
	o = &marked
	side := providers.SideBuy
	if o.Side == models.DirectionDown {
		side = providers.SideSell
	}
	started := time.Now()
	st, err := v.PlaceOrder(ctx, providers.OrderRequest{
		Symbol: o.VenueSymbol, Side: side, QtyE8: o.TargetQtyE8, ClientOrderID: o.ClientOrderID,
		ReduceOnly: o.ReduceOnly,
	})
	latency := time.Since(started).Milliseconds()
	metrics.RecordLatency(config.LatHedgeRequest, float64(latency))
	if _, uerr := UpdateHedgeOrder(ctx, o.ID, bson.M{"requestLatencyMs": latency}); uerr != nil {
		logx.Warn("could not store hedge latency", "err", uerr.Error())
	}
	switch {
	case err == nil:
		out, aerr := applyVenueState(ctx, o, st)
		if aerr != nil {
			return nil, aerr
		}
		if st.ExecutedQtyE8 > 0 {
			metrics.RecordLatency(config.LatHedgeFill, float64(latency))
			if synced, serr := syncFills(ctx, v, out); serr == nil {
				out = synced
			} else {
				logx.Warn("hedge fills not read yet", "reference", out.Reference, "err", serr.Error())
			}
		}
		return out, nil
	case providers.IsUnknownOutcome(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled):
		logx.Warn("hedge order outcome unknown - it will be looked up, not re-sent",
			"reference", o.Reference, "clientOrderId", o.ClientOrderID, "err", err.Error())
		return UpdateHedgeOrder(ctx, o.ID, bson.M{"error": "outcome unknown: " + err.Error()})
	default:
		// A definitive refusal (or one this process raised before sending).
		return failOrder(ctx, o, errMessage(err))
	}
}

func errMessage(err error) string {
	if ae, ok := apperr.As(err); ok {
		return ae.Message
	}
	return err.Error()
}

// RefreshOrder brings one order up to date with the venue.
func RefreshOrder(ctx context.Context, v providers.Venue, o *models.HedgeOrder) (*models.HedgeOrder, error) {
	if !o.Working() {
		if o.FilledQtyE8 > o.FillsQtyE8 {
			return syncFills(ctx, v, o)
		}
		return o, nil
	}
	if o.SendAttempts == 0 {
		// The intent was written but the process stopped before sending
		// (sendAttempts is persisted before every send). Its decision is
		// stale: fail it and let a fresh evaluation decide again.
		if time.Since(o.CreatedAt) > 30*time.Second {
			return failOrder(ctx, o, "never sent (the process stopped before sending)")
		}
		return o, nil
	}
	st, err := v.QueryOrder(ctx, o.VenueSymbol, o.ClientOrderID)
	switch {
	case err == nil:
		out, aerr := applyVenueState(ctx, o, st)
		if aerr != nil {
			return nil, aerr
		}
		clearAlert(ctx, models.HedgeAlertOrderUnknown, out.Reference)
		if out.FilledQtyE8 > out.FillsQtyE8 {
			if synced, serr := syncFills(ctx, v, out); serr == nil {
				out = synced
			} else {
				return out, serr
			}
		}
		if out.Status == models.HedgeOrderStatusAccepted && out.SentAt != nil && time.Since(*out.SentAt) > staleWorkingAfter {
			logx.Warn("hedge market order still resting - cancelling", "reference", out.Reference)
			return cancelAtVenue(ctx, v, out)
		}
		return out, nil
	case errors.Is(err, providers.ErrOrderNotFound):
		if o.SentAt != nil && time.Since(*o.SentAt) > unknownGrace() {
			clearAlert(ctx, models.HedgeAlertOrderUnknown, o.Reference)
			return failOrder(ctx, o, "not placed: the venue has no such order after the recvWindow")
		}
		return o, nil
	default:
		if o.SentAt != nil && time.Since(*o.SentAt) > unknownAlertAfter {
			raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertOrderUnknown, Subject: o.Reference, AssetID: &o.AssetID,
				Severity: models.AlertCritical, Message: "Hedge order state unknown: the venue cannot be queried",
				Details: map[string]any{"clientOrderId": o.ClientOrderID, "error": err.Error()}})
		}
		return o, err
	}
}

// cancelAtVenue cancels a working order and applies whatever the venue says
// it really is - a cancel that loses the race to a fill leaves the order
// FILLED, never CANCELLED.
func cancelAtVenue(ctx context.Context, v providers.Venue, o *models.HedgeOrder) (*models.HedgeOrder, error) {
	st, err := v.CancelOrder(ctx, o.VenueSymbol, o.ClientOrderID)
	if errors.Is(err, providers.ErrOrderNotFound) {
		// Final already (filled, expired) or never placed: ask.
		st, err = v.QueryOrder(ctx, o.VenueSymbol, o.ClientOrderID)
		if errors.Is(err, providers.ErrOrderNotFound) {
			if o.SentAt != nil && time.Since(*o.SentAt) > unknownGrace() {
				return failOrder(ctx, o, "not placed: the venue has no such order after the recvWindow")
			}
			return o, apperr.ConflictErr(apperr.Conflict, "The venue does not show this order yet; try again shortly")
		}
	}
	if err != nil {
		return o, apperr.New(apperr.HedgeProviderError, "Cancel outcome unknown ("+err.Error()+"); reconciliation will settle the order", 502)
	}
	out, err := applyVenueState(ctx, o, st)
	if err != nil {
		return nil, err
	}
	if out.FilledQtyE8 > out.FillsQtyE8 {
		if synced, serr := syncFills(ctx, v, out); serr == nil {
			out = synced
		}
	}
	return out, nil
}

// CancelHedgeOrder is the admin cancel of a working order.
func CancelHedgeOrder(ctx context.Context, orderID string) (models.HedgeOrderView, error) {
	target, err := FindHedgeOrderByID(ctx, orderID)
	if err != nil {
		return models.HedgeOrderView{}, err
	}
	if target == nil {
		return models.HedgeOrderView{}, apperr.NotFoundErr(apperr.NotFound, "Hedge order not found")
	}
	if !target.Working() {
		return models.HedgeOrderView{}, apperr.ConflictErr(apperr.Conflict, "Order is already "+string(target.Status))
	}
	var out *models.HedgeOrder
	if target.SendAttempts == 0 {
		now := mongox.Now()
		var o models.HedgeOrder
		err = orders().FindOneAndUpdate(ctx, bson.M{"_id": target.ID, "sendAttempts": 0, "status": models.HedgeOrderStatusPending},
			bson.M{"$set": bson.M{"status": models.HedgeOrderStatusCancelled, "finalAt": now, "closedAt": now, "updatedAt": now,
				"error": "cancelled before it was sent"}, "$unset": bson.M{"workingSlot": ""}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&o)
		if err != nil {
			if mongox.IsNotFound(err) {
				return models.HedgeOrderView{}, apperr.ConflictErr(apperr.Conflict, "The order is being sent; try again shortly")
			}
			return models.HedgeOrderView{}, err
		}
		out = &o
	} else {
		out, err = cancelAtVenue(ctx, venue(), target)
		if err != nil {
			return models.HedgeOrderView{}, err
		}
	}
	emitHedge(config.EvHedgeUpdated, out)
	return models.ToHedgeView(out), nil
}

// RefreshWorkingOrders is the recovery pass: every working order is looked
// up at the venue, and every final order whose fills are missing is synced.
// It runs when this instance becomes the hedge leader (boot recovery) and on
// every tick.
func RefreshWorkingOrders(ctx context.Context) (int, error) {
	v := venue()
	working, err := FindWorkingOrders(ctx, 200)
	if err != nil {
		return 0, err
	}
	missing, err := findOrdersMissingFills(ctx, 100)
	if err != nil {
		return 0, err
	}
	n := 0
	all := append(working, missing...)
	for i := range all {
		o := all[i]
		out, err := RefreshOrder(ctx, v, &o)
		if out != nil {
			o = *out
		}
		if !o.Working() && o.FillsQtyE8 < o.FilledQtyE8 && o.FinalAt != nil && time.Since(*o.FinalAt) > unknownAlertAfter {
			raiseAlert(ctx, hedgeAlert{Code: models.HedgeAlertFillsIncomplete, Subject: o.Reference, AssetID: &o.AssetID,
				Message: "The venue reported fills this ledger does not hold yet",
				Details: map[string]any{"filledQty": providers.FormatE8(o.FilledQtyE8), "ledgerQty": providers.FormatE8(o.FillsQtyE8)}})
		}
		if err != nil {
			logx.Warn("could not refresh hedge order", "reference", o.Reference, "err", err.Error())
			continue
		}
		n++
	}
	return n, nil
}

func newClientOrderID(id bson.ObjectID) string { return "bth-" + id.Hex() }
