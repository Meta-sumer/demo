package exposure

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/assets"
	"binarytrading/internal/testsupport"
)

// fixture is a throwaway database (testsupport.LiveStores) plus the Redis
// keys this test creates. Every key it touches is either under the book
// namespace (which carries the throwaway database name) or named after ids it
// generated, so tests sharing the Redis cannot collide.
type fixture struct {
	t      *testing.T
	ctx    context.Context
	ns     string
	assets []string
	users  []string
	seq    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	logx.Init("silent", "test")
	testsupport.LiveStores(t)
	f := &fixture{t: t, ctx: context.Background(), ns: BookNamespace()}
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rdb := redisx.R()
	keys := []string{"lock:" + rebuildLockName(), "lock:worker:exposure-reconcile"}
	iter := rdb.Scan(ctx, 0, f.ns+":*", 1000).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	for _, a := range f.assets {
		keys = append(keys, "exp:asset:"+a)
		rdb.SRem(ctx, "exp:assets", a)
		rdb.LRem(ctx, "hedge:queue", 0, a)
	}
	for _, u := range f.users {
		keys = append(keys, "exp:user:"+u)
	}
	for i := 0; i < len(keys); i += 500 {
		rdb.Del(ctx, keys[i:min(i+500, len(keys))]...)
	}
	assets.InvalidateAssetCache()
}

