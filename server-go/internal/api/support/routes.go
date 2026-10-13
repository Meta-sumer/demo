// Package support exposes the live support chat: Mount is the customer side
// (mounted at /support), MountAdmin the desk (mounted at /admin/support).
package support

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf16"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/httpx"
	"binarytrading/internal/service/audit"
	svc "binarytrading/internal/service/support"
)

// ── shared parsing ──────────────────────────────────────────────────────

func jsLen(s string) int { return len(utf16.Encode([]rune(s))) }

func strCheck(v *httpx.V, path, s string, min, max int) {
	n := jsLen(s)
	if n < min {
		v.Add(path, fmt.Sprintf("String must contain at least %d character(s)", min))
	}
	if max > 0 && n > max {
		v.Add(path, fmt.Sprintf("String must contain at most %d character(s)", max))
	}
}

// parseMessage is { body: z.string().trim().min(1).max(2000) }.
func parseMessage(r *http.Request) (string, error) {
	var in struct {
		Body *string `json:"body"`
	}
	if err := httpx.DecodeBody(r, &in); err != nil {
		return "", err
	}
	v := httpx.NewV()
	var body string
	if v.Required("body", in.Body != nil) {
		body = strings.TrimFunc(*in.Body, func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
		strCheck(v, "body", body, 1, 2000)
	}
	if err := v.Err("request body"); err != nil {
		return "", err
	}
	return body, nil
}

// parseHistory is { before?: string max 64, limit?: coerce int 1..100 }.
func parseHistory(r *http.Request) (svc.ListMessagesOptions, error) {
	q := httpx.Query(r)
	before := q.OptStr("before")
	if before != nil {
		strCheck(q.V, "before", *before, 0, 64)
	}
	limit := q.OptInt("limit", 1, 100)
	if err := q.Err(); err != nil {
		return svc.ListMessagesOptions{}, err
	}
	return svc.ListMessagesOptions{Before: before, Limit: limit}, nil
}

// thread is `{ conversation, ...messages }`.
type thread struct {
	Conversation models.ConversationView `json:"conversation"`
	Items        []models.MessageView    `json:"items"`
	HasMore      bool                    `json:"hasMore"`
}

// ── customer ────────────────────────────────────────────────────────────

// Mount: the one conversation that belongs to the signed-in user.
func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	read := r.With(httpx.RateLimit(httpx.RateLimits.Read))
	chat := r.With(httpx.RateLimit(httpx.RateLimits.Chat))

	read.Get("/conversation", httpx.H(customerThread))
	chat.Post("/messages", httpx.H(customerMessage))
	read.Post("/read", httpx.H(customerRead))
	chat.Post("/typing", httpx.H(customerTyping))
}

