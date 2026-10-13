package usdt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/service/beex"
)

// Webhooks. The raw body is verified (HMAC over "t.body", 5-minute window)
// before anything parses it; the event is then stored under its identity and
// only then acted on. A repeat delivery finds its identity taken and does
// nothing. If processing fails after the event is stored, BeeX still gets 200
// and the retry loop takes over - processing is idempotent, so a retry can
// never apply an event twice.

const (
	// MaxWebhookBody caps what is read from a webhook request.
	MaxWebhookBody = 64 << 10
	// maxEventAttempts bounds automatic reprocessing; an operator can still
	// reprocess by hand.
	maxEventAttempts = 10
	// processingGrace keeps the retry loop off an event the receiving request
	// is still processing.
	processingGrace = 2 * time.Minute
)

func eventsColl() *mongo.Collection { return mongox.C(models.CollBeexWebhookEvents) }

// WebhookResult is what the webhook endpoint reports.
type WebhookResult struct {
	EventID          string `json:"eventId"`
	Duplicate        bool   `json:"duplicate"`
	ProcessingStatus string `json:"processingStatus"`
}

// EventKey is an event's identity: a redelivery of the same event produces
// the same key, a new state of the same invoice or payout a new one.
func EventKey(p beex.WebhookPayload) string {
	part := func(s string) string {
		s = strings.TrimSpace(s)
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	switch beex.EventKind(p.Event) {
	case "invoice":
		return strings.Join([]string{part(p.Event), part(p.Reference), part(p.Status), part(p.AmountReceived.Raw)}, "|")
	case "payout":
		return strings.Join([]string{part(p.Event), part(p.Reference), part(p.Status), part(p.TxHash)}, "|")
	}
	return ""
}

func bodyHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// storedHeaders keeps the request headers that matter for an investigation.
func storedHeaders(h http.Header, remoteIP string) map[string]string {
	out := map[string]string{"remoteIp": remoteIP}
	for _, k := range []string{beex.SignatureHeader, "Content-Type", "User-Agent", "X-Request-Id", "X-Forwarded-For"} {
		if v := h.Get(k); v != "" {
			if len(v) > 512 {
				v = v[:512]
			}
			out[strings.ToLower(k)] = v
		}
	}
	return out
}

// ReceiveWebhook verifies, stores and processes one delivery. An error
// return means the delivery was not stored (bad signature, not configured);
// anything stored is answered 200.
func ReceiveWebhook(ctx context.Context, raw []byte, header http.Header, remoteIP string) (WebhookResult, error) {
	ctx = context.WithoutCancel(ctx)
	secret := env().BeexWebhookSecret
	ts, err := beex.VerifySignature(header.Get(beex.SignatureHeader), raw, secret, time.Now())
	if err != nil {
		if errors.Is(err, beex.ErrNoSecret) {
			logx.Error("BeeX webhook received but BEEX_WEBHOOK_SECRET is not set")
			return WebhookResult{}, apperr.Unavailable(apperr.ServiceUnavailable, "Webhook endpoint is not configured")
		}
		logx.Warn("BeeX webhook rejected", "reason", err.Error(), "remoteIp", remoteIP, "bytes", len(raw))
		return WebhookResult{}, apperr.Unauth("Invalid webhook signature")
	}

	now := mongox.Now()
	doc := models.BeexWebhookEvent{
		ID: bson.NewObjectID(), SignatureTimestamp: ts, RawBody: string(raw), Headers: storedHeaders(header, remoteIP),
		ReceivedAt: now, ProcessingStatus: models.WebhookPending, Deliveries: 1, LastDeliveredAt: now,
		NextAttemptAt: ptr(now.Add(processingGrace)), CreatedAt: now, UpdatedAt: now,
	}
	var p beex.WebhookPayload
	var generic map[string]any
	if err := json.Unmarshal(raw, &p); err != nil || json.Unmarshal(raw, &generic) != nil {
		// Signed by BeeX but unreadable: keep it for an operator rather than
		// make BeeX retry for a day.
		doc.EventKey = "malformed|" + bodyHash(raw)
		doc.Kind = "unknown"
		doc.ProcessingStatus = models.WebhookFailed
		doc.Error = ptr("body is not a JSON object")
		doc.NextAttemptAt = nil
		doc.Payload = map[string]any{}
	} else {
		doc.Payload = generic
		doc.Event = p.Event
		doc.Kind = beex.EventKind(p.Event)
		doc.EventKey = EventKey(p)
		if doc.EventKey == "" {
			doc.EventKey = "other|" + strings.TrimSpace(p.Event) + "|" + bodyHash(raw)
		}
		doc.Reference = strOrNil(p.Reference)
		doc.OrderID = strOrNil(p.OrderID)
		doc.ClientReference = strOrNil(p.ClientReference)
		doc.Status = strOrNil(p.Status)
		doc.Mode = strOrNil(p.Mode)
	}

	if _, err := eventsColl().InsertOne(ctx, doc); err != nil {
		if !mongox.IsDuplicateKey(err) {
			return WebhookResult{}, err
		}
		var existing models.BeexWebhookEvent
		_ = eventsColl().FindOneAndUpdate(ctx, bson.M{"eventKey": doc.EventKey},
			bson.M{"$inc": bson.M{"deliveries": int64(1)}, "$set": bson.M{"lastDeliveredAt": now}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&existing)
		logx.Info("BEEX_PAYMENT_WEBHOOK_RECEIVED", "event", doc.Event, "reference", deref(doc.Reference),
			"status", deref(doc.Status), "duplicate", true)
		return WebhookResult{EventID: existing.ID.Hex(), Duplicate: true, ProcessingStatus: existing.ProcessingStatus}, nil
	}
	logx.Info("BEEX_PAYMENT_WEBHOOK_RECEIVED", "eventId", doc.ID.Hex(), "event", doc.Event, "reference", deref(doc.Reference),
		"orderId", deref(doc.OrderID), "clientReference", deref(doc.ClientReference), "status", deref(doc.Status),
		"mode", deref(doc.Mode))
	if doc.ProcessingStatus == models.WebhookFailed {
		return WebhookResult{EventID: doc.ID.Hex(), ProcessingStatus: doc.ProcessingStatus}, nil
	}
	status, err := ProcessEvent(ctx, doc.ID, false)
	if err != nil {
		logx.Error("BeeX webhook processing failed; it will be retried", "eventId", doc.ID.Hex(), "err", err.Error())
	}
	return WebhookResult{EventID: doc.ID.Hex(), ProcessingStatus: status}, nil
}

func eventBackoff(attempt int64) time.Duration {
	d := time.Minute << min(attempt, 8) // 2m, 4m, ... capped at ~4h
	if d > 4*time.Hour {
		d = 4 * time.Hour
	}
	return d
}

// ProcessEvent applies a stored event. force reprocesses an event that was
// already settled (operator action); it is safe because applying is
// idempotent.
func ProcessEvent(ctx context.Context, id bson.ObjectID, force bool) (string, error) {
	ctx = context.WithoutCancel(ctx)
	var ev models.BeexWebhookEvent
	if err := eventsColl().FindOne(ctx, bson.M{"_id": id}).Decode(&ev); err != nil {
		if mongox.IsNotFound(err) {
			return "", apperr.NotFoundErr(apperr.NotFound, "Webhook event not found")
		}
		return "", err
	}
	if !force && ev.ProcessingStatus != models.WebhookPending && ev.ProcessingStatus != models.WebhookFailed {
		return ev.ProcessingStatus, nil
	}
	now := mongox.Now()
	if !Enabled() {
		// Kept, not dropped: processed once USDT is enabled again.
		_, err := eventsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{
			"processingStatus": models.WebhookPending, "error": "USDT is disabled; stored for later processing",
			"nextAttemptAt": now.Add(10 * time.Minute), "updatedAt": now,
		}})
		return models.WebhookPending, err
	}

	status, note, depositID, withdrawalID, perr := dispatchEvent(ctx, &ev)
	attempts := ev.Attempts + 1
	set := bson.M{"attempts": attempts, "updatedAt": mongox.Now()}
	if depositID != nil {
		set["depositId"] = *depositID
	}
	if withdrawalID != nil {
		set["withdrawalId"] = *withdrawalID
	}
	if perr != nil {
		status = models.WebhookFailed
		set["processingStatus"] = status
		set["error"] = perr.Error()
		if attempts >= maxEventAttempts {
			set["nextAttemptAt"] = nil
			logx.Error("BeeX webhook event gave up after repeated failures", "eventId", id.Hex(), "attempts", attempts, "err", perr.Error())
		} else {
			set["nextAttemptAt"] = now.Add(eventBackoff(attempts))
		}
	} else {
		set["processingStatus"] = status
		set["error"] = strOrNil(note)
		set["processedAt"] = mongox.Now()
		set["nextAttemptAt"] = nil
	}
	if _, err := eventsColl().UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
		return status, err
	}
	return status, perr
}

