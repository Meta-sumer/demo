// Package testsupport holds helpers shared by tests; only _test.go files
// import it.
package testsupport

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
)

// env reads a variable from the environment, else from the .env files the
// server itself would load (server-go/.env, then the repo root's).
func env(key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	for _, file := range []string{filepath.Join(dir, ".env"), filepath.Join(dir, "..", ".env")} {
		if vals, err := godotenv.Read(file); err == nil && vals[key] != "" {
			return vals[key]
		}
	}
	return ""
}

// withDatabase points a MongoDB URI at another database, keeping the auth
// source of a URI with credentials.
func withDatabase(uri string, cs *connstring.ConnString, name string) string {
	scheme := "mongodb://"
	if strings.HasPrefix(uri, "mongodb+srv://") {
		scheme = "mongodb+srv://"
	}
	rest, query := strings.TrimPrefix(uri, scheme), ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	if cs.Username != "" && !strings.Contains(query, "authSource=") {
		source := cs.Database
		if source == "" {
			source = "admin"
		}
		if query == "" {
			query = "?authSource=" + source
		} else {
			query += "&authSource=" + source
		}
	}
	return scheme + rest + "/" + name + query
}

// LiveStores connects mongox to a throwaway database on the configured
// MongoDB, dropped when the test ends, and redisx to the configured Redis
// (the rate limiter needs it). Redis is shared: tests must use keys of their
// own, such as ids they generate, and delete them. Skips the test when either
// store is unreachable.
func LiveStores(t *testing.T) {
	t.Helper()
	mongoURI, redisURL := env("MONGO_URI"), env("REDIS_URL")
	if mongoURI == "" || redisURL == "" {
		t.Skip("MONGO_URI / REDIS_URL not configured")
	}
	cs, err := connstring.ParseAndValidate(mongoURI)
	if err != nil || len(cs.Hosts) == 0 {
		t.Skipf("unusable MONGO_URI: %v", err)
	}
	conn, err := net.DialTimeout("tcp", cs.Hosts[0], time.Second)
	if err != nil {
		t.Skipf("MongoDB not reachable: %v", err)
	}
	_ = conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dbName := "binary_trading_test_" + bson.NewObjectID().Hex()
	if err := mongox.Connect(ctx, withDatabase(mongoURI, cs, dbName), false); err != nil {
		t.Skipf("MongoDB: %v", err)
	}
	if mongox.DB().Name() != dbName {
		_ = mongox.Disconnect(ctx)
		t.Fatalf("connected to %q, not the throwaway database", mongox.DB().Name())
	}
	if err := redisx.Connect(ctx, redisURL); err != nil {
		_ = mongox.Disconnect(ctx)
		t.Skipf("Redis: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := mongox.DB().Drop(ctx); err != nil {
			t.Errorf("drop %s: %v", dbName, err)
		}
		_ = mongox.Disconnect(ctx)
		_ = redisx.Disconnect()
	})
}
