package httpx

import (
	"context"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/platform/apperr"
	"binarytrading/internal/platform/authtoken"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
)

type ctxKey int

const (
	actorKey ctxKey = iota
	requestIDKey
)

// Actor is the authenticated caller.
type Actor struct {
	UserID      string
	Role        string
	Permissions []string
	SessionID   string
}

// ObjectID returns the actor's user id as an ObjectID.
func (a *Actor) ObjectID() bson.ObjectID {
	id, _ := bson.ObjectIDFromHex(a.UserID)
	return id
}

func ActorFrom(r *http.Request) *Actor {
	a, _ := r.Context().Value(actorKey).(*Actor)
	return a
}

// RequireActor returns the actor or UNAUTHENTICATED.
func RequireActor(r *http.Request) (*Actor, error) {
	if a := ActorFrom(r); a != nil {
		return a, nil
	}
	return nil, apperr.Unauth()
}

func RequestID(r *http.Request) string {
	id, _ := r.Context().Value(requestIDKey).(string)
	return id
}

// ── request context ─────────────────────────────────────────────────────

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// Unwrap lets http.ResponseController and the websocket upgrader reach the
// underlying writer (Hijacker, Flusher).
func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// RequestContext attaches a request id (echoed in x-request-id) and a
// request-scoped logger, and logs completion at debug level.
func RequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if id == "" || len(id) > 128 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		started := time.Now()
		log := logx.With("requestId", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = logx.IntoContext(ctx, log)
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		r = r.WithContext(ctx)
		next.ServeHTTP(rec, r)
		userID := ""
		if a := ActorFrom(r); a != nil {
			userID = a.UserID
		}
		log.Debug("request completed", "method", r.Method, "url", r.URL.RequestURI(), "status", rec.status,
			"durationMs", float64(time.Since(started).Microseconds())/1000, "userId", userID)
	})
}

// ── security headers (helmet defaults) ──────────────────────────────────

const productionCSP = "default-src 'self';base-uri 'self';font-src 'self' https: data:;form-action 'self';" +
	"frame-ancestors 'self';img-src 'self' data:;object-src 'none';script-src 'self';script-src-attr 'none';" +
	"style-src 'self' https: 'unsafe-inline';upgrade-insecure-requests"

func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		if config.IsProduction() {
			h.Set("Content-Security-Policy", productionCSP)
		}
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "cross-origin")
		h.Set("Origin-Agent-Cluster", "?1")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-DNS-Prefetch-Control", "off")
		h.Set("X-Download-Options", "noopen")
		h.Set("X-Frame-Options", "SAMEORIGIN")
		h.Set("X-Permitted-Cross-Domain-Policies", "none")
		h.Set("X-XSS-Protection", "0")
		next.ServeHTTP(w, r)
	})
}

// ── CORS ────────────────────────────────────────────────────────────────

// loopbackOrigin: a development server answers to any loopback origin,
// because Vite steps to the next free port when 5173/5174 are taken.
var loopbackOrigin = regexp.MustCompile(`^https?://(localhost|127\.0\.0\.1|\[::1\])(:\d{1,5})?$`)

func originAllowed(origin string) bool {
	if origin == config.E.ClientURL || origin == config.E.AdminURL {
		return true
	}
	return !config.IsProduction() && loopbackOrigin.MatchString(origin)
}

func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		h := w.Header()
		h.Add("Vary", "Origin")
		if origin != "" {
			if !originAllowed(origin) {
				Fail(w, http.StatusForbidden, apperr.Forbidden, "Origin not allowed", nil)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Set("Access-Control-Expose-Headers", "x-request-id, x-ratelimit-limit, x-ratelimit-remaining, x-ratelimit-reset")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			h.Set("Access-Control-Allow-Headers", "content-type, authorization, idempotency-key, x-request-id")
			h.Set("Content-Length", "0")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// BodyLimit caps JSON bodies. Multipart uploads carry their own, larger cap
// applied by the upload handler.
func BodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if r.Body != nil && mt != "multipart/form-data" {
			if r.ContentLength > config.E.MaxRequestBodyBytes {
				Fail(w, http.StatusRequestEntityTooLarge, apperr.PayloadTooLarge, "Request body is too large", nil)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, config.E.MaxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// ── authentication & RBAC ───────────────────────────────────────────────

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func actorFromClaims(c *authtoken.AccessClaims) *Actor {
	perms := c.Permissions
	if len(perms) == 0 {
		perms = config.PermissionsForRole(c.Role, nil)
	}
	return &Actor{UserID: c.Subject, Role: c.Role, Permissions: perms, SessionID: c.SID}
}

func withActor(r *http.Request, a *Actor) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), actorKey, a))
}