// dispatchEvent applies one event and classifies the outcome.
func dispatchEvent(ctx context.Context, ev *models.BeexWebhookEvent) (status, note string, depositID, withdrawalID *bson.ObjectID, err error) {
	var p beex.WebhookPayload
	if jerr := json.Unmarshal([]byte(ev.RawBody), &p); jerr != nil {
		return models.WebhookIgnored, "body is not a JSON object", nil, nil, nil
	}
	var res ApplyResult
	switch beex.EventKind(p.Event) {
	case "invoice":
		d, ferr := depositForEvent(ctx, p)
		if ferr != nil {
			return "", "", nil, nil, ferr
		}
		if d == nil {
			return models.WebhookIgnored, "no deposit with this orderId/reference", nil, nil, nil
		}
		depositID = &d.ID
		res, err = processInvoiceEvent(ctx, d, p)
	case "payout":
		w, ferr := withdrawalForEvent(ctx, p)
		if ferr != nil {
			return "", "", nil, nil, ferr
		}
		if w == nil {
			// Payout events carry no mode: only a payout we created is accepted.
			return models.WebhookIgnored, "no withdrawal with this clientReference", nil, nil, nil
		}
		withdrawalID = &w.ID
		var u payoutUpdate
		if u, err = updateFromPayoutWebhook(p); err == nil {
			res, err = applyPayoutUpdate(ctx, w.ID, u)
		}
	default:
		return models.WebhookIgnored, "unknown event " + p.Event, nil, nil, nil
	}
	if ig, ok := AsIgnored(err); ok {
		return models.WebhookIgnored, ig.Reason, depositID, withdrawalID, nil
	}
	if err != nil {
		return "", "", depositID, withdrawalID, err
	}
	if res.Changed || res.Credited {
		return models.WebhookProcessed, res.Note, depositID, withdrawalID, nil
	}
	return models.WebhookDuplicate, res.Note, depositID, withdrawalID, nil
}

