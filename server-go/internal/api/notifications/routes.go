// Package notifications is the in-app notification inbox, mounted at
// /notifications.
package notifications

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"binarytrading/internal/platform/httpx"
	svc "binarytrading/internal/service/notifications"
)

func Mount(r chi.Router) {
	r.Use(httpx.Authenticate)
	read := r.With(httpx.RateLimit(httpx.RateLimits.Read))
	read.Get("/", httpx.H(list))
	read.Post("/{id}/read", httpx.H(markRead))
	read.Post("/read-all", httpx.H(markAllRead))
}

func list(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	q := httpx.Query(r)
	page, limit := q.Page()
	if err := q.Err(); err != nil {
		return err
	}
	res, err := svc.ListNotifications(r.Context(), actor.UserID, page, limit)
	if err != nil {
		return err
	}
	return httpx.Send(w, http.StatusOK, httpx.Paginate(res.Items, page, limit, res.Total),
		map[string]any{"unread": res.Unread})
}

func markRead(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	id, err := httpx.IDParam(r, "id")
	if err != nil {
		return err
	}
	if err := svc.MarkNotificationRead(r.Context(), actor.UserID, id.Hex()); err != nil {
		return err
	}
	return httpx.OK(w, map[string]bool{"read": true})
}

func markAllRead(w http.ResponseWriter, r *http.Request) error {
	actor, err := httpx.RequireActor(r)
	if err != nil {
		return err
	}
	updated, err := svc.MarkAllNotificationsRead(r.Context(), actor.UserID)
	if err != nil {
		return err
	}
	return httpx.OK(w, map[string]int64{"updated": updated})
}
