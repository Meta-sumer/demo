//go:build loadtest

package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/marketdata"
	"binarytrading/internal/service/marketdata/providers"
	"binarytrading/internal/service/reconciliation"
	"binarytrading/internal/testsupport"
)

// TestLoadTradeStormAndBookRebuild (audit F-05, F-06): many concurrent LIVE
// trade requests through the real HTTP router, including client retries that
// reuse an idempotency key (some concurrently, some late, some with a
// different payload), against binding exposure limits, with a forced
// exposure-book rebuild and a safe-mode episode in the middle of the run.
//
// It asserts that no limit is ever breached in MongoDB (the record), that the
// Redis book equals MongoDB afterwards with no reservation left, that every
// idempotency key produced at most one trade and every successful answer for
// a key names that trade, that every customer's locked funds equal their open
// stakes, and that the reconciliation run passes. It records throughput and
// p50/p95/p99 latency.
func TestLoadTradeStormAndBookRebuild(t *testing.T) {
	requireLoadTest(t)
	loadConfig(t, nil)
	logx.Init("error", "test")
	testsupport.LiveStores(t)
	enableTx(t)
	ctx := context.Background()
	models.EnsureIndexes(ctx)
	ns := "loadtest:" + bson.NewObjectID().Hex()
	saved := config.RedisKeys
	config.RedisKeys.ExpiryQueue = ns + ":expiry:queue"
	config.RedisKeys.SettlementQueue = ns + ":settlement:queue"
	config.RedisKeys.ExposureAssets = ns + ":exp:assets"
	t.Cleanup(func() { config.RedisKeys = saved })

	// Limits under test (stake = 100.00 each).
	var (
		users   = intEnv("LOADTEST_USERS", 120)
		perUser = intEnv("LOADTEST_TRADES_PER_USER", 12)
		workers = intEnv("LOADTEST_CONCURRENCY", 48)
		// Binding by default; LOADTEST_*_STAKES=large measures capacity.
		grossLimit = int64(intEnv("LOADTEST_GROSS_STAKES", 150)) * ltStake
		netLimit   = int64(intEnv("LOADTEST_NET_STAKES", 40)) * ltStake
		userLimit  = int64(intEnv("LOADTEST_USER_STAKES", 8)) * ltStake
		expiry     = int64(300)
	)
	cfg := setRisk(t, ctx, tradingRisk(userLimit))
	asset := createAsset(t, ctx, expiry, grossLimit, netLimit)
	assetOID, _ := mongox.ParseID(asset.ID)
	// One customer in ten can afford only two trades (insufficient funds).
	us := createUsers(t, ctx, users, func(i int) int64 {
		if i%10 == 9 {
			return 2 * ltStake
		}
		return 20 * ltStake
	})
	dbName := mongox.DB().Name()
	writeResult(t, "start", map[string]any{"pid": os.Getpid(), "db": dbName})
	// Runs before LiveStores' own cleanup: let fire-and-forget writes (audit
	// rows, metrics) finish, then remove every key and the database, and
	// check the database is really gone (a straggling write recreates it).
	t.Cleanup(func() {
		time.Sleep(3 * time.Second)
		cleanupRedis(ctx, dbName, ns, asset.ID, us)
		dropAndVerify(t, dbName)
	})

	stopFeed := feedTicks(t, ctx, "btcusdt", 50*time.Millisecond)
	defer stopFeed()
	if _, err := exposure.RebuildBook(ctx, exposure.RebuildOptions{Reason: "loadtest start", Wait: 10 * time.Second, Force: true}); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(newRouter())
	defer srv.Close()
	client := &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: workers * 3, MaxConnsPerHost: workers * 3}}

	// The plan: per customer, perUser logical trades. Each is sent 1-3 times
	// at once with one idempotency key (a client retrying a slow request);
	// 1 in 5 keys is retried once more after the storm, 1 in 25 also gets a
	// concurrent copy with a different amount (a client bug).
	type job struct {
		u        *ltUser
		req      tradeReq
		copies   int
		late     bool
		mismatch bool
	}
	rng := rand.New(rand.NewPCG(42, 7))
	jobs := []job{}
	for i := range us {
		for k := 0; k < perUser; k++ {
			dir := "UP"
			if rng.Float64() < 0.35 {
				dir = "DOWN"
			}
			jobs = append(jobs, job{
				u:   &us[i],
				req: tradeReq{AssetID: asset.ID, Direction: dir, Amount: ltStake, ExpirySeconds: expiry, AccountType: "LIVE", IdempotencyKey: "lt-" + uuid.NewString()},
				// 1, 2 or 3 copies.
				copies: 1 + rng.IntN(3), late: rng.IntN(5) == 0, mismatch: rng.IntN(25) == 0,
			})
		}
	}
	rng.Shuffle(len(jobs), func(i, j int) { jobs[i], jobs[j] = jobs[j], jobs[i] })

	type keyResult struct {
		mu        sync.Mutex
		ids       map[string]bool
		statuses  []int
		mismatchS []int
	}
	results := make([]keyResult, len(jobs))
	for i := range results {
		results[i].ids = map[string]bool{}
	}
	var (
		all, accepted latencies
		codes         sync.Map // status/code -> *atomic.Int64
		requests      atomic.Int64
		done          atomic.Int64
		rebuilds      []string
		rebuildMu     sync.Mutex
	)
	count := func(k string) {
		v, _ := codes.LoadOrStore(k, new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
	}
	record := func(i int, r tradeResp, mismatch bool) {
		requests.Add(1)
		all.add(r.Latency)
		key := fmt.Sprintf("%d", r.Status)
		if r.Err != nil {
			key = "transport-error"
		} else if r.Code != "" {
			key += " " + r.Code
		}
		count(key)
		res := &results[i]
		res.mu.Lock()
		defer res.mu.Unlock()
		if mismatch {
			res.mismatchS = append(res.mismatchS, r.Status)
		} else {
			res.statuses = append(res.statuses, r.Status)
		}
		if r.Status == 201 || r.Status == 200 {
			if r.Status == 201 {
				accepted.add(r.Latency)
			}
			res.ids[r.TradeID] = true
		}
	}

	// Mid-run book events.
	var eventsWG sync.WaitGroup
	triggerAt := func(frac float64, name string, fn func() error) {
		eventsWG.Add(1)
		go func() {
			defer eventsWG.Done()
			for done.Load() < int64(frac*float64(len(jobs))) {
				time.Sleep(5 * time.Millisecond)
			}
			started := time.Now()
			err := fn()
			rebuildMu.Lock()
			rebuilds = append(rebuilds, fmt.Sprintf("%s at %d/%d jobs: %v (err %v)", name, done.Load(), len(jobs), time.Since(started).Round(time.Millisecond), err))
			rebuildMu.Unlock()
		}()
	}
	triggerAt(0.35, "forced rebuild", func() error {
		_, err := exposure.RebuildBook(ctx, exposure.RebuildOptions{Reason: "loadtest forced rebuild", Wait: 10 * time.Second, Force: true})
		return err
	})
	triggerAt(0.65, "safe mode (book marked DIRTY) then rebuild", func() error {
		if err := exposure.MarkBookDirty(ctx, "loadtest: simulated drift"); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		_, err := exposure.RebuildBook(ctx, exposure.RebuildOptions{Reason: "loadtest after safe mode", Wait: 10 * time.Second, Force: true})
		return err
	})

	start := time.Now()
	queue := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range queue {
				j := jobs[i]
				var cw sync.WaitGroup
				for c := 0; c < j.copies; c++ {
					cw.Add(1)
					go func() { defer cw.Done(); record(i, postTrade(ctx, client, srv.URL, j.u.token, j.req), false) }()
				}
				if j.mismatch {
					other := j.req
					other.Amount = 2 * ltStake
					cw.Add(1)
					go func() { defer cw.Done(); record(i, postTrade(ctx, client, srv.URL, j.u.token, other), true) }()
				}
				cw.Wait()
				done.Add(1)
			}
		}()
	}
	for i := range jobs {
		queue <- i
	}
	close(queue)
	wg.Wait()
	stormElapsed := time.Since(start)
	eventsWG.Wait()

	// Late retries: the client asks again with the same key and payload.
	lateStart := time.Now()
	var lw sync.WaitGroup
	sem := make(chan struct{}, workers)
	for i, j := range jobs {
		if !j.late {
			continue
		}
		lw.Add(1)
		sem <- struct{}{}
		go func() {
			defer lw.Done()
			defer func() { <-sem }()
			record(i, postTrade(ctx, client, srv.URL, j.u.token, j.req), false)
		}()
	}
	lw.Wait()
	totalElapsed := time.Since(start)
	_ = lateStart

	// ── assertions ─────────────────────────────────────────────────────
	codeList := []string{}
	codes.Range(func(k, v any) bool {
		codeList = append(codeList, fmt.Sprintf("%s: %d", k, v.(*atomic.Int64).Load()))
		return true
	})
	sort.Strings(codeList)
	for _, c := range codeList {
		if strings.HasPrefix(c, "5") && !strings.Contains(c, "EXPOSURE_BOOK_UNAVAILABLE") && !strings.Contains(c, "SERVICE_UNAVAILABLE") {
			t.Errorf("unexpected server error class: %s", c)
		}
		if strings.HasPrefix(c, "transport-error") {
			t.Errorf("transport errors: %s", c)
		}
	}

	// Idempotency: one trade per key, and every success names it.
	keyTrades := map[string]string{}
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{"idempotencyKey": bson.M{"$regex": "^lt-"}})
	if err != nil {
		t.Fatal(err)
	}
	var stored []models.BinaryTrade
	if err := cur.All(ctx, &stored); err != nil {
		t.Fatal(err)
	}
	for _, tr := range stored {
		k := tr.UserID.Hex() + "|" + *tr.IdempotencyKey
		if prev, dup := keyTrades[k]; dup {
			t.Errorf("idempotency key %s produced two trades: %s and %s", k, prev, tr.ID.Hex())
		}
		keyTrades[k] = tr.ID.Hex()
	}
	replayed := 0
	for i, j := range jobs {
		r := &results[i]
		if len(r.ids) > 1 {
			t.Errorf("key %s: successful answers name %d different trades", j.req.IdempotencyKey, len(r.ids))
		}
		id := keyTrades[j.u.hex+"|"+j.req.IdempotencyKey]
		for got := range r.ids {
			if got != id {
				t.Errorf("key %s: answer names %s but MongoDB has %q", j.req.IdempotencyKey, got, id)
			}
		}
		for _, s := range r.statuses {
			if s == 200 {
				replayed++
			}
		}
		// Whichever payload got there first, one key creates one trade.
		created := 0
		for _, s := range append(append([]int{}, r.statuses...), r.mismatchS...) {
			if s == 201 {
				created++
			}
		}
		if created > 1 {
			t.Errorf("key %s: %d requests were answered 201 Created", j.req.IdempotencyKey, created)
		}
	}

	// Limits, from MongoDB (the record).
	book := openLiveBook(t, ctx, assetOID)
	gross, net := book.up+book.down, book.up-book.down
	if gross > grossLimit {
		t.Errorf("asset gross %d above the limit %d", gross, grossLimit)
	}
	if net > netLimit || -net > netLimit {
		t.Errorf("asset net %d beyond the limit %d", net, netLimit)
	}
	for u, v := range book.perUser {
		if v > cfg.MaxUserExposure {
			t.Errorf("user %s exposure %d above the limit %d", u, v, cfg.MaxUserExposure)
		}
	}

	// The Redis book equals MongoDB, with no reservation left over.
	st, err := exposure.GetBookStatus(ctx)
	if err != nil || st.Status != exposure.BookReady || st.PendingReservations != 0 {
		t.Errorf("book after the run: %+v %v", st, err)
	}
	snap, err := exposure.GetExposure(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.UpStake != book.up || snap.DownStake != book.down || snap.GrossExposure != gross || snap.NetExposure != net ||
		snap.OpenTrades != book.open || snap.ReservedStake != 0 {
		t.Errorf("Redis book %+v != MongoDB up %d down %d open %d", snap, book.up, book.down, book.open)
	}
	for i := range us {
		ux, err := exposure.GetUserExposure(ctx, us[i].hex)
		if err != nil {
			t.Fatal(err)
		}
		if ux.Total != book.perUser[us[i].hex] {
			t.Errorf("user %s: Redis exposure %d, MongoDB %d", us[i].hex, ux.Total, book.perUser[us[i].hex])
		}
		w := walletOf(t, ctx, us[i].id)
		if w.LockedBalance != book.perUser[us[i].hex] {
			t.Errorf("user %s: locked %d, open stakes %d", us[i].hex, w.LockedBalance, book.perUser[us[i].hex])
		}
		if w.Balance != us[i].balance {
			t.Errorf("user %s: balance %d, funded %d (stakes are locked, not taken, until settlement)", us[i].hex, w.Balance, us[i].balance)
		}
	}
	rep, err := exposure.Reconcile(ctx)
	if err != nil || rep.Repaired() || len(rep.Drift) > 0 {
		t.Errorf("reconcile after the run found something to repair: %+v %v", rep, err)
	}
	// Every open trade is on the expiry queue.
	for id := range book.trades {
		if _, err := redisx.R().ZScore(ctx, config.RedisKeys.ExpiryQueue, id).Result(); err != nil {
			t.Errorf("open trade %s missing from the expiry queue: %v", id, err)
		}
	}
	run, err := reconciliation.RunReconciliation(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.ReconciliationOK {
		t.Errorf("reconciliation %s: %v", run.Status, run.Alerts)
	}

	acceptedTrades := len(stored)
	n := requests.Load()
	result := map[string]any{
		"customers": users, "logicalTrades": len(jobs), "httpRequests": n, "concurrency": workers,
		"acceptedTrades": acceptedTrades, "replayedAnswers": replayed,
		"stormSeconds": stormElapsed.Seconds(), "totalSeconds": totalElapsed.Seconds(),
		"requestsPerSecond": float64(n) / totalElapsed.Seconds(), "acceptedPerSecond": float64(acceptedTrades) / stormElapsed.Seconds(),
		"latencyAll":      map[string]string{"p50": ms(all.pct(50)), "p95": ms(all.pct(95)), "p99": ms(all.pct(99)), "max": ms(all.pct(100))},
		"latencyAccepted": map[string]string{"p50": ms(accepted.pct(50)), "p95": ms(accepted.pct(95)), "p99": ms(accepted.pct(99))},
		"limits":          map[string]int64{"assetGross": grossLimit, "assetNet": netLimit, "user": cfg.MaxUserExposure},
		"finalBook":       map[string]int64{"up": book.up, "down": book.down, "gross": gross, "net": net, "openTrades": book.open},
		"responses":       codeList, "bookEvents": rebuilds, "reconciliation": string(run.Status),
		"gomaxprocs": os.Getenv("GOMAXPROCS"),
	}
	writeResult(t, "TestLoadTradeStormAndBookRebuild/"+os.Getenv("LOADTEST_PROFILE"), result)
	t.Logf("requests %d in %.1fs (%.0f/s), accepted %d (%.1f/s during the storm), replayed answers %d",
		n, totalElapsed.Seconds(), float64(n)/totalElapsed.Seconds(), acceptedTrades, float64(acceptedTrades)/stormElapsed.Seconds(), replayed)
	t.Logf("latency all p50 %s p95 %s p99 %s; accepted p50 %s p95 %s", ms(all.pct(50)), ms(all.pct(95)), ms(all.pct(99)), ms(accepted.pct(50)), ms(accepted.pct(95)))
	t.Logf("final book up %d down %d gross %d/%d net %d/±%d open %d", book.up, book.down, gross, grossLimit, net, netLimit, book.open)
	for _, c := range codeList {
		t.Logf("  %s", c)
	}
	for _, e := range rebuilds {
		t.Logf("  event: %s", e)
	}
}

// enableTx reconnects the throwaway database with transactions on, as the
// production server runs.
func enableTx(t *testing.T) {
	t.Helper()
	uri := os.Getenv("MONGO_URI")
	name := mongox.DB().Name()
	rest := strings.TrimPrefix(uri, "mongodb://")
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = mongox.Disconnect(ctx)
	if err := mongox.Connect(ctx, "mongodb://"+rest+"/"+name+query, true); err != nil {
		t.Fatal(err)
	}
	if mongox.DB().Name() != name || !mongox.TransactionsAvailable() {
		t.Fatalf("reconnect: %s transactions %v", mongox.DB().Name(), mongox.TransactionsAvailable())
	}
}

// feedTicks ingests a random walk for one provider symbol through the
// production ingest path until stopped.
func feedTicks(t *testing.T, ctx context.Context, symbol string, every time.Duration) func() {
	t.Helper()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		price := 104_250.0
		rng := rand.New(rand.NewPCG(1, 2))
		tk := time.NewTicker(every)
		defer tk.Stop()
		for {
			price *= 1 + (rng.Float64()-0.5)*2e-4
			now := time.Now().UnixMilli()
			if _, err := marketdata.IngestTick(ctx, providers.Tick{Symbol: symbol, Price: price, ProviderTimestamp: &now}); err != nil {
				t.Logf("tick: %v", err)
			}
			select {
			case <-stop:
				return
			case <-tk.C:
			}
		}
	}()
	// Wait for the first accepted tick.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		docs, _ := mongox.C(models.CollAssets).Find(ctx, bson.M{"providerSymbol": symbol})
		var as []models.Asset
		if docs != nil {
			_ = docs.All(ctx, &as)
		}
		if len(as) > 0 {
			if tick, _ := marketdata.GetLastTick(ctx, as[0].ID.Hex()); tick != nil {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return func() { close(stop); wg.Wait() }
}

// dropAndVerify drops the database and checks it no longer exists.
func dropAndVerify(t *testing.T, name string) {
	for attempt := 0; attempt < 5; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		err := mongox.DB().Client().Database(name).Drop(ctx)
		names, lerr := mongox.DB().Client().ListDatabaseNames(ctx, bson.M{"name": name})
		cancel()
		if err == nil && lerr == nil && len(names) == 0 {
			return
		}
		time.Sleep(2 * time.Second)
	}
	t.Errorf("database %s could not be dropped", name)
}

// cleanupRedis deletes the keys this test created in the shared Redis.
func cleanupRedis(ctx context.Context, db, ns, assetID string, us []ltUser) {
	marks := []string{db, ns, assetID}
	for _, u := range us {
		marks = append(marks, u.hex)
	}
	iter := redisx.R().Scan(ctx, 0, "*", 1000).Iterator()
	var del []string
	for iter.Next(ctx) {
		k := iter.Val()
		for _, m := range marks {
			if strings.Contains(k, m) {
				del = append(del, k)
				break
			}
		}
	}
	if err := iter.Err(); err != nil {
		logx.Error("loadtest: key scan failed", "err", err.Error())
	}
	for i := 0; i < len(del); i += 500 {
		if err := redisx.R().Del(ctx, del[i:min(i+500, len(del))]...).Err(); err != nil {
			logx.Error("loadtest: key delete failed", "err", err.Error())
		}
	}
}

func intEnv(key string, def int) int {
	var v int
	if _, err := fmt.Sscanf(os.Getenv(key), "%d", &v); err == nil && v > 0 {
		return v
	}
	return def
}
