// Package httpx is the HTTP surface shared by every route: the response
// envelope, error mapping, middleware and request parsing.
package httpx

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"runtime/debug"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

// HandlerFunc is a route handler that returns an error instead of writing one.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// H adapts a HandlerFunc to net/http, sending any error through WriteError.
func H(h HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	}
}

// Envelope is the single success shape (spec section 33).
type envelope struct {
	Success bool           `json:"success"`
	Data    any            `json:"data"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type errorEnvelope struct {
	Success bool      `json:"success"`
	Error   errorBody `json:"error"`
}

// WriteJSON writes any value with a status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// OK sends {success:true, data} with 200.
func OK(w http.ResponseWriter, data any) error {
	WriteJSON(w, http.StatusOK, envelope{Success: true, Data: data})
	return nil
}

// Created sends {success:true, data} with 201.
func Created(w http.ResponseWriter, data any) error {
	WriteJSON(w, http.StatusCreated, envelope{Success: true, Data: data})
	return nil
}

// Send sends {success:true, data, meta?} with any status.
func Send(w http.ResponseWriter, status int, data any, meta map[string]any) error {
	WriteJSON(w, status, envelope{Success: true, Data: data, Meta: meta})
	return nil
}

// Fail writes an error envelope directly.
func Fail(w http.ResponseWriter, status int, code, message string, details any) {
	WriteJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Details: details}})
}

// WriteError is the single exit point for every failure. Clients get a
// stable code and a safe message; driver errors stay in the logs.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	log := logx.From(r.Context())

	if e, ok := apperr.As(err); ok {
		if e.StatusCode >= 500 {
			log.Error("application error", "code", e.Code, "err", e.Message)
		} else {
			log.Debug("request rejected", "code", e.Code, "err", e.Message)
		}
		Fail(w, e.StatusCode, e.Code, e.Message, e.Details)
		return
	}

	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		Fail(w, http.StatusRequestEntityTooLarge, apperr.PayloadTooLarge, "Request body is too large", nil)
		return
	}

	if mongox.IsDuplicateKey(err) {
		Fail(w, http.StatusConflict, apperr.Conflict, "Resource already exists", nil)
		return
	}

	log.Error("unhandled error", "err", err.Error())
	msg := "Internal server error"
	if !config.IsProduction() && err.Error() != "" {
		msg = err.Error()
	}
	Fail(w, http.StatusInternalServerError, apperr.InternalError, msg, nil)
}

// Recoverer turns a panic into a 500 instead of dropping the connection.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				logx.From(r.Context()).Error("handler panicked", "panic", rec, "stack", string(debug.Stack()))
				Fail(w, http.StatusInternalServerError, apperr.InternalError, "Internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// NotFound is the handler for unknown routes.
func NotFound(w http.ResponseWriter, r *http.Request) {
	Fail(w, http.StatusNotFound, apperr.NotFound, "Route "+r.Method+" "+r.URL.RequestURI()+" does not exist", nil)
}

// Paginated is the list shape every paginated endpoint returns.
type Paginated[T any] struct {
	Items      []T   `json:"items"`
	Page       int64 `json:"page"`
	Limit      int64 `json:"limit"`
	Total      int64 `json:"total"`
	TotalPages int64 `json:"totalPages"`
}

// Paginate builds the list envelope; a nil slice becomes [].
func Paginate[T any](items []T, page, limit, total int64) Paginated[T] {
	if items == nil {
		items = []T{}
	}
	pages := int64(1)
	if limit > 0 {
		pages = int64(math.Max(1, math.Ceil(float64(total)/float64(limit))))
	}
	return Paginated[T]{Items: items, Page: page, Limit: limit, Total: total, TotalPages: pages}
}
