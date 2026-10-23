//go:build loadtest

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"binarytrading/internal/config"
	"binarytrading/internal/models"
	"binarytrading/internal/platform/logx"
	"binarytrading/internal/platform/mongox"
	"binarytrading/internal/platform/redisx"
	"binarytrading/internal/service/exposure"
	"binarytrading/internal/service/reconciliation"
)

// TestFailoverMultiProcess (audit F-14, also F-05/F-06): three real server
// processes (ports 4101-4103) with workers enabled share one MongoDB database
// and one Redis database. Customers trade through all three; the process that
// holds the worker leaderships is killed (no graceful shutdown) while trades
// are open and expiring, and started again later. When every trade has
// finished, the trades, settlement rows, wallet ledger and exposure book must
// reconcile exactly and nothing may have been settled twice.
//
// Isolation: a database of its own (dropped afterwards) and a Redis database
// index of its own (LOADTEST_REDIS_DB, default 9; it must be empty at the
// start and is flushed afterwards), because the server's queue and leader
// keys are not namespaced.
func TestFailoverMultiProcess(t *testing.T) {
	requireLoadTest(t)
	loadConfig(t, nil)
	logx.Init("error", "test")
	ctx := context.Background()

	dbName := "binary_trading_failover_" + bson.NewObjectID().Hex()
	mongoURI := withDB(t, os.Getenv("MONGO_URI"), dbName)
	redisDB := intEnv("LOADTEST_REDIS_DB", 9)
	redisURL := strings.TrimRight(os.Getenv("REDIS_URL"), "/") + "/" + strconv.Itoa(redisDB)
	if err := mongox.Connect(ctx, mongoURI, true); err != nil {
		t.Fatal(err)
	}
	if err := redisx.Connect(ctx, redisURL); err != nil {
		t.Fatal(err)
	}
	if n, err := redisx.R().DBSize(ctx).Result(); err != nil || n != 0 {
		t.Fatalf("Redis database %d is not empty (%d keys, %v): pick another LOADTEST_REDIS_DB", redisDB, n, err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = redisx.R().FlushDB(c).Err()
		if err := mongox.DB().Drop(c); err != nil {
			t.Errorf("drop %s: %v", dbName, err)
		}
		_ = mongox.Disconnect(c)
		_ = redisx.Disconnect()
	})
	models.EnsureIndexes(ctx)

	const expiry = int64(30)
	setRisk(t, ctx, tradingRisk(1000*ltStake))
	asset := createAsset(t, ctx, expiry, 1_000_000*ltStake, 1_000_000*ltStake)
	assetOID, _ := mongox.ParseID(asset.ID)
	us := createUsers(t, ctx, 20, func(int) int64 { return 200 * ltStake })

	// Build the server and start the cluster.
	// LOADTEST_LOGDIR keeps the binary and the three server logs.
	dir := os.Getenv("LOADTEST_LOGDIR")
	if dir == "" {
		dir = t.TempDir()
	}
	bin := filepath.Join(dir, "server.exe")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "GOFLAGS=-p=1")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cl := &cluster{t: t, bin: bin, dir: dir, mongoURI: mongoURI, redisURL: redisURL, procs: map[int]*exec.Cmd{}}
	t.Cleanup(cl.stopAll)
	cl.start(4101)
	cl.waitReady(4101, 120*time.Second)
	waitFor(t, 30*time.Second, "4101 to lead the market feed", func() bool { return cl.leader(4101) })
	cl.start(4102)
	cl.start(4103)
	cl.waitReady(4102, 60*time.Second)
	cl.waitReady(4103, 60*time.Second)
	waitFor(t, 30*time.Second, "a fresh tick", func() bool { return cl.feedHealthy(4102) })

	client := &http.Client{Timeout: 20 * time.Second}
	var (
		placed, failedReq, crossRetries atomic.Int64
		statuses                        sync.Map
		mu                              sync.Mutex
		killAt, restartAt, takeoverAt   time.Time
		newLeader                       int
	)
	count := func(k string) {
		v, _ := statuses.LoadOrStore(k, new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
	}
	ports := []int{4101, 4102, 4103}
	alive := func(p int) bool { mu.Lock(); defer mu.Unlock(); return cl.procs[p] != nil }

	// Traders: each places a trade every ~1.5 s on a random live instance;
	// one in four is retried on another instance with the same key.
	stop := make(chan struct{})
	var tw sync.WaitGroup
	for i := range us {
		tw.Add(1)
		go func(u *ltUser, seed uint64) {
			defer tw.Done()
			rng := rand.New(rand.NewPCG(seed, 99))
			for {
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(1000+rng.IntN(1000)) * time.Millisecond):
				}
				dir := "UP"
				if rng.IntN(2) == 0 {
					dir = "DOWN"
				}
				req := tradeReq{AssetID: asset.ID, Direction: dir, Amount: ltStake, ExpirySeconds: expiry, AccountType: "LIVE", IdempotencyKey: "fo-" + uuid.NewString()}
				p := ports[rng.IntN(3)]
				if !alive(p) {
					p = 4102
				}
				r := postTrade(ctx, client, fmt.Sprintf("http://127.0.0.1:%d", p), u.token, req)
				if r.Err != nil {
					failedReq.Add(1)
					count("transport-error")
					continue
				}
				count(fmt.Sprintf("%d %s", r.Status, r.Code))
				if r.Status == 201 {
					placed.Add(1)
				}
				if rng.IntN(4) == 0 {
					q := ports[(indexOf(ports, p)+1+rng.IntN(2))%3]
					if alive(q) {
						r2 := postTrade(ctx, client, fmt.Sprintf("http://127.0.0.1:%d", q), u.token, req)
						crossRetries.Add(1)
						if r2.Err != nil {
							// The instance was killed under the request.
							count("retry transport-error")
						} else if r.Status == 201 && (r2.Status != 200 || r2.TradeID != r.TradeID) {
							t.Errorf("retry of %s on %d answered %d %s (trade %s, want replay of %s)", req.IdempotencyKey, q, r2.Status, r2.Code, r2.TradeID, r.TradeID)
						}
					}
				}
			}
		}(&us[i], uint64(i+1))
	}

	// Kill the leader at 15 s, restart it at 50 s, stop trading at 65 s.
	time.Sleep(15 * time.Second)
	mu.Lock()
	killAt = time.Now()
	cl.kill(4101)
	mu.Unlock()
	go func() {
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			for _, p := range []int{4102, 4103} {
				if cl.leader(p) {
					mu.Lock()
					if takeoverAt.IsZero() {
						takeoverAt, newLeader = time.Now(), p
					}
					mu.Unlock()
					return
				}
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
	time.Sleep(35 * time.Second)
	mu.Lock()
	restartAt = time.Now()
	cl.start(4101)
	mu.Unlock()
	cl.waitReady(4101, 60*time.Second)
	time.Sleep(15 * time.Second)
	close(stop)
	tw.Wait()
	stoppedAt := time.Now()

	// Wait until every trade has finished.
	waitFor(t, 4*time.Minute, "every trade to finish", func() bool {
		n, err := mongox.C(models.CollBinaryTrades).CountDocuments(ctx, bson.M{"status": bson.M{"$in": bson.A{models.TradeStatusOpen, models.TradeStatusSettling}}})
		return err == nil && n == 0
	})
	finishedAt := time.Now()
	// Let the last settlements' fire-and-forget work land.
	time.Sleep(2 * time.Second)

	// ── assertions ─────────────────────────────────────────────────────
	cur, err := mongox.C(models.CollBinaryTrades).Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	var trades []models.BinaryTrade
	if err := cur.All(ctx, &trades); err != nil {
		t.Fatal(err)
	}
	// A request whose connection died with the killed process may have
	// written its trade without the 201 reaching the client (the kill can land
	// between the insert and the response); such trades are bounded by the
	// requests that lost their connection, and no trade may be missing.
	if n := int64(len(trades)); n < placed.Load() || n > placed.Load()+failedReq.Load() {
		t.Errorf("MongoDB has %d trades, %d were answered 201 and %d requests lost their connection", n, placed.Load(), failedReq.Load())
	}
	outcomes := map[string]int{}
	voided := 0
	var latencies []int64
	var gapLatencies []int64
	leaderless := func(exp int64) bool {
		e := time.UnixMilli(exp)
		return !e.Before(killAt) && (takeoverAt.IsZero() || e.Before(takeoverAt.Add(5*time.Second)))
	}
	for _, tr := range trades {
		outcomes[string(tr.Status)]++
		if models.TradeVoided(&tr) {
			voided++
		}
		if tr.SettledAt != nil {
			l := tr.SettledAt.UnixMilli() - tr.ExpiryTimestamp
			latencies = append(latencies, l)
			if leaderless(tr.ExpiryTimestamp) {
				gapLatencies = append(gapLatencies, l)
			}
		}
	}

	// One settlement row per finished trade and one settling ledger movement
	// per trade (the unique indexes make a second impossible; the counts
	// prove none is missing either).
	settleRows := countBy(t, ctx, models.CollSettlements, "tradeId")
	ledgerSettle := countLedger(t, ctx, "trade:settle:")
	ledgerLock := countLedger(t, ctx, "trade:lock:")
	for _, tr := range trades {
		id := tr.ID.Hex()
		if tr.Status == models.TradeStatusCancelled && !models.TradeVoided(&tr) {
			continue
		}
		if settleRows[id] != 1 {
			t.Errorf("trade %s (%s): %d settlement rows", id, tr.Status, settleRows[id])
		}
		if ledgerSettle[id] != 1 || ledgerLock[id] != 1 {
			t.Errorf("trade %s (%s): %d settle and %d lock ledger rows", id, tr.Status, ledgerSettle[id], ledgerLock[id])
		}
	}
	for id, n := range settleRows {
		if n > 1 {
			t.Errorf("trade %s settled %d times", id, n)
		}
	}

	// Wallets: nothing locked, balance = funding + the ledger.
	for i := range us {
		w := walletOf(t, ctx, us[i].id)
		if w.LockedBalance != 0 {
			t.Errorf("user %s: %d still locked with no open trade", us[i].hex, w.LockedBalance)
		}
	}
	run, err := reconciliation.RunReconciliation(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != models.ReconciliationOK {
		t.Errorf("reconciliation %s: %v", run.Status, run.Alerts)
	}

	// Exposure. The book is a cache of MongoDB that the reconciler corrects:
	// releases after settlement match a record by (asset, user, direction,
	// stake, profit), not by trade id, so with identical trades a settlement
	// can close a sibling's record, and a boot reconcile on a restarted
	// instance re-opens records for live trades. The safety property is that
	// the book never shows LESS exposure than MongoDB (that could let a trade
	// through a limit), and that the running cluster heals it to exact.
	book := openLiveBook(t, ctx, assetOID)
	snap, err := exposure.GetExposure(ctx, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if snap.GrossExposure < book.up+book.down || snap.OpenTrades < book.open {
		t.Errorf("the book understates exposure after the run: redis %+v, mongo up %d down %d", snap, book.up, book.down)
	}
	bookDrift := map[string]any{"grossAtDrain": snap.GrossExposure, "openRecordsAtDrain": snap.OpenTrades}
	if snap.GrossExposure != 0 || snap.OpenTrades != 0 {
		bookDrift["held"] = diagnoseBook(t, ctx, trades, us, killAt, takeoverAt)
	}
	healStart := time.Now()
	healed := false
	for time.Since(healStart) < 90*time.Second {
		s2, err := exposure.GetExposure(ctx, asset.ID)
		if err == nil && s2.GrossExposure == 0 && s2.OpenTrades == 0 && s2.ReservedStake == 0 {
			healed = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	bookDrift["healedByCluster"] = healed
	bookDrift["healSeconds"] = time.Since(healStart).Round(100 * time.Millisecond).Seconds()
	if !healed {
		t.Errorf("the cluster's reconciler did not bring the book back to MongoDB within 90 s")
	}
	rep, err := exposure.Reconcile(ctx)
	st, _ := exposure.GetBookStatus(ctx)
	if err != nil || rep.Repaired() || st.Status != exposure.BookReady || st.PendingReservations != 0 {
		t.Errorf("after healing: reconcile %+v %v, status %+v", rep, err, st)
	}

	// The feed gap the failover caused, from the tick history.
	gap := maxTickGap(t, ctx, asset.ID, killAt.Add(-5*time.Second), killAt.Add(90*time.Second))

	codes := []string{}
	statuses.Range(func(k, v any) bool {
		codes = append(codes, fmt.Sprintf("%s: %d", k, v.(*atomic.Int64).Load()))
		return true
	})
	sort.Strings(codes)
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	sort.Slice(gapLatencies, func(i, j int) bool { return gapLatencies[i] < gapLatencies[j] })
	takeover := "none"
	if !takeoverAt.IsZero() {
		takeover = fmt.Sprintf("%v (by %d)", takeoverAt.Sub(killAt).Round(100*time.Millisecond), newLeader)
	}
	res := map[string]any{
		"processes": 3, "tradesAccepted": placed.Load(), "crossInstanceRetries": crossRetries.Load(), "requestErrors": failedReq.Load(),
		"outcomes": outcomes, "voided": voided, "responses": codes,
		"marketFeedTakeoverAfterKill": takeover, "maxTickGapMs": gap.Milliseconds(),
		"settleLatencyMs":           map[string]int64{"p50": pctInt(latencies, 50), "p95": pctInt(latencies, 95), "max": pctInt(latencies, 100)},
		"settleLatencyLeaderlessMs": map[string]int64{"count": int64(len(gapLatencies)), "p50": pctInt(gapLatencies, 50), "max": pctInt(gapLatencies, 100)},
		"killToRestart":             restartAt.Sub(killAt).Round(time.Second).String(), "drainAfterStop": finishedAt.Sub(stoppedAt).Round(time.Second).String(),
		"reconciliation": string(run.Status), "bookDrift": bookDrift,
	}
	writeResult(t, "TestFailoverMultiProcess", res)
	raw, _ := json.MarshalIndent(res, "", "  ")
	t.Logf("%s", raw)
}

type cluster struct {
	t        *testing.T
	bin, dir string
	mongoURI string
	redisURL string
	mu       sync.Mutex
	procs    map[int]*exec.Cmd
}

func (c *cluster) start(port int) {
	c.t.Helper()
	wd := filepath.Join(c.dir, "p"+strconv.Itoa(port))
	_ = os.MkdirAll(wd, 0o755)
	logf, err := os.OpenFile(filepath.Join(c.dir, fmt.Sprintf("server-%d.log", port)), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		c.t.Fatal(err)
	}
	cmd := exec.Command(c.bin)
	cmd.Dir = wd
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.Env = []string{
		"SYSTEMROOT=" + os.Getenv("SYSTEMROOT"), "PATH=" + os.Getenv("PATH"), "TEMP=" + os.Getenv("TEMP"), "TMP=" + os.Getenv("TMP"),
		"NODE_ENV=development", "LOG_LEVEL=warn", "HOST=127.0.0.1", "PORT=" + strconv.Itoa(port),
		"MONGO_URI=" + c.mongoURI, "REDIS_URL=" + c.redisURL, "ENABLE_MONGO_TRANSACTIONS=true",
		"JWT_SECRET=" + ltSecret, "JWT_REFRESH_SECRET=" + ltRefSec, "WORKERS_ENABLED=true",
		"MARKET_DATA_PROVIDER=simulated", "ALERT_EMAIL_ENABLED=false", "RECONCILIATION_INTERVAL_MS=60000",
		"GOMAXPROCS=2", "GOMEMLIMIT=256MiB",
	}
	if err := cmd.Start(); err != nil {
		c.t.Fatal(err)
	}
	c.mu.Lock()
	c.procs[port] = cmd
	c.mu.Unlock()
	go func() { _ = cmd.Wait(); _ = logf.Close() }()
}

// kill ends the process at once (TerminateProcess / SIGKILL): no graceful
// shutdown, no lock released.
func (c *cluster) kill(port int) {
	c.mu.Lock()
	cmd := c.procs[port]
	delete(c.procs, port)
	c.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func (c *cluster) stopAll() {
	c.mu.Lock()
	ports := []int{}
	for p := range c.procs {
		ports = append(ports, p)
	}
	c.mu.Unlock()
	for _, p := range ports {
		c.kill(p)
	}
	time.Sleep(500 * time.Millisecond)
}

func (c *cluster) ready(port int) map[string]any {
	res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/ready", port))
	if err != nil {
		return nil
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	var env struct {
		Data map[string]any `json:"data"`
	}
	_ = json.Unmarshal(raw, &env)
	return env.Data
}

func (c *cluster) waitReady(port int, d time.Duration) {
	c.t.Helper()
	waitFor(c.t, d, fmt.Sprintf("%d ready", port), func() bool {
		r := c.ready(port)
		return r != nil && r["ready"] == true
	})
}

func (c *cluster) leader(port int) bool {
	r := c.ready(port)
	if r == nil {
		return false
	}
	checks, _ := r["checks"].(map[string]any)
	w, _ := checks["workers"].(map[string]any)
	return w["marketFeedLeader"] == true
}

func (c *cluster) feedHealthy(port int) bool {
	r := c.ready(port)
	if r == nil {
		return false
	}
	checks, _ := r["checks"].(map[string]any)
	f, _ := checks["marketFeed"].(map[string]any)
	n, _ := f["healthyAssets"].(float64)
	return n >= 1
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", d, what)
}

func withDB(t *testing.T, uri, name string) string {
	t.Helper()
	if _, err := connstring.ParseAndValidate(uri); err != nil {
		t.Fatal(err)
	}
	rest := strings.TrimPrefix(uri, "mongodb://")
	query := ""
	if i := strings.Index(rest, "?"); i >= 0 {
		rest, query = rest[:i], rest[i:]
	}
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	return "mongodb://" + rest + "/" + name + query
}

func countBy(t *testing.T, ctx context.Context, coll, field string) map[string]int {
	t.Helper()
	cur, err := mongox.C(coll).Aggregate(ctx, bson.A{bson.M{"$group": bson.M{"_id": "$" + field, "n": bson.M{"$sum": 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []struct {
		ID bson.ObjectID `bson:"_id"`
		N  int           `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range rows {
		out[r.ID.Hex()] = r.N
	}
	return out
}

// countLedger counts wallet movements per trade id for one key prefix.
func countLedger(t *testing.T, ctx context.Context, prefix string) map[string]int {
	t.Helper()
	cur, err := mongox.C(models.CollWalletTransactions).Find(ctx, bson.M{"idempotencyKey": bson.M{"$regex": "^" + prefix}})
	if err != nil {
		t.Fatal(err)
	}
	var rows []models.WalletTransaction
	if err := cur.All(ctx, &rows); err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range rows {
		out[strings.TrimPrefix(*r.IdempotencyKey, prefix)]++
	}
	return out
}

// maxTickGap is the longest time between two accepted ticks in [from, to].
func maxTickGap(t *testing.T, ctx context.Context, assetID string, from, to time.Time) time.Duration {
	t.Helper()
	scores, err := redisx.R().ZRangeByScoreWithScores(ctx, config.RedisKeys.TickHistory(assetID), &redis.ZRangeBy{
		Min: strconv.FormatInt(from.UnixMilli(), 10), Max: strconv.FormatInt(to.UnixMilli(), 10)}).Result()
	if err != nil || len(scores) < 2 {
		return 0
	}
	var gap float64
	for i := 1; i < len(scores); i++ {
		gap = max(gap, scores[i].Score-scores[i-1].Score)
	}
	return time.Duration(gap) * time.Millisecond
}

func pctInt(s []int64, p float64) int64 {
	if len(s) == 0 {
		return 0
	}
	i := int(float64(len(s))*p/100+0.999999) - 1
	return s[max(0, min(i, len(s)-1))]
}

func indexOf(s []int, v int) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return 0
}

// diagnoseBook lists the trades the Redis book still holds as open, with
// their state in MongoDB.
func diagnoseBook(t *testing.T, ctx context.Context, trades []models.BinaryTrade, us []ltUser, killAt, takeoverAt time.Time) map[string]any {
	byID := map[string]models.BinaryTrade{}
	for _, tr := range trades {
		byID[tr.ID.Hex()] = tr
	}
	ns := exposure.BookNamespace()
	held := []string{}
	kinds := map[string]int{}
	for _, u := range us {
		recs, _ := redisx.R().HGetAll(ctx, ns+":ot:"+u.hex).Result()
		for id := range recs {
			tr, ok := byID[id]
			kind := "unknown trade"
			if ok {
				done, _ := redisx.R().Exists(ctx, ns+":done:"+id).Result()
				exp := time.UnixMilli(tr.ExpiryTimestamp)
				phase := "before kill"
				switch {
				case !exp.Before(killAt) && (takeoverAt.IsZero() || exp.Before(takeoverAt)):
					phase = "expired leaderless"
				case !exp.Before(killAt):
					phase = "expired after takeover"
				}
				settled := ""
				if tr.SettledAt != nil {
					settled = tr.SettledAt.Sub(exp).Round(time.Second).String()
				}
				kind = fmt.Sprintf("%s void=%v %s", tr.Status, models.TradeVoided(&tr), phase)
				held = append(held, fmt.Sprintf("%s %s created %s expiry %s settled +%s doneMark=%d",
					id, kind, tr.CreatedAt.UTC().Format("15:04:05.000"), exp.UTC().Format("15:04:05"), settled, done))
			}
			kinds[kind]++
		}
	}
	sort.Strings(held)
	for i, h := range held {
		if i == 10 {
			t.Logf("held: ... %d more", len(held)-10)
			break
		}
		t.Logf("held: %s", h)
	}
	return map[string]any{"heldRecords": len(held), "byKind": kinds, "killAt": killAt.UTC().Format("15:04:05.000"),
		"takeoverAt": takeoverAt.UTC().Format("15:04:05.000")}
}