func (f *fixture) asset(symbol string) string {
	f.t.Helper()
	id := bson.NewObjectID()
	now := mongox.Now()
	_, err := mongox.C(models.CollAssets).InsertOne(f.ctx, models.Asset{
		ID: id, Symbol: symbol + "-" + id.Hex()[18:], Name: symbol, Category: "CRYPTO", Active: true,
		MinStake: 1, MaxStake: 1_000_000_00, SupportedExpiries: []int64{60},
		ExposureLimit: 1_000_000_000_00, NetExposureLimit: 1_000_000_000_00, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	assets.InvalidateAssetCache()
	f.assets = append(f.assets, id.Hex())
	return id.Hex()
}

func (f *fixture) user() string {
	f.t.Helper()
	id := bson.NewObjectID()
	if _, err := mongox.C(models.CollUsers).InsertOne(f.ctx, bson.M{"_id": id, "status": "ACTIVE"}); err != nil {
		f.t.Fatal(err)
	}
	f.users = append(f.users, id.Hex())
	return id.Hex()
}

type tradeSpec struct {
	asset, user string
	dir         models.Direction
	stake       int64
	profit      int64
	expiry      int64 // epoch ms
	createdAgo  time.Duration
}

// insertTrade writes an OPEN LIVE trade straight to MongoDB.
func (f *fixture) insertTrade(s tradeSpec) string {
	f.t.Helper()
	id := bson.NewObjectID()
	f.insertTradeWithID(id, s)
	return id.Hex()
}

func (f *fixture) insertTradeWithID(id bson.ObjectID, s tradeSpec) {
	f.t.Helper()
	f.seq++
	created := time.Now().Add(-s.createdAgo).UTC()
	uid, _ := bson.ObjectIDFromHex(s.user)
	aid, _ := bson.ObjectIDFromHex(s.asset)
	if s.expiry == 0 {
		s.expiry = time.Now().Add(time.Minute).UnixMilli()
	}
	_, err := mongox.C(models.CollBinaryTrades).InsertOne(f.ctx, models.BinaryTrade{
		ID: id, Reference: "BT_t" + id.Hex(), UserID: uid, AssetID: aid, Symbol: "TEST",
		Direction: s.dir, AccountType: models.AccountLive, Stake: s.stake, Currency: "INR",
		Profit: s.profit, ExpiryTimestamp: s.expiry, Status: models.TradeStatusOpen,
		CreatedAt: created, UpdatedAt: created,
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// settleInMongo finishes a trade in MongoDB only (as settlement does first).
func (f *fixture) settleInMongo(id string, ago time.Duration) {
	f.t.Helper()
	oid, _ := bson.ObjectIDFromHex(id)
	at := time.Now().Add(-ago).UTC()
	if _, err := mongox.C(models.CollBinaryTrades).UpdateOne(f.ctx, bson.M{"_id": oid},
		bson.M{"$set": bson.M{"status": string(models.TradeStatusWon), "settledAt": at, "updatedAt": at}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) open(id string, s tradeSpec) OpenTrade {
	return OpenTrade{TradeID: id, AssetID: s.asset, Symbol: "TEST", UserID: s.user, Direction: s.dir,
		Stake: s.stake, Profit: s.profit, ExpiryTimestamp: s.expiry}
}

func (f *fixture) rebuild() RebuildResult {
	f.t.Helper()
	res, err := RebuildBook(f.ctx, RebuildOptions{Reason: "test", Force: true, Wait: 10 * time.Second})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

func (f *fixture) status() BookStatus {
	f.t.Helper()
	st, err := GetBookStatus(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return st
}

func unlimited() ReserveLimits {
	big := int64(1) << 50
	return ReserveLimits{UserExposure: big, AssetGross: big, AssetNet: big, PlatformGross: big, ConcentrationPercent: 100}
}

// expected is the book recomputed from MongoDB, the source of truth.
type expected struct {
	assets  map[string]RawCounters
	buckets map[string]map[string]BucketCounters
	users   map[string]map[string]int64
	gross   int64
	liab    int64
}

func (f *fixture) fromMongo() expected {
	f.t.Helper()
	cur, err := mongox.C(models.CollBinaryTrades).Find(f.ctx, openTradeFilter())
	if err != nil {
		f.t.Fatal(err)
	}
	var trades []models.BinaryTrade
	if err := cur.All(f.ctx, &trades); err != nil {
		f.t.Fatal(err)
	}
	e := expected{assets: map[string]RawCounters{}, buckets: map[string]map[string]BucketCounters{}, users: map[string]map[string]int64{}}
	for _, t := range trades {
		a, u := t.AssetID.Hex(), t.UserID.Hex()
		c := e.assets[a]
		if t.Direction == models.DirectionUp {
			c.UpStake += t.Stake
			c.UpProfit += t.Profit
		} else {
			c.DownStake += t.Stake
			c.DownProfit += t.Profit
		}
		c.OpenTrades++
		e.assets[a] = c
		if e.buckets[a] == nil {
			e.buckets[a] = map[string]BucketCounters{}
		}
		b := BucketOf(t.ExpiryTimestamp)
		e.buckets[a][b] = e.buckets[a][b].With(t.Direction, t.Stake, t.Profit)
		if e.users[u] == nil {
			e.users[u] = map[string]int64{}
		}
		e.users[u][a] += t.Stake
		e.gross += t.Stake
	}
	for _, bs := range e.buckets {
		for _, b := range bs {
			e.liab += BucketLiability(b)
		}
	}
	return e
}

// assertExact fails unless every Redis counter equals what MongoDB says,
// the book is READY and internally consistent.
func (f *fixture) assertExact(label string) {
	f.t.Helper()
	diffs := f.diff()
	if len(diffs) > 0 {
		f.t.Fatalf("%s: book differs from MongoDB:\n  %s", label, strings.Join(diffs, "\n  "))
	}
}

func (f *fixture) diff() []string {
	f.t.Helper()
	e := f.fromMongo()
	diffs := []string{}
	add := func(format string, args ...any) { diffs = append(diffs, fmt.Sprintf(format, args...)) }

	st, err := redisx.R().HGetAll(f.ctx, f.ns+":state").Result()
	if err != nil {
		f.t.Fatal(err)
	}
	if st["status"] != BookReady {
		add("status %q", st["status"])
	}
	if num(st, "gross") != e.gross {
		add("state gross %d, want %d", num(st, "gross"), e.gross)
	}
	if num(st, "liab") != e.liab {
		add("state liab %d, want %d", num(st, "liab"), e.liab)
	}
	if num(st, "users") != int64(len(e.users)) {
		add("state users %d, want %d", num(st, "users"), len(e.users))
	}
	for _, a := range f.assets {
		h, _ := redisx.R().HGetAll(f.ctx, "exp:asset:"+a).Result()
		got, want := countersFrom(h), e.assets[a]
		var wantLiab int64
		for _, b := range e.buckets[a] {
			wantLiab += BucketLiability(b)
		}
		want.ExpiryLiability, want.ExpiryBuckets = wantLiab, int64(len(e.buckets[a]))
		if got != want {
			add("asset %s counters %+v, want %+v", a, got, want)
		}
		bk, _ := redisx.R().HGetAll(f.ctx, f.ns+":bkt:"+a).Result()
		if len(bk) != len(e.buckets[a]) {
			add("asset %s has %d buckets, want %d", a, len(bk), len(e.buckets[a]))
		}
		for k, b := range e.buckets[a] {
			if parseBucket(bk[k]) != b {
				add("asset %s bucket %s %+v, want %+v", a, k, parseBucket(bk[k]), b)
			}
		}
	}
	for _, u := range f.users {
		ux, err := GetUserExposure(f.ctx, u)
		if err != nil {
			f.t.Fatal(err)
		}
		var want int64
		for _, v := range e.users[u] {
			want += v
		}
		if ux.Total != want {
			add("user %s total %d, want %d", u, ux.Total, want)
		}
		for a, v := range e.users[u] {
			if ux.PerAsset[a] != v {
				add("user %s asset %s %d, want %d", u, a, ux.PerAsset[a], v)
			}
		}
		for a, v := range ux.PerAsset {
			if e.users[u][a] != v {
				add("user %s extra asset %s %d", u, a, v)
			}
		}
	}
	snap, err := takeSnapshot(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	diffs = append(diffs, checkInternal(snap)...)
	sort.Strings(diffs)
	return diffs
}

func (f *fixture) setGrace(d time.Duration) {
	prev := ReconcileGrace
	ReconcileGrace = d
	f.t.Cleanup(func() { ReconcileGrace = prev })
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
