package exposure

// Coordinated rebuild of the book from MongoDB.
//
//  1. Take the rebuild lock (one rebuild per database at a time).
//  2. rebuildBegin: state -> REBUILDING with a new generation. From here on
//     no reservation is granted (safe mode) and every commit/close is still
//     applied to the live keys but also journalled under the generation.
//  3. Read the open LIVE trades from MongoDB and build them into temporary
//     keys (<ns>:tmp:<gen>:...), never touching the live ones.
//  4. rebuildSwap, one atomic script: replay the journal onto the temporary
//     book (per-trade idempotency makes a trade that is both in the MongoDB
//     read and in the journal count once), delete the old live keys, rename
//     the temporary keys into place, state -> READY.
//
// Why this is exact under concurrent trading: anything not journalled
// happened before step 2, so its MongoDB write (the insert precedes the
// commit, the settlement precedes the release) is visible to the read in
// step 3; anything after step 2 is in the journal. A rebuilder that stalls
// past its lock loses the generation and its swap is refused.

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/service/audit"
)

const (
	rebuildLockTTL = 60 * time.Second
	rebuildChunk   = 500
	// AuditBookRebuilt and AuditBookReconciled are the audit actions written
	// when the book is rebuilt or repaired.
	AuditBookRebuilt    = "exposure.rebuilt"
	AuditBookReconciled = "exposure.reconciled"
)

func rebuildLockName() string { return "exposure-rebuild:" + mongox.DB().Name() }

type RebuildOptions struct {
	Reason string
	// How long to wait for the lock when another instance is rebuilding.
	// 0 tries once.
	Wait time.Duration
	// Rebuild even when the book is READY.
	Force bool
}

type RebuildResult struct {
	Rebuilt    bool   `json:"rebuilt"`
	Generation string `json:"generation"`
	Previous   string `json:"previous"`
	Trades     int    `json:"trades"`
	Assets     int    `json:"assets"`
	Journal    int    `json:"journal"`
	Keys       int    `json:"keys"`
}

// openTradeFilter selects the trades that make up the house book.
func openTradeFilter() bson.M {
	return bson.M{"status": openStatuses, "accountType": models.NotDemo()}
}

var openTradeProjection = bson.M{
	"userId": 1, "assetId": 1, "symbol": 1, "direction": 1, "stake": 1, "profit": 1,
	"expiryTimestamp": 1, "status": 1, "accountType": 1, "createdAt": 1, "settledAt": 1, "updatedAt": 1,
}

// bookTrade is the projection of a trade the book needs.
type bookTrade struct {
	ID              bson.ObjectID      `bson:"_id"`
	UserID          bson.ObjectID      `bson:"userId"`
	AssetID         bson.ObjectID      `bson:"assetId"`
	Symbol          string             `bson:"symbol"`
	Direction       models.Direction   `bson:"direction"`
	Stake           int64              `bson:"stake"`
	Profit          int64              `bson:"profit"`
	ExpiryTimestamp int64              `bson:"expiryTimestamp"`
	Status          models.TradeStatus `bson:"status"`
	AccountType     string             `bson:"accountType"`
	CreatedAt       time.Time          `bson:"createdAt"`
	SettledAt       *time.Time         `bson:"settledAt"`
	UpdatedAt       time.Time          `bson:"updatedAt"`
}

func (t bookTrade) open() OpenTrade {
	return OpenTrade{
		TradeID: t.ID.Hex(), AssetID: t.AssetID.Hex(), Symbol: t.Symbol, UserID: t.UserID.Hex(),
		Direction: t.Direction, Stake: t.Stake, Profit: t.Profit, ExpiryTimestamp: t.ExpiryTimestamp,
	}
}

func (t bookTrade) live() bool {
	return (t.Status == models.TradeStatusOpen || t.Status == models.TradeStatusSettling) &&
		models.NormAccountType(t.AccountType) == models.AccountLive
}