func processInvoiceEvent(ctx context.Context, d *models.UsdtDeposit, p beex.WebhookPayload) (ApplyResult, error) {
	u, err := updateFromWebhook(p)
	if err != nil {
		return ApplyResult{}, err
	}
	res, err := applyInvoiceUpdate(ctx, d.ID, u)
	if errors.Is(err, errNeedAmounts) {
		// The event did not carry the figures a credit needs: ask BeeX for the
		// whole invoice and apply that instead.
		res, _, err = RefreshDeposit(ctx, d.ID, "refresh")
	}
	return res, err
}

func depositForEvent(ctx context.Context, p beex.WebhookPayload) (*models.UsdtDeposit, error) {
	var d models.UsdtDeposit
	var filter bson.M
	switch {
	case p.OrderID != "":
		filter = bson.M{"orderId": p.OrderID}
	case p.Reference != "":
		filter = bson.M{"reference": p.Reference}
	default:
		return nil, nil
	}
	if err := depositsColl().FindOne(ctx, filter).Decode(&d); err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &d, nil
}

func withdrawalForEvent(ctx context.Context, p beex.WebhookPayload) (*models.UsdtWithdrawal, error) {
	var w models.UsdtWithdrawal
	var filter bson.M
	switch {
	case p.ClientReference != "":
		filter = bson.M{"reference": p.ClientReference}
	case p.Reference != "":
		// Only a BeeX reference we already stored from BeeX's own answer.
		filter = bson.M{"providerReference": p.Reference}
	default:
		return nil, nil
	}
	if err := withdrawalsColl().FindOne(ctx, filter).Decode(&w); err != nil {
		if mongox.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &w, nil
}

// RetryWebhookEvents reprocesses pending and failed events that are due.
func RetryWebhookEvents(ctx context.Context, limit int64) (int, error) {
	if !Enabled() {
		return 0, nil
	}
	cur, err := eventsColl().Find(ctx, bson.M{
		"processingStatus": bson.M{"$in": bson.A{models.WebhookPending, models.WebhookFailed}},
		"attempts":         bson.M{"$lt": maxEventAttempts},
		"nextAttemptAt":    bson.M{"$lte": mongox.Now()},
	}, options.Find().SetSort(bson.D{{Key: "nextAttemptAt", Value: 1}}).SetLimit(limit).SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return 0, err
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return 0, err
	}
	for _, r := range rows {
		if _, err := ProcessEvent(ctx, r.ID, false); err != nil {
			logx.Warn("BeeX webhook retry failed", "eventId", r.ID.Hex(), "err", err.Error())
		}
	}
	return len(rows), nil
}

// ReprocessEvent is the operator's "process again" (idempotent).
func ReprocessEvent(ctx context.Context, id bson.ObjectID) (*models.BeexWebhookEvent, error) {
	if !Enabled() {
		return nil, apperr.ConflictErr(apperr.UsdtDisabled, "USDT payments are disabled; enable them before reprocessing")
	}
	if _, err := ProcessEvent(ctx, id, true); err != nil {
		if _, ok := apperr.As(err); ok {
			return nil, err
		}
		logx.Warn("operator reprocess failed", "eventId", id.Hex(), "err", err.Error())
	}
	var ev models.BeexWebhookEvent
	if err := eventsColl().FindOne(ctx, bson.M{"_id": id}).Decode(&ev); err != nil {
		return nil, err
	}
	return &ev, nil
}

// EventQuery filters webhook events.
type EventQuery struct {
	Event            *string
	Reference        *string
	ProcessingStatus *string
	DepositID        *bson.ObjectID
	WithdrawalID     *bson.ObjectID
	Page, Limit      int64
}

// ListEvents returns events newest first.
func ListEvents(ctx context.Context, q EventQuery) ([]models.BeexWebhookEvent, int64, error) {
	conds := bson.A{}
	if q.Event != nil && *q.Event != "" {
		conds = append(conds, bson.M{"event": *q.Event})
	}
	if q.Reference != nil && *q.Reference != "" {
		conds = append(conds, bson.M{"$or": bson.A{
			bson.M{"reference": *q.Reference}, bson.M{"orderId": *q.Reference}, bson.M{"clientReference": *q.Reference},
		}})
	}
	if q.ProcessingStatus != nil {
		conds = append(conds, bson.M{"processingStatus": *q.ProcessingStatus})
	}
	if q.DepositID != nil {
		conds = append(conds, bson.M{"depositId": *q.DepositID})
	}
	if q.WithdrawalID != nil {
		conds = append(conds, bson.M{"withdrawalId": *q.WithdrawalID})
	}
	filter := andFilter(conds)
	page, limit := max(q.Page, 1), max(q.Limit, 1)
	cur, err := eventsColl().Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "receivedAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip((page-1)*limit).SetLimit(limit))
	if err != nil {
		return nil, 0, err
	}
	var rows []models.BeexWebhookEvent
	if err := cur.All(ctx, &rows); err != nil {
		return nil, 0, err
	}
	total, err := eventsColl().CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// eventsFor returns the events linked to a deposit or withdrawal (by id or
// by the references BeeX uses), newest first.
func eventsFor(ctx context.Context, or bson.A) ([]models.WebhookEventView, error) {
	cur, err := eventsColl().Find(ctx, bson.M{"$or": or},
		options.Find().SetSort(bson.D{{Key: "receivedAt", Value: -1}}).SetLimit(100))
	if err != nil {
		return nil, err
	}
	var rows []models.BeexWebhookEvent
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make([]models.WebhookEventView, 0, len(rows))
	for i := range rows {
		out = append(out, models.ToWebhookEventView(&rows[i]))
	}
	return out, nil
}