// actorFromSession builds the actor from a token that CheckSession accepted.
// The live state's permissions are used: they are what a fresh token would
// carry, so a grant removed since the token was signed no longer counts.
func actorFromSession(c *authtoken.AccessClaims, s *authtoken.SessionState) *Actor {
	a := actorFromClaims(c)
	if s != nil {
		a.Permissions = s.EffectivePermissions()
	}
	return a
}

// Authenticate requires a valid access token whose session is still live:
// the account is active, its role unchanged and its sessions not revoked
// since the token was issued (authtoken.CheckSession).
func Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := bearer(r)
		if token == "" {
			WriteError(w, r, apperr.Unauth())
			return
		}
		claims, err := authtoken.VerifyAccess(token)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		state, err := authtoken.CheckSession(r.Context(), claims)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		next.ServeHTTP(w, withActor(r, actorFromSession(claims, state)))
	})
}

// OptionalAuthenticate attaches the actor when a valid, live token is present
// but never rejects.
func OptionalAuthenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token := bearer(r); token != "" {
			if claims, err := authtoken.VerifyAccess(token); err == nil {
				if state, err := authtoken.CheckSession(r.Context(), claims); err == nil {
					r = withActor(r, actorFromSession(claims, state))
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequireActiveUser re-reads the user so suspending an account takes effect
// without waiting for the access token to expire.
func RequireActiveUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := RequireActor(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		var user struct {
			Status string `bson:"status"`
			Role   string `bson:"role"`
		}
		err = mongox.C("users").FindOne(r.Context(), bson.M{"_id": actor.ObjectID()}).Decode(&user)
		if err != nil {
			if mongox.IsNotFound(err) {
				WriteError(w, r, apperr.Unauth("Account no longer exists"))
				return
			}
			WriteError(w, r, err)
			return
		}
		if user.Status != "ACTIVE" {
			WriteError(w, r, apperr.New(apperr.AccountDisabled, "Account is "+strings.ToLower(user.Status), 403))
			return
		}
		if user.Role != actor.Role {
			WriteError(w, r, apperr.New(apperr.TokenInvalid, "Session is stale, please sign in again", 401))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequireRole(roles ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, err := RequireActor(r)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			for _, role := range roles {
				if role == actor.Role {
					next.ServeHTTP(w, r)
					return
				}
			}
			WriteError(w, r, apperr.Forbid())
		})
	}
}

func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := RequireActor(r)
		if err != nil {
			WriteError(w, r, err)
			return
		}
		if !config.IsAdminRole(actor.Role) {
			WriteError(w, r, apperr.Forbid("Administrative access required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func RequirePermission(required ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			actor, err := RequireActor(r)
			if err != nil {
				WriteError(w, r, err)
				return
			}
			var missing []string
			for _, p := range required {
				if !config.HasPermission(actor.Role, actor.Permissions, p) {
					missing = append(missing, p)
				}
			}
			if len(missing) > 0 {
				WriteError(w, r, apperr.Forbid("Missing permission: "+strings.Join(missing, ", ")))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// HasPermission checks the current actor inline.
func HasPermission(r *http.Request, p string) bool {
	a := ActorFrom(r)
	return a != nil && config.HasPermission(a.Role, a.Permissions, p)
}

// AssertSelfOrPermission guards "my own resource, or an admin holding this
// permission" routes.
func AssertSelfOrPermission(r *http.Request, targetUserID, permission string) error {
	actor, err := RequireActor(r)
	if err != nil {
		return err
	}
	if actor.UserID == targetUserID || config.HasPermission(actor.Role, actor.Permissions, permission) {
		return nil
	}
	return apperr.Forbid()
}

// ── rate limiting ───────────────────────────────────────────────────────

type RateLimitOptions struct {
	Bucket  string
	Limit   int64
	Window  time.Duration
	By      string // "ip" | "user" | "user-or-ip" (default)
	Message string
}

// Named limits used across the API (spec section 29).
var RateLimits = struct {
	Login, Register, Refresh, Trade, Wallet, Transfer, Chat, Read, Admin, AdminWrite RateLimitOptions
	// MFA guards the second sign-in step (admin TOTP / recovery codes).
	MFA RateLimitOptions
}{
	MFA:        RateLimitOptions{Bucket: "auth:mfa", Limit: 10, Window: time.Minute, By: "ip"},
	Login:      RateLimitOptions{Bucket: "auth:login", Limit: 10, Window: time.Minute, By: "ip"},
	Register:   RateLimitOptions{Bucket: "auth:register", Limit: 5, Window: time.Minute, By: "ip"},
	Refresh:    RateLimitOptions{Bucket: "auth:refresh", Limit: 60, Window: time.Minute, By: "ip"},
	Trade:      RateLimitOptions{Bucket: "binary:trade", Limit: 60, Window: time.Minute, By: "user"},
	Wallet:     RateLimitOptions{Bucket: "wallet", Limit: 120, Window: time.Minute, By: "user"},
	Transfer:   RateLimitOptions{Bucket: "wallet:transfer", Limit: 10, Window: time.Minute, By: "user"},
	Chat:       RateLimitOptions{Bucket: "support:chat", Limit: 60, Window: time.Minute, By: "user"},
	Read:       RateLimitOptions{Bucket: "read", Limit: 600, Window: time.Minute, By: "user-or-ip"},
	Admin:      RateLimitOptions{Bucket: "admin", Limit: 600, Window: time.Minute, By: "user"},
	AdminWrite: RateLimitOptions{Bucket: "admin:write", Limit: 120, Window: time.Minute, By: "user"},
}

// RateLimit is a Redis fixed-window limiter shared across instances. If
// Redis is unreachable it never fails open: the same limits are enforced per
// instance in memory until Redis is back (see ratelimit_fallback.go).
func RateLimit(o RateLimitOptions) func(http.Handler) http.Handler {
	windowMs := o.Window.Milliseconds()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			identity := rateIdentity(r, o.By)
			window := time.Now().UnixMilli() / windowMs
			key := config.RedisKeys.RateLimit(o.Bucket, identity) + ":" + strconv.FormatInt(window, 10)

			count := countRequest(r.Context(), o.Bucket, key, o.Window, (window+1)*windowMs)
			remaining := o.Limit - count
			if remaining < 0 {
				remaining = 0
			}
			reset := (window + 1) * windowMs
			w.Header().Set("X-Ratelimit-Limit", strconv.FormatInt(o.Limit, 10))
			w.Header().Set("X-Ratelimit-Remaining", strconv.FormatInt(remaining, 10))
			w.Header().Set("X-Ratelimit-Reset", strconv.FormatInt(reset, 10))

			if count > o.Limit {
				retryAfterMs := reset - time.Now().UnixMilli()
				w.Header().Set("Retry-After", strconv.FormatInt((retryAfterMs+999)/1000, 10))
				msg := o.Message
				if msg == "" {
					msg = "Too many requests, please slow down"
				}
				WriteError(w, r, apperr.New(apperr.RateLimited, msg, 429, map[string]any{"retryAfterMs": retryAfterMs}))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func rateIdentity(r *http.Request, by string) string {
	ip := ClientIP(r)
	actor := ActorFrom(r)
	switch by {
	case "ip":
		return "ip:" + ip
	case "user":
		if actor != nil {
			return "user:" + actor.UserID
		}
		return "user:anonymous"
	}
	if actor != nil {
		return "user:" + actor.UserID
	}
	return "ip:" + ip
}

// ── request metadata ────────────────────────────────────────────────────

// ClientIP is the caller's address. Forwarding headers count only when the
// direct peer is a trusted proxy (TRUSTED_PROXIES); see clientip.go.
func ClientIP(r *http.Request) string {
	return clientIP(r)
}

// UserAgent is truncated to 512 characters; nil when absent.
func UserAgent(r *http.Request) *string {
	ua := r.Header.Get("User-Agent")
	if ua == "" {
		return nil
	}
	if len(ua) > 512 {
		ua = ua[:512]
	}
	return &ua
}

// Meta is the request-derived part of every audit row.
type Meta struct {
	IP        string
	UserAgent *string
	RequestID string
}

func AuditMeta(r *http.Request) Meta {
	return Meta{IP: ClientIP(r), UserAgent: UserAgent(r), RequestID: RequestID(r)}
}
