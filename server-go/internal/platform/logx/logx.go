// Package logx is structured logging (spec section 40) on log/slog.
//
// Every line carries the service name. Secrets are never passed to the logger
// in the first place; the redaction below is a second line of defence.
package logx

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

var L = slog.New(slog.NewTextHandler(os.Stdout, nil))

var redacted = map[string]bool{
	"authorization": true, "cookie": true, "password": true, "passwordhash": true,
	"refreshtoken": true, "accesstoken": true, "apikey": true, "apisecret": true,
}

// Init configures the global logger. Production logs JSON; development logs
// human-readable text.
func Init(level, nodeEnv string) {
	var lv slog.Level
	switch level {
	case "trace", "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error", "fatal":
		lv = slog.LevelError
	case "silent":
		lv = slog.Level(100)
	default:
		lv = slog.LevelInfo
	}
	if nodeEnv == "test" {
		lv = slog.Level(100)
	}
	opts := &slog.HandlerOptions{
		Level: lv,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if redacted[strings.ToLower(a.Key)] {
				return slog.String(a.Key, "[redacted]")
			}
			return a
		},
	}
	var h slog.Handler
	if nodeEnv == "production" {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	L = slog.New(h).With("service", "binary-trading-server")
	slog.SetDefault(L)
}

func Debug(msg string, args ...any) { L.Debug(msg, args...) }
func Info(msg string, args ...any)  { L.Info(msg, args...) }
func Warn(msg string, args ...any)  { L.Warn(msg, args...) }
func Error(msg string, args ...any) { L.Error(msg, args...) }

// With returns a child logger carrying extra bindings.
func With(args ...any) *slog.Logger { return L.With(args...) }

type ctxKey struct{}

// IntoContext stores a request-scoped logger.
func IntoContext(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From returns the request-scoped logger, or the global one.
func From(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return L
}