func customerThread(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	opts, err := parseHistory(r)
	if err != nil {
		return err
	}
	conversation, err := svc.GetConversationView(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	page, err := svc.ListMessages(r.Context(), conversation.ID, opts)
	if err != nil {
		return err
	}
	return httpx.OK(w, thread{Conversation: conversation, Items: page.Items, HasMore: page.HasMore})
}

func customerMessage(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	body, err := parseMessage(r)
	if err != nil {
		return err
	}
	res, err := svc.PostCustomerMessage(r.Context(), actor.UserID, body)
	if err != nil {
		return err
	}
	return httpx.Created(w, res)
}

func customerRead(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	view, err := svc.MarkReadByUser(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, view)
}

func customerTyping(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	if err := svc.SignalTypingByUser(r.Context(), actor.UserID); err != nil {
		return err
	}
	return httpx.OK(w, map[string]bool{"sent": true})
}

// ── desk ────────────────────────────────────────────────────────────────

// MountAdmin: every conversation, for staff holding the support permissions.
func MountAdmin(r chi.Router) {
	r.Use(httpx.Authenticate, httpx.RequireAdmin, httpx.RateLimit(httpx.RateLimits.Admin))

	read := r.With(httpx.RequirePermission(config.PermSupportRead))
	write := r.With(httpx.RequirePermission(config.PermSupportWrite))
	writeChat := r.With(httpx.RequirePermission(config.PermSupportWrite), httpx.RateLimit(httpx.RateLimits.Chat))

	read.Get("/conversations", httpx.H(deskList))
	read.Get("/conversations/{id}", httpx.H(deskThread))
	// Opens (or creates) a customer's thread from their profile.
	write.Post("/users/{id}/conversation", httpx.H(deskOpenForUser))
	writeChat.Post("/conversations/{id}/messages", httpx.H(deskMessage))
	read.Post("/conversations/{id}/read", httpx.H(deskRead))
	writeChat.Post("/conversations/{id}/typing", httpx.H(deskTyping))
	r.With(httpx.RequirePermission(config.PermSupportWrite), httpx.RateLimit(httpx.RateLimits.AdminWrite)).
		Patch("/conversations/{id}", httpx.H(deskUpdate))
}

func deskList(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	status := q.OptEnum("status", models.SupportStatusValues...)
	search := q.OptStr("search")
	if search != nil {
		strCheck(q.V, "search", *search, 0, 120)
	}
	mine := q.OptEnum("mine", "true", "false")
	if err := q.Err(); err != nil {
		return err
	}
	query := svc.ConversationQuery{
		Search:  search,
		Mine:    mine != nil && *mine == "true",
		AgentID: actor.UserID,
		Page:    page,
		Limit:   limit,
	}
	if status != nil {
		s := models.SupportStatus(*status)
		query.Status = &s
	}
	res, err := svc.ListConversations(r.Context(), query)
	if err != nil {
		return err
	}
	return httpx.Send(w, http.StatusOK, httpx.Paginate(res.Items, page, limit, res.Total),
		map[string]any{"unreadTotal": res.UnreadTotal})
}

func deskThread(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	opts, err := parseHistory(r)
	if err != nil {
		return err
	}
	conversation, err := svc.GetConversationForDesk(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	page, err := svc.ListMessages(r.Context(), id.Hex(), opts)
	if err != nil {
		return err
	}
	return httpx.OK(w, thread{Conversation: conversation, Items: page.Items, HasMore: page.HasMore})
}

func deskOpenForUser(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	conversation, err := svc.GetOrCreateConversation(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	view, err := svc.GetConversationForDesk(r.Context(), conversation.ID.Hex())
	if err != nil {
		return err
	}
	return httpx.OK(w, view)
}

func deskMessage(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	body, err := parseMessage(r)
	if err != nil {
		return err
	}
	res, err := svc.PostAgentMessage(r.Context(), id.Hex(), actor.UserID, body)
	if err != nil {
		return err
	}
	return httpx.Created(w, res)
}

func deskRead(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	view, err := svc.MarkReadByAgent(r.Context(), id.Hex())
	if err != nil {
		return err
	}
	return httpx.OK(w, view)
}

func deskTyping(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	if err := svc.SignalTypingByAgent(r.Context(), id.Hex(), actor.UserID); err != nil {
		return err
	}
	return httpx.OK(w, map[string]bool{"sent": true})
}

func deskUpdate(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	var in struct {
		Status     *string `json:"status"`
		AssignToMe *bool   `json:"assignToMe"`
	}
	if err := httpx.DecodeBody(r, &in); err != nil {
		return err
	}
	v := httpx.NewV()
	if in.Status != nil {
		v.Enum("status", *in.Status, models.SupportStatusValues...)
	}
	// The refine only runs once the object itself parsed.
	if v.OK() && in.Status == nil && in.AssignToMe == nil {
		v.Add("", "Nothing to change")
	}
	if err := v.Err("request body"); err != nil {
		return err
	}
	change := svc.ConversationChange{AssignToMe: in.AssignToMe}
	if in.Status != nil {
		s := models.SupportStatus(*in.Status)
		change.Status = &s
	}

	res, err := svc.UpdateConversation(r.Context(), id.Hex(), actor.UserID, change)
	if err != nil {
		return err
	}

	e := audit.FromRequest(actor, httpx.AuditMeta(r))
	e.Action = audit.SupportConversationUpdated
	e.Resource = "SupportConversation"
	e.ResourceID = id.Hex()
	e.Before = map[string]any{"status": res.Before.Status, "assignedTo": res.Before.AssignedTo}
	e.After = map[string]any{"status": res.After.Status, "assignedTo": res.After.AssignedTo}
	audit.Write(r.Context(), e)

	return httpx.OK(w, res.After)
}