// RebuildBook rebuilds the book from MongoDB under the rebuild lock. Without
// Force it does nothing when the book is already READY (another instance may
// have just rebuilt it). Returns ErrBookUnavailable when the lock stayed busy
// and the book is still not READY.
func RebuildBook(ctx context.Context, opts RebuildOptions) (RebuildResult, error) {
	var (
		lock *redisx.Lock
		err  error
	)
	if opts.Wait > 0 {
		lock, err = redisx.AcquireWithRetry(ctx, rebuildLockName(), rebuildLockTTL, opts.Wait, 25*time.Millisecond)
	} else {
		lock, err = redisx.Acquire(ctx, rebuildLockName(), rebuildLockTTL)
	}
	if err != nil {
		return RebuildResult{}, err
	}
	if lock == nil {
		st, err := GetBookStatus(ctx)
		if err != nil {
			return RebuildResult{}, err
		}
		if st.Status == BookReady {
			return RebuildResult{}, nil
		}
		return RebuildResult{}, ErrBookUnavailable
	}
	defer func() {
		if _, err := redisx.Release(context.WithoutCancel(ctx), lock); err != nil {
			logx.Warn("failed to release the exposure rebuild lock", "err", err.Error())
		}
	}()
	if !opts.Force {
		st, err := GetBookStatus(ctx)
		if err != nil {
			return RebuildResult{}, err
		}
		if st.Status == BookReady {
			return RebuildResult{}, nil
		}
	}
	return rebuildLocked(ctx, lock, opts.Reason)
}

func rebuildLocked(ctx context.Context, lock *redisx.Lock, reason string) (RebuildResult, error) {
	started := time.Now()
	rdb := redisx.R()
	ns := BookNamespace()
	gen := bson.NewObjectID().Hex()

	prev, err := rebuildBeginScript.Run(ctx, rdb, nil, ns, gen, nowArg()).Text()
	if err != nil {
		return RebuildResult{}, err
	}
	out := RebuildResult{Generation: gen, Previous: prev}
	fail := func(cause error) (RebuildResult, error) {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := rebuildAbortScript.Run(actx, rdb, nil, ns, gen, nowArg(), "rebuild failed: "+cause.Error()).Err(); err != nil {
			logx.Error("failed to abort an exposure rebuild", "gen", gen, "err", err.Error())
		}
		logx.Error("exposure rebuild failed", "gen", gen, "err", cause.Error())
		return RebuildResult{}, cause
	}
	keepLock := func() error {
		ok, err := redisx.Renew(ctx, lock, rebuildLockTTL)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("exposure rebuild lock lost")
		}
		return nil
	}

	// The open LIVE trades, read after REBUILDING was set.
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, openTradeFilter(), options.Find().SetProjection(openTradeProjection))
	if err != nil {
		return fail(err)
	}
	defer cur.Close(ctx)
	load := func(args []any) error {
		n, err := rebuildLoadScript.Run(ctx, rdb, nil, args...).Int()
		if err != nil {
			return err
		}
		if n < 0 {
			return errors.New("exposure rebuild superseded")
		}
		return keepLock()
	}
	args := []any{ns, gen, nowArg()}
	for cur.Next(ctx) {
		var t bookTrade
		if err := cur.Decode(&t); err != nil {
			return fail(err)
		}
		o := t.open()
		args = append(args, o.TradeID, o.AssetID, o.UserID, dirArg(o.Direction), o.Stake, o.Profit,
			BucketOf(o.ExpiryTimestamp), cleanSymbol(o.Symbol))
		out.Trades++
		if len(args) >= 3+8*rebuildChunk {
			if err := load(args); err != nil {
				return fail(err)
			}
			args = []any{ns, gen, nowArg()}
		}
	}
	if err := cur.Err(); err != nil {
		return fail(err)
	}
	if len(args) > 3 {
		if err := load(args); err != nil {
			return fail(err)
		}
	}

	// Every registered asset gets a hash, so views read zeros, not gaps.
	list, err := assets.ListAssetDocs(ctx, false)
	if err != nil {
		return fail(err)
	}
	out.Assets = len(list)
	for i := 0; i < len(list); i += rebuildChunk {
		a := []any{ns, gen, nowArg()}
		for _, asset := range list[i:min(i+rebuildChunk, len(list))] {
			a = append(a, asset.ID.Hex(), cleanSymbol(asset.Symbol))
		}
		if n, err := rebuildAssetsScript.Run(ctx, rdb, nil, a...).Int(); err != nil {
			return fail(err)
		} else if n < 0 {
			return fail(errors.New("exposure rebuild superseded"))
		}
	}

	extras, err := strayKeys(ctx, ns)
	if err != nil {
		return fail(err)
	}
	if err := keepLock(); err != nil {
		return fail(err)
	}

	swap := append([]any{ns, gen, nowArg()}, extras...)
	res, err := rebuildSwapScript.Run(ctx, rdb, nil, swap...).Slice()
	if err != nil {
		return fail(err)
	}
	parts := strs(res)
	if len(parts) == 0 || parts[0] != "OK" {
		// Superseded by a newer generation: that rebuild owns the book now.
		return RebuildResult{}, errors.New("exposure rebuild superseded: " + strings.Join(parts, " "))
	}
	if len(parts) >= 3 {
		out.Keys, _ = strconv.Atoi(parts[1])
		out.Journal, _ = strconv.Atoi(parts[2])
	}
	out.Rebuilt = true

	logx.Info("exposure book rebuilt from the trade ledger",
		"gen", gen, "previous", prev, "reason", reason, "trades", out.Trades, "assets", out.Assets,
		"journal", out.Journal, "ms", time.Since(started).Milliseconds())
	actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	audit.Write(actx, audit.Entry{
		ActorRole: "system", Action: AuditBookRebuilt, Resource: "ExposureBook", ResourceID: gen,
		Before: map[string]any{"status": prev}, After: out, Reason: reason,
	})
	return out, nil
}

