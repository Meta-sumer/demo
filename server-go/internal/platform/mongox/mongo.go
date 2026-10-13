// Package mongox owns the MongoDB connection.
//
// MongoDB is the permanent record for every financial fact. Multi-document
// transactions need a replica set; when one is available WithTransaction uses
// it, otherwise it degrades to a plain call and services rely on their guarded
// atomic updates and unique-index idempotency.
package mongox

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"binarytrading/internal/platform/logx"
)

var (
	client                *mongo.Client
	db                    *mongo.Database
	transactionsSupported bool
)

// Connect opens the pool and probes transaction support.
func Connect(ctx context.Context, uri string, enableTransactions bool) error {
	cs, err := connstring.ParseAndValidate(uri)
	if err != nil {
		return err
	}
	name := cs.Database
	if name == "" {
		name = "test"
	}

	opts := options.Client().
		ApplyURI(uri).
		SetServerSelectionTimeout(10 * time.Second).
		SetMaxPoolSize(50).
		SetMinPoolSize(5).
		SetRetryWrites(true).
		SetBSONOptions(&options.BSONOptions{
			// Embedded documents decoded into `any` become maps, which
			// serialise to JSON the way Mongoose's lean() output does.
			DefaultDocumentMap: true,
			// A nil slice is stored as [], matching Mongoose array defaults.
			NilSliceAsEmpty: true,
		})

	c, err := mongo.Connect(opts)
	if err != nil {
		return err
	}
	if err := c.Ping(ctx, readpref.Primary()); err != nil {
		return err
	}
	client = c
	db = c.Database(name)

	transactionsSupported = enableTransactions && probeTransactions(ctx)
	logx.Info("mongodb connected", "db", name, "transactions", transactionsSupported)
	return nil
}

func probeTransactions(ctx context.Context) bool {
	sess, err := client.StartSession()
	if err != nil {
		return false
	}
	defer sess.EndSession(ctx)
	if err := sess.StartTransaction(); err != nil {
		logx.Warn("multi-document transactions unavailable - falling back to guarded atomic writes", "err", err.Error())
		return false
	}
	// A transaction is only really started by the first operation, so probe
	// with a cheap read inside it.
	sctx := mongo.NewSessionContext(ctx, sess)
	if err := db.Collection("users").FindOne(sctx, bson.M{"_id": bson.NewObjectID()}).Err(); err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
		_ = sess.AbortTransaction(ctx)
		logx.Warn("multi-document transactions unavailable - falling back to guarded atomic writes", "err", err.Error())
		return false
	}
	_ = sess.AbortTransaction(ctx)
	return true
}

func Disconnect(ctx context.Context) error {
	if client == nil {
		return nil
	}
	return client.Disconnect(ctx)
}

// Healthy pings the primary with a short timeout.
func Healthy(ctx context.Context) bool {
	if client == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return client.Ping(ctx, readpref.Primary()) == nil
}

func TransactionsAvailable() bool { return transactionsSupported }

func Client() *mongo.Client           { return client }
func DB() *mongo.Database             { return db }
func C(name string) *mongo.Collection { return db.Collection(name) }

// Now is the timestamp to store: UTC, truncated to the millisecond precision
// BSON dates keep, so a value read back compares equal to the one written.
func Now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// WithTransaction runs fn inside a transaction when the deployment supports
// one; fn must pass the ctx it receives to every collection call. Transient
// failures (write conflicts, lock timeouts) are retried with backoff.
func WithTransaction(ctx context.Context, fn func(ctx context.Context) error) error {
	if !transactionsSupported {
		return fn(ctx)
	}
	const retries = 5
	var lastErr error
	for attempt := 0; attempt < retries; attempt++ {
		err := runOnce(ctx, fn)
		if err == nil {
			return nil
		}
		lastErr = err
		if !isTransient(err) || attempt == retries-1 {
			return err
		}
		logx.Warn("retrying transient transaction failure", "attempt", attempt)
		backoff := time.Duration(10*(1<<attempt))*time.Millisecond + time.Duration(rand.IntN(10))*time.Millisecond
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

func runOnce(ctx context.Context, fn func(ctx context.Context) error) error {
	sess, err := client.StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(context.Background())

	// `local` read concern: correctness comes from guarded findOneAndUpdate
	// calls, which are atomic on their own. Writes are majority so a committed
	// balance change survives a failover.
	txOpts := options.Transaction().
		SetReadConcern(readconcern.Local()).
		SetWriteConcern(writeconcern.Majority())
	if err := sess.StartTransaction(txOpts); err != nil {
		return err
	}
	sctx := mongo.NewSessionContext(ctx, sess)
	if err := fn(sctx); err != nil {
		_ = sess.AbortTransaction(context.Background())
		return err
	}
	return sess.CommitTransaction(sctx)
}

var retryableCodes = []int{24, 112, 225, 246, 262}

func isTransient(err error) bool {
	var se mongo.ServerError
	if errors.As(err, &se) {
		if se.HasErrorLabel("TransientTransactionError") || se.HasErrorLabel("UnknownTransactionCommitResult") {
			return true
		}
		for _, c := range retryableCodes {
			if se.HasErrorCode(c) {
				return true
			}
		}
	}
	return false
}

// IsDuplicateKey reports a write that collided with a unique index.
func IsDuplicateKey(err error) bool { return mongo.IsDuplicateKeyError(err) }

// IsNotFound reports mongo.ErrNoDocuments.
func IsNotFound(err error) bool { return errors.Is(err, mongo.ErrNoDocuments) }

// ParseID parses a 24-hex ObjectID.
func ParseID(hex string) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(hex)
	return id, err == nil
}

// Index is one index definition, created at startup.
type Index struct {
	Collection string
	Keys       bson.D
	Options    *options.IndexOptionsBuilder
}

// EnsureIndexes creates indexes the way Mongoose's autoIndex does. Failures
// are logged, not fatal: an index that conflicts with an existing definition
// of the same name must not stop the server.
func EnsureIndexes(ctx context.Context, indexes []Index) {
	for _, ix := range indexes {
		model := mongo.IndexModel{Keys: ix.Keys}
		if ix.Options != nil {
			model.Options = ix.Options
		}
		if _, err := db.Collection(ix.Collection).Indexes().CreateOne(ctx, model); err != nil {
			logx.Warn("index creation failed", "collection", ix.Collection, "err", err.Error())
		}
	}
}