// strayKeys finds live keys the rebuild must retire that the book's own
// registries may not list: anything under this book's namespace, and
// exp:user:* hashes of this database's users (older versions never removed
// them, and a stale one would read as open exposure).
func strayKeys(ctx context.Context, ns string) ([]any, error) {
	out := []any{}
	seenA, seenU := map[string]bool{}, map[string]bool{}
	scan := func(pattern string, each func(key string)) error {
		iter := redisx.R().Scan(ctx, 0, pattern, 1000).Iterator()
		for iter.Next(ctx) {
			each(iter.Val())
		}
		return iter.Err()
	}
	if err := scan(ns+":bkt:*", func(k string) {
		if id := strings.TrimPrefix(k, ns+":bkt:"); !seenA[id] {
			seenA[id] = true
			out = append(out, "a:"+id)
		}
	}); err != nil {
		return nil, err
	}
	if err := scan(ns+":ot:*", func(k string) {
		if id := strings.TrimPrefix(k, ns+":ot:"); !seenU[id] {
			seenU[id] = true
			out = append(out, "u:"+id)
		}
	}); err != nil {
		return nil, err
	}

	var candidates []bson.ObjectID
	if err := scan("exp:user:*", func(k string) {
		id := strings.TrimPrefix(k, "exp:user:")
		if oid, err := bson.ObjectIDFromHex(id); err == nil && !seenU[id] {
			candidates = append(candidates, oid)
		}
	}); err != nil {
		return nil, err
	}
	// Only users of this database: on a shared Redis the others belong to
	// another book.
	for i := 0; i < len(candidates); i += 1000 {
		batch := candidates[i:min(i+1000, len(candidates))]
		cur, err := mongox.C(models.CollUsers).Find(ctx, bson.M{"_id": bson.M{"$in": batch}},
			options.Find().SetProjection(bson.M{"_id": 1}))
		if err != nil {
			return nil, err
		}
		var rows []struct {
			ID bson.ObjectID `bson:"_id"`
		}
		if err := cur.All(ctx, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			if id := r.ID.Hex(); !seenU[id] {
				seenU[id] = true
				out = append(out, "u:"+id)
			}
		}
	}
	return out, nil
}

// EnsureBookReady makes the book READY, rebuilding it when needed and
// waiting up to wait for a rebuild running elsewhere. The acceptance path
// calls it after a reservation came back UNCERTAIN.
func EnsureBookReady(ctx context.Context, wait time.Duration) error {
	st, err := GetBookStatus(ctx)
	if err != nil {
		return err
	}
	if st.Status == BookReady {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, wait+30*time.Second)
	defer cancel()
	reason := "book " + st.Status
	if st.Reason != "" {
		reason += " (" + st.Reason + ")"
	}
	if _, err := RebuildBook(rctx, RebuildOptions{Reason: reason, Wait: wait}); err != nil {
		return err
	}
	st, err = GetBookStatus(ctx)
	if err != nil {
		return err
	}
	if st.Status != BookReady {
		return ErrBookUnavailable
	}
	return nil
}

// EnsureBookOnBoot is the worker-start check: rebuild a book that is not
// READY, otherwise verify it against MongoDB (which rebuilds it only when it
// is inconsistent). Unlike the old unconditional boot rebuild, booting an
// instance never wipes counters other instances are using.
func EnsureBookOnBoot(ctx context.Context) error {
	st, err := GetBookStatus(ctx)
	if err != nil {
		return err
	}
	if st.Status != BookReady {
		_, err := RebuildBook(ctx, RebuildOptions{Reason: "boot: book " + st.Status, Wait: 60 * time.Second})
		if errors.Is(err, ErrBookUnavailable) {
			logx.Warn("exposure book still rebuilding elsewhere; LIVE trades stay refused until it is READY")
			return nil
		}
		return err
	}
	_, err = Reconcile(ctx)
	return err
}
